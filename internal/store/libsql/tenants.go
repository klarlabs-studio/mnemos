package libsql

import (
	"context"
	"fmt"

	"go.klarlabs.de/mnemos/internal/store"
	"go.klarlabs.de/mnemos/internal/store/sqlite"
)

// Register the libSQL tenant enumerator so `consolidate --promote --all-tenants`
// can discover the per-tenant partitions of a single LOCAL libsql base store.
//
// Only local-file libSQL isolates per tenant (file-per-namespace, exactly like
// sqlite); remote libSQL discards the namespace and reports store.TenancyNone,
// so store.EnumerateTenants rejects it before this enumerator is ever reached.
func init() {
	store.RegisterTenantEnumerator("libsql", enumerateTenants)
}

// enumerateTenants delegates to the sqlite file-per-namespace enumeration,
// since local libSQL reuses SQLite's on-disk file layout. It fails closed for a
// remote DSN (belt-and-suspenders alongside the store.TenancyNone gate).
func enumerateTenants(ctx context.Context, baseDSN string) ([]store.TenantScope, error) {
	if store.TenancyModeForDSN(baseDSN) != store.TenancyNamespace {
		return nil, fmt.Errorf("libsql: remote database cannot enumerate tenants (namespace isolation is unavailable)")
	}
	return sqlite.EnumerateFileTenants(ctx, baseDSN, "libsql")
}
