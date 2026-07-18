package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Claude Code settings.json hook installation. We merge Mnemos hook entries
// into the target settings file idempotently, preserving every other key, and
// back up the original before writing.

type hookCommand struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

type hookMatcher struct {
	Matcher string        `json:"matcher,omitempty"`
	Hooks   []hookCommand `json:"hooks"`
}

// hookSpec describes one hook we install: the event it binds to, an optional
// matcher, the `mnemos hook <name>` sub-command it runs, and the wall-clock
// timeout Claude Code should allow it.
type hookSpec struct {
	Event   string // e.g. "UserPromptSubmit"
	Matcher string // optional; "" fires on all
	Sub     string // "recall" | "brief" | "capture"
	Timeout int    // seconds; must exceed the hook's own internal budget
}

// Hook timeouts, in seconds. Claude Code applies a 60s default when a hook
// omits one, which is *below* what `hook capture` budgets itself (90s, plus a
// 20s float-back) — so an unset timeout meant Claude Code SIGKILLed capture
// mid-pipeline and the session's knowledge was lost ("Hook cancelled"). Each
// value sits above the corresponding hook's internal context budget so the
// hook's own graceful, fail-open timeout is what actually governs; Claude
// Code's kill is only a last-resort backstop.
const (
	recallHookTimeout  = 15  // hookRecall budgets 12s
	briefHookTimeout   = 25  // hookBrief budgets 10s + 8s doc sync-back
	captureHookTimeout = 300 // hookCapture budgets captureBudget (240s) + 20s float-back
)

// hookSpecsFor returns the hook specs for the selected behaviors.
// behaviors is a set of "recall" | "brief" | "capture".
func hookSpecsFor(behaviors map[string]bool) []hookSpec {
	var specs []hookSpec
	if behaviors["recall"] {
		specs = append(specs, hookSpec{Event: "UserPromptSubmit", Sub: "recall", Timeout: recallHookTimeout})
	}
	if behaviors["brief"] {
		specs = append(specs, hookSpec{Event: "SessionStart", Matcher: "startup|resume", Sub: "brief", Timeout: briefHookTimeout})
	}
	if behaviors["capture"] {
		specs = append(specs, hookSpec{Event: "SessionEnd", Sub: "capture", Timeout: captureHookTimeout})
	}
	return specs
}

// isMnemosHookCommand reports whether a settings command string is one we own,
// so re-running init replaces our entries instead of duplicating them.
func isMnemosHookCommand(cmd string) bool {
	for _, sub := range []string{
		"hook recall", "hook brief", "hook capture",
		"hook prompt-submitted", "hook session-start", "hook session-end",
	} {
		if strings.Contains(cmd, sub) {
			return true
		}
	}
	return false
}

// buildHookCommand renders the shell command a hook runs. When inlineDSN is
// set (local SQLite/in-memory), it pins the brain via --db so the hook targets
// the same knowledge base regardless of the directory Claude Code launches it
// from. For credentialed DSNs it omits --db and lets the hook discover the DSN
// from the 0600 config file, keeping the secret out of settings.json.
func buildHookCommand(bin, dsn, sub string, inlineDSN bool) string {
	if inlineDSN {
		return fmt.Sprintf("%s hook %s --db %s", shQuote(bin), sub, shQuote(dsn))
	}
	return fmt.Sprintf("%s hook %s", shQuote(bin), sub)
}

// installHooks merges the given specs into settingsPath, replacing any prior
// Mnemos entries. It backs up the existing file first. Returns the backup path
// (empty if the file was newly created). inlineDSN pins the DSN into the hook
// command for credential-free backends; otherwise hooks discover it from the
// config file.
func installHooks(settingsPath, bin, dsn string, specs []hookSpec, inlineDSN bool) (backup string, err error) {
	top, err := readSettings(settingsPath)
	if err != nil {
		return "", err
	}

	hooks := decodeHooks(top["hooks"])
	// Drop our previous entries across every event, then add the fresh set.
	for event, matchers := range hooks {
		hooks[event] = dropMnemosMatchers(matchers)
		if len(hooks[event]) == 0 {
			delete(hooks, event)
		}
	}
	for _, spec := range specs {
		m := hookMatcher{
			Matcher: spec.Matcher,
			Hooks: []hookCommand{{
				Type:    "command",
				Command: buildHookCommand(bin, dsn, spec.Sub, inlineDSN),
				Timeout: spec.Timeout,
			}},
		}
		hooks[spec.Event] = append(hooks[spec.Event], m)
	}

	encoded, err := json.Marshal(hooks)
	if err != nil {
		return "", err
	}
	top["hooks"] = encoded

	if _, statErr := os.Stat(settingsPath); statErr == nil {
		backup, err = backupFile(settingsPath)
		if err != nil {
			return "", err
		}
	}
	if err := writeSettings(settingsPath, top); err != nil {
		return backup, err
	}
	return backup, nil
}

// dropMnemosMatchers removes matcher groups that are exclusively our hooks and
// strips our commands from any shared groups, leaving foreign hooks intact.
func dropMnemosMatchers(matchers []hookMatcher) []hookMatcher {
	var kept []hookMatcher
	for _, m := range matchers {
		var foreign []hookCommand
		for _, h := range m.Hooks {
			if !isMnemosHookCommand(h.Command) {
				foreign = append(foreign, h)
			}
		}
		if len(foreign) > 0 {
			m.Hooks = foreign
			kept = append(kept, m)
		}
	}
	return kept
}

func decodeHooks(raw json.RawMessage) map[string][]hookMatcher {
	hooks := map[string][]hookMatcher{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &hooks)
	}
	return hooks
}

// readSettings loads a settings.json into an order-agnostic top-level map,
// returning an empty map when the file is absent.
func readSettings(path string) (map[string]json.RawMessage, error) {
	top := map[string]json.RawMessage{}
	data, err := os.ReadFile(path) //nolint:gosec // operator's settings path
	if err != nil {
		if os.IsNotExist(err) {
			return top, nil
		}
		return nil, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return top, nil
	}
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("parse %s: %w (fix or move it aside and re-run)", path, err)
	}
	return top, nil
}

// writeSettings writes the top-level map back as pretty JSON, creating parent
// directories as needed.
func writeSettings(path string, top map[string]json.RawMessage) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	out, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	return os.WriteFile(path, out, 0o600)
}

// backupFile copies path to path.bak-mnemos (overwriting a prior backup).
func backupFile(path string) (string, error) {
	data, err := os.ReadFile(path) //nolint:gosec // operator's settings path
	if err != nil {
		return "", err
	}
	backup := path + ".bak-mnemos"
	if err := os.WriteFile(backup, data, 0o600); err != nil {
		return "", err
	}
	return backup, nil
}

// shQuote single-quotes a string for safe embedding in a POSIX shell command
// string (the form Claude Code executes hook commands as).
func shQuote(s string) string {
	if s == "" {
		return "''"
	}
	// Only quote when necessary to keep common paths readable.
	if !strings.ContainsAny(s, " \t\n'\"\\$`&|;<>()*?[]#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
