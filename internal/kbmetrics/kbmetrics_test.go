package kbmetrics_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/kbmetrics"
	"go.klarlabs.de/mnemos/internal/store"
	_ "go.klarlabs.de/mnemos/internal/store/memory"
	_ "go.klarlabs.de/mnemos/internal/store/sqlite"
)

func seed(t *testing.T, conn *store.Conn) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	for _, ev := range []domain.Event{
		{ID: "ev-a", RunID: "run-a", SchemaVersion: "v1", SourceInputID: "in", Content: "a", Timestamp: now, IngestedAt: now},
		{ID: "ev-b", RunID: "run-b", SchemaVersion: "v1", SourceInputID: "in", Content: "b", Timestamp: now, IngestedAt: now},
	} {
		if err := conn.Events.Append(ctx, ev); err != nil {
			t.Fatalf("event: %v", err)
		}
	}
	if err := conn.Claims.Upsert(ctx, []domain.Claim{
		{ID: "cl-a1", Text: "a1", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive, Confidence: 0.8, CreatedAt: now},
		{ID: "cl-a2", Text: "a2", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusContested, Confidence: 0.8, CreatedAt: now},
		{ID: "cl-b", Text: "b", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusContested, Confidence: 0.8, CreatedAt: now},
		{ID: "cl-b2", Text: "b2", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive, Confidence: 0.8, CreatedAt: now},
	}); err != nil {
		t.Fatalf("claims: %v", err)
	}
	if err := conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{
		{ClaimID: "cl-a1", EventID: "ev-a"}, {ClaimID: "cl-a2", EventID: "ev-a"}, {ClaimID: "cl-b", EventID: "ev-b"}, {ClaimID: "cl-b2", EventID: "ev-b"},
	}); err != nil {
		t.Fatalf("evidence: %v", err)
	}
	if err := conn.Relationships.Upsert(ctx, []domain.Relationship{
		{ID: "r-a", Type: domain.RelationshipTypeContradicts, FromClaimID: "cl-a1", ToClaimID: "cl-a2", CreatedAt: now},
		{ID: "r-b", Type: domain.RelationshipTypeSupports, FromClaimID: "cl-b", ToClaimID: "cl-b2", CreatedAt: now},
	}); err != nil {
		t.Fatalf("relationships: %v", err)
	}
}

// Compute counts what the store holds, and a run allowlist narrows every count
// to that run's episodes and the claims they evidence.
func TestCompute(t *testing.T) {
	for _, dsn := range []string{"memory://", "sqlite://" + filepath.Join(t.TempDir(), "m.db")} {
		t.Run(dsn[:6], func(t *testing.T) {
			conn, err := store.Open(context.Background(), dsn)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			t.Cleanup(func() { _ = conn.Close() })
			seed(t, conn)

			all, err := kbmetrics.Compute(context.Background(), conn, nil)
			if err != nil {
				t.Fatalf("Compute: %v", err)
			}
			want := kbmetrics.Counts{Runs: 2, Events: 2, Claims: 4, ContestedClaims: 2, Relationships: 2, Contradictions: 1}
			if all != want {
				t.Errorf("all = %+v, want %+v", all, want)
			}

			scoped, err := kbmetrics.Compute(context.Background(), conn, []string{"run-a"})
			if err != nil {
				t.Fatalf("Compute scoped: %v", err)
			}
			want = kbmetrics.Counts{Runs: 1, Events: 1, Claims: 2, ContestedClaims: 1, Relationships: 1, Contradictions: 1}
			if scoped != want {
				t.Errorf("run-a = %+v, want %+v", scoped, want)
			}
		})
	}
}

// Brief's counting queries must agree with Compute, on a backend that counts
// runs in the store (sqlite) and one that falls back to listing (memory). An
// episode without a run is not a run on either path.
func TestBriefAgreesWithCompute(t *testing.T) {
	for _, dsn := range []string{"memory://", "sqlite://" + filepath.Join(t.TempDir(), "b.db")} {
		t.Run(dsn[:6], func(t *testing.T) {
			ctx := context.Background()
			conn, err := store.Open(ctx, dsn)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			t.Cleanup(func() { _ = conn.Close() })
			seed(t, conn)
			now := time.Now().UTC()
			if err := conn.Events.Append(ctx, domain.Event{ID: "ev-norun", SchemaVersion: "v1", SourceInputID: "in", Content: "c", Timestamp: now, IngestedAt: now}); err != nil {
				t.Fatalf("event: %v", err)
			}

			full, err := kbmetrics.Compute(ctx, conn, nil)
			if err != nil {
				t.Fatalf("Compute: %v", err)
			}
			got, err := kbmetrics.Brief(ctx, conn)
			if err != nil {
				t.Fatalf("Brief: %v", err)
			}
			want := kbmetrics.BriefCounts{Claims: full.Claims, Runs: full.Runs, Contradictions: full.Contradictions}
			if got != want || got.Runs != 2 {
				t.Errorf("Brief = %+v, want %+v (2 runs)", got, want)
			}
		})
	}
}
