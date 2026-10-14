package store_test

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// A detector's strength is stored with the edge it inferred (relate/v4). An
// edge written without one reads as the base 1.0, as before.
func TestRelationships_InitialStrengthIsStored(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for _, b := range openBackends(t) {
		claims := []domain.Claim{
			{ID: "st0", Text: "c", Type: domain.ClaimTypeFact, Confidence: 0.5, Status: domain.ClaimStatusActive, CreatedAt: at},
			{ID: "st1", Text: "c", Type: domain.ClaimTypeFact, Confidence: 0.5, Status: domain.ClaimStatusActive, CreatedAt: at},
			{ID: "st2", Text: "c", Type: domain.ClaimTypeFact, Confidence: 0.5, Status: domain.ClaimStatusActive, CreatedAt: at},
		}
		if err := b.conn.Claims.Upsert(ctx, claims); err != nil {
			t.Fatal(err)
		}
		edges := []domain.Relationship{
			{ID: "st-scored", Type: domain.RelationshipTypeContradicts, FromClaimID: "st0", ToClaimID: "st1", CreatedAt: at,
				DerivedBy: "relate/v4:numeric", Strength: 0.72},
			{ID: "st-unset", Type: domain.RelationshipTypeSupports, FromClaimID: "st1", ToClaimID: "st2", CreatedAt: at},
		}
		if err := b.conn.Relationships.Upsert(ctx, edges); err != nil {
			t.Fatalf("%s: %v", b.name, err)
		}
		got, err := b.conn.Relationships.ListByClaimIDs(ctx, []string{"st0", "st1", "st2"})
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]float64{"st-scored": 0.72, "st-unset": 1}
		seen := 0
		for _, r := range got {
			w, ok := want[r.ID]
			if !ok {
				continue
			}
			seen++
			if s := r.EffectiveStrength(); s != w {
				t.Errorf("%s: %s strength %v, want %v", b.name, r.ID, s, w)
			}
		}
		if seen != len(want) {
			t.Errorf("%s: read back %d of %d edges", b.name, seen, len(want))
		}
	}
}
