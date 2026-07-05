package mnemos_test

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos"

	_ "go.klarlabs.de/mnemos/internal/store/memory"
)

func TestEffortBudget(t *testing.T) {
	cases := []struct {
		stakes, confidence, want float64
	}{
		{1.0, 0.0, 1.0},  // max stakes, no confidence → invest fully
		{1.0, 1.0, 0.0},  // max stakes, fully confident → nothing to gain
		{0.0, 0.0, 0.0},  // no stakes → never invest, however thin
		{0.5, 0.5, 0.25}, // middling both
		{1.0, 0.35, 0.65},
		{-1, 2, 0.0}, // clamped: stakes→0
		{2, -1, 2.0}, // clamped: stakes→1, confidence→0 → 1*(1-0)=1... see below
	}
	for _, c := range cases {
		got := mnemos.EffortBudget(c.stakes, c.confidence)
		// Recompute the clamped expectation for the clamp cases.
		want := c.want
		if c.stakes == 2 && c.confidence == -1 {
			want = 1.0 // stakes clamps to 1, confidence to 0 → 1*(1-0)
		}
		if !approxEff(got, want) {
			t.Errorf("EffortBudget(%v,%v) = %v, want %v", c.stakes, c.confidence, got, want)
		}
	}
}

func approxEff(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-9
}

// TestRecallWithEffort_LowStakesStaysCheap verifies that with zero stakes the
// budget is zero, so no escalation happens even when memory is thin.
func TestRecallWithEffort_LowStakesStaysCheap(t *testing.T) {
	mem := newReadMemory(t, "effort_cheap")
	ctx := context.Background()
	// A thin corpus: nothing matches the query.
	seedClaim(t, mem, "The mailer runs in eu-central-1.", time.Now())

	_, _, report, err := mem.RecallWithEffort(ctx, mnemos.Query{Text: "no such topic here"}, 0.0)
	if err != nil {
		t.Fatalf("RecallWithEffort: %v", err)
	}
	if report.Budget != 0 {
		t.Errorf("Budget = %v, want 0 at zero stakes", report.Budget)
	}
	if report.Passes != 1 {
		t.Errorf("Passes = %d, want 1 (no escalation at zero stakes)", report.Passes)
	}
}

// TestRecallWithEffort_SufficientNoEscalation verifies that when the first pass
// is already sufficient, no escalation happens even at maximum stakes — you
// don't spend effort you don't need. (Aggregate confidence is density-dominated,
// so enough corroborating claims clears the floor.)
func TestRecallWithEffort_SufficientNoEscalation(t *testing.T) {
	mem := newReadMemory(t, "effort_sufficient")
	ctx := context.Background()
	now := time.Now()
	for _, txt := range []string{
		"The checkout service latency spikes after a deploy.",
		"Checkout latency spikes correlate with the deploy rollout.",
		"After each checkout deploy, latency spikes for ten minutes.",
		"Deploys to checkout cause a transient latency spike.",
		"Checkout latency returns to baseline once the deploy settles.",
	} {
		seedClaim(t, mem, txt, now)
	}
	_, suf, report, err := mem.RecallWithEffort(ctx, mnemos.Query{Text: "checkout deploy latency spike"}, 1.0)
	if err != nil {
		t.Fatalf("RecallWithEffort: %v", err)
	}
	if !suf.Sufficient {
		t.Fatalf("precondition: expected a sufficient first pass, got confidence %.3f (%d claims)", suf.Confidence, suf.ClaimCount)
	}
	if report.Passes != 1 {
		t.Errorf("Passes = %d, want 1 (already sufficient — no escalation)", report.Passes)
	}
}

// TestRecallWithEffort_HighStakesEscalates verifies the EV-of-control trigger: an
// insufficient first pass at high stakes spends a second, wider+deeper pass. A
// query that matches nothing is guaranteed insufficient (0 claims → confidence
// 0), so the escalation decision is deterministic regardless of the confidence
// formula. The result is non-regressive.
func TestRecallWithEffort_HighStakesEscalates(t *testing.T) {
	mem := newReadMemory(t, "effort_escalate")
	ctx := context.Background()
	// A non-empty corpus, but nothing matches the query.
	seedClaim(t, mem, "The billing service batches invoices nightly.", time.Now())

	q := mnemos.Query{Text: "helicopter rotor blade inspection cadence"}
	_, baseSuf, err := mem.RecallWithSufficiency(ctx, q)
	if err != nil {
		t.Fatalf("RecallWithSufficiency: %v", err)
	}
	if baseSuf.Sufficient {
		t.Fatalf("precondition: expected an insufficient first pass, got %d claims", baseSuf.ClaimCount)
	}

	results, suf, report, err := mem.RecallWithEffort(ctx, q, 1.0)
	if err != nil {
		t.Fatalf("RecallWithEffort: %v", err)
	}
	if report.Budget < mnemos.EffortEscalationThreshold {
		t.Errorf("Budget = %.3f, want >= %.2f (max stakes, zero confidence)", report.Budget, mnemos.EffortEscalationThreshold)
	}
	if report.Passes != 2 {
		t.Fatalf("Passes = %d, want 2 (high stakes + insufficient → escalate)", report.Passes)
	}
	// Non-regressive: never fewer claims or lower confidence than the first pass.
	if len(results) < baseSuf.ClaimCount {
		t.Errorf("escalated returned %d claims, fewer than base %d (regressed)", len(results), baseSuf.ClaimCount)
	}
	if suf.Confidence < baseSuf.Confidence {
		t.Errorf("escalated confidence %.3f < base %.3f (regressed)", suf.Confidence, baseSuf.Confidence)
	}
}
