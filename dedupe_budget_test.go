package mnemos

import (
	"testing"
	"time"
)

// The dedupe scan is quadratic, so an unset budget must not mean "unlimited":
// that is how a plain consolidate came to run for over an hour. Unlimited is
// opt-in (negative), the default is bounded.
func TestDedupeScanBudget(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"unset gets the default", 0, defaultDedupeScanBudget},
		{"explicit budget is kept", 90 * time.Second, 90 * time.Second},
		{"negative opts out of the budget", -1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := dedupeScanBudget(ConsolidateOptions{DedupeScanBudget: tc.in}); got != tc.want {
				t.Fatalf("dedupeScanBudget(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
