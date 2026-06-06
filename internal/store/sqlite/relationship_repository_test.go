package sqlite

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

func TestRelationshipRepositoryUpsertAndListByClaim(t *testing.T) {
	db := openTestDB(t)
	defer closeDB(db)

	claimRepo := NewClaimRepository(db)
	relRepo := NewRelationshipRepository(db)

	now := time.Date(2026, 4, 12, 16, 0, 0, 0, time.UTC)
	claims := []domain.Claim{
		{ID: "cl_a", Text: "A", Type: domain.ClaimTypeFact, Confidence: 0.8, Status: domain.ClaimStatusActive, CreatedAt: now},
		{ID: "cl_b", Text: "B", Type: domain.ClaimTypeFact, Confidence: 0.8, Status: domain.ClaimStatusActive, CreatedAt: now},
	}
	if err := claimRepo.Upsert(context.Background(), claims); err != nil {
		t.Fatalf("Upsert claims error = %v", err)
	}

	rel := domain.Relationship{
		ID:          "rl_1",
		Type:        domain.RelationshipTypeSupports,
		FromClaimID: "cl_a",
		ToClaimID:   "cl_b",
		CreatedAt:   now,
	}
	if err := relRepo.Upsert(context.Background(), []domain.Relationship{rel}); err != nil {
		t.Fatalf("Upsert relationships error = %v", err)
	}

	rels, err := relRepo.ListByClaim(context.Background(), "cl_a")
	if err != nil {
		t.Fatalf("ListByClaim error = %v", err)
	}
	if len(rels) != 1 {
		t.Fatalf("ListByClaim len = %d, want 1", len(rels))
	}
	if rels[0].Type != domain.RelationshipTypeSupports {
		t.Fatalf("ListByClaim type = %q, want supports", rels[0].Type)
	}
}
