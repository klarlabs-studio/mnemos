package main

import (
	"context"
	"testing"

	mnemos "go.klarlabs.de/mnemos"
)

func TestMCPWorkingMemoryAndSignals(t *testing.T) {
	mem, err := mnemos.New(mnemos.WithStorage("memory://"))
	if err != nil {
		t.Fatalf("build facade: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	ctx := context.Background()

	// Empty store: an owner has no blocks yet.
	if out, err := mcpGetBlocks(ctx, mem, mcpGetBlocksInput{Owner: "grace"}); err != nil || out.Blocks == nil {
		t.Fatalf("get_blocks empty: %v %+v", err, out)
	}

	// set_block (replace) then read it back.
	if _, err := mcpSetBlock(ctx, mem, mcpSetBlockInput{Owner: "grace", Label: "persona", Value: "Incident analyst."}); err != nil {
		t.Fatalf("set_block: %v", err)
	}
	// append onto it.
	if _, err := mcpSetBlock(ctx, mem, mcpSetBlockInput{Owner: "grace", Label: "persona", Value: "Terse.", Append: true}); err != nil {
		t.Fatalf("set_block append: %v", err)
	}
	out, err := mcpGetBlocks(ctx, mem, mcpGetBlocksInput{Owner: "grace"})
	if err != nil {
		t.Fatalf("get_blocks: %v", err)
	}
	if len(out.Blocks) != 1 || out.Blocks[0].Label != "persona" {
		t.Fatalf("get_blocks after write: %+v", out)
	}

	// signals over an empty run is the healthy zero-case.
	if sig, err := mcpSignals(ctx, mem, mcpSignalsInput{RunID: "run-x"}); err != nil || sig.Signals == nil {
		t.Fatalf("signals: %v %+v", err, sig)
	}
}
