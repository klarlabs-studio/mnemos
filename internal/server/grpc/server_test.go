package grpc_test

import (
	"context"
	"net"
	"os"
	"testing"
	"time"

	"go.klarlabs.de/bolt"
	mnemosgrpc "go.klarlabs.de/mnemos/internal/server/grpc"
	"go.klarlabs.de/mnemos/internal/store"
	_ "go.klarlabs.de/mnemos/internal/store/memory"
	mnemosv1 "go.klarlabs.de/mnemos/proto/gen/mnemos/v1"

	grpclib "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func testLogger() *bolt.Logger {
	return bolt.New(bolt.NewJSONHandler(os.Stderr))
}

func startTestServer(t *testing.T) (mnemosv1.MnemosServiceClient, func()) {
	client, _, cleanup := startTestServerWithConn(t)
	return client, cleanup
}

// startTestServerWithConn is the same wiring as startTestServer but
// also returns the underlying *store.Conn so tests that need to bypass
// gRPC (e.g. to call SetValidity, which is an internal lifecycle path
// not exposed on the wire) can do so without standing up a second
// store.
func startTestServerWithConn(t *testing.T) (mnemosv1.MnemosServiceClient, *store.Conn, func()) {
	t.Helper()
	conn, err := store.Open(context.Background(), "memory://")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	logger := testLogger()
	mnemosSrv := mnemosgrpc.NewServer(conn, nil, logger, "test")
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
	client := mnemosv1.NewMnemosServiceClient(cc)

	cleanup := func() {
		_ = cc.Close()
		srv.GracefulStop()
		_ = conn.Close()
	}
	return client, conn, cleanup
}

func TestHealth(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	resp, err := client.Health(context.Background(), &mnemosv1.HealthRequest{})
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if resp.Status != "ok" {
		t.Errorf("Status = %q, want ok", resp.Status)
	}
	if resp.Version != "test" {
		t.Errorf("Version = %q, want test", resp.Version)
	}
}

func TestEpisodesRoundTrip(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	ctx := context.Background()
	now := timestamppb.New(time.Now().UTC())

	// Append
	_, err := client.AppendEpisodes(ctx, &mnemosv1.AppendEpisodesRequest{
		Episodes: []*mnemosv1.Episode{
			{Id: "ev-1", RunId: "r1", SchemaVersion: "v1", Content: "hello", SourceInputId: "in1", Timestamp: now, IngestedAt: now},
		},
	})
	if err != nil {
		t.Fatalf("AppendEpisodes: %v", err)
	}

	// List
	list, err := client.ListEpisodes(ctx, &mnemosv1.ListEpisodesRequest{})
	if err != nil {
		t.Fatalf("ListEpisodes: %v", err)
	}
	if len(list.Episodes) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(list.Episodes))
	}
	if list.Episodes[0].Id != "ev-1" {
		t.Errorf("event id = %q, want ev-1", list.Episodes[0].Id)
	}
}

func TestBeliefsRoundTrip(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	ctx := context.Background()
	now := timestamppb.New(time.Now().UTC())

	// Append claims
	_, err := client.AppendBeliefs(ctx, &mnemosv1.AppendBeliefsRequest{
		Beliefs: []*mnemosv1.Belief{
			{Id: "cl-1", Text: "sky is blue", Type: "fact", Confidence: 0.9, Status: "active", CreatedAt: now},
		},
	})
	if err != nil {
		t.Fatalf("AppendBeliefs: %v", err)
	}

	// List
	list, err := client.ListBeliefs(ctx, &mnemosv1.ListBeliefsRequest{})
	if err != nil {
		t.Fatalf("ListBeliefs: %v", err)
	}
	if len(list.Beliefs) != 1 {
		t.Fatalf("len(claims) = %d, want 1", len(list.Beliefs))
	}
	if list.Beliefs[0].Text != "sky is blue" {
		t.Errorf("claim text = %q, want 'sky is blue'", list.Beliefs[0].Text)
	}
}

// TestListBeliefs_RunIDFilter pins the gRPC tenant boundary: run_id
// returns only claims whose evidence links to an event tagged with
// the matching RunID. Mirrors HTTP behaviour for parity.
func TestListBeliefs_RunIDFilter(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	ctx := context.Background()
	now := timestamppb.New(time.Now().UTC())

	// Two tenants — A and B. Each gets one event + one claim evidence
	// link to that event.
	if _, err := client.AppendEpisodes(ctx, &mnemosv1.AppendEpisodesRequest{
		Episodes: []*mnemosv1.Episode{
			{Id: "ev-a", RunId: "tenant:A", SchemaVersion: "v1", Content: "a", SourceInputId: "in1", Timestamp: now, IngestedAt: now},
			{Id: "ev-b", RunId: "tenant:B", SchemaVersion: "v1", Content: "b", SourceInputId: "in2", Timestamp: now, IngestedAt: now},
		},
	}); err != nil {
		t.Fatalf("AppendEpisodes: %v", err)
	}
	if _, err := client.AppendBeliefs(ctx, &mnemosv1.AppendBeliefsRequest{
		Beliefs: []*mnemosv1.Belief{
			{Id: "cl-a", Text: "claim A", Type: "fact", Confidence: 0.9, Status: "active", CreatedAt: now},
			{Id: "cl-b", Text: "claim B", Type: "fact", Confidence: 0.9, Status: "active", CreatedAt: now},
		},
		Evidence: []*mnemosv1.BeliefEvidence{
			{BeliefId: "cl-a", EpisodeId: "ev-a"},
			{BeliefId: "cl-b", EpisodeId: "ev-b"},
		},
	}); err != nil {
		t.Fatalf("AppendBeliefs: %v", err)
	}

	// Tenant A's filter must return ONLY cl-a.
	list, err := client.ListBeliefs(ctx, &mnemosv1.ListBeliefsRequest{RunId: "tenant:A"})
	if err != nil {
		t.Fatalf("ListBeliefs: %v", err)
	}
	if len(list.Beliefs) != 1 || list.Beliefs[0].Id != "cl-a" {
		t.Fatalf("run_id=tenant:A leaked: got %v", list.Beliefs)
	}
}

// TestListBeliefs_RunIDFilter_UnknownRunFailsClosed returns empty when
// no events exist under the requested run, even if other unrelated
// claims would otherwise match the type/status filters.
func TestListBeliefs_RunIDFilter_UnknownRunFailsClosed(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	ctx := context.Background()
	now := timestamppb.New(time.Now().UTC())
	if _, err := client.AppendBeliefs(ctx, &mnemosv1.AppendBeliefsRequest{
		Beliefs: []*mnemosv1.Belief{
			{Id: "cl-orphan", Text: "orphan", Type: "fact", Confidence: 0.9, Status: "active", CreatedAt: now},
		},
	}); err != nil {
		t.Fatalf("AppendBeliefs: %v", err)
	}

	list, err := client.ListBeliefs(ctx, &mnemosv1.ListBeliefsRequest{RunId: "tenant:nobody"})
	if err != nil {
		t.Fatalf("ListBeliefs: %v", err)
	}
	if len(list.Beliefs) != 0 {
		t.Fatalf("unknown run_id leaked %d claims", len(list.Beliefs))
	}
}

// TestListBeliefs_AsOfFilter pins gRPC parity with the HTTP ?as_of=
// time-travel query: a claim with a closed [valid_from, valid_to)
// window must surface only when as_of falls inside that window.
// Without this, downstream agents that talk gRPC can't ask "what was
// true on date X".
func TestListBeliefs_AsOfFilter(t *testing.T) {
	client, conn, cleanup := startTestServerWithConn(t)
	defer cleanup()

	ctx := context.Background()
	// Anchor the test instead of "now" so SetValidity's bookkeeping is
	// deterministic regardless of wall-clock drift.
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	earlyCreated := timestamppb.New(t0)
	if _, err := client.AppendBeliefs(ctx, &mnemosv1.AppendBeliefsRequest{
		Beliefs: []*mnemosv1.Belief{
			{Id: "cl-old", Text: "old fact", Type: "fact", Confidence: 0.9, Status: "active", CreatedAt: earlyCreated},
			{Id: "cl-current", Text: "current fact", Type: "fact", Confidence: 0.9, Status: "active", CreatedAt: earlyCreated},
		},
	}); err != nil {
		t.Fatalf("AppendBeliefs: %v", err)
	}

	// cl-old's window closes at 2026-03-01. cl-current stays open.
	// Use the store directly because the gRPC surface doesn't expose
	// SetValidity (it's an internal lifecycle path).
	if err := conn.Claims.SetValidity(ctx, "cl-old", t0.AddDate(0, 2, 0)); err != nil {
		t.Fatalf("SetValidity: %v", err)
	}

	// Query as of 2026-02-01 — within cl-old's window, so both surface.
	asOfFeb := timestamppb.New(t0.AddDate(0, 1, 0))
	list, err := client.ListBeliefs(ctx, &mnemosv1.ListBeliefsRequest{AsOf: asOfFeb})
	if err != nil {
		t.Fatalf("ListBeliefs as_of Feb: %v", err)
	}
	if len(list.Beliefs) != 2 {
		t.Errorf("as_of=Feb got %d claims, want 2", len(list.Beliefs))
	}

	// Query as of 2026-04-01 — after cl-old's valid_to. Only cl-current
	// survives, cl-old must drop.
	asOfApr := timestamppb.New(t0.AddDate(0, 3, 0))
	list, err = client.ListBeliefs(ctx, &mnemosv1.ListBeliefsRequest{AsOf: asOfApr})
	if err != nil {
		t.Fatalf("ListBeliefs as_of Apr: %v", err)
	}
	if len(list.Beliefs) != 1 || list.Beliefs[0].Id != "cl-current" {
		t.Errorf("as_of=Apr leaked superseded claim: %v", list.Beliefs)
	}
}

// TestListBeliefs_RecordedAsOfFilter pins the ingestion-time axis: a
// claim recorded after the query timestamp must drop, so callers can
// reproduce a snapshot of the store as it stood at a past moment.
func TestListBeliefs_RecordedAsOfFilter(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	ctx := context.Background()
	early := timestamppb.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	late := timestamppb.New(time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC))
	if _, err := client.AppendBeliefs(ctx, &mnemosv1.AppendBeliefsRequest{
		Beliefs: []*mnemosv1.Belief{
			{Id: "cl-early", Text: "early", Type: "fact", Confidence: 0.9, Status: "active", CreatedAt: early},
			{Id: "cl-late", Text: "late", Type: "fact", Confidence: 0.9, Status: "active", CreatedAt: late},
		},
	}); err != nil {
		t.Fatalf("AppendBeliefs: %v", err)
	}

	// recorded_as_of = March 2026 — only cl-early should survive.
	cutoff := timestamppb.New(time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	list, err := client.ListBeliefs(ctx, &mnemosv1.ListBeliefsRequest{RecordedAsOf: cutoff})
	if err != nil {
		t.Fatalf("ListBeliefs: %v", err)
	}
	if len(list.Beliefs) != 1 || list.Beliefs[0].Id != "cl-early" {
		t.Errorf("recorded_as_of=March leaked late row: %v", list.Beliefs)
	}
}

func TestAssociationsRoundTrip(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	ctx := context.Background()
	now := timestamppb.New(time.Now().UTC())

	// Need claims first since relationships reference them
	_, err := client.AppendBeliefs(ctx, &mnemosv1.AppendBeliefsRequest{
		Beliefs: []*mnemosv1.Belief{
			{Id: "cl-a", Text: "a", Type: "fact", Confidence: 0.5, Status: "active", CreatedAt: now},
			{Id: "cl-b", Text: "b", Type: "fact", Confidence: 0.5, Status: "active", CreatedAt: now},
		},
	})
	if err != nil {
		t.Fatalf("AppendBeliefs: %v", err)
	}

	_, err = client.AppendAssociations(ctx, &mnemosv1.AppendAssociationsRequest{
		Associations: []*mnemosv1.Association{
			{Id: "rel-1", Type: "supports", FromBeliefId: "cl-a", ToBeliefId: "cl-b", CreatedAt: now},
		},
	})
	if err != nil {
		t.Fatalf("AppendAssociations: %v", err)
	}

	list, err := client.ListAssociations(ctx, &mnemosv1.ListAssociationsRequest{})
	if err != nil {
		t.Fatalf("ListAssociations: %v", err)
	}
	if len(list.Associations) != 1 {
		t.Fatalf("len(relationships) = %d, want 1", len(list.Associations))
	}
}

func TestEmbeddingsRoundTrip(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	ctx := context.Background()

	_, err := client.AppendEmbeddings(ctx, &mnemosv1.AppendEmbeddingsRequest{
		Embeddings: []*mnemosv1.Embedding{
			{EntityId: "ev-1", EntityType: "event", Vector: []float32{0.1, 0.2, 0.3}, Model: "test", Dimensions: 3},
		},
	})
	if err != nil {
		t.Fatalf("AppendEmbeddings: %v", err)
	}

	list, err := client.ListEmbeddings(ctx, &mnemosv1.ListEmbeddingsRequest{})
	if err != nil {
		t.Fatalf("ListEmbeddings: %v", err)
	}
	if len(list.Embeddings) != 1 {
		t.Fatalf("len(embeddings) = %d, want 1", len(list.Embeddings))
	}
	if list.Embeddings[0].EntityId != "ev-1" {
		t.Errorf("entity id = %q, want ev-1", list.Embeddings[0].EntityId)
	}
}

func TestMetrics(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	ctx := context.Background()
	now := timestamppb.New(time.Now().UTC())

	if _, err := client.AppendEpisodes(ctx, &mnemosv1.AppendEpisodesRequest{
		Episodes: []*mnemosv1.Episode{{Id: "ev-1", RunId: "r1", SchemaVersion: "v1", Content: "x", SourceInputId: "in1", Timestamp: now, IngestedAt: now}},
	}); err != nil {
		t.Fatalf("AppendEpisodes: %v", err)
	}
	if _, err := client.AppendBeliefs(ctx, &mnemosv1.AppendBeliefsRequest{
		Beliefs: []*mnemosv1.Belief{{Id: "cl-1", Text: "x", Type: "fact", Confidence: 0.5, Status: "active", CreatedAt: now}},
	}); err != nil {
		t.Fatalf("AppendBeliefs: %v", err)
	}

	m, err := client.Metrics(ctx, &mnemosv1.MetricsRequest{})
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}
	if m.Episodes != 1 {
		t.Errorf("Episodes = %d, want 1", m.Episodes)
	}
	if m.Beliefs != 1 {
		t.Errorf("Beliefs = %d, want 1", m.Beliefs)
	}
}

func TestAppendEpisodesValidation(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	_, err := client.AppendEpisodes(context.Background(), &mnemosv1.AppendEpisodesRequest{})
	if err == nil {
		t.Fatal("expected error for empty events")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", st.Code())
	}
}

func TestAppendEpisodesEmptyID(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()

	_, err := client.AppendEpisodes(context.Background(), &mnemosv1.AppendEpisodesRequest{
		Episodes: []*mnemosv1.Episode{{Id: ""}},
	})
	if err == nil {
		t.Fatal("expected error for empty event id")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", st.Code())
	}
}

func TestActionsRoundTrip(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()
	ctx := context.Background()
	at := timestamppb.New(time.Now().UTC())

	if _, err := client.AppendActions(ctx, &mnemosv1.AppendActionsRequest{
		Actions: []*mnemosv1.Action{
			{Id: "ac_1", Kind: "rollback", Subject: "payments", At: at},
			{Id: "ac_2", Kind: "deploy", Subject: "search", At: at},
		},
	}); err != nil {
		t.Fatalf("AppendActions: %v", err)
	}
	list, err := client.ListActions(ctx, &mnemosv1.ListActionsRequest{Subject: "payments"})
	if err != nil {
		t.Fatalf("ListActions: %v", err)
	}
	if len(list.Actions) != 1 {
		t.Fatalf("subject filter: want 1 got %d", len(list.Actions))
	}
	if list.Actions[0].Kind != "rollback" {
		t.Fatalf("kind round-trip: want rollback got %q", list.Actions[0].Kind)
	}
}

func TestSchemasRoundTrip(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()
	ctx := context.Background()
	now := timestamppb.New(time.Now().UTC())

	if _, err := client.AppendSchemas(ctx, &mnemosv1.AppendSchemasRequest{
		Schemas: []*mnemosv1.Schema{{
			Id: "ls_1", Statement: "rollback works", Trigger: "x_trigger", Kind: "rollback",
			Context: &mnemosv1.Context{Service: "payments"}, Evidence: []string{"ac_1"},
			Confidence: 0.7, DerivedAt: now, Source: "synthesize",
		}},
	}); err != nil {
		t.Fatalf("AppendSchemas: %v", err)
	}
	list, err := client.ListSchemas(ctx, &mnemosv1.ListSchemasRequest{Service: "payments"})
	if err != nil {
		t.Fatalf("ListSchemas: %v", err)
	}
	if len(list.Schemas) != 1 {
		t.Fatalf("service filter: want 1 got %d", len(list.Schemas))
	}
	if list.Schemas[0].Trigger != "x_trigger" {
		t.Fatalf("trigger round-trip: want x_trigger got %q", list.Schemas[0].Trigger)
	}
}

func TestEntityAssociationsRoundTrip(t *testing.T) {
	client, cleanup := startTestServer(t)
	defer cleanup()
	ctx := context.Background()
	now := timestamppb.New(time.Now().UTC())

	if _, err := client.AppendEntityAssociations(ctx, &mnemosv1.AppendEntityAssociationsRequest{
		Edges: []*mnemosv1.EntityAssociation{{
			Id: "er_1", Kind: "action_of",
			FromId: "ac_1", FromType: "action",
			ToId: "oc_1", ToType: "outcome",
			CreatedAt: now,
		}},
	}); err != nil {
		t.Fatalf("AppendEntityAssociations: %v", err)
	}
	list, err := client.ListEntityAssociations(ctx, &mnemosv1.ListEntityAssociationsRequest{Kind: "action_of"})
	if err != nil {
		t.Fatalf("ListEntityAssociations: %v", err)
	}
	if len(list.Edges) != 1 {
		t.Fatalf("kind filter: want 1 got %d", len(list.Edges))
	}
	if list.Edges[0].FromId != "ac_1" {
		t.Fatalf("from_id round-trip: %q", list.Edges[0].FromId)
	}
}
