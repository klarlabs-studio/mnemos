package relate

import (
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// The relate passes that run on every write never emit `causes`. The text
// heuristic was measured on a real brain and found nearly no causal pairs among
// the edges it would draw (see DetectCausal), so causal edges come only from
// recorded interventions. If a later change wires DetectCausal into Detect or
// DetectIncremental, this fails and points at the measurement that has to be
// redone first.
func TestDetect_NeverEmitsCausalEdges(t *testing.T) {
	at := time.Date(2026, 4, 20, 9, 0, 0, 0, time.UTC)
	claims := []domain.Claim{
		{ID: "a", Text: "We deployed payments service v1.42 to production", ValidFrom: at},
		{ID: "b", Text: "Payments service latency spiked to 1200ms after the change", ValidFrom: at.Add(5 * time.Minute)},
	}
	if rels, err := NewEngine().DetectCausal(claims); err != nil || len(rels) == 0 {
		t.Fatalf("precondition: the heuristic alone should fire on this pair (%v, %d edges)", err, len(rels))
	}

	batch, err := NewEngine().Detect(claims)
	if err != nil {
		t.Fatal(err)
	}
	incremental, err := NewEngine().DetectIncremental(claims[1:], claims[:1])
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range append(batch, incremental...) {
		if r.Type == domain.RelationshipTypeCauses || r.Type == domain.RelationshipTypeCausedBy {
			t.Fatalf("relate emitted a %s edge %s -> %s; causal edges come only from interventions", r.Type, r.FromClaimID, r.ToClaimID)
		}
	}
}

// A pair with a missing timestamp has no ordering to reason about, so the
// heuristic must not draw an edge. The doc comment always said so; the code let
// such pairs through.
func TestPlausibleCausalOrdering_MissingTimestampIsNoSignal(t *testing.T) {
	at := time.Date(2026, 4, 20, 9, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name          string
		cause, effect time.Time
		want          bool
	}{
		{"both missing", time.Time{}, time.Time{}, false},
		{"cause missing", time.Time{}, at, false},
		{"effect missing", at, time.Time{}, false},
		{"ordered within tolerance", at, at.Add(time.Hour), true},
		{"reversed", at.Add(time.Hour), at, false},
		{"too far apart", at, at.Add(causalTimeTolerance + time.Minute), false},
	} {
		if got := plausibleCausalOrdering(tc.cause, tc.effect); got != tc.want {
			t.Errorf("%s: plausibleCausalOrdering = %v, want %v", tc.name, got, tc.want)
		}
	}
}
