package client_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	client "go.klarlabs.de/mnemos/client"
)

// The wire shape is pinned end to end against a real serve + SQLite in
// cmd/mnemos (TestClient_RetractEvent_AgainstServe); this covers the request
// the client builds and how it surfaces a 404.
func TestRetractEvent_RequestAndNotFound(t *testing.T) {
	var gotPath, gotReason string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.Method + " " + r.URL.EscapedPath()
		if r.URL.Path == "/v1/episodes/ev missing/retract" {
			http.Error(w, `{"error":"episode not found"}`, http.StatusNotFound)
			return
		}
		var b struct {
			Reason string `json:"reason"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		gotReason = b.Reason
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"episode_id":"ev1","retracted_at":"2026-11-18T10:00:00Z","retracted_by":"agt","reason":"stale",
			"already_retracted":false,"impact":{"retracted_beliefs":["cl1"],"rescored_beliefs":[{"id":"cl2","trust_before":0.8,"trust_after":0.5,"remaining_evidence":1}],
			"decisions":[{"decision_id":"d1","beliefs":["cl1"]}],"uncertainty":["u"]}}`))
	}))
	t.Cleanup(srv.Close)
	c := client.New(srv.URL)

	got, err := c.RetractEvent(context.Background(), "ev1", "stale")
	if err != nil {
		t.Fatalf("RetractEvent: %v", err)
	}
	if gotPath != "POST /v1/episodes/ev1/retract" || gotReason != "stale" {
		t.Errorf("request = %q reason %q", gotPath, gotReason)
	}
	if got.EventID != "ev1" || len(got.Impact.RetractedClaims) != 1 || got.Impact.Rescored[0].RemainingEvidence != 1 ||
		got.Impact.Decisions[0].Claims[0] != "cl1" {
		t.Errorf("decoded = %+v", got)
	}
	if _, err := client.ParseTime(got.RetractedAt); err != nil {
		t.Errorf("retracted_at not RFC 3339: %v", err)
	}

	_, err = c.RetractEvent(context.Background(), "ev missing", "")
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Errorf("unknown event: err = %v, want a 404 APIError", err)
	}
	if gotPath != "POST /v1/episodes/ev%20missing/retract" {
		t.Errorf("id not path-escaped: %q", gotPath)
	}
}
