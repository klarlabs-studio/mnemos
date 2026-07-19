package sqlite

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

func seedClaim(t *testing.T, ctx context.Context, repo ClaimRepository, id string) {
	t.Helper()
	if err := repo.Upsert(ctx, []domain.Claim{{
		ID:         id,
		Text:       "claim " + id,
		Type:       domain.ClaimTypeFact,
		Confidence: 0.8,
		Status:     domain.ClaimStatusActive,
		CreatedAt:  time.Now().UTC(),
	}}); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

func trustOf(t *testing.T, ctx context.Context, repo ClaimRepository, id string) float64 {
	t.Helper()
	got, err := repo.ListByIDs(ctx, []string{id})
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	if len(got) != 1 {
		t.Fatalf("claim %s missing", id)
	}
	return got[0].TrustScore
}

// The scoped recompute must rescore exactly the named claims and leave every
// other claim untouched — otherwise it is not a safe substitute for the full
// pass.
func TestRecomputeTrustForClaims_OnlyTouchesNamedClaims(t *testing.T) {
	db := openTestDB(t)
	defer closeDB(db)
	ctx := context.Background()
	repo := NewClaimRepository(db)

	for _, id := range []string{"a", "b", "c"} {
		seedClaim(t, ctx, repo, id)
	}
	if _, err := repo.RecomputeTrust(ctx, func(float64, int, time.Time) float64 { return 0.10 }); err != nil {
		t.Fatalf("baseline recompute: %v", err)
	}

	n, err := repo.RecomputeTrustForClaims(ctx, []string{"b"}, func(float64, int, time.Time) float64 { return 0.90 })
	if err != nil {
		t.Fatalf("scoped recompute: %v", err)
	}
	if n != 1 {
		t.Errorf("rescored %d claims, want 1", n)
	}
	for id, want := range map[string]float64{"a": 0.10, "b": 0.90, "c": 0.10} {
		if got := trustOf(t, ctx, repo, id); got != want {
			t.Errorf("claim %s trust = %v, want %v", id, got, want)
		}
	}
}

// An empty id list must be a no-op, not a full-store rescore — that would
// silently reintroduce the cost this exists to remove.
func TestRecomputeTrustForClaims_EmptyIsNoOp(t *testing.T) {
	db := openTestDB(t)
	defer closeDB(db)
	ctx := context.Background()
	repo := NewClaimRepository(db)

	seedClaim(t, ctx, repo, "a")
	if _, err := repo.RecomputeTrust(ctx, func(float64, int, time.Time) float64 { return 0.25 }); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	n, err := repo.RecomputeTrustForClaims(ctx, nil, func(float64, int, time.Time) float64 { return 0.99 })
	if err != nil {
		t.Fatalf("scoped recompute: %v", err)
	}
	if n != 0 {
		t.Errorf("rescored %d claims for an empty list, want 0", n)
	}
	if got := trustOf(t, ctx, repo, "a"); got != 0.25 {
		t.Errorf("empty scope rescored a claim: trust = %v, want 0.25", got)
	}
}

// A claim can be deleted between the write and the rescore, so unknown ids
// must be skipped rather than failing the write.
func TestRecomputeTrustForClaims_UnknownIDsSkipped(t *testing.T) {
	db := openTestDB(t)
	defer closeDB(db)
	ctx := context.Background()
	repo := NewClaimRepository(db)

	seedClaim(t, ctx, repo, "a")
	n, err := repo.RecomputeTrustForClaims(ctx, []string{"a", "deleted"}, func(float64, int, time.Time) float64 { return 0.5 })
	if err != nil {
		t.Fatalf("unknown id caused an error: %v", err)
	}
	if n != 1 {
		t.Errorf("rescored %d, want 1", n)
	}
}

// Scoped and full recompute must agree on the claims they both cover; a
// divergence would mean the fast path scores differently from the audit path.
func TestRecomputeTrustForClaims_MatchesFullRecompute(t *testing.T) {
	db := openTestDB(t)
	defer closeDB(db)
	ctx := context.Background()
	repo := NewClaimRepository(db)

	for _, id := range []string{"a", "b"} {
		seedClaim(t, ctx, repo, id)
	}
	score := func(conf float64, n int, _ time.Time) float64 { return conf/2 + float64(n)/10 }

	if _, err := repo.RecomputeTrust(ctx, score); err != nil {
		t.Fatalf("full: %v", err)
	}
	full := map[string]float64{"a": trustOf(t, ctx, repo, "a"), "b": trustOf(t, ctx, repo, "b")}

	// Reset, then take the scoped path over the same claims.
	if _, err := repo.RecomputeTrust(ctx, func(float64, int, time.Time) float64 { return 0 }); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if _, err := repo.RecomputeTrustForClaims(ctx, []string{"a", "b"}, score); err != nil {
		t.Fatalf("scoped: %v", err)
	}
	for id, want := range full {
		if got := trustOf(t, ctx, repo, id); got != want {
			t.Errorf("claim %s: scoped=%v full=%v — the two paths disagree", id, got, want)
		}
	}
}
