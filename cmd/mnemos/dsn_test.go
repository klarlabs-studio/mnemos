package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/mnemos/internal/store"
)

// TestResolveDSN_URLEnvWinsEverything covers the headline contract:
// when MNEMOS_DB_URL is set we return it untouched. The user might
// be pointing at memory://, postgres://, or any future scheme — we
// should not interpret the value as a file path.
func TestResolveDSN_URLEnvWinsEverything(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	if err := os.Mkdir(filepath.Join(root, ".mnemos"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Chdir(root)

	t.Setenv("MNEMOS_DB_URL", "memory://")
	if got, want := resolveDSN(), "memory://"; got != want {
		t.Errorf("resolveDSN = %q, want %q (URL env should win even when project DB exists)", got, want)
	}

	t.Setenv("MNEMOS_DB_URL", "sqlite:///srv/cogstack.db?namespace=mnemos")
	if got := resolveDSN(); got != "sqlite:///srv/cogstack.db?namespace=mnemos" {
		t.Errorf("resolveDSN dropped query parameters: got %q", got)
	}
}

// TestResolveDSN_FallsBackToSQLiteFromProjectPath verifies the
// no-MNEMOS_DB_URL path: we wrap the resolved SQLite project file as
// sqlite://<path> so the registry can dispatch.
func TestResolveDSN_FallsBackToSQLiteFromProjectPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MNEMOS_DB_URL", "")
	// Project below home (ADR 0008: $HOME/.mnemos is the global fallback dir,
	// not a project brain).
	proj := filepath.Join(home, "proj")
	if err := os.MkdirAll(filepath.Join(proj, ".mnemos"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Chdir(proj)

	want := "sqlite://" + filepath.Join(proj, ".mnemos", "mnemos.db")
	if got := resolveDSN(); got != want {
		t.Errorf("resolveDSN = %q, want %q", got, want)
	}
}

// TestResolveDSN_FallsBackToXDGGlobal covers the case where neither
// MNEMOS_DB_URL nor a .mnemos/ project directory exists: the resolver
// should still produce a usable sqlite:// DSN pointing at the XDG
// global default.
func TestResolveDSN_FallsBackToXDGGlobal(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("MNEMOS_DB_URL", "")
	xdg := filepath.Join(root, "xdg")
	t.Setenv("XDG_DATA_HOME", xdg)
	t.Chdir(root)

	want := "sqlite://" + filepath.Join(xdg, "mnemos", "mnemos.db")
	if got := resolveDSN(); got != want {
		t.Errorf("resolveDSN = %q, want %q", got, want)
	}
}

// TestOpenConn_OpensMemoryBackend exercises the helper end-to-end
// with the memory provider — proves the providers are registered in
// cmd/mnemos and that resolveDSN+store.Open compose correctly.
func TestOpenConn_OpensMemoryBackend(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("MNEMOS_DB_URL", "memory://")
	t.Chdir(root)

	conn, err := openConn(context.Background())
	if err != nil {
		t.Fatalf("openConn(memory): %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	if conn.Events == nil || conn.Claims == nil {
		t.Errorf("memory Conn missing repos: %+v", conn)
	}
}

// TestOpenConn_OpensSQLiteBackendFromURL mirrors the memory test for
// the SQLite provider — the registry should dispatch sqlite:// DSNs
// to the SQLite provider and surface a *sql.DB on Conn.Raw.
func TestOpenConn_OpensSQLiteBackendFromURL(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("MNEMOS_DB_URL", "sqlite://"+filepath.Join(root, "explicit.db"))
	t.Chdir(root)

	conn, err := openConn(context.Background())
	if err != nil {
		t.Fatalf("openConn(sqlite): %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	if conn.Raw == nil {
		t.Error("expected SQLite Conn to expose *sql.DB through Raw")
	}
}

// TestResolveDSNForContext_TenancyDispatch verifies resolveDSNForContext routes
// per backend: Postgres → row-level (?tenant=), sqlite/mysql/local libsql →
// namespace (?namespace=<derived>), and unsupported backends fail closed.
func TestResolveDSNForContext_TenancyDispatch(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	tenant := "acme.prod"
	wantNS := store.TenantNamespace(tenant)

	cases := []struct {
		name       string
		dsn        string
		wantSuffix string // "" means expect an error
	}{
		{"postgres row-level", "postgres://u:p@h:5432/db", "?tenant=" + tenant},
		{"sqlite namespace", "sqlite:///srv/mnemos.db", "?namespace=" + wantNS},
		{"mysql namespace", "mysql://u:p@h/mnemos", "?namespace=" + wantNS},
		{"local libsql namespace", "libsql:///srv/mnemos.db", "?namespace=" + wantNS},
		{"remote libsql unsupported", "libsql://x.turso.io?authToken=t", ""},
		{"memory unsupported", "memory://", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("MNEMOS_DB_URL", c.dsn)
			ctx := withTenant(context.Background(), tenant)
			got, err := resolveDSNForContext(ctx)
			if c.wantSuffix == "" {
				if err == nil {
					t.Fatalf("expected an error for %q, got dsn %q", c.dsn, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveDSNForContext(%q): %v", c.dsn, err)
			}
			if !strings.HasSuffix(got, c.wantSuffix) {
				t.Errorf("resolveDSNForContext(%q) = %q, want suffix %q", c.dsn, got, c.wantSuffix)
			}
		})
	}
}

// TestResolveDSNForContext_NamespaceReplacesBase ensures a derived tenant
// namespace REPLACES a base ?namespace= rather than appending a duplicate
// (providers read the first value, so a duplicate would silently keep the base).
func TestResolveDSNForContext_NamespaceReplacesBase(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MNEMOS_DB_URL", "sqlite:///srv/mnemos.db?namespace=mnemos")
	ctx := withTenant(context.Background(), "acme")
	got, err := resolveDSNForContext(ctx)
	if err != nil {
		t.Fatalf("resolveDSNForContext: %v", err)
	}
	if strings.Count(got, "namespace=") != 1 {
		t.Errorf("expected exactly one namespace= param, got %q", got)
	}
	if !strings.Contains(got, "namespace="+store.TenantNamespace("acme")) {
		t.Errorf("derived namespace not present in %q", got)
	}
}

// doctor output and audit exports get pasted into bug reports and archived, so
// neither may carry a DSN password. Both used the raw resolveDSN() while
// displayDSN() — already used by the destructive-confirm paths — sat unused.
func TestDiagnosticOutputRedactsDSNPassword(t *testing.T) {
	t.Setenv("MNEMOS_DB_URL", "postgres://mnemos:hunter2@db.internal:5432/mnemos")

	got := displayDSN()
	if strings.Contains(got, "hunter2") {
		t.Fatalf("displayDSN leaked the password: %s", got)
	}
	// The redaction must stay useful as a diagnostic.
	for _, want := range []string{"db.internal", "mnemos", "redacted"} {
		if !strings.Contains(got, want) {
			t.Errorf("displayDSN dropped %q, leaving too little to diagnose with: %s", want, got)
		}
	}
}

// A credential-free local DSN must pass through untouched — redaction should
// not make the common case unreadable.
func TestDisplayDSNLeavesCredentialFreeDSNIntact(t *testing.T) {
	t.Setenv("MNEMOS_DB_URL", "sqlite:///tmp/brain.db")
	if got := displayDSN(); got != "sqlite:///tmp/brain.db" {
		t.Errorf("displayDSN rewrote a credential-free DSN: %s", got)
	}
}

// The repo overlay resolves from the SERVER's working directory, so on a
// multi-tenant server it is one un-tenanted SQLite file shared by every
// tenant. resolveDSNForContext returned a context override BEFORE consulting
// the tenant, so a --require-tenant server started inside a directory holding
// a .mnemos/mnemos.db federated that shared brain into every tenant's
// default-scope query — a cross-tenant read.
func TestResolveDSNForContext_RefusesOverrideInMultiTenantMode(t *testing.T) {
	tenantRequired = true
	t.Cleanup(func() { tenantRequired = false })

	ctx := withDSNOverride(context.Background(), "sqlite:///tmp/some-repo/.mnemos/mnemos.db")
	got, err := resolveDSNForContext(ctx)
	if err == nil {
		t.Fatalf("multi-tenant request accepted an un-tenanted brain override: %s", got)
	}
	if got != "" {
		t.Errorf("returned a DSN alongside the error: %s", got)
	}
}

// Single-tenant is the normal local setup and must be untouched: the repo
// overlay is the whole point of the override.
func TestResolveDSNForContext_OverrideHonouredSingleTenant(t *testing.T) {
	tenantRequired = false
	const repo = "sqlite:///tmp/some-repo/.mnemos/mnemos.db"

	got, err := resolveDSNForContext(withDSNOverride(context.Background(), repo))
	if err != nil {
		t.Fatalf("single-tenant override refused: %v", err)
	}
	if got != repo {
		t.Errorf("resolved %q, want the override %q", got, repo)
	}
}

// Belt and braces: the override must not even be produced in multi-tenant mode.
func TestMCPRepoBrainDSN_EmptyInMultiTenantMode(t *testing.T) {
	tenantRequired = true
	t.Cleanup(func() { tenantRequired = false })

	if got := mcpRepoBrainDSN(); got != "" {
		t.Errorf("multi-tenant server produced a repo overlay DSN: %s", got)
	}
}
