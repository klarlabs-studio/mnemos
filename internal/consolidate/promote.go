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
//  0. Subject-class eligibility (ADR 0012) — applied FIRST, per lesson. A
//     lesson is eligible only when its subject is class-level (about a
//     category: a breed, species, disease, a spider). Individual-subject
//     lessons (about a specific pet/owner) and unclassified (unknown) lessons
//     are excluded here and can NEVER promote, no matter how many tenants
//     corroborate them — the privacy invariant for a medical product. See
//     domain.EligibleForPromotion.
//  1. Cross-tenant corroboration — a candidate must have been produced
//     independently in ≥ MinTenants distinct tenants. This is simultaneously
//     the QUALITY signal (many tenants learned the same thing) and the PRIVACY
//     gate (a single-tenant lesson cannot leak, because it never becomes a
//     candidate). This is the EMERGENT path. The CURATED path (Options.Curated,
//     gated by the caller holding the promote:global curator scope) BYPASSES
//     this gate: a class-level fact may promote from a single source. Curated
//     candidates still run every other gate (de-identification, contradiction,
//     ranking, gate policy).
//  2. Token-level corroboration + de-identification — the promoted statement is
//     NOT one tenant's verbatim text. Within a cleared group the engine picks
//     the highest-confidence member EVERY content token of which was seen in
//     ≥2 distinct tenants; if no member qualifies it fails closed (drops the
//     group). This structurally guarantees every promoted word is cross-tenant
//     corroborated, so a non-denylisted specific (a pet name, customer, internal
//     term) unique to one tenant can never ride out. The denylist Deidentify
//     runs as a second layer on the chosen statement. The promoted schema keeps
//     only the generalized statement, corroboration count, and abstracted
//     evidence counts — no tenant id, no raw event text, no per-tenant evidence
//     ids.
//  3. Contradiction — a candidate that contradicts a vetted global claim is not
//     promoted silently; it is routed to Dissonant. The check itself fails
//     closed: if it cannot run, the candidate is skipped rather than promoted.
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

// DefaultEquivalenceJaccard is the Jaccard similarity (|a∩b| / |a∪b|) at which
// two lesson statements are treated as "the same lesson" for cross-tenant
// corroboration. Jaccard (rather than overlap-over-smaller) is deliberately
// strict: it penalizes the differing tokens in BOTH statements, so genuinely
// different key nouns — "restart payments" vs "restart billing" — stay in
// separate groups instead of collapsing into one falsely "corroborated" fact.
const DefaultEquivalenceJaccard = 0.6

// tokenCorroborationMinTenants is how many distinct tenants must have used a
// content token before it may appear in a promoted statement. Two is the floor
// that makes "corroborated" meaningful while remaining reachable.
const tokenCorroborationMinTenants = 2

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
	// EquivalenceJaccard is the statement-equivalence threshold used to cluster
	// lessons across tenants. Defaults to DefaultEquivalenceJaccard.
	EquivalenceJaccard float64
	// SensitiveTokens is an operator-supplied denylist of tokens/phrases that
	// must never appear in a promoted statement (customer names, PII markers,
	// internal codenames). Single tokens are matched on word boundaries; phrases
	// (containing whitespace) are matched as substrings. The tenant identifiers
	// themselves are always added to this set automatically. Entries shorter
	// than minDenylistLen are ignored to avoid catastrophic over-blocking.
	SensitiveTokens []string
	// Curated enables the ADR 0012 CURATED single-source promotion path. When
	// set, an eligible (class-level) lesson may promote from a SINGLE source,
	// bypassing the cross-tenant corroboration gate (gate 1) and the token-level
	// cross-tenant corroboration (gate 2a). It still runs the subject-class
	// eligibility gate, denylist de-identification, and the contradiction gate.
	//
	// Curated is an AUTHORIZED capability: the caller MUST have verified the
	// operator holds the promote:global curator scope (see auth.Claims.CanCurate)
	// before setting it. The pure engine trusts that authorization has happened
	// — it enforces the class-level + de-identification + contradiction floors,
	// not the identity of the curator.
	Curated bool
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
	if o.EquivalenceJaccard == 0 {
		o.EquivalenceJaccard = DefaultEquivalenceJaccard
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
// text, and NO per-tenant evidence ids — only aggregate counts. Every content
// token of Statement was, by construction, observed in ≥2 distinct tenants.
type PromotedLesson struct {
	// Statement is the cross-tenant-corroborated, de-identified lesson text.
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
	// Surprise is the peak prediction-error backing the generalization (max
	// per-member surprise from domain.Expectation). Zero when no expectation
	// data was available; HasSurprise disambiguates.
	Surprise    float64
	HasSurprise bool
	// Curated records which ADR 0012 path produced this candidate: true for the
	// curated single-source path (authorized by the promote:global scope), false
	// for the emergent cross-tenant-corroborated path. Purely for audit output;
	// both paths clear de-identification and the contradiction gate.
	Curated bool
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
	ReasonIneligibleSubject         = "ineligible subject class (not class-level)"
	ReasonInsufficientCorroboration = "insufficient cross-tenant corroboration"
	ReasonNoCorroboratedPhrasing    = "no cross-tenant-corroborated phrasing"
	ReasonTenantSpecificToken       = "statement contains tenant-specific or sensitive token"
	ReasonContradictionCheckFailed  = "contradiction check could not run"
	ReasonBelowAutoConfidence       = "below auto-promote confidence threshold"
	ReasonNoContentTokens           = "lesson has no content tokens to corroborate"
)

// Result is the structured, auditable output of a promotion pass. Every input
// lesson lands in exactly one bucket (individually if it has no content tokens,
// otherwise as part of exactly one group).
type Result struct {
	// Promoted are candidates released to the global tier (GateAuto, above
	// threshold). Ordered by prediction-error ranking, highest surprise first.
	Promoted []PromotedLesson
	// Pending are candidates awaiting operator approval (GateOperator). Ordered
	// by the same ranking.
	Pending []PromotedLesson
	// Dissonant are candidates that contradict vetted global knowledge.
	Dissonant []DissonantCandidate
	// Skipped are groups (or lessons) dropped by a gate, each with a reason.
	Skipped []SkippedCandidate
}

// GlobalKnowledge supplies the existing vetted global (neocortex) claims that
// candidates are checked against for contradiction (gate 3). A nil
// GlobalKnowledge (or one returning no claims) means "no global knowledge yet"
// — nothing can be dissonant.
type GlobalKnowledge interface {
	VettedClaims(ctx context.Context) ([]domain.Claim, error)
}

// SurpriseSource supplies the prediction-error (surprise) that backs a lesson,
// sourced from domain.Expectation reconciliation. It returns the surprise scalar
// and whether any expectation data existed for the lesson. A nil SurpriseSource
// makes candidates rank purely by corroboration count.
type SurpriseSource interface {
	SurpriseFor(ctx context.Context, lesson domain.Lesson) (surprise float64, hasData bool)
}

// Promoter runs the promotion pass. It is safe for concurrent use; it holds no
// mutable state.
type Promoter struct {
	global   GlobalKnowledge
	surprise SurpriseSource
	relate   relate.Engine
	// detect is the contradiction detector (gate 3). It defaults to the
	// relate-backed relateContradicts; it is a field so tests can inject a
	// failing detector to exercise the fail-closed path.
	detect func(statement string, globalClaims []domain.Claim) (conflict string, isDissonant bool, err error)
}

// NewPromoter builds a Promoter. Either dependency may be nil: a nil
// GlobalKnowledge disables the contradiction gate (nothing is dissonant), and a
// nil SurpriseSource makes ranking fall back to corroboration count.
func NewPromoter(global GlobalKnowledge, surprise SurpriseSource) *Promoter {
	p := &Promoter{
		global:   global,
		surprise: surprise,
		relate:   relate.NewEngine(),
	}
	p.detect = p.relateContradicts
	return p
}

// member is one tenant's lesson inside a cluster, kept with its owning tenant
// and precomputed tokens so per-token cross-tenant corroboration can be
// computed for the group.
type member struct {
	tenant   string
	lesson   domain.Lesson
	tokens   map[string]struct{}
	surprise float64
	hasSurp  bool
	// sortKey is the canonical ordering key (statement|tenant|id), making
	// clustering deterministic and independent of input order.
	sortKey string
}

// group is a cluster of equivalent lessons discovered across tenants.
type group struct {
	anchor  map[string]struct{} // tokens of the canonically-first member — the merge signature
	members []member
}

func (g *group) tenants() map[string]struct{} {
	out := make(map[string]struct{}, len(g.members))
	for _, m := range g.members {
		out[m.tenant] = struct{}{}
	}
	return out
}

// Promote runs the five gates over the supplied per-tenant lessons and returns a
// structured, auditable result.
func (p *Promoter) Promote(ctx context.Context, tenants []TenantLessons, opts Options) (Result, error) {
	opts = opts.withDefaults()

	// Build the de-identification denylist: operator-supplied sensitive tokens
	// plus every tenant identifier (a tenant's own id must never surface).
	denylist := buildDenylist(opts.SensitiveTokens, tenants)

	var res Result

	// Flatten to members, recording (gate 9) any lesson with no content tokens
	// so nothing is silently dropped.
	members := make([]member, 0)
	for _, tl := range tenants {
		for _, lesson := range tl.Lessons {
			// Gate 0 (ADR 0012): subject-class eligibility, applied FIRST. Only
			// class-level lessons are eligible; individual-subject and unknown
			// lessons are excluded here and can NEVER promote — the privacy
			// invariant. This runs BEFORE any cross-tenant counting so it holds
			// on both the emergent and curated paths.
			if !domain.EligibleForPromotion(lesson.SubjectClass) {
				res.Skipped = append(res.Skipped, SkippedCandidate{
					Statement:       lesson.Statement,
					DistinctTenants: 0,
					Reason:          ReasonIneligibleSubject,
				})
				continue
			}
			toks := relate.ContentTokens(lesson.Statement)
			if len(toks) == 0 {
				res.Skipped = append(res.Skipped, SkippedCandidate{
					Statement:       lesson.Statement,
					DistinctTenants: 0,
					Reason:          ReasonNoContentTokens,
				})
				continue
			}
			s, hasData := 0.0, false
			if p.surprise != nil {
				s, hasData = p.surprise.SurpriseFor(ctx, lesson)
			}
			members = append(members, member{
				tenant:   tl.Tenant,
				lesson:   lesson,
				tokens:   toks,
				surprise: s,
				hasSurp:  hasData,
				sortKey:  lesson.Statement + "\x00" + tl.Tenant + "\x00" + lesson.ID,
			})
		}
	}

	// Gate 1 — cluster equivalent lessons across tenants, deterministically.
	groups := clusterMembers(members, opts.EquivalenceJaccard)

	// Fetch vetted global claims once for the contradiction gate.
	var globalClaims []domain.Claim
	if p.global != nil {
		var err error
		globalClaims, err = p.global.VettedClaims(ctx)
		if err != nil {
			return Result{}, err
		}
	}

	var cleared []PromotedLesson

	for _, g := range groups {
		tenantSet := g.tenants()
		distinct := len(tenantSet)

		// Gates 1 + 2a select the statement. The path differs:
		//   - Emergent (default): require ≥MinTenants corroboration (gate 1) and
		//     a phrasing every token of which was seen in ≥2 tenants (gate 2a) —
		//     structural cross-tenant privacy.
		//   - Curated (ADR 0012, authorized by promote:global): a single source
		//     is allowed, so both are bypassed and the representative (highest-
		//     confidence) statement is used. Eligibility (gate 0) already proved
		//     the subject is class-level, and de-identification (gate 2b) still
		//     runs — those are the privacy floor on this path.
		var stmt string
		if opts.Curated {
			stmt = representativeStatement(g)
		} else {
			// Gate 1: corroboration / privacy. A lesson in fewer than MinTenants
			// distinct tenants can NEVER promote — this is the no-leak guarantee.
			if distinct < opts.MinTenants {
				res.Skipped = append(res.Skipped, SkippedCandidate{
					Statement:       representativeStatement(g),
					DistinctTenants: distinct,
					Reason:          ReasonInsufficientCorroboration,
				})
				continue
			}

			// Gate 2a: token-level corroboration. Choose the highest-confidence
			// member every content token of which was seen in ≥2 distinct tenants.
			corr, ok := corroboratedStatement(g)
			if !ok {
				res.Skipped = append(res.Skipped, SkippedCandidate{
					Statement:       representativeStatement(g),
					DistinctTenants: distinct,
					Reason:          ReasonNoCorroboratedPhrasing,
				})
				continue
			}
			stmt = corr
		}

		// Gate 2b: de-identification. Fail closed if the chosen statement still
		// carries a denylisted/tenant token.
		clean, ok := Deidentify(stmt, denylist)
		if !ok {
			res.Skipped = append(res.Skipped, SkippedCandidate{
				Statement:       stmt,
				DistinctTenants: distinct,
				Reason:          ReasonTenantSpecificToken,
			})
			continue
		}

		cand := PromotedLesson{
			Statement:       clean,
			Polarity:        groupPolarity(g),
			DistinctTenants: distinct,
			EvidenceCount:   groupEvidence(g),
			Confidence:      groupConfidence(g),
			Surprise:        groupSurprise(g),
			HasSurprise:     groupHasSurprise(g),
			Curated:         opts.Curated,
		}
		if scope, shared := groupScope(g); shared {
			cand.Scope = scope
		}

		// Gate 3: contradiction against vetted global knowledge — fail closed.
		conflict, isDissonant, err := p.detect(cand.Statement, globalClaims)
		if err != nil {
			res.Skipped = append(res.Skipped, SkippedCandidate{
				Statement:       cand.Statement,
				DistinctTenants: distinct,
				Reason:          ReasonContradictionCheckFailed,
			})
			continue
		}
		if isDissonant {
			res.Dissonant = append(res.Dissonant, DissonantCandidate{
				Candidate:     cand,
				ConflictsWith: conflict,
			})
			continue
		}

		cleared = append(cleared, cand)
	}

	// Gate 4: prediction-error ranking. Highest surprise first; candidates
	// without expectation data fall back to corroboration count.
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

// clusterMembers groups equivalent members using Jaccard similarity. It is
// order-independent: members are sorted canonically first (so greedy assignment
// is stable), then a group-merge fixpoint collapses any two groups whose anchor
// token sets are equivalent — making the final Result invariant under any
// permutation of the input.
func clusterMembers(members []member, jaccardThreshold float64) []group {
	sorted := make([]member, len(members))
	copy(sorted, members)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].sortKey < sorted[j].sortKey })

	var groups []group
	for _, m := range sorted {
		idx := -1
		for i := range groups {
			if jaccard(groups[i].anchor, m.tokens) >= jaccardThreshold {
				idx = i
				break
			}
		}
		if idx == -1 {
			groups = append(groups, group{anchor: m.tokens, members: []member{m}})
			continue
		}
		groups[idx].members = append(groups[idx].members, m)
	}

	// Fixpoint merge: collapse equivalent groups until stable. Anchors are the
	// canonically-first member's tokens (groups were created in sorted order),
	// so the merge target is deterministic.
	for {
		merged := false
		for i := 0; i < len(groups) && !merged; i++ {
			for j := i + 1; j < len(groups); j++ {
				if jaccard(groups[i].anchor, groups[j].anchor) >= jaccardThreshold {
					groups[i].members = append(groups[i].members, groups[j].members...)
					groups = append(groups[:j], groups[j+1:]...)
					merged = true
					break
				}
			}
		}
		if !merged {
			break
		}
	}
	return groups
}

// corroboratedStatement returns the statement of the highest-confidence member
// whose EVERY content token was observed in ≥ tokenCorroborationMinTenants
// distinct tenants. This structurally guarantees no promoted word is unique to a
// single tenant. Returns ok=false when no member qualifies (fail closed).
func corroboratedStatement(g group) (string, bool) {
	// Per-token distinct-tenant sets across the whole group.
	tokenTenants := make(map[string]map[string]struct{})
	for _, m := range g.members {
		for tok := range m.tokens {
			set := tokenTenants[tok]
			if set == nil {
				set = make(map[string]struct{})
				tokenTenants[tok] = set
			}
			set[m.tenant] = struct{}{}
		}
	}

	// Candidate members ordered by confidence desc, then canonical key for
	// determinism.
	cand := make([]member, len(g.members))
	copy(cand, g.members)
	sort.SliceStable(cand, func(i, j int) bool {
		if cand[i].lesson.Confidence != cand[j].lesson.Confidence {
			return cand[i].lesson.Confidence > cand[j].lesson.Confidence
		}
		return cand[i].sortKey < cand[j].sortKey
	})

	for _, m := range cand {
		allCorroborated := true
		for tok := range m.tokens {
			if len(tokenTenants[tok]) < tokenCorroborationMinTenants {
				allCorroborated = false
				break
			}
		}
		if allCorroborated {
			return m.lesson.Statement, true
		}
	}
	return "", false
}

// representativeStatement is the statement used purely for audit/skip reporting
// (the highest-confidence member's text). It is NEVER used as a promoted
// statement — corroboratedStatement gates that.
func representativeStatement(g group) string {
	best := g.members[0]
	for _, m := range g.members[1:] {
		if m.lesson.Confidence > best.lesson.Confidence {
			best = m
		}
	}
	return best.lesson.Statement
}

func groupEvidence(g group) int {
	total := 0
	for _, m := range g.members {
		total += len(m.lesson.Evidence)
	}
	return total
}

func groupConfidence(g group) float64 {
	best := 0.0
	for _, m := range g.members {
		if m.lesson.Confidence > best {
			best = m.lesson.Confidence
		}
	}
	return best
}

// groupSurprise is the PEAK per-member surprise (max, not sum) so ranking
// reflects the sharpest prediction error the generalization is built on rather
// than being biased toward larger groups.
func groupSurprise(g group) float64 {
	best := 0.0
	for _, m := range g.members {
		if m.hasSurp && m.surprise > best {
			best = m.surprise
		}
	}
	return best
}

func groupHasSurprise(g group) bool {
	for _, m := range g.members {
		if m.hasSurp {
			return true
		}
	}
	return false
}

func groupPolarity(g group) domain.LessonPolarity {
	// Use the highest-confidence member's polarity for a stable sense.
	best := g.members[0]
	for _, m := range g.members[1:] {
		if m.lesson.Confidence > best.lesson.Confidence {
			best = m
		}
	}
	return normPolarity(best.lesson.Polarity)
}

// groupScope returns the scope shared by every member, and whether it is shared.
func groupScope(g group) (domain.Scope, bool) {
	s := g.members[0].lesson.Scope
	for _, m := range g.members[1:] {
		if !m.lesson.Scope.Equal(s) {
			return domain.Scope{}, false
		}
	}
	return s, true
}

// relateContradicts reports whether statement contradicts any vetted global
// claim, reusing internal/relate's contradiction detection. It FAILS CLOSED: any
// error from the detector is returned so the caller skips (never promotes)
// rather than silently treating an unrunnable check as "no contradiction".
func (p *Promoter) relateContradicts(statement string, globalClaims []domain.Claim) (string, bool, error) {
	if len(globalClaims) == 0 {
		return "", false, nil
	}
	cand := domain.Claim{ID: "cand", Text: statement}
	rels, err := p.relate.DetectIncremental([]domain.Claim{cand}, globalClaims)
	if err != nil {
		return "", false, err
	}
	byID := make(map[string]string, len(globalClaims))
	for _, c := range globalClaims {
		byID[c.ID] = c.Text
	}
	for _, r := range rels {
		if r.Type != domain.RelationshipTypeContradicts {
			continue
		}
		if r.FromClaimID == "cand" {
			if txt, ok := byID[r.ToClaimID]; ok {
				return txt, true, nil
			}
		}
		if r.ToClaimID == "cand" {
			if txt, ok := byID[r.FromClaimID]; ok {
				return txt, true, nil
			}
		}
	}
	return "", false, nil
}

// rankCandidates sorts in place by prediction-error, highest surprise first,
// then by corroboration count, then confidence, then statement (stable,
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

// minDenylistLen is the shortest denylist entry that may be applied. Anything
// shorter would over-block (a 1–2 char fragment matches almost any statement).
const minDenylistLen = 3

// Deidentify enforces the privacy floor on a promoted statement. It trims
// surrounding whitespace and, if the statement contains any denylisted token
// (matched on word boundaries) or phrase (matched as a substring), returns
// ok=false so the caller drops the candidate (fail closed). It never silently
// mutates meaning; a statement that cannot be safely emitted is dropped, not
// scrubbed into something misleading.
func Deidentify(statement string, denylist map[string]struct{}) (string, bool) {
	clean := strings.TrimSpace(statement)
	if clean == "" {
		return "", false
	}
	lower := strings.ToLower(clean)
	words := wordSet(lower)
	for tok := range denylist {
		if len(tok) < minDenylistLen {
			continue
		}
		if strings.ContainsAny(tok, " \t") {
			// Multi-word phrase: substring match.
			if strings.Contains(lower, tok) {
				return "", false
			}
			continue
		}
		// Single token: word-boundary match to avoid over-blocking (denylist
		// "acme" must not veto the unrelated word "acmeter").
		if _, ok := words[tok]; ok {
			return "", false
		}
	}
	return clean, true
}

// wordSet splits an already-lowercased string on any non-alphanumeric rune,
// returning the set of word tokens — the boundary model used by Deidentify.
func wordSet(lower string) map[string]struct{} {
	fields := strings.FieldsFunc(lower, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	out := make(map[string]struct{}, len(fields))
	for _, f := range fields {
		out[f] = struct{}{}
	}
	return out
}

// buildDenylist merges operator-supplied sensitive tokens with every tenant
// identifier (and its whitespace/underscore/hyphen-split parts, so a tenant id
// like "acme-corp" also blocks the bare "acme"). All entries are lowercased;
// entries shorter than minDenylistLen are dropped here so a short tenant id
// cannot make Deidentify over-block everything.
func buildDenylist(sensitive []string, tenants []TenantLessons) map[string]struct{} {
	out := make(map[string]struct{})
	add := func(s string) {
		s = strings.ToLower(strings.TrimSpace(s))
		if len(s) >= minDenylistLen {
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
			add(part)
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

// jaccard is |a ∩ b| / |a ∪ b|. It penalizes tokens unique to EITHER set, so
// two statements that share a common verb but differ on the key noun score low
// and stay in separate clusters. Empty sets never match.
func jaccard(a, b map[string]struct{}) float64 {
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
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}
