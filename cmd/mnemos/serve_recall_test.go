package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func recallFacadeServer(t *testing.T) *httptest.Server {
	t.Helper()
	t.Setenv("MNEMOS_DB_URL", "sqlite://"+filepath.Join(t.TempDir(), "recall.db"))
	_, conn := openTestStore(t)
	mem, err := newLibraryMemory(context.Background(), "")
	if err != nil {
		t.Fatalf("build facade: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	srv := httptest.NewServer(newServerMuxWithMemory(conn, mem, false, true))
	t.Cleanup(srv.Close)
	return srv
}

func TestServe_RecallAllModes(t *testing.T) {
	srv := recallFacadeServer(t)
	for _, mode := range []string{"sufficiency", "effort", "context", "conflicts", "iterative"} {
		resp, err := http.Get(srv.URL + "/v1/recall?query=x&mode=" + mode)
		if err != nil {
			t.Fatalf("GET %s: %v", mode, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("mode %s: status %d, want 200", mode, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
}

func TestServe_RecallRequiresQuery(t *testing.T) {
	srv := recallFacadeServer(t)
	resp, err := http.Get(srv.URL + "/v1/recall")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestServe_RecallRejectsUnknownMode(t *testing.T) {
	srv := recallFacadeServer(t)
	resp, err := http.Get(srv.URL + "/v1/recall?query=x&mode=bogus")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestServe_RecallUnavailableWithoutFacade(t *testing.T) {
	_, conn := openTestStore(t)
	srv := httptest.NewServer(newServerMux(conn))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/v1/recall?query=x")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}
