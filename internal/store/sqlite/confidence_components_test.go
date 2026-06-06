package sqlite

import (
	"context"
	"reflect"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// TestSQLite_ClaimConfidenceComponents_RoundTrip pins the persistence
// contract for #39: a claim saved with named components loads back
// with the same map, and a claim saved without any components loads
// back with a nil map (the explicit "no decomposition surfaced"
// state, not a zero-valued map).
func TestSQLite_ClaimConfidenceComponents_RoundTrip(t *testing.T) {
	db := openTestDB(t)
	defer closeDB(db)
	repo := NewClaimRepository(db)

	ctx := context.Background()
	now := time.Now().UTC()

	withComponents := domain.Claim{
		ID:         "cl_components",
		Text:       "fact with components",
		Type:       domain.ClaimTypeFact,
		Confidence: 0.78,
		Status:     domain.ClaimStatusActive,
		CreatedAt:  now,
		ConfidenceComponents: map[string]float64{
			"data_quality":     0.9,
			"recency":          0.7,
			"corroboration":    0.5,
			"source_authority": 0.8,
		},
	}
	plain := domain.Claim{
		ID:         "cl_plain",
		Text:       "fact without components",
		Type:       domain.ClaimTypeFact,
		Confidence: 0.6,
		Status:     domain.ClaimStatusActive,
		CreatedAt:  now,
	}
	if err := repo.Upsert(ctx, []domain.Claim{withComponents, plain}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := repo.ListByIDs(ctx, []string{"cl_components", "cl_plain"})
	if err != nil {
		t.Fatalf("ListByIDs: %v", err)
	}
	byID := map[string]domain.Claim{}
	for _, c := range got {
		byID[c.ID] = c
	}
	if !reflect.DeepEqual(byID["cl_components"].ConfidenceComponents, withComponents.ConfidenceComponents) {
		t.Errorf("components round-trip lost data: got %+v, want %+v",
			byID["cl_components"].ConfidenceComponents, withComponents.ConfidenceComponents)
	}
	if byID["cl_plain"].ConfidenceComponents != nil {
		t.Errorf("plain claim loaded with non-nil components: %+v", byID["cl_plain"].ConfidenceComponents)
	}
}
