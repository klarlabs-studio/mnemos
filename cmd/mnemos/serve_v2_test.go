package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/auth"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
)

// issueScopedToken mints a JWT carrying explicit write scopes so the v2 write
// paths (which reuse v1's requireScope) accept the request.
func issueScopedToken(t *testing.T, secret []byte, userID string, scopes ...string) string {
	t.Helper()
	tok, _, err := auth.NewIssuer(secret).IssueUserToken(domain.User{
		ID:        userID,
		Scopes:    scopes,
		Status:    domain.UserStatusActive,
		CreatedAt: time.Now().UTC(),
	}, time.Hour)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	return "Bearer " + tok
}

func seedClaimV2(t *testing.T, conn *store.Conn, id, text, ctype string) {
	t.Helper()
	c := domain.Claim{
		ID:         id,
		Text:       text,
		Type:       domain.ClaimType(ctype),
		Confidence: 0.9,
		Status:     domain.ClaimStatusActive,
		CreatedAt:  time.Now().UTC(),
		CreatedBy:  domain.SystemUser,
	}
	if err := conn.Claims.Upsert(context.Background(), []domain.Claim{c}); err != nil {
		t.Fatalf("seed claim: %v", err)
	}
}

// TestServeV2_BeliefsMirrorClaims proves GET /v2/beliefs returns the same data
// as GET /v1/claims under brain-native field names.
func TestServeV2_BeliefsMirrorClaims(t *testing.T) {
	_, conn := openTestStore(t)
	seedClaimV2(t, conn, "cl_1", "the sky is blue", "fact")
	seedClaimV2(t, conn, "cl_2", "it will rain", "hypothesis")

	srv := httptest.NewServer(newServerMux(conn))
	defer srv.Close()

	// v1
	v1Resp := getJSON(t, srv.URL+"/v1/claims", nil)
	defer func() { _ = v1Resp.Body.Close() }()
	var v1 claimsResponse
	if err := json.NewDecoder(v1Resp.Body).Decode(&v1); err != nil {
		t.Fatalf("decode v1: %v", err)
	}

	// v2
	v2Resp := getJSON(t, srv.URL+"/v2/beliefs", nil)
	defer func() { _ = v2Resp.Body.Close() }()
	if v2Resp.StatusCode != http.StatusOK {
		t.Fatalf("v2 status = %d", v2Resp.StatusCode)
	}
	var v2 beliefsResponseV2
	if err := json.NewDecoder(v2Resp.Body).Decode(&v2); err != nil {
		t.Fatalf("decode v2: %v", err)
	}

	if len(v2.Beliefs) != len(v1.Claims) || len(v2.Beliefs) != 2 {
		t.Fatalf("belief count = %d, want %d (=2)", len(v2.Beliefs), len(v1.Claims))
	}
	// Same underlying data, mapped 1:1.
	for i := range v1.Claims {
		if v2.Beliefs[i].ID != v1.Claims[i].ID || v2.Beliefs[i].Text != v1.Claims[i].Text {
			t.Errorf("belief[%d] = %+v, want same as claim %+v", i, v2.Beliefs[i], v1.Claims[i])
		}
	}

	// The raw v2 body must use the brain key "beliefs", never "claims".
	rawResp := getJSON(t, srv.URL+"/v2/beliefs", nil)
	defer func() { _ = rawResp.Body.Close() }()
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(rawResp.Body).Decode(&raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	if _, ok := raw["beliefs"]; !ok {
		t.Errorf("v2 body missing brain key \"beliefs\"; keys=%v", keysOf(raw))
	}
	if _, ok := raw["claims"]; ok {
		t.Errorf("v2 body leaked v1 key \"claims\"")
	}
}

// TestServeV2_EpisodesMirrorEvents proves GET /v2/episodes mirrors /v1/events.
func TestServeV2_EpisodesMirrorEvents(t *testing.T) {
	_, conn := openTestStore(t)
	now := time.Now().UTC()
	seedEventConn(t, conn, "ev_1", "run:a", "hello", "src", "{}", now)

	srv := httptest.NewServer(newServerMux(conn))
	defer srv.Close()

	resp := getJSON(t, srv.URL+"/v2/episodes", nil)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := raw["episodes"]; !ok {
		t.Errorf("missing brain key \"episodes\"; keys=%v", keysOf(raw))
	}
	if _, ok := raw["events"]; ok {
		t.Errorf("leaked v1 key \"events\"")
	}
	var body episodesResponseV2
	if err := json.Unmarshal(mustJSON(t, raw), &body); err != nil {
		t.Fatalf("remarshal: %v", err)
	}
	if len(body.Episodes) != 1 || body.Episodes[0].ID != "ev_1" {
		t.Fatalf("episodes = %+v, want one ev_1", body.Episodes)
	}
}

// TestServeV2_AssociationsRenameCrossRefs proves association edges rename the
// belief cross-references (from_belief_id / to_belief_id).
func TestServeV2_AssociationsRenameCrossRefs(t *testing.T) {
	_, conn := openTestStore(t)
	seedClaimV2(t, conn, "cl_a", "a", "fact")
	seedClaimV2(t, conn, "cl_b", "b", "fact")
	if err := conn.Relationships.Upsert(context.Background(), []domain.Relationship{{
		ID:          "rel_1",
		Type:        domain.RelationshipTypeSupports,
		FromClaimID: "cl_a",
		ToClaimID:   "cl_b",
		CreatedAt:   time.Now().UTC(),
		CreatedBy:   domain.SystemUser,
	}}); err != nil {
		t.Fatalf("seed relationship: %v", err)
	}

	srv := httptest.NewServer(newServerMux(conn))
	defer srv.Close()

	resp := getJSON(t, srv.URL+"/v2/associations", nil)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body associationsResponseV2
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Associations) != 1 {
		t.Fatalf("associations = %d, want 1", len(body.Associations))
	}
	a := body.Associations[0]
	if a.FromBeliefID != "cl_a" || a.ToBeliefID != "cl_b" {
		t.Errorf("cross-refs = (%s,%s), want (cl_a,cl_b)", a.FromBeliefID, a.ToBeliefID)
	}
}

// TestServeV2_MetricsBrainNames proves /v2/metrics carries brain-named counts.
func TestServeV2_MetricsBrainNames(t *testing.T) {
	_, conn := openTestStore(t)
	seedClaimV2(t, conn, "cl_x", "x", "fact")
	now := time.Now().UTC()
	seedEventConn(t, conn, "ev_x", "run:x", "x", "src", "{}", now)

	srv := httptest.NewServer(newServerMux(conn))
	defer srv.Close()

	resp := getJSON(t, srv.URL+"/v2/metrics", nil)
	defer func() { _ = resp.Body.Close() }()
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, k := range []string{"episodes", "beliefs", "associations", "dissonances", "contested_beliefs"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("metrics missing brain key %q; keys=%v", k, keysOf(raw))
		}
	}
	for _, k := range []string{"events", "claims", "relationships", "contradictions", "contested_claims"} {
		if _, ok := raw[k]; ok {
			t.Errorf("metrics leaked v1 key %q", k)
		}
	}
}

// TestServeV2_WriteBeliefRoundTrips proves a POST /v2/beliefs with brain-named
// JSON persists exactly like a v1 claim write (readable back via /v1/claims).
func TestServeV2_WriteBeliefRoundTrips(t *testing.T) {
	secret, _ := setupJWTTestEnv(t)
	_, conn := openTestStore(t)
	hdr := map[string]string{"Authorization": issueScopedToken(t, secret, "usr_w", domain.ScopeClaimsWrite)}

	srv := httptest.NewServer(newServerMux(conn))
	defer srv.Close()

	body := map[string]any{
		"beliefs": []map[string]any{{
			"id": "cl_v2", "text": "written via v2", "type": "fact", "status": "active",
		}},
	}
	post := postJSON(t, srv.URL+"/v2/beliefs", body, hdr)
	_ = post.Body.Close()
	if post.StatusCode != http.StatusCreated && post.StatusCode != http.StatusOK {
		t.Fatalf("v2 write status = %d", post.StatusCode)
	}

	// Read back through v1 — the storage is shared and v1-shaped.
	got := getJSON(t, srv.URL+"/v1/claims", nil)
	defer func() { _ = got.Body.Close() }()
	var out claimsResponse
	if err := json.NewDecoder(got.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	found := false
	for _, c := range out.Claims {
		if c.ID == "cl_v2" && c.Text == "written via v2" {
			found = true
		}
	}
	if !found {
		t.Fatalf("v2-written belief not readable via v1 /v1/claims: %+v", out.Claims)
	}
}

// TestServeV2_SchemasSurfaceNeocortex proves GET /v2/schemas returns promoted
// global schemas under brain-native names (the neocortex read surface).
func TestServeV2_SchemasSurfaceNeocortex(t *testing.T) {
	_, conn := openTestStore(t)
	if conn.GlobalSchemas == nil {
		t.Skip("sqlite backend has no GlobalSchemas store")
	}
	if err := conn.GlobalSchemas.Upsert(context.Background(), domain.GlobalSchema{
		ID:              "gs_1",
		Statement:       "restart the pod when memory climbs past 90%",
		Scope:           domain.Context{Service: "api", Env: "prod"},
		DistinctTenants: 3,
		EvidenceCount:   12,
		Confidence:      0.88,
		Status:          domain.GlobalSchemaStatusActive,
		PromotedAt:      time.Now().UTC(),
		CreatedBy:       domain.SystemUser,
	}); err != nil {
		t.Fatalf("seed global schema: %v", err)
	}

	srv := httptest.NewServer(newServerMux(conn))
	defer srv.Close()

	resp := getJSON(t, srv.URL+"/v2/schemas", nil)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body schemasResponseV2
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Total != 1 || len(body.Schemas) != 1 {
		t.Fatalf("schemas = %+v, want 1", body.Schemas)
	}
	s := body.Schemas[0]
	if s.ID != "gs_1" || s.DistinctTenants != 3 || s.Context.Service != "api" {
		t.Errorf("schema = %+v, want gs_1/api/3 tenants", s)
	}
}

// TestServeV2_MapperRoundTrips exercises the pure mappers directly.
func TestServeV2_MapperRoundTrips(t *testing.T) {
	// belief evidence cross-ref rename round-trips.
	v1In := claimsResponse{
		Claims:   []claimDTO{{ID: "c1", Text: "t"}},
		Evidence: []claimEvidenceItem{{ClaimID: "c1", EventID: "e1"}},
		Total:    1, Limit: 50,
	}
	raw := mustJSON(t, v1In)
	out, err := remapClaimsToBeliefs(raw)
	if err != nil {
		t.Fatalf("remap: %v", err)
	}
	b := out.(beliefsResponseV2)
	if len(b.Evidence) != 1 || b.Evidence[0].BeliefID != "c1" || b.Evidence[0].EpisodeID != "e1" {
		t.Fatalf("evidence remap = %+v", b.Evidence)
	}

	// association request maps back to relationship request keys.
	reqBody := mustJSON(t, appendAssociationsRequestV2{Associations: []associationDTO{
		{ID: "r1", Type: "supports", FromBeliefID: "a", ToBeliefID: "b", CreatedAt: ""},
	}})
	v1Bytes, err := remapAssociationsReqToRelationships(reqBody)
	if err != nil {
		t.Fatalf("remap req: %v", err)
	}
	var v1Req appendRelationshipsRequest
	if err := json.Unmarshal(v1Bytes, &v1Req); err != nil {
		t.Fatalf("decode mapped: %v", err)
	}
	if v1Req.Relationships[0].FromClaimID != "a" || v1Req.Relationships[0].ToClaimID != "b" {
		t.Fatalf("association→relationship remap = %+v", v1Req.Relationships[0])
	}
}

// TestAPISurfaceParity_V2 is the v2 counterpart of TestAPISurfaceParity: it
// asserts that every v1 core resource has a brain-named v2 equivalent, and that
// the v1 routes it aliases still exist (i.e. v1 was NOT renamed away).
func TestAPISurfaceParity_V2(t *testing.T) {
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("locate repo root: %v", err)
	}

	v1Routes := readRoutes(t, filepath.Join(root, "cmd/mnemos/serve.go"))
	v2Routes := readRoutes(t, filepath.Join(root, "cmd/mnemos/serve_v2.go"))

	// The brain-native rename map: each v1 core resource → its v2 alias.
	pairs := []struct{ v1, v2 string }{
		{"/v1/events", "/v2/episodes"},
		{"/v1/claims", "/v2/beliefs"},
		{"/v1/relationships", "/v2/associations"},
		{"/v1/metrics", "/v2/metrics"},
	}
	for _, p := range pairs {
		if !v1Routes[p.v1] {
			t.Errorf("v1 route %q missing — v1 must remain byte-identical (never renamed away)", p.v1)
		}
		if !v2Routes[p.v2] {
			t.Errorf("v2 brain-native route %q missing for v1 %q", p.v2, p.v1)
		}
	}

	// Guard against an un-mapped v2 route sneaking in. Known routes are either a
	// recorded v1 alias (pairs) or a v2-only brain-native resource (no v1
	// equivalent by design — e.g. the neocortex /v2/schemas).
	v2Only := map[string]bool{
		"/v2/schemas": true, // ADR 0011 neocortex (domain.GlobalSchema); new, no v1 alias.
	}
	known := map[string]bool{}
	for _, p := range pairs {
		known[p.v2] = true
	}
	for r := range v2Routes {
		if !known[r] && !v2Only[r] {
			t.Errorf("v2 route %q has no recorded v1 counterpart in TestAPISurfaceParity_V2 — add the pair", r)
		}
	}
}

var routePattern = regexp.MustCompile(`(?m)mux\.Handle(?:Func)?\("(/[^"]*)"`)

func readRoutes(t *testing.T, path string) map[string]bool {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	out := map[string]bool{}
	for _, m := range routePattern.FindAllSubmatch(src, -1) {
		out[string(m[1])] = true
	}
	return out
}

// --- small test helpers ---

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
