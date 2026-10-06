package pipeline

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/relate"
	"go.klarlabs.de/mnemos/internal/store"
	_ "go.klarlabs.de/mnemos/internal/store/memory"
)

// notReadyClaims is a claim repository whose candidate index is mid-build.
type notReadyClaims struct{ ports.ClaimRepository }

func (notReadyClaims) RelateCandidates(context.Context, relate.CandidateQuery) ([]domain.Claim, error) {
	return nil, ports.ErrRelateCandidatesNotReady
}

// While a store's index is being built, the write path must still see every
// claim: a partial index would silently drop candidates.
func TestExistingForRelate_FallsBackWhileTheIndexIsBuilding(t *testing.T) {
	ctx := context.Background()
	conn, err := store.Open(ctx, "memory://relate-fallback")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	if err := conn.Claims.Upsert(ctx, []domain.Claim{
		{ID: "b", Text: "zebra crossing", Type: domain.ClaimTypeFact, Confidence: 0.5, Status: domain.ClaimStatusActive, CreatedAt: at},
		{ID: "a", Text: "unrelated words", Type: domain.ClaimTypeFact, Confidence: 0.5, Status: domain.ClaimStatusActive, CreatedAt: at},
	}); err != nil {
		t.Fatal(err)
	}
	conn.Claims = notReadyClaims{conn.Claims}
	got, err := ExistingForRelate(ctx, conn, []domain.Claim{{ID: "n", Text: "zebra"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
		t.Fatalf("fallback returned %v, want every claim in candidate order [a b]", got)
	}
}
