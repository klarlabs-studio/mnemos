package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"go.klarlabs.de/mnemos"
)

// passagesMemory is a library memory holding one episode in each of two runs.
func passagesMemory(t *testing.T) mnemos.Memory {
	t.Helper()
	t.Setenv("MNEMOS_DB_URL", "sqlite://"+filepath.Join(t.TempDir(), "passages.db"))
	mem, err := newLibraryMemory(context.Background(), "")
	if err != nil {
		t.Fatalf("build facade: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	at := time.Date(2023, time.May, 8, 13, 56, 0, 0, time.UTC)
	for _, ev := range []mnemos.Event{
		{ID: "ev-a", RunID: "run-a", At: at, Type: "conversation", Content: "Jon: I booked the flight to Paris.\nGina: Rome was the highlight of my trip."},
		{ID: "ev-b", RunID: "run-b", At: at, Type: "conversation", Content: "Jon: Paris was too expensive this year.\nGina: Then go to Rome."},
	} {
		if err := mem.RememberEvent(context.Background(), ev); err != nil {
			t.Fatalf("RememberEvent: %v", err)
		}
	}
	return mem
}

func getPassages(t *testing.T, h http.Handler, path string, runs []string) (int, []passageDTO) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if runs != nil {
		req = req.WithContext(withAllowedRuns(req.Context(), runs))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var body struct {
		Passages []passageDTO `json:"passages"`
	}
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return rec.Code, body.Passages
}

func TestServePassages_ReturnsRankedPassagesWithProvenance(t *testing.T) {
	h := makePassagesHandler(passagesMemory(t))
	code, got := getPassages(t, h, "/v1/passages?query=Paris+or+Rome&limit=1", nil)
	if code != http.StatusOK || len(got) != 1 {
		t.Fatalf("status %d, %d passages; want 200 and 1", code, len(got))
	}
	if got[0].EpisodeID == "" || got[0].Text == "" || got[0].At != "2023-05-08T13:56:00Z" || got[0].Score <= 0 {
		t.Errorf("passage = %+v, want episode, text, time and a positive score", got[0])
	}
}

func TestServePassages_RequiresQuery(t *testing.T) {
	code, _ := getPassages(t, makePassagesHandler(passagesMemory(t)), "/v1/passages", nil)
	if code != http.StatusBadRequest {
		t.Errorf("status %d, want 400", code)
	}
}

// Passages are raw episode text: a token confined to some runs must not read
// another run's lines, with or without naming a run.
func TestServePassages_RunScopedTokenSeesOnlyItsRun(t *testing.T) {
	h := makePassagesHandler(passagesMemory(t))
	if code, _ := getPassages(t, h, "/v1/passages?query=Paris", []string{"run-b"}); code != http.StatusForbidden {
		t.Errorf("no run_id: status %d, want 403", code)
	}
	if code, _ := getPassages(t, h, "/v1/passages?query=Paris&run_id=run-a", []string{"run-b"}); code != http.StatusForbidden {
		t.Errorf("other run: status %d, want 403", code)
	}
	code, got := getPassages(t, h, "/v1/passages?query=Paris&run_id=run-b", []string{"run-b"})
	if code != http.StatusOK || len(got) == 0 {
		t.Fatalf("own run: status %d, %d passages", code, len(got))
	}
	for _, p := range got {
		if p.EpisodeID != "ev-b" {
			t.Errorf("passage from %q, want only run-b's episode", p.EpisodeID)
		}
	}
}

func TestServePassages_UnavailableWithoutFacade(t *testing.T) {
	code, _ := getPassages(t, makePassagesHandler(nil), "/v1/passages?query=x", nil)
	if code != http.StatusServiceUnavailable {
		t.Errorf("status %d, want 503", code)
	}
}
