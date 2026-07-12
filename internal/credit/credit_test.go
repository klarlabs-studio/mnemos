package credit

import (
	"math"
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
)

func obsExp(claimID string, predicted, observed, tolerance float64) domain.Expectation {
	return domain.Expectation{
		ClaimID:        claimID,
		Predicted:      predicted,
		Observed:       observed,
		Tolerance:      tolerance,
		HasObservation: true,
	}
}

// TestAssign_ValidatedCreditsRefutedBlames is the core of ADR 0014: a decision
// whose prediction was borne out credits its belief (positive delta); one whose
// prediction was refuted blames it (negative delta).
func TestAssign_ValidatedCreditsRefutedBlames(t *testing.T) {
	decisions := []domain.Decision{
		{ID: "d_val", Beliefs: []string{"b_val"}},
		{ID: "d_ref", Beliefs: []string{"b_ref"}},
	}
	exps := map[string]domain.Expectation{
		"b_val": obsExp("b_val", 10, 10, 5),  // exact hit → validated
		"b_ref": obsExp("b_ref", 10, 100, 5), // far miss → refuted
	}

	got := Assign(decisions, exps)

	valSum := SumFor(got["b_val"])
	refSum := SumFor(got["b_ref"])
	if valSum <= 0 {
		t.Errorf("validated belief credit = %v, want > 0", valSum)
	}
	if refSum >= 0 {
		t.Errorf("refuted belief credit = %v, want < 0", refSum)
	}
	// A single-belief decision gets the full ±Step.
	if !approxEq(valSum, Step, 1e-9) {
		t.Errorf("validated single-belief credit = %v, want %v", valSum, Step)
	}
	if !approxEq(refSum, -Step, 1e-9) {
		t.Errorf("refuted single-belief credit = %v, want %v", refSum, -Step)
	}
}

// TestAssign_EqualSplit verifies a decision distributes its credit equally across
// all beliefs — including a sibling belief that carries no expectation of its own.
func TestAssign_EqualSplit(t *testing.T) {
	decisions := []domain.Decision{
		{ID: "d", Beliefs: []string{"predictor", "sibling"}},
	}
	exps := map[string]domain.Expectation{
		"predictor": obsExp("predictor", 10, 10, 5), // validated, full +Step for the decision
	}

	got := Assign(decisions, exps)

	wantEach := Step / 2
	if s := SumFor(got["predictor"]); !approxEq(s, wantEach, 1e-9) {
		t.Errorf("predictor credit = %v, want %v (half split)", s, wantEach)
	}
	if s := SumFor(got["sibling"]); !approxEq(s, wantEach, 1e-9) {
		t.Errorf("sibling credit = %v, want %v (half split)", s, wantEach)
	}
}

// TestAssign_NoObservationNoCredit verifies an unobserved (or absent) expectation
// leaves the belief unchanged — no error has been measured yet.
func TestAssign_NoObservationNoCredit(t *testing.T) {
	decisions := []domain.Decision{
		{ID: "d1", Beliefs: []string{"open"}},   // expectation exists but no observation
		{ID: "d2", Beliefs: []string{"nopred"}}, // no expectation at all
	}
	exps := map[string]domain.Expectation{
		"open": {ClaimID: "open", Predicted: 10, Tolerance: 5}, // HasObservation false
	}

	got := Assign(decisions, exps)
	if len(got) != 0 {
		t.Errorf("no observed expectation should yield no contributions; got %v", got)
	}
}

// TestAssign_Deterministic verifies the pure function is order-independent and
// stable — the basis for idempotent re-application.
func TestAssign_Deterministic(t *testing.T) {
	exps := map[string]domain.Expectation{
		"b1": obsExp("b1", 10, 12, 5),
		"b2": obsExp("b2", 10, 40, 5),
	}
	a := Assign([]domain.Decision{{ID: "z", Beliefs: []string{"b1"}}, {ID: "a", Beliefs: []string{"b2"}}}, exps)
	b := Assign([]domain.Decision{{ID: "a", Beliefs: []string{"b2"}}, {ID: "z", Beliefs: []string{"b1"}}}, exps)
	if SumFor(a["b1"]) != SumFor(b["b1"]) || SumFor(a["b2"]) != SumFor(b["b2"]) {
		t.Errorf("Assign not order-independent: %v vs %v", a, b)
	}
}

// TestSumFor_ClampsToCreditCap verifies accumulated credit cannot run away from
// the evidence-based base — the decayed/bounded guarantee.
func TestSumFor_ClampsToCreditCap(t *testing.T) {
	var many []Contribution
	for i := 0; i < 100; i++ {
		many = append(many, Contribution{Key: "credit:x", Delta: Step})
	}
	if s := SumFor(many); s != CreditCap {
		t.Errorf("SumFor over cap = %v, want %v", s, CreditCap)
	}
	for i := range many {
		many[i].Delta = -Step
	}
	if s := SumFor(many); s != -CreditCap {
		t.Errorf("SumFor under cap = %v, want %v", s, -CreditCap)
	}
}

// TestDecisionDelta_Bounds checks the signed error stays within [-Step, +Step]
// and moves in the right direction with surprise.
func TestDecisionDelta_Bounds(t *testing.T) {
	cases := []struct {
		name                string
		predicted, obs, tol float64
		wantSign            int
	}{
		{"exact hit", 10, 10, 5, +1},
		{"within tolerance", 10, 12, 5, +1},
		{"at edge", 10, 15, 5, 0},
		{"just over", 10, 16, 5, -1},
		{"far miss", 10, 1000, 5, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, ok := decisionDelta(obsExp("c", tc.predicted, tc.obs, tc.tol))
			if !ok {
				t.Fatal("decisionDelta not ok")
			}
			if math.Abs(d) > Step+1e-9 {
				t.Errorf("delta %v exceeds Step %v", d, Step)
			}
			gotSign := 0
			switch {
			case d > 1e-9:
				gotSign = +1
			case d < -1e-9:
				gotSign = -1
			}
			if gotSign != tc.wantSign {
				t.Errorf("delta %v sign = %d, want %d", d, gotSign, tc.wantSign)
			}
		})
	}
}

func approxEq(a, b, eps float64) bool { return math.Abs(a-b) < eps }
