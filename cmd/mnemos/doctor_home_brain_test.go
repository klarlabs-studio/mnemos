package main

import (
	"os"
	"path/filepath"
	"testing"
)

// writeHomeBrain creates a non-empty $HOME/.mnemos/mnemos.db under the given
// home dir and returns its path.
func writeHomeBrain(t *testing.T, home string) string {
	t.Helper()
	dir := filepath.Join(home, ".mnemos")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	p := filepath.Join(dir, "mnemos.db")
	if err := os.WriteFile(p, []byte("not empty"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

func TestProbeHomeBrainShadow_WarnsWhenOrphaned(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	writeHomeBrain(t, home)
	// Active brain is somewhere else entirely.
	t.Setenv("MNEMOS_DB_URL", "sqlite:///somewhere/else/mnemos.db")

	check, present := probeHomeBrainShadow()
	if !present {
		t.Fatal("expected a warning for an orphaned home brain")
	}
	if check.Status != "warn" {
		t.Errorf("status = %q, want warn", check.Status)
	}
}

func TestProbeHomeBrainShadow_QuietWhenHomeBrainIsActive(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	p := writeHomeBrain(t, home)
	// The home brain IS the active brain — not orphaned, so no warning.
	t.Setenv("MNEMOS_DB_URL", "sqlite://"+p)

	if _, present := probeHomeBrainShadow(); present {
		t.Error("home brain is the active brain; must not warn")
	}
}

func TestProbeHomeBrainShadow_QuietWhenAbsent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MNEMOS_DB_URL", "sqlite:///somewhere/else/mnemos.db")

	if _, present := probeHomeBrainShadow(); present {
		t.Error("no home brain exists; must not warn")
	}
}

func TestProbeHomeBrainShadow_QuietWhenEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".mnemos")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Zero-byte file (e.g. a freshly-touched but unused path) is not a brain.
	if err := os.WriteFile(filepath.Join(dir, "mnemos.db"), nil, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("MNEMOS_DB_URL", "sqlite:///somewhere/else/mnemos.db")

	if _, present := probeHomeBrainShadow(); present {
		t.Error("empty home brain file must not warn")
	}
}
