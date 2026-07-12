package postgres

import (
	"context"
	"database/sql"
	"fmt"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
)

// Register the Postgres tenant enumerator so `consolidate --promote
// --all-tenants` can discover the tenants of a single Postgres base store.
func init() {
	store.RegisterTenantEnumerator("postgres", enumerateTenants)
	store.RegisterTenantEnumerator("postgresql", enumerateTenants)
}

// enumerateTenants discovers every tenant of a Postgres base store (row-level
// isolation, ADR 0007) and reads each one's lessons scoped to that tenant.
//
// Unlike the namespace backends, Postgres keeps all tenants in ONE schema and
// isolates rows via the `tenant` column + row-level security. Enumeration is
// therefore a cross-tenant read: `SELECT DISTINCT tenant FROM <ns>.lessons`.
// This is an inherently privileged operation — the offline consolidation pass
// must connect with a role that can see across tenants (superuser / BYPASSRLS),
// exactly as it must to compute cross-tenant corroboration at all.
//
// Attribution is kept correct REGARDLESS of that role by reading each tenant's
// lessons with an explicit `WHERE tenant = $1` filter rather than relying on
// RLS. Under an RLS-bypassing role a naive `?tenant=<id>` + ListAll would return
// every tenant's rows under every scope, giving one tenant's fact false
// cross-tenant corroboration — the exact leak ADR 0011 forbids. The explicit
// filter makes each returned scope contain that tenant's rows and no other's.
func enumerateTenants(ctx context.Context, baseDSN string) ([]store.TenantScope, error) {
	// Open the base (unscoped) store; bootstrap guarantees the `tenant` column
	// on <ns>.lessons exists (it is in the ADR-0007 RLS `scoped` set).
	conn, err := store.Open(ctx, baseDSN)
	if err != nil {
		return nil, fmt.Errorf("postgres: enumerate tenants: open base store: %w", err)
	}
	defer func() { _ = conn.Close() }()

	db, ok := conn.Raw.(*sql.DB)
	if !ok {
		return nil, fmt.Errorf("postgres: enumerate tenants: unexpected raw handle %T", conn.Raw)
	}
	parsed, err := ParseDSN(baseDSN)
	if err != nil {
		return nil, fmt.Errorf("postgres: enumerate tenants: %w", err)
	}
	ns := parsed.Namespace

	// Distinct real tenants (the reserved __default__ partition is unscoped
	// data, not a tenant, so it is excluded).
	rows, err := db.QueryContext(ctx, fmt.Sprintf(
		`SELECT DISTINCT tenant FROM %s WHERE tenant <> $1 ORDER BY tenant`, qualify(ns, "lessons"),
	), defaultTenant)
	if err != nil {
		return nil, fmt.Errorf("postgres: list tenants: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var tenants []string
	for rows.Next() {
		var tenant string
		if err := rows.Scan(&tenant); err != nil {
			return nil, fmt.Errorf("postgres: scan tenant: %w", err)
		}
		if !tenantRE.MatchString(tenant) {
			// Skip anything that is not a valid tenant id (defense in depth).
			continue
		}
		tenants = append(tenants, tenant)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: iterate tenants: %w", err)
	}

	repo := LessonRepository{db: db, ns: ns}
	scopes := make([]store.TenantScope, 0, len(tenants))
	for _, tenant := range tenants {
		lessons, err := readTenantLessons(ctx, db, repo, ns, tenant)
		if err != nil {
			return nil, fmt.Errorf("postgres: read lessons for tenant %q: %w", tenant, err)
		}
		scopes = append(scopes, store.TenantScope{
			Tenant:  tenant,
			DSN:     store.SetDSNParam(baseDSN, "tenant", tenant),
			Lessons: lessons,
		})
	}
	return scopes, nil
}

// readTenantLessons reads one tenant's lessons with an explicit tenant filter,
// so attribution is correct even under an RLS-bypassing connection. It mirrors
// LessonRepository.ListAll's projection and evidence hydration.
func readTenantLessons(ctx context.Context, db *sql.DB, repo LessonRepository, ns, tenant string) ([]domain.Lesson, error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`
SELECT id, statement, scope_service, scope_env, scope_team, trigger, kind, confidence, derived_at, last_verified, source, created_by, subject_class
FROM %s WHERE tenant = $1 ORDER BY confidence DESC, derived_at DESC`, qualify(ns, "lessons")), tenant)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]domain.Lesson, 0)
	for rows.Next() {
		l, err := scanLessonRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Hydrate evidence per lesson. lesson_evidence rows are keyed by the unique
	// lesson id, so this stays within the tenant even under a bypassing role.
	for i := range out {
		ev, err := repo.ListEvidence(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Evidence = ev
	}
	return out, nil
}
