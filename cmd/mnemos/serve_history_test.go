package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// TestClaimHistory_AppendsOnEveryUpsert pins the load-bearing audit
// property of #38: every Upsert path appends one row to the claim's
// version chain. A fresh insert + two edits = three rows.
func TestClaimHistory_AppendsOnEveryUpsert(t *testing.T) {
	_, conn := openTestStore(t)
	srv := httptest.NewServer(newServerMux(conn))
	defer srv.Close()
	ctx := context.Background()
	now := time.Now().UTC()

	// v1: fresh insert.
	if err := conn.Claims.Upsert(ctx, []domain.Claim{{
		ID: "cl_hist", Text: "first text",
		Type: domain.ClaimTypeFact, Confidence: 0.6,
		Status: domain.ClaimStatusActive, CreatedAt: now,
	}}); err != nil {
		t.Fatalf("v1 upsert: %v", err)
	}
	// v2: edit text + bump confidence.
	if err := conn.Claims.Upsert(ctx, []domain.Claim{{
		ID: "cl_hist", Text: "refined text",
		Type: domain.ClaimTypeFact, Confidence: 0.8,
		Status: domain.ClaimStatusActive, CreatedAt: now,
	}}); err != nil {
		t.Fatalf("v2 upsert: %v", err)
	}
	// v3: flip status to contested.
	if err := conn.Claims.Upsert(ctx, []domain.Claim{{
		ID: "cl_hist", Text: "refined text",
		Type: domain.ClaimTypeFact, Confidence: 0.8,
		Status: domain.ClaimStatusContested, CreatedAt: now,
	}}); err != nil {
		t.Fatalf("v3 upsert: %v", err)
	}

	resp, err := http.Get(srv.URL + "/v1/beliefs/cl_hist/history")
	if err != nil {
		t.Fatalf("GET history: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body claimHistoryResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Versions) != 3 {
		t.Fatalf("got %d versions, want 3", len(body.Versions))
	}
	// Newest first.
	if body.Versions[0].Version != 3 || body.Versions[0].Status != "contested" {
		t.Errorf("rank[0] not v3/contested: %+v", body.Versions[0])
	}
	if body.Versions[1].Version != 2 || body.Versions[1].Confidence != 0.8 {
		t.Errorf("rank[1] not v2: %+v", body.Versions[1])
	}
	if body.Versions[2].Version != 1 || body.Versions[2].Text != "first text" {
		t.Errorf("rank[2] not v1: %+v", body.Versions[2])
	}
}

// TestClaimHistory_UnknownClaimEmpty returns an empty chain (not 404)
// for a claim id that was never upserted — matches the audit semantics
// "no history" rather than "lookup failed".
func TestClaimHistory_UnknownClaimEmpty(t *testing.T) {
	_, conn := openTestStore(t)
	srv := httptest.NewServer(newServerMux(conn))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/v1/beliefs/cl_nope/history")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body claimHistoryResponse
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if len(body.Versions) != 0 {
		t.Errorf("unknown claim returned %d versions", len(body.Versions))
	}
}

// TestClaimHistory_RejectsNonGET pins that the subresource is GET-
// only. The auth middleware gates write methods upstream, so an
// unauthenticated POST returns 4xx (auth check fires before the
// method check) rather than the literal 405 — either is fine for a
// read-only endpoint, the contract is "don't accept this method".
func TestClaimHistory_RejectsNonGET(t *testing.T) {
	_, conn := openTestStore(t)
	srv := httptest.NewServer(newServerMux(conn))
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/v1/beliefs/cl_x/history", "application/json", nil)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 400 {
		t.Errorf("status = %d, want 4xx (POST not accepted)", resp.StatusCode)
	}
}
