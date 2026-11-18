package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"go.klarlabs.de/bolt"
	"go.klarlabs.de/mnemos"
	"go.klarlabs.de/mnemos/internal/domain"
	mnemosgrpc "go.klarlabs.de/mnemos/internal/server/grpc"
	"go.klarlabs.de/mnemos/internal/store"
	mnemosv1 "go.klarlabs.de/mnemos/proto/gen/mnemos/v1"

	grpclib "google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestRecallParity_EverySurfaceAgrees asks the same questions through the CLI,
// both MCP recall tools, REST /v1/search, gRPC Search and the Go library against one brain,
// and requires the same beliefs in the same order from each. They share one
// path (recall.go → query.Engine.Recall); this pins the behaviour, and
// TestRecallSurfacesGoThroughEngineRecall pins the structure.
//
// The brain holds a belief retracted after the recorded-as-of instant, so the
// time-bounded case can only agree if every surface parses and forwards both
// time axes identically.
func TestRecallParity_EverySurfaceAgrees(t *testing.T) {
	for _, env := range []string{"MNEMOS_EMBED_PROVIDER", "MNEMOS_LLM_PROVIDER", "MNEMOS_URL"} {
		t.Setenv(env, "")
	}
	// Retrieval write-backs mutate the brain between surfaces; the comparison
	// is of what each surface retrieves, so they are off.
	for _, env := range []string{"MNEMOS_HEBBIAN", "MNEMOS_RECONSOLIDATE", "MNEMOS_INHIBIT"} {
		t.Setenv(env, "off")
	}
	t.Chdir(t.TempDir()) // no repo brain to federate

	dsn := "sqlite://" + filepath.Join(t.TempDir(), "parity.db")
	t.Setenv("MNEMOS_DB_URL", dsn)
	ctx := context.Background()
	conn, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = conn.Close() }()

	seed := func(id, text string) {
		t.Helper()
		now := time.Now().UTC()
		if err := conn.Events.Append(ctx, domain.Event{
			ID: "ev-" + id, RunID: "parity", SchemaVersion: "v1", SourceInputID: "parity",
			Content: text, Timestamp: now, IngestedAt: now,
		}); err != nil {
			t.Fatalf("event %s: %v", id, err)
		}
		if err := conn.Claims.Upsert(ctx, []domain.Claim{{
			ID: "cl-" + id, Text: text, Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive,
			Confidence: 0.8, TrustScore: 0.8, CreatedAt: now, ValidFrom: now.Add(-time.Hour),
		}}); err != nil {
			t.Fatalf("claim %s: %v", id, err)
		}
		if err := conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{{ClaimID: "cl-" + id, EventID: "ev-" + id}}); err != nil {
			t.Fatalf("evidence %s: %v", id, err)
		}
	}
	seed("pool", "checkout service connection pool size is 50")
	seed("disk", "checkout outage cause was database disk saturation")
	time.Sleep(5 * time.Millisecond)
	recordedAt := time.Now().UTC()
	time.Sleep(5 * time.Millisecond)
	seed("rca", "checkout outage cause was the connection pool change")
	disk, err := conn.Claims.ListByIDs(ctx, []string{"cl-disk"})
	if err != nil || len(disk) != 1 {
		t.Fatalf("load: %v", err)
	}
	disk[0].Status = domain.ClaimStatusDeprecated
	if err := conn.Claims.UpsertWithReason(ctx, disk, "retracted"); err != nil {
		t.Fatalf("retract: %v", err)
	}

	srv := httptest.NewServer(newServerMux(conn))
	defer srv.Close()
	grpcClient := startParityGRPC(t, conn)
	mem, err := mnemos.New(mnemos.WithStorage(dsn), mnemos.WithPassiveMode())
	if err != nil {
		t.Fatalf("library: %v", err)
	}
	defer func() { _ = mem.Close() }()

	const question = "checkout outage cause connection pool database"
	recorded := recordedAt.Format(time.RFC3339Nano)
	asOf := time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano)

	type surface func(t *testing.T, recordedAsOf string) []string
	surfaces := map[string]surface{
		"cli": func(t *testing.T, recordedAsOf string) []string {
			args := []string{"--at", asOf}
			if recordedAsOf != "" {
				args = append(args, "--recorded-at", recordedAsOf)
			}
			var runErr error
			out := captureStdout(t, func() { runErr = runQuery(append(args, question), Flags{}) })
			if runErr != nil {
				t.Fatalf("cli: %v", runErr)
			}
			var got recallOutput
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatalf("cli output: %v\n%s", err, out)
			}
			return claimIDs(got.Claims)
		},
		"mcp query_knowledge": func(t *testing.T, recordedAsOf string) []string {
			got, err := mcpRunQueryScoped(ctx, mcpQueryInput{Question: question, AsOf: asOf, RecordedAsOf: recordedAsOf})
			if err != nil {
				t.Fatalf("query_knowledge: %v", err)
			}
			return claimIDs(got.Claims)
		},
		"mcp recall_at_time": func(t *testing.T, recordedAsOf string) []string {
			got, err := mcpRunRecallAtTime(ctx, mcpRecallAtTimeInput{Question: question, AsOf: asOf, RecordedAsOf: recordedAsOf})
			if err != nil {
				t.Fatalf("recall_at_time: %v", err)
			}
			return claimIDs(got.Claims)
		},
		"rest /v1/search": func(t *testing.T, recordedAsOf string) []string {
			q := url.Values{"query": {question}, "as_of": {asOf}}
			if recordedAsOf != "" {
				q.Set("recorded_as_of", recordedAsOf)
			}
			resp, err := http.Get(srv.URL + "/v1/search?" + q.Encode())
			if err != nil {
				t.Fatalf("rest: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("rest status %d", resp.StatusCode)
			}
			var body searchResponse
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("rest decode: %v", err)
			}
			ids := make([]string, 0, len(body.Claims))
			for _, c := range body.Claims {
				ids = append(ids, c.ID)
			}
			return ids
		},
		"grpc Search": func(t *testing.T, recordedAsOf string) []string {
			at, _ := parseInstant(asOf)
			req := &mnemosv1.SearchRequest{Query: question, AsOf: timestamppb.New(at)}
			if recordedAsOf != "" {
				rec, _ := parseInstant(recordedAsOf)
				req.RecordedAsOf = timestamppb.New(rec)
			}
			resp, err := grpcClient.Search(ctx, req)
			if err != nil {
				t.Fatalf("grpc: %v", err)
			}
			ids := make([]string, 0, len(resp.GetBeliefs()))
			for _, b := range resp.GetBeliefs() {
				ids = append(ids, b.GetId())
			}
			return ids
		},
		"library": func(t *testing.T, recordedAsOf string) []string {
			q := mnemos.Query{Text: question}
			q.AsOf, _ = parseInstant(asOf)
			q.RecordedAsOf, _ = parseInstant(recordedAsOf)
			res, err := mem.Recall(ctx, q)
			if err != nil {
				t.Fatalf("library: %v", err)
			}
			ids := make([]string, 0, len(res))
			for _, r := range res {
				ids = append(ids, r.ClaimID)
			}
			return ids
		},
	}

	for _, tc := range []struct {
		name, recordedAsOf string
		want               []string // order-insensitive membership the answer must have
		absent             []string
	}{
		{"now", "", []string{"cl-rca", "cl-pool"}, []string{"cl-disk"}},
		{"as recorded before the root cause", recorded, []string{"cl-disk", "cl-pool"}, []string{"cl-rca"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reference []string
			var referenceName string
			for _, name := range []string{"cli", "mcp query_knowledge", "mcp recall_at_time", "rest /v1/search", "grpc Search", "library"} {
				got := surfaces[name](t, tc.recordedAsOf)
				for _, id := range tc.want {
					if !slices.Contains(got, id) {
						t.Errorf("%s: missing %s in %v", name, id, got)
					}
				}
				for _, id := range tc.absent {
					if slices.Contains(got, id) {
						t.Errorf("%s: returned %s, which it must not", name, id)
					}
				}
				if reference == nil {
					reference, referenceName = got, name
					continue
				}
				if !slices.Equal(got, reference) {
					t.Errorf("%s returned %v, %s returned %v: the same question must get the same beliefs on every surface",
						name, got, referenceName, reference)
				}
			}
		})
	}
}

// startParityGRPC serves conn over gRPC with the daemon surfaces attached, as
// `mnemos serve` wires them, and auth off (no verifier).
func startParityGRPC(t *testing.T, conn *store.Conn) mnemosv1.MnemosServiceClient {
	t.Helper()
	impl := withDaemonSurfaces(mnemosgrpc.NewServer(conn, nil, bolt.New(bolt.NewJSONHandler(io.Discard)), "test"))
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
	return mnemosv1.NewMnemosServiceClient(cc)
}
