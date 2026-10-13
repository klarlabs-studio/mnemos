package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// brainCounts is what a reader of the brain can observe after a capture.
type brainCounts struct{ events, claims, evidence int }

func countBrain(t *testing.T) brainCounts {
	t.Helper()
	ctx := context.Background()
	conn, err := openConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	events, err := conn.Events.ListAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := conn.Claims.CountAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	links, err := conn.Claims.ListAllEvidence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return brainCounts{events: len(events), claims: int(claims), evidence: len(links)}
}

// End to end through the capture write path: capturing the same session text
// again — the Sep 19–20 replay that re-stored 56k claims — must leave the
// brain exactly as it was. No new episode, no new claim, and no new evidence
// link that would let the repeat raise trust.
func TestE2E_RecapturingASessionIsIdempotent(t *testing.T) {
	ctx := context.Background()
	t.Setenv("MNEMOS_DB_URL", "sqlite://"+filepath.Join(t.TempDir(), "capture.db"))
	text := "We decided to run every release through kiln.\n" +
		"The retry budget for the outbox drain is 3 attempts.\n" +
		"Postgres enforces tenant isolation with row-level security."
	in := mcpProcessTextInput{Text: text, SessionID: "session-replayed"}

	if _, err := mcpRunProcessText(ctx, "claude-code", in); err != nil {
		t.Fatal(err)
	}
	first := countBrain(t)
	if first.claims == 0 {
		t.Fatal("first capture stored no claims; the test would prove nothing")
	}
	for range 2 {
		if _, err := mcpRunProcessText(ctx, "claude-code", in); err != nil {
			t.Fatal(err)
		}
	}
	if again := countBrain(t); again != first {
		t.Errorf("recapturing the same session changed the brain: %+v -> %+v", first, again)
	}
}

// The same beliefs captured in a NEW session are the same claims, now with a
// second, independent piece of evidence each.
func TestE2E_RestatingInANewSessionLinksInsteadOfDuplicating(t *testing.T) {
	ctx := context.Background()
	t.Setenv("MNEMOS_DB_URL", "sqlite://"+filepath.Join(t.TempDir(), "capture.db"))
	text := "We decided to run every release through kiln."

	for _, session := range []string{"session-1", "session-2"} {
		if _, err := mcpRunProcessText(ctx, "claude-code", mcpProcessTextInput{Text: text, SessionID: session}); err != nil {
			t.Fatal(err)
		}
	}
	got := countBrain(t)
	if got.claims != 1 || got.evidence != 2 {
		t.Errorf("two sessions stating one belief = %+v, want 1 claim with 2 evidence links", got)
	}
}

// A compacted session's transcript contributes nothing from its summary: the
// restated session and the summary's scaffolding are not conversation.
func TestE2E_CompactionSummaryIsNotCaptured(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	summary := strings.ReplaceAll(compactionSummaryText, "\n", `\n`)
	summary = strings.ReplaceAll(summary, `"`, `\"`)
	lines := []string{
		`{"type":"user","message":{"role":"user","content":"Move the release pipeline to kiln"}}`,
		`{"type":"user","isCompactSummary":true,"message":{"role":"user","content":"` + summary + `"}}`,
		`{"type":"user","message":{"role":"user","content":"` + summary + `"}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(extractTranscriptText(path, 64*1024))
	if got != "Move the release pipeline to kiln" {
		t.Errorf("capture text = %q, want only the real user message", got)
	}
}
