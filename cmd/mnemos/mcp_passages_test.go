package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"go.klarlabs.de/mnemos"
)

func seedPassageRuns(t *testing.T) {
	t.Helper()
	t.Setenv("MNEMOS_DB_URL", "sqlite://"+filepath.Join(t.TempDir(), "brain.db"))
	mem, err := newLibraryMemory(context.Background(), "")
	if err != nil {
		t.Fatalf("build facade: %v", err)
	}
	defer func() { _ = mem.Close() }()
	for _, ev := range []mnemos.Event{
		{ID: "ev-a", RunID: "alpha", At: time.Now().UTC(), Type: "conversation", Content: "Jon: I booked the flight to Paris.\nGina: Rome was the highlight."},
		{ID: "ev-b", RunID: "beta", At: time.Now().UTC(), Type: "conversation", Content: "Jon: Paris was too expensive.\nGina: Then go to Rome."},
	} {
		if err := mem.RememberEvent(context.Background(), ev); err != nil {
			t.Fatalf("RememberEvent: %v", err)
		}
	}
}

func TestMCPSearchPassages_ReturnsMatchingLines(t *testing.T) {
	seedPassageRuns(t)
	out, err := mcpRunSearchPassages(context.Background(), mcpSearchPassagesInput{Query: "Paris or Rome", Limit: 1})
	if err != nil || len(out.Passages) != 1 {
		t.Fatalf("search_passages = %+v, %v; want one passage", out, err)
	}
	if p := out.Passages[0]; p.EpisodeID == "" || p.Text == "" || p.At == "" || p.Score <= 0 {
		t.Errorf("passage = %+v, want episode, text, time and a positive score", p)
	}
	if _, err := mcpRunSearchPassages(context.Background(), mcpSearchPassagesInput{Query: " "}); err == nil {
		t.Error("an empty query must be refused")
	}
}

func TestMCPSearchPassages_EnforcesRunScope(t *testing.T) {
	seedPassageRuns(t)
	ctx := runScopedCtx("alpha")
	if _, err := mcpRunSearchPassages(ctx, mcpSearchPassagesInput{Query: "Paris"}); err == nil {
		t.Error("an unscoped search from a run-restricted token must be denied (fail-closed)")
	}
	if _, err := mcpRunSearchPassages(ctx, mcpSearchPassagesInput{Query: "Paris", RunID: "beta"}); err == nil {
		t.Error("a search in a run outside the allowlist must be denied")
	}
	out, err := mcpRunSearchPassages(ctx, mcpSearchPassagesInput{Query: "Paris", RunID: "alpha"})
	if err != nil || len(out.Passages) == 0 {
		t.Fatalf("allowed run: %+v, %v", out, err)
	}
	for _, p := range out.Passages {
		if p.EpisodeID != "ev-a" {
			t.Errorf("passage from %q, want only alpha's episode", p.EpisodeID)
		}
	}
}
