package main

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// Note: the handleResolve wrapper calls os.Exit on error paths, so these
// tests exercise the underlying repo + status-history contract rather
// than the handler itself. The handler's wiring is trivial once the repo
// call works.

func TestResolve_ChangesStatusesAndRecordsTransitions(t *testing.T) {
	_, conn := openTestStore(t)
	ctx := context.Background()
	repo := conn.Claims

	now := time.Now().UTC()
	winner := domain.Claim{
		ID: "cl_winner", Text: "Winner text", Type: domain.ClaimTypeFact,
		Confidence: 0.9, Status: domain.ClaimStatusContested, CreatedAt: now,
	}
	loser := domain.Claim{
		ID: "cl_loser", Text: "Loser text", Type: domain.ClaimTypeFact,
		Confidence: 0.7, Status: domain.ClaimStatusContested, CreatedAt: now,
	}
	if err := repo.Upsert(ctx, []domain.Claim{winner, loser}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Simulate the handler's core action: flip statuses and upsert with a reason.
	winner.Status = domain.ClaimStatusResolved
	loser.Status = domain.ClaimStatusDeprecated
	if err := repo.UpsertWithReason(ctx, []domain.Claim{winner, loser}, "evidence review by jane"); err != nil {
		t.Fatalf("resolve upsert: %v", err)
	}

	winnerClaims, _ := repo.ListByIDs(ctx, []string{"cl_winner"})
	loserClaims, _ := repo.ListByIDs(ctx, []string{"cl_loser"})
	if len(winnerClaims) != 1 || winnerClaims[0].Status != domain.ClaimStatusResolved {
		t.Errorf("winner status = %v, want resolved", winnerClaims)
	}
	if len(loserClaims) != 1 || loserClaims[0].Status != domain.ClaimStatusDeprecated {
		t.Errorf("loser status = %v, want deprecated", loserClaims)
	}

	// Each claim should have 2 history rows: the initial (""→contested) and
	// the resolution (contested→resolved / contested→deprecated).
	wHist, _ := repo.ListStatusHistoryByClaimID(ctx, "cl_winner")
	if len(wHist) != 2 {
		t.Fatalf("winner history: %d rows, want 2", len(wHist))
	}
	if wHist[1].FromStatus != domain.ClaimStatusContested || wHist[1].ToStatus != domain.ClaimStatusResolved {
		t.Errorf("winner second transition = %+v, want contested→resolved", wHist[1])
	}
	if wHist[1].Reason != "evidence review by jane" {
		t.Errorf("winner reason not captured: %q", wHist[1].Reason)
	}

	lHist, _ := repo.ListStatusHistoryByClaimID(ctx, "cl_loser")
	if len(lHist) != 2 || lHist[1].ToStatus != domain.ClaimStatusDeprecated {
		t.Errorf("loser history wrong: %+v", lHist)
	}
}
