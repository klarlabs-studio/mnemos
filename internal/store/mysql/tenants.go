package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
)

// Register the MySQL/MariaDB tenant enumerator so `consolidate --promote
// --all-tenants` can discover the per-tenant partitions of a single MySQL base
// server.
func init() {
	store.RegisterTenantEnumerator("mysql", enumerateTenants)
	store.RegisterTenantEnumerator("mariadb", enumerateTenants)
}

// enumerateTenants discovers every physical tenant partition of a MySQL base
// server and reads each one's lessons in isolation.
//
// Namespace-per-tenant isolation for MySQL is database-per-namespace (see the
// package doc): a tenant whose derived namespace is `t_<slug>_<hash>` lives in a
// distinct MySQL *database* of that name. Enumeration lists the server's
// databases whose name is a valid derived tenant namespace ("t_"-prefixed — see
// store.TenantNamespace) AND that actually contain a `lessons` table (so an
// unrelated `t_*` database belonging to another application is never opened,
// which would otherwise create the Mnemos schema in it). Each matching database
// is opened as its own physically-isolated store.
func enumerateTenants(ctx context.Context, baseDSN string) ([]store.TenantScope, error) {
	parsed, err := ParseDSN(baseDSN)
	if err != nil {
		return nil, fmt.Errorf("mysql: enumerate tenants: %w", err)
	}

	admin, err := sql.Open("mysql", parsed.AdminDSN)
	if err != nil {
		return nil, fmt.Errorf("mysql: open admin: %w", err)
	}
	defer func() { _ = admin.Close() }()
	if err := admin.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("mysql: ping admin: %w", err)
	}

	// Databases that hold a Mnemos `lessons` table — the ground-truth marker of
	// a Mnemos partition. Filtering on the table's presence (rather than the name
	// alone) avoids opening — and thereby bootstrapping the schema into — a
	// same-named database owned by something else.
	rows, err := admin.QueryContext(ctx, `
SELECT DISTINCT table_schema
FROM information_schema.tables
WHERE table_name = 'lessons'
ORDER BY table_schema`)
	if err != nil {
		return nil, fmt.Errorf("mysql: list tenant databases: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var namespaces []string
	for rows.Next() {
		var schema string
		if err := rows.Scan(&schema); err != nil {
			return nil, fmt.Errorf("mysql: scan tenant database: %w", err)
		}
		if !strings.HasPrefix(schema, "t_") || !namespaceRE.MatchString(schema) {
			continue
		}
		namespaces = append(namespaces, schema)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mysql: iterate tenant databases: %w", err)
	}

	scopes := make([]store.TenantScope, 0, len(namespaces))
	for _, ns := range namespaces {
		scopedDSN := store.SetDSNParam(baseDSN, "namespace", ns)
		lessons, claims, err := readScoped(ctx, scopedDSN)
		if err != nil {
			return nil, fmt.Errorf("mysql: read tenant data for namespace %q: %w", ns, err)
		}
		scopes = append(scopes, store.TenantScope{Tenant: ns, DSN: scopedDSN, Lessons: lessons, Claims: claims})
	}
	return scopes, nil
}

// readScoped opens a single scoped MySQL DSN and returns its lessons and claims,
// both read under that tenant's physical database isolation. Claims feed the
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
