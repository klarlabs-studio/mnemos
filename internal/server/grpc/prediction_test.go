package grpc_test

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mnemos "go.klarlabs.de/mnemos"
	"go.klarlabs.de/mnemos/internal/auth"
	"go.klarlabs.de/mnemos/internal/domain"
	mnemosgrpc "go.klarlabs.de/mnemos/internal/server/grpc"
	"go.klarlabs.de/mnemos/internal/store"
	_ "go.klarlabs.de/mnemos/internal/store/sqlite"
	mnemosv1 "go.klarlabs.de/mnemos/proto/gen/mnemos/v1"

	grpclib "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// startSharedBrainServer serves one SQLite brain over gRPC with auth on: the store
// conn and the Memory facade point at the same file, so what an RPC wrote
// through the facade can be read back from the store.
func startSharedBrainServer(t *testing.T) (mnemosv1.MnemosServiceClient, *store.Conn, mnemos.Memory, *auth.Issuer) {
	t.Helper()
	dsn := "sqlite://" + filepath.Join(t.TempDir(), "brain.db")
	conn, err := store.Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	mem, err := mnemos.New(mnemos.WithStorage(dsn), mnemos.WithPassiveMode())
	if err != nil {
		t.Fatalf("build facade: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	secret, err := auth.GenerateSecret()
	if err != nil {
		t.Fatalf("secret: %v", err)
	}
	impl := mnemosgrpc.NewServerWithMemory(conn, mem, auth.NewVerifier(secret, conn.RevokedTokens), testLogger(), "test")
	t.Cleanup(func() { _ = impl.Close() })
	srv := grpclib.NewServer(grpclib.UnaryInterceptor(impl.UnaryInterceptor()))
	impl.Register(srv)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.GracefulStop)
	cc, err := grpclib.NewClient(lis.Addr().String(), grpclib.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = cc.Close() })
	return mnemosv1.NewMnemosServiceClient(cc), conn, mem, auth.NewIssuer(secret)
}

// The prediction loop over gRPC: set, read, observe and reconcile. Every
// response is the expectation as the store holds it, and the verdict lands in
// the store.
func TestGRPC_ExpectationLoop(t *testing.T) {
	client, conn, _, issuer := startSharedBrainServer(t)
	seedTwoRuns(t, conn)
	ctx := bearerCtx(t, mintToken(t, issuer, []string{domain.ScopeClaimsWrite}, nil))

	if _, err := client.GetExpectation(ctx, &mnemosv1.GetExpectationRequest{BeliefId: "cl-mine"}); status.Code(err) != codes.NotFound {
		t.Fatalf("GetExpectation before any: %v, want NotFound", err)
	}
	if _, err := client.RecordObservation(ctx, &mnemosv1.RecordObservationRequest{BeliefId: "cl-mine", Observed: 3}); status.Code(err) != codes.NotFound {
		t.Fatalf("RecordObservation with no expectation: %v, want NotFound", err)
	}
	if _, err := client.SetExpectation(ctx, &mnemosv1.SetExpectationRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("SetExpectation without belief_id: %v, want InvalidArgument", err)
	}

	horizon := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	set, err := client.SetExpectation(ctx, &mnemosv1.SetExpectationRequest{
		BeliefId: "cl-mine", Predicted: 10, Tolerance: -2, Horizon: timestamppb.New(horizon),
	})
	if err != nil {
		t.Fatalf("SetExpectation: %v", err)
	}
	if set.GetTolerance() != 2 || !set.GetHorizon().AsTime().Equal(horizon) || set.GetHasObservation() {
		t.Errorf("SetExpectation = %+v, want tolerance normalised to 2 and the horizon kept", set)
	}
	got, err := client.GetExpectation(ctx, &mnemosv1.GetExpectationRequest{BeliefId: "cl-mine"})
	if err != nil || got.GetPredicted() != 10 {
		t.Fatalf("GetExpectation = %+v, %v", got, err)
	}

	obs, err := client.RecordObservation(ctx, &mnemosv1.RecordObservationRequest{BeliefId: "cl-mine", Observed: 11})
	if err != nil {
		t.Fatalf("RecordObservation: %v", err)
	}
	if !obs.GetHasObservation() || obs.GetObserved() != 11 {
		t.Errorf("RecordObservation = %+v", obs)
	}

	rec, err := client.ReconcileExpectations(ctx, &mnemosv1.ReconcileExpectationsRequest{})
	if err != nil {
		t.Fatalf("ReconcileExpectations: %v", err)
	}
	if r := rec.GetReconciliations(); len(r) != 1 || r[0].GetBeliefId() != "cl-mine" || !r[0].GetConfirmed() {
		t.Fatalf("reconciliations = %+v, want cl-mine confirmed", r)
	}
	stored, ok, err := conn.Expectations.Get(context.Background(), "cl-mine")
	if err != nil || !ok || !stored.Resolved {
		t.Errorf("store: %+v ok=%v err=%v, want resolved", stored, ok, err)
	}
	again, err := client.ReconcileExpectations(ctx, &mnemosv1.ReconcileExpectationsRequest{})
	if err != nil || len(again.GetReconciliations()) != 0 {
		t.Errorf("second reconcile = %+v, %v; want nothing left open", again, err)
	}
}

// PredictiveError and BrainHealth answer what the library answers.
func TestGRPC_PredictiveErrorAndBrainHealth(t *testing.T) {
	client, conn, mem, issuer := startSharedBrainServer(t)
	seedTwoRuns(t, conn)
	ctx := bearerCtx(t, mintToken(t, issuer, []string{"claims:read"}, nil))

	pe, err := client.PredictiveError(ctx, &mnemosv1.PredictiveErrorRequest{})
	if err != nil {
		t.Fatalf("PredictiveError: %v", err)
	}
	want, err := mem.PredictiveError(context.Background())
	if err != nil {
		t.Fatalf("library: %v", err)
	}
	if pe.GetTotal() != want.Total || pe.GetHotspot() != want.Hotspot || len(pe.GetLevels()) != len(want.Levels) {
		t.Errorf("gRPC %+v, library %+v", pe, want)
	}

	for _, full := range []bool{false, true} {
		h, err := client.BrainHealth(ctx, &mnemosv1.BrainHealthRequest{Full: full})
		if err != nil {
			t.Fatalf("BrainHealth(full=%v): %v", full, err)
		}
		lib, err := mem.BrainHealthFull(context.Background())
		if err != nil {
			t.Fatalf("library health: %v", err)
		}
		if h.GetStatus() != string(lib.Status) || len(h.GetVitals()) != len(lib.Vitals) || len(h.GetPathologies()) != len(lib.Pathologies) || h.GetAt() == nil {
			t.Errorf("BrainHealth(full=%v) = %+v, library %+v", full, h, lib)
		}
	}
}

// Passages and ContextBlock read raw run content, so a run-scoped token must
// name one of its runs; inside it they return what the brain holds.
func TestGRPC_PassagesAndContextBlock(t *testing.T) {
	client, conn, _, issuer := startSharedBrainServer(t)
	seedTwoRuns(t, conn)
	open := bearerCtx(t, mintToken(t, issuer, []string{"claims:read"}, nil))
	scoped := bearerCtx(t, mintToken(t, issuer, []string{"claims:read"}, []string{"run-mine"}))

	ps, err := client.Passages(open, &mnemosv1.PassagesRequest{Query: "theirs"})
	if err != nil {
		t.Fatalf("Passages: %v", err)
	}
	if len(ps.GetPassages()) == 0 || ps.GetPassages()[0].GetEpisodeId() != "ev-theirs" {
		t.Errorf("Passages = %+v, want ev-theirs first", ps.GetPassages())
	}
	if _, err := client.Passages(open, &mnemosv1.PassagesRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("Passages without a query: %v, want InvalidArgument", err)
	}
	if _, err := client.Passages(scoped, &mnemosv1.PassagesRequest{Query: "theirs"}); status.Code(err) != codes.PermissionDenied {
		t.Errorf("run-scoped Passages without run_id: %v, want PermissionDenied", err)
	}
	if _, err := client.Passages(scoped, &mnemosv1.PassagesRequest{Query: "theirs", RunId: "run-theirs"}); status.Code(err) != codes.PermissionDenied {
		t.Errorf("run-scoped Passages on another run: %v, want PermissionDenied", err)
	}

	block, err := client.ContextBlock(scoped, &mnemosv1.ContextBlockRequest{RunId: "run-mine"})
	if err != nil {
		t.Fatalf("ContextBlock: %v", err)
	}
	if b := block.GetContext(); !strings.Contains(b, "cl-mine") || strings.Contains(b, "cl-theirs") {
		t.Errorf("context block for run-mine:\n%s", b)
	}
	if _, err := client.ContextBlock(scoped, &mnemosv1.ContextBlockRequest{RunId: "run-theirs"}); status.Code(err) != codes.PermissionDenied {
		t.Errorf("run-scoped ContextBlock on another run: %v, want PermissionDenied", err)
	}
	if _, err := client.ContextBlock(open, &mnemosv1.ContextBlockRequest{}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("ContextBlock without run_id: %v, want InvalidArgument", err)
	}
}

// Without the daemon surfaces attached, ProcessText and Search say so rather
// than pretending to have run.
func TestGRPC_DaemonSurfacesUnconfigured(t *testing.T) {
	client, _, _, issuer := startSharedBrainServer(t)
	ctx := bearerCtx(t, mintToken(t, issuer, []string{domain.ScopeWildcard}, nil))
	if _, err := client.ProcessText(ctx, &mnemosv1.ProcessTextRequest{Text: "t"}); status.Code(err) != codes.Unimplemented {
		t.Errorf("ProcessText: %v, want Unimplemented", err)
	}
	if _, err := client.Search(ctx, &mnemosv1.SearchRequest{Query: "q"}); status.Code(err) != codes.Unimplemented {
		t.Errorf("Search: %v, want Unimplemented", err)
	}
}
