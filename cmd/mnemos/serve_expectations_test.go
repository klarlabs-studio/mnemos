package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/auth"
	"go.klarlabs.de/mnemos/internal/domain"
)

// TestServe_ExpectationLifecycle exercises the predict + observe half of the
// expectation surface over HTTP: attach an expectation to a claim, record the
// observed value, and read the two back for the surprise comparison.
func TestServe_ExpectationLifecycle(t *testing.T) {
	secret, _ := setupJWTTestEnv(t)
	_, conn := openTestStore(t)
	now := time.Now().UTC()
	seedClaimConn(t, conn, "cl_pred", "weekly plan approved", "decision", "active", 1.0, now)

	if err := conn.Users.Create(context.Background(), domain.User{
		ID: "usr_writer", Name: "w", Email: "w@test.local",
		Status: domain.UserStatusActive, Scopes: []string{domain.ScopeClaimsWrite},
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	tok, _, err := auth.NewIssuer(secret).IssueUserToken(domain.User{
		ID: "usr_writer", Scopes: []string{domain.ScopeClaimsWrite}, Status: domain.UserStatusActive,
	}, time.Hour)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	srv := httptest.NewServer(newServerMux(conn))
	defer srv.Close()
	authHdr := map[string]string{"Authorization": "Bearer " + tok}

	// 1) Attach an expectation: 100 ± 10 by next week.
	resp := postJSON(t, srv.URL+"/v1/claims/cl_pred/expectation", map[string]any{
		"predicted": 100.0, "tolerance": 10.0, "horizon": now.Add(7 * 24 * time.Hour).Format(time.RFC3339),
	}, authHdr)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("set expectation status = %d, want 201", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// 2) Record the observed value.
	resp2 := postJSON(t, srv.URL+"/v1/claims/cl_pred/observation", map[string]any{"observed": 104.0}, authHdr)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("observe status = %d, want 200", resp2.StatusCode)
	}
	_ = resp2.Body.Close()

	// 3) Read it back.
	got := getJSON(t, srv.URL+"/v1/claims/cl_pred/expectation", authHdr)
	defer func() { _ = got.Body.Close() }()
	var exp expectationDTO
	if err := json.NewDecoder(got.Body).Decode(&exp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if exp.Predicted != 100 || exp.Tolerance != 10 {
		t.Errorf("expectation predicted/tolerance = %v/%v, want 100/10", exp.Predicted, exp.Tolerance)
	}
	if !exp.HasObservation || exp.Observed != 104 {
		t.Errorf("observation not recorded: has=%v observed=%v", exp.HasObservation, exp.Observed)
	}
}

// TestServe_ExpectationNotFound returns 404 when a claim has no expectation.
func TestServe_ExpectationNotFound(t *testing.T) {
	_, conn := openTestStore(t)
	srv := httptest.NewServer(newServerMux(conn))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/v1/claims/cl_missing/expectation")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}
