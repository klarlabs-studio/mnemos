package store

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// TenancyMode describes how a backend isolates one tenant's data from another's.
type TenancyMode int

const (
	// TenancyNone means the backend offers no per-tenant isolation primitive, so
	// multi-tenant serving MUST be refused (fail closed). memory:// is ephemeral
	// and remote libSQL ignores namespace (each remote database is already its
	// own tenant boundary, provisioned out of band).
	TenancyNone TenancyMode = iota
	// TenancyRowLevel means the backend isolates rows within one namespace via a
	// per-connection tenant key — today only Postgres, via the `mnemos.tenant`
	// GUC + row-level security. The DSN carries `?tenant=<id>`.
	TenancyRowLevel
	// TenancyNamespace means the backend isolates each tenant into a separate
	// physical namespace (Postgres schema / MySQL database / SQLite or local
	// libSQL file). The DSN carries `?namespace=<derived>` and the provider
	// creates the partition on first open. Used for every non-Postgres backend
	// that can physically partition.
	TenancyNamespace
)

// ReservedNamespace is the default, unscoped partition (ADR 0001 §3). It is the
// namespace-tenancy analogue of the reserved `__default__` tenant: no derived
// tenant namespace can ever equal it (derivations are always "t_"-prefixed), so
// a bearer can never scope to the global/unscoped data.
const ReservedNamespace = "mnemos"

// TenancyModeForDSN classifies how the backend behind dsn isolates tenants.
//
// Postgres → row-level (RLS). sqlite/mysql/mariadb and LOCAL libSQL → namespace
// (separate schema/database/file). Everything else — memory, and REMOTE libSQL
// (which discards namespace and would therefore silently co-mingle tenants) —
// reports TenancyNone so the require-tenant gates refuse to start.
func TenancyModeForDSN(dsn string) TenancyMode {
	scheme, rest, ok := strings.Cut(dsn, "://")
	if !ok {
		return TenancyNone
	}
	switch scheme {
	case "postgres", "postgresql":
		return TenancyRowLevel
	case "sqlite", "sqlite3", "mysql", "mariadb":
		return TenancyNamespace
	case "libsql":
		// Local file mode (libsql:///abs/path — empty host, path-leading slash)
		// isolates by file, exactly like SQLite. Remote mode
		// (libsql://host.turso.io?authToken=…) ignores namespace entirely, so it
		// cannot isolate tenants within one process and must fail closed.
		if strings.HasPrefix(rest, "/") {
			return TenancyNamespace
		}
		return TenancyNone
	default:
		return TenancyNone
	}
}

// TenantNamespace derives a physical namespace (schema/database/file partition)
// from a tenant id, for backends that isolate by namespace rather than row-level
// security.
//
// The tenant charset ([A-Za-z0-9_.:-], up to 128 chars) is broader than the
// namespace charset ([a-z][a-z0-9_], max 63, letter-first), so the mapping cannot
// be identity: it lowercases and replaces every out-of-charset rune with '_',
// then appends a hash of the FULL original tenant id. The hash makes the mapping
// collision-resistant — two distinct tenants that sanitize to the same slug (e.g.
// "Acme" and "acme", or "a.b" and "a:b") still land in distinct namespaces.
//
// The result is always "t_"-prefixed, so it is letter-first, stays within 63
// bytes, and can never equal the reserved default namespace [ReservedNamespace].
// The input is assumed already validated as a tenant id by the caller.
func TenantNamespace(tenant string) string {
	sum := sha256.Sum256([]byte(tenant))
	h := hex.EncodeToString(sum[:6]) // 12 hex chars — ample against collisions

	var b strings.Builder
	b.Grow(len(tenant))
	for _, r := range strings.ToLower(tenant) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	slug := b.String()
	const maxSlug = 40
	if len(slug) > maxSlug {
		slug = slug[:maxSlug]
	}
	// "t_"(2) + slug(≤40) + "_"(1) + hash(12) = ≤55 bytes, within the 63-byte
	// namespace limit and matching ^[a-z][a-z0-9_]{0,62}$.
	return "t_" + slug + "_" + h
}
