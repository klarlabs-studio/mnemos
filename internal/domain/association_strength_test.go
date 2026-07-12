package domain

import (
	"testing"
	"time"
)

func TestAssociation_EffectiveStrength(t *testing.T) {
	cases := []struct {
		name   string
		stored float64
		want   float64
	}{
		{"unset reads as base 1.0", 0, 1},
		{"negative reads as base 1.0", -3, 1},
		{"positive is itself", 4.5, 4.5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := Association{Strength: tc.stored}
			if got := a.EffectiveStrength(); got != tc.want {
				t.Errorf("EffectiveStrength() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAssociation_Validate_RejectsNegativeStrength(t *testing.T) {
	base := Association{
		ID: "r1", Type: RelationshipTypeSupports,
		FromClaimID: "a", ToClaimID: "b", CreatedAt: time.Now(),
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid association rejected: %v", err)
	}
	bad := base
	bad.Strength = -1
	if err := bad.Validate(); err == nil {
		t.Error("negative strength should be rejected by Validate")
	}
	// Zero is fine (means unset / base).
	ok := base
	ok.Strength = 0
	if err := ok.Validate(); err != nil {
		t.Errorf("zero strength should be valid (unset), got %v", err)
	}
}
