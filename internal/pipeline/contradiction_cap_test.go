package pipeline

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/relate"
	"go.klarlabs.de/mnemos/internal/store"
)

// The per-claim contradiction cap must hold across writes, not only within one
// detection pass: a claim already holding the cap gains no new inferred
// contradiction, while explicit edges and re-writes of a stored pair pass.
func TestCapStoredContradictions_HoldsAcrossWrites(t *testing.T) {
	ctx := context.Background()
	conn, err := store.Open(ctx, "memory://")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	inferred := relate.ContradictionDerivedBy(relate.DetectorNumeric)
	var claims []domain.Claim
	for i := 0; i < relate.MaxContradictionsPerClaim+4; i++ {
		claims = append(claims, domain.Claim{ID: fmt.Sprintf("c%02d", i), Text: fmt.Sprintf("claim %d", i),
			Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive, CreatedAt: at})
	}
	claims = append(claims, domain.Claim{ID: "hub", Text: "hub claim", Type: domain.ClaimTypeFact,
		Status: domain.ClaimStatusActive, CreatedAt: at})
	if err := conn.Claims.Upsert(ctx, claims); err != nil {
		t.Fatal(err)
	}
	var stored []domain.Relationship
	for i := 0; i < relate.MaxContradictionsPerClaim; i++ {
		stored = append(stored, domain.Relationship{ID: fmt.Sprintf("old%d", i), Type: domain.RelationshipTypeContradicts,
			FromClaimID: "hub", ToClaimID: fmt.Sprintf("c%02d", i), CreatedAt: at, DerivedBy: inferred, Strength: 0.8})
	}
	if err := conn.Relationships.Upsert(ctx, stored); err != nil {
		t.Fatal(err)
	}

	n := relate.MaxContradictionsPerClaim
	incoming := []domain.Relationship{
		// Inferred, to a full claim: dropped.
		{ID: "new-over", Type: domain.RelationshipTypeContradicts, FromClaimID: fmt.Sprintf("c%02d", n), ToClaimID: "hub", DerivedBy: inferred, Strength: 0.9},
		// Re-write of a pair already stored (reversed direction): kept.
		{ID: "rewrite", Type: domain.RelationshipTypeContradicts, FromClaimID: "c00", ToClaimID: "hub", DerivedBy: inferred, Strength: 0.9},
		// Asserted by a caller, not inferred: written as given.
		{ID: "explicit", Type: domain.RelationshipTypeContradicts, FromClaimID: fmt.Sprintf("c%02d", n+1), ToClaimID: "hub"},
		// Inferred, between claims with room: kept.
		{ID: "fine", Type: domain.RelationshipTypeContradicts, FromClaimID: fmt.Sprintf("c%02d", n+2), ToClaimID: fmt.Sprintf("c%02d", n+3), DerivedBy: inferred, Strength: 0.7},
		// Not a contradiction: never capped.
		{ID: "sup", Type: domain.RelationshipTypeSupports, FromClaimID: fmt.Sprintf("c%02d", n+2), ToClaimID: "hub", DerivedBy: relate.ModelVersion},
	}
	out, dropped := CapStoredContradictions(ctx, conn.Relationships, incoming)
	if dropped != 1 {
		t.Fatalf("dropped %d, want 1", dropped)
	}
	got := map[string]bool{}
	for _, r := range out {
		got[r.ID] = true
	}
	for _, id := range []string{"rewrite", "explicit", "fine", "sup"} {
		if !got[id] {
			t.Errorf("%s was dropped", id)
		}
	}
	if got["new-over"] {
		t.Error("an inferred contradiction took the hub past the cap")
	}
}
