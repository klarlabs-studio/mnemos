package mnemos_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"go.klarlabs.de/mnemos"
	_ "go.klarlabs.de/mnemos/sqlite"
)

// recallUnderLockBudget is how long a recall may take while another process
// holds the write lock. busy_timeout is 5s, so any write the read path waits
// on synchronously blows straight through it.
const recallUnderLockBudget = 3 * time.Second

// holdSQLiteWriteLock opens a second connection to the brain file — as another
// process would — and holds its write lock until the test ends. That is what a
// long `mnemos consolidate` looks like to every other caller.
func holdSQLiteWriteLock(t *testing.T, path string) {
	t.Helper()
	ctx := context.Background()
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

// A consolidation run held the write lock for over four hours and recall
// answered nothing for the whole of it: the cognitive write-backs Recall makes
// after it has its answer (Hebbian strengthening, reconsolidation, inhibition)
// each waited out busy_timeout before the answer was returned. Reading under
// WAL never needs the write lock, so recall must still answer, promptly.
func TestRecall_AnswersWhileAnotherProcessHoldsTheWriteLock(t *testing.T) {
	clearMnemosEnv(t)
	path := filepath.Join(t.TempDir(), "brain.db")
	mem, err := mnemos.New(mnemos.WithStorage("sqlite://" + path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = mem.Close() }()
	ctx := context.Background()
	for _, text := range []string{
		"The billing service retries webhook deliveries three times.",
		"The billing service signs webhook deliveries with HMAC.",
		"The billing service stores webhook deliveries for thirty days.",
	} {
		if _, err := mem.RememberClaimWithEvidence(ctx, text, []string{text}, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}

	holdSQLiteWriteLock(t, path)

	start := time.Now()
	results, err := mem.Recall(ctx, mnemos.Query{Text: "billing service webhook deliveries"})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Recall under a held write lock: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("Recall under a held write lock returned nothing")
	}
	if elapsed > recallUnderLockBudget {
		t.Fatalf("Recall took %v under a held write lock, want under %v: a post-answer write is blocking the answer", elapsed, recallUnderLockBudget)
	}
}
