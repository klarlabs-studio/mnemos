package mnemos

import (
	"context"
	"testing"
	"time"
)

// seedLowConfClaim persists a low-confidence indexed claim (so a single recall of
// it reads as insufficient, driving the iterative loop to keep going).
func seedLowConfClaim(t *testing.T, m *memory, text string) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	evID := "ev-" + text[:min(6, len(text))] + time.Now().Format("150405.000000000")
	if err := m.RememberEvent(ctx, Event{ID: evID, At: now, Type: "observation", Content: text}); err != nil {
		t.Fatalf("RememberEvent: %v", err)
	}
	id, err := m.RememberClaim(ctx, ClaimItem{Text: text, Confidence: 0.2, EventIDs: []string{evID}, ValidFrom: now})
	if err != nil {
		t.Fatalf("RememberClaim: %v", err)
	}
	return id
}

// TestRecallIterative_ExpandsToGap verifies the retrieve↔reason fixpoint reaches a
// claim that a single-shot recall of the original query misses: round 0 finds A
// (insufficient), reasons a follow-up from A's terms, and a later round pulls in B.
func TestRecallIterative_ExpandsToGap(t *testing.T) {
	m := calibMem(t)
	ctx := context.Background()

	bID := seedLowConfClaim(t, m, "checkout latency spike root cause")
	_ = seedLowConfClaim(t, m, "deploy caused checkout latency") // A — matches the query

	// Single-shot on "deploy" finds A but not B (B has no "deploy" token, hop 0).
	single, err := m.Recall(ctx, Query{Text: "deploy"})
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if containsID(single, bID) {
		t.Fatalf("precondition: single-shot recall should not reach B; got %v", resultIDs(single))
	}

	results, rounds, err := m.RecallIterative(ctx, Query{Text: "deploy"}, 4)
	if err != nil {
		t.Fatalf("RecallIterative: %v", err)
	}
	if !containsID(results, bID) {
		t.Errorf("iterative loop should reach the gap claim B; got %v (rounds=%d)", resultIDs(results), rounds)
	}
	if rounds < 2 {
		t.Errorf("expected >= 2 rounds (a follow-up was needed); got %d", rounds)
	}
	if rounds > 4 {
		t.Errorf("rounds %d exceeded the budget of 4", rounds)
	}
}

// TestRecallIterative_SufficientStopsEarly verifies a well-covered query resolves
// in a single round (no wasted follow-ups).
func TestRecallIterative_SufficientStopsEarly(t *testing.T) {
	m := calibMem(t)
	ctx := context.Background()
	for _, txt := range []string{
		"The checkout service latency spikes after a deploy.",
		"Checkout latency spikes correlate with the deploy rollout.",
		"After each checkout deploy, latency spikes for ten minutes.",
		"Deploys to checkout cause a transient latency spike.",
		"Checkout latency returns to baseline once the deploy settles.",
	} {
		seedTextClaim(t, m, txt)
	}
	_, rounds, err := m.RecallIterative(ctx, Query{Text: "checkout deploy latency spike"}, 4)
	if err != nil {
		t.Fatalf("RecallIterative: %v", err)
	}
	if rounds != 1 {
		t.Errorf("a sufficient corpus should resolve in 1 round; got %d", rounds)
	}
}
