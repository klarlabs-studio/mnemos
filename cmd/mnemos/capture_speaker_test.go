package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/extract"
)

// TestCaptureText_LabelsEachMessageWithItsRole pins #445 item 4 for Claude
// Code capture: capture used to concatenate user and assistant text with no
// speaker, so "I found the race" from the assistant read exactly like a
// statement the user made about themselves. Each message now opens with its
// role, which the extractor resolves.
func TestCaptureText_LabelsEachMessageWithItsRole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	lines := []string{
		`{"type":"user","message":{"role":"user","content":"I prefer table-driven tests in Go."}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"I found the race in the retry loop.\nMy next step is adding a deadline to the client."}]}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	got := extractTranscriptText(path, 64*1024)
	want := "user: I prefer table-driven tests in Go.\n" +
		"assistant: I found the race in the retry loop.\nMy next step is adding a deadline to the client.\n"
	if got != want {
		t.Fatalf("capture text = %q\nwant %q", got, want)
	}

	// End to end through rule-based extraction, the fallback every capture
	// without an LLM takes: the assistant's self-statements stay the
	// assistant's, and none becomes a fact about the user.
	claims, _, err := extract.NewEngine().Extract(context.Background(), []domain.Event{{ID: "ev_1", Content: got}})
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, c := range claims {
		texts = append(texts, c.Text)
	}
	for _, w := range []string{
		"The user prefers table-driven tests in Go",
		"The assistant found the race in the retry loop",
		"The assistant's next step is adding a deadline to the client",
	} {
		if !slices.Contains(texts, w) {
			t.Errorf("missing claim %q\n got: %q", w, texts)
		}
	}
	for _, c := range texts {
		if strings.HasPrefix(c, "The user found") || strings.HasPrefix(c, "The user's next step") {
			t.Errorf("assistant self-statement attributed to the user: %q", c)
		}
	}
}
