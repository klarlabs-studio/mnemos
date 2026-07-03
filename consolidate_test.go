package mnemos_test

import (
	"context"
	"testing"

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
