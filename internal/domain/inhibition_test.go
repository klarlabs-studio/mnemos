package domain

import "testing"

func TestBelief_EffectiveInhibition(t *testing.T) {
	// Absent → 0 (inert).
	if got := (Belief{}).EffectiveInhibition(); got != 0 {
		t.Errorf("unset inhibition should read 0, got %v", got)
	}
	// Present → itself.
	b := Belief{ConfidenceComponents: map[string]float64{InhibitionComponentKey: 0.4}}
	if got := b.EffectiveInhibition(); got != 0.4 {
		t.Errorf("EffectiveInhibition = %v, want 0.4", got)
	}
	// Out-of-range clamps defensively.
	over := Belief{ConfidenceComponents: map[string]float64{InhibitionComponentKey: 5}}
	if got := over.EffectiveInhibition(); got != 1 {
		t.Errorf("over-range inhibition should clamp to 1, got %v", got)
	}
	// A plain (non-credit) inhibition component in [0,1] passes Validate.
	c := Claim{ID: "c1", Text: "x", Type: ClaimTypeFact, Confidence: 0.8, Status: ClaimStatusActive,
		ConfidenceComponents: map[string]float64{InhibitionComponentKey: 0.3}}
	if err := c.Validate(); err != nil {
		t.Errorf("a [0,1] inhibition component should validate, got %v", err)
	}
}
