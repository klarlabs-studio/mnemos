package main

import (
	"encoding/json"
	"net/http"
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
)

// ADR 0041 over REST: a new id restating a held belief is stored as a
// redirect to it, a re-post of that id stays a redirect, and the response
// says where each id went.
func TestServe_AppendClaims_ReportsRemapOntoHeldBelief(t *testing.T) {
	st := newServeJWTTest(t)
	post := func(id string) appendResponse {
		t.Helper()
		body := map[string]any{"beliefs": []map[string]any{{
			"id": id, "text": "the billing service retries failed charges three times",
			"type": "fact", "confidence": 0.8, "status": "active",
		}}}
		resp := postJSON(t, st.Srv.URL+"/v1/beliefs", body, st.Auth)
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("POST %s: status = %d, want 201", id, resp.StatusCode)
		}
		var ack appendResponse
		if err := json.NewDecoder(resp.Body).Decode(&ack); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return ack
	}

	if ack := post("cl_brain_a"); ack.Accepted != 1 || ack.Folded != 0 || len(ack.Remapped) != 0 {
		t.Fatalf("first write ack = %+v, want accepted=1 and no remap", ack)
	}
	ack := post("cl_brain_b")
	if ack.Accepted != 1 || ack.Folded != 1 || ack.Remapped["cl_brain_b"] != "cl_brain_a" {
		t.Fatalf("restatement ack = %+v, want folded=1 and cl_brain_b -> cl_brain_a", ack)
	}
	ack = post("cl_brain_b")
	if ack.Folded != 0 || ack.Remapped["cl_brain_b"] != "cl_brain_a" {
		t.Fatalf("re-post ack = %+v, want folded=0 and cl_brain_b -> cl_brain_a", ack)
	}
	if c := getClaim(t, st, "cl_brain_b"); c.Status != domain.ClaimStatusDeprecated || c.MergedInto != "cl_brain_a" {
		t.Fatalf("cl_brain_b = %s/%q, want a deprecated redirect to cl_brain_a", c.Status, c.MergedInto)
	}
	if c := getClaim(t, st, "cl_brain_a"); c.Status != domain.ClaimStatusActive {
		t.Fatalf("cl_brain_a = %s, want active", c.Status)
	}
}
