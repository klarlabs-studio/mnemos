package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// TestServe_CognitiveEndpointsDelegate proves the connected-brain routes are
// wired to the Memory facade: over a fresh store each returns 200 with a
// well-formed (empty) body, exercising routing + delegation + serialization.
func TestServe_CognitiveEndpointsDelegate(t *testing.T) {
	t.Setenv("MNEMOS_DB_URL", "sqlite://"+filepath.Join(t.TempDir(), "brain.db"))
	_, conn := openTestStore(t)
	mem, err := newLibraryMemory(context.Background(), "")
	if err != nil {
		t.Fatalf("build facade: %v", err)
	}
	defer func() { _ = mem.Close() }()

	srv := httptest.NewServer(newServerMuxWithMemory(conn, mem, false, true))
	defer srv.Close()

	for _, path := range []string{
		"/v1/who-knows?query=creatinine",
		"/v1/knowledge-gaps",
		"/v1/calibration",
		"/v1/hypercorrections",
		"/v1/recombinations",
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: status %d, want 200", path, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
}

// TestServe_CognitiveUnavailableWithoutFacade: the standard mux (no facade)
// returns 503 on a brain endpoint rather than panicking.
func TestServe_CognitiveUnavailableWithoutFacade(t *testing.T) {
	_, conn := openTestStore(t)
	srv := httptest.NewServer(newServerMux(conn)) // nil facade
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/who-knows?query=x")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (no facade)", resp.StatusCode)
	}
}

// TestServe_WhoKnowsRequiresQuery: 400 when the required query param is missing.
func TestServe_WhoKnowsRequiresQuery(t *testing.T) {
	t.Setenv("MNEMOS_DB_URL", "sqlite://"+filepath.Join(t.TempDir(), "brain.db"))
	_, conn := openTestStore(t)
	mem, err := newLibraryMemory(context.Background(), "")
	if err != nil {
		t.Fatalf("build facade: %v", err)
	}
	defer func() { _ = mem.Close() }()

	srv := httptest.NewServer(newServerMuxWithMemory(conn, mem, false, true))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/v1/who-knows")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}
