package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// Another connection commits while a claim upsert is between its read and its
// first write. A deferred transaction cannot upgrade a stale snapshot, so the
// upsert failed at once with SQLITE_BUSY_SNAPSHOT (517), which busy_timeout
// cannot wait out. Background embedding (#450) made this routine: the next
// Remember collided with the previous one's vector writes. Write transactions
// now begin IMMEDIATE and wait for the lock instead.
func TestClaimUpsert_WaitsForAConcurrentWriter(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "brain.db")
	db, err := open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	repo := NewClaimRepository(db)
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	claim := func(id string) domain.Claim {
		return domain.Claim{ID: id, Text: "c " + id, Type: domain.ClaimTypeFact, Confidence: 0.5, Status: domain.ClaimStatusActive, CreatedAt: at}
	}
	if err := repo.Upsert(ctx, []domain.Claim{claim("seed")}); err != nil {
		t.Fatal(err)
	}

	other, err := sql.Open("sqlite", dsnFor(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	conn, err := other.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO claims (id, text, type, confidence, status, created_at)
		VALUES ('other', 'written by another process', 'fact', 0.5, 'active', '2026-10-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	committed := make(chan error, 1)
	go func() {
		time.Sleep(300 * time.Millisecond)
		_, err := conn.ExecContext(ctx, `COMMIT`)
		committed <- err
	}()

	// Re-upserting "seed" makes the transaction read before it writes.
	if err := repo.Upsert(ctx, []domain.Claim{claim("seed"), claim("new")}); err != nil {
		t.Fatalf("upsert failed against a concurrent writer: %v", err)
	}
	if err := <-committed; err != nil {
		t.Fatal(err)
	}
}
