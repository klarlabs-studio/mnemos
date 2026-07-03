package mnemos

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/felixgeelhaar/chronos"
	"github.com/felixgeelhaar/chronos/embed"
	"github.com/google/uuid"
	axidomain "go.klarlabs.de/axi/domain"
	"go.klarlabs.de/bolt"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/embedding"
	"go.klarlabs.de/mnemos/internal/kernel"
	"go.klarlabs.de/mnemos/internal/llm"
	"go.klarlabs.de/mnemos/internal/pipeline"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/query"
	"go.klarlabs.de/mnemos/internal/relate"
	"go.klarlabs.de/mnemos/internal/store"
	"go.klarlabs.de/mnemos/internal/trust"
	"go.klarlabs.de/mnemos/providers"
)

// chronosEventNamespace is the deterministic UUID namespace used when
// hashing string ids (run id, event type) into uuid.UUID for Chronos
// EntityState identifiers. Stable across processes so the same
// (RunID, Type) tuple always maps to the same Chronos series.
var chronosEventNamespace = uuid.NewSHA1(uuid.NameSpaceURL, []byte("mnemos://events"))

// memory is the concrete implementation of [Memory] returned by [New].
type memory struct {
	conn    *store.Conn
	actorID string
	info    Info   // immutable runtime config snapshot, exposed via Info()
	cfg     config // retained so Tenant() can re-run the New assembly
	dsn     string // resolved storage DSN, tenant-rewritten by Tenant()
	// tenant is the tenant id this view is scoped to, read from the DSN's
	// `tenant=` param; it is "" for an unscoped store (no tenant param), which
	// preserves the legacy chronos keys. When non-empty it is mixed into the
	// chronos EntityID/ScopeID so a SINGLE shared chronos engine keeps tenants
	// isolated — chronos scopes by entity/scope, not tenant, so without this
	// two tenants' temporal signals would merge.
	tenant    string
	extractor *pipeline.Extractor
	relator   relate.Engine
	query     query.Engine
	embedder  embedding.Client

	// chronos is the bundled or user-supplied temporal engine. Always
	// non-nil after a successful [New].
	chronos *embed.Engine
	// chronosOwned is true when [New] constructed the engine itself
	// and is responsible for closing it. False when the caller
	// supplied one via [WithChronos].
	chronosOwned bool

	// wkernel is the in-process governed axi kernel every public write
	// routes through. Built unconditionally by [New] — there is no
	// direct-write fallback, so no write can bypass the evidence chain
	// + budget. See governed_writes.go.
	wkernel *kernel.Governed
	// logger backs the kernel's domain-event log. Nil keeps axi chatter
	// off stderr (the library default); cmd/mnemos can supply its own.
	logger *bolt.Logger

	// lastSession holds the axi session recorded by the most recent
	// public write, exposed via [Memory.LastWriteSession]. Guarded by
	// sessionMu for concurrent writers.
	sessionMu   sync.RWMutex
	lastSession *axidomain.ExecutionSession

	// tokenBudget is the cumulative language-model token budget across all
	// writes on this Memory (set via [WithTokenBudget]). Zero means no
	// cumulative cap. tokensSpent accumulates the per-write token spend
	// reported on each session's "llm" evidence record; once it reaches
	// the budget, further writes are rejected pre-execution. Atomic so
	// concurrent writers account safely.
	tokenBudget int64
	tokensSpent atomic.Int64
}

var _ Memory = (*memory)(nil)

// setLastSession records s as the session of the most recent write.
func (m *memory) setLastSession(s *axidomain.ExecutionSession) {
	m.sessionMu.Lock()
	m.lastSession = s
	m.sessionMu.Unlock()
}

// LastWriteSession implements [Memory.LastWriteSession]. Returns nil
// before any write has been performed.
func (m *memory) LastWriteSession() WriteSession {
	m.sessionMu.RLock()
	s := m.lastSession
	m.sessionMu.RUnlock()
	if s == nil {
		return nil
	}
	return newWriteSession(s)
}

// Remember implements [Memory.Remember]. The write routes through the
// axi kernel: this method validates input and builds the invocation; the
// rememberExecutor performs the ingest -> extract -> relate -> persist
// pipeline and records the evidence chain. See governed_writes.go.
func (m *memory) Remember(ctx context.Context, item Item) error {
	if strings.TrimSpace(item.Content) == "" {
		return errors.New("mnemos: Remember: Content is required")
	}
	_, err := dispatchWrite[rememberOutput](ctx, m, actionRemember, rememberInput{Item: item})
	return err
}

// RememberClaim implements [Memory.RememberClaim]. The write routes
// through the axi kernel; the rememberClaimExecutor persists the claim
// (and any evidence links) and records the claim id in the evidence
// chain. The generated claim id is returned to the caller.
func (m *memory) RememberClaim(ctx context.Context, item ClaimItem) (string, error) {
	if strings.TrimSpace(item.Text) == "" {
		return "", errors.New("mnemos: RememberClaim: Text is required")
	}
	// Spec non-negotiable: "Claims require evidence. An assertion without
	// evidence is rejected." Fail-closed here, before the governed write
	// runs, so no evidence-less claim is ever persisted. Blank ids do not
	// count as evidence.
	if !hasEvidence(item.EventIDs) {
		return "", errors.New("mnemos: RememberClaim: at least one evidence EventID is required (claims require evidence)")
	}
	out, err := dispatchWrite[rememberClaimOutput](ctx, m, actionRememberClaim, rememberClaimInput{Claim: item})
	if err != nil {
		return "", err
	}
	m.embedAsync(out.ClaimID, "claim", item.Text)
	return out.ClaimID, nil
}

// RecordDecision persists an agent decision (belief -> plan -> outcome audit
// record) through the governed write path and returns its id. Statement is
// required; RiskLevel defaults to "medium" when empty. This is the public
// entry point for the decision audit trail that [Memory.AuditTrail]-style
// consumers read back via [Memory.GetDecision] / [Memory.ListDecisions].
func (m *memory) RecordDecision(ctx context.Context, d Decision) (string, error) {
	if strings.TrimSpace(d.Statement) == "" {
		return "", errors.New("mnemos: RecordDecision: Statement is required")
	}
	out, err := dispatchWrite[recordDecisionOutput](ctx, m, actionRecordDecision, recordDecisionInput{Decision: d})
	if err != nil {
		return "", err
	}
	// Embed the decision so it is semantically recallable alongside events and
	// claims (same async, best-effort embed-on-write path).
	m.embedAsync(out.DecisionID, "decision", d.Statement+" "+d.Reasoning)
	return out.DecisionID, nil
}

// SetClaimLifecycle implements [Memory.SetClaimLifecycle].
func (m *memory) SetClaimLifecycle(ctx context.Context, claimID string, lifecycle ClaimLifecycle) error {
	if strings.TrimSpace(claimID) == "" {
		return errors.New("mnemos: SetClaimLifecycle: claimID is required")
	}
	_, err := dispatchWrite[setClaimLifecycleOutput](ctx, m, actionSetClaimLifecycle, setClaimLifecycleInput{
		ClaimID:   claimID,
		Lifecycle: lifecycle,
	})
	return err
}

// GetDecision returns the decision with the given id, or a not-found error.
func (m *memory) GetDecision(ctx context.Context, id string) (Decision, error) {
	if strings.TrimSpace(id) == "" {
		return Decision{}, errors.New("mnemos: GetDecision: id is required")
	}
	d, err := m.conn.Decisions.GetByID(ctx, id)
	if err != nil {
		return Decision{}, fmt.Errorf("mnemos: GetDecision: %w", err)
	}
	return toPublicDecision(d), nil
}

// ListDecisions returns recorded decisions (most-recent first), capped at
// limit (0 = no cap). It is the read side of the decision audit trail.
func (m *memory) ListDecisions(ctx context.Context, limit int) ([]Decision, error) {
	all, err := m.conn.Decisions.ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("mnemos: ListDecisions: %w", err)
	}
	out := make([]Decision, 0, len(all))
	for _, d := range all {
		out = append(out, toPublicDecision(d))
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Recall implements [Memory.Recall].
func (m *memory) Recall(ctx context.Context, q Query) ([]Result, error) {
	if strings.TrimSpace(q.Text) == "" {
		return nil, errors.New("mnemos: Recall: Text is required")
	}

	opts := query.AnswerOptions{
		Hops:           q.Hops,
		AsOf:           q.AsOf,
		RecordedAsOf:   q.RecordedAsOf,
		IncludeHistory: q.IncludeHistory,
		Lifecycle:      domain.ClaimLifecycle(q.Lifecycle),
	}

	var ans domain.Answer
	var err error
	if q.RunID != "" {
		ans, err = m.query.AnswerForRunWithOptions(q.Text, q.RunID, opts)
	} else {
		ans, err = m.query.AnswerWithOptions(q.Text, opts)
	}
	if err != nil {
		return nil, fmt.Errorf("mnemos: answer: %w", err)
	}

	results := make([]Result, 0, len(ans.Claims))
	for _, c := range ans.Claims {
		results = append(results, Result{
			ClaimID:     c.ID,
			Text:        c.Text,
			Type:        string(c.Type),
			Confidence:  c.Confidence,
			TrustScore:  c.TrustScore,
			HopDistance: ans.ClaimHopDistance[c.ID],
			Provenance:  ans.ClaimProvenance[c.ID],
		})
	}
	if q.Limit > 0 && len(results) > q.Limit {
		results = results[:q.Limit]
	}
	// Suppress unused-context: the underlying engine is sync today; ctx
	// is reserved for future cancellation propagation through the
	// query.Engine surface.
	_ = ctx
	return results, nil
}

// Get implements [Memory.Get]. Exact lookup by claim id. Returns a
// not-found error when the id is unknown.
func (m *memory) Get(ctx context.Context, claimID string) (Claim, error) {
	id := strings.TrimSpace(claimID)
	if id == "" {
		return Claim{}, errors.New("mnemos: Get: claimID is required")
	}
	claims, err := m.conn.Claims.ListByIDs(ctx, []string{id})
	if err != nil {
		return Claim{}, fmt.Errorf("mnemos: Get: %w", err)
	}
	for _, c := range claims {
		if c.ID == id {
			return toPublicClaim(c), nil
		}
	}
	return Claim{}, fmt.Errorf("mnemos: Get: claim %q not found", id)
}

// Scan implements [Memory.Scan]. Returns claims whose valid-time interval
// overlaps the [ScanQuery] window, ordered by ValidFrom, honouring Limit.
func (m *memory) Scan(ctx context.Context, q ScanQuery) ([]Claim, error) {
	all, err := m.conn.Claims.ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("mnemos: Scan: %w", err)
	}
	out := make([]Claim, 0, len(all))
	for _, c := range all {
		if !claimOverlapsWindow(c, q.ValidFrom, q.ValidUntil) {
			continue
		}
		out = append(out, toPublicClaim(c))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ValidFrom.Before(out[j].ValidFrom) })
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

// claimOverlapsWindow reports whether a claim's valid-time interval
// [ValidFrom, ValidTo) overlaps the [from, until) window. Zero bounds on
// either side act as open (unbounded) edges. A claim with a zero ValidFrom
// is treated as "valid since before tracking began" (negative infinity);
// a zero ValidTo means "still valid" (positive infinity).
func claimOverlapsWindow(c domain.Claim, from, until time.Time) bool {
	// Claim ends before the window starts → no overlap.
	if !from.IsZero() && !c.ValidTo.IsZero() && !c.ValidTo.After(from) {
		return false
	}
	// Claim starts at/after the window ends → no overlap.
	if !until.IsZero() && !c.ValidFrom.IsZero() && !c.ValidFrom.Before(until) {
		return false
	}
	return true
}

// toPublicClaim projects an internal domain.Claim into the stable public
// [Claim] read shape. ValidTo's zero value ("still valid") maps to a nil
// ValidUntil; CreatedAt maps to the transaction-time RecordedAt.
func toPublicClaim(c domain.Claim) Claim {
	out := Claim{
		ID:         c.ID,
		Statement:  c.Text,
		Type:       string(c.Type),
		Confidence: c.Confidence,
		TrustScore: c.TrustScore,
		ValidFrom:  c.ValidFrom,
		RecordedAt: c.CreatedAt,
		Lifecycle:  ClaimLifecycle(c.Lifecycle),
	}
	if !c.ValidTo.IsZero() {
		vt := c.ValidTo
		out.ValidUntil = &vt
	}
	return out
}

// toPublicDecision maps the internal domain decision onto the public shape.
func toPublicDecision(d domain.Decision) Decision {
	return Decision{
		ID:           d.ID,
		Statement:    d.Statement,
		Plan:         d.Plan,
		Reasoning:    d.Reasoning,
		RiskLevel:    string(d.RiskLevel),
		Beliefs:      d.Beliefs,
		Alternatives: d.Alternatives,
		OutcomeID:    d.OutcomeID,
		ChosenAt:     d.ChosenAt,
		CreatedBy:    d.CreatedBy,
		CreatedAt:    d.CreatedAt,
	}
}

// RememberEvent implements [Memory.RememberEvent]. The write routes
// through the axi kernel; the rememberEventExecutor appends the event,
// forwards it to the bundled Chronos engine, and records the event id in
// the evidence chain. The action is idempotent on event id.
func (m *memory) RememberEvent(ctx context.Context, e Event) error {
	if e.At.IsZero() {
		return errors.New("mnemos: RememberEvent: At is required")
	}
	if strings.TrimSpace(e.Content) == "" {
		return errors.New("mnemos: RememberEvent: Content is required")
	}
	e.ID = strings.TrimSpace(e.ID)
	out, err := dispatchWrite[rememberEventOutput](ctx, m, actionRememberEvent, rememberEventInput{Event: e})
	if err != nil {
		return err
	}
	m.embedAsync(out.EventID, "event", e.Content)
	return nil
}

// embedAsync embeds text and stores the vector for (entityID, entityType) in the
// BACKGROUND, so recall can rank it semantically without adding an embedding
// round-trip to the write path. It is:
//
//   - opt-in: a no-op unless an embedder is configured (WithSharedProvider /
//     WithEnhancedMode) and the backend has an embeddings repository;
//   - best-effort: it never blocks or fails the originating write — an embedding
//     is derived, recomputable state (this mirrors [pipeline.GenerateEmbeddings],
//     which also Upserts embeddings directly rather than through the write kernel);
//   - idempotent: the upsert is keyed on (entity_id, entity_type), so a re-embed
//     (model change, backfill) overwrites cleanly.
func (m *memory) embedAsync(entityID, entityType, text string) {
	if m.embedder == nil || m.conn == nil || m.conn.Embeddings == nil {
		return
	}
	if entityID == "" || strings.TrimSpace(text) == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		vectors, err := m.embedder.Embed(ctx, []string{text})
		if err != nil || len(vectors) == 0 || len(vectors[0]) == 0 {
			if m.logger != nil {
				m.logger.Ctx(ctx).Warn().Err(err).Str("entity_id", entityID).Str("entity_type", entityType).Msg("mnemos: async embed skipped")
			}
			return
		}
		if err := m.conn.Embeddings.Upsert(ctx, entityID, entityType, vectors[0], embedding.ModelIDOf(m.embedder), m.actorID); err != nil {
			if m.logger != nil {
				m.logger.Ctx(ctx).Warn().Err(err).Str("entity_id", entityID).Str("entity_type", entityType).Msg("mnemos: async embed upsert failed")
			}
		}
	}()
}

// eventToEntityState maps a public mnemos.Event into a chronos.EntityState
// suitable for the bundled in-process engine. See [memory.RememberEvent]
// for the mapping rationale.
func (m *memory) eventToEntityState(e Event, eventID string) chronos.EntityState {
	typ := strings.TrimSpace(e.Type)
	if typ == "" {
		typ = "untyped"
	}
	runID := e.RunID
	if runID == "" {
		runID = "default"
	}
	meta := map[string]string{
		"mnemos_event_id": eventID,
		"event_type":      typ,
	}
	if c := strings.TrimSpace(e.Content); c != "" {
		if len(c) > 200 {
			c = c[:200]
		}
		meta["content_preview"] = c
	}
	// Tenant-scope the entity + scope keys so a single shared chronos engine
	// never merges two tenants' series (chronos isolates by entity/scope, not
	// tenant). Tenant ids are NUL-free (validated by tenantRE in Tenant()), so
	// the NUL separator unambiguously delimits the tenant prefix from the
	// value — a tenant-prefixed key can never equal an unscoped key.
	entityKey := typ
	if m.tenant != "" {
		entityKey = m.tenant + "\x00" + typ
		meta["tenant"] = m.tenant
	}
	// Use the event's real numeric features when present so Chronos detects
	// patterns on actual VALUES; fall back to the presence default (a single
	// 1.0) so cadence is still tracked for presence-only events.
	features := e.Features
	if len(features) == 0 {
		features = []float64{1.0}
	}
	return chronos.EntityState{
		ID:        uuid.New(),
		EntityID:  uuid.NewSHA1(chronosEventNamespace, []byte(entityKey)),
		ScopeID:   m.chronosScopeID(runID),
		Timestamp: e.At.UTC(),
		Features:  features,
		Labels:    []string{"event"},
		Meta:      meta,
	}
}

// isValidClaimType reports whether t is acceptable for a write: a built-in
// claim type, or one the consumer registered via WithClaimTypes. This is the
// configurable-vocabulary check the write executor enforces at the boundary.
func (m *memory) isValidClaimType(t domain.ClaimType) bool {
	if domain.IsBuiltinClaimType(t) {
		return true
	}
	for _, extra := range m.cfg.extraClaimTypes {
		if domain.ClaimType(extra) == t {
			return true
		}
	}
	return false
}

// chronosScopeID derives the Chronos scope UUID for a run, applying the same
// tenant mix + default-run rule eventToEntityState uses to STORE events — so
// [Memory.Signals] reads signals from the exact scope events were written under.
func (m *memory) chronosScopeID(runID string) uuid.UUID {
	if runID == "" {
		runID = "default"
	}
	scopeKey := runID
	if m.tenant != "" {
		scopeKey = m.tenant + "\x00" + runID
	}
	return uuid.NewSHA1(chronosEventNamespace, []byte(scopeKey))
}

// Timeline implements [Memory.Timeline].
func (m *memory) Timeline(ctx context.Context, q TimelineQuery) ([]Event, error) {
	var events []domain.Event
	var err error
	if q.RunID != "" {
		events, err = m.conn.Events.ListByRunID(ctx, q.RunID)
	} else {
		events, err = m.conn.Events.ListAll(ctx)
	}
	if err != nil {
		return nil, fmt.Errorf("mnemos: list events: %w", err)
	}

	out := make([]Event, 0, len(events))
	for _, ev := range events {
		if !q.From.IsZero() && ev.Timestamp.Before(q.From) {
			continue
		}
		if !q.To.IsZero() && ev.Timestamp.After(q.To) {
			continue
		}
		typ := ev.Metadata["event_type"]
		if len(q.Types) > 0 && !slices.Contains(q.Types, typ) {
			continue
		}
		out = append(out, Event{
			ID:       ev.ID,
			At:       ev.Timestamp,
			Type:     typ,
			Content:  ev.Content,
			Metadata: copyMetaWithout(ev.Metadata, "event_type"),
			RunID:    ev.RunID,
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })

	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

// Signals implements [Memory.Signals]. It asks the bundled Chronos engine to
// detect temporal patterns over the run's scope (recomputing from the stored
// event series, so the read reflects everything ingested so far), then projects
// them onto the public [Signal] shape. No Chronos wired, or nothing detected,
// yields (nil, nil) — an empty result means "all quiet", never an error.
func (m *memory) Signals(ctx context.Context, q SignalQuery) ([]Signal, error) {
	if m.chronos == nil {
		return nil, nil
	}
	scopeID := m.chronosScopeID(q.RunID)
	detected, err := m.chronos.Detect(ctx, []uuid.UUID{scopeID})
	if err != nil {
		return nil, fmt.Errorf("mnemos: detect signals: %w", err)
	}
	runID := q.RunID
	if runID == "" {
		runID = "default"
	}
	out := make([]Signal, 0, len(detected))
	for _, s := range detected {
		if q.MinConfidence > 0 && s.Confidence < q.MinConfidence {
			continue
		}
		out = append(out, Signal{
			Pattern:     string(s.Pattern),
			RunID:       runID,
			Strength:    s.Strength,
			Confidence:  s.Confidence,
			Class:       string(s.ConfidenceClass),
			DetectedAt:  s.DetectedAt,
			WindowStart: s.Window.Start,
			WindowEnd:   s.Window.End,
		})
		if q.Limit > 0 && len(out) == q.Limit {
			break
		}
	}
	return out, nil
}

// Consolidate implements [Memory.Consolidate] — the maintenance "sleep" pass.
func (m *memory) Consolidate(ctx context.Context, opts ConsolidateOptions) (ConsolidateResult, error) {
	threshold := opts.DedupeThreshold
	if threshold <= 0 {
		threshold = 0.92
	}
	plan, err := pipeline.PlanSemanticDedupe(ctx, m.conn, threshold)
	if err != nil {
		return ConsolidateResult{}, fmt.Errorf("mnemos: consolidate: plan dedupe: %w", err)
	}
	res := ConsolidateResult{ClaimsScanned: plan.ClaimsScanned, DuplicateGroups: len(plan.Merges)}
	if opts.DryRun || len(plan.Merges) == 0 {
		return res, nil
	}
	merged, err := pipeline.ApplySemanticDedupe(ctx, m.conn, plan)
	if err != nil {
		return res, fmt.Errorf("mnemos: consolidate: apply dedupe: %w", err)
	}
	res.Merged = merged
	// Merging changed evidence counts, so recompute trust (confidence ×
	// corroboration × freshness) — the renormalisation half of the sleep pass.
	// Best-effort: a scorer-less backend still consolidated correctly.
	if scorer, ok := m.conn.Claims.(ports.TrustScorer); ok {
		now := time.Now().UTC()
		if n, terr := scorer.RecomputeTrust(ctx, func(confidence float64, evidenceCount int, latestEvidence time.Time) float64 {
			return trust.Score(confidence, evidenceCount, latestEvidence, now)
		}); terr == nil {
			res.TrustRefreshed = n
		}
	}
	return res, nil
}

// Close implements [Memory.Close].
func (m *memory) Close() error {
	var firstErr error
	if m.conn != nil {
		if err := m.conn.Close(); err != nil {
			firstErr = err
		}
		m.conn = nil
	}
	if m.chronos != nil && m.chronosOwned {
		if err := m.chronos.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	m.chronos = nil
	// Flush + close the kernel's durable evidence log (no-op when no
	// MNEMOS_AXI_EVIDENCE_LOG / WithEvidenceLog path was configured).
	if m.wkernel != nil {
		if err := m.wkernel.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// mergeMetadata produces the input metadata map for ingest.IngestText
// from an Item. The "source" key follows the same convention as the CLI
// (defaults to "raw_text" when none supplied).
func mergeMetadata(item Item) map[string]string {
	out := map[string]string{}
	maps.Copy(out, item.Metadata)
	if item.Source != "" {
		out["source"] = item.Source
	}
	return out
}

func copyMetaWithout(in map[string]string, skip string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		if k == skip {
			continue
		}
		out[k] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// newEventID returns a fresh, collision-resistant event id. Uses a
// random UUID rather than a timestamp so concurrent writes can't mint the
// same id (the same scheme internal/parser uses for its event ids).
func newEventID() string {
	return "ev_" + uuid.NewString()
}

// hasEvidence reports whether eventIDs contains at least one non-blank
// evidence reference. Whitespace-only ids do not count — the spec
// requires every stored claim to reference real backing evidence.
func hasEvidence(eventIDs []string) bool {
	return len(nonBlank(eventIDs)) > 0
}

// nonBlank returns the trimmed, non-empty entries of in, preserving order.
// Used to normalise evidence EventIDs before persistence.
func nonBlank(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// newClaimID returns a fresh, collision-resistant claim id. UUID-based
// for the same reason as [newEventID]: a UnixNano timestamp collides when
// two goroutines call RememberClaim within the same nanosecond.
func newClaimID() string {
	return "cl_" + uuid.NewString()
}

// newDecisionID returns a fresh, collision-resistant decision id.
func newDecisionID() string {
	return "dec_" + uuid.NewString()
}

// textGenAdapter wraps a [providers.TextGenerator] to satisfy the
// internal llm.Client contract Mnemos's engines consume.
type textGenAdapter struct {
	tg providers.TextGenerator
}

func newTextGenAdapter(tg providers.TextGenerator) llm.Client {
	return &textGenAdapter{tg: tg}
}

// Complete satisfies llm.Client by translating between the internal
// message shape and the public providers.TextGenerator surface.
func (a *textGenAdapter) Complete(ctx context.Context, messages []llm.Message) (llm.Response, error) {
	in := providers.GenerateTextInput{
		Messages: make([]providers.Message, len(messages)),
	}
	for i, m := range messages {
		in.Messages[i] = providers.Message{
			Role:    providers.Role(string(m.Role)),
			Content: m.Content,
		}
	}
	out, err := a.tg.GenerateText(ctx, in)
	if err != nil {
		return llm.Response{}, err
	}
	return llm.Response{
		Content:      out.Content,
		Model:        out.Model,
		InputTokens:  out.InputTokens,
		OutputTokens: out.OutputTokens,
	}, nil
}

// embedderAdapter wraps a [providers.Embedder] to satisfy the internal
// embedding.Client contract.
type embedderAdapter struct {
	e providers.Embedder
}

func newEmbedderAdapter(e providers.Embedder) embedding.Client {
	return &embedderAdapter{e: e}
}

// Embed satisfies embedding.Client by delegating to the public
// providers.Embedder.
func (a *embedderAdapter) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out, err := a.e.Embed(ctx, providers.EmbedInput{Texts: texts})
	if err != nil {
		return nil, err
	}
	return out.Vectors, nil
}

// ModelID satisfies embedding.ModelIdentifier by forwarding the wrapped
// provider's model id when it reports one (providers.ModelIdentifier), so
// recall can stamp + filter by model. Empty when the provider is unnamed.
func (a *embedderAdapter) ModelID() string {
	if mi, ok := a.e.(providers.ModelIdentifier); ok {
		return mi.ModelID()
	}
	return ""
}
