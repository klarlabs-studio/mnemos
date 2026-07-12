package client_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.klarlabs.de/bolt"
	"go.klarlabs.de/fortify/retry"
	"go.klarlabs.de/mnemos/client"
)

// fakeRegistry stands in for `mnemos serve` end-to-end. Self-contained
// here so client tests don't pull in cmd/mnemos.
type fakeRegistry struct {
	events []client.Event
	claims []client.Claim
	rels   []client.Relationship
	embs   []client.Embedding
	token  string // if non-empty, write methods require Bearer matching this
}

func (f *fakeRegistry) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, client.HealthResponse{Status: "ok", Version: "test"})
	})
	mux.HandleFunc("/v1/metrics", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, client.MetricsResponse{Events: int64(len(f.events)), Claims: int64(len(f.claims))})
	})
	mux.HandleFunc("/v1/episodes", f.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if r.URL.Query().Get("run_id") != "" {
				w.Header().Set("X-Test-RunID", r.URL.Query().Get("run_id"))
			}
			writeJSON(w, http.StatusOK, client.ListEventsResponse{Events: f.events, Total: len(f.events), Limit: 50, Offset: 0})
		case http.MethodPost:
			var body struct {
				Events []client.Event `json:"episodes"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, `{"error":"bad body"}`, http.StatusBadRequest)
				return
			}
			f.events = append(f.events, body.Events...)
			writeJSON(w, http.StatusCreated, client.AppendResponse{Accepted: len(body.Events)})
		}
	}))
	mux.HandleFunc("/v1/beliefs", f.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			// Echo back the type/status filter so tests can verify chaining wired through.
			out := client.ListClaimsResponse{Claims: f.claims, Total: len(f.claims), Limit: 50, Offset: 0}
			if r.URL.Query().Get("type") != "" {
				w.Header().Set("X-Test-Type", r.URL.Query().Get("type"))
			}
			if r.URL.Query().Get("status") != "" {
				w.Header().Set("X-Test-Status", r.URL.Query().Get("status"))
			}
			if r.URL.Query().Get("run_id") != "" {
				w.Header().Set("X-Test-RunID", r.URL.Query().Get("run_id"))
			}
			writeJSON(w, http.StatusOK, out)
		case http.MethodPost:
			var body client.AppendClaimsBody
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, `{"error":"bad body"}`, http.StatusBadRequest)
				return
			}
			f.claims = append(f.claims, body.Claims...)
			writeJSON(w, http.StatusCreated, client.AppendResponse{Accepted: len(body.Claims)})
		}
	}))
	mux.HandleFunc("/v1/associations", f.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if r.URL.Query().Get("type") != "" {
				w.Header().Set("X-Test-Type", r.URL.Query().Get("type"))
			}
			writeJSON(w, http.StatusOK, client.ListRelationshipsResponse{Relationships: f.rels, Total: len(f.rels), Limit: 50, Offset: 0})
		case http.MethodPost:
			var body struct {
				Relationships []client.Relationship `json:"associations"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, `{"error":"bad body"}`, http.StatusBadRequest)
				return
			}
			f.rels = append(f.rels, body.Relationships...)
			writeJSON(w, http.StatusCreated, client.AppendResponse{Accepted: len(body.Relationships)})
		}
	}))
	mux.HandleFunc("/v1/search", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		if query == "" {
			http.Error(w, `{"error":"query required"}`, http.StatusBadRequest)
			return
		}
		topK := 10
		if v := r.URL.Query().Get("top_k"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				topK = n
			}
		}
		writeJSON(w, http.StatusOK, client.SearchResponse{
			Query:  query,
			TopK:   topK,
			Total:  0,
			Claims: []client.SearchClaim{{ID: "cl_test", Text: "stub", Type: "fact", Status: "active"}},
		})
	})
	mux.HandleFunc("/v1/context", func(w http.ResponseWriter, r *http.Request) {
		runID := r.URL.Query().Get("run_id")
		if runID == "" {
			http.Error(w, `{"error":"run_id required"}`, http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, struct {
			RunID   string `json:"run_id"`
			Context string `json:"context"`
		}{
			RunID:   runID,
			Context: "# Memory context (run " + runID + ")\n## Active claims (0)\n",
		})
	})
	mux.HandleFunc("/v1/embeddings", f.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if r.URL.Query().Get("entity_type") != "" {
				w.Header().Set("X-Test-EntityType", r.URL.Query().Get("entity_type"))
			}
			writeJSON(w, http.StatusOK, client.ListEmbeddingsResponse{Embeddings: f.embs, Total: len(f.embs), Limit: 50, Offset: 0})
		case http.MethodPost:
			var body struct {
				Embeddings []client.Embedding `json:"embeddings"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, `{"error":"bad body"}`, http.StatusBadRequest)
				return
			}
			f.embs = append(f.embs, body.Embeddings...)
			writeJSON(w, http.StatusCreated, client.AppendResponse{Accepted: len(body.Embeddings)})
		}
	}))
	return mux
}

func (f *fakeRegistry) requireAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || f.token == "" {
			h(w, r)
			return
		}
		got := r.Header.Get("Authorization")
		if got != "Bearer "+f.token {
			http.Error(w, `{"error":"missing or invalid bearer token"}`, http.StatusUnauthorized)
			return
		}
		h(w, r)
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// TestClient_TenantHeader proves the per-request tenant selection (ADR 0009
// Phase 3): WithTenant sets the X-Mnemos-Tenant header a multi-tenant hosted
// brain reads, an empty/absent tenant sends no header, and one client can hit
// two tenants back-to-back (the federation primitive).
func TestClient_TenantHeader(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get(client.TenantHeader))
		writeJSON(w, http.StatusOK, client.SearchResponse{})
	}))
	defer srv.Close()

	c := client.New(srv.URL)
	ctx := context.Background()
	// Personal (no tenant) then a scoped tenant — same client, two scopes.
	if _, err := c.Search(ctx, "q", client.SearchOptions{}); err != nil {
		t.Fatalf("personal search: %v", err)
	}
	if _, err := c.Search(client.WithTenant(ctx, "repo_acme_abc123"), "q", client.SearchOptions{}); err != nil {
		t.Fatalf("tenant search: %v", err)
	}
	// A blank tenant is a no-op: still no header.
	if _, err := c.Search(client.WithTenant(ctx, "  "), "q", client.SearchOptions{}); err != nil {
		t.Fatalf("blank-tenant search: %v", err)
	}
	want := []string{"", "repo_acme_abc123", ""}
	if len(got) != len(want) {
		t.Fatalf("got %d requests, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("request %d tenant header = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestClient_Health(t *testing.T) {
	srv := httptest.NewServer((&fakeRegistry{}).handler())
	defer srv.Close()

	got, err := client.New(srv.URL).Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if got.Status != "ok" || got.Version != "test" {
		t.Errorf("got %+v", got)
	}
}

func TestEvents_AppendThenList(t *testing.T) {
	reg := &fakeRegistry{}
	srv := httptest.NewServer(reg.handler())
	defer srv.Close()

	c := client.New(srv.URL)
	ctx := context.Background()

	resp, err := c.Events().Append(ctx, []client.Event{
		{ID: "ev_1", Content: "Test event", Timestamp: client.FormatTime(time.Now())},
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if resp.Accepted != 1 {
		t.Errorf("accepted = %d, want 1", resp.Accepted)
	}

	list, err := c.Events().List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if list.Total != 1 || list.Events[0].ID != "ev_1" {
		t.Errorf("got %+v", list)
	}
}

func TestClaims_FilterChainPropagatesQueryParams(t *testing.T) {
	reg := &fakeRegistry{}
	srv := httptest.NewServer(reg.handler())
	defer srv.Close()

	c := client.New(srv.URL)
	// Chain three filters; verify they all reach the server.
	resp := captureHeaders(t, srv.URL+"/v1/beliefs?type=decision&status=active&limit=25", srv.Client(), nil)
	if resp.Header.Get("X-Test-Type") != "decision" || resp.Header.Get("X-Test-Status") != "active" {
		t.Fatalf("filter pass-through broken: type=%q status=%q", resp.Header.Get("X-Test-Type"), resp.Header.Get("X-Test-Status"))
	}

	// And via the actual fluent client.
	_, err := c.Claims().Type("decision").Status("active").Limit(25).List(context.Background())
	if err != nil {
		t.Fatalf("fluent List: %v", err)
	}
}

func TestEventsAndClaims_RunIDFilterReachesServer(t *testing.T) {
	// The fluent builder must send ?run_id= on both /v1/events and /v1/claims
	// so a run-partitioned consumer scopes server-side instead of paging the
	// whole store and filtering by hand. Capture the query the builder emits.
	var gotEventsRunID, gotClaimsRunID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/episodes":
			gotEventsRunID = r.URL.Query().Get("run_id")
			writeJSON(w, http.StatusOK, client.ListEventsResponse{Events: []client.Event{}, Limit: 50})
		case "/v1/beliefs":
			gotClaimsRunID = r.URL.Query().Get("run_id")
			writeJSON(w, http.StatusOK, client.ListClaimsResponse{Claims: []client.Claim{}, Limit: 50})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := client.New(srv.URL)
	ctx := context.Background()
	if _, err := c.Events().RunID("animal:abc").List(ctx); err != nil {
		t.Fatalf("events List: %v", err)
	}
	if _, err := c.Claims().Status("active").RunID("animal:abc").List(ctx); err != nil {
		t.Fatalf("claims List: %v", err)
	}
	if gotEventsRunID != "animal:abc" {
		t.Errorf("events run_id not sent: got %q", gotEventsRunID)
	}
	if gotClaimsRunID != "animal:abc" {
		t.Errorf("claims run_id not sent: got %q", gotClaimsRunID)
	}
}

func TestNewFromEnv_DisabledWhenUnset(t *testing.T) {
	t.Setenv("MNEMOS_URL", "")
	c := client.NewFromEnv()
	if c.IsEnabled() {
		t.Fatal("client should be disabled when MNEMOS_URL is unset")
	}
	// Every call is a graceful no-op: reads yield empty, writes accepted,
	// nothing errors — no server is running.
	ctx := context.Background()
	ev, err := c.Events().List(ctx)
	if err != nil || ev == nil || len(ev.Events) != 0 {
		t.Fatalf("disabled events list: resp=%v err=%v", ev, err)
	}
	if _, err := c.Claims().RunID("animal:x").List(ctx); err != nil {
		t.Fatalf("disabled claims list errored: %v", err)
	}
	if _, err := c.Events().Append(ctx, []client.Event{{ID: "ev_1"}}); err != nil {
		t.Fatalf("disabled append errored: %v", err)
	}
	if _, err := c.Metrics(ctx); err != nil {
		t.Fatalf("disabled metrics errored: %v", err)
	}
}

func TestNewFromEnv_EnabledWhenSet(t *testing.T) {
	reg := &fakeRegistry{}
	srv := httptest.NewServer(reg.handler())
	defer srv.Close()

	t.Setenv("MNEMOS_URL", srv.URL)
	c := client.NewFromEnv()
	if !c.IsEnabled() {
		t.Fatal("client should be enabled when MNEMOS_URL is set")
	}
	if _, err := c.Events().List(context.Background()); err != nil {
		t.Fatalf("enabled events list: %v", err)
	}
}

func TestNilAndDisabledClient_AreNoOp(t *testing.T) {
	ctx := context.Background()
	// A nil *Client must not panic — the guard in do() covers it, so
	// consumers can hold a possibly-nil client and call through it.
	var nilClient *client.Client
	if nilClient.IsEnabled() {
		t.Fatal("nil client should report disabled")
	}
	if _, err := nilClient.Events().List(ctx); err != nil {
		t.Fatalf("nil events list errored: %v", err)
	}
	if _, err := nilClient.Claims().List(ctx); err != nil {
		t.Fatalf("nil claims list errored: %v", err)
	}
	if _, err := nilClient.Metrics(ctx); err != nil {
		t.Fatalf("nil metrics errored: %v", err)
	}
	// ForRun off a nil client is also safe.
	if _, err := nilClient.ForRun("animal:x").Claims().List(ctx); err != nil {
		t.Fatalf("nil ForRun claims list errored: %v", err)
	}
}

func TestForRun_ScopesRunIDOnEventsAndClaims(t *testing.T) {
	var gotEventsRunID, gotClaimsRunID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/episodes":
			gotEventsRunID = r.URL.Query().Get("run_id")
			writeJSON(w, http.StatusOK, client.ListEventsResponse{Events: []client.Event{}, Limit: 50})
		case "/v1/beliefs":
			gotClaimsRunID = r.URL.Query().Get("run_id")
			writeJSON(w, http.StatusOK, client.ListClaimsResponse{Claims: []client.Claim{}, Limit: 50})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	scoped := client.New(srv.URL).ForRun("animal:xyz")
	ctx := context.Background()
	if _, err := scoped.Events().List(ctx); err != nil {
		t.Fatalf("scoped events List: %v", err)
	}
	if _, err := scoped.Claims().Status("active").List(ctx); err != nil {
		t.Fatalf("scoped claims List: %v", err)
	}
	if gotEventsRunID != "animal:xyz" {
		t.Errorf("ForRun did not scope events run_id: got %q", gotEventsRunID)
	}
	if gotClaimsRunID != "animal:xyz" {
		t.Errorf("ForRun did not scope claims run_id: got %q", gotClaimsRunID)
	}
}

func TestExpectations_SetObserveRead(t *testing.T) {
	// A tiny in-memory expectation store behind the three endpoints.
	var stored *client.Expectation
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/expectation") && r.Method == http.MethodPost:
			var body client.Expectation
			_ = json.NewDecoder(r.Body).Decode(&body)
			body.ClaimID = "cl1"
			stored = &body
			writeJSON(w, http.StatusCreated, stored)
		case strings.HasSuffix(r.URL.Path, "/observation") && r.Method == http.MethodPost:
			var body struct {
				Observed float64 `json:"observed"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			stored.Observed = body.Observed
			stored.HasObservation = true
			writeJSON(w, http.StatusOK, stored)
		case strings.HasSuffix(r.URL.Path, "/expectation") && r.Method == http.MethodGet:
			if stored == nil {
				http.Error(w, `{"error":"none"}`, http.StatusNotFound)
				return
			}
			writeJSON(w, http.StatusOK, stored)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := client.New(srv.URL)
	ctx := context.Background()

	set, err := c.SetExpectation(ctx, "cl1", 100, 10, time.Time{})
	if err != nil || set == nil || set.Predicted != 100 || set.Tolerance != 10 {
		t.Fatalf("SetExpectation: %v %+v", err, set)
	}
	obs, err := c.RecordObservation(ctx, "cl1", 104)
	if err != nil || obs == nil || !obs.HasObservation || obs.Observed != 104 {
		t.Fatalf("RecordObservation: %v %+v", err, obs)
	}
	got, err := c.Expectation(ctx, "cl1")
	if err != nil || got == nil || got.Predicted != 100 || got.Observed != 104 {
		t.Fatalf("Expectation: %v %+v", err, got)
	}
}

func TestExpectation_NotFoundReturnsNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":"none"}`, http.StatusNotFound)
	}))
	defer srv.Close()
	got, err := client.New(srv.URL).Expectation(context.Background(), "missing")
	if err != nil {
		t.Fatalf("404 should be nil,nil not error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil expectation, got %+v", got)
	}
}

func TestClaims_AppendSendsCreatedBy(t *testing.T) {
	reg := &fakeRegistry{}
	srv := httptest.NewServer(reg.handler())
	defer srv.Close()

	c := client.New(srv.URL)
	_, err := c.Claims().Append(context.Background(),
		[]client.Claim{{ID: "cl1", Text: "approved plan", Type: "decision", CreatedBy: "coach:42"}}, nil)
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if len(reg.claims) != 1 || reg.claims[0].CreatedBy != "coach:42" {
		t.Fatalf("created_by not sent on append: %+v", reg.claims)
	}
}

func TestRelationships_TypeFilter(t *testing.T) {
	reg := &fakeRegistry{}
	srv := httptest.NewServer(reg.handler())
	defer srv.Close()
	c := client.New(srv.URL)
	if _, err := c.Relationships().Type("contradicts").List(context.Background()); err != nil {
		t.Fatalf("List: %v", err)
	}
	if _, err := c.Relationships().Append(context.Background(), []client.Relationship{
		{ID: "r1", Type: "supports", FromClaimID: "a", ToClaimID: "b"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
}

func TestEmbeddings_EntityTypeFilter(t *testing.T) {
	reg := &fakeRegistry{}
	srv := httptest.NewServer(reg.handler())
	defer srv.Close()
	c := client.New(srv.URL)
	if _, err := c.Embeddings().EntityType("event").List(context.Background()); err != nil {
		t.Fatalf("List: %v", err)
	}
	if _, err := c.Embeddings().Append(context.Background(), []client.Embedding{
		{EntityID: "ev_1", EntityType: "event", Vector: []float32{0.1, 0.2}, Model: "test"},
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
}

func TestAuth_HeaderRequiredOnWrites(t *testing.T) {
	reg := &fakeRegistry{token: "shh"}
	srv := httptest.NewServer(reg.handler())
	defer srv.Close()

	noauth := client.New(srv.URL)
	_, err := noauth.Events().Append(context.Background(), []client.Event{{ID: "ev_x"}})
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized {
		t.Fatalf("got %v, want APIError 401", err)
	}

	authed := client.New(srv.URL, client.WithToken("shh"))
	if _, err := authed.Events().Append(context.Background(), []client.Event{{ID: "ev_x"}}); err != nil {
		t.Fatalf("authed write failed: %v", err)
	}
}

func TestAPIError_TypedAndCarriesServerMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"oh no"}`))
	}))
	defer srv.Close()

	_, err := client.New(srv.URL).Health(context.Background())
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("got %v, want *APIError", err)
	}
	if apiErr.Status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", apiErr.Status)
	}
	if !strings.Contains(apiErr.Error(), "oh no") {
		t.Errorf("server message lost: %v", apiErr)
	}
}

func TestRetry_Retries5xxThenSucceeds(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := attempts.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"please retry"}`))
			return
		}
		writeJSON(w, http.StatusOK, client.HealthResponse{Status: "ok", Version: "after-retries"})
	}))
	defer srv.Close()

	c := client.New(srv.URL, client.WithRetry(retry.Config{
		MaxAttempts:   5,
		InitialDelay:  1 * time.Millisecond,
		MaxDelay:      5 * time.Millisecond,
		BackoffPolicy: retry.BackoffExponential,
	}))
	got, err := c.Health(context.Background())
	if err != nil {
		t.Fatalf("Health (with retry): %v", err)
	}
	if got.Version != "after-retries" {
		t.Errorf("got %+v, want version 'after-retries'", got)
	}
	if attempts.Load() != 3 {
		t.Errorf("attempts = %d, want 3", attempts.Load())
	}
}

func TestRetry_DoesNotRetry4xx(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"client mistake"}`))
	}))
	defer srv.Close()

	c := client.New(srv.URL, client.WithRetry(retry.Config{
		MaxAttempts: 5, InitialDelay: 1 * time.Millisecond, BackoffPolicy: retry.BackoffConstant,
	}))
	_, err := c.Health(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if attempts.Load() != 1 {
		t.Errorf("attempts = %d, want 1 (no retry on 4xx)", attempts.Load())
	}
}

func TestLogger_RecordsEachRequest(t *testing.T) {
	srv := httptest.NewServer((&fakeRegistry{}).handler())
	defer srv.Close()

	var buf bytes.Buffer
	logger := bolt.New(bolt.NewJSONHandler(&buf))
	c := client.New(srv.URL, client.WithLogger(logger))
	if _, err := c.Health(context.Background()); err != nil {
		t.Fatalf("Health: %v", err)
	}
	if _, err := c.Metrics(context.Background()); err != nil {
		t.Fatalf("Metrics: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `"path":"/health"`) {
		t.Errorf("logger missed /health: %s", out)
	}
	if !strings.Contains(out, `"path":"/v1/metrics"`) {
		t.Errorf("logger missed /v1/metrics: %s", out)
	}
	if !strings.Contains(out, `"status":200`) {
		t.Errorf("logger missed status: %s", out)
	}
}

func captureHeaders(t *testing.T, url string, h *http.Client, body []byte) *http.Response {
	t.Helper()
	var req *http.Request
	var err error
	if body != nil {
		req, err = http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	} else {
		req, err = http.NewRequest(http.MethodGet, url, nil)
	}
	if err != nil {
		t.Fatalf("build req: %v", err)
	}
	resp, err := h.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp
}

func TestParseTimeAndFormatTime_RoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	formatted := client.FormatTime(now)
	parsed, err := client.ParseTime(formatted)
	if err != nil {
		t.Fatalf("ParseTime: %v", err)
	}
	if !parsed.Equal(now) {
		t.Errorf("round-trip mismatch: got %v, want %v", parsed, now)
	}
}

// Example_basic shows the recommended fluent shape: build the client once
// with auth/logger/retry, then call resource accessors with chained
// filters on the way to a terminal verb.
func Example_basic() {
	ctx := context.Background()
	logger := bolt.New(bolt.NewJSONHandler(bytes.NewBuffer(nil))) // discard for example
	c := client.New("http://localhost:7777",
		client.WithToken("optional-token"),
		client.WithLogger(logger),
		client.WithRetry(retry.Config{
			MaxAttempts:   3,
			InitialDelay:  200 * time.Millisecond,
			MaxDelay:      time.Second,
			BackoffPolicy: retry.BackoffExponential,
			Jitter:        true,
		}),
	)

	// Write
	if _, err := c.Events().Append(ctx, []client.Event{{
		ID:            "ev_demo",
		RunID:         "demo-run",
		SchemaVersion: "v1",
		Content:       "We chose Postgres for the new service",
		SourceInputID: "in_demo",
		Timestamp:     client.FormatTime(time.Now()),
	}}); err != nil {
		panic(err)
	}

	// Read with chained filters
	list, err := c.Claims().Type("decision").Status("active").Limit(10).List(ctx)
	if err != nil {
		panic(err)
	}
	for _, claim := range list.Claims {
		fmt.Printf("[%s] %s\n", claim.Type, claim.Text)
	}
}

func TestClient_Search(t *testing.T) {
	srv := httptest.NewServer((&fakeRegistry{}).handler())
	defer srv.Close()

	got, err := client.New(srv.URL).Search(context.Background(), "deployment", client.SearchOptions{TopK: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got.Query != "deployment" {
		t.Errorf("query = %q, want deployment", got.Query)
	}
	if got.TopK != 5 {
		t.Errorf("top_k = %d, want 5", got.TopK)
	}
}

func TestClient_SearchRequiresQuery(t *testing.T) {
	srv := httptest.NewServer((&fakeRegistry{}).handler())
	defer srv.Close()

	_, err := client.New(srv.URL).Search(context.Background(), "", client.SearchOptions{})
	if err == nil {
		t.Errorf("expected error when query is empty")
	}
}

func TestClient_Context(t *testing.T) {
	srv := httptest.NewServer((&fakeRegistry{}).handler())
	defer srv.Close()

	got, err := client.New(srv.URL).Context(context.Background(), "run-1", client.ContextOptions{})
	if err != nil {
		t.Fatalf("Context: %v", err)
	}
	if !strings.Contains(got, "# Memory context (run run-1)") {
		t.Errorf("got %q", got)
	}
}

func TestClient_ContextRequiresRunID(t *testing.T) {
	srv := httptest.NewServer((&fakeRegistry{}).handler())
	defer srv.Close()

	_, err := client.New(srv.URL).Context(context.Background(), "", client.ContextOptions{})
	if err == nil {
		t.Errorf("expected error when runID is empty")
	}
}
