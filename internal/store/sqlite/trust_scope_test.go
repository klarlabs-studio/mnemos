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
	if _, err := repo.RecomputeTrust(ctx, domain.TrustScoring{Score: func(domain.TrustInput) float64 { return 0.10 }}); err != nil {
		t.Fatalf("baseline recompute: %v", err)
	}

	n, err := repo.RecomputeTrustForClaims(ctx, []string{"b"}, domain.TrustScoring{Score: func(domain.TrustInput) float64 { return 0.90 }})
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
	if _, err := repo.RecomputeTrust(ctx, domain.TrustScoring{Score: func(domain.TrustInput) float64 { return 0.25 }}); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	n, err := repo.RecomputeTrustForClaims(ctx, nil, domain.TrustScoring{Score: func(domain.TrustInput) float64 { return 0.99 }})
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
	n, err := repo.RecomputeTrustForClaims(ctx, []string{"a", "deleted"}, domain.TrustScoring{Score: func(domain.TrustInput) float64 { return 0.5 }})
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
	score := func(in domain.TrustInput) float64 { return in.Confidence/2 + float64(in.EvidenceCount)/10 }

	if _, err := repo.RecomputeTrust(ctx, domain.TrustScoring{Score: score}); err != nil {
		t.Fatalf("full: %v", err)
	}
	full := map[string]float64{"a": trustOf(t, ctx, repo, "a"), "b": trustOf(t, ctx, repo, "b")}

	// Reset, then take the scoped path over the same claims.
	if _, err := repo.RecomputeTrust(ctx, domain.TrustScoring{Score: func(domain.TrustInput) float64 { return 0 }}); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if _, err := repo.RecomputeTrustForClaims(ctx, []string{"a", "b"}, domain.TrustScoring{Score: score}); err != nil {
		t.Fatalf("scoped: %v", err)
	}
	for id, want := range full {
		if got := trustOf(t, ctx, repo, id); got != want {
			t.Errorf("claim %s: scoped=%v full=%v — the two paths disagree", id, got, want)
		}
	}
}

// The full recompute commits in batches so it never holds the write lock over
// the whole corpus (#466). Every claim must still be rescored, across batches.
func TestRecomputeTrust_CommitsInBatches(t *testing.T) {
	ctx := context.Background()
	repo := NewClaimRepository(openTestDB(t))
	ids := []string{"a", "b", "c", "d", "e"}
	for _, id := range ids {
		seedClaim(t, ctx, repo, id)
	}

	prevBatch, prevPause := trustWriteBatch, trustBatchPause
	trustWriteBatch, trustBatchPause = 2, 0
	t.Cleanup(func() { trustWriteBatch, trustBatchPause = prevBatch, prevPause })

	n, err := repo.RecomputeTrust(ctx, domain.TrustScoring{Score: func(domain.TrustInput) float64 { return 0.42 }})
	if err != nil {
		t.Fatal(err)
	}
	if n != len(ids) {
		t.Fatalf("rescored %d claims, want %d", n, len(ids))
	}
	for _, id := range ids {
		if got := trustOf(t, ctx, repo, id); got != 0.42 {
			t.Fatalf("claim %s trust = %v, want 0.42", id, got)
		}
	}
}

// A pass cancelled part-way keeps the batches that already committed.
func TestRecomputeTrust_CancelKeepsCommittedBatches(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	repo := NewClaimRepository(openTestDB(t))
	for _, id := range []string{"a", "b", "c", "d"} {
		seedClaim(t, ctx, repo, id)
	}

	prevBatch, prevPause := trustWriteBatch, trustBatchPause
	trustWriteBatch, trustBatchPause = 2, 0
	t.Cleanup(func() { trustWriteBatch, trustBatchPause = prevBatch, prevPause })

	calls := 0
	n, err := repo.RecomputeTrust(ctx, domain.TrustScoring{Score: func(domain.TrustInput) float64 {
		calls++
		if calls == 3 { // inside the second batch
			cancel()
		}
		return 0.42
	}})
	if err == nil {
		t.Fatal("expected the cancelled context to stop the pass")
	}
	if n != 2 {
		t.Fatalf("committed %d claims before stopping, want the first batch of 2", n)
	}
}
