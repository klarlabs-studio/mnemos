package curiosity

import "testing"

// The uncertain/stale belief must outrank the well-corroborated one, and the
// corroborated one must not even enter the queue (nothing to acquire).
func TestPrioritize_RanksUncertainStaleFirst(t *testing.T) {
	beliefs := []Belief{
		{
			ClaimID:  "strong",
			Text:     "well-corroborated fact",
			Trust:    0.95,
			Salience: 0.9,
		},
		{
			ClaimID:   "weak",
			Text:      "shaky stale belief",
			Trust:     0.15,
			Salience:  0.6,
			Staleness: 0.8,
			StaleDays: 200,
			Stale:     true,
		},
	}

	got := Prioritize(beliefs, 0)
	if len(got) != 1 {
		t.Fatalf("expected only the uncertain belief queued, got %d items: %+v", len(got), got)
	}
	if got[0].ClaimID != "weak" {
		t.Fatalf("expected 'weak' ranked first, got %q", got[0].ClaimID)
	}
	if got[0].Action != ActionVerify {
		t.Fatalf("stale belief should call for verify, got %q", got[0].Action)
	}
	if got[0].Reason != ReasonStale {
		t.Fatalf("expected primary reason stale, got %q", got[0].Reason)
	}
}

func TestPrioritize_EmptyStore(t *testing.T) {
	if got := Prioritize(nil, 0); len(got) != 0 {
		t.Fatalf("empty input must yield empty queue, got %+v", got)
	}
	// A store of only strong beliefs is effectively empty for curiosity.
	strong := []Belief{{ClaimID: "a", Trust: 0.9, Salience: 0.9}}
	if got := Prioritize(strong, 0); len(got) != 0 {
		t.Fatalf("all-strong store must yield empty queue, got %+v", got)
	}
}

func TestPrioritize_LimitRespected(t *testing.T) {
	beliefs := []Belief{
		{ClaimID: "a", Text: "a", Trust: 0.1, Salience: 0.9, Stale: true, Staleness: 0.9},
		{ClaimID: "b", Text: "b", Trust: 0.2, Salience: 0.8, Stale: true, Staleness: 0.8},
		{ClaimID: "c", Text: "c", Trust: 0.3, Salience: 0.7, Stale: true, Staleness: 0.7},
	}
	got := Prioritize(beliefs, 2)
	if len(got) != 2 {
		t.Fatalf("limit=2 must cap the queue at 2, got %d", len(got))
	}
	// The two highest-priority beliefs survive the cap, in order.
	if got[0].ClaimID != "a" || got[1].ClaimID != "b" {
		t.Fatalf("limit must keep the top-ranked items, got %q,%q", got[0].ClaimID, got[1].ClaimID)
	}
}

// Contested beliefs are the most urgent reason and route to resolve.
func TestPrioritize_ContestedRoutesToResolve(t *testing.T) {
	beliefs := []Belief{
		{
			ClaimID:        "x",
			Text:           "disputed",
			Trust:          0.4,
			Salience:       0.7,
			Contradictions: 3,
			Contested:      true,
			Stale:          true,
			Staleness:      0.5,
		},
	}
	got := Prioritize(beliefs, 0)
	if len(got) != 1 {
		t.Fatalf("expected 1 item, got %d", len(got))
	}
	if got[0].Reason != ReasonContested || got[0].Action != ActionResolve {
		t.Fatalf("contested belief should resolve, got reason=%q action=%q", got[0].Reason, got[0].Action)
	}
	// Both reasons are recorded, contested first.
	if len(got[0].Reasons) < 2 || got[0].Reasons[0] != ReasonContested {
		t.Fatalf("expected contested-first multi-reason list, got %+v", got[0].Reasons)
	}
}

// Over-confidence of the author bumps priority above an otherwise identical
// well-calibrated belief.
func TestPrioritize_OverconfidenceBump(t *testing.T) {
	base := Belief{ClaimID: "cal", Text: "c", Trust: 0.4, Salience: 0.6, Stale: true, Staleness: 0.5}
	calm := Prioritize([]Belief{base}, 0)
	over := base
	over.SourceOverconfidence = 0.6
	hot := Prioritize([]Belief{over}, 0)
	if hot[0].Priority <= calm[0].Priority {
		t.Fatalf("over-confident author should raise priority: %.4f !> %.4f", hot[0].Priority, calm[0].Priority)
	}
}
