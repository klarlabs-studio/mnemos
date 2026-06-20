package mnemos_test

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos"

	_ "go.klarlabs.de/mnemos/internal/store/memory"
)

// newReadMemory builds a passive-mode Memory on an isolated namespace and
// seeds a source event so claims can be linked to evidence.
func newReadMemory(t *testing.T, ns string) mnemos.Memory {
	t.Helper()
	clearMnemosEnv(t)
	mem, err := mnemos.New(
		mnemos.WithStorage("memory://?namespace="+ns),
		mnemos.WithPassiveMode(),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	return mem
}

// seedClaim persists an evidence event then a claim linked to it and
// returns the claim id.
func seedClaim(t *testing.T, mem mnemos.Memory, text string, validFrom time.Time) string {
	t.Helper()
	ctx := context.Background()
	evID := "ev-" + time.Now().Format("150405.000000000")
	if err := mem.RememberEvent(ctx, mnemos.Event{ID: evID, At: validFrom, Type: "observation", Content: text}); err != nil {
		t.Fatalf("RememberEvent: %v", err)
	}
	id, err := mem.RememberClaim(ctx, mnemos.ClaimItem{
		Text:      text,
		EventIDs:  []string{evID},
		ValidFrom: validFrom,
	})
	if err != nil {
		t.Fatalf("RememberClaim: %v", err)
	}
	return id
}

// TestGet_ExactLookup verifies Get returns the claim by id and an error
// for an unknown id.
func TestGet_ExactLookup(t *testing.T) {
	mem := newReadMemory(t, "read_get")
	ctx := context.Background()

	id := seedClaim(t, mem, "The customer prefers async communication.", time.Now())

	claim, err := mem.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if claim.ID != id {
		t.Errorf("Get returned id %q, want %q", claim.ID, id)
	}
	if claim.Statement != "The customer prefers async communication." {
		t.Errorf("Get returned statement %q", claim.Statement)
	}
	if claim.RecordedAt.IsZero() {
		t.Error("Get returned zero RecordedAt; transaction-time must be populated")
	}

	if _, err := mem.Get(ctx, "cl_does_not_exist"); err == nil {
		t.Error("Get for unknown id returned nil error; expected not-found error")
	}
}

// TestScan_TimeRange verifies Scan returns claims whose valid time
// overlaps the requested [ValidFrom, ValidUntil] window, honouring Limit.
func TestScan_TimeRange(t *testing.T) {
	mem := newReadMemory(t, "read_scan")
	ctx := context.Background()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_ = seedClaim(t, mem, "January fact.", base)
	_ = seedClaim(t, mem, "March fact.", base.AddDate(0, 2, 0))
	_ = seedClaim(t, mem, "June fact.", base.AddDate(0, 5, 0))

	// Window covering Jan..Apr should include Jan + Mar, exclude June.
	claims, err := mem.Scan(ctx, mnemos.ScanQuery{
		ValidFrom:  base,
		ValidUntil: base.AddDate(0, 3, 0),
		Limit:      100,
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(claims) != 2 {
		t.Fatalf("Scan returned %d claims, want 2 (Jan, Mar)", len(claims))
	}

	// Limit caps the result size.
	limited, err := mem.Scan(ctx, mnemos.ScanQuery{
		ValidFrom:  base,
		ValidUntil: base.AddDate(0, 12, 0),
		Limit:      1,
	})
	if err != nil {
		t.Fatalf("Scan (limited): %v", err)
	}
	if len(limited) != 1 {
		t.Errorf("Scan with Limit=1 returned %d claims, want 1", len(limited))
	}
}

// TestRecall_RecordedAt_TransactionTime verifies the transaction-time
// axis is reachable through the public Query: a claim recorded "now" is
// invisible to a RecordedAsOf in the past.
func TestRecall_RecordedAt_TransactionTime(t *testing.T) {
	mem := newReadMemory(t, "read_recorded_at")
	ctx := context.Background()

	past := time.Now().Add(-1 * time.Hour)
	_ = seedClaim(t, mem, "async communication preference recorded now", time.Now().Add(-24*time.Hour))

	// RecordedAsOf in the past: the claim was recorded just now, so it
	// must NOT appear.
	pastView, err := mem.Recall(ctx, mnemos.Query{
		Text:         "async communication preference",
		RecordedAsOf: past,
	})
	if err != nil {
		t.Fatalf("Recall (past RecordedAsOf): %v", err)
	}
	if len(pastView) != 0 {
		t.Errorf("expected 0 claims under past RecordedAsOf, got %d", len(pastView))
	}

	// RecordedAsOf now (or unset): the claim is visible.
	nowView, err := mem.Recall(ctx, mnemos.Query{
		Text:         "async communication preference",
		RecordedAsOf: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("Recall (now RecordedAsOf): %v", err)
	}
	if len(nowView) == 0 {
		t.Error("expected the claim to be visible under current RecordedAsOf")
	}
}
