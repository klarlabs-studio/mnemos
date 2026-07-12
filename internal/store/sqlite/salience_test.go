package sqlite

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// TestSQLite_Salience_RoundTrip pins the ADR-0013 §4 persistence contract: a
// salience / stakes weight rides in the existing confidence_components column (no
// new column), survives an Upsert round-trip, and is writable on an existing claim
// via the BeliefCreditWriter component-writer capability without disturbing its
// trust score.
func TestSQLite_Salience_RoundTrip(t *testing.T) {
	db := openTestDB(t)
	defer closeDB(db)
	repo := NewClaimRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()

	c := domain.Claim{
		ID:         "cl_salient",
		Text:       "life-threatening fact",
		Type:       domain.ClaimTypeFact,
		Confidence: 0.7,
		Status:     domain.ClaimStatusActive,
		CreatedAt:  now,
		TrustScore: 0.42,
		ConfidenceComponents: map[string]float64{
			domain.SalienceComponentKey: 0.95,
			"corroboration":             0.5,
		},
	}
	if err := repo.Upsert(ctx, []domain.Claim{c}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got := loadOne(t, repo, "cl_salient")
	if v, ok := got.Salience(); !ok || v != 0.95 {
		t.Fatalf("salience round-trip lost: v=%.2f ok=%v (components=%+v)", v, ok, got.ConfidenceComponents)
	}

	// Update salience on the existing claim via the component writer, preserving
	// the other component and holding trust steady.
	merged := map[string]float64{}
	for k, v := range got.ConfidenceComponents {
		merged[k] = v
	}
	merged[domain.SalienceComponentKey] = 0.3
	if err := repo.ApplyBeliefCredit(ctx, "cl_salient", merged, got.TrustScore); err != nil {
		t.Fatalf("ApplyBeliefCredit: %v", err)
	}
	after := loadOne(t, repo, "cl_salient")
	if v, _ := after.Salience(); v != 0.3 {
		t.Fatalf("salience update not persisted: %.2f", v)
	}
	if after.ConfidenceComponents["corroboration"] != 0.5 {
		t.Fatalf("other components should be preserved: %+v", after.ConfidenceComponents)
	}
	if after.TrustScore != got.TrustScore {
		t.Fatalf("trust score should be unchanged by a salience write: %.4f -> %.4f", got.TrustScore, after.TrustScore)
	}
}

func loadOne(t *testing.T, repo ClaimRepository, id string) domain.Claim {
	t.Helper()
	cs, err := repo.ListByIDs(context.Background(), []string{id})
	if err != nil || len(cs) != 1 {
		t.Fatalf("load %s: err=%v n=%d", id, err, len(cs))
	}
	return cs[0]
}
