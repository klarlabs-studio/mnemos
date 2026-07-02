package mnemos

import (
	"fmt"
	"regexp"
	"strings"
)

// defaultTenant is the reserved tenant an unscoped [Memory] reads and writes. It
// MUST match the postgres provider's default (the column DEFAULT and the GUC set
// on a non-tenant connection) so existing single-tenant data stays reachable.
const defaultTenant = "__default__"

// tenantRE validates a tenant id. Broader than a namespace (it is not a schema
// identifier) but restricted to a quote/backslash-free charset so it is safe to
// interpolate into the `SET mnemos.tenant = '<id>'` the postgres provider runs.
var tenantRE = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

// Tenant implements [Memory.Tenant]. It opens a fresh, tenant-scoped view over the
// same DSN with a `tenant=` parameter; the postgres provider pins that tenant as a
// per-connection GUC and row-level security enforces isolation default-deny.
func (m *memory) Tenant(id string) (Memory, error) {
	id = strings.TrimSpace(id)
	if !tenantRE.MatchString(id) {
		return nil, fmt.Errorf("mnemos: invalid tenant id %q (want %s)", id, tenantRE.String())
	}
	if id == defaultTenant {
		return nil, fmt.Errorf("mnemos: tenant id %q is reserved", defaultTenant)
	}
	backend, _ := parseBackendNamespace(m.dsn)
	if backend != "postgres" && backend != "postgresql" {
		return nil, fmt.Errorf("mnemos: tenant scoping is only supported on postgres (this store is %q)", backend)
	}
	cfg := m.cfg
	cfg.storageDSN = withTenantParam(m.dsn, id)
	return newFromCfg(cfg)
}

// withTenantParam appends the tenant query parameter to a storage DSN, respecting
// any existing query string.
func withTenantParam(dsn, id string) string {
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "tenant=" + id
}
