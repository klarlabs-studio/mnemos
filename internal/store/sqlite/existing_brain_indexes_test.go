package sqlite

import (
	"context"
	"path/filepath"
	"testing"
)

// A brain from the previous release is already at currentSchemaVersion. An
// index added after it was created must still appear when it is next opened.
// When the post-migrate indexes sat behind migrate's version gate they never
// did: idx_claims_live was missing on every existing brain, and the queries
// that name it with INDEXED BY failed with "no such index", which broke
// BrainHealth outright. Every test opened a fresh database, which gets every
// index from the start, so none noticed.
func TestBootstrap_AddsNewIndexesToAnExistingBrain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing.db")
	db, err := open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := Bootstrap(db); err != nil {
		t.Fatal(err)
	}
	// An existing brain: current schema version, the later indexes absent.
	for _, idx := range []string{"idx_claims_live", "idx_claims_created_id", "idx_claims_test_requirement_ref"} {
		if _, err := db.Exec(`DROP INDEX IF EXISTS ` + idx); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()

	db, err = open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := Bootstrap(db); err != nil {
		t.Fatal(err)
	}
	for _, idx := range []string{"idx_claims_live", "idx_claims_created_id", "idx_claims_test_requirement_ref"} {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = ?`, idx).Scan(&n); err != nil || n != 1 {
			t.Errorf("index %s missing after reopening an existing brain (%d, %v)", idx, n, err)
		}
	}
	r := NewClaimRepository(db)
	if _, err := r.CountLiveClaims(context.Background()); err != nil {
		t.Fatalf("CountLiveClaims on an existing brain: %v", err)
	}
	if _, err := r.SampleLiveClaims(context.Background(), 10, 1); err != nil {
		t.Fatalf("SampleLiveClaims on an existing brain: %v", err)
	}
}
