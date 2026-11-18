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
// cmd/mnemos (TestClient_UnretractEvent_AgainstServe); this covers the request
// the client builds and how it surfaces 404 and 409.
func TestUnretractEvent_RequestAndRefusals(t *testing.T) {
	var gotPath, gotReason string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.Method + " " + r.URL.EscapedPath()
		switch r.URL.Path {
		case "/v1/episodes/ev missing/unretract":
			http.Error(w, `{"error":"episode not found"}`, http.StatusNotFound)
			return
		case "/v1/episodes/ev-never/unretract":
			http.Error(w, `{"error":"episode was never retracted"}`, http.StatusConflict)
			return
		}
		var b struct {
			Reason string `json:"reason"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		gotReason = b.Reason
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"episode_id":"ev1","unretracted_at":"2026-11-18T11:00:00Z","unretracted_by":"agt","reason":"fine",
			"generation":1,"retracted_at":"2026-11-18T10:00:00Z","retracted_by":"bob","already_unretracted":false,
			"impact":{"restored_beliefs":[{"id":"cl1","status":"active"}],"skipped_beliefs":[{"id":"cl3","reason":"changed"}],
			"relinked_beliefs":[{"id":"cl2","status":"active","trust_before":0.5,"trust_after":0.8}],"uncertainty":["u"]}}`))
	}))
	t.Cleanup(srv.Close)
	c := client.New(srv.URL)

	got, err := c.UnretractEvent(context.Background(), "ev1", "fine")
	if err != nil {
		t.Fatalf("UnretractEvent: %v", err)
	}
	if gotPath != "POST /v1/episodes/ev1/unretract" || gotReason != "fine" {
		t.Errorf("request = %q reason %q", gotPath, gotReason)
	}
	if got.EventID != "ev1" || got.Generation != 1 || got.RetractedBy != "bob" ||
		got.Impact.Restored[0].Status != "active" || got.Impact.Skipped[0].ID != "cl3" || got.Impact.Relinked[0].TrustAfter != 0.8 {
		t.Errorf("decoded = %+v", got)
	}
	if _, err := client.ParseTime(got.UnretractedAt); err != nil {
		t.Errorf("unretracted_at not RFC 3339: %v", err)
	}

	var apiErr *client.APIError
	if _, err = c.UnretractEvent(context.Background(), "ev missing", ""); !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
		t.Errorf("unknown event: err = %v, want a 404 APIError", err)
	}
	if gotPath != "POST /v1/episodes/ev%20missing/unretract" {
		t.Errorf("id not path-escaped: %q", gotPath)
	}
	if _, err = c.UnretractEvent(context.Background(), "ev-never", ""); !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict {
		t.Errorf("never retracted: err = %v, want a 409 APIError", err)
	}
}
