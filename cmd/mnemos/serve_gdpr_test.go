package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/auth"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
)

// gdprFixture wires the same JWT plumbing the write tests use plus a
// claims:write-scoped token. Centralising it keeps each test body
// focused on the assertions that matter.
type gdprFixture struct {
	conn   *store.Conn
	srv    *httptest.Server
	header string
}

func newGDPRFixture(t *testing.T) gdprFixture {
	t.Helper()
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte(i + 7)
	}
	t.Setenv("MNEMOS_JWT_SECRET", hex.EncodeToString(secret))
	t.Setenv("HOME", t.TempDir())

	_, conn := openTestStore(t)
	if err := conn.Users.Create(context.Background(), domain.User{
		ID:        "usr_compliance",
		Name:      "compliance",
		Email:     "c@test.local",
		Status:    domain.UserStatusActive,
		Scopes:    []string{domain.ScopeClaimsWrite},
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	srv := httptest.NewServer(newServerMux(conn))
	t.Cleanup(srv.Close)

	tok, _, err := auth.NewIssuer(secret).IssueUserToken(domain.User{
		ID:     "usr_compliance",
		Scopes: []string{domain.ScopeClaimsWrite},
		Status: domain.UserStatusActive,
	}, time.Hour)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	return gdprFixture{conn: conn, srv: srv, header: "Bearer " + tok}
}

func gdprDelete(t *testing.T, f gdprFixture, runID string) (int, deleteClaimsResponse) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, f.srv.URL+"/v1/claims?run_id="+runID, nil)
	req.Header.Set("Authorization", f.header)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body deleteClaimsResponse
	_ = json.NewDecoder(resp.Body).Decode(&body)
	return resp.StatusCode, body
}

// TestDeleteClaims_CascadesAcrossRepositories pins the GDPR Art.17
// contract: one call removes every row tied to the run_id across
// claims, evidence, embeddings, and events, with per-table counts.
func TestDeleteClaims_CascadesAcrossRepositories(t *testing.T) {
	f := newGDPRFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()

	seedEventConn(t, f.conn, "ev-a", "tenant:A", "doomed", "src1", "{}", now)
	seedEventConn(t, f.conn, "ev-b", "tenant:B", "spared", "src2", "{}", now)
	seedClaimConn(t, f.conn, "cl-a", "doomed claim", "fact", "active", 0.9, now)
	seedClaimConn(t, f.conn, "cl-b", "spared claim", "fact", "active", 0.9, now)
	if err := f.conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{
		{ClaimID: "cl-a", EventID: "ev-a"},
		{ClaimID: "cl-b", EventID: "ev-b"},
	}); err != nil {
		t.Fatalf("UpsertEvidence: %v", err)
	}
	if err := f.conn.Embeddings.Upsert(ctx, "cl-a", "claim", []float32{1, 0, 0}, "m", ""); err != nil {
		t.Fatalf("upsert embedding: %v", err)
	}

	status, resp := gdprDelete(t, f, "tenant:A")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if resp.ClaimsDeleted != 1 || resp.EventsDeleted != 1 || resp.EmbeddingsDeleted != 1 {
		t.Errorf("counts wrong: %+v", resp)
	}
	if resp.RunID != "tenant:A" {
		t.Errorf("RunID echoed wrong: %q", resp.RunID)
	}

	// Spared tenant survives.
	leftClaims, _ := f.conn.Claims.ListByIDs(ctx, []string{"cl-b"})
	if len(leftClaims) != 1 {
		t.Errorf("spared tenant lost claims: %+v", leftClaims)
	}
	if _, err := f.conn.Events.GetByID(ctx, "ev-b"); err != nil {
		t.Errorf("spared tenant lost event: %v", err)
	}

	// Doomed gone.
	gone, _ := f.conn.Claims.ListByIDs(ctx, []string{"cl-a"})
	if len(gone) != 0 {
		t.Errorf("doomed claim still present: %+v", gone)
	}
	if _, err := f.conn.Events.GetByID(ctx, "ev-a"); err == nil {
		t.Errorf("doomed event still present")
	}
}

// TestDeleteClaims_Idempotent: second call returns 200 with zero
// counts. Compliance retries must not error out.
func TestDeleteClaims_Idempotent(t *testing.T) {
	f := newGDPRFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	seedEventConn(t, f.conn, "ev-x", "tenant:X", "x", "src", "{}", now)
	seedClaimConn(t, f.conn, "cl-x", "x", "fact", "active", 0.9, now)
	if err := f.conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{{ClaimID: "cl-x", EventID: "ev-x"}}); err != nil {
		t.Fatalf("UpsertEvidence: %v", err)
	}

	if status, _ := gdprDelete(t, f, "tenant:X"); status != http.StatusOK {
		t.Fatalf("first DELETE status = %d", status)
	}
	status, resp := gdprDelete(t, f, "tenant:X")
	if status != http.StatusOK {
		t.Errorf("repeat DELETE status = %d, want 200", status)
	}
	if resp.ClaimsDeleted != 0 || resp.EventsDeleted != 0 {
		t.Errorf("repeat DELETE returned non-zero counts: %+v", resp)
	}
}

// TestDeleteClaims_SharedEvidencePreservesClaim is the subtle
// correctness case: a claim with evidence under multiple run_ids
// must NOT be cascade-deleted when one run is wiped.
func TestDeleteClaims_SharedEvidencePreservesClaim(t *testing.T) {
	f := newGDPRFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	seedEventConn(t, f.conn, "ev-a", "tenant:A", "a", "src", "{}", now)
	seedEventConn(t, f.conn, "ev-b", "tenant:B", "b", "src", "{}", now)
	seedClaimConn(t, f.conn, "cl-shared", "fact shared by A and B", "fact", "active", 0.9, now)
	if err := f.conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{
		{ClaimID: "cl-shared", EventID: "ev-a"},
		{ClaimID: "cl-shared", EventID: "ev-b"},
	}); err != nil {
		t.Fatalf("UpsertEvidence: %v", err)
	}

	status, resp := gdprDelete(t, f, "tenant:A")
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if resp.ClaimsDeleted != 0 {
		t.Errorf("shared claim should NOT be cascade-deleted; counts=%+v", resp)
	}
	if resp.EventsDeleted != 1 {
		t.Errorf("expected ev-a deleted, got %+v", resp)
	}
	got, _ := f.conn.Claims.ListByIDs(ctx, []string{"cl-shared"})
	if len(got) != 1 {
		t.Fatalf("shared claim lost: %+v", got)
	}
}

// TestDeleteClaims_RequiresRunID — without run_id, 400 not "delete
// everything". A missing query param must NOT silently nuke the
// store.
func TestDeleteClaims_RequiresRunID(t *testing.T) {
	f := newGDPRFixture(t)
	req, _ := http.NewRequest(http.MethodDelete, f.srv.URL+"/v1/claims", nil)
	req.Header.Set("Authorization", f.header)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// TestDeleteClaims_RequiresAuthScope blocks the call without
// claims:write. GDPR delete is privileged.
func TestDeleteClaims_RequiresAuthScope(t *testing.T) {
	f := newGDPRFixture(t)
	req, _ := http.NewRequest(http.MethodDelete, f.srv.URL+"/v1/claims?run_id=anything", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 400 {
		t.Errorf("status = %d, want 4xx (auth required)", resp.StatusCode)
	}
}
