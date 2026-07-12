package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
)

// TestProcessEndpoint drives POST /v1/process end-to-end: it runs the real
// ingest→extract→relate pipeline against a temp SQLite brain and asserts the
// response counts plus that claims actually landed in the store. This is the
// server-side path a hosted capture hook POSTs to.
func TestProcessEndpoint(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MNEMOS_URL", "") // ensure local, not hosted
	dbPath := filepath.Join(t.TempDir(), "brain.db")
	t.Setenv("MNEMOS_DB_URL", "sqlite://"+dbPath)

	// Inject a wildcard scope + actor (jwtAuthMiddleware does this in production).
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := withScopes(r.Context(), []string{domain.ScopeWildcard})
		ctx = withActor(ctx, "tester")
		makeProcessHandler()(w, r.WithContext(ctx))
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	body := `{"text":"We migrated the cache from Memcached to Redis for pub/sub support."}`
	resp, err := http.Post(srv.URL+"/v1/process", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /v1/process: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out processResponseDTO
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.RunID == "" || out.Events == 0 {
		t.Fatalf("expected a run id and >=1 event, got %+v", out)
	}

	// Verify claims actually persisted.
	conn, err := openConn(context.Background())
	if err != nil {
		t.Fatalf("openConn: %v", err)
	}
	defer func() { _ = conn.Close() }()
	claims, err := conn.Claims.ListAll(context.Background())
	if err != nil {
		t.Fatalf("list claims: %v", err)
	}
	if len(claims) != out.Claims {
		t.Errorf("store has %d claims, response reported %d", len(claims), out.Claims)
	}
}

// TestProcessEndpoint_RequiresScope confirms the write scope is enforced.
func TestProcessEndpoint_RequiresScope(t *testing.T) {
	srv := httptest.NewServer(makeProcessHandler()) // no scope injected
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/v1/process", "application/json", strings.NewReader(`{"text":"x"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (missing claims:write)", resp.StatusCode)
	}
}

// TestHostedHookRouting verifies the recall/brief/capture hooks call the hosted
// brain's REST endpoints (with the bearer token) when MNEMOS_URL is configured,
// instead of touching a local store.
func TestHostedHookRouting(t *testing.T) {
	var (
		mu   sync.Mutex
		hits []string
		auth string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, r.Method+" "+r.URL.Path)
		if a := r.Header.Get("Authorization"); a != "" {
			auth = a
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/search":
			_, _ = w.Write([]byte(`{"query":"q","claims":[{"id":"c1","text":"chose Redis","type":"decision","trust_score":0.9}],"contradictions":[]}`))
		case "/v1/metrics":
			_, _ = w.Write([]byte(`{"runs":2,"episodes":5,"beliefs":7,"dissonances":1}`))
		case "/v1/process":
			_, _ = w.Write([]byte(`{"run_id":"r1","events":1,"claims":2,"relationships":0}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	t.Setenv("MNEMOS_URL", srv.URL)
	t.Setenv("MNEMOS_TOKEN", "tok123")
	if !hostedConfigured() {
		t.Fatal("MNEMOS_URL set → hostedConfigured must be true")
	}

	// recall → GET /v1/search
	hookRecall(hookEvent{Prompt: "what did we choose for cache?"})
	// brief → GET /v1/metrics
	hookBrief(hookEvent{})
	// capture → POST /v1/process
	transcript := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(transcript, []byte(`{"type":"user","message":{"role":"user","content":"switch cache to Redis"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hookCapture(hookEvent{TranscriptPath: transcript})

	mu.Lock()
	defer mu.Unlock()
	want := map[string]bool{"GET /v1/search": false, "GET /v1/metrics": false, "POST /v1/process": false}
	for _, h := range hits {
		if _, ok := want[h]; ok {
			want[h] = true
		}
	}
	for h, seen := range want {
		if !seen {
			t.Errorf("hosted hook did not call %q (hits: %v)", h, hits)
		}
	}
	if auth != "Bearer tok123" {
		t.Errorf("Authorization = %q, want %q", auth, "Bearer tok123")
	}
}
