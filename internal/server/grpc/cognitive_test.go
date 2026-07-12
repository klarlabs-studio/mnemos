package grpc_test

import (
	"context"
	"net"
	"testing"

	mnemos "go.klarlabs.de/mnemos"
	mnemosgrpc "go.klarlabs.de/mnemos/internal/server/grpc"
	"go.klarlabs.de/mnemos/internal/store"
	_ "go.klarlabs.de/mnemos/internal/store/memory"
	mnemosv1 "go.klarlabs.de/mnemos/proto/gen/mnemos/v1"

	grpclib "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

func startBrainServer(t *testing.T, mem mnemos.Memory) (mnemosv1.MnemosServiceClient, func()) {
	t.Helper()
	conn, err := store.Open(context.Background(), "memory://")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	mnemosSrv := mnemosgrpc.NewServerWithMemory(conn, mem, nil, testLogger(), "test")
	srv := grpclib.NewServer(grpclib.UnaryInterceptor(mnemosSrv.UnaryInterceptor()))
	mnemosSrv.Register(srv)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()

	cc, err := grpclib.NewClient(lis.Addr().String(), grpclib.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	cleanup := func() {
		_ = cc.Close()
		srv.GracefulStop()
		_ = conn.Close()
	}
	return mnemosv1.NewMnemosServiceClient(cc), cleanup
}

// TestGRPC_CognitiveDelegates: with a facade, the connected-brain RPCs return
// well-formed (empty) responses over the wire — proving the delegation +
// serialization path.
func TestGRPC_CognitiveDelegates(t *testing.T) {
	mem, err := mnemos.New(mnemos.WithStorage("memory://"))
	if err != nil {
		t.Fatalf("build facade: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	client, cleanup := startBrainServer(t, mem)
	defer cleanup()
	ctx := context.Background()

	if _, err := client.WhoKnows(ctx, &mnemosv1.WhoKnowsRequest{Query: "creatinine", Limit: 5}); err != nil {
		t.Errorf("WhoKnows: %v", err)
	}
	if _, err := client.KnowledgeGaps(ctx, &mnemosv1.KnowledgeGapsRequest{Limit: 10}); err != nil {
		t.Errorf("KnowledgeGaps: %v", err)
	}
	if _, err := client.Calibration(ctx, &mnemosv1.CalibrationRequest{}); err != nil {
		t.Errorf("Calibration: %v", err)
	}
	if _, err := client.Hypercorrections(ctx, &mnemosv1.HypercorrectionsRequest{}); err != nil {
		t.Errorf("Hypercorrections: %v", err)
	}
	if _, err := client.Recombinations(ctx, &mnemosv1.RecombinationsRequest{Limit: 10}); err != nil {
		t.Errorf("Recombinations: %v", err)
	}
	if _, err := client.AnalogousBeliefs(ctx, &mnemosv1.AnalogousBeliefsRequest{BeliefId: "cl1", Limit: 5}); err != nil {
		t.Errorf("AnalogousBeliefs: %v", err)
	}
}

// TestGRPC_CognitiveUnavailableWithoutFacade: nil facade → codes.Unavailable,
// not a panic or a misleading empty result.
func TestGRPC_CognitiveUnavailableWithoutFacade(t *testing.T) {
	client, cleanup := startBrainServer(t, nil)
	defer cleanup()
	_, err := client.WhoKnows(context.Background(), &mnemosv1.WhoKnowsRequest{Query: "x"})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("want codes.Unavailable, got %v", err)
	}
}
