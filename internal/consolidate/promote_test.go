package consolidate

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
)

// --- test doubles ---

type fakeGlobal struct {
	claims []domain.Claim
}

func (f fakeGlobal) VettedClaims(context.Context) ([]domain.Claim, error) {
	return f.claims, nil
}

// fakeSurprise keys prediction-error by lesson ID.
type fakeSurprise struct {
	byID map[string]float64
}

func (f fakeSurprise) SurpriseFor(_ context.Context, l domain.Lesson) (float64, bool) {
	v, ok := f.byID[l.ID]
	return v, ok
}

func lesson(id, tenant, statement string, conf float64, evidence int) domain.Lesson {
	ev := make([]string, evidence)
	for i := range ev {
		ev[i] = "act_" + tenant + "_" + id
	}
	return domain.Lesson{
		ID:         id,
		Statement:  statement,
		Confidence: conf,
		Evidence:   ev,
		Polarity:   domain.LessonPolarityPositive,
	}
}

func hasStatement(cands []PromotedLesson, substr string) bool {
	for _, c := range cands {
		if contains(c.Statement, substr) {
			return true
		}
	}
	return false
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func skipReason(res Result, substr string) (SkippedCandidate, bool) {
	for _, s := range res.Skipped {
		if contains(s.Statement, substr) {
			return s, true
		}
	}
	return SkippedCandidate{}, false
}

// TestPromotion_NoSingleTenantLeak is the ADR-0011 no-leak guardrail: a fact
// present in exactly ONE tenant must NEVER promote, while a fact corroborated in
// ≥ MinTenants distinct tenants does. This is the privacy floor — the
// corroboration gate IS the privacy gate.
func TestPromotion_NoSingleTenantLeak(t *testing.T) {
	// Fact F: only tenant-alpha ever produced it.
	factF := "canary deploy on search improves tail latency"
	// Fact G: independently produced by three distinct tenants.
	factG := "rollback on payments reliably resolves the incident"

	// Alpha's copy of G carries a unique embellishment ("runbook17") AND the
	// highest confidence — a naive "take the most-confident member verbatim"
	// would leak it. Token-level corroboration must strip it.
	factGAlpha := "rollback on payments reliably resolves the incident via runbook17"

	tenants := []TenantLessons{
		{Tenant: "alpha", Lessons: []domain.Lesson{
			lesson("f1", "alpha", factF, 0.9, 4),
			lesson("g1", "alpha", factGAlpha, 0.8, 3),
		}},
		{Tenant: "bravo", Lessons: []domain.Lesson{
			lesson("g2", "bravo", factG, 0.7, 5),
		}},
		{Tenant: "charlie", Lessons: []domain.Lesson{
			lesson("g3", "charlie", factG, 0.75, 2),
		}},
	}

	p := NewPromoter(nil, nil)
	res, err := p.Promote(context.Background(), tenants, Options{Gate: GateAuto})
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}

	// G must appear in the promoted output.
	if !hasStatement(res.Promoted, "rollback on payments") {
		t.Fatalf("fact G (corroborated in 3 tenants) must be promoted; promoted=%+v", res.Promoted)
	}
	// Alpha's single-tenant embellishment must be stripped, not promoted.
	if hasStatement(res.Promoted, "runbook17") || hasStatement(res.Pending, "runbook17") {
		t.Fatalf("LEAK: single-tenant token 'runbook17' rode out on the promoted G statement: %+v", res.Promoted)
	}

	// F must NEVER appear anywhere it could leak: not promoted, not pending,
	// not dissonant.
	if hasStatement(res.Promoted, "canary deploy on search") {
		t.Fatalf("LEAK: single-tenant fact F appeared in Promoted")
	}
	if hasStatement(res.Pending, "canary deploy on search") {
		t.Fatalf("LEAK: single-tenant fact F appeared in Pending")
	}
	for _, d := range res.Dissonant {
		if contains(d.Candidate.Statement, "canary deploy on search") {
			t.Fatalf("LEAK: single-tenant fact F appeared in Dissonant")
		}
	}

	// And it must be explicitly recorded as skipped for the audit trail.
	s, ok := skipReason(res, "canary deploy on search")
	if !ok {
		t.Fatalf("fact F must be recorded in Skipped for auditability")
	}
	if s.Reason != ReasonInsufficientCorroboration {
		t.Fatalf("fact F skip reason = %q, want %q", s.Reason, ReasonInsufficientCorroboration)
	}
	if s.DistinctTenants != 1 {
		t.Fatalf("fact F distinct tenants = %d, want 1", s.DistinctTenants)
	}

	// The promoted G must carry a corroboration count and abstracted evidence,
	// but no tenant identifiers or raw evidence ids.
	var promotedG PromotedLesson
	for _, c := range res.Promoted {
		if contains(c.Statement, "rollback on payments") {
			promotedG = c
		}
	}
	if promotedG.DistinctTenants != 3 {
		t.Fatalf("promoted G distinct tenants = %d, want 3", promotedG.DistinctTenants)
	}
	if promotedG.EvidenceCount != 3+5+2 {
		t.Fatalf("promoted G evidence count = %d, want %d (abstracted sum)", promotedG.EvidenceCount, 10)
	}
}

func TestPromotion_DeidentifyStripsTenantTokens(t *testing.T) {
	// Deidentify unit behavior: a denylisted token drops the statement.
	deny := map[string]struct{}{"acme": {}}
	if _, ok := Deidentify("rollback on acme-payments succeeds", deny); ok {
		t.Fatalf("Deidentify must fail closed on a denylisted token")
	}
	if got, ok := Deidentify("  rollback on payments succeeds  ", deny); !ok || got != "rollback on payments succeeds" {
		t.Fatalf("Deidentify should trim and pass a clean statement, got %q ok=%v", got, ok)
	}

	// End-to-end: a lesson corroborated across 3 tenants whose statement still
	// carries a sensitive token (a shared vendor/PII marker) must be dropped,
	// even though it clears the corroboration gate.
	sensitiveStmt := "escalate the acmecorp outage to the on-call lead"
	tenants := []TenantLessons{
		{Tenant: "t1", Lessons: []domain.Lesson{lesson("a", "t1", sensitiveStmt, 0.9, 2)}},
		{Tenant: "t2", Lessons: []domain.Lesson{lesson("b", "t2", sensitiveStmt, 0.9, 2)}},
		{Tenant: "t3", Lessons: []domain.Lesson{lesson("c", "t3", sensitiveStmt, 0.9, 2)}},
	}
	p := NewPromoter(nil, nil)
	res, err := p.Promote(context.Background(), tenants, Options{
		Gate:            GateAuto,
		SensitiveTokens: []string{"acmecorp"},
	})
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if hasStatement(res.Promoted, "acmecorp") || hasStatement(res.Pending, "acmecorp") {
		t.Fatalf("LEAK: statement with sensitive token was promoted")
	}
	s, ok := skipReason(res, "acmecorp")
	if !ok || s.Reason != ReasonTenantSpecificToken {
		t.Fatalf("sensitive statement must be skipped with tenant-token reason, got %+v ok=%v", s, ok)
	}
}

func TestPromotion_ContradictionRoutesToDissonant(t *testing.T) {
	// A candidate corroborated in 3 tenants that contradicts a vetted global
	// claim must be routed to Dissonant, not Promoted.
	stmt := "rollback on payments never resolves the incident"
	tenants := []TenantLessons{
		{Tenant: "t1", Lessons: []domain.Lesson{lesson("a", "t1", stmt, 0.9, 2)}},
		{Tenant: "t2", Lessons: []domain.Lesson{lesson("b", "t2", stmt, 0.9, 2)}},
		{Tenant: "t3", Lessons: []domain.Lesson{lesson("c", "t3", stmt, 0.9, 2)}},
	}
	global := fakeGlobal{claims: []domain.Claim{
		{ID: "gc1", Text: "rollback on payments reliably resolves the incident"},
	}}

	p := NewPromoter(global, nil)
	res, err := p.Promote(context.Background(), tenants, Options{Gate: GateAuto})
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if hasStatement(res.Promoted, "rollback on payments") {
		t.Fatalf("contradicting candidate must NOT be promoted")
	}
	if len(res.Dissonant) != 1 {
		t.Fatalf("expected 1 dissonant candidate, got %d (%+v)", len(res.Dissonant), res.Dissonant)
	}
	if res.Dissonant[0].ConflictsWith == "" {
		t.Fatalf("dissonant candidate must record the conflicting global claim text")
	}
}

func TestPromotion_PredictionErrorOrdering(t *testing.T) {
	// Two candidates, both corroborated in 3 tenants. The one backed by higher
	// aggregate surprise ranks first.
	low := "scale up on search reduces queue depth"
	high := "restart on billing clears the stuck job"

	mk := func(idPfx, stmt string) []TenantLessons {
		return []TenantLessons{
			{Tenant: "t1", Lessons: []domain.Lesson{lesson(idPfx+"1", "t1", stmt, 0.9, 2)}},
			{Tenant: "t2", Lessons: []domain.Lesson{lesson(idPfx+"2", "t2", stmt, 0.9, 2)}},
			{Tenant: "t3", Lessons: []domain.Lesson{lesson(idPfx+"3", "t3", stmt, 0.9, 2)}},
		}
	}
	var tenants []TenantLessons
	tenants = append(tenants, mk("low", low)...)
	tenants = append(tenants, mk("high", high)...)

	surprise := fakeSurprise{byID: map[string]float64{
		"low1": 0.1, "low2": 0.1, "low3": 0.1, // peak 0.1
		"high1": 0.9, "high2": 0.9, "high3": 0.9, // peak 0.9
	}}

	p := NewPromoter(nil, surprise)
	res, err := p.Promote(context.Background(), tenants, Options{Gate: GateAuto})
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if len(res.Promoted) != 2 {
		t.Fatalf("expected 2 promoted, got %d", len(res.Promoted))
	}
	if !contains(res.Promoted[0].Statement, "restart on billing") {
		t.Fatalf("highest-surprise candidate must rank first, got order: %q then %q",
			res.Promoted[0].Statement, res.Promoted[1].Statement)
	}
	if !res.Promoted[0].HasSurprise || res.Promoted[0].Surprise <= res.Promoted[1].Surprise {
		t.Fatalf("ranking must reflect aggregate surprise: %+v", res.Promoted)
	}
}

func TestPromotion_PredictionErrorFallsBackToCorroboration(t *testing.T) {
	// With no surprise data, candidates rank by corroboration count.
	four := "deploy on api passes smoke tests"
	three := "restart on billing clears the stuck queue"

	tenants := []TenantLessons{
		{Tenant: "t1", Lessons: []domain.Lesson{lesson("f1", "t1", four, 0.9, 1), lesson("h1", "t1", three, 0.9, 1)}},
		{Tenant: "t2", Lessons: []domain.Lesson{lesson("f2", "t2", four, 0.9, 1), lesson("h2", "t2", three, 0.9, 1)}},
		{Tenant: "t3", Lessons: []domain.Lesson{lesson("f3", "t3", four, 0.9, 1), lesson("h3", "t3", three, 0.9, 1)}},
		{Tenant: "t4", Lessons: []domain.Lesson{lesson("f4", "t4", four, 0.9, 1)}},
	}
	p := NewPromoter(nil, nil)
	res, err := p.Promote(context.Background(), tenants, Options{Gate: GateAuto})
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if len(res.Promoted) != 2 {
		t.Fatalf("expected 2 promoted, got %d (%+v)", len(res.Promoted), res.Promoted)
	}
	if res.Promoted[0].DistinctTenants != 4 {
		t.Fatalf("higher-corroboration candidate must rank first, got %+v", res.Promoted)
	}
}

func TestPromotion_OperatorGateProducesPending(t *testing.T) {
	stmt := "rollback on payments reliably resolves the incident"
	tenants := []TenantLessons{
		{Tenant: "t1", Lessons: []domain.Lesson{lesson("a", "t1", stmt, 0.9, 2)}},
		{Tenant: "t2", Lessons: []domain.Lesson{lesson("b", "t2", stmt, 0.9, 2)}},
		{Tenant: "t3", Lessons: []domain.Lesson{lesson("c", "t3", stmt, 0.9, 2)}},
	}
	p := NewPromoter(nil, nil)
	// Default gate is operator.
	res, err := p.Promote(context.Background(), tenants, Options{})
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if len(res.Promoted) != 0 {
		t.Fatalf("operator gate must NOT write to Promoted, got %+v", res.Promoted)
	}
	if len(res.Pending) != 1 {
		t.Fatalf("operator gate must emit 1 pending candidate, got %d", len(res.Pending))
	}
}

func TestPromotion_AutoGateBelowThresholdSkips(t *testing.T) {
	stmt := "rollback on payments reliably resolves the incident"
	// Confidence below the default auto floor (LessonConfidenceMin = 0.55).
	tenants := []TenantLessons{
		{Tenant: "t1", Lessons: []domain.Lesson{lesson("a", "t1", stmt, 0.4, 2)}},
		{Tenant: "t2", Lessons: []domain.Lesson{lesson("b", "t2", stmt, 0.3, 2)}},
		{Tenant: "t3", Lessons: []domain.Lesson{lesson("c", "t3", stmt, 0.45, 2)}},
	}
	p := NewPromoter(nil, nil)
	res, err := p.Promote(context.Background(), tenants, Options{Gate: GateAuto})
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if len(res.Promoted) != 0 {
		t.Fatalf("below-threshold candidate must not auto-promote, got %+v", res.Promoted)
	}
	if _, ok := skipReason(res, "rollback on payments"); !ok {
		t.Fatalf("below-threshold candidate must be recorded as skipped")
	}
}

func TestPromotion_ScopeRetainedOnlyWhenShared(t *testing.T) {
	stmt := "rollback on payments reliably resolves the incident"
	shared := domain.Scope{Service: "payments"}
	tenants := []TenantLessons{
		{Tenant: "t1", Lessons: []domain.Lesson{withScope(lesson("a", "t1", stmt, 0.9, 1), shared)}},
		{Tenant: "t2", Lessons: []domain.Lesson{withScope(lesson("b", "t2", stmt, 0.9, 1), shared)}},
		{Tenant: "t3", Lessons: []domain.Lesson{withScope(lesson("c", "t3", stmt, 0.9, 1), shared)}},
	}
	p := NewPromoter(nil, nil)
	res, _ := p.Promote(context.Background(), tenants, Options{Gate: GateAuto})
	if len(res.Promoted) != 1 || !res.Promoted[0].Scope.Equal(shared) {
		t.Fatalf("shared scope should be retained on the promoted lesson, got %+v", res.Promoted)
	}

	// Divergent scope should be cleared.
	tenants[2].Lessons[0].Scope = domain.Scope{Service: "search"}
	res2, _ := p.Promote(context.Background(), tenants, Options{Gate: GateAuto})
	if len(res2.Promoted) != 1 || !res2.Promoted[0].Scope.IsEmpty() {
		t.Fatalf("divergent scope should be cleared, got %+v", res2.Promoted)
	}
}

func withScope(l domain.Lesson, s domain.Scope) domain.Lesson {
	l.Scope = s
	return l
}

func withConf(l domain.Lesson, c float64) domain.Lesson {
	l.Confidence = c
	return l
}

// TestPromotion_TokenLevelCorroboration is the privacy-critical vector the
// original guardrail passed vacuously: a group corroborated in 3 tenants where
// exactly ONE member carries a non-denylisted specific token ("fluffy", a pet
// name). Even though that member has the HIGHEST confidence, the token appears
// in only one tenant, so it must be stripped from the promoted statement —
// promotion falls back to a member whose every token is cross-tenant
// corroborated. The specific token must never appear anywhere in the output.
func TestPromotion_TokenLevelCorroboration(t *testing.T) {
	corroborated := "restart on payments clears the queue"
	leaky := "restart on payments clears the fluffy queue" // "fluffy" unique to t1

	tenants := []TenantLessons{
		// t1 has the leaky phrasing AND the highest confidence.
		{Tenant: "t1", Lessons: []domain.Lesson{withConf(lesson("a", "t1", leaky, 0.99, 2), 0.99)}},
		{Tenant: "t2", Lessons: []domain.Lesson{lesson("b", "t2", corroborated, 0.90, 2)}},
		{Tenant: "t3", Lessons: []domain.Lesson{lesson("c", "t3", corroborated, 0.90, 2)}},
	}

	p := NewPromoter(nil, nil)
	res, err := p.Promote(context.Background(), tenants, Options{Gate: GateAuto})
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}

	// A generalization DID promote (the corroborated phrasing).
	if len(res.Promoted) != 1 {
		t.Fatalf("expected the corroborated phrasing to promote, got %+v / skipped %+v", res.Promoted, res.Skipped)
	}
	// The single-tenant token must not appear ANYWHERE.
	if hasStatement(res.Promoted, "fluffy") || hasStatement(res.Pending, "fluffy") {
		t.Fatalf("LEAK: single-tenant token 'fluffy' rode out in a promoted statement: %+v", res.Promoted)
	}
	for _, d := range res.Dissonant {
		if contains(d.Candidate.Statement, "fluffy") {
			t.Fatalf("LEAK: single-tenant token 'fluffy' in dissonant output")
		}
	}
	if !contains(res.Promoted[0].Statement, "restart on payments clears the queue") {
		t.Fatalf("promoted statement should be the corroborated phrasing, got %q", res.Promoted[0].Statement)
	}
}

// TestPromotion_NearParaphrasesDoNotMerge verifies the tightened (Jaccard)
// equivalence: three tenants that each restart a DIFFERENT service on OOM share
// the verb but not the key noun, so they must NOT collapse into one falsely
// "3-tenant corroborated" fact — none promotes, and no service noun leaks.
func TestPromotion_NearParaphrasesDoNotMerge(t *testing.T) {
	tenants := []TenantLessons{
		{Tenant: "t1", Lessons: []domain.Lesson{lesson("a", "t1", "restart payments on oom", 0.9, 2)}},
		{Tenant: "t2", Lessons: []domain.Lesson{lesson("b", "t2", "restart billing on oom", 0.9, 2)}},
		{Tenant: "t3", Lessons: []domain.Lesson{lesson("c", "t3", "restart search on oom", 0.9, 2)}},
	}
	p := NewPromoter(nil, nil)
	res, err := p.Promote(context.Background(), tenants, Options{Gate: GateAuto})
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if len(res.Promoted) != 0 || len(res.Pending) != 0 {
		t.Fatalf("distinct-key-noun paraphrases must NOT promote, got promoted=%+v pending=%+v", res.Promoted, res.Pending)
	}
	// They must stay as three SEPARATE single-tenant groups (proving they did
	// not merge): each is skipped for insufficient corroboration, not for
	// no-corroborated-phrasing (which would mean they had merged).
	if len(res.Skipped) != 3 {
		t.Fatalf("expected 3 separate single-tenant skips (no merge), got %+v", res.Skipped)
	}
	for _, s := range res.Skipped {
		if s.Reason != ReasonInsufficientCorroboration {
			t.Fatalf("paraphrases must stay unmerged (insufficient corroboration), got reason %q", s.Reason)
		}
	}
	for _, noun := range []string{"payments", "billing", "search"} {
		if hasStatement(res.Promoted, noun) || hasStatement(res.Pending, noun) {
			t.Fatalf("LEAK: single-tenant service noun %q promoted", noun)
		}
	}
}

// TestPromotion_ContradictionCheckFailsClosed asserts that when the
// contradiction detector cannot run (returns an error), the candidate is NOT
// promoted — it is skipped with the check-failed reason. This is the gate-3
// fail-closed guarantee.
func TestPromotion_ContradictionCheckFailsClosed(t *testing.T) {
	stmt := "rollback on payments reliably resolves the incident"
	tenants := []TenantLessons{
		{Tenant: "t1", Lessons: []domain.Lesson{lesson("a", "t1", stmt, 0.9, 2)}},
		{Tenant: "t2", Lessons: []domain.Lesson{lesson("b", "t2", stmt, 0.9, 2)}},
		{Tenant: "t3", Lessons: []domain.Lesson{lesson("c", "t3", stmt, 0.9, 2)}},
	}
	// A non-empty global forces the detector to run; the injected detector fails.
	global := fakeGlobal{claims: []domain.Claim{{ID: "gc1", Text: "unrelated global claim"}}}
	p := NewPromoter(global, nil)
	p.detect = func(string, []domain.Claim) (string, bool, error) {
		return "", false, errors.New("boom: detector unavailable")
	}

	res, err := p.Promote(context.Background(), tenants, Options{Gate: GateAuto})
	if err != nil {
		t.Fatalf("Promote should not surface the per-candidate detector error: %v", err)
	}
	if len(res.Promoted) != 0 || len(res.Pending) != 0 {
		t.Fatalf("candidate must NOT promote when the contradiction check fails, got %+v", res.Promoted)
	}
	s, ok := skipReason(res, "rollback on payments")
	if !ok || s.Reason != ReasonContradictionCheckFailed {
		t.Fatalf("candidate must be skipped with the check-failed reason, got %+v ok=%v", s, ok)
	}
}

// TestPromotion_OrderIndependent asserts the Result is invariant under any
// permutation of tenants and of lessons within a tenant — the guarantee the
// canonical sort + fixpoint merge exist to provide.
func TestPromotion_OrderIndependent(t *testing.T) {
	g := "rollback on payments reliably resolves the incident"
	h := "restart on billing clears the stuck queue"
	// Build a corpus: g in 3 tenants, h in 3 tenants, plus a single-tenant fact.
	base := []TenantLessons{
		{Tenant: "t1", Lessons: []domain.Lesson{lesson("g1", "t1", g, 0.9, 2), lesson("h1", "t1", h, 0.8, 1)}},
		{Tenant: "t2", Lessons: []domain.Lesson{lesson("h2", "t2", h, 0.85, 1), lesson("g2", "t2", g, 0.7, 3)}},
		{Tenant: "t3", Lessons: []domain.Lesson{lesson("g3", "t3", g, 0.75, 2), lesson("h3", "t3", h, 0.9, 1)}},
		{Tenant: "t4", Lessons: []domain.Lesson{lesson("s1", "t4", "solo fact about widgets nobody else saw", 0.9, 1)}},
	}
	p := NewPromoter(nil, nil)
	want, err := p.Promote(context.Background(), base, Options{Gate: GateAuto})
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}

	// Permutation: reverse tenant order and reverse lessons within each.
	shuffled := make([]TenantLessons, len(base))
	for i := range base {
		src := base[len(base)-1-i]
		rev := make([]domain.Lesson, len(src.Lessons))
		for j := range src.Lessons {
			rev[j] = src.Lessons[len(src.Lessons)-1-j]
		}
		shuffled[i] = TenantLessons{Tenant: src.Tenant, Lessons: rev}
	}
	got, err := p.Promote(context.Background(), shuffled, Options{Gate: GateAuto})
	if err != nil {
		t.Fatalf("Promote(shuffled): %v", err)
	}

	if !reflect.DeepEqual(want, got) {
		t.Fatalf("Result must be order-independent.\n want=%+v\n got =%+v", want, got)
	}
}

// TestPromotion_NoContentTokensRecorded asserts a lesson with no content tokens
// is not silently dropped — it lands in Skipped with the dedicated reason.
func TestPromotion_NoContentTokensRecorded(t *testing.T) {
	tenants := []TenantLessons{
		{Tenant: "t1", Lessons: []domain.Lesson{lesson("a", "t1", "the a is of to", 0.9, 1)}},
	}
	p := NewPromoter(nil, nil)
	res, err := p.Promote(context.Background(), tenants, Options{Gate: GateAuto})
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Reason != ReasonNoContentTokens {
		t.Fatalf("stop-word-only lesson must be recorded as skipped with no-content-tokens reason, got %+v", res.Skipped)
	}
}
