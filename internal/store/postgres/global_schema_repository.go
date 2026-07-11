package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"go.klarlabs.de/mnemos/internal/domain"
)

// GlobalSchemaRepository persists promoted, de-identified schemas (ADR 0011
// Phase B) in the configured Postgres namespace. UNLIKE every per-tenant
// repository, the backing global_schemas table is deliberately NOT tenant-scoped
// (no RLS, no tenant column) — the neocortex is the SHARED tier and must be
// readable across tenants (see schema.sql). This repository therefore issues
// plain SQL with no tenant predicate.
type GlobalSchemaRepository struct {
	db pgQuerier
	ns string
}

// Upsert writes (or replaces) the promoted schema on its id primary key.
func (r GlobalSchemaRepository) Upsert(ctx context.Context, s domain.GlobalSchema) error {
	if err := s.Validate(); err != nil {
		return fmt.Errorf("invalid global schema: %w", err)
	}
	createdBy := s.CreatedBy
	if createdBy == "" {
		createdBy = domain.SystemUser
	}
	_, err := r.db.ExecContext(ctx, fmt.Sprintf(`
INSERT INTO %s (
	id, statement, scope_service, scope_env, scope_team, polarity,
	distinct_tenants, evidence_count, confidence, surprise, has_surprise,
	status, promoted_at, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
ON CONFLICT (id) DO UPDATE SET
	statement = excluded.statement,
	scope_service = excluded.scope_service,
	scope_env = excluded.scope_env,
	scope_team = excluded.scope_team,
	polarity = excluded.polarity,
	distinct_tenants = excluded.distinct_tenants,
	evidence_count = excluded.evidence_count,
	confidence = excluded.confidence,
	surprise = excluded.surprise,
	has_surprise = excluded.has_surprise,
	status = excluded.status,
	promoted_at = excluded.promoted_at,
	created_by = excluded.created_by`, qualify(r.ns, "global_schemas")),
		s.ID, s.Statement, s.Scope.Service, s.Scope.Env, s.Scope.Team, string(s.Polarity),
		s.DistinctTenants, s.EvidenceCount, s.Confidence, s.Surprise, s.HasSurprise,
		string(s.Status), s.PromotedAt.UTC(), createdBy)
	if err != nil {
		return fmt.Errorf("upsert global schema %s: %w", s.ID, err)
	}
	return nil
}

const pgGlobalSchemaCols = `id, statement, scope_service, scope_env, scope_team, polarity,
	distinct_tenants, evidence_count, confidence, surprise, has_surprise,
	status, promoted_at, created_by`

// GetByID returns the schema with the given id, or ok=false when none exists.
func (r GlobalSchemaRepository) GetByID(ctx context.Context, id string) (domain.GlobalSchema, bool, error) {
	row := r.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT %s FROM %s WHERE id = $1`, pgGlobalSchemaCols, qualify(r.ns, "global_schemas")), id)
	s, err := scanPGGlobalSchema(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.GlobalSchema{}, false, nil
	}
	if err != nil {
		return domain.GlobalSchema{}, false, err
	}
	return s, true, nil
}

// ListAll returns every promoted schema, newest promotion first.
func (r GlobalSchemaRepository) ListAll(ctx context.Context) ([]domain.GlobalSchema, error) {
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`SELECT %s FROM %s ORDER BY promoted_at DESC NULLS LAST, id ASC`, pgGlobalSchemaCols, qualify(r.ns, "global_schemas")))
	if err != nil {
		return nil, fmt.Errorf("list global schemas: %w", err)
	}
	return collectPGGlobalSchemas(rows)
}

// ListByStatus returns promoted schemas in the given lifecycle status.
func (r GlobalSchemaRepository) ListByStatus(ctx context.Context, status domain.GlobalSchemaStatus) ([]domain.GlobalSchema, error) {
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`SELECT %s FROM %s WHERE status = $1 ORDER BY promoted_at DESC NULLS LAST, id ASC`, pgGlobalSchemaCols, qualify(r.ns, "global_schemas")), string(status))
	if err != nil {
		return nil, fmt.Errorf("list global schemas by status: %w", err)
	}
	return collectPGGlobalSchemas(rows)
}

// Approve activates a pending schema (pending → active). Returns an
// sql.ErrNoRows-wrapped error when the id does not exist.
func (r GlobalSchemaRepository) Approve(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, fmt.Sprintf(`UPDATE %s SET status = $1 WHERE id = $2`, qualify(r.ns, "global_schemas")), string(domain.GlobalSchemaStatusActive), id)
	if err != nil {
		return fmt.Errorf("approve global schema %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("approve global schema %s: rows affected: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("approve global schema %s: %w", id, sql.ErrNoRows)
	}
	return nil
}

// CountAll returns the total number of promoted schema rows.
func (r GlobalSchemaRepository) CountAll(ctx context.Context) (int64, error) {
	var n int64
	if err := r.db.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s`, qualify(r.ns, "global_schemas"))).Scan(&n); err != nil {
		return 0, fmt.Errorf("count global schemas: %w", err)
	}
	return n, nil
}

// DeleteAll wipes every promoted schema row.
func (r GlobalSchemaRepository) DeleteAll(ctx context.Context) error {
	if _, err := r.db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s`, qualify(r.ns, "global_schemas"))); err != nil {
		return fmt.Errorf("delete all global schemas: %w", err)
	}
	return nil
}

func collectPGGlobalSchemas(rows *sql.Rows) ([]domain.GlobalSchema, error) {
	defer func() { _ = rows.Close() }()
	out := make([]domain.GlobalSchema, 0)
	for rows.Next() {
		s, err := scanPGGlobalSchema(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

type pgRowScanner interface {
	Scan(dest ...any) error
}

func scanPGGlobalSchema(sc pgRowScanner) (domain.GlobalSchema, error) {
	var (
		s          domain.GlobalSchema
		polarity   string
		status     string
		promotedAt sql.NullTime
	)
	if err := sc.Scan(
		&s.ID, &s.Statement, &s.Scope.Service, &s.Scope.Env, &s.Scope.Team, &polarity,
		&s.DistinctTenants, &s.EvidenceCount, &s.Confidence, &s.Surprise, &s.HasSurprise,
		&status, &promotedAt, &s.CreatedBy,
	); err != nil {
		return domain.GlobalSchema{}, err
	}
	s.Polarity = domain.SchemaPolarity(polarity)
	s.Status = domain.GlobalSchemaStatus(status)
	if promotedAt.Valid {
		s.PromotedAt = promotedAt.Time.UTC()
	}
	return s, nil
}
