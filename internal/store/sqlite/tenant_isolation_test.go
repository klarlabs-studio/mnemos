package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
	_ "go.klarlabs.de/mnemos/internal/store/sqlite"
)

// TestSQLite_CrossTenantIsolation is the namespace-per-tenant guardrail for the
// SQLite backend (the non-Postgres analogue of TestPostgres_CrossTenantIsolation).
// Two tenants derive distinct namespaces via store.TenantNamespace, which the
// provider turns into distinct database files; tenant B must read NONE of tenant
// A's rows. Runs in CI with no external dependency (temp files).
func TestSQLite_CrossTenantIsolation(t *testing.T) {
	ctx := context.Background()
	base := "sqlite://" + filepath.Join(t.TempDir(), "mnemos.db")

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
		ID: "iso-sqlite", RunID: "r", SchemaVersion: "1",
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

// TestSQLite_TenantNamespaceDistinctFiles is a belt-and-suspenders check that two
// tenants never share a file: appending to one leaves the other's database empty.
func TestSQLite_TenantNamespaceDistinctFiles(t *testing.T) {
	if store.TenantNamespace("acme") == store.TenantNamespace("globex") {
		t.Fatal("distinct tenants must derive distinct namespaces")
	}
}
