package main

import (
	"context"
	"encoding/json"
	"io"
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
	secure := jwtAuthMiddleware(verifier, inner, false /* requireTenant */, false /* publicReads */, false /* metricsPublic */)
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
	// Bare-liveness infra endpoints never require a token, even secure.
	for _, p := range []string{"/health", "/healthz"} {
		if rr, ran := get(secure, p, ""); rr.Code != http.StatusOK || !ran {
			t.Errorf("secure %s anon: code=%d ran=%v, want 200 (liveness open)", p, rr.Code, ran)
		}
	}
	// Prometheus metrics require a token by default — it's NOT a bare-liveness
	// infra endpoint, and public-reads does not cover it.
	if rr, ran := get(secure, "/internal/metrics", ""); rr.Code != http.StatusUnauthorized || ran {
		t.Errorf("secure default anon GET /internal/metrics: code=%d ran=%v, want 401 pre-handler", rr.Code, ran)
	}
	if rr, ran := get(secure, "/internal/metrics", tok); rr.Code != http.StatusOK || !ran {
		t.Errorf("secure authed GET /internal/metrics: code=%d ran=%v, want 200/handler-ran", rr.Code, ran)
	}

	// Opt-in public reads: anonymous GET passes for data reads...
	public := jwtAuthMiddleware(verifier, inner, false /* requireTenant */, true /* publicReads */, false /* metricsPublic */)
	if rr, ran := get(public, "/v1/schemas", ""); rr.Code != http.StatusOK || !ran {
		t.Errorf("public-reads anon GET: code=%d ran=%v, want 200/handler-ran", rr.Code, ran)
	}
	// ...but public-reads must NOT open /internal/metrics — only --metrics-public does.
	if rr, ran := get(public, "/internal/metrics", ""); rr.Code != http.StatusUnauthorized || ran {
		t.Errorf("public-reads anon GET /internal/metrics: code=%d ran=%v, want 401 (metrics needs --metrics-public)", rr.Code, ran)
	}

	// Opt-in metrics-public: anonymous GET /internal/metrics passes.
	metricsOpen := jwtAuthMiddleware(verifier, inner, false /* requireTenant */, false /* publicReads */, true /* metricsPublic */)
	if rr, ran := get(metricsOpen, "/internal/metrics", ""); rr.Code != http.StatusOK || !ran {
		t.Errorf("metrics-public anon GET /internal/metrics: code=%d ran=%v, want 200/handler-ran", rr.Code, ran)
	}
	// metrics-public does not weaken data reads: they still require a token.
	if rr, ran := get(metricsOpen, "/v1/beliefs", ""); rr.Code != http.StatusUnauthorized || ran {
		t.Errorf("metrics-public anon GET /v1/beliefs: code=%d ran=%v, want 401 (data reads still authed)", rr.Code, ran)
	}
}

// TestHealth_BareLivenessNoSensitiveFields asserts the anonymous /health(z)
// response is a bare {"status":"ok"} — no version, db path, counts, or tenant
// info leaks to an unauthenticated probe.
func TestHealth_BareLivenessNoSensitiveFields(t *testing.T) {
	_, conn := openTestStore(t)
	srv := httptest.NewServer(newServerMux(conn))
	defer srv.Close()

	for _, path := range []string{"/health", "/healthz"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("get %s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", path, resp.StatusCode)
		}
		var m map[string]any
		if err := json.Unmarshal(body, &m); err != nil {
			t.Fatalf("%s decode: %v (body=%s)", path, err, body)
		}
		if m["status"] != "ok" {
			t.Errorf("%s status = %v, want ok", path, m["status"])
		}
		// Bare liveness: nothing beyond status. Explicitly reject the fields the
		// old handler leaked.
		for _, k := range []string{"version", "healthy", "checks", "db", "database", "tenant"} {
			if _, ok := m[k]; ok {
				t.Errorf("%s leaked field %q in bare liveness body: %s", path, k, body)
			}
		}
		if len(m) != 1 {
			t.Errorf("%s body has %d fields, want exactly 1 (status): %s", path, len(m), body)
		}
	}
}
