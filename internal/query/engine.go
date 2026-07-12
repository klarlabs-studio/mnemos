package query

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/embedding"
	"go.klarlabs.de/mnemos/internal/llm"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/trust"
)

type eventLister interface {
	ports.EventRepository
	ListAll(ctx context.Context) ([]domain.Event, error)
	ListByRunID(ctx context.Context, runID string) ([]domain.Event, error)
}

// Engine answers natural-language questions by ranking events, resolving claims,
// and detecting contradictions.
type Engine struct {
	events          eventLister
	claims          ports.ClaimRepository
	relationships   ports.RelationshipRepository
	decisions       ports.DecisionRepository
	incidents       ports.IncidentRepository
	embeddings      ports.EmbeddingRepository
	embedClient     embedding.Client
	llmClient       llm.Client
	eventTextSearch ports.TextSearcher
	claimTextSearch ports.TextSearcher
	// eventVectorSearch is the optional native-vector recall path. Set from
	// embeddings when the backing store implements EventVectorSearcher (e.g.
	// Postgres with pgvector). When present, AnswerWithOptions fetches the
	// top-K candidate events by vector instead of loading the whole corpus
	// with ListAll and cosining in Go.
	eventVectorSearch ports.EventVectorSearcher
}

// eventVectorTopK is how many candidate events the native vector path pulls
// (ordered by cosine distance) before answerEventLimit narrows to the final
// answer set. Generous enough that at small corpora it covers everything
// (matching the old whole-corpus behaviour), bounded enough that at scale it
// stays cheap.
const eventVectorTopK = 64

// answerEventLimit is how many top-ranked events feed claim resolution for a
// single answer. Matches the long-standing rankEventsWithFallback limit.
const answerEventLimit = 5

// rrfK is the Reciprocal Rank Fusion damping constant (Cormack et al. 2009).
// A hit's fused score is Σ 1/(rrfK + rank) over the rankers it appears in, so a
// larger k flattens the contribution of top ranks and lets many-list agreement
// outweigh any single ranker's #1. 60 is the field-standard default.
const rrfK = 60

// NewEngine returns an Engine wired to the given event, claim, and relationship stores.
func NewEngine(events eventLister, claims ports.ClaimRepository, relationships ports.RelationshipRepository) Engine {
	return Engine{events: events, claims: claims, relationships: relationships}
}

// WithDecisions wires a DecisionRepository so the engine can serve audit-trail
// queries via AuditTrail.
func (e Engine) WithDecisions(decisions ports.DecisionRepository) Engine {
	e.decisions = decisions
	return e
}

// WithIncidents wires an IncidentRepository so the engine can serve
// WhyWereWeWrong analysis.
func (e Engine) WithIncidents(incidents ports.IncidentRepository) Engine {
	e.incidents = incidents
	return e
}

// WithEmbeddings configures semantic search support on the engine.
// When both an embedding repository and client are set, queries use cosine
// similarity against stored event embeddings instead of token overlap.
func (e Engine) WithEmbeddings(repo ports.EmbeddingRepository, client embedding.Client) Engine {
	e.embeddings = repo
	e.embedClient = client
	// Adopt the native vector path when the store provides one. The type
	// assertion is the only wiring needed — New() already hands us the
	// concrete repository, so no separate option or constructor arg exists.
	if vs, ok := repo.(ports.EventVectorSearcher); ok {
		e.eventVectorSearch = vs
	}
	return e
}

// WithLLM configures LLM-grounded answer generation. When set, the engine
// uses the LLM to synthesize answers from retrieved claims instead of using
// a fixed template. Falls back to template answers on LLM failure.
func (e Engine) WithLLM(client llm.Client) Engine {
	e.llmClient = client
	return e
}

// WithTextSearch wires the v0.10 hybrid retrieval path: a BM25
// keyword index over events and another over claims. When both are
// set, the engine combines BM25 with cosine similarity (when also
// available) into a hybrid relevance score; when only BM25 is set,
// it replaces the legacy token-overlap fallback. Either argument may
// be nil to opt out of that side.
func (e Engine) WithTextSearch(events, claims ports.TextSearcher) Engine {
	e.eventTextSearch = events
	e.claimTextSearch = claims
	return e
}

// AnswerOptions tunes a query without requiring callers that just want the
// default behavior to learn a new constructor signature. Hops controls
// graph-expansion of the directly-retrieved claim set: 0 means no expansion,
// N means follow up to N supports/contradicts edges from the seed claims.
// MinTrust filters out claims whose computed trust_score (see internal/trust)
// is strictly below the threshold; 0 disables the filter.
//
// AsOf enables point-in-time queries against the temporal-validity layer
// (see domain.Claim.IsValidAt). When non-zero, only claims that were in
// force at that instant are returned; when zero, the engine substitutes
// time.Now() so the default is "what is currently true". IncludeHistory
// disables temporal filtering entirely — callers see superseded claims
// alongside current ones, useful for `--history` / audit views.
type AnswerOptions struct {
	Hops     int
	MinTrust float64
	AsOf     time.Time
	// RecordedAsOf is the ingestion-time axis. When non-zero, the
	// engine drops claims with CreatedAt > RecordedAsOf so the
	// response reproduces what the store knew as of that timestamp.
	// Zero value disables the filter (the common case).
	RecordedAsOf   time.Time
	IncludeHistory bool
	// AllowedClaimIDs, when non-nil, restricts the answer set to
	// claims whose id is in the map. Used by `query --entity` to
	// constrain results to a single entity's claims without
	// rewriting the retrieval pipeline. nil disables the filter
	// (the common case).
	AllowedClaimIDs map[string]struct{}
	// HopKinds, when non-empty, restricts hop expansion to relationship
	// edges of these types. Empty means "follow every kind", preserving
	// pre-causal behaviour. Used by `query --kind causes,validates ...`
	// to walk a single semantic family of edges (e.g. only the causal
	// graph, not contradictions).
	HopKinds []domain.RelationshipType
	// Scope, when non-empty, narrows the answer to claims whose
	// per-claim Scope matches the supplied filter. Empty fields in
	// the filter act as wildcards (Scope.Matches semantics): a
	// filter of {Service:"payments"} matches any claim with
	// Service="payments" regardless of Env/Team.
	Scope domain.Scope
	// Consumer controls contradiction handling. ConsumerAgent triggers
	// automatic trust-based resolution (the winning claim is kept; the
	// loser is demoted from the Claims slice). ConsumerUser surfaces
	// contradictions with a human-readable explanation. The zero value
	// is treated as ConsumerUser for backward compatibility.
	Consumer domain.Consumer
	// Visibility controls which claims the query sees.
	//   VisibilityPersonal – only personal claims (the caller's own notes).
	//   VisibilityTeam    – personal + team claims (default; zero value).
	//   VisibilityOrg     – all claims: personal, team, and org-wide.
	// The zero value is treated as VisibilityTeam for backward compatibility.
	Visibility domain.Visibility
	// Lifecycle, when non-empty, restricts the answer to claims whose
	// human-promotion state matches (e.g. domain.ClaimLifecyclePromoted to
	// recall only durable, human-endorsed knowledge). The zero value
	// disables the filter, so ordinary recall is unchanged — claims that
	// were never routed through a candidate→promoted review still appear.
	Lifecycle domain.ClaimLifecycle
	// Prime enables spreading-activation priming at retrieval (ADR 0013 §2,
	// Collins & Loftus). When true, after the initial ranked+expanded claim
	// set is assembled, activation is seeded on the top-ranked beliefs and
	// spread over relationship edges (decaying per hop, weighted by edge
	// type). The accumulated activation is blended into each claim's ranking
	// score as a small, bounded associative boost, so a belief strongly
	// associated with a direct hit rises in the results. Off by default so
	// ordinary recall is byte-for-byte unchanged; toggled by `query --prime`
	// / MNEMOS_SPREADING_ACTIVATION. The boost is bounded (see
	// spreadWeightActivation / spreadActivationCap) so a weakly-ranked belief
	// can never outrank a strong direct match on association alone.
	Prime bool
}

// Answer searches all stored events for the best answer to the given question.
func (e Engine) Answer(question string) (domain.Answer, error) {
	return e.AnswerWithOptions(question, AnswerOptions{})
}

// AnswerWithOptions is the configurable form of Answer. The plain Answer
// method delegates here with a zero-value AnswerOptions so existing callers
// see no behavior change.
//
// It runs a corrective-retrieval gate (R3, CRAG-style): the first pass answers
// from the best available candidate set; if that answer grades as insufficient
// (no claims, or confidence below recallSufficiencyFloor), it makes ONE bounded
// corrective pass — widening the narrow pgvector top-K to the whole corpus and
// relaxing the soft MinTrust quality gate — and keeps whichever answer is
// stronger. The retry is structurally bounded to a single extra pass (so it
// respects the caller's tool-call budget), only fires on a weak first pass, and
// never returns an answer worse than the one it started with. Semantic filters
// (Scope, Lifecycle, Visibility, AsOf) are NEVER relaxed — loosening them would
// return results the caller explicitly excluded.
func (e Engine) AnswerWithOptions(question string, opts AnswerOptions) (domain.Answer, error) {
	ctx := context.Background()
	ans, usedFastPath, err := e.answerOnce(ctx, question, opts)
	if err != nil {
		return domain.Answer{}, err
	}
	if !recallInsufficient(ans) {
		return ans, nil
	}
	// Corrective pass. It can help two ways: widen the corpus (only meaningful
	// when the first pass took the narrow fast-path) or relax MinTrust (only when
	// it was set). If neither applies, the retry can't recover anything, so skip.
	relaxed, relaxedFilters := relaxRecall(opts)
	if !usedFastPath && !relaxedFilters {
		return ans, nil
	}
	corrected, cerr := e.answerCorpusWide(ctx, question, relaxed)
	if cerr == nil && strongerAnswer(corrected, ans) {
		return corrected, nil
	}
	return ans, nil
}

// answerOnce runs a single recall pass and reports whether it took the native
// vector fast-path (pulling only the top-K candidate events by cosine distance)
// or fell through to the whole-corpus ListAll path. The fast-path is used
// whenever it can serve the query; it falls through on any miss (no searcher, no
// embedder, extension absent, or zero vector hits) so behaviour is identical to
// the pre-gate flow when the corrective pass doesn't fire.
func (e Engine) answerOnce(ctx context.Context, question string, opts AnswerOptions) (domain.Answer, bool, error) {
	if candidates, ok := e.eventsByHybrid(ctx, question); ok {
		ans, err := e.answerWithEvents(ctx, question, candidates, opts, true)
		return ans, true, err
	}
	allEvents, err := e.events.ListAll(ctx)
	if err != nil {
		return domain.Answer{}, false, fmt.Errorf("load events for query: %w", err)
	}
	ans, err := e.answerWithEvents(ctx, question, allEvents, opts, false)
	return ans, false, err
}

// answerCorpusWide is the corrective pass: it forces the whole-corpus path
// (bypassing the narrow vector top-K) so events the dense/sparse top-K missed
// can still surface. Used only by the corrective-retrieval gate.
func (e Engine) answerCorpusWide(ctx context.Context, question string, opts AnswerOptions) (domain.Answer, error) {
	allEvents, err := e.events.ListAll(ctx)
	if err != nil {
		return domain.Answer{}, fmt.Errorf("load events for corrective query: %w", err)
	}
	return e.answerWithEvents(ctx, question, allEvents, opts, false)
}

// recallSufficiencyFloor is the Answer.Confidence below which the first recall
// pass is graded "insufficient" and the corrective gate fires. computeConfidence
// tops out around 0.7, so a genuinely grounded answer clears this comfortably
// while empty or weakly-supported recall falls under it.
const recallSufficiencyFloor = 0.35

// recallInsufficient grades a recall pass: an answer with no claims, or one whose
// aggregate confidence is below the floor, is a candidate for corrective retrieval.
func recallInsufficient(a domain.Answer) bool {
	return len(a.Claims) == 0 || a.Confidence < recallSufficiencyFloor
}

// relaxRecall loosens only the SOFT quality gate (MinTrust) for a corrective
// pass, reporting whether anything changed. Semantic filters (Scope, Lifecycle,
// Visibility, AsOf/RecordedAsOf) are deliberately left intact: relaxing them
// would surface claims the caller explicitly asked to exclude, changing the
// meaning of the query rather than just widening its reach.
func relaxRecall(opts AnswerOptions) (AnswerOptions, bool) {
	if opts.MinTrust > 0 {
		opts.MinTrust = 0
		return opts, true
	}
	return opts, false
}

// strongerAnswer reports whether the corrective answer should replace the first
// one. It never trades a non-empty answer for an empty one; otherwise it prefers
// more claims, breaking ties by higher confidence. This makes the gate strictly
// non-regressive — the corrective pass can only improve or be discarded.
func strongerAnswer(corrected, original domain.Answer) bool {
	if len(corrected.Claims) == 0 {
		return false
	}
	if len(original.Claims) == 0 {
		return true
	}
	if len(corrected.Claims) != len(original.Claims) {
		return len(corrected.Claims) > len(original.Claims)
	}
	return corrected.Confidence > original.Confidence
}

// eventsByHybrid serves the native fast-path recall: embed the question, ask
// the pgvector searcher for the top-K nearest event ids (the dense leg), and —
// when a text searcher is wired — fuse in a full-text ranking of the same
// question (the sparse leg) by Reciprocal Rank Fusion before loading events.
// The sparse leg is what makes this HYBRID: exact tokens the embedding blurs
// (SHAs, service names, error codes, flag names) are recalled even when they
// fall outside the dense top-K, and RRF consumes only ranks so the two
// incomparable score scales (cosine distance vs ts_rank_cd) fuse without any
// normalisation. With no text searcher wired it degrades to the pure dense
// order — identical to the old vector-only fast-path.
//
// Returns ok=false — signalling the caller to fall back to ListAll — whenever
// the dense path is unavailable or yields nothing: no searcher wired, no
// embedder, an embed/search error, ErrVectorSearchUnavailable from a backend
// without pgvector, or an empty candidate set. (A dense miss falls back rather
// than serving sparse-only, so backends without pgvector still take the
// whole-corpus hybrid path in rankEventsWithFallback, unchanged.)
func (e Engine) eventsByHybrid(ctx context.Context, question string) ([]domain.Event, bool) {
	if e.eventVectorSearch == nil || e.embedClient == nil {
		return nil, false
	}
	qVectors, err := e.embedClient.Embed(ctx, []string{question})
	if err != nil || len(qVectors) == 0 {
		return nil, false
	}
	hits, err := e.eventVectorSearch.SearchEventsByVector(ctx, qVectors[0], embedding.ModelIDOf(e.embedClient), eventVectorTopK, 0)
	if err != nil || len(hits) == 0 {
		return nil, false
	}
	denseIDs := make([]string, 0, len(hits))
	for _, h := range hits {
		denseIDs = append(denseIDs, h.EventID)
	}

	// Sparse (lexical) leg — best-effort. An FTS error, a backend without a text
	// searcher, or an empty match all leave the dense order intact; only a
	// non-empty full-text ranking triggers fusion.
	orderedIDs := denseIDs
	if e.eventTextSearch != nil {
		if ftsHits, ferr := e.eventTextSearch.SearchByText(ctx, question, eventVectorTopK); ferr == nil && len(ftsHits) > 0 {
			sparseIDs := make([]string, 0, len(ftsHits))
			for _, h := range ftsHits {
				sparseIDs = append(sparseIDs, h.ID)
			}
			orderedIDs = fuseRRF([][]string{denseIDs, sparseIDs})
		}
	}

	events, err := e.events.ListByIDs(ctx, orderedIDs)
	if err != nil || len(events) == 0 {
		return nil, false
	}
	// ListByIDs makes no ordering promise; re-emit the events in the fused rank
	// order so the caller can treat them as already-ranked and skip a second,
	// whole-corpus cosine pass (the very cost this path exists to avoid).
	byID := make(map[string]domain.Event, len(events))
	for _, ev := range events {
		byID[ev.ID] = ev
	}
	ranked := make([]domain.Event, 0, len(orderedIDs))
	for _, id := range orderedIDs {
		if ev, ok := byID[id]; ok {
			ranked = append(ranked, ev)
		}
	}
	return ranked, true
}

// fuseRRF merges several ranked id lists into one by Reciprocal Rank Fusion:
// each id accumulates Σ 1/(rrfK + rank) across every list it appears in (rank
// 1-based), and the ids are returned in descending fused score. Because RRF
// reads only ranks, never scores, it fuses rankers whose score scales are
// incomparable (cosine similarity vs ts_rank_cd) without any normalisation, and
// an id that ranks well in BOTH legs outscores one that tops a single leg. Ties
// resolve by first appearance for deterministic output.
func fuseRRF(rankings [][]string) []string {
	score := make(map[string]float64)
	order := make([]string, 0)
	for _, list := range rankings {
		for rank, id := range list {
			if _, seen := score[id]; !seen {
				order = append(order, id)
			}
			score[id] += 1.0 / float64(rrfK+rank+1)
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		return score[order[i]] > score[order[j]]
	})
	return order
}

// AnswerForRun searches events belonging to the specified run for the best answer.
func (e Engine) AnswerForRun(question, runID string) (domain.Answer, error) {
	return e.AnswerForRunWithOptions(question, runID, AnswerOptions{})
}

// AnswerForRunWithOptions is the configurable form of AnswerForRun.
func (e Engine) AnswerForRunWithOptions(question, runID string, opts AnswerOptions) (domain.Answer, error) {
	ctx := context.Background()
	if strings.TrimSpace(runID) == "" {
		return domain.Answer{}, fmt.Errorf("run id is required")
	}
	events, err := e.events.ListByRunID(ctx, runID)
	if err != nil {
		return domain.Answer{}, fmt.Errorf("load events for run: %w", err)
	}
	if len(events) == 0 {
		return domain.Answer{AnswerText: fmt.Sprintf("No events found for run %q.", runID)}, nil
	}
	return e.answerWithEvents(ctx, question, events, opts, false)
}

// answerWithEvents resolves claims, trust, and contradictions for a set of
// events. When preRanked is true the events are already ordered by relevance
// (e.g. the native pgvector `<=>` fast-path) and are used as-is, truncated to
// answerEventLimit — this skips rankEventsWithFallback, whose cosine branch
// would otherwise reload and re-score the WHOLE event-embedding corpus in Go,
// negating the fast-path's entire reason for existing. When false, the events
// are the full corpus and get the hybrid BM25 + cosine rank.
func (e Engine) answerWithEvents(ctx context.Context, question string, allEvents []domain.Event, opts AnswerOptions, preRanked bool) (domain.Answer, error) {
	q := strings.TrimSpace(question)
	if q == "" {
		return domain.Answer{}, fmt.Errorf("query question is required")
	}
	if len(allEvents) == 0 {
		return domain.Answer{AnswerText: "No ingested events yet."}, nil
	}

	var topEvents []domain.Event
	if preRanked {
		topEvents = allEvents
		if len(topEvents) > answerEventLimit {
			topEvents = topEvents[:answerEventLimit]
		}
	} else {
		topEvents = e.rankEventsWithFallback(ctx, q, allEvents, answerEventLimit)
	}
	if len(topEvents) == 0 {
		return domain.Answer{
			AnswerText: fmt.Sprintf("I have %d events in the knowledge base, but none are relevant to %q. Try a different question or use --embed for semantic search.", len(allEvents), q),
		}, nil
	}
	eventIDs := make([]string, 0, len(topEvents))
	for _, event := range topEvents {
		eventIDs = append(eventIDs, event.ID)
	}

	claims, err := e.claims.ListByEventIDs(ctx, eventIDs)
	if err != nil {
		return domain.Answer{}, fmt.Errorf("load claims for query: %w", err)
	}
	nowUTC := time.Now().UTC()
	for i := range claims {
		if claims[i].LastExecuted.IsZero() {
			claims[i].LastExecuted = trust.EffectiveExecutionTime(
				claims[i].LastExecuted,
				claims[i].LastVerified,
				claims[i].ValidFrom,
				claims[i].CreatedAt,
			)
		}
		if claims[i].Liveness == "" || claims[i].Liveness == domain.LivenessUnknown {
			claims[i].Liveness = trust.EvaluateLiveness(
				claims[i].LastExecuted,
				claims[i].LastVerified,
				claims[i].ValidFrom,
				claims[i].CreatedAt,
				nowUTC,
				claims[i].TrustScore,
			)
		}
		score, rationale := trust.ScoreCredibility(trust.CredibilityInputs{
			CurrentTrust:    claims[i].TrustScore,
			SourceAuthority: claims[i].SourceAuthority,
			Liveness:        claims[i].Liveness,
			CitationCount:   claims[i].CitationCount,
			LastExecuted:    claims[i].LastExecuted,
			LastVerified:    claims[i].LastVerified,
			ValidFrom:       claims[i].ValidFrom,
			CreatedAt:       claims[i].CreatedAt,
			Now:             nowUTC,
			IsTest:          claims[i].Type == domain.ClaimTypeTestResult,
			TestLastRunAt:   claims[i].TestLastRunAt,
			TestPassCount:   claims[i].TestPassCount,
			TestFailCount:   claims[i].TestFailCount,
		})
		claims[i].TrustScore = score
		claims[i].ProvenanceRationale = rationale
	}

	// Entity scope: if the caller restricted the answer to claims
	// linked to a specific entity (--entity in the CLI), drop
	// everything else before ranking. The map is small (one entity's
	// worth of claim ids); the filter is O(claims).
	if opts.AllowedClaimIDs != nil {
		filtered := make([]domain.Claim, 0, len(claims))
		for _, c := range claims {
			if _, ok := opts.AllowedClaimIDs[c.ID]; ok {
				filtered = append(filtered, c)
			}
		}
		claims = filtered
	}

	// Scope filter: narrow the candidate set to claims whose Scope
	// matches the caller's filter before any ranking. Empty filter
	// is a no-op so single-tenant deployments see no change.
	if !opts.Scope.IsEmpty() {
		filtered := make([]domain.Claim, 0, len(claims))
		for _, c := range claims {
			if c.Scope.Matches(opts.Scope) {
				filtered = append(filtered, c)
			}
		}
		claims = filtered
	}

	// Visibility filter: enforce workspace isolation. The zero value is
	// treated as VisibilityTeam. Resolution is additive — each tier
	// includes claims visible to narrower tiers:
	//   personal → only VisibilityPersonal claims
	//   team     → VisibilityPersonal + VisibilityTeam claims (default)
	//   org      → all claims (no filter needed)
	vis := opts.Visibility
	if vis == "" {
		vis = domain.VisibilityTeam
	}
	if vis != domain.VisibilityOrg {
		allowed := visibilityAllowed(vis)
		filtered := make([]domain.Claim, 0, len(claims))
		for _, c := range claims {
			cv := c.Visibility
			if cv == "" {
				cv = domain.VisibilityTeam
			}
			if allowed[cv] {
				filtered = append(filtered, c)
			}
		}
		claims = filtered
	}

	// Lifecycle filter: narrow to a promotion state when requested. Empty
	// is a no-op, so ordinary recall (including claims that were never
	// routed through a candidate→promoted review) is unchanged.
	if opts.Lifecycle != "" {
		filtered := make([]domain.Claim, 0, len(claims))
		for _, c := range claims {
			if c.Lifecycle == opts.Lifecycle {
				filtered = append(filtered, c)
			}
		}
		claims = filtered
	}

	// Filter out low-trust claims before ranking — saves work on the
	// cosine pass and prevents low-trust noise from displacing
	// high-trust answers in the top-N.
	if opts.MinTrust > 0 {
		filtered := make([]domain.Claim, 0, len(claims))
		for _, c := range claims {
			if c.TrustScore >= opts.MinTrust {
				filtered = append(filtered, c)
			}
		}
		claims = filtered
	}

	// Temporal filter: by default, exclude claims that have been
	// superseded (valid_to in the past). Callers asking for history
	// (--include-history) opt out; --at <date> queries swap the
	// cutoff for a point-in-time check.
	if !opts.IncludeHistory {
		asOf := opts.AsOf
		if asOf.IsZero() {
			asOf = time.Now().UTC()
		}
		filtered := make([]domain.Claim, 0, len(claims))
		for _, c := range claims {
			if c.IsValidAt(asOf) {
				filtered = append(filtered, c)
			}
		}
		claims = filtered
	}

	// Ingestion-time filter (the second axis of the bi-temporal
	// model). Drop rows recorded after RecordedAsOf so the response
	// reproduces what the store knew at that timestamp. Independent
	// of the validity filter — a claim that was valid yesterday but
	// recorded today returns under (AsOf=yesterday, RecordedAsOf=now)
	// and disappears under (AsOf=yesterday, RecordedAsOf=yesterday).
	if !opts.RecordedAsOf.IsZero() {
		filtered := make([]domain.Claim, 0, len(claims))
		for _, c := range claims {
			if !c.CreatedAt.After(opts.RecordedAsOf) {
				filtered = append(filtered, c)
			}
		}
		claims = filtered
	}

	// Re-rank claims by semantic similarity when embeddings are available.
	claims = e.rankClaimsByCosine(ctx, q, claims)

	// Boost claims matching the question's intent (e.g., "decisions" → decision type).
	claims = boostClaimsByQuestionIntent(q, claims)

	// Track hop distance per claim — direct claims are hop 0; expansion
	// fills in 1..opts.Hops for claims reached via supports/contradicts edges.
	hopDistance := make(map[string]int, len(claims))
	for _, c := range claims {
		hopDistance[c.ID] = 0
	}
	if opts.Hops > 0 {
		expanded, err := e.expandClaimsByHops(ctx, claims, opts.Hops, hopDistance, opts.HopKinds)
		if err != nil {
			// Hop expansion is additive — log via the standard error path
			// rather than failing the whole answer.
			return domain.Answer{}, fmt.Errorf("expand claims by %d hops: %w", opts.Hops, err)
		}
		claims = append(claims, expanded...)
	}

	// Spreading-activation priming (ADR 0013 §2): re-rank the assembled claim
	// set by blending a bounded associative boost into each claim's score.
	// Opt-in (opts.Prime) so ordinary recall is unchanged; purely a
	// read-only re-weight over the already-retrieved + graph-expanded claims.
	if opts.Prime {
		primed, err := e.applySpreadingActivation(ctx, claims)
		if err != nil {
			return domain.Answer{}, fmt.Errorf("spreading-activation priming: %w", err)
		}
		claims = primed
	}

	contradictions, err := collectContradictions(ctx, e.relationships, claims)
	if err != nil {
		return domain.Answer{}, fmt.Errorf("load contradictions for query: %w", err)
	}

	provenance := e.computeClaimProvenance(ctx, claims, topEvents)
	narratives := e.buildClaimNarratives(ctx, claims)

	answerText := e.generateAnswer(ctx, q, claims, contradictions, len(topEvents), provenance, narratives)
	if opts.Hops > 0 {
		expandedCount := 0
		for _, c := range claims {
			if hopDistance[c.ID] > 0 {
				expandedCount++
			}
		}
		if expandedCount > 0 {
			answerText += fmt.Sprintf(" Expanded %d additional claim(s) via supports/contradicts edges (up to %d hop(s)).", expandedCount, opts.Hops)
		}
	}

	stale := computeStaleClaims(claims, nowUTC)

	// Dual-mode contradiction handling:
	//   agent  → auto-resolve via trust scoring; demote losing claims
	//   user   → surface contradictions with human-readable explanation
	autoResolved := false
	contradictionExplanation := ""
	var verdicts []domain.Verdict
	if len(contradictions) > 0 {
		if opts.Consumer == domain.ConsumerAgent {
			claims, verdicts, autoResolved = resolveContradictionsForAgent(claims, contradictions, nowUTC)
			if autoResolved {
				// Re-collect contradictions against the pruned claim set so
				// the returned slice reflects reality after resolution.
				contradictions, _ = collectContradictions(ctx, e.relationships, claims)
			}
		} else {
			// Default: ConsumerUser — explain but do not resolve.
			contradictionExplanation = buildContradictionExplanation(contradictions, claims)
		}
	}

	// Compute confidence score for the answer.
	confidence := computeConfidence(claims, contradictions, nowUTC)

	return domain.Answer{
		AnswerText:               answerText,
		Claims:                   claims,
		Contradictions:           contradictions,
		TimelineEventIDs:         eventIDs,
		ClaimProvenance:          provenance,
		ClaimHopDistance:         hopDistance,
		StaleClaimIDs:            stale,
		AutoResolved:             autoResolved,
		ContradictionExplanation: contradictionExplanation,
		Verdicts:                 verdicts,
		Confidence:               confidence,
	}, nil
}

// resolveContradictionsForAgent automatically resolves contradictions using
// structural confidence scores. For each contradicting pair:
//   - if either claim's Confidence is below the floor (0.7) or the margin
//     is too slim (< 0.2), TrustScore is used as a tiebreak; if the trust
//     margin is also slim the pair is escalated;
//   - otherwise the lower-confidence claim is demoted and a trust/update
//     Verdict is produced with a rationale string.
//
// Returns the (possibly pruned) claim slice, a slice of Verdicts (one per
// pair processed), and a bool that is true when at least one claim was demoted.
func resolveContradictionsForAgent(claims []domain.Claim, contradictions []domain.Relationship, _ time.Time) ([]domain.Claim, []domain.Verdict, bool) {
	const (
		confidenceFloor  = 0.7
		escalationMargin = 0.2
		trustTiebreak    = 0.05 // minimum TrustScore delta to break a slim-confidence tie
	)
	demoted := map[string]struct{}{}
	verdicts := make([]domain.Verdict, 0, len(contradictions))

	for _, rel := range contradictions {
		if rel.Type != domain.RelationshipTypeContradicts {
			continue
		}
		var from, to *domain.Claim
		for i := range claims {
			if claims[i].ID == rel.FromClaimID {
				from = &claims[i]
			}
			if claims[i].ID == rel.ToClaimID {
				to = &claims[i]
			}
		}
		if from == nil || to == nil {
			continue
		}

		// Test-aware tiebreak runs before the generic confidence path: when
		// both claims are test_result rows under the same requirement ref,
		// the most recent run with the higher pass-ratio is the right
		// winner regardless of LLM-assigned Confidence (which is uniform
		// across CI-generated test claims). Falls through to the generic
		// path on a tie or when one side lacks test provenance.
		if from.Type == domain.ClaimTypeTestResult && to.Type == domain.ClaimTypeTestResult &&
			from.TestRequirementRef != "" && from.TestRequirementRef == to.TestRequirementRef {
			if winner, loser, rationale, ok := pickTestWinner(*from, *to); ok {
				demoted[loser.ID] = struct{}{}
				action := domain.VerdictActionTrust
				if loser.TrustScore > 0.5 {
					action = domain.VerdictActionUpdate
				}
				verdicts = append(verdicts, domain.Verdict{
					WinnerClaimID: winner.ID,
					LoserClaimID:  loser.ID,
					Confidence:    winner.Confidence,
					Rationale:     rationale,
					Action:        action,
				})
				continue
			}
		}

		diff := from.Confidence - to.Confidence
		if diff < 0 {
			diff = -diff
		}

		// When confidence is sufficient and the margin is wide enough,
		// resolve immediately without consulting TrustScore.
		if from.Confidence >= confidenceFloor && to.Confidence >= confidenceFloor && diff >= escalationMargin {
			var winner, loser *domain.Claim
			if from.Confidence >= to.Confidence {
				winner, loser = from, to
			} else {
				winner, loser = to, from
			}
			demoted[loser.ID] = struct{}{}
			action := domain.VerdictActionTrust
			if loser.TrustScore > 0.5 {
				action = domain.VerdictActionUpdate
			}
			rationale := fmt.Sprintf(
				"confidence: winner %.2f vs loser %.2f (margin %.2f); trust: winner %.2f vs loser %.2f",
				winner.Confidence, loser.Confidence, diff,
				winner.TrustScore, loser.TrustScore,
			)
			verdicts = append(verdicts, domain.Verdict{
				WinnerClaimID: winner.ID,
				LoserClaimID:  loser.ID,
				Confidence:    winner.Confidence,
				Rationale:     rationale,
				Action:        action,
			})
			continue
		}

		// Slim-confidence case: use TrustScore as a tiebreak.
		trustDiff := from.TrustScore - to.TrustScore
		if trustDiff < 0 {
			trustDiff = -trustDiff
		}
		if trustDiff < trustTiebreak {
			// Trust delta is also too slim — escalate to human.
			reason := fmt.Sprintf(
				"cannot auto-resolve: confidence %.2f vs %.2f (floor %.2f, margin %.2f); trust %.2f vs %.2f (min delta %.2f)",
				from.Confidence, to.Confidence, confidenceFloor, diff,
				from.TrustScore, to.TrustScore, trustTiebreak,
			)
			verdicts = append(verdicts, domain.Verdict{
				Action:           domain.VerdictActionEscalate,
				EscalationReason: reason,
			})
			continue
		}

		// TrustScore breaks the tie.
		var winner, loser *domain.Claim
		if from.TrustScore >= to.TrustScore {
			winner, loser = from, to
		} else {
			winner, loser = to, from
		}
		demoted[loser.ID] = struct{}{}
		action := domain.VerdictActionTrust
		if loser.TrustScore > 0.5 {
			action = domain.VerdictActionUpdate
		}
		rationale := fmt.Sprintf(
			"confidence margin slim (%.2f vs %.2f, Δ%.2f < %.2f); trust tiebreak: winner %.2f vs loser %.2f (Δ%.2f)",
			from.Confidence, to.Confidence, diff, escalationMargin,
			winner.TrustScore, loser.TrustScore, trustDiff,
		)
		verdicts = append(verdicts, domain.Verdict{
			WinnerClaimID: winner.ID,
			LoserClaimID:  loser.ID,
			Confidence:    winner.Confidence,
			Rationale:     rationale,
			Action:        action,
		})
	}

	if len(demoted) == 0 {
		return claims, verdicts, false
	}
	pruned := make([]domain.Claim, 0, len(claims)-len(demoted))
	for _, c := range claims {
		if _, drop := demoted[c.ID]; !drop {
			pruned = append(pruned, c)
		}
	}
	return pruned, verdicts, true
}

// pickTestWinner resolves a contradiction between two ClaimTypeTestResult
// claims under the same TestRequirementRef. Returns the winner, loser, a
// rationale string, and ok=true when a confident pick can be made.
//
// Decision order:
//  1. Recency: more-recent TestLastRunAt wins when the gap is ≥ 24h. A
//     test that ran today with 0/0 still loses to a stale 50/0 only if
//     the recency gap is below threshold and the pass-ratio gap is
//     decisive — see step 2.
//  2. Pass-ratio decisiveness: |pass-fail|/total. Wins when the ratio
//     gap is ≥ 0.2 and at least one side has a meaningful run count.
//
// Returns ok=false when neither signal exceeds its threshold so the
// generic confidence/trust path can take over.
func pickTestWinner(a, b domain.Claim) (winner, loser *domain.Claim, rationale string, ok bool) {
	const (
		recencyThreshold = 24 * time.Hour
		ratioThreshold   = 0.2
	)

	recencyGap := a.TestLastRunAt.Sub(b.TestLastRunAt)
	if recencyGap < 0 {
		recencyGap = -recencyGap
	}
	if !a.TestLastRunAt.IsZero() && !b.TestLastRunAt.IsZero() && recencyGap >= recencyThreshold {
		var w, l *domain.Claim
		if a.TestLastRunAt.After(b.TestLastRunAt) {
			w, l = &a, &b
		} else {
			w, l = &b, &a
		}
		return w, l, fmt.Sprintf(
			"test recency: winner ran %s, loser ran %s (Δ%s ≥ %s)",
			w.TestLastRunAt.UTC().Format(time.RFC3339),
			l.TestLastRunAt.UTC().Format(time.RFC3339),
			recencyGap.Round(time.Hour),
			recencyThreshold,
		), true
	}

	ra := passRatio(a)
	rb := passRatio(b)
	gap := ra - rb
	if gap < 0 {
		gap = -gap
	}
	if gap >= ratioThreshold {
		var w, l *domain.Claim
		if ra >= rb {
			w, l = &a, &b
		} else {
			w, l = &b, &a
		}
		return w, l, fmt.Sprintf(
			"test pass-ratio: winner %d/%d (%.2f) vs loser %d/%d (%.2f), Δ%.2f ≥ %.2f",
			w.TestPassCount, w.TestPassCount+w.TestFailCount, passRatio(*w),
			l.TestPassCount, l.TestPassCount+l.TestFailCount, passRatio(*l),
			gap, ratioThreshold,
		), true
	}

	return nil, nil, "", false
}

func passRatio(c domain.Claim) float64 {
	total := c.TestPassCount + c.TestFailCount
	if total == 0 {
		return 0
	}
	diff := c.TestPassCount - c.TestFailCount
	if diff < 0 {
		diff = -diff
	}
	return float64(diff) / float64(total)
}

// buildContradictionExplanation produces a human-readable prose summary of
// the contradictions found in the answer, referencing each pair by claim text
// so the reader can reason about the conflict without having to look up IDs.
func buildContradictionExplanation(contradictions []domain.Relationship, claims []domain.Claim) string {
	if len(contradictions) == 0 {
		return ""
	}
	byID := make(map[string]domain.Claim, len(claims))
	for _, c := range claims {
		byID[c.ID] = c
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d contradiction(s) detected:\n", len(contradictions))
	for i, rel := range contradictions {
		from, fOk := byID[rel.FromClaimID]
		to, tOk := byID[rel.ToClaimID]
		if !fOk || !tOk {
			fmt.Fprintf(&b, "  %d. Claim %s contradicts claim %s (details unavailable).\n", i+1, rel.FromClaimID, rel.ToClaimID)
			continue
		}
		fmt.Fprintf(&b, "  %d. %q (trust: %.2f) contradicts %q (trust: %.2f).",
			i+1, from.Text, from.TrustScore, to.Text, to.TrustScore)
		diff := from.TrustScore - to.TrustScore
		if diff < 0 {
			diff = -diff
		}
		confDiff := from.Confidence - to.Confidence
		if confDiff < 0 {
			confDiff = -confDiff
		}
		switch {
		case from.Confidence < 0.7 || to.Confidence < 0.7 || confDiff < 0.2:
			b.WriteString(" Confidence too low or margin too slim for automatic resolution — human review recommended.")
		case from.TrustScore > to.TrustScore:
			fmt.Fprintf(&b, " The first claim has higher trust (Δ%.2f).", diff)
		default:
			fmt.Fprintf(&b, " The second claim has higher trust (Δ%.2f).", diff)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// has decayed below the trust floor. The reference timestamp is the
// later of LastVerified and ValidFrom (validFrom is set from the
// source event timestamp by the pipeline, so it doubles as a
// "latest evidence" proxy when LastVerified is unset). Claims
// without any usable timestamp are treated as not-stale rather than
// flagged with a false signal.
func computeStaleClaims(claims []domain.Claim, now time.Time) []string {
	if len(claims) == 0 {
		return nil
	}
	out := make([]string, 0)
	for _, c := range claims {
		if trust.IsStale(c.ValidFrom, c.LastVerified, now, c.HalfLifeDays, 0) {
			out = append(out, c.ID)
		}
	}
	return out
}

// computeConfidence returns a 0–1 score indicating how confident the system
// is in the answer, based on evidence quality:
//   - average trust score of claims (higher is better)
//   - citation density (more claims = more evidence)
//   - contradiction penalty (presence of unresolved contradictions reduces confidence)
//   - recency (fresher claims = higher confidence)
//
// A value ≥ 0.9 means the answer is "never wrong on recall" grade.
func computeConfidence(claims []domain.Claim, contradictions []domain.Relationship, now time.Time) float64 {
	if len(claims) == 0 {
		return 0.0
	}

	// 1. Average trust score (weight: 0.4)
	var trustSum float64
	for _, c := range claims {
		trustSum += c.TrustScore
	}
	avgTrust := trustSum / float64(len(claims))

	// 2. Citation density bonus (weight: 0.2)
	// More claims = more evidence, but with diminishing returns.
	densityScore := math.Min(float64(len(claims))/5.0, 1.0)

	// 3. Contradiction penalty (weight: 0.3)
	// Each contradiction reduces confidence; max penalty for 3+ contradictions.
	contradictionPenalty := math.Min(float64(len(contradictions))*0.1, 0.3)

	// 4. Recency bonus (weight: 0.1)
	// Fresher claims = higher confidence.
	var ageSum float64
	for _, c := range claims {
		lastActive := c.LastVerified
		if lastActive.IsZero() || c.ValidFrom.After(lastActive) {
			lastActive = c.ValidFrom
		}
		if lastActive.IsZero() {
			lastActive = c.CreatedAt
		}
		ageHours := now.Sub(lastActive).Hours()
		if ageHours < 0 {
			ageHours = 0
		}
		// Claims younger than 24h → 1.0, older than 30 days → 0.0
		ageScore := 1.0 - math.Min(ageHours/(30*24), 1.0)
		ageSum += ageScore
	}
	avgRecency := ageSum / float64(len(claims))

	// Weighted combination.
	confidence := avgTrust*0.4 + densityScore*0.2 - contradictionPenalty + avgRecency*0.1
	if confidence < 0 {
		confidence = 0
	}
	if confidence > 1 {
		confidence = 1
	}
	return confidence
}

// expandClaimsByHops does a BFS through the relationship graph from the
// given seed claims, returning the newly-discovered claims (not the seeds
// themselves). hopDistance is mutated in place: each newly-seen claim is
// recorded with its hop distance from the seed set. Termination: when the
// frontier of newly-discovered IDs is empty or maxHops is reached.
func (e Engine) expandClaimsByHops(ctx context.Context, seed []domain.Claim, maxHops int, hopDistance map[string]int, kinds []domain.RelationshipType) ([]domain.Claim, error) {
	if maxHops <= 0 || len(seed) == 0 {
		return nil, nil
	}
	frontier := make([]string, 0, len(seed))
	for _, c := range seed {
		frontier = append(frontier, c.ID)
	}

	allowed := make(map[domain.RelationshipType]struct{}, len(kinds))
	for _, k := range kinds {
		allowed[k] = struct{}{}
	}

	var expanded []domain.Claim
	for hop := 1; hop <= maxHops && len(frontier) > 0; hop++ {
		rels, err := e.relationships.ListByClaimIDs(ctx, frontier)
		if err != nil {
			return nil, fmt.Errorf("list relationships for hop %d: %w", hop, err)
		}
		nextIDs := map[string]struct{}{}
		for _, rel := range rels {
			if len(allowed) > 0 {
				if _, ok := allowed[rel.Type]; !ok {
					continue
				}
			}
			for _, neighbor := range []string{rel.FromClaimID, rel.ToClaimID} {
				if _, seen := hopDistance[neighbor]; seen {
					continue
				}
				nextIDs[neighbor] = struct{}{}
			}
		}
		if len(nextIDs) == 0 {
			break
		}
		ids := make([]string, 0, len(nextIDs))
		for id := range nextIDs {
			ids = append(ids, id)
		}
		newClaims, err := e.claims.ListByIDs(ctx, ids)
		if err != nil {
			return nil, fmt.Errorf("load hop-%d claims: %w", hop, err)
		}
		for _, c := range newClaims {
			hopDistance[c.ID] = hop
		}
		expanded = append(expanded, newClaims...)
		frontier = ids
	}
	return expanded, nil
}

// buildClaimNarratives returns a per-claim lifecycle sentence for claims
// that have non-trivial history (at least one real status transition after
// the initial insert). Claims whose status never changed from their first
// recording have no narrative — there's no story to tell.
//
// Format example:
//
//	"First recorded as active on 2026-04-12; became contested on 2026-04-15
//	 (auto: contradiction detected); resolved on 2026-04-18 (evidence
//	 review by jane)."
//
// This is the narrative layer from issue #6 — turning the claim_status_history
// rows into a prose explanation so the query answer carries a temporal
// summary instead of just a current snapshot.
func (e Engine) buildClaimNarratives(ctx context.Context, claims []domain.Claim) map[string]string {
	if len(claims) == 0 {
		return nil
	}
	out := make(map[string]string, len(claims))
	// Only narrate the top few claims — a query for 50 claims shouldn't
	// dump 50 timelines into the answer.
	limit := 3
	if len(claims) < limit {
		limit = len(claims)
	}
	for i := 0; i < limit; i++ {
		c := claims[i]
		hist, err := e.claims.ListStatusHistoryByClaimID(ctx, c.ID)
		if err != nil || len(hist) == 0 {
			continue
		}
		// Narrative is only interesting when the status actually changed at
		// some point. A single initial-insert row (from_status="") has
		// nothing to tell beyond the current status snapshot, which the
		// main answer already shows.
		hasRealTransition := false
		for _, t := range hist {
			if t.FromStatus != "" {
				hasRealTransition = true
				break
			}
		}
		if !hasRealTransition {
			continue
		}
		out[c.ID] = formatNarrative(hist)
	}
	return out
}

func formatNarrative(hist []domain.ClaimStatusTransition) string {
	if len(hist) == 0 {
		return ""
	}
	var b strings.Builder
	for i, t := range hist {
		switch {
		case i == 0 && t.FromStatus == "":
			// Fresh history: we saw the insert.
			fmt.Fprintf(&b, "First recorded as %s on %s", t.ToStatus, t.ChangedAt.Format("2006-01-02"))
		case i == 0:
			// Backfilled / pre-existing: first recorded transition was
			// from an already-known status. Phrase it as an update rather
			// than as an initial creation.
			fmt.Fprintf(&b, "Transitioned from %s to %s on %s", t.FromStatus, t.ToStatus, t.ChangedAt.Format("2006-01-02"))
		default:
			fmt.Fprintf(&b, "; became %s on %s", t.ToStatus, t.ChangedAt.Format("2006-01-02"))
		}
		if t.Reason != "" {
			fmt.Fprintf(&b, " (%s)", t.Reason)
		}
	}
	b.WriteString(".")
	return b.String()
}

// computeClaimProvenance builds a per-claim origin map: "local" for claims
// whose evidence events have no pulled_from_registry metadata, or the
// registry URL when at least one evidence event was pulled. The first
// non-local origin wins because the question users ask is "where did this
// originate?" — once a claim is known to have a remote source, that's the
// load-bearing fact.
//
// Failures (e.g. evidence lookup error) silently yield an empty map; the
// engine never blocks an answer on provenance attribution.
func (e Engine) computeClaimProvenance(ctx context.Context, claims []domain.Claim, topEvents []domain.Event) map[string]string {
	if len(claims) == 0 {
		return nil
	}
	claimIDs := make([]string, 0, len(claims))
	for _, c := range claims {
		claimIDs = append(claimIDs, c.ID)
	}
	evidence, err := e.claims.ListEvidenceByClaimIDs(ctx, claimIDs)
	if err != nil || len(evidence) == 0 {
		return nil
	}

	// eventOrigin: event id → "local" or "<registry-url>"
	eventOrigin := make(map[string]string, len(topEvents))
	for _, ev := range topEvents {
		if reg, ok := ev.Metadata["pulled_from_registry"]; ok && reg != "" {
			eventOrigin[ev.ID] = reg
		} else {
			eventOrigin[ev.ID] = "local"
		}
	}

	out := make(map[string]string, len(claimIDs))
	for _, link := range evidence {
		origin, ok := eventOrigin[link.EventID]
		if !ok {
			continue // evidence event not in our top set; skip
		}
		existing, seen := out[link.ClaimID]
		if !seen || (existing == "local" && origin != "local") {
			out[link.ClaimID] = origin
		}
	}
	return out
}

// rankEventsWithFallback chooses the best ranking strategy available:
//   - Hybrid (BM25 + cosine) when both signals are wired up.
//   - Either signal alone when only one is wired up.
//   - Legacy token-overlap ranker when neither is available
//     (in-memory test doubles and embedding-less, FTS-less DBs).
//
// Hybrid scoring rationale: BM25 catches lexical / proper-noun
// matches that embeddings miss; embeddings catch synonyms and
// paraphrases that BM25 misses. Combining them is a well-trodden
// retrieval technique that typically yields a +20–40% nDCG over
// either signal alone — see the "obvious choice" v0.10 design note.
func (e Engine) rankEventsWithFallback(ctx context.Context, question string, events []domain.Event, limit int) []domain.Event {
	cosScores := e.cosineEventScores(ctx, question, events)
	bm25Scores := e.bm25EventScores(ctx, question, len(events)+limit)
	if len(cosScores) == 0 && len(bm25Scores) == 0 {
		return rankEvents(question, events, limit)
	}
	return rankEventsByHybridScore(events, cosScores, bm25Scores, limit)
}

// cosineEventScores returns a map of event id → cosine similarity
// against the question embedding, or an empty map when embeddings
// aren't available (so the caller can detect "no signal").
func (e Engine) cosineEventScores(ctx context.Context, question string, events []domain.Event) map[string]float64 {
	if e.embeddings == nil || e.embedClient == nil || len(events) == 0 {
		return nil
	}
	stored, err := e.embeddings.ListByEntityType(ctx, "event")
	if err != nil || len(stored) == 0 {
		return nil
	}
	// Confine to the query embedder's model space so vectors from a different
	// model are never cosined together (empty query model = no filter).
	qModel := embedding.ModelIDOf(e.embedClient)
	vecByID := make(map[string][]float32, len(stored))
	for _, rec := range stored {
		if qModel != "" && rec.Model != qModel {
			continue
		}
		vecByID[rec.EntityID] = rec.Vector
	}
	hasAny := false
	for _, ev := range events {
		if _, ok := vecByID[ev.ID]; ok {
			hasAny = true
			break
		}
	}
	if !hasAny {
		return nil
	}
	qVectors, err := e.embedClient.Embed(ctx, []string{question})
	if err != nil || len(qVectors) == 0 {
		return nil
	}
	qVec := qVectors[0]
	out := make(map[string]float64, len(events))
	for _, ev := range events {
		vec, ok := vecByID[ev.ID]
		if !ok {
			continue
		}
		sim, err := embedding.CosineSimilarity(qVec, vec)
		if err != nil {
			continue
		}
		out[ev.ID] = float64(sim)
	}
	return out
}

// bm25EventScores returns event id → BM25 relevance from the FTS5
// index. Already sign-flipped so higher = better. Empty when no
// TextSearcher is wired or the search returns nothing.
func (e Engine) bm25EventScores(ctx context.Context, question string, limit int) map[string]float64 {
	if e.eventTextSearch == nil {
		return nil
	}
	hits, err := e.eventTextSearch.SearchByText(ctx, question, limit)
	if err != nil || len(hits) == 0 {
		return nil
	}
	out := make(map[string]float64, len(hits))
	for _, h := range hits {
		out[h.ID] = h.Score
	}
	return out
}

// rankEventsByHybridScore combines the two signal maps into one
// composite score and returns the top-`limit` events. The math is
// deliberately conservative: max-normalise each signal into [0, 1]
// independently, then take the equal-weighted sum. This avoids
// arbitrary weighting decisions while letting either signal dominate
// when the other is silent.
func rankEventsByHybridScore(events []domain.Event, cos, bm25 map[string]float64, limit int) []domain.Event {
	cosMax := maxScore(cos)
	bmMax := maxScore(bm25)

	type scored struct {
		event domain.Event
		score float64
	}
	out := make([]scored, 0, len(events))
	for _, ev := range events {
		var c, b float64
		if cosMax > 0 {
			c = cos[ev.ID] / cosMax
		}
		if bmMax > 0 {
			b = bm25[ev.ID] / bmMax
		}
		if c == 0 && b == 0 {
			// Neither signal saw this event — drop rather than
			// pretend it's a relevant hit.
			continue
		}
		// When only one signal is present, that score carries the
		// full weight; when both, equal-weighted average.
		switch {
		case cosMax > 0 && bmMax > 0:
			out = append(out, scored{ev, 0.5*c + 0.5*b})
		case bmMax > 0:
			out = append(out, scored{ev, b})
		default:
			out = append(out, scored{ev, c})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].score == out[j].score {
			return out[i].event.Timestamp.After(out[j].event.Timestamp)
		}
		return out[i].score > out[j].score
	})
	end := limit
	if end > len(out) {
		end = len(out)
	}
	result := make([]domain.Event, 0, end)
	for i := 0; i < end; i++ {
		result = append(result, out[i].event)
	}
	return result
}

func maxScore(m map[string]float64) float64 {
	max := 0.0
	for _, v := range m {
		if v > max {
			max = v
		}
	}
	return max
}

// cosineClaimScores mirrors cosineEventScores but for claim embeddings.
func (e Engine) cosineClaimScores(ctx context.Context, question string, claims []domain.Claim) map[string]float64 {
	if e.embeddings == nil || e.embedClient == nil || len(claims) == 0 {
		return nil
	}
	stored, err := e.embeddings.ListByEntityType(ctx, "claim")
	if err != nil || len(stored) == 0 {
		return nil
	}
	// Confine to the query embedder's model space (empty query model = no filter).
	qModel := embedding.ModelIDOf(e.embedClient)
	vecByID := make(map[string][]float32, len(stored))
	for _, rec := range stored {
		if qModel != "" && rec.Model != qModel {
			continue
		}
		vecByID[rec.EntityID] = rec.Vector
	}
	hasAny := false
	for _, cl := range claims {
		if _, ok := vecByID[cl.ID]; ok {
			hasAny = true
			break
		}
	}
	if !hasAny {
		return nil
	}
	qVectors, err := e.embedClient.Embed(ctx, []string{question})
	if err != nil || len(qVectors) == 0 {
		return nil
	}
	qVec := qVectors[0]
	out := make(map[string]float64, len(claims))
	for _, cl := range claims {
		vec, ok := vecByID[cl.ID]
		if !ok {
			continue
		}
		sim, err := embedding.CosineSimilarity(qVec, vec)
		if err != nil {
			continue
		}
		out[cl.ID] = float64(sim)
	}
	return out
}

// bm25ClaimScores mirrors bm25EventScores but for the claims_fts index.
func (e Engine) bm25ClaimScores(ctx context.Context, question string, limit int) map[string]float64 {
	if e.claimTextSearch == nil {
		return nil
	}
	hits, err := e.claimTextSearch.SearchByText(ctx, question, limit)
	if err != nil || len(hits) == 0 {
		return nil
	}
	out := make(map[string]float64, len(hits))
	for _, h := range hits {
		out[h.ID] = h.Score
	}
	return out
}

// rankClaimsByCosine reorders claims by relevance to the question.
// Despite the legacy name, this is now the hybrid ranker: it
// combines BM25 (when a TextSearcher is wired) with cosine
// similarity (when embeddings are wired), max-normalising each
// signal into [0, 1] before equal-weighted summation. Claims with
// no signal at all retain their original relative position via the
// idx tiebreak so callers see "embedding-less" claims at the bottom
// rather than randomly shuffled.
func (e Engine) rankClaimsByCosine(ctx context.Context, question string, claims []domain.Claim) []domain.Claim {
	if len(claims) <= 1 {
		return claims
	}

	cos := e.cosineClaimScores(ctx, question, claims)
	bm := e.bm25ClaimScores(ctx, question, len(claims)+10)
	if len(cos) == 0 && len(bm) == 0 {
		return claims
	}

	cosMax := maxScore(cos)
	bmMax := maxScore(bm)

	type scored struct {
		claim domain.Claim
		score float64
		idx   int // original index for stable ordering of tied / signal-less claims
	}
	scoredClaims := make([]scored, 0, len(claims))
	for i, cl := range claims {
		var c, b float64
		if cosMax > 0 {
			c = cos[cl.ID] / cosMax
		}
		if bmMax > 0 {
			b = bm[cl.ID] / bmMax
		}
		var s float64
		switch {
		case cosMax > 0 && bmMax > 0:
			s = 0.5*c + 0.5*b
		case bmMax > 0:
			s = b
		case cosMax > 0:
			s = c
		}
		if s == 0 {
			s = -1 // signal-less claim; sinks below any positive hit but keeps original order
		}
		scoredClaims = append(scoredClaims, scored{claim: cl, score: s, idx: i})
	}

	sort.Slice(scoredClaims, func(i, j int) bool {
		if scoredClaims[i].score == scoredClaims[j].score {
			return scoredClaims[i].idx < scoredClaims[j].idx
		}
		return scoredClaims[i].score > scoredClaims[j].score
	})

	result := make([]domain.Claim, 0, len(scoredClaims))
	for _, sc := range scoredClaims {
		result = append(result, sc.claim)
	}
	return result
}

// inferQuestionIntent returns a preferred claim type based on question keywords,
// or empty string if no clear intent is detected.
func inferQuestionIntent(question string) domain.ClaimType {
	q := strings.ToLower(question)
	decisionWords := []string{"decision", "decide", "chose", "choose", "pick", "selected", "approve", "commit"}
	hypothesisWords := []string{"risk", "might", "could", "possibly", "hypothesis", "maybe", "uncertain", "assume"}
	factWords := []string{"what happened", "did we", "how many", "status", "metric", "measure"}

	for _, w := range decisionWords {
		if strings.Contains(q, w) {
			return domain.ClaimTypeDecision
		}
	}
	for _, w := range hypothesisWords {
		if strings.Contains(q, w) {
			return domain.ClaimTypeHypothesis
		}
	}
	for _, w := range factWords {
		if strings.Contains(q, w) {
			return domain.ClaimTypeFact
		}
	}
	return ""
}

// boostClaimsByQuestionIntent reorders claims so those matching the question's
// intent (decision/hypothesis/fact) appear first. Preserves relative order
// within each group.
func boostClaimsByQuestionIntent(question string, claims []domain.Claim) []domain.Claim {
	intent := inferQuestionIntent(question)
	if intent == "" || len(claims) <= 1 {
		return claims
	}

	matched := make([]domain.Claim, 0)
	other := make([]domain.Claim, 0)
	for _, c := range claims {
		if c.Type == intent {
			matched = append(matched, c)
		} else {
			other = append(other, c)
		}
	}
	if len(matched) == 0 {
		return claims
	}
	return append(matched, other...)
}

// BM25 parameters tuned for short-to-medium technical documents.
const (
	bm25K1 = 1.5
	bm25B  = 0.75
)

// docTokens returns all tokens (including duplicates) from text, normalized.
func docTokens(text string) []string {
	out := []string{}
	for _, token := range strings.Fields(strings.ToLower(text)) {
		token = strings.Trim(token, ",.;:!?()[]{}\"'")
		if token == "" {
			continue
		}
		out = append(out, token)
	}
	return out
}

// rankEvents scores events against the question using BM25, a standard
// information retrieval algorithm that accounts for term frequency,
// inverse document frequency, and document length normalization.
func rankEvents(question string, events []domain.Event, limit int) []domain.Event {
	if len(events) == 0 {
		return nil
	}

	qTokens := docTokens(question)
	if len(qTokens) == 0 {
		return nil
	}

	// Build document frequency for BM25 IDF.
	df := map[string]int{}
	docLens := make([]int, len(events))
	totalLen := 0
	for i, event := range events {
		tokens := docTokens(event.Content)
		docLens[i] = len(tokens)
		totalLen += len(tokens)
		seen := map[string]struct{}{}
		for _, t := range tokens {
			if _, ok := seen[t]; ok {
				continue
			}
			seen[t] = struct{}{}
			df[t]++
		}
	}
	avgDocLen := float64(totalLen) / float64(len(events))
	n := float64(len(events))

	// Deduplicate query tokens (BM25 treats each query term once).
	qUnique := map[string]struct{}{}
	for _, t := range qTokens {
		qUnique[t] = struct{}{}
	}

	type scored struct {
		event domain.Event
		score float64
	}
	scoredEvents := make([]scored, 0, len(events))
	for i, event := range events {
		tokens := docTokens(event.Content)
		tf := map[string]int{}
		for _, t := range tokens {
			tf[t]++
		}

		s := 0.0
		docLen := float64(docLens[i])
		for qt := range qUnique {
			freq := tf[qt]
			if freq == 0 {
				continue
			}
			dfQT := df[qt]
			// BM25 IDF: log((N - df + 0.5) / (df + 0.5) + 1)
			idf := math.Log((n-float64(dfQT)+0.5)/(float64(dfQT)+0.5) + 1)
			numerator := float64(freq) * (bm25K1 + 1)
			denominator := float64(freq) + bm25K1*(1-bm25B+bm25B*docLen/avgDocLen)
			s += idf * numerator / denominator
		}

		if s > 0 {
			scoredEvents = append(scoredEvents, scored{event: event, score: s})
		}
	}

	if len(scoredEvents) == 0 {
		return nil
	}

	sort.Slice(scoredEvents, func(i, j int) bool {
		if scoredEvents[i].score == scoredEvents[j].score {
			return scoredEvents[i].event.Timestamp.After(scoredEvents[j].event.Timestamp)
		}
		return scoredEvents[i].score > scoredEvents[j].score
	})

	out := make([]domain.Event, 0, min(limit, len(scoredEvents)))
	for i := 0; i < len(scoredEvents) && i < limit; i++ {
		out = append(out, scoredEvents[i].event)
	}
	return out
}

func collectContradictions(ctx context.Context, repo ports.RelationshipRepository, claims []domain.Claim) ([]domain.Relationship, error) {
	seen := map[string]struct{}{}
	result := make([]domain.Relationship, 0)
	for _, claim := range claims {
		rels, err := repo.ListByClaim(ctx, claim.ID)
		if err != nil {
			return nil, err
		}
		for _, rel := range rels {
			if rel.Type != domain.RelationshipTypeContradicts {
				continue
			}
			if _, ok := seen[rel.ID]; ok {
				continue
			}
			seen[rel.ID] = struct{}{}
			result = append(result, rel)
		}
	}
	return result, nil
}

// generateAnswer produces the answer text. When an LLM client is configured,
// it synthesizes a grounded answer from the retrieved claims. Falls back to
// the template-based answer on LLM failure or when no client is set.
func (e Engine) generateAnswer(ctx context.Context, question string, claims []domain.Claim, contradictions []domain.Relationship, eventCount int, provenance map[string]string, narratives map[string]string) string {
	if e.llmClient == nil || len(claims) == 0 {
		return buildAnswerText(question, claims, contradictions, eventCount, provenance, narratives)
	}

	answer, err := e.groundedAnswer(ctx, question, claims, contradictions)
	if err != nil {
		// Fall back to template on any LLM error.
		return buildAnswerText(question, claims, contradictions, eventCount, provenance, narratives)
	}
	return answer
}

const groundedSystemPrompt = `You are Mnemos, an evidence-backed knowledge engine. Answer the user's question using ONLY the provided claims as evidence.

Rules:
1. Cite claims by their number (e.g., [1], [2]) when referencing them.
2. If claims contradict each other, explicitly acknowledge the contradiction.
3. Do not add information not present in the claims.
4. Be concise — 2-4 sentences.
5. If the claims do not address the question, say so.`

func (e Engine) groundedAnswer(ctx context.Context, question string, claims []domain.Claim, contradictions []domain.Relationship) (string, error) {
	var b strings.Builder
	b.WriteString("Question: ")
	b.WriteString(question)
	b.WriteString("\n\nClaims:\n")
	for i, cl := range claims {
		fmt.Fprintf(&b, "[%d] %s (type: %s, confidence: %.2f, status: %s)\n", i+1, cl.Text, cl.Type, cl.Confidence, cl.Status)
	}
	if len(contradictions) > 0 {
		b.WriteString("\nContradictions:\n")
		for _, rel := range contradictions {
			fmt.Fprintf(&b, "- Claim %s contradicts claim %s\n", rel.FromClaimID, rel.ToClaimID)
		}
	}

	resp, err := e.llmClient.Complete(ctx, []llm.Message{
		{Role: llm.RoleSystem, Content: groundedSystemPrompt},
		{Role: llm.RoleUser, Content: b.String()},
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Content), nil
}

func buildAnswerText(question string, claims []domain.Claim, contradictions []domain.Relationship, eventCount int, provenance map[string]string, narratives map[string]string) string {
	if len(claims) == 0 {
		return fmt.Sprintf("I could not find claims yet for %q. Try running extract/relate first.", question)
	}

	parts := []string{}
	parts = append(parts, fmt.Sprintf("For %q, the strongest signal is: %s%s.", question, claims[0].Text, provenanceSuffix(claims[0].ID, provenance)))
	if n, ok := narratives[claims[0].ID]; ok {
		parts = append(parts, "Evolution: "+n)
	}

	if len(claims) > 1 {
		parts = append(parts, fmt.Sprintf("Other relevant claim: %s%s.", claims[1].Text, provenanceSuffix(claims[1].ID, provenance)))
		if n, ok := narratives[claims[1].ID]; ok {
			parts = append(parts, "Evolution: "+n)
		}
	}

	if len(contradictions) > 0 {
		parts = append(parts, fmt.Sprintf("I also found %d contradiction(s), so this topic is contested.", len(contradictions)))
	} else {
		parts = append(parts, "No contradictions were found in the current claim set.")
	}

	if remoteCount := countRemoteClaims(claims, provenance); remoteCount > 0 {
		parts = append(parts, fmt.Sprintf("Context used %d event(s) and %d claim(s) (%d from a connected registry).", eventCount, len(claims), remoteCount))
	} else {
		parts = append(parts, fmt.Sprintf("Context used %d event(s) and %d claim(s).", eventCount, len(claims)))
	}
	return strings.Join(parts, " ")
}

// provenanceSuffix returns " (from <registry-url>)" for claims pulled from a
// registry, empty for local or unknown claims. Local claims aren't tagged
// because that's the unmarked default — flagging every local one would add
// noise to single-project queries.
func provenanceSuffix(claimID string, provenance map[string]string) string {
	if provenance == nil {
		return ""
	}
	origin, ok := provenance[claimID]
	if !ok || origin == "local" || origin == "" {
		return ""
	}
	return " (from " + origin + ")"
}

func countRemoteClaims(claims []domain.Claim, provenance map[string]string) int {
	if provenance == nil {
		return 0
	}
	n := 0
	for _, c := range claims {
		if origin := provenance[c.ID]; origin != "" && origin != "local" {
			n++
		}
	}
	return n
}

// AuditEntry is a single row in an AuditTrail report: the decision that went
// wrong, the claim IDs that underpinned it, and the outcome that falsified it.
type AuditEntry struct {
	Decision        domain.Decision
	RefutedBeliefs  []string
	FailedOutcomeID string
}

// AuditTrailOptions controls which decisions appear in the audit trail.
// All fields are optional — the zero value returns every decision that has
// a non-empty FailedOutcomeID (i.e. decisions that were later refuted).
type AuditTrailOptions struct {
	// Service, when non-empty, restricts results to decisions whose
	// Scope.Service matches the filter (case-sensitive). Empty means all services.
	Service string
	// IncludeSuccessful, when true, also returns decisions that have no
	// FailedOutcomeID (decisions that were never falsified). Default false
	// so the audit trail focuses on actionable failures.
	IncludeSuccessful bool
	// RiskLevel, when non-empty, restricts results to the given risk level
	// ("low", "medium", "high", "critical"). Empty means all risk levels.
	RiskLevel string
}

// AuditTrail returns the set of decisions that were later refuted by a failed
// outcome, optionally filtered by service and/or risk level. It requires that
// WithDecisions was called; if no DecisionRepository is wired it returns an
// error.
func (e Engine) AuditTrail(ctx context.Context, opts AuditTrailOptions) ([]AuditEntry, error) {
	if e.decisions == nil {
		return nil, fmt.Errorf("query.Engine: AuditTrail called but no DecisionRepository is wired — call WithDecisions first")
	}

	var decisions []domain.Decision
	var err error

	switch {
	case opts.RiskLevel != "":
		decisions, err = e.decisions.ListByRiskLevel(ctx, opts.RiskLevel)
	default:
		decisions, err = e.decisions.ListAll(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("query.Engine: AuditTrail: listing decisions: %w", err)
	}

	entries := make([]AuditEntry, 0, len(decisions))
	for _, d := range decisions {
		if opts.Service != "" && d.Scope.Service != opts.Service { //nolint:gocritic
			continue
		}
		if !opts.IncludeSuccessful && d.FailedOutcomeID == "" {
			continue
		}
		entries = append(entries, AuditEntry{
			Decision:        d,
			RefutedBeliefs:  d.RefutedBeliefs,
			FailedOutcomeID: d.FailedOutcomeID,
		})
	}
	return entries, nil
}

// WhyWereWeWrongReport is the structured result of a WhyWereWeWrong
// analysis. It bundles the incident, the root-cause claim with its full
// provenance breakdown, the decisions that relied on that claim, and a
// plain-English explanation of what went wrong epistemically.
type WhyWereWeWrongReport struct {
	// Incident is the incident under analysis.
	Incident domain.Incident
	// RootClaim is the claim that turned out to be wrong. Nil when
	// the incident has no RootCauseClaimID or the claim is not found.
	RootClaim *domain.ProvenanceReport
	// AffectedDecisions are the decisions that were built on the
	// wrong belief, hydrated from the incident's DecisionIDs.
	AffectedDecisions []domain.Decision
	// Explanation is a short prose summary suitable for display.
	Explanation string
}

// WhyWereWeWrong performs a post-incident epistemic analysis: given an
// incident ID it fetches the incident, loads the root-cause claim's full
// provenance, and surfaces all decisions that were grounded in that claim.
// This drives the "Why were we wrong?" dashboard and anti-lesson synthesis.
//
// Requires WithIncidents and WithDecisions to be called; returns an error
// if either is missing.
func (e Engine) WhyWereWeWrong(ctx context.Context, incidentID string) (WhyWereWeWrongReport, error) {
	if e.incidents == nil {
		return WhyWereWeWrongReport{}, fmt.Errorf("query.Engine: WhyWereWeWrong called but no IncidentRepository is wired — call WithIncidents first")
	}
	if e.decisions == nil {
		return WhyWereWeWrongReport{}, fmt.Errorf("query.Engine: WhyWereWeWrong called but no DecisionRepository is wired — call WithDecisions first")
	}

	incident, found, err := e.incidents.GetByID(ctx, incidentID)
	if err != nil {
		return WhyWereWeWrongReport{}, fmt.Errorf("WhyWereWeWrong: load incident %s: %w", incidentID, err)
	}
	if !found {
		return WhyWereWeWrongReport{}, fmt.Errorf("WhyWereWeWrong: incident %s not found", incidentID)
	}

	report := WhyWereWeWrongReport{Incident: incident}

	// Load root-cause claim provenance.
	if incident.RootCauseClaimID != "" {
		prov, err := e.WhyTrustClaim(ctx, incident.RootCauseClaimID)
		if err == nil {
			report.RootClaim = &prov
		}
		// If the claim is not found we proceed without it — the incident
		// may have been opened with a forward reference to a claim not
		// yet ingested.
	}

	// Hydrate affected decisions.
	for _, did := range incident.DecisionIDs {
		d, err := e.decisions.ListAll(ctx) // narrow below
		if err != nil {
			return WhyWereWeWrongReport{}, fmt.Errorf("WhyWereWeWrong: list decisions: %w", err)
		}
		for _, dec := range d {
			if dec.ID == did {
				report.AffectedDecisions = append(report.AffectedDecisions, dec)
				break
			}
		}
	}

	report.Explanation = buildWWWExplanation(report)
	return report, nil
}

// buildWWWExplanation produces a plain-English summary of the
// WhyWereWeWrong analysis. Called after all fields are populated.
func buildWWWExplanation(r WhyWereWeWrongReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Incident %q (%s severity, %s): %s",
		r.Incident.Title, r.Incident.Severity, r.Incident.Status, r.Incident.Summary)

	if r.RootClaim != nil {
		fmt.Fprintf(&b, "\n\nThe root-cause claim was: %q (trust score %.2f). %s",
			r.RootClaim.ClaimText, r.RootClaim.Score, r.RootClaim.Rationale)
	} else if r.Incident.RootCauseClaimID != "" {
		fmt.Fprintf(&b, "\n\nRoot-cause claim ID: %s (claim not found in store).",
			r.Incident.RootCauseClaimID)
	}

	if len(r.AffectedDecisions) > 0 {
		fmt.Fprintf(&b, "\n\n%d decision(s) relied on this belief:", len(r.AffectedDecisions))
		for _, d := range r.AffectedDecisions {
			fmt.Fprintf(&b, "\n  • [%s] %s (risk: %s)", d.ID, d.Statement, d.RiskLevel)
		}
	}

	return b.String()
}

// visibilityAllowed returns the set of Visibility values a caller at the given
// tier may see. The tier is additive: team callers see personal + team claims;
// personal callers see only their own personal claims.
func visibilityAllowed(vis domain.Visibility) map[domain.Visibility]bool {
	switch vis {
	case domain.VisibilityPersonal:
		return map[domain.Visibility]bool{
			domain.VisibilityPersonal: true,
		}
	default: // VisibilityTeam and anything unexpected
		return map[domain.Visibility]bool{
			domain.VisibilityPersonal: true,
			domain.VisibilityTeam:     true,
		}
	}
}

// WhyTrustClaim builds a ProvenanceReport for the given claim ID: it fetches
// the claim, re-runs ScoreCredibility with per-signal breakdown, and returns
// a machine-readable + human-readable explanation of the trust decision.
//
// Returns domain.ErrNotFound (wrapped) if the claim does not exist.
func (e Engine) WhyTrustClaim(ctx context.Context, claimID string) (domain.ProvenanceReport, error) {
	claims, err := e.claims.ListByIDs(ctx, []string{claimID})
	if err != nil {
		return domain.ProvenanceReport{}, fmt.Errorf("WhyTrustClaim: load claim %s: %w", claimID, err)
	}
	if len(claims) == 0 {
		return domain.ProvenanceReport{}, fmt.Errorf("WhyTrustClaim: claim %s not found", claimID)
	}
	c := claims[0]

	nowUTC := time.Now().UTC()
	if c.LastExecuted.IsZero() {
		c.LastExecuted = trust.EffectiveExecutionTime(c.LastExecuted, c.LastVerified, c.ValidFrom, c.CreatedAt)
	}
	if c.Liveness == "" || c.Liveness == domain.LivenessUnknown {
		c.Liveness = trust.EvaluateLiveness(c.LastExecuted, c.LastVerified, c.ValidFrom, c.CreatedAt, nowUTC, c.TrustScore)
	}

	in := trust.CredibilityInputs{
		CurrentTrust:    c.TrustScore,
		SourceAuthority: c.SourceAuthority,
		Liveness:        c.Liveness,
		CitationCount:   c.CitationCount,
		LastExecuted:    c.LastExecuted,
		LastVerified:    c.LastVerified,
		ValidFrom:       c.ValidFrom,
		CreatedAt:       c.CreatedAt,
		Now:             nowUTC,
		IsTest:          c.Type == domain.ClaimTypeTestResult,
		TestLastRunAt:   c.TestLastRunAt,
		TestPassCount:   c.TestPassCount,
		TestFailCount:   c.TestFailCount,
	}
	score, signals, rationale, prose := trust.BuildReport(in)

	return domain.ProvenanceReport{
		ClaimID:        c.ID,
		ClaimText:      c.Text,
		Score:          score,
		Signals:        signals,
		Rationale:      rationale,
		ProseRationale: prose,
		SourceDocument: c.SourceDocument,
		Liveness:       c.Liveness,
	}, nil
}

// MemoryQualityMetrics holds telemetry about the current memory store health.
type MemoryQualityMetrics struct {
	TotalClaims        int
	AvgTrustScore      float64
	AvgConfidence      float64
	StaleCount         int
	ContestedCount     int
	ContradictionCount int
	AvgCitationCount   float64
}

// ComputeMemoryQuality scans all claims and returns quality telemetry.
// This is the "memory-quality telemetry" sub-task of reliability-first recall.
func (e Engine) ComputeMemoryQuality(ctx context.Context) (MemoryQualityMetrics, error) {
	claims, err := e.claims.ListAll(ctx)
	if err != nil {
		return MemoryQualityMetrics{}, fmt.Errorf("compute memory quality: %w", err)
	}
	if len(claims) == 0 {
		return MemoryQualityMetrics{}, nil
	}

	var trustSum, confSum, citSum float64
	staleCount, contestedCount := 0, 0
	for _, c := range claims {
		trustSum += c.TrustScore
		confSum += c.Confidence
		citSum += float64(c.CitationCount)
		if c.Status == domain.ClaimStatusContested {
			contestedCount++
		}
	}
	// Get contradiction count.
	rels, err := e.relationships.ListAll(ctx)
	if err != nil {
		return MemoryQualityMetrics{}, fmt.Errorf("compute memory quality: %w", err)
	}
	contradictionCount := 0
	for _, r := range rels {
		if r.Type == domain.RelationshipTypeContradicts {
			contradictionCount++
		}
	}

	return MemoryQualityMetrics{
		TotalClaims:        len(claims),
		AvgTrustScore:      trustSum / float64(len(claims)),
		AvgConfidence:      confSum / float64(len(claims)),
		StaleCount:         staleCount,
		ContestedCount:     contestedCount,
		ContradictionCount: contradictionCount,
		AvgCitationCount:   citSum / float64(len(claims)),
	}, nil
}

// EscalateClaimForAgent is trigger-3 escalation: an agent has determined it
// cannot proceed and explicitly requests a human decision on a specific claim.
// It records the request as a Verdict with Action=escalate and the
// agent-provided reason verbatim, so the audit trail captures who escalated
// and why.  The claim must exist; if not found an error is returned.
func (e Engine) EscalateClaimForAgent(ctx context.Context, claimID, agentReason string) (domain.Verdict, error) {
	claims, err := e.claims.ListByIDs(ctx, []string{claimID})
	if err != nil {
		return domain.Verdict{}, fmt.Errorf("escalate claim %q: %w", claimID, err)
	}
	if len(claims) == 0 {
		return domain.Verdict{}, fmt.Errorf("escalate claim %q: not found", claimID)
	}
	c := claims[0]
	reason := agentReason
	if reason == "" {
		reason = "agent-requested escalation (no reason provided)"
	}
	return domain.Verdict{
		WinnerClaimID:    c.ID,
		LoserClaimID:     "",
		Confidence:       c.Confidence,
		Rationale:        fmt.Sprintf("agent explicitly escalated claim %q: %s", c.ID, reason),
		Action:           domain.VerdictActionEscalate,
		EscalationReason: reason,
	}, nil
}

// SLOThresholds defines the reliability-first SLOs for rollout guardrails.
type SLOThresholds struct {
	MinRecallPassRate float64 // recall_pass_rate must be >= this
	MinConfidenceAvg  float64 // avg confidence must be >= this
	MaxStaleRatio     float64 // stale claims / total must be <= this
}

// DefaultSLOs is the baseline SLO configuration for production rollouts.
var DefaultSLOs = SLOThresholds{
	MinRecallPassRate: 0.95, // "never wrong on recall" = 95%+
	MinConfidenceAvg:  0.6,  // reasonably confident answers
	MaxStaleRatio:     0.1,  // at most 10% stale claims
}

// CheckSLOs evaluates the current memory quality against SLO thresholds.
// Returns a list of violation strings (empty when all SLOs pass).
func (e Engine) CheckSLOs(ctx context.Context, slo SLOThresholds) ([]string, error) {
	metrics, err := e.ComputeMemoryQuality(ctx)
	if err != nil {
		return nil, fmt.Errorf("check SLOs: %w", err)
	}
	var violations []string

	// Recall pass rate is only meaningful when we have benchmark results.
	// This is a placeholder — in practice, the benchmark harness
	// writes results to benchmarks/results/ and CI checks them.
	// The SLO check here focuses on memory-store health.

	if metrics.TotalClaims > 0 {
		staleRatio := float64(metrics.StaleCount) / float64(metrics.TotalClaims)
		if staleRatio > slo.MaxStaleRatio {
			violations = append(violations,
				fmt.Sprintf("stale_ratio %.2f > %.2f (stale: %d / total: %d)",
					staleRatio, slo.MaxStaleRatio, metrics.StaleCount, metrics.TotalClaims))
		}
	}

	if metrics.AvgConfidence < slo.MinConfidenceAvg {
		violations = append(violations,
			fmt.Sprintf("avg_confidence %.3f < %.3f", metrics.AvgConfidence, slo.MinConfidenceAvg))
	}

	return violations, nil
}
