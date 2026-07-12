package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/auth"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
)

// allowlistToken mints a user token whose grant is tenant "acme" (default) plus
// an allowlist of {acme, repo_x}. Returns the token and a verifier over the same
// store so the surfaces can validate it.
func allowlistToken(t *testing.T) (tok string, verifier *auth.Verifier) {
	t.Helper()
	base, err := store.Open(context.Background(), "sqlite://"+t.TempDir()+"/t.db")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = base.Close() })
	secret, err := auth.GenerateSecret()
	if err != nil {
		t.Fatalf("secret: %v", err)
	}
	user := domain.User{ID: "u", Status: domain.UserStatusActive, CreatedAt: time.Now()}
	tok, _, err = auth.NewIssuer(secret).IssueUserTokenWithTenants(user, "acme", []string{"acme", "repo_x"}, time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	return tok, auth.NewVerifier(secret, base.RevokedTokens)
}

// TestREST_TenantSelectionAndDenial covers the REST surface's per-request tenant
// selection (ADR 0009): an allowed X-Mnemos-Tenant scopes the request; no
// selection uses the default; a tenant outside the grant is denied 401 BEFORE
// the handler runs. (gRPC has TestGRPC_TenantAllowlist_SelectionAndDenial.)
func TestREST_TenantSelectionAndDenial(t *testing.T) {
	tok, verifier := allowlistToken(t)

	var gotTenant string
	var handlerRan bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerRan = true
		if tn, ok := tenantFromContext(r.Context()); ok {
			gotTenant = tn
		}
		w.WriteHeader(http.StatusOK)
	})
	h := jwtAuthMiddleware(verifier, inner, true /* requireTenant */, false /* publicReads */, false /* metricsPublic */)

	do := func(selectTenant string) *httptest.ResponseRecorder {
		gotTenant, handlerRan = "", false
		req := httptest.NewRequest(http.MethodGet, "/v1/anything", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		if selectTenant != "" {
			req.Header.Set("X-Mnemos-Tenant", selectTenant)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}

	if rr := do("repo_x"); rr.Code != http.StatusOK || gotTenant != "repo_x" {
		t.Errorf("select repo_x: code=%d tenant=%q, want 200/repo_x", rr.Code, gotTenant)
	}
	if rr := do(""); rr.Code != http.StatusOK || gotTenant != "acme" {
		t.Errorf("no selection: code=%d tenant=%q, want 200/acme(default)", rr.Code, gotTenant)
	}
	if rr := do("forbidden"); rr.Code != http.StatusUnauthorized || handlerRan {
		t.Errorf("select forbidden: code=%d handlerRan=%v, want 401 pre-handler", rr.Code, handlerRan)
	}
	if rr := do("__default__"); rr.Code != http.StatusUnauthorized || handlerRan {
		t.Errorf("select reserved default: code=%d handlerRan=%v, want 401", rr.Code, handlerRan)
	}
}

// TestMCP_SelectedTenant covers the MCP-HTTP surface's tenant-selection decision
// (shared by both transport hooks): allowed selections resolve, a tenant outside
// the grant and the reserved default are denied.
func TestMCP_SelectedTenant(t *testing.T) {
	claims := &auth.Claims{Tenant: "acme", Tenants: []string{"acme", "repo_x"}}
	req := func(tenant string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		if tenant != "" {
			r.Header.Set("X-Mnemos-Tenant", tenant)
		}
		return r
	}
	cases := []struct {
		selectTenant string
		want         string
		ok           bool
	}{
		{"repo_x", "repo_x", true},
		{"acme", "acme", true},
		{"", "acme", true}, // default
		{"forbidden", "", false},
		{"__default__", "", false},
	}
	for _, c := range cases {
		got, ok := mcpSelectedTenant(claims, req(c.selectTenant))
		if got != c.want || ok != c.ok {
			t.Errorf("mcpSelectedTenant(select=%q) = (%q,%v), want (%q,%v)", c.selectTenant, got, ok, c.want, c.ok)
		}
	}
}
