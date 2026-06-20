package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.klarlabs.de/mnemos"
	mnemoshttp "go.klarlabs.de/mnemos/http"

	_ "go.klarlabs.de/mnemos/internal/store/memory"
)

func newStore(t *testing.T, ns string) mnemos.Store {
	t.Helper()
	t.Setenv("MNEMOS_DB_URL", "")
	store, err := mnemos.New(
		mnemos.WithStorage("memory://?namespace="+ns),
		mnemos.WithPassiveMode(),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// TestNewHandler_RecallRoundTrip exercises the spec's http.NewHandler:
// remember a fact over the wire, then recall it.
func TestNewHandler_RecallRoundTrip(t *testing.T) {
	store := newStore(t, "http_roundtrip")
	handler := mnemoshttp.NewHandler(store)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	// Remember.
	body, _ := json.Marshal(map[string]any{
		"type":    "fact",
		"content": "The user prefers Go for backend services.",
	})
	resp, err := http.Post(srv.URL+"/v1/remember", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST remember: %v", err)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		t.Fatalf("remember status = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Recall.
	q, _ := json.Marshal(map[string]any{"text": "user Go backend"})
	resp2, err := http.Post(srv.URL+"/v1/recall", "application/json", bytes.NewReader(q))
	if err != nil {
		t.Fatalf("POST recall: %v", err)
	}
	defer func() { _ = resp2.Body.Close() }()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("recall status = %d", resp2.StatusCode)
	}
	var out struct {
		Results []struct {
			Text string `json:"text"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&out); err != nil {
		t.Fatalf("decode recall: %v", err)
	}
	if len(out.Results) == 0 {
		t.Fatal("recall returned no results")
	}
}

// TestNewHandler_WriteIsGoverned proves the delivery adapter does not
// bypass the governed kernel: a remember over HTTP leaves a verifiable
// write session on the backing store.
func TestNewHandler_WriteIsGoverned(t *testing.T) {
	store := newStore(t, "http_governed")
	handler := mnemoshttp.NewHandler(store)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	body, _ := json.Marshal(map[string]any{"type": "fact", "content": "Governed over HTTP."})
	resp, err := http.Post(srv.URL+"/v1/remember", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST remember: %v", err)
	}
	_ = resp.Body.Close()

	ws := store.LastWriteSession()
	if ws == nil || ws.ID() == "" {
		t.Fatal("HTTP write left no governed session — no-bypass guarantee violated")
	}
	if err := ws.VerifyEvidenceChain(); err != nil {
		t.Errorf("evidence chain broken: %v", err)
	}
}

// TestNewHandler_ClaimWithoutEvidenceRejected proves the evidence
// non-negotiable holds over the wire.
func TestNewHandler_ClaimWithoutEvidenceRejected(t *testing.T) {
	store := newStore(t, "http_no_evidence")
	handler := mnemoshttp.NewHandler(store)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	body, _ := json.Marshal(map[string]any{"text": "Evidence-less claim over HTTP."})
	resp, err := http.Post(srv.URL+"/v1/claims", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST claims: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
		t.Fatalf("evidence-less claim accepted over HTTP (status %d)", resp.StatusCode)
	}
}

// TestListenAndServe_StartsAndStops verifies the spec's
// http.ListenAndServe convenience boots a server that answers requests.
func TestListenAndServe_StartsAndStops(t *testing.T) {
	store := newStore(t, "http_listen")
	handler := mnemoshttp.NewHandler(store)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- mnemoshttp.ListenAndServe(ctx, "127.0.0.1:0", handler)
	}()
	// Give it a moment then cancel; ListenAndServe must return cleanly.
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-errCh:
		if err != nil && err != http.ErrServerClosed && err != context.Canceled {
			t.Fatalf("ListenAndServe returned %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ListenAndServe did not return after context cancel")
	}
}
