package mnemos

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/ports"
)

const otherConversation = `Jon: I finally booked the flight to Paris for the dance competition.
Gina: Rome was the highlight of my trip, the food was unreal.
Jon: My studio opens next month if the permits come through.
Gina: I started selling my clothing line online last week.`

func TestSplitPassages_PairsLinesAndSkipsBlanks(t *testing.T) {
	got := splitPassages("a\n\nb\nc\n  \nd\ne")
	want := []string{"a\nb", "c\nd", "e"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("splitPassages = %q, want %q", got, want)
	}
}

// A passage is the stored wording itself, so it answers when no belief kept
// the detail — here no belief was extracted at all.
func TestPassages_ReturnsTheBestMatchingStoredLines(t *testing.T) {
	m := calibMem(t)
	ctx := context.Background()
	at := time.Date(2023, time.May, 8, 13, 56, 0, 0, time.UTC)
	for _, ev := range []Event{
		{ID: "ev-conv", At: at, Type: "conversation", Content: conversation},
		{ID: "ev-other", At: at.Add(24 * time.Hour), Type: "conversation", Content: otherConversation},
	} {
		if err := m.RememberEvent(ctx, ev); err != nil {
			t.Fatalf("RememberEvent: %v", err)
		}
	}

	got, err := m.Passages(ctx, PassageQuery{Text: "Where did Melanie take the kids camping?", Limit: 2})
	if err != nil {
		t.Fatalf("Passages: %v", err)
	}
	if len(got) == 0 || len(got) > 2 {
		t.Fatalf("Passages returned %d, want 1..2", len(got))
	}
	if !strings.Contains(got[0].Text, "camping at the lake") {
		t.Errorf("first passage = %q, want the camping line", got[0].Text)
	}
	if got[0].EpisodeID != "ev-conv" || !got[0].At.Equal(at) {
		t.Errorf("first passage from %q at %v, want ev-conv at %v", got[0].EpisodeID, got[0].At, at)
	}
	for i := 1; i < len(got); i++ {
		if got[i].Score > got[i-1].Score {
			t.Errorf("passages not ranked: %v after %v", got[i].Score, got[i-1].Score)
		}
	}
}

func TestPassages_RequiresText(t *testing.T) {
	m := calibMem(t)
	if _, err := m.Passages(context.Background(), PassageQuery{Text: "  "}); err == nil {
		t.Error("Passages with empty text returned no error")
	}
}

func TestPassages_NoMatchIsEmpty(t *testing.T) {
	m := calibMem(t)
	ctx := context.Background()
	if err := m.RememberEvent(ctx, Event{ID: "ev-conv", At: time.Now().UTC(), Type: "conversation", Content: conversation}); err != nil {
		t.Fatalf("RememberEvent: %v", err)
	}
	got, err := m.Passages(ctx, PassageQuery{Text: "quantum chromodynamics"})
	if err != nil || len(got) != 0 {
		t.Errorf("Passages = %d, %v; want none, nil", len(got), err)
	}
}

// The SQLite store pre-selects candidates through its episode text index, the
// path a real brain takes.
func TestPassages_UsesTheEpisodeIndexOnSQLite(t *testing.T) {
	for _, k := range []string{"MNEMOS_STORAGE", "MNEMOS_MODE", "MNEMOS_LLM_PROVIDER", "MNEMOS_API_KEY"} {
		t.Setenv(k, "")
	}
	mem, err := New(WithStorage("sqlite://"+filepath.Join(t.TempDir(), "passages.db")), WithPassiveMode())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	if _, ok := mem.(*memory).conn.Events.(ports.TextSearcher); !ok {
		t.Fatal("SQLite episode store has no text index; this test would exercise the fallback")
	}
	ctx := context.Background()
	for _, ev := range []Event{
		{ID: "ev-conv", At: time.Now().UTC(), Type: "conversation", Content: conversation},
		{ID: "ev-other", At: time.Now().UTC(), Type: "conversation", Content: otherConversation},
	} {
		if err := mem.RememberEvent(ctx, ev); err != nil {
			t.Fatalf("RememberEvent: %v", err)
		}
	}
	got, err := mem.Passages(ctx, PassageQuery{Text: "Which cities did Jon and Gina visit, Paris or Rome?"})
	if err != nil || len(got) == 0 {
		t.Fatalf("Passages = %d, %v", len(got), err)
	}
	if got[0].EpisodeID != "ev-other" || !strings.Contains(got[0].Text, "Paris") {
		t.Errorf("first passage = %q from %q, want the Paris/Rome lines", got[0].Text, got[0].EpisodeID)
	}
}
