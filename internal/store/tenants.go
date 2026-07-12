package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"go.klarlabs.de/mnemos/internal/domain"
)

// TenantScope identifies one tenant partition discovered by enumerating a
// single multi-tenant store, together with that tenant's synthesized lessons
// read under isolation.
//
// It is the input the ADR-0011 consolidation ("promote") pass consumes when it
// is pointed at ONE multi-tenant backend (`consolidate --promote --all-tenants
// --db <dsn>`) instead of an explicit list of per-tenant DSNs: the enumerator
// discovers the tenants and reads each one's lessons scoped to that tenant, so
// no cross-tenant bleed occurs at read time.
type TenantScope struct {
	// Tenant is an opaque, distinct-per-tenant label — the tenant id for
	// row-level (Postgres) backends, or the derived physical namespace (the
	// "t_"-prefixed [TenantNamespace] value) for namespace backends. It is used
	// only to count distinct corroborating tenants and to seed the promotion
	// de-identification denylist; it never appears in any promoted output.
	Tenant string

	// DSN is the fully tenant-scoped DSN to Open for reading ONLY this tenant's
	// data — a `?namespace=<derived>` DSN pointing at a physically separate
	// file/database for namespace backends, or a `?tenant=<id>` DSN for the
	// Postgres row-level model. Callers use it for auxiliary per-tenant reads
	// (e.g. the prediction-error ranking signal).
	DSN string

	// Lessons are this tenant's synthesized lessons, already read under that
	// tenant's scope. They are pre-read (rather than left to the caller via DSN)
	// so attribution is correct on EVERY backend regardless of connection role:
	// the Postgres enumerator reads with an explicit `WHERE tenant = $1` filter
	// so a promoted candidate can never gain false cross-tenant corroboration
	// from an RLS-bypassing (superuser) connection.
	Lessons []domain.Lesson

	// Claims are this tenant's claims, read under the SAME tenant scope as
	// Lessons (namespace isolation for sqlite/mysql/local libsql, an explicit
	// `WHERE tenant = $1` filter for postgres). They feed the ADR 0012 knowledge
	// promotion path (Path A): the consolidation pass synthesizes knowledge
	// schemas from the class-level subset and unions them with Lessons. Pre-read
	// under isolation for the same anti-leak reason as Lessons — a claim can never
	// gain false cross-tenant corroboration from an RLS-bypassing connection.
	Claims []domain.Claim
}

// TenantEnumerator discovers the tenant partitions of a single multi-tenant
// base store and returns each as a [TenantScope] (with that tenant's lessons
// read in isolation). Providers that can physically or logically enumerate
// their tenants register one per scheme in init() via [RegisterTenantEnumerator].
type TenantEnumerator func(ctx context.Context, baseDSN string) ([]TenantScope, error)

var (
	enumeratorsMu sync.RWMutex
	enumerators   = map[string]TenantEnumerator{}
)

// RegisterTenantEnumerator associates a URL scheme with a tenant-enumeration
// factory. Providers invoke this from init() for the backends that can
// enumerate their tenants (namespace-per-tenant: sqlite/mysql/local libsql;
// row-level: postgres). Duplicate registration panics so collisions surface at
// startup rather than silently shadowing.
func RegisterTenantEnumerator(scheme string, fn TenantEnumerator) {
	if scheme == "" {
		panic("store: cannot register tenant enumerator with empty scheme")
	}
	if fn == nil {
		panic("store: cannot register nil TenantEnumerator for " + scheme)
	}
	enumeratorsMu.Lock()
	defer enumeratorsMu.Unlock()
	if _, dup := enumerators[scheme]; dup {
		panic("store: duplicate tenant enumerator registration for " + scheme)
	}
	enumerators[scheme] = fn
}

// EnumerateTenants discovers the tenants of a single multi-tenant store and
// returns each tenant's scope + lessons. The DSN's URL scheme picks the
// provider enumerator.
//
// Backends that offer no per-tenant isolation primitive (memory, remote libSQL —
// see [TenancyModeForDSN]) cannot enumerate tenants and return an error, matching
// the fail-closed posture of the multi-tenant serve/MCP gates: a store that
// cannot isolate must never be treated as multi-tenant.
//
// The result is ordered by tenant label for determinism.
func EnumerateTenants(ctx context.Context, baseDSN string) ([]TenantScope, error) {
	scheme, _, ok := strings.Cut(baseDSN, "://")
	if !ok || scheme == "" {
		return nil, fmt.Errorf("store: dsn %q missing scheme://", baseDSN)
	}
	if TenancyModeForDSN(baseDSN) == TenancyNone {
		return nil, fmt.Errorf("store: backend %q cannot enumerate tenants (need postgres, sqlite, mysql, or local libsql)", scheme)
	}
	enumeratorsMu.RLock()
	fn, found := enumerators[scheme]
	enumeratorsMu.RUnlock()
	if !found {
		return nil, fmt.Errorf("store: no tenant enumerator registered for %q", scheme)
	}
	scopes, err := fn(ctx, baseDSN)
	if err != nil {
		return nil, err
	}
	sort.Slice(scopes, func(i, j int) bool { return scopes[i].Tenant < scopes[j].Tenant })
	return scopes, nil
}

// SetDSNParam returns dsn with the query parameter key set to value, removing any
// existing occurrence of that key first. It uses string surgery (not net/url) to
// avoid re-encoding credentials in the DSN; key/value must be charset-safe
// (validated namespaces / tenant ids). It is the store-side analogue of the
// per-request scoping the serve/MCP layer applies, reused by tenant enumerators
// when they build a per-tenant scoped DSN.
func SetDSNParam(dsn, key, value string) string {
	stripped := stripDSNParam(dsn, key)
	sep := "?"
	if strings.Contains(stripped, "?") {
		sep = "&"
	}
	return stripped + sep + key + "=" + value
}

// stripDSNParam removes a single named query parameter from a DSN, leaving the
// rest untouched.
func stripDSNParam(dsn, key string) string {
	base, query, ok := strings.Cut(dsn, "?")
	if !ok {
		return dsn
	}
	kept := make([]string, 0)
	for _, p := range strings.Split(query, "&") {
		if p == key || strings.HasPrefix(p, key+"=") {
			continue
		}
		kept = append(kept, p)
	}
	if len(kept) == 0 {
		return base
	}
	return base + "?" + strings.Join(kept, "&")
}
