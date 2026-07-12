package consolidate

import (
	"context"
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
)

func schema(id, stmt string) domain.Schema {
	return domain.Schema{ID: id, Statement: stmt}
}

// TestAssimilation_ConsistentGetsBoundedBoost is the core ADR 0013 §5 signal: an
// item consistent with an existing schema earns a bounded, additive boost (faster
// consolidation), while an unrelated item earns none.
func TestAssimilation_ConsistentGetsBoundedBoost(t *testing.T) {
	schemas := []domain.Schema{schema("s1", "rollback on payments reliably succeeds")}

	consistent := AssessAssimilation("rollback on payments reliably succeeds consistently", schemas, AssimilationOptions{})
	unrelated := AssessAssimilation("cats prefer warm windowsills during winter", schemas, AssimilationOptions{})

	if !consistent.Consistent() {
		t.Fatalf("schema-consistent item should assimilate, got fit=%q sim=%.3f", consistent.Fit, consistent.Similarity)
	}
	if consistent.Boost <= 0 {
		t.Fatalf("consistent item should earn a positive boost, got %.3f", consistent.Boost)
	}
	if consistent.Boost > MaxAssimilationBoost {
		t.Fatalf("boost must be bounded by %.3f, got %.3f", MaxAssimilationBoost, consistent.Boost)
	}
	if unrelated.Consistent() {
		t.Fatalf("unrelated item should not assimilate, got fit=%q", unrelated.Fit)
	}
	if unrelated.Boost != 0 {
		t.Fatalf("unrelated item must earn no boost, got %.3f", unrelated.Boost)
	}
	if consistent.Boost <= unrelated.Boost {
		t.Fatalf("consistent boost (%.3f) must exceed unrelated boost (%.3f) — faster consolidation", consistent.Boost, unrelated.Boost)
	}
}

// TestAssimilation_ContradictionRoutesToAccommodation: an item that contradicts an
// existing schema (same topic, opposite polarity) is flagged for accommodation
// and earns NO boost — it must not be silently assimilated.
func TestAssimilation_ContradictionRoutesToAccommodation(t *testing.T) {
	schemas := []domain.Schema{schema("s1", "rollback on payments reliably succeeds")}

	sig := AssessAssimilation("rollback on payments does not reliably succeed", schemas, AssimilationOptions{})

	if !sig.NeedsAccommodation() {
		t.Fatalf("contradicting item should route to accommodation, got fit=%q", sig.Fit)
	}
	if sig.Consistent() {
		t.Fatalf("contradicting item must not be marked consistent")
	}
	if sig.Boost != 0 {
		t.Fatalf("contradicting item must earn no boost, got %.3f", sig.Boost)
	}
	if sig.MatchStatement == "" {
		t.Fatalf("dissonant signal should record the conflicting schema for audit")
	}
}

// TestAssimilation_NeutralWhenNothingToMatch: no schemas, or an item with no
// content tokens, yields a neutral signal with no boost.
func TestAssimilation_NeutralWhenNothingToMatch(t *testing.T) {
	noSchemas := AssessAssimilation("rollback on payments reliably succeeds", nil, AssimilationOptions{})
	if noSchemas.Fit != FitNeutral || noSchemas.Boost != 0 {
		t.Fatalf("no schemas → neutral/no boost, got fit=%q boost=%.3f", noSchemas.Fit, noSchemas.Boost)
	}

	schemas := []domain.Schema{schema("s1", "rollback on payments reliably succeeds")}
	noTokens := AssessAssimilation("the a of to in on", schemas, AssimilationOptions{})
	if noTokens.Fit != FitNeutral || noTokens.Boost != 0 {
		t.Fatalf("token-less item → neutral/no boost, got fit=%q boost=%.3f", noTokens.Fit, noTokens.Boost)
	}
}

// TestAssimilation_BoostNeverBypassesEligibilityOrNoLeak proves the boost is not a
// backdoor: a maximally schema-consistent, high-confidence lesson still cannot
// promote if its subject is individual (ADR-0012 eligibility gate) or if it was
// seen in only one tenant (ADR-0007 no-leak gate). The assimilation signal
// accelerates consolidation; it never touches these gates.
func TestAssimilation_BoostNeverBypassesEligibilityOrNoLeak(t *testing.T) {
	fact := "aspirin is toxic to cats"

	// Sanity: this item IS schema-consistent, so absent the gates it would be
	// accelerated.
	sig := AssessAssimilation(fact, []domain.Schema{schema("g1", fact)}, AssimilationOptions{})
	if !sig.Consistent() || sig.Boost <= 0 {
		t.Fatalf("precondition: item should be schema-consistent, got fit=%q boost=%.3f", sig.Fit, sig.Boost)
	}

	ctx := context.Background()
	p := NewPromoter(nil, nil)

	// Eligibility gate: an INDIVIDUAL-subject lesson, corroborated in 3 tenants and
	// at maximum (boosted) confidence, still never promotes.
	var indiv []TenantLessons
	for _, tn := range []string{"a", "b", "c"} {
		indiv = append(indiv, TenantLessons{
			Tenant:  tn,
			Lessons: []domain.Lesson{lessonWithClass("i_"+tn, tn, fact, 0.99, 3, domain.SubjectClassIndividual)},
		})
	}
	res, err := p.Promote(ctx, indiv, Options{Gate: GateAuto})
	if err != nil {
		t.Fatalf("promote (individual): %v", err)
	}
	if len(res.Promoted) != 0 || len(res.Pending) != 0 {
		t.Fatalf("individual-subject lesson must never promote despite max confidence: promoted=%d pending=%d", len(res.Promoted), len(res.Pending))
	}

	// No-leak gate: a class-level, schema-consistent, max-confidence lesson seen in
	// a SINGLE tenant still never promotes.
	single := []TenantLessons{{
		Tenant:  "only",
		Lessons: []domain.Lesson{lessonWithClass("c1", "only", fact, 0.99, 3, domain.SubjectClassClass)},
	}}
	res2, err := p.Promote(ctx, single, Options{Gate: GateAuto})
	if err != nil {
		t.Fatalf("promote (single tenant): %v", err)
	}
	if len(res2.Promoted) != 0 || len(res2.Pending) != 0 {
		t.Fatalf("single-tenant lesson must never promote despite max confidence: promoted=%d pending=%d", len(res2.Promoted), len(res2.Pending))
	}
}
