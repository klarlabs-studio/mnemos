package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

var (
	scriptTagRE  = regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script>`)
	styleBlockRE = regexp.MustCompile(`(?is)<style\b`)
	styleAttrRE  = regexp.MustCompile(`(?is)<[a-z][^>]*\sstyle\s*=`)
	handlerRE    = regexp.MustCompile(`(?is)<[a-z][^>]*\son[a-z]+\s*=`)
	assetRefRE   = regexp.MustCompile(`(?is)(?:<script[^>]*\ssrc|<link[^>]*\shref)="(/assets/[^"]+)"`)
)

// The CSP forbids inline code, and the pages contain none. The two facts only
// protect anything together: 'unsafe-inline' was in the policy because the
// landing page's lead form and the whole /app SPA were inline <script> blocks,
// so the policy could not be tightened without breaking them, and nothing
// checked either side. This pins the exact policy AND reads the HTML the server
// actually serves for any inline script, <style>, style="…" or on…= handler,
// then loads every asset the pages reference — anonymously, as a browser
// would — so a page cannot be left pointing at a file the server refuses.
func TestServe_CSPForbidsInlineCode(t *testing.T) {
	srv := httptest.NewServer(newServerMux(newServerTestStore_conn(t)))
	defer srv.Close()

	if strings.Contains(contentSecurityPolicy, "unsafe-inline") || strings.Contains(contentSecurityPolicy, "unsafe-eval") {
		t.Fatalf("CSP allows inline or eval'd code: %s", contentSecurityPolicy)
	}
	for _, d := range []string{"script-src 'self';", "style-src 'self';"} {
		if !strings.Contains(contentSecurityPolicy, d) {
			t.Fatalf("CSP lost %q: %s", d, contentSecurityPolicy)
		}
	}

	assets := map[string]bool{}
	for _, page := range []string{"/", "/app"} {
		resp, err := http.Get(srv.URL + page)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if got := resp.Header.Get("Content-Security-Policy"); got != contentSecurityPolicy {
			t.Errorf("%s CSP = %q, want the pinned policy", page, got)
		}
		html := string(body)
		for _, m := range scriptTagRE.FindAllStringSubmatch(html, -1) {
			attrs, inner := m[1], strings.TrimSpace(m[2])
			if strings.Contains(attrs, `type="application/ld+json"`) {
				continue // a data block: CSP does not execute it
			}
			if inner != "" || !strings.Contains(attrs, "src=") {
				t.Errorf("%s has an inline <script>%s", page, attrs)
			}
		}
		if styleBlockRE.MatchString(html) {
			t.Errorf("%s has an inline <style> block", page)
		}
		if loc := styleAttrRE.FindString(html); loc != "" {
			t.Errorf("%s has a style= attribute: %s", page, loc)
		}
		if loc := handlerRE.FindString(html); loc != "" {
			t.Errorf("%s has an inline event handler: %s", page, loc)
		}
		for _, m := range assetRefRE.FindAllStringSubmatch(html, -1) {
			assets[m[1]] = true
		}
	}

	if len(assets) != len(webAssetRoutes) {
		t.Errorf("pages reference %d assets, server routes %d: %v", len(assets), len(webAssetRoutes), assets)
	}
	for path := range assets {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK || len(body) == 0 {
			t.Errorf("GET %s = %d (%d bytes) without a token; a public page's asset must load anonymously", path, resp.StatusCode, len(body))
			continue
		}
		want := "text/css"
		if strings.HasSuffix(path, ".js") {
			want = "text/javascript"
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, want) {
			t.Errorf("GET %s Content-Type = %q, want %s (nosniff makes a wrong type fatal)", path, ct, want)
		}
	}
}

// The pages are public, so the files they load must be too. Under the real
// JWT middleware (secure defaults, multi-tenant on) an anonymous request for
// every asset reaches its handler, while a data route next to them is still
// refused — so the bypass is the asset list, not a hole.
func TestAuth_PageAssetsArePublicLikeThePages(t *testing.T) {
	_, verifier := readAuthFixture(t)
	ran := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		ran = true
		w.WriteHeader(http.StatusOK)
	})
	h := jwtAuthMiddleware(verifier, inner, true /* requireTenant */, false /* publicReads */, false /* metricsPublic */)
	for path := range webAssetRoutes {
		ran = false
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusOK || !ran {
			t.Errorf("anonymous GET %s: code=%d handler ran=%v; the page that loads it is public", path, rr.Code, ran)
		}
	}
	ran = false
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/assets/../v1/beliefs", nil))
	if ran {
		t.Error("a path outside the asset list reached the handler anonymously")
	}
	ran = false
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/beliefs", nil))
	if rr.Code != http.StatusUnauthorized || ran {
		t.Errorf("anonymous GET /v1/beliefs: code=%d ran=%v, want 401", rr.Code, ran)
	}
}
