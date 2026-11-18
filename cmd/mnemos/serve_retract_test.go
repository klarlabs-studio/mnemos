package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
)

func (f runScopeFixture) retract(t *testing.T, episodeID, token string, body any) *http.Response {
	t.Helper()
	return postJSON(t, f.srv.URL+"/v1/episodes/"+episodeID+"/retract", body, map[string]string{"Authorization": "Bearer " + token})
}

// POST /v1/episodes/{id}/retract returns the same retraction.Report the CLI
// and MCP paths return, retracts the beliefs the episode alone supported, and
// attributes the change to the token's subject.
func TestREST_RetractEpisode_SharesTheCLIReport(t *testing.T) {
	f := newRunScopeFixture(t)
	f.seedRun(t, "run-a", "ev-a", "cl-a")
	tok := f.openToken(t, []string{domain.ScopeClaimsWrite})

	resp := f.retract(t, "ev-a", tok, map[string]string{"reason": "wrong runbook"})
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got retractionView
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.EpisodeID != "ev-a" || got.AlreadyRetracted || got.Reason != "wrong runbook" || got.RetractedBy != "agt_open" {
		t.Errorf("report = %+v, want a first retraction of ev-a by agt_open", got)
	}
	if b := got.Impact.RetractedBeliefs; len(b) != 1 || b[0] != "cl-a" {
		t.Errorf("retracted beliefs = %v, want [cl-a]", b)
	}

	gw, err := govwrite.Wrap(f.conn, nil)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	viaCLI, err := runRetraction(context.Background(), gw, "ev-a", "", "operator", nil)
	if err != nil {
		t.Fatalf("CLI path: %v", err)
	}
	if !viaCLI.AlreadyRetracted {
		t.Error("CLI path did not see the REST retraction")
	}
	viaCLI.AlreadyRetracted = false
	if !reflect.DeepEqual(viaCLI, got) {
		t.Errorf("CLI report %+v differs from REST report %+v", viaCLI, got)
	}
}

func TestREST_RetractEpisode_RefusalsAreClean(t *testing.T) {
	f := newRunScopeFixture(t)
	f.seedRun(t, "run-a", "ev-a", "cl-a")
	f.seedRun(t, "run-b", "ev-b", "cl-b")

	cases := []struct {
		name    string
		episode string
		token   string
		body    any
		method  string
		want    int
	}{
		{"read-only token", "ev-a", f.openToken(t, []string{"claims:read"}), map[string]string{}, http.MethodPost, http.StatusForbidden},
		{"unknown episode", "ev-missing", f.openToken(t, []string{domain.ScopeClaimsWrite}), map[string]string{}, http.MethodPost, http.StatusNotFound},
		{"episode in another run", "ev-b", f.runScopedToken(t, []string{domain.ScopeClaimsWrite}, []string{"run-a"}), map[string]string{}, http.MethodPost, http.StatusNotFound},
		{"unknown field", "ev-a", f.openToken(t, []string{domain.ScopeClaimsWrite}), map[string]string{"why": "x"}, http.MethodPost, http.StatusBadRequest},
		{"GET", "ev-a", f.openToken(t, []string{domain.ScopeClaimsWrite}), nil, http.MethodGet, http.StatusMethodNotAllowed},
	}
	for _, tc := range cases {
		var code int
		if tc.method == http.MethodPost {
			resp := f.retract(t, tc.episode, tc.token, tc.body)
			code = resp.StatusCode
			_ = resp.Body.Close()
		} else {
			code = f.status(t, tc.method, "/v1/episodes/"+tc.episode+"/retract", tc.token)
		}
		if code != tc.want {
			t.Errorf("%s: status = %d, want %d", tc.name, code, tc.want)
		}
	}
	for _, id := range []string{"cl-a", "cl-b"} {
		got, err := f.conn.Claims.ListByIDs(context.Background(), []string{id})
		if err != nil || len(got) != 1 || got[0].Status == domain.ClaimStatusDeprecated {
			t.Errorf("%s after refused retractions = %+v (err %v), want untouched", id, got, err)
		}
	}
}

// A body is optional: the reason is.
func TestREST_RetractEpisode_EmptyBody(t *testing.T) {
	f := newRunScopeFixture(t)
	f.seedRun(t, "run-a", "ev-a", "cl-a")
	if got := f.status(t, http.MethodPost, "/v1/episodes/ev-a/retract", f.openToken(t, []string{domain.ScopeClaimsWrite})); got != http.StatusOK {
		t.Errorf("empty body: status = %d, want 200", got)
	}
}

// The handler writes through the request's tenant writer (scopedWriter), not
// the process-wide one it was built with.
func TestREST_RetractEpisode_UsesTheTenantWriter(t *testing.T) {
	_, shared := openTestStore(t)
	_, tenant := openTestStore(t)
	seedEventConn(t, tenant, "ev-t", "run-t", "tenant content", "src-t", "{}", time.Now().UTC())
	sharedW, err := govwrite.Wrap(shared, nil)
	if err != nil {
		t.Fatalf("wrap shared: %v", err)
	}
	tenantW, err := govwrite.Wrap(tenant, nil)
	if err != nil {
		t.Fatalf("wrap tenant: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/episodes/ev-t/retract", strings.NewReader(`{}`))
	ctx := withReqWriter(req.Context(), tenantW)
	ctx = context.WithValue(ctx, scopesContextKey{}, []string{domain.ScopeClaimsWrite})
	rr := httptest.NewRecorder()
	makeEpisodeSubresourceHandler(sharedW)(rr, req.WithContext(ctx))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d (%s), want 200 from the tenant store", rr.Code, rr.Body.String())
	}
	if _, ok, err := govwrite.LookupRetraction(context.Background(), tenant, "ev-t"); err != nil || !ok {
		t.Errorf("tenant store has no retraction (ok=%v err=%v)", ok, err)
	}
}
