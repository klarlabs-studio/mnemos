package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

func TestSetValidity_RoundTripsThroughLoad(t *testing.T) {
	db, err := open(filepath.Join(t.TempDir(), "v.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()

	repo := NewClaimRepository(db)
	now := time.Now().UTC().Truncate(time.Second)
	claim := domain.Claim{
		ID:         "cl1",
		Text:       "Felix works at Acme",
		Type:       domain.ClaimTypeFact,
		Confidence: 0.8,
		Status:     domain.ClaimStatusActive,
		CreatedAt:  now.Add(-30 * 24 * time.Hour),
	}
	if err := repo.Upsert(ctx, []domain.Claim{claim}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	cutoff := now
	if err := repo.SetValidity(ctx, "cl1", cutoff); err != nil {
		t.Fatalf("SetValidity: %v", err)
	}
	loaded, err := repo.ListByIDs(ctx, []string{"cl1"})
	if err != nil || len(loaded) != 1 {
		t.Fatalf("load: %v, n=%d", err, len(loaded))
	}
	if loaded[0].ValidTo.IsZero() {
		t.Fatalf("expected ValidTo to be set, got zero")
	}
	if !loaded[0].ValidTo.Equal(cutoff) {
		t.Fatalf("ValidTo round-trip mismatch: got %s, want %s", loaded[0].ValidTo, cutoff)
	}

	// Clearing it returns zero again.
	if err := repo.SetValidity(ctx, "cl1", time.Time{}); err != nil {
		t.Fatalf("clear validity: %v", err)
	}
	loaded, err = repo.ListByIDs(ctx, []string{"cl1"})
	if err != nil || len(loaded) != 1 {
		t.Fatalf("reload: %v, n=%d", err, len(loaded))
	}
	if !loaded[0].ValidTo.IsZero() {
		t.Fatalf("expected ValidTo cleared, got %s", loaded[0].ValidTo)
	}
}

func TestUpsert_DefaultsValidFromToCreatedAt(t *testing.T) {
	db, err := open(filepath.Join(t.TempDir(), "v.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()

	repo := NewClaimRepository(db)
	createdAt := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	claim := domain.Claim{
		ID: "cl_default", Text: "x", Type: domain.ClaimTypeFact,
		Confidence: 0.8, Status: domain.ClaimStatusActive, CreatedAt: createdAt,
		// ValidFrom intentionally left zero — repo must default it.
	}
	if err := repo.Upsert(ctx, []domain.Claim{claim}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	loaded, err := repo.ListByIDs(ctx, []string{"cl_default"})
	if err != nil || len(loaded) != 1 {
		t.Fatalf("load: %v, n=%d", err, len(loaded))
	}
	if !loaded[0].ValidFrom.Equal(createdAt) {
		t.Fatalf("ValidFrom default = %s, want %s", loaded[0].ValidFrom, createdAt)
	}
}

func TestMigrate_BackfillsValidFromOnLegacyV2Schema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "v2.db")

	// Build a v2 DB shape: includes trust_score (v0.7) but no
	// valid_from / valid_to (v0.8 columns). Mirrors the upgrade
	// path real users will hit going from v0.7.x to v0.8.0.
	if err := func() error {
		raw, err := open(path)
		if err != nil {
			return err
		}
		defer func() { _ = raw.Close() }()
		// Force user_version back to 2 and drop the v3 columns so
		// migrate has work to do on the next Open. Index must come
		// down first because it references valid_to.
		if _, err := raw.Exec(`
			DROP INDEX IF EXISTS idx_claims_valid_to;
			ALTER TABLE claims DROP COLUMN valid_to;
			ALTER TABLE claims DROP COLUMN valid_from;
			PRAGMA user_version = 2;
		`); err != nil {
			return err
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := raw.Exec(
			`INSERT INTO claims (id, text, type, confidence, status, created_at)
			 VALUES ('cl_old', 'legacy', 'fact', 0.8, 'active', ?)`,
			now,
		); err != nil {
			return err
		}
		return nil
	}(); err != nil {
		t.Fatalf("seed v2 db: %v", err)
	}

	// Re-open: migrate() should add the columns and backfill
	// valid_from from created_at.
	db, err := open(path)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	repo := NewClaimRepository(db)
	loaded, err := repo.ListByIDs(context.Background(), []string{"cl_old"})
	if err != nil || len(loaded) != 1 {
		t.Fatalf("load after migrate: %v, n=%d", err, len(loaded))
	}
	if loaded[0].ValidFrom.IsZero() {
		t.Fatal("expected backfilled ValidFrom, got zero")
	}
	if !loaded[0].CreatedAt.Equal(loaded[0].ValidFrom) {
		t.Fatalf("ValidFrom (%s) should equal CreatedAt (%s) after backfill", loaded[0].ValidFrom, loaded[0].CreatedAt)
	}
	if !loaded[0].ValidTo.IsZero() {
		t.Fatalf("ValidTo should be NULL on backfill, got %s", loaded[0].ValidTo)
	}
}
