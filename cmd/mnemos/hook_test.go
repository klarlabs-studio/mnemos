package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
)

func TestReadHookEvent(t *testing.T) {
	ev := readHookEvent(strings.NewReader(`{"hook_event_name":"UserPromptSubmit","prompt":"hello","session_id":"s1"}`))
	if ev.Prompt != "hello" || ev.SessionID != "s1" {
		t.Errorf("parsed event wrong: %+v", ev)
	}
	// Empty / malformed stdin yields a zero event, not a panic.
	if got := readHookEvent(strings.NewReader("")); got.Prompt != "" {
		t.Errorf("empty stdin should yield zero event, got %+v", got)
	}
	if got := readHookEvent(strings.NewReader("{bad")); got.Prompt != "" {
		t.Errorf("malformed stdin should yield zero event, got %+v", got)
	}
}

func TestFormatRecall(t *testing.T) {
	out := mcpQueryOutput{
		Claims: []domain.Claim{
			{Text: "chose Postgres", Type: domain.ClaimTypeFact, TrustScore: 0.8},
			{Text: "rate limiting on gateway", Type: domain.ClaimTypeDecision, TrustScore: 0.5},
		},
	}
	got := formatRecall(out)
	if !strings.Contains(got, "chose Postgres") || !strings.Contains(got, "trust 0.80") {
		t.Errorf("recall missing claim/trust: %q", got)
	}
	// Empty result → empty string (no injection).
	if formatRecall(mcpQueryOutput{}) != "" {
		t.Error("empty query output should format to empty string")
	}
}

func TestExtractTranscriptText(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.jsonl")
	content := strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"Switch cache to Redis"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Migrating to Redis for pub/sub"},{"type":"tool_use","name":"Edit","input":{}}]}}`,
		`{"type":"user","content":"ok"}`,
		`{"type":"system","content":"ignored"}`,
		``,
		`{bad json`,
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got := extractTranscriptText(path, 20*1024)
	if !strings.Contains(got, "Switch cache to Redis") {
		t.Errorf("missing user string content: %q", got)
	}
	if !strings.Contains(got, "Migrating to Redis for pub/sub") {
		t.Errorf("missing assistant text block: %q", got)
	}
	if strings.Contains(got, "ignored") {
		t.Errorf("system role should be skipped: %q", got)
	}
	if strings.Contains(got, "Edit") {
		t.Errorf("tool_use block should be skipped: %q", got)
	}
}

func TestExtractTranscriptTextMissingFile(t *testing.T) {
	if got := extractTranscriptText("/no/such/file.jsonl", 1024); got != "" {
		t.Errorf("missing file should yield empty string, got %q", got)
	}
}
