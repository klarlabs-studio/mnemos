package sqlite

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
)

// Register the SQLite tenant enumerator so `consolidate --promote --all-tenants`
// can discover the per-tenant partitions of a single sqlite base store.
func init() {
	store.RegisterTenantEnumerator("sqlite", enumerateTenants)
	store.RegisterTenantEnumerator("sqlite3", enumerateTenants)
}

// enumerateTenants discovers every physical tenant partition of a sqlite base
// store and reads each one's lessons in isolation.
//
// Namespace-per-tenant isolation for sqlite is file-per-namespace (see
// [parseDSN]): a tenant whose derived namespace is `t_<slug>_<hash>` lives in a
// distinct file `"<base>_<ns><ext>"`. Enumeration is therefore the reverse: glob
// the sibling files matching that pattern, keep the ones whose middle segment is
// a valid derived tenant namespace (always "t_"-prefixed — see
// store.TenantNamespace), and open each as its own physically-isolated store.
// The reserved default file ("<base><ext>", namespace "mnemos") is deliberately
// NOT matched, so unscoped/global data is never treated as a tenant.
func enumerateTenants(ctx context.Context, baseDSN string) ([]store.TenantScope, error) {
	return EnumerateFileTenants(ctx, baseDSN, "sqlite")
}

// EnumerateFileTenants is the shared file-per-namespace enumeration used by both
// the sqlite provider and libSQL's local-file mode (which reuses the sqlite file
// scheme). scheme selects which registered provider the discovered scoped DSNs
// open through, and how the base DSN is canonicalised for path resolution.
func EnumerateFileTenants(ctx context.Context, baseDSN, scheme string) ([]store.TenantScope, error) {
	parsed, err := parseDSN(canonicalSQLiteDSN(baseDSN, scheme))
	if err != nil {
		return nil, fmt.Errorf("%s: enumerate tenants: %w", scheme, err)
	}
	// parsed.Path is the base (default-namespace) file. Tenant files are
	// "<baseNoExt>_<ns><ext>".
	ext := filepath.Ext(parsed.Path)
	baseNoExt := strings.TrimSuffix(parsed.Path, ext)
	prefix := baseNoExt + "_"

	matches, err := filepath.Glob(prefix + "t_*" + ext)
	if err != nil {
		return nil, fmt.Errorf("%s: glob tenant files: %w", scheme, err)
	}

	scopes := make([]store.TenantScope, 0, len(matches))
	for _, path := range matches {
		trimmed := strings.TrimPrefix(path, prefix)
		ns := strings.TrimSuffix(trimmed, ext)
		// Defensive: skip sidecar files (-wal/-shm/-journal) and anything whose
		// middle segment is not a valid derived tenant namespace. Real tenant
		// namespaces are always "t_"-prefixed and match namespaceRE.
		if !strings.HasPrefix(ns, "t_") || !namespaceRE.MatchString(ns) {
			continue
		}
		scopedDSN := store.SetDSNParam(baseDSN, "namespace", ns)
		lessons, claims, err := readScoped(ctx, scopedDSN)
		if err != nil {
			return nil, fmt.Errorf("%s: read tenant data for namespace %q: %w", scheme, ns, err)
		}
		scopes = append(scopes, store.TenantScope{Tenant: ns, DSN: scopedDSN, Lessons: lessons, Claims: claims})
	}
	return scopes, nil
}

// canonicalSQLiteDSN rewrites a libsql:// local DSN to a sqlite:// DSN so the
// sqlite parseDSN can resolve the base file path. A sqlite/sqlite3 DSN is
// returned unchanged. The namespace param is stripped so parseDSN yields the
// BASE (default-namespace) file path, not a tenant file.
func canonicalSQLiteDSN(baseDSN, scheme string) string {
	dsn := store.SetDSNParam(baseDSN, "namespace", defaultNamespace)
	if scheme == "libsql" {
		// libsql:///abs/path → sqlite:///abs/path (same on-disk file layout).
		dsn = "sqlite://" + strings.TrimPrefix(dsn, "libsql://")
	}
	return dsn
}

// readScoped opens a single scoped sqlite DSN and returns its lessons and claims,
// both read under that tenant's physical namespace isolation. Claims feed the
// ADR 0012 knowledge promotion path (Path A). Reading through the scoped conn's
// own repositories keeps SELECT/Scan parity with ListAll — no hand-written SQL.
func readScoped(ctx context.Context, scopedDSN string) ([]domain.Lesson, []domain.Claim, error) {
	conn, err := store.Open(ctx, scopedDSN)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = conn.Close() }()
	lessons, err := conn.Lessons.ListAll(ctx)
	if err != nil {
		return nil, nil, err
	}
	claims, err := conn.Claims.ListAll(ctx)
	if err != nil {
		return nil, nil, err
	}
	return lessons, claims, nil
}
