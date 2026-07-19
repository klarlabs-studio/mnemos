package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkspaceRegistry_RoundTripAndResolve(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)

	// Two workspaces; "acme" spans two folders, "inner" is nested under one.
	acmeA := filepath.Join(t.TempDir(), "acme-api")
	acmeB := filepath.Join(t.TempDir(), "acme-web")
	inner := filepath.Join(acmeA, "pkg", "deep")
	reg := workspaceRegistry{Workspaces: map[string]*workspace{
		"acme":  {Folders: []string{acmeA, acmeB}, DB: "sqlite:///w/acme.db"},
		"inner": {Folders: []string{inner}, DB: "sqlite:///w/inner.db"},
	}}
	if err := saveWorkspaceRegistry(reg); err != nil {
		t.Fatalf("save: %v", err)
	}

	// A session in acmeB resolves to "acme".
	if dsn, name, folder := resolveWorkspaceBrain(acmeB); name != "acme" || dsn != "sqlite:///w/acme.db" || folder != acmeB {
		t.Errorf("acmeB → (%q,%q,%q), want acme", dsn, name, folder)
	}
	// A deep dir under acmeA resolves to acme via the ancestor match...
	sub := filepath.Join(acmeA, "cmd")
	if _, name, folder := resolveWorkspaceBrain(sub); name != "acme" || folder != acmeA {
		t.Errorf("sub of acmeA → (%q,%q), want acme/%s", name, folder, acmeA)
	}
	// ...but the more specific nested workspace wins where it applies.
	if _, name := "", ""; true {
		if _, n, _ := resolveWorkspaceBrain(inner); n != "inner" {
			t.Errorf("inner dir → %q, want inner (most-specific match must win)", n)
		}
		_ = name
	}
	// Outside any workspace → empty.
	if dsn, name, _ := resolveWorkspaceBrain(t.TempDir()); dsn != "" || name != "" {
		t.Errorf("outside → (%q,%q), want empty", dsn, name)
	}
	if dsn, _, _ := resolveWorkspaceBrain(""); dsn != "" {
		t.Error("empty cwd → empty")
	}
}

func TestWorkspaceRegistry_ActivePinOverridesFolder(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	aFolder := filepath.Join(t.TempDir(), "a")
	bFolder := filepath.Join(t.TempDir(), "b")
	reg := workspaceRegistry{Workspaces: map[string]*workspace{
		"a": {Folders: []string{aFolder}, DB: "sqlite:///w/a.db"},
		"b": {Folders: []string{bFolder}, DB: "sqlite:///w/b.db"},
	}, Active: "b"}
	if err := saveWorkspaceRegistry(reg); err != nil {
		t.Fatal(err)
	}

	// A pin overrides folder resolution: cwd inside a's folder still resolves b.
	if dsn, name, folder := resolveWorkspaceBrain(aFolder); name != "b" || dsn != "sqlite:///w/b.db" || folder != bFolder {
		t.Errorf("pinned: (%q,%q,%q), want b/%s", dsn, name, folder, bFolder)
	}
	// Even with an empty cwd the pin resolves.
	if _, name, _ := resolveWorkspaceBrain(""); name != "b" {
		t.Errorf("pin with empty cwd → %q, want b", name)
	}

	// Clearing the pin restores folder-based resolution.
	reg.Active = ""
	if err := saveWorkspaceRegistry(reg); err != nil {
		t.Fatal(err)
	}
	if _, name, _ := resolveWorkspaceBrain(aFolder); name != "a" {
		t.Errorf("unpinned: cwd in a → %q, want a", name)
	}

	// A pin at a workspace that no longer exists falls through to folders.
	reg.Active = "gone"
	if err := saveWorkspaceRegistry(reg); err != nil {
		t.Fatal(err)
	}
	if _, name, _ := resolveWorkspaceBrain(aFolder); name != "a" {
		t.Errorf("stale pin → %q, want folder match a", name)
	}
}

func TestWorkspaceExportImport_RoundTrip(t *testing.T) {
	// Source machine: a workspace with local folders.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	srcFolder := filepath.Join(t.TempDir(), "src")
	reg := workspaceRegistry{Workspaces: map[string]*workspace{
		"acme": {Folders: []string{srcFolder}, DB: "sqlite:///src/acme.db"},
	}}
	if err := saveWorkspaceRegistry(reg); err != nil {
		t.Fatal(err)
	}
	def := filepath.Join(t.TempDir(), "acme.yaml")
	handleWorkspaceExport([]string{"acme", "--out", def}, Flags{})

	// Target machine: a fresh registry; import with a machine-local folder.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	dstFolder := filepath.Join(t.TempDir(), "dst")
	handleWorkspaceImport([]string{def, "--folder", dstFolder}, Flags{})

	got, err := loadWorkspaceRegistry()
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}
	ws := got.Workspaces["acme"]
	if ws == nil {
		t.Fatal("acme not imported")
	}
	// Folder was overridden to the local path...
	if len(ws.Folders) != 1 || ws.Folders[0] != filepath.Clean(dstFolder) {
		t.Errorf("imported folders = %v, want [%s]", ws.Folders, dstFolder)
	}
	// ...but the name (hence hosted tenant) is preserved — the shared identity.
	if deriveHostedTenant("acme") == "" {
		t.Fatal("tenant derivation broken")
	}
	// And the imported workspace activates from the new folder.
	if _, name, _ := resolveWorkspaceBrain(dstFolder); name != "acme" {
		t.Errorf("imported workspace didn't activate: %q", name)
	}
}

func TestWorkspaceRegistry_EmptyWhenNoFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	reg, err := loadWorkspaceRegistry()
	if err != nil {
		t.Fatalf("a missing registry file is not an error: %v", err)
	}
	if len(reg.Workspaces) != 0 {
		t.Errorf("no file → empty registry, got %+v", reg.Workspaces)
	}
	if dsn, _, _ := resolveWorkspaceBrain("/anywhere"); dsn != "" {
		t.Error("no registry → no workspace")
	}
}

// A malformed registry must be reported, never silently read as empty. Every
// mutator does load → mutate → save, and save rewrites the file wholesale, so
// swallowing the parse error turned the next `workspace create` into "delete
// every registered workspace" with no warning.
func TestWorkspaceRegistry_MalformedFileIsAnError(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	path := workspaceRegistryPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("workspaces: {this is: [not valid yaml\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := loadWorkspaceRegistry(); err == nil {
		t.Fatal("malformed registry parsed as valid; a mutation would now erase every workspace")
	}
}

// The hook path must fail open (a broken registry cannot block a session) but
// must not pretend the workspace simply does not exist without saying so.
func TestResolveWorkspaceBrain_FailsOpenOnMalformedRegistry(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)
	path := workspaceRegistryPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(":\n  bad\n\tmixed indent"), 0o600); err != nil {
		t.Fatal(err)
	}

	dsn, name, folder := resolveWorkspaceBrain(t.TempDir())
	if dsn != "" || name != "" || folder != "" {
		t.Errorf("expected a clean fail-open, got dsn=%q name=%q folder=%q", dsn, name, folder)
	}
}

// The registry can hold a credentialed DSN (workspace DB comes from the global
// --db flag), so it must not be world-readable, and it must be replaced
// atomically rather than truncated in place.
func TestSaveWorkspaceRegistry_ModeAndAtomicity(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfg)

	reg := workspaceRegistry{Workspaces: map[string]*workspace{
		"acme": {Folders: []string{"/tmp/acme"}, DB: "postgres://u:secret@db/mnemos"},
	}}
	if err := saveWorkspaceRegistry(reg); err != nil {
		t.Fatalf("save: %v", err)
	}

	path := workspaceRegistryPath()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("registry mode %o is group/world readable; it can carry a DSN password", perm)
	}

	// No stray temp files left behind by the atomic write.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".workspaces-") {
			t.Errorf("atomic write left a temp file behind: %s", e.Name())
		}
	}

	back, err := loadWorkspaceRegistry()
	if err != nil {
		t.Fatalf("round-trip load: %v", err)
	}
	if back.Workspaces["acme"] == nil {
		t.Error("round-trip lost the workspace")
	}
}
