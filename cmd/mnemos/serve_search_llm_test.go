package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// TestServe_SearchEndpointMakesNoLLMCalls pins #472 on the HTTP read path the
// hosted recall hook uses on every prompt: /v1/search returns claims, never an
// answer text, so a configured language model must not be called. It used to
// make one grounded-answer completion per request and discard the result.
func TestServe_SearchEndpointMakesNoLLMCalls(t *testing.T) {
	var completions atomic.Int64
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		completions.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"grounded answer"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer model.Close()
	t.Setenv("MNEMOS_LLM_PROVIDER", "openai-compat")
	t.Setenv("MNEMOS_LLM_BASE_URL", model.URL)
	t.Setenv("MNEMOS_LLM_MODEL", "stub")

	_, conn := openTestStore(t)
	now := time.Now().UTC()
	seedEventConn(t, conn, "ev_go", "run_go", "The user prefers Go for backend services", "src", "{}", now)
	seedClaimConn(t, conn, "cl_go", "The user prefers Go for backend services", "fact", "active", 0.9, now)
	if err := conn.Claims.UpsertEvidence(context.Background(), []domain.ClaimEvidence{
		{ClaimID: "cl_go", EventID: "ev_go"},
	}); err != nil {
		t.Fatalf("UpsertEvidence: %v", err)
	}

	srv := httptest.NewServer(newServerMux(conn))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/search?query=" + url.QueryEscape("which language for backend services"))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body searchResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Claims) == 0 {
		t.Fatal("search returned no claims; with none the engine skips the answer call anyway, so the assertion below would be vacuous")
	}
	if got := completions.Load(); got != 0 {
		t.Fatalf("/v1/search made %d LLM completion(s), want 0", got)
	}
}
