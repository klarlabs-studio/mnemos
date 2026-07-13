package sqlite

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

func journalFixture(now time.Time) []domain.JournalEntry {
	return []domain.JournalEntry{
		{ID: "p1", At: now, Kind: domain.JournalKindConsolidation, Data: `{"total":0.4}`},
		{ID: "b1", At: now.Add(time.Second), Kind: domain.JournalKindBeliefTrust, SubjectID: "cl1", Data: `{"delta":0.05}`},
		{ID: "b2", At: now.Add(2 * time.Second), Kind: domain.JournalKindBeliefTrust, SubjectID: "cl1", Data: `{"delta":-0.02}`},
	}
}

// TestJournalRepository_RoundTrip covers the ADR-0018 append-only journal: entries
// round-trip, List filters by kind newest-first, ListBySubject scopes to a claim,
// Append is idempotent on id, and limit is honored.
func TestJournalRepository_RoundTrip(t *testing.T) {
	db := openTestDB(t)
	defer closeDB(db)
	ctx := context.Background()
	r := NewJournalRepository(db)
	now := time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)

	if err := r.Append(ctx, journalFixture(now)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// Idempotent on id: re-appending the same ids must not duplicate.
	if err := r.Append(ctx, journalFixture(now)); err != nil {
		t.Fatalf("re-Append: %v", err)
	}

	cons, err := r.List(ctx, domain.JournalKindConsolidation, 10)
	if err != nil {
		t.Fatalf("List consolidation: %v", err)
	}
	if len(cons) != 1 || cons[0].ID != "p1" {
		t.Fatalf("consolidation entries = %+v, want 1 (p1)", cons)
	}
	if cons[0].Data != `{"total":0.4}` {
		t.Errorf("data round-trip = %q", cons[0].Data)
	}

	all, err := r.List(ctx, "", 10)
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("all entries = %d, want 3 (idempotency held)", len(all))
	}
	if all[0].ID != "b2" {
		t.Errorf("newest-first ordering broken: first = %s, want b2", all[0].ID)
	}

	bs, err := r.ListBySubject(ctx, "cl1", 10)
	if err != nil {
		t.Fatalf("ListBySubject: %v", err)
	}
	if len(bs) != 2 || bs[0].ID != "b2" {
		t.Fatalf("subject entries = %+v, want 2 newest-first (b2,b1)", bs)
	}

	if lim, _ := r.List(ctx, "", 1); len(lim) != 1 {
		t.Errorf("limit not honored: got %d", len(lim))
	}
}
