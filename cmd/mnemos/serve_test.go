package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/auth"
	"go.klarlabs.de/mnemos/internal/domain"
)

func TestServe_LandingReturnsHTML(t *testing.T) {
	srv := httptest.NewServer(newServerMux(newServerTestStore_conn(t)))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if ct == "" || ct[:9] != "text/html" {
		t.Fatalf("Content-Type = %q, want text/html...", ct)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if len(body) < 100 {
		t.Fatalf("body suspiciously small: %d bytes", len(body))
	}
	if !strings.Contains(string(body), "Mnemos") {
		t.Errorf("landing body missing Mnemos brand")
	}
}

func TestServe_RegistryAppReturnsHTML(t *testing.T) {
	srv := httptest.NewServer(newServerMux(newServerTestStore_conn(t)))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/app")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(body), "Mnemos Registry") {
		t.Errorf("/app body missing expected title 'Mnemos Registry'")
	}
}

func TestServe_SecurityHeadersOnPublicRoutes(t *testing.T) {
	srv := httptest.NewServer(newServerMux(newServerTestStore_conn(t)))
	defer srv.Close()

	cases := []string{"/", "/app", "/health"}
	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			resp, err := http.Get(srv.URL + path)
			if err != nil {
				t.Fatalf("get %s: %v", path, err)
			}
			defer func() { _ = resp.Body.Close() }()

			required := map[string]string{
				"X-Content-Type-Options": "nosniff",
				"X-Frame-Options":        "DENY",
				"Referrer-Policy":        "no-referrer",
			}
			for k, want := range required {
				if got := resp.Header.Get(k); got != want {
					t.Errorf("%s header %s = %q, want %q", path, k, got, want)
				}
			}
			csp := resp.Header.Get("Content-Security-Policy")
			if csp == "" {
				t.Errorf("%s missing Content-Security-Policy", path)
			}
			for _, fragment := range []string{"default-src 'self'", "frame-ancestors 'none'", "form-action 'self'"} {
				if !strings.Contains(csp, fragment) {
					t.Errorf("%s CSP missing %q (got %q)", path, fragment, csp)
				}
			}
		})
	}
}

func TestServe_HSTSOnlyWithTLSEnv(t *testing.T) {
	// Without TLS env vars set, HSTS must NOT be emitted — sending
	// it over plain HTTP would brick subsequent local-dev requests.
	t.Setenv("MNEMOS_TLS_CERT_FILE", "")
	t.Setenv("MNEMOS_TLS_KEY_FILE", "")

	srv := httptest.NewServer(newServerMux(newServerTestStore_conn(t)))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if got := resp.Header.Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS emitted without TLS: %q", got)
	}
}

func TestServe_RootRejectsUnknownPath(t *testing.T) {
	srv := httptest.NewServer(newServerMux(newServerTestStore_conn(t)))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/random-path-not-a-route")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (root handler shouldn't catch-all)", resp.StatusCode)
	}
}

func TestServe_HealthReturnsOK(t *testing.T) {
	srv := httptest.NewServer(newServerMux(newServerTestStore_conn(t)))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body healthResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Status != "ok" {
		t.Fatalf("status = %q, want 'ok'", body.Status)
	}
}

func TestServe_ListEventsReturnsAndPaginates(t *testing.T) {
	_, conn := openTestStore(t)
	base := time.Now().UTC()
	for i := 0; i < 5; i++ {
		seedEventConn(t, conn, "e"+string(rune('1'+i)), "r1", "claim text "+string(rune('A'+i)), "in"+string(rune('1'+i)), `{"source":"file"}`, base.Add(time.Duration(i)*time.Minute))
	}

	mux := newServerMux(conn)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/events?limit=2&offset=1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var body eventsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 5 {
		t.Errorf("total = %d, want 5", body.Total)
	}
	if body.Limit != 2 || body.Offset != 1 {
		t.Errorf("limit=%d offset=%d, want 2/1", body.Limit, body.Offset)
	}
	if len(body.Events) != 2 {
		t.Errorf("got %d events, want 2", len(body.Events))
	}
}

func TestServe_ListEventsFiltersByRunID(t *testing.T) {
	_, conn := openTestStore(t)
	base := time.Now().UTC()
	seedEventConn(t, conn, "e1", "run-A", "alpha", "in1", `{}`, base)
	seedEventConn(t, conn, "e2", "run-A", "beta", "in2", `{}`, base.Add(time.Minute))
	seedEventConn(t, conn, "e3", "run-B", "gamma", "in3", `{}`, base.Add(2*time.Minute))

	mux := newServerMux(conn)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/events?run_id=run-A")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var body eventsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 2 {
		t.Errorf("total = %d, want 2 (only run-A events)", body.Total)
	}
	for _, e := range body.Events {
		if e.RunID != "run-A" {
			t.Errorf("got event with run_id %q, want only run-A", e.RunID)
		}
	}
}

func TestServe_ContextEndpointReturnsBlock(t *testing.T) {
	_, conn := openTestStore(t)
	base := time.Now().UTC()
	seedEventConn(t, conn, "e1", "ctx-run", "deployment succeeded in production", "in1", `{}`, base)

	mux := newServerMux(conn)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/context?run_id=ctx-run")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body contextResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.RunID != "ctx-run" {
		t.Errorf("run_id = %q, want ctx-run", body.RunID)
	}
	if !strings.Contains(body.Context, "# Memory context (run ctx-run)") {
		t.Errorf("context missing header:\n%s", body.Context)
	}
	if !strings.Contains(body.Context, "## Active claims") {
		t.Errorf("context missing claims section:\n%s", body.Context)
	}
}

func TestServe_SearchEndpointRejectsMissingQuery(t *testing.T) {
	_, conn := openTestStore(t)
	mux := newServerMux(conn)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/search")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestServe_ListClaimsBitemporalAxes(t *testing.T) {
	_, conn := openTestStore(t)
	ctx := context.Background()

	// Three claims:
	//   cl_old_valid: created last week, valid since last month
	//   cl_recent_valid: created today, valid since last month
	//   cl_recent_future: created today, valid only from tomorrow
	now := time.Now().UTC()
	lastWeek := now.AddDate(0, 0, -7)
	lastMonth := now.AddDate(0, -1, 0)
	tomorrow := now.AddDate(0, 0, 1)
	if err := conn.Claims.Upsert(ctx, []domain.Claim{
		{ID: "cl_old", Text: "old", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive, CreatedAt: lastWeek, ValidFrom: lastMonth},
		{ID: "cl_recent", Text: "recent", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive, CreatedAt: now, ValidFrom: lastMonth},
		{ID: "cl_future", Text: "future", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive, CreatedAt: now, ValidFrom: tomorrow},
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	mux := newServerMux(conn)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	get := func(query string) claimsResponse {
		t.Helper()
		resp, err := http.Get(srv.URL + "/v1/claims?" + query)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		var body claimsResponse
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return body
	}

	yesterday := now.AddDate(0, 0, -1).Format(time.RFC3339)

	// recorded_as_of=yesterday drops cl_recent + cl_future (both
	// CreatedAt=now); cl_old (CreatedAt=lastWeek) stays.
	got := get("recorded_as_of=" + yesterday)
	if got.Total != 1 || got.Claims[0].ID != "cl_old" {
		t.Errorf("recorded_as_of=yesterday returned %d claims, want 1 (cl_old): %+v", got.Total, got.Claims)
	}

	// as_of=now drops cl_future (ValidFrom=tomorrow) but keeps both
	// recorded-recently rows.
	got = get("as_of=" + now.Format(time.RFC3339))
	for _, c := range got.Claims {
		if c.ID == "cl_future" {
			t.Errorf("as_of=now should drop cl_future, got it: %+v", got.Claims)
		}
	}

	// Both axes together: recorded last week + valid then. cl_old
	// matches; cl_recent + cl_future drop on recorded; cl_old stays.
	got = get("recorded_as_of=" + yesterday + "&as_of=" + now.Format(time.RFC3339))
	if got.Total != 1 || got.Claims[0].ID != "cl_old" {
		t.Errorf("both filters returned %d claims, want 1 (cl_old): %+v", got.Total, got.Claims)
	}
}

func TestServe_SearchEndpointDefaultsTopK(t *testing.T) {
	_, conn := openTestStore(t)
	mux := newServerMux(conn)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/search?query=anything")
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
	if body.TopK != 10 {
		t.Errorf("default top_k = %d, want 10", body.TopK)
	}
	if body.Query != "anything" {
		t.Errorf("query = %q, want 'anything'", body.Query)
	}
}

func TestServe_SearchEndpointRejectsBadMinTrust(t *testing.T) {
	_, conn := openTestStore(t)
	mux := newServerMux(conn)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/search?query=x&min_trust=2.5")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (min_trust > 1.0 rejected)", resp.StatusCode)
	}
}

func TestServe_ContextEndpointRejectsMissingRunID(t *testing.T) {
	_, conn := openTestStore(t)
	mux := newServerMux(conn)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/context")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestServe_ListEventsCapsAtMax(t *testing.T) {
	_, conn := openTestStore(t)
	mux := newServerMux(conn)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/events?limit=10000")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var body eventsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Limit != maxServePageLimit {
		t.Errorf("limit = %d, want %d (capped)", body.Limit, maxServePageLimit)
	}
}

func TestServe_ListClaimsFiltersByType(t *testing.T) {
	_, conn := openTestStore(t)
	now := time.Now().UTC()
	seedClaimConn(t, conn, "c1", "fact one", "fact", "active", 0.8, now)
	seedClaimConn(t, conn, "c2", "decision one", "decision", "active", 0.9, now)
	seedClaimConn(t, conn, "c3", "decision two", "decision", "active", 0.85, now)

	mux := newServerMux(conn)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/claims?type=decision")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var body claimsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 2 || len(body.Claims) != 2 {
		t.Errorf("total=%d len=%d, want 2/2", body.Total, len(body.Claims))
	}
	for _, c := range body.Claims {
		if c.Type != "decision" {
			t.Errorf("got claim with type %q", c.Type)
		}
	}
}

func TestServe_ListClaimsRejectsInvalidType(t *testing.T) {
	srv := httptest.NewServer(newServerMux(newServerTestStore_conn(t)))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/claims?type=bogus")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// TestServe_ListClaimsByRunID pins the load-bearing tenant filter:
// run_id returns only claims whose evidence links to an event tagged
// with the matching RunID. Critical contract for integrators that
// scope memory by run_id prefix (e.g. animal:<uuid>).
// TestServe_AppendClaimHonorsCreatedBy proves the per-claim created_by override
// (mnemos ClaimItem.CreatedBy) round-trips through the HTTP API: an append that
// supplies created_by attributes the claim to that author (not the token actor),
// and it's echoed back on read.
func TestServe_AppendClaimHonorsCreatedBy(t *testing.T) {
	secret, _ := setupJWTTestEnv(t)
	_, conn := openTestStore(t)
	now := time.Now().UTC()
	// Seed the event the decision claim is evidence-linked to (so the run_id
	// read can resolve it).
	seedEventConn(t, conn, "ev_dec", "athlete:42", "decision context", "src", "{}", now)
	// A writer authorised for claim writes. Its id ("usr_writer") is the TOKEN
	// actor — the created_by override in the body must beat it.
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

	body := map[string]any{
		"claims": []map[string]any{{
			"id": "cl_dec", "text": "approved plan", "type": "decision",
			"status": "active", "created_by": "coach:99",
		}},
		"evidence": []map[string]any{{"claim_id": "cl_dec", "event_id": "ev_dec"}},
	}
	resp := postJSON(t, srv.URL+"/v1/claims", body, authHdr)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		t.Fatalf("append status = %d", resp.StatusCode)
	}

	got := getJSON(t, srv.URL+"/v1/claims?run_id=athlete:42", authHdr)
	defer func() { _ = got.Body.Close() }()
	var out claimsResponse
	if err := json.NewDecoder(got.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Claims) != 1 || out.Claims[0].ID != "cl_dec" {
		t.Fatalf("expected cl_dec, got %+v", out.Claims)
	}
	if out.Claims[0].CreatedBy != "coach:99" {
		t.Errorf("CreatedBy = %q, want coach:99 (per-claim override must beat token actor usr_writer)", out.Claims[0].CreatedBy)
	}
}

func TestServe_ListClaimsByRunID(t *testing.T) {
	_, conn := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Seed events on two different runs.
	seedEventConn(t, conn, "ev_runA", "tenant:A", "content A", "src_a", "{}", now)
	seedEventConn(t, conn, "ev_runB", "tenant:B", "content B", "src_b", "{}", now)

	// Two claims, each evidence-linked to its tenant's event.
	seedClaimConn(t, conn, "cl_A", "claim A", "fact", "active", 0.9, now)
	seedClaimConn(t, conn, "cl_B", "claim B", "fact", "active", 0.9, now)
	if err := conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{
		{ClaimID: "cl_A", EventID: "ev_runA"},
		{ClaimID: "cl_B", EventID: "ev_runB"},
	}); err != nil {
		t.Fatalf("UpsertEvidence: %v", err)
	}

	mux := newServerMux(conn)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Tenant A's filter should return ONLY cl_A — never cl_B.
	resp, err := http.Get(srv.URL + "/v1/claims?run_id=tenant:A")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body claimsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 1 || len(body.Claims) != 1 || body.Claims[0].ID != "cl_A" {
		t.Fatalf("run_id=tenant:A returned %d claims, want only cl_A: %+v", body.Total, body.Claims)
	}
}

// TestServe_ListClaimsByRunID_UnknownRun returns empty, never the
// unfiltered claim list. Fail-closed contract.
func TestServe_ListClaimsByRunID_UnknownRun(t *testing.T) {
	_, conn := openTestStore(t)
	now := time.Now().UTC()
	// Seed a claim WITHOUT an event for the requested run.
	seedClaimConn(t, conn, "cl_only", "lonely", "fact", "active", 0.9, now)

	mux := newServerMux(conn)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/claims?run_id=tenant:nobody")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body claimsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 0 || len(body.Claims) != 0 {
		t.Fatalf("unknown run_id leaked %d claims: %+v", body.Total, body.Claims)
	}
}

// TestServe_ListClaimsByRunID_NoEvidenceFailsClosed pins that a claim
// with NO evidence whatsoever is excluded from a run_id-scoped query.
// Otherwise an orphan claim would leak across every tenant filter.
func TestServe_ListClaimsByRunID_NoEvidenceFailsClosed(t *testing.T) {
	_, conn := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// One event under run X, one orphan claim with no evidence link.
	seedEventConn(t, conn, "ev_x", "tenant:X", "content X", "src_x", "{}", now)
	seedClaimConn(t, conn, "cl_with_ev", "tied", "fact", "active", 0.9, now)
	seedClaimConn(t, conn, "cl_orphan", "untied", "fact", "active", 0.9, now)
	if err := conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{
		{ClaimID: "cl_with_ev", EventID: "ev_x"},
	}); err != nil {
		t.Fatalf("UpsertEvidence: %v", err)
	}

	mux := newServerMux(conn)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/claims?run_id=tenant:X")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body claimsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 1 || body.Claims[0].ID != "cl_with_ev" {
		t.Fatalf("orphan claim should be excluded; got %+v", body.Claims)
	}
}

func TestServe_ListRelationshipsFiltersByType(t *testing.T) {
	_, conn := openTestStore(t)
	now := time.Now().UTC()
	seedClaimConn(t, conn, "c1", "a", "fact", "active", 0.8, now)
	seedClaimConn(t, conn, "c2", "b", "fact", "active", 0.8, now)
	seedClaimConn(t, conn, "c3", "c", "fact", "active", 0.8, now)
	seedRelationshipConn(t, conn, "r1", "supports", "c1", "c2", now)
	seedRelationshipConn(t, conn, "r2", "contradicts", "c1", "c3", now)

	srv := httptest.NewServer(newServerMux(conn))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/relationships?type=contradicts")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var body relationshipsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 1 || len(body.Relationships) != 1 || body.Relationships[0].ID != "r1" && body.Relationships[0].Type != "contradicts" {
		t.Errorf("expected 1 contradiction, got %+v", body)
	}
}

func TestServe_MetricsCountsSchemaCorrectly(t *testing.T) {
	_, conn := openTestStore(t)
	now := time.Now().UTC()
	seedEventConn(t, conn, "e1", "run-a", "x", "in1", `{}`, now)
	seedEventConn(t, conn, "e2", "run-b", "y", "in2", `{}`, now)
	seedClaimConn(t, conn, "c1", "a", "fact", "active", 0.8, now)
	seedClaimConn(t, conn, "c2", "b", "fact", "contested", 0.8, now)
	seedRelationshipConn(t, conn, "r1", "contradicts", "c1", "c2", now)

	srv := httptest.NewServer(newServerMux(conn))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/metrics")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var body metricsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Runs != 2 || body.Events != 2 || body.Claims != 2 || body.ContestedClaims != 1 || body.Relationships != 1 || body.Contradictions != 1 {
		t.Errorf("metrics = %+v", body)
	}
}

func TestServe_UnsupportedMethodReturns405(t *testing.T) {
	// Needs auth to reach the handler; without it the middleware correctly
	// returns 401 before the method dispatch.
	st := newServeJWTTest(t)

	req, _ := http.NewRequest(http.MethodDelete, st.Srv.URL+"/v1/events", nil)
	for k, v := range st.Auth {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", resp.StatusCode)
	}
}

func TestServe_UnknownRouteReturns404(t *testing.T) {
	srv := httptest.NewServer(newServerMux(newServerTestStore_conn(t)))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/nope")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}
