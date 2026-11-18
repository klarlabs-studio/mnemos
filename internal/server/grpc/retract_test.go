package grpc_test

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/auth"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
	mnemosgrpc "go.klarlabs.de/mnemos/internal/server/grpc"
	"go.klarlabs.de/mnemos/internal/store"
	_ "go.klarlabs.de/mnemos/internal/store/sqlite"
	mnemosv1 "go.klarlabs.de/mnemos/proto/gen/mnemos/v1"

	grpclib "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// startSQLiteAuthServer is startAuthServer over a real SQLite store in a temp
// dir, with no Memory facade: retraction runs on the governed writer alone.
func startSQLiteAuthServer(t *testing.T) (mnemosv1.MnemosServiceClient, *store.Conn, *auth.Issuer) {
	t.Helper()
	conn, err := store.Open(context.Background(), "sqlite://"+filepath.Join(t.TempDir(), "mnemos.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	secret, err := auth.GenerateSecret()
	if err != nil {
		t.Fatalf("secret: %v", err)
	}
	impl := mnemosgrpc.NewServerWithMemory(conn, nil, auth.NewVerifier(secret, conn.RevokedTokens), testLogger(), "test")
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
	return mnemosv1.NewMnemosServiceClient(cc), conn, auth.NewIssuer(secret)
}

func seedRetractable(t *testing.T, conn *store.Conn) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	for _, ev := range []domain.Event{
		{ID: "ev-mine", Content: "mine", SchemaVersion: "1.0", SourceInputID: "in", RunID: "run-mine", Timestamp: now, IngestedAt: now},
		{ID: "ev-other", Content: "other", SchemaVersion: "1.0", SourceInputID: "in", RunID: "run-mine", Timestamp: now, IngestedAt: now},
		{ID: "ev-theirs", Content: "theirs", SchemaVersion: "1.0", SourceInputID: "in", RunID: "run-theirs", Timestamp: now, IngestedAt: now},
	} {
		if err := conn.Events.Append(ctx, ev); err != nil {
			t.Fatalf("seed event %s: %v", ev.ID, err)
		}
	}
	if err := conn.Claims.Upsert(ctx, []domain.Claim{
		{ID: "cl-only", Text: "only", Type: domain.ClaimTypeFact, Confidence: 0.9, Status: domain.ClaimStatusActive, CreatedAt: now},
		{ID: "cl-shared", Text: "shared", Type: domain.ClaimTypeFact, Confidence: 0.9, Status: domain.ClaimStatusActive, CreatedAt: now},
		{ID: "cl-theirs", Text: "theirs", Type: domain.ClaimTypeFact, Confidence: 0.9, Status: domain.ClaimStatusActive, CreatedAt: now},
	}); err != nil {
		t.Fatalf("seed claims: %v", err)
	}
	if err := conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{
		{ClaimID: "cl-only", EventID: "ev-mine"},
		{ClaimID: "cl-shared", EventID: "ev-mine"},
		{ClaimID: "cl-shared", EventID: "ev-other"},
		{ClaimID: "cl-theirs", EventID: "ev-theirs"},
	}); err != nil {
		t.Fatalf("seed evidence: %v", err)
	}
}

// RetractEpisode reports the same impact as the governed writer's retraction
// (which the CLI, MCP and REST call), attributes it to the token subject, and
// is idempotent.
func TestGRPC_RetractEpisode(t *testing.T) {
	client, conn, issuer := startSQLiteAuthServer(t)
	seedRetractable(t, conn)
	ctx := bearerCtx(t, mintToken(t, issuer, []string{domain.ScopeClaimsWrite}, nil))

	got, err := client.RetractEpisode(ctx, &mnemosv1.RetractEpisodeRequest{EpisodeId: "ev-mine", Reason: "bad source"})
	if err != nil {
		t.Fatalf("RetractEpisode: %v", err)
	}
	if got.GetEpisodeId() != "ev-mine" || got.GetAlreadyRetracted() || got.GetReason() != "bad source" || got.GetRetractedBy() != "agent-1" {
		t.Errorf("response = %+v, want a first retraction of ev-mine by agent-1", got)
	}
	im := got.GetImpact()
	if b := im.GetRetractedBeliefs(); len(b) != 1 || b[0] != "cl-only" {
		t.Errorf("retracted beliefs = %v, want [cl-only]", b)
	}
	if r := im.GetRescoredBeliefs(); len(r) != 1 || r[0].GetId() != "cl-shared" || r[0].GetRemainingEvidence() != 1 {
		t.Errorf("rescored = %v, want cl-shared kept on one episode", r)
	}
	if len(im.GetUncertainty()) == 0 {
		t.Error("no uncertainty statements")
	}

	// The record the writer stored is the one the RPC rendered.
	stored, ok, err := govwrite.LookupRetraction(context.Background(), conn, "ev-mine")
	if err != nil || !ok {
		t.Fatalf("lookup: ok=%v err=%v", ok, err)
	}
	if !stored.RetractedAt.Equal(got.GetRetractedAt().AsTime()) || len(stored.Impact.RetractedBeliefs) != 1 {
		t.Errorf("stored %+v differs from response %+v", stored, got)
	}

	again, err := client.RetractEpisode(ctx, &mnemosv1.RetractEpisodeRequest{EpisodeId: "ev-mine"})
	if err != nil {
		t.Fatalf("repeat: %v", err)
	}
	if !again.GetAlreadyRetracted() || again.GetReason() != "bad source" {
		t.Errorf("repeat = %+v, want the stored record marked already retracted", again)
	}
}

func TestGRPC_RetractEpisode_Refusals(t *testing.T) {
	client, conn, issuer := startSQLiteAuthServer(t)
	seedRetractable(t, conn)
	write := []string{domain.ScopeClaimsWrite}

	cases := []struct {
		name  string
		token string
		id    string
		want  codes.Code
	}{
		{"claims:read token", mintToken(t, issuer, []string{"claims:read"}, nil), "ev-mine", codes.PermissionDenied},
		{"unknown episode", mintToken(t, issuer, write, nil), "ev-missing", codes.NotFound},
		{"episode in another run", mintToken(t, issuer, write, []string{"run-mine"}), "ev-theirs", codes.NotFound},
		{"no episode id", mintToken(t, issuer, write, nil), " ", codes.InvalidArgument},
	}
	for _, tc := range cases {
		_, err := client.RetractEpisode(bearerCtx(t, tc.token), &mnemosv1.RetractEpisodeRequest{EpisodeId: tc.id})
		if got := status.Code(err); got != tc.want {
			t.Errorf("%s: code = %v (%v), want %v", tc.name, got, err, tc.want)
		}
	}
	for _, id := range []string{"ev-mine", "ev-theirs"} {
		if _, ok, err := govwrite.LookupRetraction(context.Background(), conn, id); err != nil || ok {
			t.Errorf("%s retracted by a refused call (ok=%v err=%v)", id, ok, err)
		}
	}
}
