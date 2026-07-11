package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFindProjectDB_NoMnemosDirReturnsFalse(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Chdir(root)

	got, _, ok := findProjectDB()
	if ok {
		t.Fatalf("expected no project DB, got %q", got)
	}
}

func TestFindProjectDB_FindsInCurrentDirectory(t *testing.T) {
	// Project brains live strictly BELOW $HOME (ADR 0008), so put the project
	// in a subdirectory of home rather than at home itself.
	home := t.TempDir()
	t.Setenv("HOME", home)
	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(filepath.Join(proj, ".mnemos"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Chdir(proj)

	got, _, ok := findProjectDB()
	if !ok {
		t.Fatal("expected project DB, got none")
	}
	want := filepath.Join(proj, ".mnemos", "mnemos.db")
	if got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
}

func TestFindProjectDB_WalksUpToAncestor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(filepath.Join(proj, ".mnemos"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	deep := filepath.Join(proj, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatalf("mkdirall: %v", err)
	}
	t.Chdir(deep)

	got, _, ok := findProjectDB()
	if !ok {
		t.Fatal("expected project DB, got none")
	}
	want := filepath.Join(proj, ".mnemos", "mnemos.db")
	if got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
}

func TestFindProjectDB_PrefersNearestAncestor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	outer := filepath.Join(home, "outer")
	if err := os.MkdirAll(filepath.Join(outer, ".mnemos"), 0o755); err != nil {
		t.Fatalf("mkdir outer: %v", err)
	}
	inner := filepath.Join(outer, "a", "b")
	if err := os.MkdirAll(filepath.Join(inner, ".mnemos"), 0o755); err != nil {
		t.Fatalf("mkdir inner: %v", err)
	}
	deep := filepath.Join(inner, "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatalf("mkdirall: %v", err)
	}
	t.Chdir(deep)

	got, _, ok := findProjectDB()
	if !ok {
		t.Fatal("expected project DB, got none")
	}
	want := filepath.Join(inner, ".mnemos", "mnemos.db")
	if got != want {
		t.Fatalf("path = %q, want %q (should prefer nearest ancestor)", got, want)
	}
}

func TestFindProjectDB_StopsAtHomeDirectory(t *testing.T) {
	root := t.TempDir()
	// .mnemos lives ABOVE the configured HOME, so discovery must stop before it.
	if err := os.Mkdir(filepath.Join(root, ".mnemos"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	t.Setenv("HOME", home)
	t.Chdir(home)

	if got, _, ok := findProjectDB(); ok {
		t.Fatalf("expected discovery to stop at HOME, got %q", got)
	}
}

// TestFindProjectDB_HomeMnemosNotAdopted is the ADR 0008 guardrail: a .mnemos
// directory at $HOME itself is the global fallback dir, not a project brain, so
// discovery must fall through to the XDG default rather than adopt it.
func TestFindProjectDB_HomeMnemosNotAdopted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.Mkdir(filepath.Join(home, ".mnemos"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Chdir(home)

	if got, _, ok := findProjectDB(); ok {
		t.Fatalf("$HOME/.mnemos must not be adopted as a project brain, got %q", got)
	}

	// And from a subdirectory of home with no closer .mnemos, still not adopted.
	sub := filepath.Join(home, "sub", "dir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdirall: %v", err)
	}
	t.Chdir(sub)
	if got, _, ok := findProjectDB(); ok {
		t.Fatalf("from a subdir of home, $HOME/.mnemos must not be adopted, got %q", got)
	}
}

func TestResolveDBPath_ProjectBeatsGlobalDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg"))
	// Project below home (ADR 0008: home itself is never a project root).
	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(filepath.Join(proj, ".mnemos"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Chdir(proj)

	want := filepath.Join(proj, ".mnemos", "mnemos.db")
	if got := resolveDBPath(); got != want {
		t.Fatalf("resolveDBPath = %q, want %q (project should win over XDG)", got, want)
	}
}

func TestResolveDBPath_FallsBackToXDGGlobal(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	xdg := filepath.Join(root, "xdg")
	t.Setenv("XDG_DATA_HOME", xdg)
	t.Chdir(root)

	want := filepath.Join(xdg, "mnemos", "mnemos.db")
	if got := resolveDBPath(); got != want {
		t.Fatalf("resolveDBPath = %q, want %q", got, want)
	}
}
