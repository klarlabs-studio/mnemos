package grpc_test

import (
	"context"
	"testing"

	mnemos "go.klarlabs.de/mnemos"
	mnemosv1 "go.klarlabs.de/mnemos/proto/gen/mnemos/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestGRPC_RecallAllModes(t *testing.T) {
	mem, err := mnemos.New(mnemos.WithStorage("memory://"))
	if err != nil {
		t.Fatalf("build facade: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	client, cleanup := startBrainServer(t, mem)
	defer cleanup()
	ctx := context.Background()

	for _, mode := range []string{"sufficiency", "effort", "context", "conflicts", "iterative"} {
		resp, err := client.Recall(ctx, &mnemosv1.RecallRequest{Query: "creatinine", Mode: mode})
		if err != nil {
			t.Errorf("Recall mode=%s: %v", mode, err)
			continue
		}
		if resp.GetMode() != mode {
			t.Errorf("Recall mode=%s: echoed mode %q", mode, resp.GetMode())
		}
	}
}

func TestGRPC_RecallValidation(t *testing.T) {
	mem, err := mnemos.New(mnemos.WithStorage("memory://"))
	if err != nil {
		t.Fatalf("build facade: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	client, cleanup := startBrainServer(t, mem)
	defer cleanup()
	ctx := context.Background()

	if _, err := client.Recall(ctx, &mnemosv1.RecallRequest{Query: ""}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("empty query: want InvalidArgument, got %v", err)
	}
	if _, err := client.Recall(ctx, &mnemosv1.RecallRequest{Query: "x", Mode: "bogus"}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("bad mode: want InvalidArgument, got %v", err)
	}
}

func TestGRPC_RecallUnavailableWithoutFacade(t *testing.T) {
	client, cleanup := startBrainServer(t, nil)
	defer cleanup()
	_, err := client.Recall(context.Background(), &mnemosv1.RecallRequest{Query: "x"})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("want codes.Unavailable, got %v", err)
	}
}
