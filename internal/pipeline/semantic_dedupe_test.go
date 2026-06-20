package pipeline

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/store"
	// The named import below also registers the sqlite provider via its
	// init(), so store.Open("sqlite://...") works without a blank import.
	"go.klarlabs.de/mnemos/internal/store/sqlite"
)

// openDedupeDB opens a fresh SQLite-backed Conn at a temp path for
// dedupe tests. Returns both the *sql.DB (for the test fixture's
// raw inserts) and the *store.Conn (for the dedupe functions).
func openDedupeDB(t *testing.T) (*sql.DB, *store.Conn) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "dedupe.db")
	conn, err := store.Open(context.Background(), "sqlite://"+dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	db, ok := conn.Raw.(*sql.DB)
	if !ok || db == nil {
		t.Fatal("sqlite Conn missing *sql.DB raw handle")
	}
	return db, conn
}

func TestPlanSemanticDedupe_FindsParaphraseCluster(t *testing.T) {
	db, conn := openDedupeDB(t)

	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)

	// Three claims; cl_a and cl_b are near-duplicates by vector
	// distance; cl_c is unrelated.
	for _, c := range []struct {
		id, text string
		trust    float64
	}{
		{"cl_a", "Felix needs coffee for the flea market", 0.4},
		{"cl_b", "Felix needs coffee for the flea market.", 0.7}, // higher trust → wins
		{"cl_c", "Cake tongs are required", 0.5},
	} {
		if _, err := db.Exec(
			`INSERT INTO claims (id, text, type, confidence, status, created_at, trust_score)
			 VALUES (?, ?, 'fact', 0.8, 'active', ?, ?)`,
			c.id, c.text, now, c.trust,
		); err != nil {
			t.Fatalf("insert claim %s: %v", c.id, err)
		}
	}

	// Embeddings: cl_a and cl_b are nearly identical (cos ≈ 0.999);
	// cl_c is orthogonal.
	embRepo := sqlite.NewEmbeddingRepository(db)
	if err := embRepo.Upsert(ctx, "cl_a", "claim", []float32{1, 0.01, 0}, "test", ""); err != nil {
		t.Fatalf("embed cl_a: %v", err)
	}
	if err := embRepo.Upsert(ctx, "cl_b", "claim", []float32{1, 0.02, 0}, "test", ""); err != nil {
		t.Fatalf("embed cl_b: %v", err)
	}
	if err := embRepo.Upsert(ctx, "cl_c", "claim", []float32{0, 0, 1}, "test", ""); err != nil {
		t.Fatalf("embed cl_c: %v", err)
	}

	plan, err := PlanSemanticDedupe(ctx, conn, 0.95)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.Merges) != 1 {
		t.Fatalf("expected 1 merge cluster, got %d: %+v", len(plan.Merges), plan.Merges)
	}
	m := plan.Merges[0]
	if m.WinnerID != "cl_b" {
		t.Fatalf("expected cl_b (higher trust) to win, got %s", m.WinnerID)
	}
	if len(m.DuplicateIDs) != 1 || m.DuplicateIDs[0] != "cl_a" {
		t.Fatalf("expected cl_a as the duplicate, got %v", m.DuplicateIDs)
	}
	if m.MaxSimilarity < 0.99 {
		t.Fatalf("expected similarity ~0.999, got %v", m.MaxSimilarity)
	}
}

func TestPlanSemanticDedupe_SkipsClaimsWithoutEmbedding(t *testing.T) {
	db, conn := openDedupeDB(t)

	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)

	for _, id := range []string{"a", "b", "c"} {
		if _, err := db.Exec(
			`INSERT INTO claims (id, text, type, confidence, status, created_at)
			 VALUES (?, 'x', 'fact', 0.8, 'active', ?)`,
			id, now,
		); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	// Only one has an embedding.
	embRepo := sqlite.NewEmbeddingRepository(db)
	if err := embRepo.Upsert(ctx, "a", "claim", []float32{1, 0, 0}, "m", ""); err != nil {
		t.Fatalf("embed: %v", err)
	}

	plan, err := PlanSemanticDedupe(ctx, conn, 0.9)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.SkippedNoEmbedding != 2 {
		t.Fatalf("expected 2 skipped, got %d", plan.SkippedNoEmbedding)
	}
	if len(plan.Merges) != 0 {
		t.Fatalf("no merges expected with single embedded claim, got %d", len(plan.Merges))
	}
}

func TestApplySemanticDedupe_ReassignsEvidenceAndDeletesDup(t *testing.T) {
	db, conn := openDedupeDB(t)

	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)

	// Two near-duplicate claims, each with its own evidence event.
	if _, err := db.Exec(
		`INSERT INTO events (id, run_id, schema_version, content, source_input_id, timestamp, metadata_json, ingested_at)
		 VALUES ('ev1', 'r', 'v1', 'c1', 'i1', ?, '{}', ?), ('ev2', 'r', 'v1', 'c2', 'i2', ?, '{}', ?)`,
		now, now, now, now,
	); err != nil {
		t.Fatalf("insert events: %v", err)
	}
	for _, c := range []struct{ id, ev string }{{"cl_winner", "ev1"}, {"cl_dup", "ev2"}} {
		if _, err := db.Exec(
			`INSERT INTO claims (id, text, type, confidence, status, created_at, trust_score)
			 VALUES (?, 'x', 'fact', 0.8, 'active', ?, ?)`,
			c.id, now, 0.5,
		); err != nil {
			t.Fatalf("insert claim: %v", err)
		}
		if _, err := db.Exec(
			`INSERT INTO claim_evidence (claim_id, event_id) VALUES (?, ?)`,
			c.id, c.ev,
		); err != nil {
			t.Fatalf("insert evidence: %v", err)
		}
	}

	plan := SemanticDedupePlan{
		Merges: []SemanticMerge{{
			WinnerID:     "cl_winner",
			DuplicateIDs: []string{"cl_dup"},
		}},
	}
	merged, err := ApplySemanticDedupe(ctx, conn, plan)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if merged != 1 {
		t.Fatalf("merged = %d, want 1", merged)
	}

	// Winner now has both events; duplicate is gone.
	var evCount int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM claim_evidence WHERE claim_id = 'cl_winner'`,
	).Scan(&evCount); err != nil {
		t.Fatalf("count evidence: %v", err)
	}
	if evCount != 2 {
		t.Fatalf("expected winner to absorb both evidence rows, got %d", evCount)
	}
	var dupExists int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM claims WHERE id = 'cl_dup'`,
	).Scan(&dupExists); err != nil {
		t.Fatalf("check dup: %v", err)
	}
	if dupExists != 0 {
		t.Fatalf("duplicate claim should be deleted")
	}
}

func TestApplySemanticDedupe_DropsSelfLoopRelationships(t *testing.T) {
	db, conn := openDedupeDB(t)

	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339Nano)

	for _, id := range []string{"cl_winner", "cl_dup"} {
		if _, err := db.Exec(
			`INSERT INTO claims (id, text, type, confidence, status, created_at)
			 VALUES (?, 'x', 'fact', 0.8, 'active', ?)`,
			id, now,
		); err != nil {
			t.Fatalf("insert claim %s: %v", id, err)
		}
	}
	// A relationship between the two duplicates would self-loop after
	// the merge. ApplySemanticDedupe should drop it.
	if _, err := db.Exec(
		`INSERT INTO relationships (id, type, from_claim_id, to_claim_id, created_at)
		 VALUES ('rl1', 'supports', 'cl_winner', 'cl_dup', ?)`,
		now,
	); err != nil {
		t.Fatalf("insert rel: %v", err)
	}

	if _, err := ApplySemanticDedupe(ctx, conn, SemanticDedupePlan{
		Merges: []SemanticMerge{{WinnerID: "cl_winner", DuplicateIDs: []string{"cl_dup"}}},
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	var n int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM relationships WHERE from_claim_id = to_claim_id`,
	).Scan(&n); err != nil {
		t.Fatalf("count loops: %v", err)
	}
	if n != 0 {
		t.Fatalf("self-loop should have been dropped, got %d", n)
	}
}
