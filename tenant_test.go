package mnemos_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"go.klarlabs.de/mnemos"

	_ "go.klarlabs.de/mnemos/internal/store/memory"
	_ "go.klarlabs.de/mnemos/internal/store/postgres"
	_ "go.klarlabs.de/mnemos/internal/store/sqlite"
)

// skipIfBypassRLS skips when the connection role bypasses row-level security
// (superuser or BYPASSRLS) — RLS-based isolation cannot be validated as such a
// role. Provide a non-superuser TEST_POSTGRES_DSN to exercise it.
func skipIfBypassRLS(t *testing.T, dsn string) {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer func() { _ = db.Close() }()
	var bypass bool
	if err := db.QueryRow(
		`SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname = current_user`,
	).Scan(&bypass); err != nil {
		t.Fatalf("check bypassrls: %v", err)
	}
	if bypass {
		t.Skip("TEST_POSTGRES_DSN role bypasses RLS (superuser/BYPASSRLS); provide a non-superuser role to test tenant isolation")
	}
}

// TestTenant_PostgresIsolation is the load-bearing test for ADR 0007: it proves
// default-deny cross-tenant isolation on Postgres — one tenant can neither recall
// nor Get another tenant's claims, and the unscoped (default) view sees neither.
// Gated on TEST_POSTGRES_DSN so `go test ./...` stays hermetic.
func TestTenant_PostgresIsolation(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN not set; skipping postgres tenant-isolation test")
	}
	skipIfBypassRLS(t, dsn)
	clearMnemosEnv(t)
	ns := fmt.Sprintf("ten_%d", time.Now().UnixNano())
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	base, err := mnemos.New(mnemos.WithStorage(dsn+sep+"namespace="+ns), mnemos.WithPassiveMode())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = base.Close() }()

	a, err := base.Tenant("tenant_a")
	if err != nil {
		t.Fatalf("Tenant(a): %v", err)
	}
	defer func() { _ = a.Close() }()
	b, err := base.Tenant("tenant_b")
	if err != nil {
		t.Fatalf("Tenant(b): %v", err)
	}
	defer func() { _ = b.Close() }()

	ctx := context.Background()
	const secret = "alpha private incident zzqq"
	idA, err := a.RememberClaimWithEvidence(ctx, secret, []string{"evt:a1"}, time.Now().UTC())
	if err != nil {
		t.Fatalf("a.RememberClaimWithEvidence: %v", err)
	}

	// A can read its own claim by id (exact lookup — independent of recall ranking).
	if got, err := a.Get(ctx, idA); err != nil || !strings.Contains(got.Statement, "alpha private incident") {
		t.Fatalf("tenant A cannot read its own claim: got %+v err %v", got, err)
	}
	// B must NOT be able to read A's claim by id — RLS filters it to not-found.
	if _, err := b.Get(ctx, idA); err == nil {
		t.Fatal("ISOLATION BREACH: tenant B read tenant A's claim by id")
	}
	// The unscoped/default view must not see A's data either.
	if _, err := base.Get(ctx, idA); err == nil {
		t.Fatal("ISOLATION BREACH: default tenant read tenant A's claim by id")
	}

	// Reverse: B writes, A cannot read it.
	idB, err := b.RememberClaimWithEvidence(ctx, "beta private incident wwpp", []string{"evt:b1"}, time.Now().UTC())
	if err != nil {
		t.Fatalf("b.RememberClaimWithEvidence: %v", err)
	}
	if _, err := a.Get(ctx, idB); err == nil {
		t.Fatal("ISOLATION BREACH: tenant A read tenant B's claim by id")
	}
	if got, err := b.Get(ctx, idB); err != nil || !strings.Contains(got.Statement, "beta private incident") {
		t.Fatalf("tenant B cannot read its own claim: got %+v err %v", got, err)
	}
}

// TestTenant_UnsupportedBackendFailsClosed verifies Tenant() errors (rather than
// silently sharing) on a backend that does not enforce isolation.
func TestTenant_UnsupportedBackendFailsClosed(t *testing.T) {
	t.Parallel()
	clearMnemosEnv(t)
	mem, err := mnemos.New(mnemos.WithStorage("memory://?namespace=ten_unsupported"), mnemos.WithPassiveMode())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = mem.Close() }()

	if _, err := mem.Tenant("acme"); err == nil {
		t.Fatal("expected an error: tenant scoping is not supported on the memory backend")
	}
}

// TestTenant_RejectsBadIDs covers validation (runs before the backend check, so a
// hermetic memory store suffices).
func TestTenant_RejectsBadIDs(t *testing.T) {
	t.Parallel()
	clearMnemosEnv(t)
	mem, err := mnemos.New(mnemos.WithStorage("memory://?namespace=ten_badids"), mnemos.WithPassiveMode())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = mem.Close() }()

	for _, bad := range []string{"", "has space", "quote'inject", "__default__"} {
		if _, err := mem.Tenant(bad); err == nil {
			t.Fatalf("expected error for invalid/reserved tenant id %q", bad)
		}
	}
}
