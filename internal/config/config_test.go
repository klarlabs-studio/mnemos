package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "mnemos.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadAndEnvOverrides(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, `
db:
  url: postgres://localhost/mnemos
  max_conns: 25
llm:
  provider: anthropic
  api_key: sk-test
  timeout: 30s
serve:
  port: 8080
feedback:
  decay: 0.9
federation:
  enabled: true
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	env := cfg.EnvOverrides()

	want := map[string]string{
		"MNEMOS_DB_URL":             "postgres://localhost/mnemos",
		"MNEMOS_DB_MAX_CONNS":       "25",
		"MNEMOS_LLM_PROVIDER":       "anthropic",
		"MNEMOS_LLM_API_KEY":        "sk-test",
		"MNEMOS_LLM_TIMEOUT":        "30s",
		"MNEMOS_SERVE_PORT":         "8080",
		"MNEMOS_FEEDBACK_DECAY":     "0.9",
		"MNEMOS_FEDERATION_ENABLED": "true",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("env[%s] = %q, want %q", k, env[k], v)
		}
	}
	// Empty leaves must not appear (would shadow downstream defaults).
	if _, ok := env["MNEMOS_LLM_MODEL"]; ok {
		t.Errorf("empty leaf MNEMOS_LLM_MODEL should be omitted")
	}
}

func TestServerBlockEnvOverrides(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, `
server:
  url: https://brain.example.com
  token: tok-abc
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	env := cfg.EnvOverrides()
	if env["MNEMOS_URL"] != "https://brain.example.com" {
		t.Errorf("MNEMOS_URL = %q", env["MNEMOS_URL"])
	}
	if env["MNEMOS_TOKEN"] != "tok-abc" {
		t.Errorf("MNEMOS_TOKEN = %q", env["MNEMOS_TOKEN"])
	}
}

func TestPrecedenceBlockEnvOverride(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, "precedence: global-wins\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if env := cfg.EnvOverrides(); env["MNEMOS_PRECEDENCE"] != "global-wins" {
		t.Errorf("MNEMOS_PRECEDENCE = %q, want global-wins", env["MNEMOS_PRECEDENCE"])
	}
	// An empty precedence leaf must be omitted so it never shadows the default.
	empty := writeConfig(t, dir, "db:\n  url: memory://\n")
	ecfg, err := Load(empty)
	if err != nil {
		t.Fatalf("Load empty: %v", err)
	}
	if _, ok := ecfg.EnvOverrides()["MNEMOS_PRECEDENCE"]; ok {
		t.Error("empty precedence leaf should be omitted")
	}
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, "llm:\n  provdier: typo\n")
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for unknown field, got nil")
	}
}

func TestLoadRejectsMalformedYAML(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, "llm: [unterminated\n")
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for malformed YAML, got nil")
	}
}

func TestHydrateEnvironmentWins(t *testing.T) {
	t.Setenv("MNEMOS_LLM_PROVIDER", "openai") // operator's export
	cfg := &Config{}
	cfg.LLM.Provider = "anthropic" // config file says otherwise
	cfg.LLM.Model = "claude-opus-4-8"

	applied, err := Hydrate(cfg)
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	if got := os.Getenv("MNEMOS_LLM_PROVIDER"); got != "openai" {
		t.Errorf("environment should win: MNEMOS_LLM_PROVIDER = %q, want openai", got)
	}
	if got := os.Getenv("MNEMOS_LLM_MODEL"); got != "claude-opus-4-8" {
		t.Errorf("config should fill gap: MNEMOS_LLM_MODEL = %q", got)
	}
	// Only the gap-filled key is reported as applied.
	for _, k := range applied {
		if k == "MNEMOS_LLM_PROVIDER" {
			t.Errorf("Hydrate must not report an already-set var as applied")
		}
	}
}

func TestDiscoverPrecedence(t *testing.T) {
	dir := t.TempDir()

	// Explicit path wins even over env.
	t.Setenv(EnvConfigPath, "/from/env.yaml")
	if p, ok := Discover("/explicit.yaml"); !ok || p != "/explicit.yaml" {
		t.Errorf("explicit should win: got %q, %v", p, ok)
	}

	// Env used when no explicit path.
	if p, ok := Discover(""); !ok || p != "/from/env.yaml" {
		t.Errorf("env path expected: got %q, %v", p, ok)
	}

	// Walk-up discovery in a project dir. Empty reads as unset (Discover
	// treats "" as absent), and t.Setenv restores it after the test.
	t.Setenv(EnvConfigPath, "")
	proj := filepath.Join(dir, "proj")
	mn := filepath.Join(proj, ".mnemos")
	if err := os.MkdirAll(mn, 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(mn, "mnemos.yaml")
	if err := os.WriteFile(cfgPath, []byte("llm:\n  provider: anthropic\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(proj, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	restore := chdir(t, sub)
	defer restore()
	// Resolve symlinks: on macOS /var is a symlink to /private/var, so the
	// working directory reported by os.Getwd (and thus the discovered path)
	// is the resolved form while cfgPath is not.
	wantPath, _ := filepath.EvalSymlinks(cfgPath)
	if p, ok := Discover(""); !ok || p != wantPath {
		t.Errorf("walk-up discovery expected %q: got %q, %v", wantPath, p, ok)
	}
}

func TestLoadAndHydrateMissingExplicitIsError(t *testing.T) {
	if _, _, err := LoadAndHydrate("/definitely/not/here.yaml"); err == nil {
		t.Fatal("missing explicit config should error")
	}
}

func TestLoadAndHydrateMissingImplicitIsFine(t *testing.T) {
	t.Setenv(EnvConfigPath, "") // empty reads as unset
	// chdir to an empty temp dir with no .mnemos and a fake XDG home.
	dir := t.TempDir()
	restore := chdir(t, dir)
	defer restore()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg-empty"))
	t.Setenv("HOME", dir)
	path, applied, err := LoadAndHydrate("")
	if err != nil {
		t.Fatalf("implicit miss should not error: %v", err)
	}
	if path != "" || applied != nil {
		t.Errorf("implicit miss should report no path/applied: %q, %v", path, applied)
	}
}

func chdir(t *testing.T, dir string) func() {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	return func() { _ = os.Chdir(prev) }
}
