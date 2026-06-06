package sqlite

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

func TestClaimRepositoryStatusHistory_RecordsInitialInsertThenTransitions(t *testing.T) {
	db := openTestDB(t)
	defer closeDB(db)

	ctx := context.Background()
	repo := NewClaimRepository(db)

	now := time.Date(2026, 4, 18, 9, 0, 0, 0, time.UTC)
	claim := domain.Claim{
		ID:         "cl_hist",
		Text:       "Lifecycle test",
		Type:       domain.ClaimTypeFact,
		Confidence: 0.8,
		Status:     domain.ClaimStatusActive,
		CreatedAt:  now,
	}

	// Initial insert → history row with from_status = "".
	if err := repo.Upsert(ctx, []domain.Claim{claim}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	hist, err := repo.ListStatusHistoryByClaimID(ctx, "cl_hist")
	if err != nil {
		t.Fatalf("list hist: %v", err)
	}
	if len(hist) != 1 || hist[0].FromStatus != "" || hist[0].ToStatus != domain.ClaimStatusActive {
		t.Fatalf("after insert: got %+v, want single ('' -> active) row", hist)
	}

	// Same-status upsert → no new row.
	if err := repo.Upsert(ctx, []domain.Claim{claim}); err != nil {
		t.Fatalf("re-upsert same status: %v", err)
	}
	hist, _ = repo.ListStatusHistoryByClaimID(ctx, "cl_hist")
	if len(hist) != 1 {
		t.Fatalf("after no-op upsert: %d rows, want 1", len(hist))
	}

	// Status change → new row with from_status = active.
	claim.Status = domain.ClaimStatusContested
	if err := repo.UpsertWithReason(ctx, []domain.Claim{claim}, "contradicts cl_other"); err != nil {
		t.Fatalf("contested upsert: %v", err)
	}
	hist, _ = repo.ListStatusHistoryByClaimID(ctx, "cl_hist")
	if len(hist) != 2 {
		t.Fatalf("after contest: %d rows, want 2", len(hist))
	}
	if hist[1].FromStatus != domain.ClaimStatusActive || hist[1].ToStatus != domain.ClaimStatusContested {
		t.Fatalf("bad second transition: %+v", hist[1])
	}
	if hist[1].Reason != "contradicts cl_other" {
		t.Fatalf("reason not captured: %q", hist[1].Reason)
	}

	// Resolved → one more row.
	claim.Status = domain.ClaimStatusResolved
	if err := repo.UpsertWithReason(ctx, []domain.Claim{claim}, "operator chose winner"); err != nil {
		t.Fatalf("resolve upsert: %v", err)
	}
	hist, _ = repo.ListStatusHistoryByClaimID(ctx, "cl_hist")
	if len(hist) != 3 {
		t.Fatalf("after resolve: %d rows, want 3", len(hist))
	}
	if hist[2].FromStatus != domain.ClaimStatusContested || hist[2].ToStatus != domain.ClaimStatusResolved {
		t.Fatalf("bad third transition: %+v", hist[2])
	}
}

func TestClaimRepositoryStatusHistory_EmptyForUnknownClaim(t *testing.T) {
	db := openTestDB(t)
	defer closeDB(db)

	hist, err := NewClaimRepository(db).ListStatusHistoryByClaimID(context.Background(), "cl_nope")
	if err != nil {
		t.Fatalf("list hist: %v", err)
	}
	if len(hist) != 0 {
		t.Fatalf("got %d rows for unknown claim, want 0", len(hist))
	}
}

func TestClaimRepositoryUpsertAndListByEventIDs(t *testing.T) {
	db := openTestDB(t)
	defer closeDB(db)

	eventRepo := NewEventRepository(db)
	claimRepo := NewClaimRepository(db)

	now := time.Date(2026, 4, 12, 14, 0, 0, 0, time.UTC)
	event := domain.Event{
		ID:            "ev_for_claim",
		SchemaVersion: "v1",
		Content:       "Team decided to postpone launch",
		SourceInputID: "in_1",
		Timestamp:     now,
		IngestedAt:    now,
		Metadata:      map[string]string{"chunk_kind": "text"},
	}
	if err := eventRepo.Append(context.Background(), event); err != nil {
		t.Fatalf("Append() event error = %v", err)
	}

	claim := domain.Claim{
		ID:         "cl_1",
		Text:       "Team decided to postpone launch",
		Type:       domain.ClaimTypeDecision,
		Confidence: 0.9,
		Status:     domain.ClaimStatusActive,
		CreatedAt:  now,
	}
	if err := claimRepo.Upsert(context.Background(), []domain.Claim{claim}); err != nil {
		t.Fatalf("Upsert() claim error = %v", err)
	}

	if err := claimRepo.UpsertEvidence(context.Background(), []domain.ClaimEvidence{{ClaimID: "cl_1", EventID: "ev_for_claim"}}); err != nil {
		t.Fatalf("UpsertEvidence() error = %v", err)
	}

	claims, err := claimRepo.ListByEventIDs(context.Background(), []string{"ev_for_claim"})
	if err != nil {
		t.Fatalf("ListByEventIDs() error = %v", err)
	}
	if len(claims) != 1 {
		t.Fatalf("ListByEventIDs() len = %d, want 1", len(claims))
	}
	if claims[0].ID != "cl_1" {
		t.Fatalf("ListByEventIDs() claim id = %q, want cl_1", claims[0].ID)
	}
}

// TestClaimVisibility_DefaultsToTeam verifies that a claim stored without an
// explicit Visibility value is retrieved with Visibility == VisibilityTeam.
func TestClaimVisibility_DefaultsToTeam(t *testing.T) {
	db := openTestDB(t)
	defer closeDB(db)

	ctx := context.Background()
	repo := NewClaimRepository(db)

	claim := domain.Claim{
		ID:         "cl_vis_default",
		Text:       "default visibility claim",
		Type:       domain.ClaimTypeFact,
		Confidence: 0.8,
		Status:     domain.ClaimStatusActive,
		CreatedAt:  time.Now().UTC(),
		// Visibility intentionally omitted — should default to "team".
	}
	if err := repo.Upsert(ctx, []domain.Claim{claim}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	got, err := repo.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(ListAll) = %d, want 1", len(got))
	}
	if got[0].Visibility != domain.VisibilityTeam {
		t.Errorf("Visibility = %q, want %q", got[0].Visibility, domain.VisibilityTeam)
	}
}

// TestClaimVisibility_RoundTrip verifies that each explicit Visibility value
// survives a persist-and-retrieve cycle unchanged.
func TestClaimVisibility_RoundTrip(t *testing.T) {
	cases := []domain.Visibility{
		domain.VisibilityPersonal,
		domain.VisibilityTeam,
		domain.VisibilityOrg,
	}

	for _, vis := range cases {
		t.Run(string(vis), func(t *testing.T) {
			db := openTestDB(t)
			defer closeDB(db)

			ctx := context.Background()
			repo := NewClaimRepository(db)

			claim := domain.Claim{
				ID:         "cl_vis_" + string(vis),
				Text:       "visibility round-trip: " + string(vis),
				Type:       domain.ClaimTypeFact,
				Confidence: 0.75,
				Status:     domain.ClaimStatusActive,
				CreatedAt:  time.Now().UTC(),
				Visibility: vis,
			}
			if err := repo.Upsert(ctx, []domain.Claim{claim}); err != nil {
				t.Fatalf("Upsert: %v", err)
			}

			got, err := repo.ListAll(ctx)
			if err != nil {
				t.Fatalf("ListAll: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("len(ListAll) = %d, want 1", len(got))
			}
			if got[0].Visibility != vis {
				t.Errorf("Visibility = %q, want %q", got[0].Visibility, vis)
			}
		})
	}
}
