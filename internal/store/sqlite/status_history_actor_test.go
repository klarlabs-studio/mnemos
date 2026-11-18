package sqlite

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// The per-claim status history must return who made each transition, as the
// Postgres, MySQL and memory backends do. It used to select every column but
// changed_by, so the actor read back empty on SQLite only.
func TestClaimRepositoryStatusHistory_ReturnsChangedBy(t *testing.T) {
	db := openTestDB(t)
	defer closeDB(db)
	ctx := context.Background()
	repo := NewClaimRepository(db)

	claim := domain.Claim{
		ID: "cl_actor", Text: "who changed me", Type: domain.ClaimTypeFact,
		Confidence: 0.8, Status: domain.ClaimStatusActive, CreatedAt: time.Now().UTC(),
	}
	if err := repo.UpsertWithReasonAs(ctx, []domain.Claim{claim}, "seed", "alice"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	claim.Status = domain.ClaimStatusDeprecated
	if err := repo.UpsertWithReasonAs(ctx, []domain.Claim{claim}, "wrong", "bob"); err != nil {
		t.Fatalf("deprecate: %v", err)
	}
	hist, err := repo.ListStatusHistoryByClaimID(ctx, "cl_actor")
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(hist) != 2 || hist[0].ChangedBy != "alice" || hist[1].ChangedBy != "bob" {
		t.Fatalf("history = %+v, want transitions by alice then bob", hist)
	}
}
