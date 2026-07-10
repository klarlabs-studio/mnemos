package mnemos

import (
	"fmt"
	"regexp"
	"strings"

	"go.klarlabs.de/mnemos/internal/store"
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
// same DSN. The isolation mechanism depends on the backend
// ([store.TenancyModeForDSN]):
//   - Postgres pins the tenant as a per-connection GUC (`tenant=` param) and
//     row-level security enforces isolation default-deny.
//   - sqlite / mysql / local libSQL open a physically separate namespace per
//     tenant (`namespace=<derived>`), created on first open.
//   - backends that cannot isolate (memory, remote libSQL) are refused.
func (m *memory) Tenant(id string) (Memory, error) {
	id = strings.TrimSpace(id)
	if !tenantRE.MatchString(id) {
		return nil, fmt.Errorf("mnemos: invalid tenant id %q (want %s)", id, tenantRE.String())
	}
	if id == defaultTenant {
		return nil, fmt.Errorf("mnemos: tenant id %q is reserved", defaultTenant)
	}
	cfg := m.cfg
	switch store.TenancyModeForDSN(m.dsn) {
	case store.TenancyRowLevel:
		cfg.storageDSN = withDSNParam(m.dsn, "tenant", id)
	case store.TenancyNamespace:
		cfg.storageDSN = setDSNParam(m.dsn, "namespace", store.TenantNamespace(id))
	default:
		backend, _ := parseBackendNamespace(m.dsn)
		return nil, fmt.Errorf("mnemos: tenant scoping is not supported on this backend (%q); use postgres, sqlite, mysql, or local libsql", backend)
	}
	return newFromCfg(cfg)
}

// withDSNParam appends a query parameter to a storage DSN, respecting any
// existing query string.
func withDSNParam(dsn, key, value string) string {
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + key + "=" + value
}

// setDSNParam sets a query parameter, removing any existing occurrence first so
// the derived namespace replaces (never duplicates) a base one.
func setDSNParam(dsn, key, value string) string {
	base, query, ok := strings.Cut(dsn, "?")
	if !ok {
		return dsn + "?" + key + "=" + value
	}
	kept := make([]string, 0)
	for p := range strings.SplitSeq(query, "&") {
		if p == key || strings.HasPrefix(p, key+"=") {
			continue
		}
		kept = append(kept, p)
	}
	kept = append(kept, key+"="+value)
	return base + "?" + strings.Join(kept, "&")
}
