package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
	"go.klarlabs.de/mnemos/internal/trust"
)

func openTrustBackfillStore(t *testing.T, n int) *store.Conn {
	t.Helper()
	conn, err := store.Open(context.Background(), "sqlite://"+filepath.Join(t.TempDir(), "backfill.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	now := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	cs := make([]domain.Claim, n)
	for i := range cs {
		cs[i] = domain.Claim{
			ID: "c" + string(rune('a'+i)), Text: "belief " + string(rune('a'+i)), Type: domain.ClaimTypeFact,
			Confidence: 0.8, Status: domain.ClaimStatusActive, CreatedAt: now,
		}
	}
	if err := conn.Claims.Upsert(context.Background(), cs); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return conn
}

func TestPlanTrustBackfill_SelectsEveryClaimNotOnTheCurrentModel(t *testing.T) {
	claims := []domain.Claim{
		{ID: "a"},                                // never versioned: trust/v1
		{ID: "b", TrustModelVersion: "trust/v1"}, // explicitly old
		{ID: "c", TrustModelVersion: trust.ModelVersion},
	}
	p := planTrustBackfill(claims)
	if p.Total != 3 || len(p.Stale) != 2 || p.Stale[0] != "a" || p.Stale[1] != "b" {
		t.Fatalf("plan = %+v, want a and b stale out of 3", p)
	}
	if p.ByVersion[""] != 1 || p.ByVersion["trust/v1"] != 1 {
		t.Errorf("ByVersion = %v", p.ByVersion)
	}
}

// The backfill rescores in bounded batches, stamps every row, and converges:
// a second run finds nothing to do. An interrupted run is just a first run
// that stopped early — what it finished is already current and is skipped.
func TestApplyTrustBackfill_StampsInBatchesAndConverges(t *testing.T) {
	ctx := context.Background()
	conn := openTrustBackfillStore(t, 7)

	all, _ := conn.Claims.ListAll(ctx)
	plan := planTrustBackfill(all)
	if len(plan.Stale) != 7 {
		t.Fatalf("fresh claims should all be stale, got %d", len(plan.Stale))
	}
	// Interrupt after the first batch of 3.
	at := time.Date(2026, 10, 5, 12, 0, 0, 123456789, time.UTC)
	if _, _, err := applyTrustBackfill(ctx, conn, plan.Stale[:3], 3, at); err != nil {
		t.Fatalf("partial backfill: %v", err)
	}
	all, _ = conn.Claims.ListAll(ctx)
	if left := planTrustBackfill(all); len(left.Stale) != 4 {
		t.Fatalf("after the first batch %d remain stale, want 4", len(left.Stale))
	}

	all, _ = conn.Claims.ListAll(ctx)
	rescored, batches, err := applyTrustBackfill(ctx, conn, planTrustBackfill(all).Stale, 3, at)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if rescored != 4 || batches != 2 {
		t.Errorf("resume rescored %d in %d batches, want 4 in 2", rescored, batches)
	}
	all, _ = conn.Claims.ListAll(ctx)
	if left := planTrustBackfill(all); len(left.Stale) != 0 {
		t.Fatalf("%d claims still stale after the backfill", len(left.Stale))
	}
	for _, c := range all {
		if !c.TrustComputedAt.Equal(at.Truncate(time.Microsecond)) {
			t.Errorf("%s: trust_computed_at = %v, want %v", c.ID, c.TrustComputedAt, at.Truncate(time.Microsecond))
		}
	}
}

// The read-back is the point of the batch verification: a stamp that did not
// persist must fail the run, not be reported as done.
func TestVerifyTrustBatch_RejectsAStampThatDidNotPersist(t *testing.T) {
	ctx := context.Background()
	conn := openTrustBackfillStore(t, 2)
	ids := []string{"ca", "cb"}
	if err := verifyTrustBatch(ctx, conn, ids, trust.Scorer(time.Now().UTC().Truncate(time.Microsecond))); err == nil {
		t.Fatal("verify accepted claims that were never rescored")
	}
}

// --dry-run is consumed by the GLOBAL flag parser, so the subcommand never sees
// it in its own args. Reading it from args alone made `recompute-trust --stale
// --dry-run` rescore the store for real.
func TestParseRecomputeTrustArgs_HonoursTheGlobalDryRun(t *testing.T) {
	flags, rest := ParseFlags([]string{"recompute-trust", "--stale", "--dry-run", "--batch", "50"})
	if len(rest) == 0 || rest[0] != "recompute-trust" {
		t.Fatalf("unexpected positional args %v", rest)
	}
	stale, dryRun, batch, err := parseRecomputeTrustArgs(rest[1:], flags)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !stale || !dryRun || batch != 50 {
		t.Fatalf("stale=%v dryRun=%v batch=%d, want true true 50 (args after global parse: %v)", stale, dryRun, batch, rest[1:])
	}
	// A full recompute has no dry run, so asking for one must fail rather than write.
	flags, rest = ParseFlags([]string{"recompute-trust", "--dry-run"})
	if _, _, _, err := parseRecomputeTrustArgs(rest[1:], flags); err == nil {
		t.Fatal("recompute-trust --dry-run without --stale was accepted and would rewrite every row")
	}
}
