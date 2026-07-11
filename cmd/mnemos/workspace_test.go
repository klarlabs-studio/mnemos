package main

import (
	"path/filepath"
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

func TestWorkspaceRegistry_EmptyWhenNoFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	reg := loadWorkspaceRegistry()
	if len(reg.Workspaces) != 0 {
		t.Errorf("no file → empty registry, got %+v", reg.Workspaces)
	}
	if dsn, _, _ := resolveWorkspaceBrain("/anywhere"); dsn != "" {
		t.Error("no registry → no workspace")
	}
}
