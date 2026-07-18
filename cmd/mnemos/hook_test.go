package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/query"
)

func TestCaptureBudget(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want time.Duration
	}{
		{"unset uses default", "", defaultCaptureBudget},
		{"override is honored", "90s", 90 * time.Second},
		{"minutes parse", "5m", 5 * time.Minute},
		{"malformed falls back", "not-a-duration", defaultCaptureBudget},
		{"zero falls back", "0s", defaultCaptureBudget},
		{"negative falls back", "-30s", defaultCaptureBudget},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MNEMOS_CAPTURE_TIMEOUT", tc.env)
			if got := captureBudget(); got != tc.want {
				t.Errorf("captureBudget() = %s, want %s", got, tc.want)
			}
		})
	}
}

// The default budget must stay under the hook timeout `mnemos init` installs,
// or Claude Code kills capture before its own budget applies — the original
// "Hook cancelled" failure.
func TestDefaultCaptureBudgetFitsHookTimeout(t *testing.T) {
	if defaultCaptureBudget >= captureHookTimeout*time.Second {
		t.Errorf("defaultCaptureBudget %s >= captureHookTimeout %ds; Claude Code would cancel capture mid-pipeline",
			defaultCaptureBudget, captureHookTimeout)
	}
}

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

func TestRenderRecall(t *testing.T) {
	got := renderRecall([]recallClaim{
		{Text: "chose Postgres", Type: string(domain.ClaimTypeFact), TrustScore: 0.8},
		{Text: "rate limiting on gateway", Type: string(domain.ClaimTypeDecision), TrustScore: 0.5},
	}, 1)
	if !strings.Contains(got, "chose Postgres") || !strings.Contains(got, "trust 0.80") {
		t.Errorf("recall missing claim/trust: %q", got)
	}
	if !strings.Contains(got, "1 contradiction") {
		t.Errorf("recall missing contradiction note: %q", got)
	}
	// Empty result → empty string (no injection).
	if renderRecall(nil, 0) != "" {
		t.Error("empty claim list should render to empty string")
	}
}

func TestRenderBrief(t *testing.T) {
	got := renderBrief(12, 3, 2, 0, query.PrecedenceTenantWins)
	if !strings.Contains(got, "12 claims across 3 runs") || !strings.Contains(got, "2 open contradiction") {
		t.Errorf("brief wrong: %q", got)
	}
	if strings.Contains(got, "scoped to this workspace") {
		t.Errorf("no repo overlay expected: %q", got)
	}
	withRepo := renderBrief(12, 3, 0, 5, query.PrecedenceTenantWins)
	if !strings.Contains(withRepo, "+5 claim(s) scoped to this workspace") {
		t.Errorf("repo overlay note missing: %q", withRepo)
	}
}

func TestHostedConfigured(t *testing.T) {
	t.Setenv("MNEMOS_URL", "")
	if hostedConfigured() {
		t.Error("no MNEMOS_URL → local mode")
	}
	t.Setenv("MNEMOS_URL", "https://brain.example.com")
	if !hostedConfigured() {
		t.Error("MNEMOS_URL set → hosted mode")
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
