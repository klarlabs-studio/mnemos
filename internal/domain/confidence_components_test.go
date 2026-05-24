package domain

import (
	"strings"
	"testing"
	"time"
)

func validClaim(components map[string]float64) Claim {
	return Claim{
		ID:                   "cl_x",
		Text:                 "x",
		Type:                 ClaimTypeFact,
		Confidence:           0.7,
		Status:               ClaimStatusActive,
		CreatedAt:            time.Now().UTC(),
		ConfidenceComponents: components,
	}
}

// TestClaim_ValidateConfidenceComponents pins the per-component
// validation surface: each value must be in [0, 1] and keys must be
// non-empty. Otherwise an inbound payload could silently smuggle
// nonsense into the column.
func TestClaim_ValidateConfidenceComponents(t *testing.T) {
	t.Parallel()
	t.Run("happy path", func(t *testing.T) {
		c := validClaim(map[string]float64{
			"data_quality":     0.9,
			"corroboration":    0.5,
			"recency":          1.0,
			"source_authority": 0.0,
		})
		if err := c.Validate(); err != nil {
			t.Errorf("valid components rejected: %v", err)
		}
	})
	t.Run("empty key", func(t *testing.T) {
		c := validClaim(map[string]float64{"": 0.5})
		if err := c.Validate(); err == nil {
			t.Error("expected error on empty key")
		}
	})
	t.Run("whitespace key", func(t *testing.T) {
		c := validClaim(map[string]float64{"   ": 0.5})
		if err := c.Validate(); err == nil {
			t.Error("expected error on whitespace-only key")
		}
	})
	t.Run("value below zero", func(t *testing.T) {
		c := validClaim(map[string]float64{"x": -0.1})
		err := c.Validate()
		if err == nil {
			t.Fatal("expected error on negative component")
		}
		if !strings.Contains(err.Error(), "x") {
			t.Errorf("error should name the bad key, got %v", err)
		}
	})
	t.Run("value above one", func(t *testing.T) {
		if err := validClaim(map[string]float64{"x": 1.5}).Validate(); err == nil {
			t.Error("expected error on >1 component")
		}
	})
	t.Run("nil components ok", func(t *testing.T) {
		if err := validClaim(nil).Validate(); err != nil {
			t.Errorf("nil components should be valid: %v", err)
		}
	})
	t.Run("empty map ok", func(t *testing.T) {
		if err := validClaim(map[string]float64{}).Validate(); err != nil {
			t.Errorf("empty components should be valid: %v", err)
		}
	})
}
