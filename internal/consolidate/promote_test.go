package consolidate

import (
	"context"
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

	tenants := []TenantLessons{
		{Tenant: "alpha", Lessons: []domain.Lesson{
			lesson("f1", "alpha", factF, 0.9, 4),
			lesson("g1", "alpha", factG, 0.8, 3),
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
		"low1": 0.1, "low2": 0.1, "low3": 0.1, // aggregate 0.3
		"high1": 0.9, "high2": 0.9, "high3": 0.9, // aggregate 2.7
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
