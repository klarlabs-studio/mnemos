package grpc_test

import (
	"context"
	"testing"

	mnemos "go.klarlabs.de/mnemos"
	mnemosv1 "go.klarlabs.de/mnemos/proto/gen/mnemos/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestGRPC_ClaimCRUD(t *testing.T) {
	mem, err := mnemos.New(mnemos.WithStorage("memory://"))
	if err != nil {
		t.Fatalf("build facade: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	client, cleanup := startBrainServer(t, mem)
	defer cleanup()
	ctx := context.Background()

	// GetClaim on a missing id → NotFound.
	if _, err := client.GetClaim(ctx, &mnemosv1.GetClaimRequest{ClaimId: "nope"}); status.Code(err) != codes.NotFound {
		t.Errorf("GetClaim missing: want NotFound, got %v", err)
	}

	// Classify a statement → OK; empty text → InvalidArgument.
	if _, err := client.Classify(ctx, &mnemosv1.ClassifyRequest{Text: "a brand new statement"}); err != nil {
		t.Errorf("Classify: %v", err)
	}
	if _, err := client.Classify(ctx, &mnemosv1.ClassifyRequest{Text: ""}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("Classify empty: want InvalidArgument, got %v", err)
	}

	// SetClaimLifecycle with a bad value → InvalidArgument.
	if _, err := client.SetClaimLifecycle(ctx, &mnemosv1.SetClaimLifecycleRequest{ClaimId: "x", Lifecycle: "bogus"}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("SetClaimLifecycle bad: want InvalidArgument, got %v", err)
	}

	// GetDecision on a missing id → NotFound.
	if _, err := client.GetDecision(ctx, &mnemosv1.GetDecisionRequest{Id: "nope"}); status.Code(err) != codes.NotFound {
		t.Errorf("GetDecision missing: want NotFound, got %v", err)
	}
}

func TestGRPC_ClaimCRUDUnavailableWithoutFacade(t *testing.T) {
	client, cleanup := startBrainServer(t, nil)
	defer cleanup()
	_, err := client.GetClaim(context.Background(), &mnemosv1.GetClaimRequest{ClaimId: "x"})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("want codes.Unavailable, got %v", err)
	}
}
