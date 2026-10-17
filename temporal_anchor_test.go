package mnemos

import (
	"context"
	"strings"
	"testing"
	"time"
)

func anchorMem(t *testing.T, namespace string) *memory {
	t.Helper()
	for _, k := range []string{"MNEMOS_STORAGE", "MNEMOS_MODE", "MNEMOS_LLM_PROVIDER", "MNEMOS_API_KEY"} {
		t.Setenv(k, "")
	}
	mem, err := New(WithStorage("memory://?namespace="+namespace), WithPassiveMode())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	return mem.(*memory)
}

func storedTexts(t *testing.T, m *memory) []string {
	t.Helper()
	claims, err := m.conn.Claims.ListAll(context.Background())
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	out := make([]string, 0, len(claims))
	for _, c := range claims {
		out = append(out, c.Text)
	}
	return out
}

// Item.At says when the content was said. A belief that keeps the speaker's
// "yesterday" is stored with the date that resolves it, so it still means
// something when it is recalled months later.
func TestRemember_AtResolvesRelativeDatesInBeliefs(t *testing.T) {
	m := anchorMem(t, "anchor_at")
	said := time.Date(2023, time.July, 3, 13, 36, 0, 0, time.UTC)
	if err := m.Remember(context.Background(), Item{
		Type:    "fact",
		Content: "Melanie signed up for a pottery class yesterday.",
		At:      said,
	}); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	texts := storedTexts(t, m)
	if len(texts) == 0 {
		t.Fatal("no belief stored")
	}
	resolved := false
	for _, text := range texts {
		if strings.Contains(text, "yesterday (2 July 2023)") {
			resolved = true
		} else if strings.Contains(text, "yesterday") {
			t.Errorf("belief %q keeps an unresolved \"yesterday\"", text)
		}
	}
	if !resolved {
		t.Errorf("beliefs %q: none carries the resolved date", texts)
	}
}

// Without At, Mnemos does not know when the content was said, so beliefs are
// stored as extracted.
func TestRemember_WithoutAtStoresBeliefsAsExtracted(t *testing.T) {
	m := anchorMem(t, "anchor_no_at")
	if err := m.Remember(context.Background(), Item{
		Type:    "fact",
		Content: "Melanie signed up for a pottery class yesterday.",
	}); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	for _, text := range storedTexts(t, m) {
		if strings.Contains(text, "(") {
			t.Errorf("belief %q was annotated without a reference time", text)
		}
	}
}
