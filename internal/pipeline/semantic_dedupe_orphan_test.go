package pipeline

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
	_ "go.klarlabs.de/mnemos/internal/store/memory"
)

// seedMergeClaim writes one active claim with its own anchoring event and
// evidence link, so a merge has real evidence to union.
func seedMergeClaim(t *testing.T, conn *store.Conn, id string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	if err := conn.Events.Append(ctx, domain.Event{
		ID: "ev_" + id, Content: "source for " + id, SourceInputID: "src_" + id,
		Timestamp: now, IngestedAt: now,
	}); err != nil {
		t.Fatalf("append event for %s: %v", id, err)
	}
	if err := conn.Claims.Upsert(ctx, []domain.Claim{{
		ID: id, Text: "claim " + id, Type: domain.ClaimTypeFact, Confidence: 0.8,
		Status: domain.ClaimStatusActive, CreatedAt: now, ValidFrom: now,
	}}); err != nil {
		t.Fatalf("upsert claim %s: %v", id, err)
	}
	if err := conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{{ClaimID: id, EventID: "ev_" + id}}); err != nil {
		t.Fatalf("upsert evidence for %s: %v", id, err)
	}
}

// seedLoserDependents hangs every kind of side-table row a claim can own off
// the losing claim: an entity link, a forward expectation, and feedback state.
// These are the dependents that used to survive the merge pointing at a claim
// that was no longer reachable.
func seedLoserDependents(t *testing.T, conn *store.Conn, loserID string) domain.Entity {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	ent, err := conn.Entities.FindOrCreate(ctx, "kafka", domain.EntityTypeConcept, "tester")
	if err != nil {
		t.Fatalf("FindOrCreate entity: %v", err)
	}
	if err := conn.Entities.LinkClaim(ctx, loserID, ent.ID, "mention"); err != nil {
		t.Fatalf("LinkClaim: %v", err)
	}
	if conn.Expectations != nil {
		if err := conn.Expectations.Upsert(ctx, domain.Expectation{
			ClaimID: loserID, Predicted: 42, Tolerance: 1, CreatedAt: now,
		}); err != nil {
			t.Fatalf("Expectations.Upsert: %v", err)
		}
	}
	if conn.Feedback != nil {
		if err := conn.Feedback.Upsert(ctx, domain.ClaimFeedback{
			ClaimID: loserID, HelpfulCount: 3, NegativeFeedbackStreak: 1, LastFeedbackAt: now,
		}); err != nil {
			t.Fatalf("Feedback.Upsert: %v", err)
		}
	}
	return ent
}

// evidenceEventsFor returns the event ids linked to a claim.
func evidenceEventsFor(t *testing.T, conn *store.Conn, claimID string) []string {
	t.Helper()
	links, err := conn.Claims.ListEvidenceByClaimIDs(context.Background(), []string{claimID})
	if err != nil {
		t.Fatalf("ListEvidenceByClaimIDs: %v", err)
	}
	out := make([]string, 0, len(links))
	for _, l := range links {
		out = append(out, l.EventID)
	}
	return out
}

// TestApplySemanticDedupe_StrandsNoOrphanedDependents is the N3 regression.
//
// The merge used to move the loser's evidence to the winner and then attempt
// the delete WITHOUT having dealt with the claim↔entity links. On an
// FK-enforcing backend that delete fails, so the merge aborted having already
// stripped the loser: an ACTIVE claim with zero evidence, still reachable by
// recall, plus an entity link, an expectation and feedback state all pointing
// at a claim the merge considered gone.
//
// The contract now: the winner ends up with the UNION of the evidence and every
// reachable dependent, and the loser survives only as a deprecated redirect to
// the winner (#387) — never as an active, un-evidenced claim.
func TestApplySemanticDedupe_StrandsNoOrphanedDependents(t *testing.T) {
	ctx := context.Background()
	_, conn := openDedupeDB(t)

	seedMergeClaim(t, conn, "cl_win")
	seedMergeClaim(t, conn, "cl_lose")
	ent := seedLoserDependents(t, conn, "cl_lose")

	merged, err := ApplySemanticDedupe(ctx, conn, SemanticDedupePlan{
		Merges: []SemanticMerge{{WinnerID: "cl_win", DuplicateIDs: []string{"cl_lose"}}},
	})
	if err != nil {
		t.Fatalf("ApplySemanticDedupe: %v", err)
	}
	if merged != 1 {
		t.Errorf("merged = %d, want 1", merged)
	}

	// 1. The loser is a deprecated redirect to the winner, not an active claim.
	assertRedirect(t, conn, "cl_lose", "cl_win")

	// 2. The winner retains the UNION of evidence, not just its own.
	events := evidenceEventsFor(t, conn, "cl_win")
	if !containsAll(events, "ev_cl_win", "ev_cl_lose") {
		t.Errorf("winner evidence = %v, want the union {ev_cl_win, ev_cl_lose}", events)
	}

	// 3. The loser's entity link moved to the winner instead of dangling.
	winnerEnts, _, err := conn.Entities.ListEntitiesForClaim(ctx, "cl_win")
	if err != nil {
		t.Fatalf("ListEntitiesForClaim: %v", err)
	}
	found := false
	for _, e := range winnerEnts {
		if e.ID == ent.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("winner did not inherit the loser's entity link %s; links=%+v", ent.ID, winnerEnts)
	}

	// 4. The loser's forward expectation moved to the winner.
	if conn.Expectations != nil {
		exp, ok, err := conn.Expectations.Get(ctx, "cl_win")
		if err != nil {
			t.Fatalf("Expectations.Get: %v", err)
		}
		if !ok || exp.Predicted != 42 {
			t.Errorf("winner did not inherit the loser's expectation: ok=%v exp=%+v", ok, exp)
		}
	}

	// 5. The loser's feedback counters folded into the winner.
	if conn.Feedback != nil {
		fb, ok, err := conn.Feedback.Get(ctx, "cl_win")
		if err != nil {
			t.Fatalf("Feedback.Get: %v", err)
		}
		if !ok || fb.HelpfulCount != 3 {
			t.Errorf("winner did not inherit the loser's feedback: ok=%v fb=%+v", ok, fb)
		}
	}
}

// TestApplySemanticDedupe_KeepsLoserAsRedirect: a merge keeps the losing row,
// deprecated and pointing at the winner, as Wikidata keeps a merged item as a
// redirect (#387). Its id still resolves, and the winner holds the union of the
// evidence.
func TestApplySemanticDedupe_KeepsLoserAsRedirect(t *testing.T) {
	ctx := context.Background()
	conn, err := store.Open(ctx, "memory://")
	if err != nil {
		t.Fatalf("open memory store: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	seedMergeClaim(t, conn, "cl_win")
	seedMergeClaim(t, conn, "cl_lose")

	merged, err := ApplySemanticDedupe(ctx, conn, SemanticDedupePlan{
		Merges: []SemanticMerge{{WinnerID: "cl_win", DuplicateIDs: []string{"cl_lose"}}},
	})
	if err != nil {
		t.Fatalf("ApplySemanticDedupe: %v", err)
	}
	if merged != 1 {
		t.Errorf("merged = %d, want 1", merged)
	}
	assertRedirect(t, conn, "cl_lose", "cl_win")
	if events := evidenceEventsFor(t, conn, "cl_win"); !containsAll(events, "ev_cl_win", "ev_cl_lose") {
		t.Errorf("winner evidence = %v, want the union", events)
	}
	if events := evidenceEventsFor(t, conn, "cl_lose"); len(events) != 0 {
		t.Errorf("the redirect kept evidence %v; it must all have moved to the winner", events)
	}
}

func assertRedirect(t *testing.T, conn *store.Conn, loserID, winnerID string) {
	t.Helper()
	got, err := conn.Claims.ListByIDs(context.Background(), []string{loserID})
	if err != nil {
		t.Fatalf("ListByIDs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("merged claim %s was deleted; it must stay as a redirect", loserID)
	}
	if got[0].Status != domain.ClaimStatusDeprecated {
		t.Errorf("redirect %s status = %q, want deprecated", loserID, got[0].Status)
	}
	if got[0].MergedInto != winnerID {
		t.Errorf("redirect %s merged_into = %q, want %q", loserID, got[0].MergedInto, winnerID)
	}
}

func containsAll(got []string, want ...string) bool {
	set := make(map[string]struct{}, len(got))
	for _, g := range got {
		set[g] = struct{}{}
	}
	for _, w := range want {
		if _, ok := set[w]; !ok {
			return false
		}
	}
	return true
}
