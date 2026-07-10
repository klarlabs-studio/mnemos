package libsql_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
	_ "go.klarlabs.de/mnemos/internal/store/libsql"
)

// TestLibsql_LocalCrossTenantIsolation is the namespace-per-tenant guardrail for
// LOCAL libSQL (file mode). Two tenants derive distinct namespaces, which the
// provider turns into distinct local files; tenant B must read NONE of tenant
// A's rows. Runs in CI with no external dependency. Remote libSQL is deliberately
// excluded from namespace tenancy (store.TenancyModeForDSN → TenancyNone).
func TestLibsql_LocalCrossTenantIsolation(t *testing.T) {
	ctx := context.Background()
	// libsql:///abs/path — triple slash = local file mode.
	base := "libsql://" + filepath.Join(t.TempDir(), "mnemos.db")

	openTenant := func(tenant string) *store.Conn {
		dsn := base + "?namespace=" + store.TenantNamespace(tenant)
		c, err := store.Open(ctx, dsn)
		if err != nil {
			t.Fatalf("open %s: %v", tenant, err)
		}
		t.Cleanup(func() { _ = c.Close() })
		return c
	}

	a := openTenant("acme")
	b := openTenant("globex")

	now := time.Now().UTC()
	ev := domain.Event{
		ID: "iso-libsql", RunID: "r", SchemaVersion: "1",
		Content: "tenant-A secret", SourceInputID: "in",
		Timestamp: now, IngestedAt: now, CreatedBy: domain.SystemUser,
	}
	if err := a.Events.Append(ctx, ev); err != nil {
		t.Fatalf("tenant A append: %v", err)
	}

	bEvents, err := b.Events.ListAll(ctx)
	if err != nil {
		t.Fatalf("tenant B list: %v", err)
	}
	for _, e := range bEvents {
		if e.ID == ev.ID {
			t.Fatalf("CROSS-TENANT LEAK: tenant B read tenant A's event %q", e.ID)
		}
	}

	aEvents, err := a.Events.ListAll(ctx)
	if err != nil {
		t.Fatalf("tenant A list: %v", err)
	}
	found := false
	for _, e := range aEvents {
		if e.ID == ev.ID {
			found = true
		}
	}
	if !found {
		t.Error("tenant A cannot see its own event")
	}
}
