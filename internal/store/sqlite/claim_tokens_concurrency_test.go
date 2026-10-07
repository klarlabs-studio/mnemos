package sqlite

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/relate"
)

// seedUnindexed opens a brain at path with n claims and then clears the token
// index, so the next open has to build it.
func seedUnindexed(t *testing.T, path string, n int) {
	t.Helper()
	db, err := open(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if _, err := db.Exec(`INSERT INTO claims (id, text, type, confidence, status, created_at)
			VALUES (?, ?, 'fact', 0.5, 'active', '2026-10-01T00:00:00Z')`,
			fmt.Sprintf("cl_%05d", i), fmt.Sprintf("deploy pipeline service %d runs integration tests", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`DELETE FROM claim_tokens; UPDATE relate_token_state SET tokenizer_version = '', building_version = '', build_cursor = ''`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
}

func tokenizerReady(t *testing.T, db *sql.DB) bool {
	t.Helper()
	var v string
	if err := db.QueryRow(`SELECT tokenizer_version FROM relate_token_state WHERE id = 1`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v == relate.TokenizerVersion
}

// Another process commits while a build batch runs. With a deferred
// transaction the batch read first, then failed its lock upgrade with
// SQLITE_BUSY_SNAPSHOT the moment it tried to insert; busy_timeout cannot
// wait that out. Taking the write lock up front waits for the writer instead.
func TestClaimTokenBuild_WaitsForAConcurrentWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "brain.db")
	seedUnindexed(t, path, 50)
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`INSERT INTO relate_token_state(id, tokenizer_version, building_version, build_cursor)
		VALUES (1, '', ?, '') ON CONFLICT(id) DO UPDATE SET building_version = excluded.building_version`, relate.TokenizerVersion); err != nil {
		t.Fatal(err)
	}

	// The other process: holds the write lock, then commits a new claim.
	other, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close() }()
	otherConn, err := other.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = otherConn.Close() }()
	if _, err := otherConn.ExecContext(t.Context(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	if _, err := otherConn.ExecContext(t.Context(), `INSERT INTO claims (id, text, type, confidence, status, created_at)
		VALUES ('cl_other', 'hook capture', 'fact', 0.5, 'active', '2026-10-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	committed := make(chan error, 1)
	go func() {
		time.Sleep(300 * time.Millisecond)
		_, err := otherConn.ExecContext(t.Context(), `COMMIT`)
		committed <- err
	}()

	if _, _, err := buildClaimTokenBatch(db, ""); err != nil {
		t.Fatalf("build batch failed against a concurrent writer: %v", err)
	}
	if err := <-committed; err != nil {
		t.Fatal(err)
	}
}

// A build that cannot finish leaves the brain openable: the index is an
// accelerator, and relate falls back to the full corpus until it is ready.
// Before, the build's error failed the open, so a lock held past busy_timeout
// by another process made the brain unopenable.
func TestOpen_SurvivesAnUnfinishableTokenBuild(t *testing.T) {
	path := filepath.Join(t.TempDir(), "brain.db")
	seedUnindexed(t, path, 50)

	holder, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := holder.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}

	db, err := open(path)
	if err != nil {
		t.Fatalf("open failed while another process held the write lock: %v", err)
	}
	if tokenizerReady(t, db) {
		t.Fatal("index reports ready although its build could not run")
	}
	_ = db.Close()

	if _, err := conn.ExecContext(t.Context(), `ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	_ = holder.Close()

	db, err = open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if !tokenizerReady(t, db) {
		t.Fatal("index not built by the next open once the lock was free")
	}
}
