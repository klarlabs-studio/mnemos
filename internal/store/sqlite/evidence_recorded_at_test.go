package sqlite

import (
	"context"
	"path/filepath"
	"testing"
)

// ADR 0036 step 3: claim_evidence.created_at is added to an existing brain
// without a backfill. Its links read back with a zero CreatedAt, which the
// recorded-as-of read resolves with recorded.LinkRecordedAt, rather than
// failing to parse or inventing a time.
func TestEvidenceCreatedAt_LegacyLinksReadAsUnrecorded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pre-evidence-time.db")
	db, err := open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec(`ALTER TABLE claim_evidence DROP COLUMN created_at`); err != nil {
		t.Fatalf("simulate legacy shape: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO claims (id, text, type, confidence, status, created_at, created_by, valid_from)
		VALUES ('c1','legacy belief','fact',0.9,'active','2026-01-01T00:00:00Z','tester','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert legacy claim: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO claim_evidence (claim_id, event_id) VALUES ('c1', 'e1')`); err != nil {
		t.Fatalf("insert legacy link: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	db2, err := open(path)
	if err != nil {
		t.Fatalf("reopen must migrate, got: %v", err)
	}
	t.Cleanup(func() { _ = db2.Close() })
	links, err := NewClaimRepository(db2).ListEvidenceByClaimIDs(context.Background(), []string{"c1"})
	if err != nil {
		t.Fatalf("list evidence on migrated brain: %v", err)
	}
	if len(links) != 1 || !links[0].CreatedAt.IsZero() {
		t.Fatalf("legacy links = %+v, want one with a zero CreatedAt", links)
	}
}
