package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
)

func crudFacadeServer(t *testing.T) *httptest.Server {
	t.Helper()
	t.Setenv("MNEMOS_DB_URL", "sqlite://"+filepath.Join(t.TempDir(), "crud.db"))
	_, conn := openTestStore(t)
	mem, err := newLibraryMemory(context.Background(), "")
	if err != nil {
		t.Fatalf("build facade: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	srv := httptest.NewServer(newServerMuxWithMemory(conn, mem, false))
	t.Cleanup(srv.Close)
	return srv
}

func TestServe_GetClaimNotFound(t *testing.T) {
	srv := crudFacadeServer(t)
	resp, err := http.Get(srv.URL + "/v1/beliefs/does-not-exist")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestServe_ClassifyOnEmptyStore(t *testing.T) {
	srv := crudFacadeServer(t)
	resp, err := http.Get(srv.URL + "/v1/classify?text=" + url.QueryEscape("a brand new statement"))
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestServe_ClassifyRequiresText(t *testing.T) {
	srv := crudFacadeServer(t)
	resp, err := http.Get(srv.URL + "/v1/classify")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestServe_ListDecisionsEmpty(t *testing.T) {
	srv := crudFacadeServer(t)
	resp, err := http.Get(srv.URL + "/v1/decisions")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestServe_ClaimCRUDUnavailableWithoutFacade(t *testing.T) {
	_, conn := openTestStore(t)
	srv := httptest.NewServer(newServerMux(conn)) // nil facade
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/v1/decisions")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}
