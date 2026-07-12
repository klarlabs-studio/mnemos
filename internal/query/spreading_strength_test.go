package query

import "testing"

// TestStrengthActivationFactor pins the ADR-0015 §4 weighting curve: a base-strength
// edge is neutral (so a never-co-activated graph primes exactly as before), and the
// boost rises monotonically with strength up to a bounded saturation.
func TestStrengthActivationFactor(t *testing.T) {
	if f := strengthActivationFactor(1); f != 1 {
		t.Errorf("base strength must be neutral (1.0), got %v", f)
	}
	if f := strengthActivationFactor(0); f != 1 {
		t.Errorf("unset/zero strength must be neutral (1.0), got %v", f)
	}
	mid := strengthActivationFactor(5)
	if mid <= 1 || mid >= 1+strengthActivationBoost {
		t.Errorf("mid strength factor %v should be strictly between 1 and %v", mid, 1+strengthActivationBoost)
	}
	if strengthActivationFactor(5) <= strengthActivationFactor(3) {
		t.Error("factor should increase monotonically with strength")
	}
	top := strengthActivationFactor(strengthActivationCap)
	if top != 1+strengthActivationBoost {
		t.Errorf("factor at cap should be 1+boost=%v, got %v", 1+strengthActivationBoost, top)
	}
	if strengthActivationFactor(1000) != top {
		t.Error("factor must saturate at the cap, not exceed it")
	}
}
