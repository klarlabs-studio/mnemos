package grpc_test

import (
	"context"
	"testing"

	mnemos "go.klarlabs.de/mnemos"
	mnemosv1 "go.klarlabs.de/mnemos/proto/gen/mnemos/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestGRPC_WorkingMemAndTemporal(t *testing.T) {
	mem, err := mnemos.New(mnemos.WithStorage("memory://"))
	if err != nil {
		t.Fatalf("build facade: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	client, cleanup := startBrainServer(t, mem)
	defer cleanup()
	ctx := context.Background()

	// SetBlock then GetBlocks round-trips.
	if _, err := client.SetBlock(ctx, &mnemosv1.SetBlockRequest{Owner: "grace", Label: "focus", Value: "incident-1"}); err != nil {
		t.Fatalf("SetBlock: %v", err)
	}
	blocks, err := client.GetBlocks(ctx, &mnemosv1.GetBlocksRequest{Owner: "grace"})
	if err != nil {
		t.Fatalf("GetBlocks: %v", err)
	}
	if len(blocks.GetBlocks()) != 1 || blocks.GetBlocks()[0].GetLabel() != "focus" {
		t.Fatalf("GetBlocks round-trip: %+v", blocks.GetBlocks())
	}

	// Synthesize, Timeline, Signals return OK over the wire (empty store).
	if _, err := client.Synthesize(ctx, &mnemosv1.SynthesizeRequest{}); err != nil {
		t.Errorf("Synthesize: %v", err)
	}
	if _, err := client.Timeline(ctx, &mnemosv1.TimelineRequest{RunId: "r"}); err != nil {
		t.Errorf("Timeline: %v", err)
	}
	if _, err := client.Signals(ctx, &mnemosv1.SignalsRequest{RunId: "r"}); err != nil {
		t.Errorf("Signals: %v", err)
	}
}

func TestGRPC_WorkingMemValidation(t *testing.T) {
	mem, err := mnemos.New(mnemos.WithStorage("memory://"))
	if err != nil {
		t.Fatalf("build facade: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	client, cleanup := startBrainServer(t, mem)
	defer cleanup()
	ctx := context.Background()

	if _, err := client.GetBlocks(ctx, &mnemosv1.GetBlocksRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("GetBlocks no owner: want InvalidArgument, got %v", err)
	}
	if _, err := client.SetBlock(ctx, &mnemosv1.SetBlockRequest{Owner: "grace"}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("SetBlock no label: want InvalidArgument, got %v", err)
	}
}

func TestGRPC_WorkingMemUnavailableWithoutFacade(t *testing.T) {
	client, cleanup := startBrainServer(t, nil)
	defer cleanup()
	_, err := client.Synthesize(context.Background(), &mnemosv1.SynthesizeRequest{})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("want codes.Unavailable, got %v", err)
	}
}
