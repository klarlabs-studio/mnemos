package consolidate

import (
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
)

func TestSaliencePriorityMultiplier_NeutralIsIdentity(t *testing.T) {
	if got := SaliencePriorityMultiplier(domain.NeutralSalience); got != 1.0 {
		t.Fatalf("neutral salience should be a 1.0 multiplier, got %.4f", got)
	}
}

func TestSaliencePriorityMultiplier_MonotonicAndBounded(t *testing.T) {
	lo := SaliencePriorityMultiplier(0)
	mid := SaliencePriorityMultiplier(0.5)
	hi := SaliencePriorityMultiplier(1)
	if !(lo < mid && mid < hi) {
		t.Fatalf("multiplier must increase with stakes: %.3f %.3f %.3f", lo, mid, hi)
	}
	// Bounded to 1±weight and strictly positive across the whole range.
	if lo != 1-SaliencePriorityWeight || hi != 1+SaliencePriorityWeight {
		t.Fatalf("bounds wrong: lo=%.3f hi=%.3f want %.3f/%.3f", lo, hi, 1-SaliencePriorityWeight, 1+SaliencePriorityWeight)
	}
	if lo <= 0 {
		t.Fatalf("multiplier must stay strictly positive, got %.3f", lo)
	}
	// Defensive clamp on out-of-range input.
	if SaliencePriorityMultiplier(5) != hi || SaliencePriorityMultiplier(-5) != lo {
		t.Fatalf("out-of-range input should clamp")
	}
}
