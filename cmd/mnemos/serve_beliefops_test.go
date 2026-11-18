package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/auth"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
)

// beliefOpsFixture is a real `serve` mux over one SQLite brain, with the
// Memory facade and the direct conn on the SAME database, so a test can write
// through the HTTP surface and assert on what the store returns.
type beliefOpsFixture struct {
	srv    *httptest.Server
	conn   *store.Conn
	secret []byte
}

func newBeliefOpsFixture(t *testing.T) beliefOpsFixture {
	t.Helper()
	secret, _ := setupJWTTestEnv(t)
	dsn := "sqlite://" + filepath.Join(t.TempDir(), "beliefops.db")
	t.Setenv("MNEMOS_DB_URL", dsn)
	conn, err := store.Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	mem, err := newLibraryMemory(context.Background(), "")
	if err != nil {
		t.Fatalf("build facade: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	srv := httptest.NewServer(newServerMuxWithMemory(conn, mem, false, false, false))
	t.Cleanup(srv.Close)
	return beliefOpsFixture{srv: srv, conn: conn, secret: secret}
}

func (f beliefOpsFixture) token(t *testing.T, scopes ...string) string {
	t.Helper()
	tok, _, err := auth.NewIssuer(f.secret).IssueAgentTokenWithScopes("agt_ops", scopes, time.Hour)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	return tok
}

func (f beliefOpsFixture) runToken(t *testing.T, scopes, runs []string) string {
	t.Helper()
	tok, _, err := auth.NewIssuer(f.secret).IssueAgentTokenWithScopesAndRuns("agt_run", scopes, runs, time.Hour)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	return tok
}

// call sends body (JSON-encoded unless nil) and decodes a 2xx response into out.
func (f beliefOpsFixture) call(t *testing.T, method, path, token string, body, out any) int {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, f.srv.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if out != nil && resp.StatusCode < 300 {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("decode %s %s: %v", method, path, err)
		}
	}
	return resp.StatusCode
}

func (f beliefOpsFixture) stored(t *testing.T, id string) domain.Claim {
	t.Helper()
	got, err := f.conn.Claims.ListByIDs(context.Background(), []string{id})
	if err != nil || len(got) != 1 {
		t.Fatalf("read %s back: %v (%d rows)", id, err, len(got))
	}
	return got[0]
}

func TestServe_BeliefGovernanceRoutes(t *testing.T) {
	f := newBeliefOpsFixture(t)
	now := time.Now().UTC()
	for _, id := range []string{"cl-a", "cl-b", "cl-c"} {
		seedClaimConn(t, f.conn, id, "belief "+id, "fact", "active", 0.8, now)
	}
	writer := f.token(t, domain.ScopeClaimsWrite)
	reader := f.token(t, "claims:read")

	if got := f.call(t, http.MethodPost, "/v1/beliefs/cl-c/deprecate", reader, nil, nil); got != http.StatusForbidden {
		t.Errorf("deprecate with claims:read = %d, want 403", got)
	}
	if got := f.call(t, http.MethodPost, "/v1/beliefs/cl-missing/deprecate", writer, nil, nil); got != http.StatusNotFound {
		t.Errorf("deprecate unknown = %d, want 404", got)
	}
	if got := f.call(t, http.MethodPost, "/v1/beliefs/cl-c/deprecate", writer, map[string]string{"reason": "stale"}, nil); got != http.StatusOK {
		t.Fatalf("deprecate = %d", got)
	}
	if s := f.stored(t, "cl-c").Status; s != domain.ClaimStatusDeprecated {
		t.Errorf("stored status after deprecate = %s", s)
	}

	if got := f.call(t, http.MethodPost, "/v1/beliefs/cl-a/update", writer, map[string]any{"text": " "}, nil); got != http.StatusBadRequest {
		t.Errorf("update with blank text = %d, want 400", got)
	}
	if got := f.call(t, http.MethodPost, "/v1/beliefs/cl-a/update", writer, map[string]any{"text": "revised", "confidence": 0.6}, nil); got != http.StatusOK {
		t.Fatalf("update = %d", got)
	}
	if c := f.stored(t, "cl-a"); c.Text != "revised" || c.Confidence != 0.6 {
		t.Errorf("stored after update = %q %.2f", c.Text, c.Confidence)
	}

	if got := f.call(t, http.MethodPost, "/v1/beliefs/cl-a/resolve", writer, map[string]string{"loser_id": "cl-b"}, nil); got != http.StatusOK {
		t.Fatalf("resolve = %d", got)
	}
	if a, b := f.stored(t, "cl-a").Status, f.stored(t, "cl-b").Status; a != domain.ClaimStatusResolved || b != domain.ClaimStatusDeprecated {
		t.Errorf("stored after resolve: %s / %s", a, b)
	}

	var esc struct {
		EscalationReason string `json:"escalation_reason"`
	}
	if got := f.call(t, http.MethodPost, "/v1/beliefs/cl-a/escalate", writer, map[string]string{"reason": "human please"}, &esc); got != http.StatusOK || esc.EscalationReason != "human please" {
		t.Errorf("escalate = %d %+v", got, esc)
	}

	// verify is memory_promote's operation and takes its scope.
	if got := f.call(t, http.MethodPost, "/v1/beliefs/cl-a/verify", writer, nil, nil); got != http.StatusForbidden {
		t.Errorf("verify with claims:write only = %d, want 403", got)
	}
	if got := f.call(t, http.MethodPost, "/v1/beliefs/cl-a/verify", f.token(t, domain.ScopePromoteGlobal), nil, nil); got != http.StatusOK {
		t.Fatalf("verify = %d", got)
	}
	if c := f.stored(t, "cl-a"); c.VerifyCount != 1 || c.LastVerified.IsZero() {
		t.Errorf("stored after verify: count %d last %v", c.VerifyCount, c.LastVerified)
	}
}

func TestServe_FactsAndScan(t *testing.T) {
	f := newBeliefOpsFixture(t)
	writer := f.token(t, domain.ScopeClaimsWrite)

	if got := f.call(t, http.MethodPost, "/v1/facts", f.token(t, "claims:read"), map[string]any{"text": "x", "evidence": []string{"y"}}, nil); got != http.StatusForbidden {
		t.Errorf("facts with claims:read = %d, want 403", got)
	}
	if got := f.call(t, http.MethodPost, "/v1/facts", writer, map[string]any{"text": "x", "evidence": []string{" "}}, nil); got != http.StatusBadRequest {
		t.Errorf("facts without evidence = %d, want 400", got)
	}
	if got := f.call(t, http.MethodPost, "/v1/facts", f.runToken(t, []string{domain.ScopeClaimsWrite}, []string{"run-a"}), map[string]any{"text": "x", "evidence": []string{"y"}}, nil); got != http.StatusForbidden {
		t.Errorf("facts with a run-scoped token = %d, want 403", got)
	}
	var created factResponse
	if got := f.call(t, http.MethodPost, "/v1/facts", writer, map[string]any{
		"text": "The billing service runs in eu-west-1.", "evidence": []string{"runbook p.4"}, "valid_from": "2026-01-01",
	}, &created); got != http.StatusCreated || created.BeliefID == "" {
		t.Fatalf("facts = %d %+v", got, created)
	}
	c := f.stored(t, created.BeliefID)
	if c.Text != "The billing service runs in eu-west-1." || !c.ValidFrom.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("stored fact = %q valid_from %v", c.Text, c.ValidFrom)
	}
	links, err := f.conn.Claims.ListEvidenceByClaimIDs(context.Background(), []string{created.BeliefID})
	if err != nil || len(links) != 1 {
		t.Fatalf("evidence links = %+v, %v", links, err)
	}

	reader := f.token(t, "claims:read")
	var inWindow scanResponse
	if got := f.call(t, http.MethodGet, "/v1/scan?valid_from=2026-01-01&valid_until=2026-02-01", reader, nil, &inWindow); got != http.StatusOK {
		t.Fatalf("scan = %d", got)
	}
	if len(inWindow.Beliefs) != 1 || inWindow.Beliefs[0].ID != created.BeliefID {
		t.Errorf("scan in window = %+v", inWindow.Beliefs)
	}
	var before scanResponse
	f.call(t, http.MethodGet, "/v1/scan?valid_until=2025-12-31", reader, nil, &before)
	if len(before.Beliefs) != 0 {
		t.Errorf("scan before the fact = %+v, want none", before.Beliefs)
	}
	if got := f.call(t, http.MethodGet, "/v1/scan?valid_from=yesterday", reader, nil, nil); got != http.StatusBadRequest {
		t.Errorf("scan with a bad instant = %d, want 400", got)
	}
}

func TestServe_EntityAssociations(t *testing.T) {
	f := newBeliefOpsFixture(t)
	edge := map[string]any{"edges": []map[string]string{{
		"id": "er-1", "kind": "action_of", "from_id": "ac-1", "from_type": "action", "to_id": "oc-1", "to_type": "outcome",
	}}}
	if got := f.call(t, http.MethodPost, "/v1/entity-associations", f.token(t, domain.ScopeClaimsWrite), edge, nil); got != http.StatusForbidden {
		t.Errorf("append with claims:write = %d, want 403 (needs relationships:write)", got)
	}
	writer := f.token(t, domain.ScopeRelationshipsWrite)
	if got := f.call(t, http.MethodPost, "/v1/entity-associations", writer, map[string]any{"edges": []map[string]string{{"id": "bad"}}}, nil); got != http.StatusBadRequest {
		t.Errorf("append invalid edge = %d, want 400", got)
	}
	var accepted appendResponse
	if got := f.call(t, http.MethodPost, "/v1/entity-associations", writer, edge, &accepted); got != http.StatusOK || accepted.Accepted != 1 {
		t.Fatalf("append = %d %+v", got, accepted)
	}
	stored, err := f.conn.EntityRels.ListAll(context.Background())
	if err != nil || len(stored) != 1 || stored[0].CreatedBy != "agt_ops" {
		t.Fatalf("stored edges = %+v, %v", stored, err)
	}

	reader := f.token(t, "claims:read")
	for _, q := range []string{"", "?kind=action_of", "?entity_id=ac-1&entity_type=action"} {
		var list entityAssociationsResponse
		if got := f.call(t, http.MethodGet, "/v1/entity-associations"+q, reader, nil, &list); got != http.StatusOK || list.Total != 1 || list.Edges[0].ToID != "oc-1" {
			t.Errorf("list %q = %d %+v", q, got, list)
		}
	}
	if got := f.call(t, http.MethodGet, "/v1/entity-associations?entity_id=ac-1", reader, nil, nil); got != http.StatusBadRequest {
		t.Errorf("entity_id without entity_type = %d, want 400", got)
	}
}
