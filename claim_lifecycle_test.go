package mnemos_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go.klarlabs.de/mnemos"

	_ "go.klarlabs.de/mnemos/internal/store/memory"
)

// seedSeq makes evidence-event ids unique within a test without deriving
// them from claim text (which could be empty or start with a multi-byte
// rune). Incremented by each seedClaimLifecycle call.
var seedSeq int

// seedClaimLifecycle persists an evidence event then a claim in the given
// lifecycle state and returns the claim id.
func seedClaimLifecycle(t *testing.T, mem mnemos.Memory, text string, lc mnemos.ClaimLifecycle) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now()
	seedSeq++
	evID := fmt.Sprintf("ev-%s-%d", now.Format("150405.000000000"), seedSeq)
	if err := mem.RememberEvent(ctx, mnemos.Event{ID: evID, At: now, Type: "observation", Content: text}); err != nil {
		t.Fatalf("RememberEvent: %v", err)
	}
	id, err := mem.RememberClaim(ctx, mnemos.ClaimItem{
		Text:      text,
		EventIDs:  []string{evID},
		ValidFrom: now,
		Lifecycle: lc,
	})
	if err != nil {
		t.Fatalf("RememberClaim: %v", err)
	}
	return id
}

// TestClaimLifecycle_RoundTrip verifies the lifecycle written on a ClaimItem
// survives the write and reads back on the public Claim projection.
func TestClaimLifecycle_RoundTrip(t *testing.T) {
	mem := newReadMemory(t, "lifecycle_roundtrip")
	ctx := context.Background()

	id := seedClaimLifecycle(t, mem, "Postgres RLS enforces tenant isolation.", mnemos.ClaimLifecyclePromoted)

	claim, err := mem.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if claim.Lifecycle != mnemos.ClaimLifecyclePromoted {
		t.Errorf("Get returned lifecycle %q, want %q", claim.Lifecycle, mnemos.ClaimLifecyclePromoted)
	}

	// An unset lifecycle round-trips as empty (the "never routed through a
	// candidate→promoted review" default), not a spurious value.
	plainID := seedClaimLifecycle(t, mem, "The cache TTL is five minutes.", "")
	plain, err := mem.Get(ctx, plainID)
	if err != nil {
		t.Fatalf("Get plain: %v", err)
	}
	if plain.Lifecycle != "" {
		t.Errorf("plain claim lifecycle = %q, want empty", plain.Lifecycle)
	}
}

// TestSetClaimLifecycle_Transition verifies the in-place lifecycle
// transition: a candidate claim promoted to promoted, then superseded,
// each state reading back via Get. An invalid target is rejected and an
// unknown claim id errors.
func TestSetClaimLifecycle_Transition(t *testing.T) {
	mem := newReadMemory(t, "lifecycle_transition")
	ctx := context.Background()

	id := seedClaimLifecycle(t, mem, "RollOps ships on pushed version tags.", mnemos.ClaimLifecycleCandidate)

	if err := mem.SetClaimLifecycle(ctx, id, mnemos.ClaimLifecyclePromoted); err != nil {
		t.Fatalf("SetClaimLifecycle promoted: %v", err)
	}
	got, err := mem.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Lifecycle != mnemos.ClaimLifecyclePromoted {
		t.Errorf("after promote, lifecycle = %q, want promoted", got.Lifecycle)
	}

	if err := mem.SetClaimLifecycle(ctx, id, mnemos.ClaimLifecycleSuperseded); err != nil {
		t.Fatalf("SetClaimLifecycle superseded: %v", err)
	}
	got, err = mem.Get(ctx, id)
	if err != nil {
		t.Fatalf("Get 2: %v", err)
	}
	if got.Lifecycle != mnemos.ClaimLifecycleSuperseded {
		t.Errorf("after supersede, lifecycle = %q, want superseded", got.Lifecycle)
	}

	if err := mem.SetClaimLifecycle(ctx, id, mnemos.ClaimLifecycle("bogus")); err == nil {
		t.Error("SetClaimLifecycle accepted an invalid target; want rejection")
	}
	if err := mem.SetClaimLifecycle(ctx, "cl_missing", mnemos.ClaimLifecyclePromoted); err == nil {
		t.Error("SetClaimLifecycle on unknown claim returned nil; want error")
	}
}

// TestClaimLifecycle_RejectsInvalid verifies RememberClaim rejects an
// unrecognised lifecycle at the write boundary, so recall's lifecycle
// filter can't be defeated by an arbitrary stored string. Empty stays valid.
func TestClaimLifecycle_RejectsInvalid(t *testing.T) {
	mem := newReadMemory(t, "lifecycle_reject")
	ctx := context.Background()

	if err := mem.RememberEvent(ctx, mnemos.Event{ID: "ev-bad", At: time.Now(), Type: "observation", Content: "x"}); err != nil {
		t.Fatalf("RememberEvent: %v", err)
	}
	_, err := mem.RememberClaim(ctx, mnemos.ClaimItem{
		Text:      "A claim with a bogus lifecycle.",
		EventIDs:  []string{"ev-bad"},
		Lifecycle: mnemos.ClaimLifecycle("bogus"),
	})
	if err == nil {
		t.Fatal("RememberClaim accepted an invalid lifecycle; want rejection")
	}
}

// TestClaimLifecycle_RecallFilter verifies Query.Lifecycle narrows recall to
// a single promotion state, and that an empty filter is a no-op (all claims
// remain eligible regardless of lifecycle).
func TestClaimLifecycle_RecallFilter(t *testing.T) {
	mem := newReadMemory(t, "lifecycle_recall")
	ctx := context.Background()

	seedClaimLifecycle(t, mem, "Deploys ship on pushed version tags.", mnemos.ClaimLifecyclePromoted)
	seedClaimLifecycle(t, mem, "Deploys might use a manifest apply.", mnemos.ClaimLifecycleCandidate)

	// Filter to promoted: only the human-endorsed claim is eligible.
	promoted, err := mem.Recall(ctx, mnemos.Query{
		Text:      "how do deploys ship",
		Lifecycle: mnemos.ClaimLifecyclePromoted,
	})
	if err != nil {
		t.Fatalf("Recall promoted: %v", err)
	}
	for _, r := range promoted {
		got, gerr := mem.Get(ctx, r.ClaimID)
		if gerr != nil {
			t.Fatalf("Get %s: %v", r.ClaimID, gerr)
		}
		if got.Lifecycle != mnemos.ClaimLifecyclePromoted {
			t.Errorf("promoted-filtered recall returned claim with lifecycle %q", got.Lifecycle)
		}
	}

	// Empty filter is a no-op: recall is not constrained by lifecycle.
	all, err := mem.Recall(ctx, mnemos.Query{Text: "how do deploys ship"})
	if err != nil {
		t.Fatalf("Recall all: %v", err)
	}
	if len(all) < len(promoted) {
		t.Errorf("unfiltered recall returned %d < promoted-filtered %d; empty filter must not narrow", len(all), len(promoted))
	}
}
