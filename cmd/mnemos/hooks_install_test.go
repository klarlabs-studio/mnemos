package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readHooksMap(t *testing.T, path string) map[string][]hookMatcher {
	t.Helper()
	top, err := readSettings(path)
	if err != nil {
		t.Fatalf("readSettings: %v", err)
	}
	return decodeHooks(top["hooks"])
}

func countCommands(matchers []hookMatcher) int {
	n := 0
	for _, m := range matchers {
		n += len(m.Hooks)
	}
	return n
}

func TestInstallHooksFreshAndIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	specs := hookSpecsFor(map[string]bool{"recall": true, "brief": true, "capture": true})

	// Fresh install.
	if _, err := installHooks(path, "/bin/mnemos", "sqlite:///tmp/a.db", specs, true); err != nil {
		t.Fatalf("install: %v", err)
	}
	hooks := readHooksMap(t, path)
	for _, ev := range []string{"UserPromptSubmit", "SessionStart", "SessionEnd"} {
		if countCommands(hooks[ev]) != 1 {
			t.Errorf("%s: got %d commands, want 1", ev, countCommands(hooks[ev]))
		}
	}

	// Re-install must not duplicate.
	if _, err := installHooks(path, "/bin/mnemos", "sqlite:///tmp/a.db", specs, true); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	hooks = readHooksMap(t, path)
	if got := countCommands(hooks["UserPromptSubmit"]); got != 1 {
		t.Errorf("after reinstall UserPromptSubmit = %d commands, want 1 (no duplication)", got)
	}
}

func TestInstallHooksPreservesForeignEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")

	// Seed with a foreign hook and a top-level key.
	seed := map[string]any{
		"model": "opus",
		"hooks": map[string]any{
			"UserPromptSubmit": []any{
				map[string]any{"hooks": []any{
					map[string]any{"type": "command", "command": "/opt/other run"},
				}},
			},
		},
	}
	data, _ := json.MarshalIndent(seed, "", "  ")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	specs := hookSpecsFor(map[string]bool{"recall": true})
	backup, err := installHooks(path, "/bin/mnemos", "sqlite:///tmp/a.db", specs, true)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if backup == "" {
		t.Error("expected a backup when overwriting an existing file")
	}

	top, _ := readSettings(path)
	if string(top["model"]) != `"opus"` {
		t.Errorf("top-level key not preserved: model=%s", top["model"])
	}
	hooks := decodeHooks(top["hooks"])
	cmds := hooks["UserPromptSubmit"]
	var foreign, ours int
	for _, m := range cmds {
		for _, h := range m.Hooks {
			if isMnemosHookCommand(h.Command) {
				ours++
			} else if strings.Contains(h.Command, "/opt/other") {
				foreign++
			}
		}
	}
	if foreign != 1 || ours != 1 {
		t.Errorf("expected 1 foreign + 1 mnemos hook, got foreign=%d ours=%d", foreign, ours)
	}
}

// TestInstallHooksSetsTimeouts guards the capture-hook regression: Claude Code
// defaults an unset hook timeout to 60s, but `hook capture` budgets itself 90s
// (plus a 20s float-back), so a timeout-less entry got SIGKILLed mid-pipeline
// and the session was lost ("Hook cancelled"). Every installed hook must carry
// a timeout that exceeds its own internal budget, so the hook's graceful,
// fail-open timeout is what actually governs.
func TestInstallHooksSetsTimeouts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	specs := hookSpecsFor(map[string]bool{"recall": true, "brief": true, "capture": true})
	if _, err := installHooks(path, "/bin/mnemos", "sqlite:///tmp/a.db", specs, true); err != nil {
		t.Fatalf("install: %v", err)
	}

	// event -> the hook's own internal context budget, in seconds.
	internalBudget := map[string]int{
		"UserPromptSubmit": 12,                                  // hookRecall
		"SessionStart":     18,                                  // hookBrief: 10s + 8s doc sync-back
		"SessionEnd":       int(captureBudget/time.Second) + 20, // hookCapture: captureBudget + float-back
	}
	hooks := readHooksMap(t, path)
	for event, budget := range internalBudget {
		matchers := hooks[event]
		if len(matchers) == 0 {
			t.Fatalf("%s: no hook installed", event)
		}
		for _, m := range matchers {
			for _, h := range m.Hooks {
				if h.Timeout == 0 {
					t.Errorf("%s: timeout unset, so Claude Code applies its 60s default", event)
					continue
				}
				if h.Timeout <= budget {
					t.Errorf("%s: timeout %ds <= internal budget %ds; the hook would be killed mid-run",
						event, h.Timeout, budget)
				}
			}
		}
	}
}

func TestBuildHookCommandQuotesDSN(t *testing.T) {
	cmd := buildHookCommand("/usr/local/bin/mnemos", "sqlite:///tmp/a.db", "recall", true)
	if !strings.Contains(cmd, "hook recall --db") {
		t.Errorf("unexpected command: %q", cmd)
	}
	if !isMnemosHookCommand(cmd) {
		t.Errorf("buildHookCommand output not recognized as ours: %q", cmd)
	}
	// Without inlining, no DSN in the command (config-carried).
	noInline := buildHookCommand("/usr/local/bin/mnemos", "postgres://u:p@h/db", "recall", false)
	if strings.Contains(noInline, "postgres://") {
		t.Errorf("credentialed DSN must not appear in hook command: %q", noInline)
	}
}

func TestShQuote(t *testing.T) {
	cases := map[string]string{
		"/usr/local/bin/mnemos": "/usr/local/bin/mnemos", // no quoting needed
		"":                      "''",                    // empty
		"/path with space/x":    "'/path with space/x'",  // spaces
		"a'b":                   `'a'\''b'`,              // embedded quote
	}
	for in, want := range cases {
		if got := shQuote(in); got != want {
			t.Errorf("shQuote(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReadSettingsMalformedErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSettings(path); err == nil {
		t.Fatal("expected error on malformed settings.json")
	}
}
