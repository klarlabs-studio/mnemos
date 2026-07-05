package mnemos_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/mnemos"

	_ "go.klarlabs.de/mnemos/internal/store/memory"
	_ "go.klarlabs.de/mnemos/internal/store/sqlite"
)

// TestBlocks_SQLiteBackend exercises the block lifecycle against the SQLite
// backend (raw SQL) through the public API, so the sqlite repository — not just
// the in-memory one — is covered.
func TestBlocks_SQLiteBackend(t *testing.T) {
	clearMnemosEnv(t)
	mem, err := mnemos.New(
		mnemos.WithSQLite(filepath.Join(t.TempDir(), "blocks.db")),
		mnemos.WithPassiveMode(),
	)
	if err != nil {
		t.Fatalf("New sqlite: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	ctx := context.Background()
	const owner = "grace"

	if err := mem.SetBlock(ctx, owner, "persona", "analyst"); err != nil {
		t.Fatalf("SetBlock: %v", err)
	}
	if err := mem.AppendBlock(ctx, owner, "persona", "terse"); err != nil {
		t.Fatalf("AppendBlock: %v", err)
	}
	blocks, err := mem.Blocks(ctx, owner)
	if err != nil {
		t.Fatalf("Blocks: %v", err)
	}
	if len(blocks) != 1 || blocks[0].Value != "analyst\nterse" {
		t.Fatalf("sqlite round-trip wrong: %+v", blocks)
	}
	if err := mem.SetBlock(ctx, owner, "persona", ""); err != nil {
		t.Fatalf("SetBlock delete: %v", err)
	}
	if blocks, _ := mem.Blocks(ctx, owner); len(blocks) != 0 {
		t.Errorf("delete failed on sqlite: %d blocks remain", len(blocks))
	}
}

// TestBlocks_SetListGet covers the working-memory block lifecycle: set two
// labeled blocks, read them back label-ordered, and confirm values round-trip.
func TestBlocks_SetListGet(t *testing.T) {
	mem := newReadMemory(t, "blocks_basic")
	ctx := context.Background()
	const owner = "grace"

	if err := mem.SetBlock(ctx, owner, "persona", "Incident analyst. Terse, evidence-first."); err != nil {
		t.Fatalf("SetBlock persona: %v", err)
	}
	if err := mem.SetBlock(ctx, owner, "open_threads", "Investigating checkout latency."); err != nil {
		t.Fatalf("SetBlock open_threads: %v", err)
	}

	blocks, err := mem.Blocks(ctx, owner)
	if err != nil {
		t.Fatalf("Blocks: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2", len(blocks))
	}
	// Label-ordered: open_threads < persona.
	if blocks[0].Label != "open_threads" || blocks[1].Label != "persona" {
		t.Errorf("blocks not label-ordered: %q, %q", blocks[0].Label, blocks[1].Label)
	}
	if blocks[1].Value != "Incident analyst. Terse, evidence-first." {
		t.Errorf("persona value round-trip wrong: %q", blocks[1].Value)
	}
	if blocks[0].UpdatedAt.IsZero() {
		t.Error("UpdatedAt not populated")
	}

	// Another owner sees nothing.
	other, err := mem.Blocks(ctx, "kai")
	if err != nil {
		t.Fatalf("Blocks(kai): %v", err)
	}
	if len(other) != 0 {
		t.Errorf("owner isolation: kai sees %d blocks, want 0", len(other))
	}
}

// TestSetBlock_ReplaceAndDelete verifies set replaces in place and an empty value
// deletes the block.
func TestSetBlock_ReplaceAndDelete(t *testing.T) {
	mem := newReadMemory(t, "blocks_replace")
	ctx := context.Background()
	const owner = "grace"

	_ = mem.SetBlock(ctx, owner, "focus", "first")
	if err := mem.SetBlock(ctx, owner, "focus", "second"); err != nil {
		t.Fatalf("SetBlock replace: %v", err)
	}
	blocks, _ := mem.Blocks(ctx, owner)
	if len(blocks) != 1 || blocks[0].Value != "second" {
		t.Fatalf("replace failed: %+v", blocks)
	}
	if err := mem.SetBlock(ctx, owner, "focus", ""); err != nil {
		t.Fatalf("SetBlock delete: %v", err)
	}
	blocks, _ = mem.Blocks(ctx, owner)
	if len(blocks) != 0 {
		t.Errorf("empty value should delete; got %d blocks", len(blocks))
	}
}

// TestSetBlock_TooLarge verifies working memory is bounded: an over-limit value
// is rejected rather than silently truncated.
func TestSetBlock_TooLarge(t *testing.T) {
	mem := newReadMemory(t, "blocks_toolarge")
	ctx := context.Background()
	oversize := strings.Repeat("x", mnemos.WorkingMemoryBlockLimit+1)
	err := mem.SetBlock(ctx, "grace", "persona", oversize)
	if err == nil {
		t.Fatal("SetBlock accepted an over-limit value; want ErrBlockTooLarge")
	}
	if err.Error() != mnemos.ErrBlockTooLarge.Error() {
		t.Errorf("got %v, want ErrBlockTooLarge", err)
	}
}

// TestAppendBlock_EvictsOldest verifies the attention-budget eviction: appending
// past the cap keeps the block within the limit by dropping the OLDEST lines
// while retaining the newest.
func TestAppendBlock_EvictsOldest(t *testing.T) {
	mem := newReadMemory(t, "blocks_evict")
	ctx := context.Background()
	const owner = "grace"

	// Each line ~100 chars; ~30 lines overflows the 2000-char cap.
	pad := strings.Repeat("_", 88)
	for i := 0; i < 30; i++ {
		line := fmt.Sprintf("line%02d %s", i, pad)
		if err := mem.AppendBlock(ctx, owner, "log", line); err != nil {
			t.Fatalf("AppendBlock %d: %v", i, err)
		}
	}
	blocks, err := mem.Blocks(ctx, owner)
	if err != nil {
		t.Fatalf("Blocks: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1", len(blocks))
	}
	got := blocks[0].Value
	if len(got) > mnemos.WorkingMemoryBlockLimit {
		t.Errorf("block over cap after eviction: %d > %d", len(got), mnemos.WorkingMemoryBlockLimit)
	}
	if !strings.Contains(got, "line29") {
		t.Error("newest line (line29) evicted — eviction must keep the freshest content")
	}
	if strings.Contains(got, "line00") {
		t.Error("oldest line (line00) survived — eviction must drop the oldest first")
	}
}
