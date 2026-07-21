package main

import (
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

func day(d int) time.Time { return time.Date(2026, 7, d, 0, 0, 0, 0, time.UTC) }

func TestUnclassifiedNewestFirst_SelectsAndOrders(t *testing.T) {
	closed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	claims := []domain.Claim{
		{ID: "oldest", CreatedAt: day(1)},
		{ID: "newest", CreatedAt: day(3)},
		{ID: "middle", CreatedAt: day(2)},
		// Already judged — re-classifying would pay twice for the same answer.
		{ID: "done", CreatedAt: day(3), Durability: domain.DurabilityDurable},
		// Cannot surface at recall, so a verdict buys the reader nothing.
		{ID: "deprecated", CreatedAt: day(3), Status: domain.ClaimStatusDeprecated},
		{ID: "closed", CreatedAt: day(3), ValidTo: closed},
	}
	got := unclassifiedNewestFirst(claims, 0)
	want := []string{"newest", "middle", "oldest"}
	if len(got) != len(want) {
		t.Fatalf("selected %d, want %d: %v", len(got), len(want), ids(got))
	}
	for i, w := range want {
		if got[i].ID != w {
			t.Fatalf("position %d = %q, want %q (%v)", i, got[i].ID, w, ids(got))
		}
	}
}

func TestUnclassifiedNewestFirst_RespectsLimit(t *testing.T) {
	claims := []domain.Claim{
		{ID: "a", CreatedAt: day(3)},
		{ID: "b", CreatedAt: day(2)},
		{ID: "c", CreatedAt: day(1)},
	}
	got := unclassifiedNewestFirst(claims, 2)
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
		t.Fatalf("limit must keep the newest beliefs: %v", ids(got))
	}
}

// A dry run and the apply that follows must pick the SAME beliefs, or the
// preview describes work the apply does not do. Ties are the common case here,
// not the exception: thousands of beliefs share a capture timestamp.
func TestUnclassifiedNewestFirst_TiesBreakDeterministically(t *testing.T) {
	same := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	claims := []domain.Claim{
		{ID: "c", CreatedAt: same},
		{ID: "a", CreatedAt: same},
		{ID: "b", CreatedAt: same},
	}
	first := ids(unclassifiedNewestFirst(claims, 2))
	for i := 0; i < 5; i++ {
		got := ids(unclassifiedNewestFirst(claims, 2))
		if got[0] != first[0] || got[1] != first[1] {
			t.Fatalf("selection is not stable across runs: %v then %v", first, got)
		}
	}
	if first[0] != "a" || first[1] != "b" {
		t.Fatalf("ties must break on ID, got %v", first)
	}
}

// An unrecognised value normalizes to unknown, so such a belief must still be a
// classification target rather than being skipped forever as "already judged".
func TestUnclassifiedNewestFirst_TreatsGarbageAsUnclassified(t *testing.T) {
	claims := []domain.Claim{{ID: "weird", CreatedAt: day(1), Durability: domain.Durability("nonsense")}}
	if got := unclassifiedNewestFirst(claims, 0); len(got) != 1 {
		t.Fatalf("a belief with an unrecognised verdict must be re-classified, got %v", ids(got))
	}
}

func TestCountUnclassifiedRecallable(t *testing.T) {
	claims := []domain.Claim{
		{ID: "a", CreatedAt: day(1)},
		{ID: "b", CreatedAt: day(1), Durability: domain.DurabilitySessionLocal},
		{ID: "c", CreatedAt: day(1), Status: domain.ClaimStatusDeprecated},
	}
	if got := countUnclassifiedRecallable(claims); got != 1 {
		t.Fatalf("count = %d, want 1 (only the unclassified recallable belief)", got)
	}
}

func ids(cs []domain.Claim) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.ID
	}
	return out
}
