// Package consolidate implements ADR 0011 Phase B: the systems-consolidation
// pass that bridges per-tenant memory (the "hippocampus") to the shared global
// brain (the "neocortex").
//
// The unit of consolidation is the synthesized [domain.Lesson]. Each tenant's
// synthesis layer (internal/synthesize) independently distils Action→Outcome
// chains into Lessons inside its own isolated namespace (ADR 0007). This pass
// takes those per-tenant Lesson sets and promotes ONLY the safe, generalizable
// subset to the global tier — the small set of operational truths that many
// tenants discovered independently.
//
// The engine is deliberately pure and I/O-free: the caller supplies per-tenant
// Lessons (as [TenantLessons]) and, optionally, the existing global knowledge
// and prediction-error signal behind small interfaces. That keeps every gate
// unit-testable without a live multi-tenant database and keeps the privacy
// guarantee (a lesson seen in only one tenant NEVER promotes) verifiable in a
// single deterministic test — see TestPromotion_NoSingleTenantLeak.
//
// Gates, applied strictly in order:
//
//  1. Cross-tenant corroboration — a candidate must have been produced
//     independently in ≥ MinTenants distinct tenants. This is simultaneously
//     the QUALITY signal (many tenants learned the same thing) and the PRIVACY
//     gate (a single-tenant lesson cannot leak, because it never becomes a
//     candidate).
//  2. De-identification — the promoted schema keeps only the generalized
//     statement, the corroboration count, and abstracted evidence counts; it
//     carries no tenant id, no raw event text, no per-tenant evidence ids. If a
//     statement contains a token we cannot safely strip, the candidate is
//     dropped (fail closed).
//  3. Contradiction — a candidate that contradicts a vetted global claim is not
//     promoted silently; it is routed to Dissonant for operator / next-cycle
//     resolution.
//  4. Prediction-error ranking — surviving candidates are ranked by aggregate
//     surprise (from domain.Expectation) of the decisions/claims backing them,
//     highest first (learn hardest where the agent was most surprised).
//     Candidates without expectation data fall back to corroboration count.
//  5. Gate policy — auto promotes candidates above the confidence threshold
//     immediately; operator (the default, safe for regulated domains) emits
//     them as Pending, requiring explicit approval before any global write.
package consolidate

import (
	"context"
	"sort"
	"strings"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/relate"
)

// GatePolicy selects how candidates that clear every quality/privacy gate are
// released to the global tier.
type GatePolicy string

const (
	// GateOperator emits cleared candidates as Pending — they require an
	// explicit human approval before any global write. This is the default
	// because promotion into a shared brain is irreversible-ish and the safe
	// posture for regulated domains is human-in-the-loop.
	GateOperator GatePolicy = "operator"
	// GateAuto promotes cleared candidates whose confidence is at or above the
	// threshold immediately, with no human step.
	GateAuto GatePolicy = "auto"
)

// DefaultMinTenants is the corroboration/privacy threshold: a lesson must be
// independently produced in at least this many distinct tenants to become a
// promotion candidate. Three is the smallest count that meaningfully rules out
// idiosyncratic single- or two-tenant folklore while remaining reachable on a
// modestly sized federation.
const DefaultMinTenants = 3

// DefaultEquivalenceOverlap is the normalized-token-overlap ratio (intersection
// over the smaller token set) at which two lesson statements are treated as
// "the same lesson" for cross-tenant corroboration. Tuned to cluster genuine
// paraphrases without collapsing distinct operational truths.
const DefaultEquivalenceOverlap = 0.6

// Options tunes a promotion pass. The zero value is usable and reproduces the
// project defaults via withDefaults.
type Options struct {
	// MinTenants is the cross-tenant corroboration threshold (gate 1).
	// Defaults to DefaultMinTenants.
	MinTenants int
	// Gate selects auto vs operator release (gate 5). Defaults to GateOperator.
	Gate GatePolicy
	// AutoConfidence is the confidence floor a candidate must reach to be
	// promoted immediately under GateAuto. Candidates below it are skipped with
	// a reason rather than promoted. Defaults to domain.LessonConfidenceMin.
	AutoConfidence float64
	// EquivalenceOverlap is the statement-equivalence threshold used to cluster
	// lessons across tenants. Defaults to DefaultEquivalenceOverlap.
	EquivalenceOverlap float64
	// SensitiveTokens is an operator-supplied denylist of tokens/substrings that
	// must never appear in a promoted statement (customer names, PII markers,
	// internal codenames). Matched case-insensitively as substrings. The tenant
	// identifiers themselves are always added to this set automatically.
	SensitiveTokens []string
}

func (o Options) withDefaults() Options {
	if o.MinTenants <= 0 {
		o.MinTenants = DefaultMinTenants
	}
	if o.Gate == "" {
		o.Gate = GateOperator
	}
	if o.AutoConfidence == 0 {
		o.AutoConfidence = domain.LessonConfidenceMin
	}
	if o.EquivalenceOverlap == 0 {
		o.EquivalenceOverlap = DefaultEquivalenceOverlap
	}
	return o
}

// TenantLessons is one tenant's set of synthesized lessons, supplied by the
// caller. Tenant is an opaque identifier (namespace, tenant id) — it is used
// only to count distinct corroborating tenants and to seed the de-identification
// denylist; it never appears in any promoted output.
type TenantLessons struct {
	Tenant  string
	Lessons []domain.Lesson
}

// PromotedLesson is a de-identified, generalized lesson ready for (or pending
// approval into) the global tier. It carries NO tenant identifier, NO raw event
// text, and NO per-tenant evidence ids — only aggregate counts. This is the
// only shape that ever crosses the tenant→global boundary.
type PromotedLesson struct {
	// Statement is the generalized, de-identified lesson text.
	Statement string
	// Scope is the operational scope shared by the corroborating lessons
	// (service/env/team). It is retained only when it is identical across the
	// corroborating tenants — a shared "payments" scope is a generalization, not
	// a tenant secret. A divergent scope is cleared to empty.
	Scope domain.Scope
	// Polarity carries the positive/anti-lesson sense of the generalization.
	Polarity domain.LessonPolarity
	// DistinctTenants is how many distinct tenants independently produced an
	// equivalent lesson — the corroboration count. Always ≥ MinTenants for a
	// promoted/pending candidate.
	DistinctTenants int
	// EvidenceCount is the abstracted, aggregate count of corroborating pieces
	// of evidence across all contributing tenants (sum of per-lesson evidence
	// lengths). It is a magnitude, never a list of ids.
	EvidenceCount int
	// Confidence is the representative (max) confidence among the corroborating
	// lessons.
	Confidence float64
	// Surprise is the aggregate prediction-error backing the generalization
	// (sum of per-lesson surprise from domain.Expectation). Zero when no
	// expectation data was available; HasSurprise disambiguates.
	Surprise    float64
	HasSurprise bool
}

// DissonantCandidate is a candidate that cleared corroboration and
// de-identification but contradicts a vetted global claim. It is surfaced for
// operator / next-cycle resolution instead of being promoted or dropped
// silently.
type DissonantCandidate struct {
	Candidate PromotedLesson
	// ConflictsWith is the text of the vetted global claim the candidate
	// contradicts (the first detected conflict).
	ConflictsWith string
}

// SkippedCandidate records a lesson (or lesson group) that did not promote, with
// a machine-stable reason, so a promotion pass is fully auditable.
type SkippedCandidate struct {
	Statement string
	// DistinctTenants is the corroboration count observed for the group (0/1 for
	// single-tenant skips).
	DistinctTenants int
	Reason          string
}

// Skip reasons (machine-stable).
const (
	ReasonInsufficientCorroboration = "insufficient cross-tenant corroboration"
	ReasonTenantSpecificToken       = "statement contains tenant-specific or sensitive token"
	ReasonBelowAutoConfidence       = "below auto-promote confidence threshold"
)

// Result is the structured, auditable output of a promotion pass. Every input
// lesson group lands in exactly one bucket.
type Result struct {
	// Promoted are candidates released to the global tier (GateAuto, above
	// threshold). Ordered by prediction-error ranking, highest surprise first.
	Promoted []PromotedLesson
	// Pending are candidates awaiting operator approval (GateOperator, or
	// GateAuto candidates that are not below threshold are still promoted; only
	// operator mode fills this). Ordered by the same ranking.
	Pending []PromotedLesson
	// Dissonant are candidates that contradict vetted global knowledge.
	Dissonant []DissonantCandidate
	// Skipped are groups dropped by a gate, each with a reason.
	Skipped []SkippedCandidate
}

// GlobalKnowledge supplies the existing vetted global (neocortex) claims that
// candidates are checked against for contradiction (gate 3). A nil
// GlobalKnowledge (or one returning no claims) means "no global knowledge yet"
// — nothing can be dissonant.
type GlobalKnowledge interface {
	VettedClaims(ctx context.Context) ([]domain.Claim, error)
}

// SurpriseSource supplies the aggregate prediction-error (surprise) that backs a
// lesson, sourced from domain.Expectation reconciliation. It returns the
// surprise scalar and whether any expectation data existed for the lesson. A nil
// SurpriseSource means candidates rank purely by corroboration count.
type SurpriseSource interface {
	SurpriseFor(ctx context.Context, lesson domain.Lesson) (surprise float64, hasData bool)
}

// Promoter runs the promotion pass. It is safe for concurrent use; it holds no
// mutable state.
type Promoter struct {
	global   GlobalKnowledge
	surprise SurpriseSource
	relate   relate.Engine
}

// NewPromoter builds a Promoter. Either dependency may be nil: a nil
// GlobalKnowledge disables the contradiction gate (nothing is dissonant), and a
// nil SurpriseSource makes ranking fall back to corroboration count.
func NewPromoter(global GlobalKnowledge, surprise SurpriseSource) *Promoter {
	return &Promoter{
		global:   global,
		surprise: surprise,
		relate:   relate.NewEngine(),
	}
}

// group is an internal cluster of equivalent lessons discovered across tenants.
type group struct {
	repTokens  map[string]struct{} // representative token set (first member)
	statement  string              // representative statement (highest confidence)
	scope      domain.Scope
	scopeSet   bool
	scopeDiv   bool // scope diverged across members
	polarity   domain.LessonPolarity
	tenants    map[string]struct{}
	confidence float64
	evidence   int
	surprise   float64
	hasSurp    bool
	members    []domain.Lesson
}

// Promote runs the five gates over the supplied per-tenant lessons and returns a
// structured, auditable result.
func (p *Promoter) Promote(ctx context.Context, tenants []TenantLessons, opts Options) (Result, error) {
	opts = opts.withDefaults()

	// Build the de-identification denylist: operator-supplied sensitive tokens
	// plus every tenant identifier (a tenant's own id must never surface).
	denylist := buildDenylist(opts.SensitiveTokens, tenants)

	// Gate 1 — cluster equivalent lessons across tenants and count distinct
	// corroborating tenants.
	groups := p.cluster(ctx, tenants, opts.EquivalenceOverlap)

	// Fetch vetted global claims once for the contradiction gate.
	var globalClaims []domain.Claim
	if p.global != nil {
		var err error
		globalClaims, err = p.global.VettedClaims(ctx)
		if err != nil {
			return Result{}, err
		}
	}

	var res Result
	var cleared []PromotedLesson

	for _, g := range groups {
		distinct := len(g.tenants)

		// Gate 1: corroboration / privacy. A lesson in fewer than MinTenants
		// distinct tenants can NEVER promote — this is the no-leak guarantee.
		if distinct < opts.MinTenants {
			res.Skipped = append(res.Skipped, SkippedCandidate{
				Statement:       g.statement,
				DistinctTenants: distinct,
				Reason:          ReasonInsufficientCorroboration,
			})
			continue
		}

		// Gate 2: de-identification. Fail closed if the statement carries a
		// token we cannot safely strip.
		clean, ok := Deidentify(g.statement, denylist)
		if !ok {
			res.Skipped = append(res.Skipped, SkippedCandidate{
				Statement:       g.statement,
				DistinctTenants: distinct,
				Reason:          ReasonTenantSpecificToken,
			})
			continue
		}

		cand := PromotedLesson{
			Statement:       clean,
			Polarity:        g.polarity,
			DistinctTenants: distinct,
			EvidenceCount:   g.evidence,
			Confidence:      g.confidence,
			Surprise:        g.surprise,
			HasSurprise:     g.hasSurp,
		}
		if g.scopeSet && !g.scopeDiv {
			cand.Scope = g.scope
		}

		// Gate 3: contradiction against vetted global knowledge.
		if conflict, isDissonant := p.contradicts(cand.Statement, globalClaims); isDissonant {
			res.Dissonant = append(res.Dissonant, DissonantCandidate{
				Candidate:     cand,
				ConflictsWith: conflict,
			})
			continue
		}

		cleared = append(cleared, cand)
	}

	// Gate 4: prediction-error ranking. Highest aggregate surprise first;
	// candidates without expectation data fall back to corroboration count.
	rankCandidates(cleared)

	// Gate 5: gate policy.
	for _, cand := range cleared {
		switch opts.Gate {
		case GateAuto:
			if cand.Confidence < opts.AutoConfidence {
				res.Skipped = append(res.Skipped, SkippedCandidate{
					Statement:       cand.Statement,
					DistinctTenants: cand.DistinctTenants,
					Reason:          ReasonBelowAutoConfidence,
				})
				continue
			}
			res.Promoted = append(res.Promoted, cand)
		default: // GateOperator
			res.Pending = append(res.Pending, cand)
		}
	}

	return res, nil
}

// cluster groups equivalent lessons across every tenant using greedy
// normalized-token-overlap matching, aggregating per-group corroboration,
// evidence, confidence and surprise.
func (p *Promoter) cluster(ctx context.Context, tenants []TenantLessons, overlapThreshold float64) []group {
	var groups []group

	for _, tl := range tenants {
		for _, lesson := range tl.Lessons {
			toks := contentTokens(lesson.Statement)
			if len(toks) == 0 {
				continue
			}

			// Find the first group this lesson is equivalent to.
			idx := -1
			for i := range groups {
				if tokenOverlapRatio(toks, groups[i].repTokens) >= overlapThreshold {
					idx = i
					break
				}
			}

			surprise, hasData := 0.0, false
			if p.surprise != nil {
				surprise, hasData = p.surprise.SurpriseFor(ctx, lesson)
			}

			if idx == -1 {
				g := group{
					repTokens:  toks,
					statement:  lesson.Statement,
					scope:      lesson.Scope,
					scopeSet:   true,
					polarity:   normPolarity(lesson.Polarity),
					tenants:    map[string]struct{}{tl.Tenant: {}},
					confidence: lesson.Confidence,
					evidence:   len(lesson.Evidence),
					members:    []domain.Lesson{lesson},
				}
				if hasData {
					g.surprise = surprise
					g.hasSurp = true
				}
				groups = append(groups, g)
				continue
			}

			g := &groups[idx]
			g.tenants[tl.Tenant] = struct{}{}
			g.evidence += len(lesson.Evidence)
			g.members = append(g.members, lesson)
			if lesson.Confidence > g.confidence {
				g.confidence = lesson.Confidence
				g.statement = lesson.Statement // most-confident statement represents the group
			}
			if !g.scope.Equal(lesson.Scope) {
				g.scopeDiv = true
			}
			if hasData {
				g.surprise += surprise
				g.hasSurp = true
			}
		}
	}
	return groups
}

// contradicts reports whether statement contradicts any vetted global claim,
// reusing internal/relate's contradiction detection. The candidate statement is
// treated as a "new" claim and the global claims as "existing"; any contradicts
// edge crossing that boundary marks the candidate dissonant.
func (p *Promoter) contradicts(statement string, globalClaims []domain.Claim) (string, bool) {
	if len(globalClaims) == 0 {
		return "", false
	}
	cand := domain.Claim{ID: "cand", Text: statement}
	rels, err := p.relate.DetectIncremental([]domain.Claim{cand}, globalClaims)
	if err != nil {
		return "", false
	}
	byID := make(map[string]string, len(globalClaims))
	for _, c := range globalClaims {
		byID[c.ID] = c.Text
	}
	for _, r := range rels {
		if r.Type != domain.RelationshipTypeContradicts {
			continue
		}
		// Identify the global side of the edge.
		if r.FromClaimID == "cand" {
			if txt, ok := byID[r.ToClaimID]; ok {
				return txt, true
			}
		}
		if r.ToClaimID == "cand" {
			if txt, ok := byID[r.FromClaimID]; ok {
				return txt, true
			}
		}
	}
	return "", false
}

// rankCandidates sorts in place by prediction-error, highest surprise first,
// then by corroboration count, then by confidence, then statement (stable,
// deterministic). Candidates without expectation data have Surprise 0 and thus
// fall through to the corroboration tie-break — satisfying "lessons without
// expectation data rank by corroboration count".
func rankCandidates(cands []PromotedLesson) {
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].Surprise != cands[j].Surprise {
			return cands[i].Surprise > cands[j].Surprise
		}
		if cands[i].DistinctTenants != cands[j].DistinctTenants {
			return cands[i].DistinctTenants > cands[j].DistinctTenants
		}
		if cands[i].Confidence != cands[j].Confidence {
			return cands[i].Confidence > cands[j].Confidence
		}
		return cands[i].Statement < cands[j].Statement
	})
}

// Deidentify enforces the privacy floor on a promoted statement. It trims
// surrounding whitespace and, if the statement contains any denylisted token or
// substring (case-insensitive), returns ok=false so the caller drops the
// candidate (fail closed). It never silently mutates meaning; a statement that
// cannot be safely emitted is dropped, not scrubbed into something misleading.
func Deidentify(statement string, denylist map[string]struct{}) (string, bool) {
	clean := strings.TrimSpace(statement)
	if clean == "" {
		return "", false
	}
	lower := strings.ToLower(clean)
	for tok := range denylist {
		if tok == "" {
			continue
		}
		if strings.Contains(lower, tok) {
			return "", false
		}
	}
	return clean, true
}

// buildDenylist merges operator-supplied sensitive tokens with every tenant
// identifier (and its whitespace/underscore/hyphen-split parts, so a tenant id
// like "acme-corp" also blocks the bare "acme"). All entries are lowercased.
func buildDenylist(sensitive []string, tenants []TenantLessons) map[string]struct{} {
	out := make(map[string]struct{})
	add := func(s string) {
		s = strings.ToLower(strings.TrimSpace(s))
		if s != "" {
			out[s] = struct{}{}
		}
	}
	for _, s := range sensitive {
		add(s)
	}
	for _, t := range tenants {
		add(t.Tenant)
		for _, part := range strings.FieldsFunc(t.Tenant, func(r rune) bool {
			return r == '-' || r == '_' || r == ' ' || r == '/' || r == '.'
		}) {
			if len(part) >= 3 { // skip trivial fragments that would over-block
				add(part)
			}
		}
	}
	return out
}

// normPolarity treats empty polarity as positive, matching domain.Lesson's
// backward-compat rule.
func normPolarity(p domain.LessonPolarity) domain.LessonPolarity {
	if p == "" {
		return domain.LessonPolarityPositive
	}
	return p
}

// --- normalization helpers (self-contained; mirror internal/relate's approach
// without depending on its unexported internals) ---

// promoteStopWords are common English words dropped before computing statement
// overlap so equivalence keys on content, not grammar.
var promoteStopWords = map[string]struct{}{
	"the": {}, "a": {}, "an": {}, "is": {}, "are": {}, "was": {}, "were": {},
	"be": {}, "been": {}, "being": {}, "have": {}, "has": {}, "had": {},
	"do": {}, "does": {}, "did": {}, "will": {}, "would": {}, "shall": {},
	"should": {}, "may": {}, "might": {}, "must": {}, "can": {}, "could": {},
	"to": {}, "of": {}, "in": {}, "for": {}, "on": {}, "with": {}, "at": {},
	"by": {}, "from": {}, "as": {}, "into": {}, "through": {}, "this": {},
	"that": {}, "these": {}, "those": {}, "it": {}, "its": {}, "and": {},
	"or": {}, "but": {}, "so": {}, "than": {}, "then": {},
}

// contentTokens lowercases text, strips punctuation and stop words, and applies
// the same minimal stemming as internal/relate so "succeeds"/"succeed" collapse.
func contentTokens(text string) map[string]struct{} {
	words := strings.Fields(strings.ToLower(text))
	out := make(map[string]struct{}, len(words))
	for _, w := range words {
		w = strings.Trim(w, ",.;:!?()[]{}\"'")
		if w == "" {
			continue
		}
		if _, ok := promoteStopWords[w]; ok {
			continue
		}
		out[stem(w)] = struct{}{}
	}
	return out
}

func stem(word string) string {
	if len(word) > 5 && strings.HasSuffix(word, "ed") {
		return strings.TrimSuffix(word, "ed")
	}
	if len(word) > 5 && strings.HasSuffix(word, "es") {
		return strings.TrimSuffix(word, "es")
	}
	if len(word) > 4 && strings.HasSuffix(word, "s") {
		return strings.TrimSuffix(word, "s")
	}
	return word
}

// tokenOverlapRatio is |a ∩ b| / min(|a|, |b|) — the fraction of the smaller
// token set that is shared. Empty sets never match.
func tokenOverlapRatio(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	small, large := a, b
	if len(b) < len(a) {
		small, large = b, a
	}
	for tok := range small {
		if _, ok := large[tok]; ok {
			inter++
		}
	}
	return float64(inter) / float64(len(small))
}
