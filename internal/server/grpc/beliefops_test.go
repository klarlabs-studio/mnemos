package grpc_test

import (
	"context"
	"net"
	"path/filepath"
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

// startBeliefOpsServer serves gRPC over ONE SQLite brain shared by the direct
// conn and the Memory facade, so an RPC's effect can be read back from the
// store it wrote.
func startBeliefOpsServer(t *testing.T) (mnemosv1.MnemosServiceClient, *store.Conn, *auth.Issuer) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "brain.db")
	conn, err := store.Open(context.Background(), "sqlite://"+path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	mem, err := mnemos.New(mnemos.WithSQLite(path), mnemos.WithPassiveMode())
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
	return mnemosv1.NewMnemosServiceClient(cc), conn, auth.NewIssuer(secret)
}

func seedBeliefs(t *testing.T, conn *store.Conn, ids ...string) {
	t.Helper()
	now := time.Now().UTC()
	for _, id := range ids {
		if err := conn.Claims.Upsert(context.Background(), []domain.Claim{{
			ID: id, Text: "belief " + id, Type: domain.ClaimTypeFact, Confidence: 0.8,
			Status: domain.ClaimStatusActive, CreatedAt: now, ValidFrom: now,
		}}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
}

func readBelief(t *testing.T, conn *store.Conn, id string) domain.Claim {
	t.Helper()
	got, err := conn.Claims.ListByIDs(context.Background(), []string{id})
	if err != nil || len(got) != 1 {
		t.Fatalf("read %s: %v (%d rows)", id, err, len(got))
	}
	return got[0]
}

func TestGRPC_BeliefGovernance(t *testing.T) {
	client, conn, issuer := startBeliefOpsServer(t)
	seedBeliefs(t, conn, "cl-a", "cl-b", "cl-c")
	ctx := bearerCtx(t, mintToken(t, issuer, []string{domain.ScopeClaimsWrite}, nil))

	if _, err := client.DeprecateBelief(ctx, &mnemosv1.DeprecateBeliefRequest{BeliefId: "cl-missing"}); status.Code(err) != codes.NotFound {
		t.Errorf("deprecate unknown: %v, want NotFound", err)
	}
	ch, err := client.DeprecateBelief(ctx, &mnemosv1.DeprecateBeliefRequest{BeliefId: "cl-c", Reason: "stale"})
	if err != nil || ch.GetNewStatus() != "deprecated" {
		t.Fatalf("DeprecateBelief = %v, %v", ch, err)
	}
	if s := readBelief(t, conn, "cl-c").Status; s != domain.ClaimStatusDeprecated {
		t.Errorf("stored status = %s", s)
	}
	if _, err := client.RestoreBelief(ctx, &mnemosv1.RestoreBeliefRequest{BeliefId: "cl-missing"}); status.Code(err) != codes.NotFound {
		t.Errorf("restore unknown: %v, want NotFound", err)
	}
	rs, err := client.RestoreBelief(ctx, &mnemosv1.RestoreBeliefRequest{BeliefId: "cl-c", Reason: "still true"})
	if err != nil || rs.GetNewStatus() != "active" || rs.GetOldStatus() != "deprecated" {
		t.Fatalf("RestoreBelief = %v, %v", rs, err)
	}
	if s := readBelief(t, conn, "cl-c").Status; s != domain.ClaimStatusActive {
		t.Errorf("stored status after restore = %s", s)
	}

	if _, err := client.UpdateBelief(ctx, &mnemosv1.UpdateBeliefRequest{BeliefId: "cl-a"}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("update without text: %v, want InvalidArgument", err)
	}
	if _, err := client.UpdateBelief(ctx, &mnemosv1.UpdateBeliefRequest{BeliefId: "cl-a", Text: "revised", Confidence: 0.6}); err != nil {
		t.Fatalf("UpdateBelief: %v", err)
	}
	if c := readBelief(t, conn, "cl-a"); c.Text != "revised" || c.Confidence != 0.6 {
		t.Errorf("stored after update = %q %.2f", c.Text, c.Confidence)
	}

	if _, err := client.ResolveDissonance(ctx, &mnemosv1.ResolveDissonanceRequest{WinnerId: "cl-a", LoserId: "cl-b"}); err != nil {
		t.Fatalf("ResolveDissonance: %v", err)
	}
	if a, b := readBelief(t, conn, "cl-a").Status, readBelief(t, conn, "cl-b").Status; a != domain.ClaimStatusResolved || b != domain.ClaimStatusDeprecated {
		t.Errorf("stored after resolve: %s / %s", a, b)
	}

	esc, err := client.EscalateBelief(ctx, &mnemosv1.EscalateBeliefRequest{BeliefId: "cl-a", Reason: "human please"})
	if err != nil || esc.GetEscalationReason() != "human please" {
		t.Errorf("EscalateBelief = %v, %v", esc, err)
	}

	curator := bearerCtx(t, mintToken(t, issuer, []string{domain.ScopePromoteGlobal}, nil))
	if _, err := client.VerifyBelief(curator, &mnemosv1.VerifyBeliefRequest{BeliefId: "cl-a"}); err != nil {
		t.Fatalf("VerifyBelief: %v", err)
	}
	if c := readBelief(t, conn, "cl-a"); c.VerifyCount != 1 {
		t.Errorf("stored verify_count = %d, want 1", c.VerifyCount)
	}

	fb, err := client.RecordBeliefFeedback(ctx, &mnemosv1.RecordBeliefFeedbackRequest{BeliefId: "cl-a", Helpful: false, Note: "off"})
	if err != nil || fb.GetNegativeFeedbackStreak() != 1 {
		t.Fatalf("RecordBeliefFeedback = %v, %v", fb, err)
	}
	state, ok, err := conn.Feedback.Get(context.Background(), "cl-a")
	if err != nil || !ok || state.NegativeFeedbackStreak != 1 || state.LastFeedbackNote != "off" {
		t.Errorf("stored feedback = %+v ok=%v err=%v", state, ok, err)
	}
	if c := readBelief(t, conn, "cl-a"); c.Confidence >= 0.6 {
		t.Errorf("an unhelpful vote left confidence at %.3f", c.Confidence)
	}

	hist, err := client.BeliefHistory(ctx, &mnemosv1.BeliefHistoryRequest{BeliefId: "cl-a"})
	if err != nil || len(hist.GetVersions()) == 0 {
		t.Fatalf("BeliefHistory = %v, %v", hist, err)
	}
	prov, err := client.BeliefProvenance(ctx, &mnemosv1.BeliefProvenanceRequest{BeliefId: "cl-a"})
	if err != nil || prov.GetBeliefId() != "cl-a" || len(prov.GetSignals()) == 0 {
		t.Errorf("BeliefProvenance = %v, %v", prov, err)
	}
	if _, err := client.BeliefProvenance(ctx, &mnemosv1.BeliefProvenanceRequest{BeliefId: "cl-missing"}); status.Code(err) != codes.NotFound {
		t.Errorf("provenance unknown: %v, want NotFound", err)
	}
}

func TestGRPC_RememberFactAndScan(t *testing.T) {
	client, conn, issuer := startBeliefOpsServer(t)
	ctx := bearerCtx(t, mintToken(t, issuer, []string{domain.ScopeClaimsWrite}, nil))

	if _, err := client.RememberFact(ctx, &mnemosv1.RememberFactRequest{Text: "x"}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("fact without evidence: %v, want InvalidArgument", err)
	}
	runScoped := bearerCtx(t, mintToken(t, issuer, []string{domain.ScopeClaimsWrite}, []string{"run-a"}))
	if _, err := client.RememberFact(runScoped, &mnemosv1.RememberFactRequest{Text: "x", Evidence: []string{"y"}}); status.Code(err) != codes.PermissionDenied {
		t.Errorf("fact with run-scoped token: %v, want PermissionDenied", err)
	}
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	res, err := client.RememberFact(ctx, &mnemosv1.RememberFactRequest{
		Text: "The billing service runs in eu-west-1.", Evidence: []string{"runbook p.4"}, ValidFrom: timestamppb.New(from),
	})
	if err != nil || res.GetBeliefId() == "" {
		t.Fatalf("RememberFact = %v, %v", res, err)
	}
	if c := readBelief(t, conn, res.GetBeliefId()); c.Text != "The billing service runs in eu-west-1." || !c.ValidFrom.Equal(from) {
		t.Errorf("stored fact = %q %v", c.Text, c.ValidFrom)
	}

	in, err := client.ScanBeliefs(ctx, &mnemosv1.ScanBeliefsRequest{
		ValidFrom: timestamppb.New(from), ValidUntil: timestamppb.New(from.AddDate(0, 1, 0)),
	})
	if err != nil || len(in.GetBeliefs()) != 1 || in.GetBeliefs()[0].GetId() != res.GetBeliefId() {
		t.Errorf("ScanBeliefs in window = %v, %v", in, err)
	}
	before, err := client.ScanBeliefs(ctx, &mnemosv1.ScanBeliefsRequest{ValidUntil: timestamppb.New(from.AddDate(0, 0, -1))})
	if err != nil || len(before.GetBeliefs()) != 0 {
		t.Errorf("ScanBeliefs before = %v, %v", before, err)
	}
	if _, err := client.ScanBeliefs(runScoped, &mnemosv1.ScanBeliefsRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Errorf("scan with run-scoped token: %v, want PermissionDenied", err)
	}
}

// A restated fact folds onto the belief the brain already holds: RememberFact
// returns the held belief's id with known set, the store gains the evidence
// and no claim row.
func TestGRPC_RememberFact_RestatementReturnsHeldBelief(t *testing.T) {
	client, conn, issuer := startBeliefOpsServer(t)
	ctx := bearerCtx(t, mintToken(t, issuer, []string{domain.ScopeClaimsWrite}, nil))

	first, err := client.RememberFact(ctx, &mnemosv1.RememberFactRequest{Text: "The billing service runs in eu-west-1.", Evidence: []string{"runbook p.4"}})
	if err != nil || first.GetKnown() {
		t.Fatalf("first RememberFact = %v, %v; want a new belief", first, err)
	}
	again, err := client.RememberFact(ctx, &mnemosv1.RememberFactRequest{Text: "the billing service runs in eu-west-1", Evidence: []string{"incident 812"}})
	if err != nil {
		t.Fatalf("restatement: %v", err)
	}
	if again.GetBeliefId() != first.GetBeliefId() || !again.GetKnown() {
		t.Fatalf("restatement = %v, want belief %s known", again, first.GetBeliefId())
	}
	all, err := conn.Claims.ListAll(context.Background())
	if err != nil || len(all) != 1 {
		t.Fatalf("store holds %d claims (%v), want 1", len(all), err)
	}
	links, err := conn.Claims.ListEvidenceByClaimIDs(context.Background(), []string{first.GetBeliefId()})
	if err != nil || len(links) != 2 {
		t.Fatalf("held belief has %d evidence links (%v), want 2", len(links), err)
	}
	fresh, err := client.RememberFact(ctx, &mnemosv1.RememberFactRequest{Text: "Payments owns the billing service.", Evidence: []string{"org chart"}})
	if err != nil || fresh.GetKnown() || fresh.GetBeliefId() == first.GetBeliefId() {
		t.Fatalf("new fact = %v, %v; want a new belief", fresh, err)
	}
}
