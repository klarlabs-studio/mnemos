package mnemos

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/credit"
	"go.klarlabs.de/mnemos/internal/domain"
)

// seedDecision records a decision backed by the given belief claim ids.
func seedDecision(t *testing.T, m *memory, id string, beliefs ...string) {
	t.Helper()
	if err := m.conn.Decisions.Append(context.Background(), domain.Decision{
		ID:        id,
		Statement: "decision " + id,
		RiskLevel: domain.RiskLevelMedium,
		Beliefs:   beliefs,
		ChosenAt:  time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed decision %s: %v", id, err)
	}
}

// seedObservedExpectation attaches a resolved prediction to a belief claim.
func seedObservedExpectation(t *testing.T, m *memory, claimID string, predicted, observed, tolerance float64) {
	t.Helper()
	if err := m.conn.Expectations.Upsert(context.Background(), domain.Expectation{
		ClaimID:        claimID,
		Predicted:      predicted,
		Observed:       observed,
		Tolerance:      tolerance,
		HasObservation: true,
		CreatedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed expectation %s: %v", claimID, err)
	}
}

func trustOf(t *testing.T, m *memory, id string) domain.Claim {
	t.Helper()
	cs, err := m.conn.Claims.ListByIDs(context.Background(), []string{id})
	if err != nil || len(cs) != 1 {
		t.Fatalf("load claim %s: err=%v n=%d", id, err, len(cs))
	}
	return cs[0]
}

func creditComponentSum(c domain.Claim) (sum float64, keys int) {
	for k, v := range c.ConfidenceComponents {
		if credit.IsCreditKey(k) {
			sum += v
			keys++
		}
	}
	return sum, keys
}

// TestConsolidate_CreditAssignment is the ADR-0014 acceptance test: a belief
// backing a decision whose expectation was REFUTED loses trust; VALIDATED gains;
// the delta is bounded and attributed; re-running is idempotent (no double
// credit); a belief with no resolved expectation is unchanged.
func TestConsolidate_CreditAssignment(t *testing.T) {
	m := calibMem(t)
	ctx := context.Background()

	// Three beliefs with identical confidence → identical evidence-based base
	// trust, so the credit effect is isolated by comparison against the control.
	const conf = 0.8
	seedClaim(t, m, "b_refuted", conf)
	seedClaim(t, m, "b_validated", conf)
	seedClaim(t, m, "b_control", conf) // no decision, no expectation

	seedDecision(t, m, "d_refuted", "b_refuted")
	seedDecision(t, m, "d_validated", "b_validated")

	seedObservedExpectation(t, m, "b_refuted", 10, 100, 5)  // far miss → refuted
	seedObservedExpectation(t, m, "b_validated", 10, 10, 5) // exact → validated

	res, err := m.Consolidate(ctx, ConsolidateOptions{AssignCredit: true})
	if err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	if res.Credited != 2 {
		t.Fatalf("Credited = %d, want 2 (refuted + validated)", res.Credited)
	}

	refuted := trustOf(t, m, "b_refuted")
	validated := trustOf(t, m, "b_validated")
	control := trustOf(t, m, "b_control")

	// Directional: refuted below control (base), validated above.
	if !(refuted.TrustScore < control.TrustScore) {
		t.Errorf("refuted trust %.4f should be < control (base) %.4f", refuted.TrustScore, control.TrustScore)
	}
	if !(validated.TrustScore > control.TrustScore) {
		t.Errorf("validated trust %.4f should be > control (base) %.4f", validated.TrustScore, control.TrustScore)
	}

	// Bounded: each single-belief decision moves trust by at most credit.Step.
	if d := control.TrustScore - refuted.TrustScore; d > credit.Step+1e-9 {
		t.Errorf("refuted delta %.4f exceeds Step %.4f", d, credit.Step)
	}
	if d := validated.TrustScore - control.TrustScore; d > credit.Step+1e-9 {
		t.Errorf("validated delta %.4f exceeds Step %.4f", d, credit.Step)
	}

	// Attributed: the trust change is recorded in confidence_components, keyed to
	// the driving decision + prediction — never a silent mutation.
	refSum, refKeys := creditComponentSum(refuted)
	if refKeys == 0 || refSum >= 0 {
		t.Errorf("refuted belief missing negative attributed credit: sum=%.4f keys=%d", refSum, refKeys)
	}
	if !hasCreditKeyFor(refuted, "d_refuted") {
		t.Errorf("refuted credit component not attributed to decision d_refuted: %v", refuted.ConfidenceComponents)
	}
	valSum, valKeys := creditComponentSum(validated)
	if valKeys == 0 || valSum <= 0 {
		t.Errorf("validated belief missing positive attributed credit: sum=%.4f keys=%d", valSum, valKeys)
	}

	// Unchanged: control belief carries no credit component.
	if _, ctrlKeys := creditComponentSum(control); ctrlKeys != 0 {
		t.Errorf("control belief should carry no credit component; got %v", control.ConfidenceComponents)
	}

	// Idempotent: a second pass produces byte-identical credit components and the
	// same trust — no double credit.
	res2, err := m.Consolidate(ctx, ConsolidateOptions{AssignCredit: true})
	if err != nil {
		t.Fatalf("Consolidate (2nd): %v", err)
	}
	if res2.Credited != 2 {
		t.Fatalf("2nd Credited = %d, want 2", res2.Credited)
	}
	refuted2 := trustOf(t, m, "b_refuted")
	validated2 := trustOf(t, m, "b_validated")
	if !approxEqual(refuted.TrustScore, refuted2.TrustScore, 1e-9) {
		t.Errorf("refuted trust not idempotent: %.6f -> %.6f", refuted.TrustScore, refuted2.TrustScore)
	}
	if !approxEqual(validated.TrustScore, validated2.TrustScore, 1e-9) {
		t.Errorf("validated trust not idempotent: %.6f -> %.6f", validated.TrustScore, validated2.TrustScore)
	}
	if s1, _ := creditComponentSum(refuted); !approxEqual(s1, refSum, 1e-12) {
		t.Errorf("refuted credit component doubled: %.6f -> %.6f", refSum, s1)
	}
}

// TestConsolidate_CreditDistributesToSibling verifies credit reaches a sibling
// belief that had no prediction of its own — the decision's whole belief set is
// credited, split equally.
func TestConsolidate_CreditDistributesToSibling(t *testing.T) {
	m := calibMem(t)
	ctx := context.Background()

	seedClaim(t, m, "predictor", 0.8)
	seedClaim(t, m, "sibling", 0.8)
	seedDecision(t, m, "d_multi", "predictor", "sibling")
	seedObservedExpectation(t, m, "predictor", 10, 100, 5) // refuted → both blamed

	if _, err := m.Consolidate(ctx, ConsolidateOptions{AssignCredit: true}); err != nil {
		t.Fatalf("Consolidate: %v", err)
	}

	sibSum, sibKeys := creditComponentSum(trustOf(t, m, "sibling"))
	if sibKeys == 0 || sibSum >= 0 {
		t.Errorf("sibling belief should be blamed via distribution: sum=%.4f keys=%d", sibSum, sibKeys)
	}
	predSum, _ := creditComponentSum(trustOf(t, m, "predictor"))
	if !approxEqual(sibSum, predSum, 1e-9) {
		t.Errorf("equal split expected: predictor=%.4f sibling=%.4f", predSum, sibSum)
	}
}

func hasCreditKeyFor(c domain.Claim, decisionID string) bool {
	for k := range c.ConfidenceComponents {
		if credit.IsCreditKey(k) && strings.Contains(k, decisionID) {
			return true
		}
	}
	return false
}

func approxEqual(a, b, eps float64) bool { return math.Abs(a-b) < eps }
