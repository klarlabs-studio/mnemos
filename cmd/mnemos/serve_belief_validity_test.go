package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// A record of a dated observation stays true, so its writer must be able to
// say it decays slowly; a state claim ends, so its writer must be able to
// close it. Neither was possible over REST: the wire had no half-life and no
// validity, and every such belief decayed on the default constant.
func TestServe_AppendClaims_SetsHalfLifeAndValidity(t *testing.T) {
	st := newServeJWTTest(t)
	from := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	post := func(belief map[string]any) {
		t.Helper()
		resp := postJSON(t, st.Srv.URL+"/v1/beliefs", map[string]any{"beliefs": []map[string]any{belief}}, st.Auth)
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusCreated {
			var msg errorResponse
			_ = json.NewDecoder(resp.Body).Decode(&msg)
			t.Fatalf("status = %d, want 201 (%v)", resp.StatusCode, msg.Error)
		}
	}
	belief := map[string]any{
		"id": "cl_condition", "text": "Condition diagnosed on 2026-03-01: otitis externa",
		"type": "fact", "confidence": 0.9, "status": "active",
		"valid_from": from.Format(time.RFC3339), "half_life_days": 3650.0,
	}
	post(belief)

	c := getClaim(t, st, "cl_condition")
	if c.HalfLifeDays != 3650 || !c.ValidFrom.Equal(from) || !c.ValidTo.IsZero() {
		t.Fatalf("half_life=%v valid_from=%v valid_to=%v; want 3650, %v, open", c.HalfLifeDays, c.ValidFrom, c.ValidTo, from)
	}

	// Re-posting the same belief with valid_to closes it.
	to := from.Add(21 * 24 * time.Hour)
	belief["valid_to"] = to.Format(time.RFC3339)
	post(belief)
	c = getClaim(t, st, "cl_condition")
	if !c.ValidTo.Equal(to) || c.HalfLifeDays != 3650 {
		t.Errorf("after close: valid_to=%v half_life=%v; want %v, 3650", c.ValidTo, c.HalfLifeDays, to)
	}
}

func TestServe_AppendClaims_RejectsBadHalfLifeAndValidity(t *testing.T) {
	st := newServeJWTTest(t)
	for name, extra := range map[string]map[string]any{
		"negative half-life":     {"half_life_days": -1.0},
		"valid_to before from":   {"valid_from": "2026-03-02T00:00:00Z", "valid_to": "2026-03-01T00:00:00Z"},
		"unparseable valid_to":   {"valid_to": "soon"},
		"unparseable valid_from": {"valid_from": "yesterday"},
	} {
		belief := map[string]any{"id": "cl_bad", "text": "x", "type": "fact", "confidence": 0.5, "status": "active"}
		for k, v := range extra {
			belief[k] = v
		}
		resp := postJSON(t, st.Srv.URL+"/v1/beliefs", map[string]any{"beliefs": []map[string]any{belief}}, st.Auth)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, resp.StatusCode)
		}
	}
}

func getClaim(t *testing.T, st serveJWTBase, id string) domain.Claim {
	t.Helper()
	cs, err := st.Conn.Claims.ListByIDs(context.Background(), []string{id})
	if err != nil || len(cs) != 1 {
		t.Fatalf("ListByIDs(%s) = %d, %v", id, len(cs), err)
	}
	return cs[0]
}

func TestServe_ListClaims_EchoesHalfLifeAndValidity(t *testing.T) {
	st := newServeJWTTest(t)
	resp := postJSON(t, st.Srv.URL+"/v1/beliefs", map[string]any{"beliefs": []map[string]any{{
		"id": "cl_dated", "text": "Weight on 2026-03-01: 4.22 kg", "type": "fact", "confidence": 0.9, "status": "active",
		"valid_from": "2026-03-01T09:00:00Z", "valid_to": "2026-03-02T09:00:00Z", "half_life_days": 3650.0,
	}}}, st.Auth)
	_ = resp.Body.Close()
	req, _ := http.NewRequest(http.MethodGet, st.Srv.URL+"/v1/beliefs", nil)
	req.Header.Set("Authorization", st.Auth["Authorization"])
	got, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = got.Body.Close() }()
	var body claimsResponse
	if err := json.NewDecoder(got.Body).Decode(&body); err != nil || len(body.Claims) != 1 {
		t.Fatalf("decode: %v, %d claims", err, len(body.Claims))
	}
	c := body.Claims[0]
	if c.ValidFrom != "2026-03-01T09:00:00Z" || c.ValidTo != "2026-03-02T09:00:00Z" || c.HalfLifeDays != 3650 {
		t.Errorf("listed belief = %+v", c)
	}
}
