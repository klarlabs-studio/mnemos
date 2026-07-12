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

// readAuthFixture mints a valid user token plus a verifier over the same store,
// for exercising the read-auth gate in single-tenant mode.
func readAuthFixture(t *testing.T) (tok string, verifier *auth.Verifier) {
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
	tok, _, err = auth.NewIssuer(secret).IssueUserToken(user, time.Hour)
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	return tok, auth.NewVerifier(secret, base.RevokedTokens)
}

// TestREST_ReadsRequireAuthByDefault is the secure-by-default guardrail: in
// single-tenant mode an anonymous GET is rejected (401) unless the operator
// opted into a public read API, and infra endpoints stay open regardless.
func TestREST_ReadsRequireAuthByDefault(t *testing.T) {
	tok, verifier := readAuthFixture(t)

	var handlerRan bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerRan = true
		w.WriteHeader(http.StatusOK)
	})

	get := func(h http.Handler, path, bearer string) (*httptest.ResponseRecorder, bool) {
		handlerRan = false
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr, handlerRan
	}

	// Secure default (publicReads=false): anonymous read is denied 401 pre-handler.
	secure := jwtAuthMiddleware(verifier, inner, false /* requireTenant */, false /* publicReads */)
	if rr, ran := get(secure, "/v1/schemas", ""); rr.Code != http.StatusUnauthorized || ran {
		t.Errorf("secure default anon GET /v1/schemas: code=%d ran=%v, want 401 pre-handler", rr.Code, ran)
	}
	if rr, ran := get(secure, "/v1/beliefs", ""); rr.Code != http.StatusUnauthorized || ran {
		t.Errorf("secure default anon GET /v1/beliefs: code=%d ran=%v, want 401 pre-handler", rr.Code, ran)
	}
	// A valid token is accepted under the secure default.
	if rr, ran := get(secure, "/v1/beliefs", tok); rr.Code != http.StatusOK || !ran {
		t.Errorf("secure default authed GET: code=%d ran=%v, want 200/handler-ran", rr.Code, ran)
	}
	// Infra endpoints never require a token, even secure.
	if rr, ran := get(secure, "/health", ""); rr.Code != http.StatusOK || !ran {
		t.Errorf("secure /health anon: code=%d ran=%v, want 200 (infra open)", rr.Code, ran)
	}

	// Opt-in public reads: anonymous GET passes.
	public := jwtAuthMiddleware(verifier, inner, false /* requireTenant */, true /* publicReads */)
	if rr, ran := get(public, "/v1/schemas", ""); rr.Code != http.StatusOK || !ran {
		t.Errorf("public-reads anon GET: code=%d ran=%v, want 200/handler-ran", rr.Code, ran)
	}
}
