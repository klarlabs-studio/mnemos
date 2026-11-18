package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/bolt"
	"go.klarlabs.de/mnemos"
	"go.klarlabs.de/mnemos/client"
	"go.klarlabs.de/mnemos/internal/auth"
	"go.klarlabs.de/mnemos/internal/domain"
	mnemosgrpc "go.klarlabs.de/mnemos/internal/server/grpc"
	"go.klarlabs.de/mnemos/internal/store"
	mnemosv1 "go.klarlabs.de/mnemos/proto/gen/mnemos/v1"

	grpclib "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// bindingFixture is one brain served on every daemon transport: REST over the
// production mux and gRPC over the production server with the daemon surfaces
// attached (withDaemonSurfaces), both with auth on, plus the library facade and
// the raw store they share. Everything points at one SQLite file pinned in
// MNEMOS_DB_URL, which is also where openWriter-based paths (process_text,
// record_decision) write.
type bindingFixture struct {
	conn   *store.Conn
	mem    mnemos.Memory
	rest   *httptest.Server
	grpc   mnemosv1.MnemosServiceClient
	issuer *auth.Issuer
}

func newBindingFixture(t *testing.T) bindingFixture {
	t.Helper()
	for _, env := range []string{"MNEMOS_EMBED_PROVIDER", "MNEMOS_LLM_PROVIDER", "MNEMOS_URL"} {
		t.Setenv(env, "")
	}
	secret, _ := setupJWTTestEnv(t)
	t.Chdir(t.TempDir()) // no repo brain to federate
	dsn := "sqlite://" + filepath.Join(t.TempDir(), "bindings.db")
	t.Setenv("MNEMOS_DB_URL", dsn)
	ctx := context.Background()
	conn, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	mem, err := newLibraryMemory(ctx, "")
	if err != nil {
		t.Fatalf("library facade: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })

	rest := httptest.NewServer(newServerMuxWithMemory(conn, mem, false, false, false))
	t.Cleanup(rest.Close)

	logger := bolt.New(bolt.NewJSONHandler(io.Discard))
	impl := withDaemonSurfaces(mnemosgrpc.NewServerWithMemory(conn, mem, auth.NewVerifier(secret, conn.RevokedTokens), logger, "test"))
	gs := grpclib.NewServer(grpclib.UnaryInterceptor(impl.UnaryInterceptor()))
	impl.Register(gs)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.GracefulStop)
	cc, err := grpclib.NewClient(lis.Addr().String(), grpclib.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = cc.Close() })
	return bindingFixture{conn: conn, mem: mem, rest: rest, grpc: mnemosv1.NewMnemosServiceClient(cc), issuer: auth.NewIssuer(secret)}
}

func (f bindingFixture) token(t *testing.T, scopes ...string) string {
	t.Helper()
	tok, _, err := f.issuer.IssueAgentTokenWithScopes("agt_bindings", scopes, time.Hour)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	return tok
}

func (f bindingFixture) grpcCtx(t *testing.T, scopes ...string) context.Context {
	return metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+f.token(t, scopes...))
}

func (f bindingFixture) client(t *testing.T, scopes ...string) *client.Client {
	return client.New(f.rest.URL, client.WithToken(f.token(t, scopes...)))
}

// seedBelief stores one evidenced belief directly in the store.
func (f bindingFixture) seedBelief(t *testing.T, id, text string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	if err := f.conn.Events.Append(ctx, domain.Event{
		ID: "ev-" + id, RunID: "run-bind", SchemaVersion: "v1", SourceInputID: "bind",
		Content: text, Timestamp: now, IngestedAt: now,
	}); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	if err := f.conn.Claims.Upsert(ctx, []domain.Claim{{
		ID: id, Text: text, Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive,
		Confidence: 0.8, TrustScore: 0.8, CreatedAt: now, ValidFrom: now.Add(-time.Hour),
	}}); err != nil {
		t.Fatalf("seed claim: %v", err)
	}
	if err := f.conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{{ClaimID: id, EventID: "ev-" + id}}); err != nil {
		t.Fatalf("seed evidence: %v", err)
	}
}

// gRPC ProcessText runs the REST /v1/process pipeline: what it reports is what
// the store holds afterwards, a read-only token is refused, and gRPC Search
// then finds the same beliefs REST /v1/search does.
func TestGRPC_ProcessTextAndSearch_MatchREST(t *testing.T) {
	f := newBindingFixture(t)
	writeCtx := f.grpcCtx(t, domain.ScopeClaimsWrite)
	off := false

	if _, err := f.grpc.ProcessText(f.grpcCtx(t, "claims:read"), &mnemosv1.ProcessTextRequest{Text: "x"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("claims:read ProcessText: %v, want PermissionDenied", err)
	}
	if _, err := f.grpc.ProcessText(writeCtx, &mnemosv1.ProcessTextRequest{Text: "  "}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty text: %v, want InvalidArgument", err)
	}

	out, err := f.grpc.ProcessText(writeCtx, &mnemosv1.ProcessTextRequest{
		Text:          "The checkout service stores orders in Postgres. The checkout service runs in the eu-west region.",
		UseLlm:        &off,
		UseEmbeddings: &off,
	})
	if err != nil {
		t.Fatalf("ProcessText: %v", err)
	}
	if out.GetRunId() == "" || out.GetBeliefs() == 0 || out.GetUsedLlm() {
		t.Fatalf("ProcessText = %+v, want a run with beliefs and no LLM", out)
	}
	events, err := f.conn.Events.ListByRunID(context.Background(), out.GetRunId())
	if err != nil || len(events) != int(out.GetEpisodes()) {
		t.Fatalf("store holds %d episodes for run %s (err %v), ProcessText reported %d", len(events), out.GetRunId(), err, out.GetEpisodes())
	}

	const q = "checkout service orders postgres"
	got, err := f.grpc.Search(f.grpcCtx(t, "claims:read"), &mnemosv1.SearchRequest{Query: q})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	var grpcIDs []string
	for _, b := range got.GetBeliefs() {
		grpcIDs = append(grpcIDs, b.GetId())
	}
	if len(grpcIDs) == 0 {
		t.Fatal("gRPC Search found nothing for text it just processed")
	}
	restBody, err := f.client(t, "claims:read").Search(context.Background(), q, client.SearchOptions{})
	if err != nil {
		t.Fatalf("REST search: %v", err)
	}
	var restIDs []string
	for _, c := range restBody.Claims {
		restIDs = append(restIDs, c.ID)
	}
	if !slices.Equal(grpcIDs, restIDs) {
		t.Errorf("gRPC Search %v, REST /v1/search %v: one implementation must give one answer", grpcIDs, restIDs)
	}

	if _, err := f.grpc.Search(f.grpcCtx(t, "claims:read"), &mnemosv1.SearchRequest{Query: q, Visibility: "everyone"}); status.Code(err) != codes.InvalidArgument {
		t.Errorf("bad visibility: %v, want InvalidArgument", err)
	}
}

// REST, the client and gRPC all report Memory.PredictiveError.
func TestPredictiveError_RESTClientAndGRPCAgreeWithLibrary(t *testing.T) {
	f := newBindingFixture(t)
	f.seedBelief(t, "cl-pe", "the cache hit rate is high")
	want, err := f.mem.PredictiveError(context.Background())
	if err != nil {
		t.Fatalf("library: %v", err)
	}
	got, err := f.client(t, "claims:read").PredictiveError(context.Background())
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	if got.Total != want.Total || got.Hotspot != want.Hotspot || len(got.Levels) != len(want.Levels) || got.LevelsMeasured != want.LevelsMeasured {
		t.Errorf("client %+v, library %+v", got, want)
	}
	pe, err := f.grpc.PredictiveError(f.grpcCtx(t, "claims:read"), &mnemosv1.PredictiveErrorRequest{})
	if err != nil {
		t.Fatalf("gRPC: %v", err)
	}
	if pe.GetTotal() != want.Total || len(pe.GetLevels()) != len(want.Levels) {
		t.Errorf("gRPC %+v, library %+v", pe, want)
	}
}

// The prediction loop closes over REST + client: set, observe, reconcile. The
// reconciliation is read back from the store, a claims:read token is refused,
// and the MCP tool closes a second prediction through the same function.
func TestReconcileExpectations_ClientAndMCP(t *testing.T) {
	f := newBindingFixture(t)
	f.seedBelief(t, "cl-lat", "p99 latency is 120ms")
	f.seedBelief(t, "cl-err", "error rate is 1 percent")
	ctx := context.Background()
	w := f.client(t, domain.ScopeClaimsWrite)

	if _, err := w.SetExpectation(ctx, "cl-lat", 120, 10, time.Time{}); err != nil {
		t.Fatalf("SetExpectation: %v", err)
	}
	if _, err := w.RecordObservation(ctx, "cl-lat", 125); err != nil {
		t.Fatalf("RecordObservation: %v", err)
	}

	var apiErr *client.APIError
	if _, err := f.client(t, "claims:read").ReconcileExpectations(ctx); !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
		t.Fatalf("claims:read reconcile: %v, want 403", err)
	}

	recs, err := w.ReconcileExpectations(ctx)
	if err != nil {
		t.Fatalf("ReconcileExpectations: %v", err)
	}
	if len(recs) != 1 || recs[0].ClaimID != "cl-lat" || !recs[0].Confirmed || recs[0].Observed != 125 {
		t.Fatalf("reconciliations = %+v, want cl-lat confirmed at 125", recs)
	}
	stored, ok, err := f.conn.Expectations.Get(ctx, "cl-lat")
	if err != nil || !ok || !stored.Resolved {
		t.Fatalf("stored expectation = %+v ok=%v err=%v, want resolved", stored, ok, err)
	}

	if _, err := w.SetExpectation(ctx, "cl-err", 1, 0.1, time.Time{}); err != nil {
		t.Fatalf("SetExpectation: %v", err)
	}
	if _, err := w.RecordObservation(ctx, "cl-err", 5); err != nil {
		t.Fatalf("RecordObservation: %v", err)
	}
	out, err := mcpReconcileExpectations(ctx)
	if err != nil {
		t.Fatalf("reconcile_expectations: %v", err)
	}
	if len(out.Reconciliations) != 1 || out.Reconciliations[0].BeliefID != "cl-err" || out.Reconciliations[0].Confirmed {
		t.Fatalf("MCP reconciliations = %+v, want cl-err refuted", out.Reconciliations)
	}
	if stored, ok, _ := f.conn.Expectations.Get(ctx, "cl-err"); !ok || !stored.Resolved {
		t.Errorf("cl-err not resolved in the store: %+v", stored)
	}
}

// POST /v1/decisions records through record_decision's implementation; the
// client gets back the decision the store holds.
func TestRecordDecision_ClientAgainstServe(t *testing.T) {
	f := newBindingFixture(t)
	f.seedBelief(t, "cl-db", "the orders database is near capacity")
	ctx := context.Background()

	var apiErr *client.APIError
	if _, err := f.client(t, "claims:read").RecordDecision(ctx, client.RecordDecisionRequest{Statement: "s", RiskLevel: "low"}); !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
		t.Fatalf("claims:read: %v, want 403", err)
	}
	w := f.client(t, domain.ScopeClaimsWrite)
	if _, err := w.RecordDecision(ctx, client.RecordDecisionRequest{Statement: "s", RiskLevel: "extreme"}); !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		t.Fatalf("bad risk level: %v, want 400", err)
	}
	if _, err := w.RecordDecision(ctx, client.RecordDecisionRequest{Statement: "s", RiskLevel: "low", ChosenAt: "yesterday-ish"}); !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadRequest {
		t.Fatalf("bad chosen_at: %v, want 400", err)
	}

	got, err := w.RecordDecision(ctx, client.RecordDecisionRequest{
		Statement: "scale the orders database", Plan: "add a replica", RiskLevel: "medium",
		Beliefs: []string{"cl-db"}, Alternatives: []string{"do nothing"}, ChosenAt: "2026-11-01",
	})
	if err != nil {
		t.Fatalf("RecordDecision: %v", err)
	}
	stored, err := f.conn.Decisions.GetByID(ctx, got.ID)
	if err != nil {
		t.Fatalf("decision %q not in the store: %v", got.ID, err)
	}
	if stored.Statement != got.Statement || string(stored.RiskLevel) != "medium" || !slices.Equal(stored.Beliefs, []string{"cl-db"}) ||
		stored.CreatedBy != "agt_bindings" || got.ChosenAt != "2026-11-01T00:00:00Z" {
		t.Errorf("client %+v, store %+v", got, stored)
	}
}

// The library's Metrics and ContextBlock agree with the REST routes they now
// share an implementation with.
func TestLibraryMetricsAndContextBlock_MatchREST(t *testing.T) {
	f := newBindingFixture(t)
	f.seedBelief(t, "cl-m1", "the queue depth is bounded")
	f.seedBelief(t, "cl-m2", "the worker pool has eight workers")
	ctx := context.Background()

	m, err := f.mem.Metrics(ctx)
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}
	rm, err := f.client(t, "claims:read").Metrics(ctx)
	if err != nil {
		t.Fatalf("REST metrics: %v", err)
	}
	if m.Claims != 2 || m.Events != 2 || m.Runs != 1 || rm.Claims != m.Claims || rm.Events != m.Events || rm.Runs != m.Runs {
		t.Errorf("library %+v, REST %+v", m, rm)
	}

	block, err := f.mem.ContextBlock(ctx, mnemos.ContextBlockQuery{RunID: "run-bind"})
	if err != nil {
		t.Fatalf("ContextBlock: %v", err)
	}
	restBlock, err := f.client(t, "claims:read").Context(ctx, "run-bind", client.ContextOptions{})
	if err != nil {
		t.Fatalf("REST context: %v", err)
	}
	if !containsAll(block, "cl-m1", "cl-m2") || stripGenerated(block) != stripGenerated(restBlock) {
		t.Errorf("library block:\n%s\nREST block:\n%s", block, restBlock)
	}
	gb, err := f.grpc.ContextBlock(f.grpcCtx(t, "claims:read"), &mnemosv1.ContextBlockRequest{RunId: "run-bind"})
	if err != nil {
		t.Fatalf("gRPC ContextBlock: %v", err)
	}
	if stripGenerated(gb.GetContext()) != stripGenerated(block) {
		t.Errorf("gRPC block differs:\n%s", gb.GetContext())
	}
	if _, err := f.mem.ContextBlock(ctx, mnemos.ContextBlockQuery{}); err == nil {
		t.Error("ContextBlock without a run: want an error")
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

// stripGenerated drops the block's "Generated <ts>." footer line, the one part
// that legitimately differs between two renders.
func stripGenerated(block string) string {
	lines := strings.Split(block, "\n")
	kept := lines[:0]
	for _, l := range lines {
		if !strings.HasPrefix(l, "Generated ") {
			kept = append(kept, l)
		}
	}
	return strings.Join(kept, "\n")
}

// seedBeliefInRun stores one evidenced belief in the given run.
func (f bindingFixture) seedBeliefInRun(t *testing.T, id, runID, text string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	if err := f.conn.Events.Append(ctx, domain.Event{
		ID: "ev-" + id, RunID: runID, SchemaVersion: "v1", SourceInputID: "bind",
		Content: text, Timestamp: now, IngestedAt: now,
	}); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	if err := f.conn.Claims.Upsert(ctx, []domain.Claim{{
		ID: id, Text: text, Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive,
		Confidence: 0.8, TrustScore: 0.8, CreatedAt: now, ValidFrom: now.Add(-time.Hour),
	}}); err != nil {
		t.Fatalf("seed claim: %v", err)
	}
	if err := f.conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{{ClaimID: id, EventID: "ev-" + id}}); err != nil {
		t.Fatalf("seed evidence: %v", err)
	}
}

// A token scoped to run A cannot set, observe or read an expectation on a
// run-B belief, on REST, gRPC or MCP (as an HTTP MCP request carries its
// claims), and cannot reconcile, which spans every run. Inside its own run it
// works. The run-B expectation is untouched in the store afterwards.
func TestExpectations_RunScopedTokenConfinedToItsRuns(t *testing.T) {
	f := newBindingFixture(t)
	f.seedBeliefInRun(t, "cl-run-a", "run-a", "run a latency is 50ms")
	f.seedBeliefInRun(t, "cl-run-b", "run-b", "run b latency is 80ms")
	ctx := context.Background()

	open := f.client(t, domain.ScopeClaimsWrite)
	if _, err := open.SetExpectation(ctx, "cl-run-b", 80, 5, time.Time{}); err != nil {
		t.Fatalf("unrestricted SetExpectation: %v", err)
	}

	tok, _, err := f.issuer.IssueAgentTokenWithScopesAndRuns("agt_run_a", []string{domain.ScopeClaimsWrite}, []string{"run-a"}, time.Hour)
	if err != nil {
		t.Fatalf("issue run-scoped token: %v", err)
	}

	// REST (through the Go client).
	scoped := client.New(f.rest.URL, client.WithToken(tok))
	var apiErr *client.APIError
	if got, err := scoped.Expectation(ctx, "cl-run-b"); err != nil || got != nil {
		t.Errorf("REST read of run-b expectation: %+v, %v; want 404 (nil)", got, err)
	}
	if _, err := scoped.SetExpectation(ctx, "cl-run-b", 1, 0, time.Time{}); !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Errorf("REST set on run-b: %v, want 404", err)
	}
	if _, err := scoped.RecordObservation(ctx, "cl-run-b", 1); !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Errorf("REST observe on run-b: %v, want 404", err)
	}
	if _, err := scoped.ReconcileExpectations(ctx); !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
		t.Errorf("REST reconcile with a run-scoped token: %v, want 403", err)
	}
	if _, err := scoped.SetExpectation(ctx, "cl-run-a", 50, 5, time.Time{}); err != nil {
		t.Errorf("REST set on own run: %v", err)
	}

	// gRPC.
	gctx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+tok)
	if _, err := f.grpc.GetExpectation(gctx, &mnemosv1.GetExpectationRequest{BeliefId: "cl-run-b"}); status.Code(err) != codes.NotFound {
		t.Errorf("gRPC read of run-b: %v, want NotFound", err)
	}
	if _, err := f.grpc.SetExpectation(gctx, &mnemosv1.SetExpectationRequest{BeliefId: "cl-run-b", Predicted: 1}); status.Code(err) != codes.NotFound {
		t.Errorf("gRPC set on run-b: %v, want NotFound", err)
	}
	if _, err := f.grpc.RecordObservation(gctx, &mnemosv1.RecordObservationRequest{BeliefId: "cl-run-b", Observed: 1}); status.Code(err) != codes.NotFound {
		t.Errorf("gRPC observe on run-b: %v, want NotFound", err)
	}
	if _, err := f.grpc.ReconcileExpectations(gctx, &mnemosv1.ReconcileExpectationsRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Errorf("gRPC reconcile with a run-scoped token: %v, want PermissionDenied", err)
	}
	if _, err := f.grpc.RecordObservation(gctx, &mnemosv1.RecordObservationRequest{BeliefId: "cl-run-a", Observed: 52}); err != nil {
		t.Errorf("gRPC observe on own run: %v", err)
	}

	// MCP over HTTP: the validated token's claims ride on the request context.
	mctx := withClaims(ctx, &auth.Claims{Runs: []string{"run-a"}, Scopes: []string{domain.ScopeClaimsWrite}})
	if _, err := mcpRunRecordExpectation(mctx, mcpRecordExpectationInput{ClaimID: "cl-run-b", Predicted: 1}); err == nil {
		t.Error("MCP record_expectation on run-b: want an error")
	}
	if _, err := mcpRunRecordObservation(mctx, mcpRecordObservationInput{ClaimID: "cl-run-b", Observed: 1}); err == nil {
		t.Error("MCP record_observation on run-b: want an error")
	}
	if _, err := mcpReconcileExpectations(mctx); err == nil {
		t.Error("MCP reconcile_expectations with a run-scoped token: want an error")
	}
	if _, err := mcpRunRecordObservation(mctx, mcpRecordObservationInput{ClaimID: "cl-run-a", Observed: 51}); err != nil {
		t.Errorf("MCP record_observation on own run: %v", err)
	}

	stored, ok, err := f.conn.Expectations.Get(ctx, "cl-run-b")
	if err != nil || !ok || stored.Predicted != 80 || stored.HasObservation {
		t.Errorf("run-b expectation = %+v ok=%v err=%v; want untouched (80, unobserved)", stored, ok, err)
	}
	own, ok, err := f.conn.Expectations.Get(ctx, "cl-run-a")
	if err != nil || !ok || own.Observed != 51 {
		t.Errorf("run-a expectation = %+v ok=%v err=%v; want observed 51", own, ok, err)
	}
}
