package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/auth"
	"go.klarlabs.de/mnemos/internal/domain"
)

func batch4Server(t *testing.T) (*httptest.Server, map[string]string) {
	t.Helper()
	secret, _ := setupJWTTestEnv(t)
	t.Setenv("MNEMOS_DB_URL", "sqlite://"+filepath.Join(t.TempDir(), "b4.db"))
	_, conn := openTestStore(t)
	mem, err := newLibraryMemory(context.Background(), "")
	if err != nil {
		t.Fatalf("build facade: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	if err := conn.Users.Create(context.Background(), domain.User{
		ID: "usr_writer", Name: "w", Email: "w@test.local",
		Status: domain.UserStatusActive, Scopes: []string{domain.ScopeClaimsWrite},
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	tok, _, err := auth.NewIssuer(secret).IssueUserToken(domain.User{
		ID: "usr_writer", Scopes: []string{domain.ScopeClaimsWrite}, Status: domain.UserStatusActive,
	}, time.Hour)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	srv := httptest.NewServer(newServerMuxWithMemory(conn, mem, false, true))
	t.Cleanup(srv.Close)
	return srv, map[string]string{"Authorization": "Bearer " + tok}
}

func TestServe_Batch4Reads(t *testing.T) {
	srv, _ := batch4Server(t)
	for _, path := range []string{"/v1/blocks?owner=grace", "/v1/timeline?run_id=r", "/v1/signals?run_id=r"} {
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

func TestServe_BlocksRequiresOwner(t *testing.T) {
	srv, _ := batch4Server(t)
	resp, err := http.Get(srv.URL + "/v1/blocks")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestServe_RecordActionAndSynthesize(t *testing.T) {
	srv, authHdr := batch4Server(t)

	resp := postJSON(t, srv.URL+"/v1/actions", map[string]any{
		"kind": "investigate", "subject": "incident-1", "actor": "grace",
	}, authHdr)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("record action status = %d, want 201", resp.StatusCode)
	}
	_ = resp.Body.Close()

	resp2 := postJSON(t, srv.URL+"/v1/synthesize", struct{}{}, authHdr)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("synthesize status = %d, want 200", resp2.StatusCode)
	}
	_ = resp2.Body.Close()
}

func TestServe_Batch4WriteNeedsToken(t *testing.T) {
	srv, _ := batch4Server(t)
	resp, err := http.Post(srv.URL+"/v1/actions", "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (no token)", resp.StatusCode)
	}
}

func TestServe_Batch4UnavailableWithoutFacade(t *testing.T) {
	_, conn := openTestStore(t)
	srv := httptest.NewServer(newServerMux(conn))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/v1/timeline?run_id=r")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
}
