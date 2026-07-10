package store

import (
	"regexp"
	"strings"
	"testing"
)

// namespaceRE mirrors the provider-side namespace contract (ADR 0001 §3). Every
// derived tenant namespace MUST satisfy it, or the provider would reject the DSN.
var namespaceRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

func TestTenantNamespace_ValidAndPrefixed(t *testing.T) {
	cases := []string{
		"acme",
		"acme.prod",
		"Acme",                   // uppercase — must survive lowercasing
		"team:platform-eu",       // ':' and '-' are valid tenant chars, invalid namespace chars
		"a",                      // shortest
		strings.Repeat("x", 128), // longest tenant id
		"UPPER.Case_Mix-99:z",
	}
	for _, tenant := range cases {
		ns := TenantNamespace(tenant)
		if !namespaceRE.MatchString(ns) {
			t.Errorf("TenantNamespace(%q) = %q, not a valid namespace (%s)", tenant, ns, namespaceRE)
		}
		if !strings.HasPrefix(ns, "t_") {
			t.Errorf("TenantNamespace(%q) = %q, want a t_ prefix", tenant, ns)
		}
		if ns == ReservedNamespace {
			t.Errorf("TenantNamespace(%q) collided with the reserved namespace %q", tenant, ReservedNamespace)
		}
		if len(ns) > 63 {
			t.Errorf("TenantNamespace(%q) = %q (%d bytes), exceeds the 63-byte limit", tenant, ns, len(ns))
		}
	}
}

func TestTenantNamespace_Deterministic(t *testing.T) {
	first := TenantNamespace("acme")
	second := TenantNamespace("acme")
	if first != second {
		t.Fatalf("TenantNamespace must be deterministic: %q != %q", first, second)
	}
}

func TestTenantNamespace_DistinctForCollidingSlugs(t *testing.T) {
	// Tenants that sanitize to the same slug must still map to distinct
	// namespaces — the hash suffix guarantees this. Without it, "acme.prod" and
	// "acme:prod" and "acme_prod" would all collapse to "t_acme_prod".
	seen := map[string]string{}
	for _, tenant := range []string{"acme.prod", "acme:prod", "acme_prod", "acme-prod", "Acme.Prod"} {
		ns := TenantNamespace(tenant)
		if other, dup := seen[ns]; dup {
			t.Errorf("namespace collision: %q and %q both derive %q", tenant, other, ns)
		}
		seen[ns] = tenant
	}
}

func TestTenancyModeForDSN(t *testing.T) {
	cases := []struct {
		dsn  string
		want TenancyMode
	}{
		{"postgres://u:p@h:5432/db", TenancyRowLevel},
		{"postgresql://u:p@h/db", TenancyRowLevel},
		{"sqlite:///var/lib/mnemos/mnemos.db", TenancyNamespace},
		{"sqlite3:///tmp/x.db", TenancyNamespace},
		{"mysql://u:p@h:3306/db", TenancyNamespace},
		{"mariadb://u:p@h/db", TenancyNamespace},
		{"libsql:///abs/path/local.db", TenancyNamespace},      // local file → isolates
		{"libsql://my-db.turso.io?authToken=xyz", TenancyNone}, // remote → cannot isolate
		{"memory://", TenancyNone},
		{"memory://?namespace=x", TenancyNone},
		{"bogus://whatever", TenancyNone},
		{"no-scheme", TenancyNone},
	}
	for _, c := range cases {
		if got := TenancyModeForDSN(c.dsn); got != c.want {
			t.Errorf("TenancyModeForDSN(%q) = %v, want %v", c.dsn, got, c.want)
		}
	}
}
