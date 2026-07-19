package mnemos

import (
	"strings"
	"testing"
)

// Re-scoping an already-scoped Memory must REPLACE the tenant, not append a
// second one. Postgres used an appending helper while the namespace branch
// replaced, so m.Tenant("a").Tenant("b") produced `?tenant=a&tenant=b`.
// postgres.ParseDSN and tenantFromDSN both read it with Query().Get, which
// returns the FIRST value — leaving the Memory pinned to tenant a while the
// caller believed it was b. Silent cross-tenant reads and writes, no error.
//
// This is an internal test because the defect lives in DSN rewriting, and
// reproducing it through the public API would need a live Postgres (the
// namespace branch, which sqlite uses, was always correct).
func TestTenant_RescopingReplacesTheTenantParam(t *testing.T) {
	const base = "postgres://u:p@localhost:5432/mnemos"

	first := setDSNParam(base, "tenant", "alpha")
	second := setDSNParam(first, "tenant", "beta")

	if n := strings.Count(second, "tenant="); n != 1 {
		t.Fatalf("re-scoping produced %d tenant params: %s", n, second)
	}
	if got := tenantFromDSN(second); got != "beta" {
		t.Errorf("effective tenant = %q, want %q (DSN: %s)", got, "beta", second)
	}
	if strings.Contains(second, "alpha") {
		t.Errorf("the previous tenant survived re-scoping: %s", second)
	}
}

// Re-scoping must preserve unrelated query parameters — silently dropping
// sslmode or pool settings while rewriting the tenant would be its own outage.
func TestTenant_RescopingPreservesOtherParams(t *testing.T) {
	base := "postgres://u:p@localhost:5432/mnemos?sslmode=require&tenant=alpha&pool_max_conns=5"
	got := setDSNParam(base, "tenant", "beta")

	for _, want := range []string{"sslmode=require", "pool_max_conns=5", "tenant=beta"} {
		if !strings.Contains(got, want) {
			t.Errorf("re-scoped DSN lost %q: %s", want, got)
		}
	}
	if strings.Contains(got, "tenant=alpha") {
		t.Errorf("stale tenant survived: %s", got)
	}
	if got := tenantFromDSN(got); got != "beta" {
		t.Errorf("effective tenant = %q, want beta", got)
	}
}

// The namespace branch (sqlite/mysql/local libSQL) was already correct; pin it
// so the two branches cannot drift apart again.
func TestTenant_NamespaceRescopingAlsoReplaces(t *testing.T) {
	base := "sqlite:///tmp/brain.db?namespace=t_alpha"
	got := setDSNParam(base, "namespace", "t_beta")
	if n := strings.Count(got, "namespace="); n != 1 {
		t.Fatalf("re-scoping produced %d namespace params: %s", n, got)
	}
	if !strings.Contains(got, "namespace=t_beta") {
		t.Errorf("namespace not replaced: %s", got)
	}
}
