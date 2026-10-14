package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos"
)

// readUnderLockBudget is how long a read may take while another process holds
// the write lock. busy_timeout is 5s, so any write a read waits on
// synchronously blows straight through it.
const readUnderLockBudget = 3 * time.Second

// seedLockedBrain writes a few related beliefs to a fresh SQLite brain, points
// MNEMOS_DB_URL at it, and then holds its write lock from a second connection
// for the rest of the test — what a long `mnemos consolidate` looks like to
// every other process.
func seedLockedBrain(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "brain.db")
	dsn := "sqlite://" + path
	t.Setenv("MNEMOS_DB_URL", dsn)
	ctx := context.Background()

	mem, err := mnemos.New(mnemos.WithStorage(dsn))
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{
		"The billing service retries webhook deliveries three times.",
		"The billing service signs webhook deliveries with HMAC.",
		"The billing service stores webhook deliveries for thirty days.",
	} {
		if _, err := mem.RememberClaimWithEvidence(ctx, text, []string{text}, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := mem.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = conn.ExecContext(ctx, `ROLLBACK`)
		_ = conn.Close()
		_ = db.Close()
	})
}

// `mnemos query` wrote a compilation_jobs row before it read anything, so
// while consolidation held the write lock every query failed with
// "create job: … database is locked (SQLITE_BUSY)". A read must not need the
// write lock to answer.
func TestQueryCommand_AnswersWhileAnotherProcessHoldsTheWriteLock(t *testing.T) {
	seedLockedBrain(t)

	var runErr error
	start := time.Now()
	out := captureStdout(t, func() {
		runErr = runQuery([]string{"billing service webhook deliveries"}, Flags{})
	})
	elapsed := time.Since(start)
	if runErr != nil {
		t.Fatalf("query under a held write lock: %v", runErr)
	}
	var resp struct {
		Beliefs []json.RawMessage `json:"beliefs"`
	}
	// A fresh brain prints the first-run banner ahead of the JSON answer.
	if i := strings.Index(out, "{"); i > 0 {
		out = out[i:]
	}
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode query output %q: %v", out, err)
	}
	if len(resp.Beliefs) == 0 {
		t.Fatalf("query under a held write lock returned no beliefs: %s", out)
	}
	if elapsed > readUnderLockBudget {
		t.Fatalf("query took %v under a held write lock, want under %v", elapsed, readUnderLockBudget)
	}
}

// query_knowledge and the UserPromptSubmit recall hook share mcpRunQuery. The
// hook budgets 12s, and its cognitive write-backs each waited out busy_timeout
// after the answer was in hand.
func TestMCPQuery_AnswersWhileAnotherProcessHoldsTheWriteLock(t *testing.T) {
	seedLockedBrain(t)

	start := time.Now()
	out, err := mcpRunQuery(context.Background(), mcpQueryInput{Question: "billing service webhook deliveries"})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("query_knowledge under a held write lock: %v", err)
	}
	if len(out.Claims) == 0 {
		t.Fatal("query_knowledge under a held write lock returned no claims")
	}
	if elapsed > readUnderLockBudget {
		t.Fatalf("query_knowledge took %v under a held write lock, want under %v", elapsed, readUnderLockBudget)
	}
}
