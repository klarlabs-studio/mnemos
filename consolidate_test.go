package mnemos_test

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos"

	_ "go.klarlabs.de/mnemos/internal/store/memory"
)

// TestConsolidate_EmptyStoreIsNoOp verifies the sleep pass runs cleanly on a
// store with nothing to merge: no error, nothing merged. The merge internals are
// covered by internal/pipeline's dedupe tests; this exercises the public method.
func TestConsolidate_EmptyStoreIsNoOp(t *testing.T) {
	clearMnemosEnv(t)
	mem, err := mnemos.New(mnemos.WithStorage("memory://?namespace=consolidate"), mnemos.WithPassiveMode())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	ctx := context.Background()

	res, err := mem.Consolidate(ctx, mnemos.ConsolidateOptions{})
	if err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	if res.Merged != 0 {
		t.Errorf("empty store merged %d claims, want 0", res.Merged)
	}

	// DryRun must never mutate.
	dry, err := mem.Consolidate(ctx, mnemos.ConsolidateOptions{DryRun: true})
	if err != nil {
		t.Fatalf("Consolidate(dry): %v", err)
	}
	if dry.Merged != 0 {
		t.Errorf("dry run reported %d merged; must be 0", dry.Merged)
	}
}

// TestConsolidate_ForgetsStaleButNotPromoted verifies active forgetting: a
// low-trust, non-promoted claim is deprecated (forgotten) while a promoted claim
// of equally low trust is protected — human-endorsed knowledge is never forgotten.
func TestConsolidate_ForgetsStaleButNotPromoted(t *testing.T) {
	clearMnemosEnv(t)
	mem, err := mnemos.New(mnemos.WithStorage("memory://?namespace=consolidate_forget"), mnemos.WithPassiveMode())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	ctx := context.Background()

	// Two low-confidence (→ low-trust) claims on DIFFERENT topics (so dedupe
	// doesn't merge them): one ordinary, one promoted.
	if err := mem.RememberEvent(ctx, mnemos.Event{ID: "ev-z", At: time.Now(), Type: "observation", Content: "zebra"}); err != nil {
		t.Fatal(err)
	}
	if err := mem.RememberEvent(ctx, mnemos.Event{ID: "ev-g", At: time.Now(), Type: "observation", Content: "giraffe"}); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.RememberClaim(ctx, mnemos.ClaimItem{Text: "the zebra migration finished", Confidence: 0.05, EventIDs: []string{"ev-z"}}); err != nil {
		t.Fatalf("RememberClaim(ordinary): %v", err)
	}
	if _, err := mem.RememberClaim(ctx, mnemos.ClaimItem{Text: "the giraffe deploy succeeded", Confidence: 0.05, EventIDs: []string{"ev-g"}, Lifecycle: mnemos.ClaimLifecyclePromoted}); err != nil {
		t.Fatalf("RememberClaim(promoted): %v", err)
	}

	res, err := mem.Consolidate(ctx, mnemos.ConsolidateOptions{ForgetBelowTrust: 0.5})
	if err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	if res.Forgotten != 1 {
		t.Fatalf("Forgotten = %d, want 1 (the ordinary low-trust claim, not the promoted one)", res.Forgotten)
	}

	// The promoted claim must still be recallable; the forgotten one must not.
	promoted, err := mem.Recall(ctx, mnemos.Query{Text: "giraffe deploy"})
	if err != nil {
		t.Fatalf("Recall promoted: %v", err)
	}
	if len(promoted) == 0 {
		t.Error("promoted claim was forgotten — human-endorsed knowledge must be protected")
	}
	forgotten, err := mem.Recall(ctx, mnemos.Query{Text: "zebra migration"})
	if err != nil {
		t.Fatalf("Recall forgotten: %v", err)
	}
	for _, r := range forgotten {
		if r.Text == "the zebra migration finished" {
			t.Error("forgotten claim still surfaces in recall — deprecation should exclude it")
		}
	}
}
