package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// GlobalSchemaRepository is the SQLite-backed implementation of
// [ports.GlobalSchemaRepository] — the neocortex (shared/global) tier written
// by the ADR 0011 consolidation pass. It stores only de-identified fields:
// there is no tenant column and no evidence-id table, by design.
type GlobalSchemaRepository struct {
	db *sql.DB
}

// NewGlobalSchemaRepository returns a repository bound to the given *sql.DB.
func NewGlobalSchemaRepository(db *sql.DB) GlobalSchemaRepository {
	return GlobalSchemaRepository{db: db}
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
	_, err := r.db.ExecContext(ctx, `
INSERT INTO global_schemas (
	id, statement, scope_service, scope_env, scope_team, polarity,
	distinct_tenants, evidence_count, confidence, surprise, has_surprise,
	status, promoted_at, created_by)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
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
	created_by = excluded.created_by
`,
		s.ID, s.Statement, s.Scope.Service, s.Scope.Env, s.Scope.Team, string(s.Polarity),
		s.DistinctTenants, s.EvidenceCount, s.Confidence, s.Surprise, boolToInt(s.HasSurprise),
		string(s.Status), s.PromotedAt.UTC().Format(time.RFC3339Nano), createdBy)
	if err != nil {
		return fmt.Errorf("upsert global schema %s: %w", s.ID, err)
	}
	return nil
}

const globalSchemaCols = `id, statement, scope_service, scope_env, scope_team, polarity,
	distinct_tenants, evidence_count, confidence, surprise, has_surprise,
	status, promoted_at, created_by`

// GetByID returns the schema with the given id, or ok=false when none exists.
func (r GlobalSchemaRepository) GetByID(ctx context.Context, id string) (domain.GlobalSchema, bool, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+globalSchemaCols+` FROM global_schemas WHERE id = ?`, id)
	s, err := scanGlobalSchema(row)
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
	rows, err := r.db.QueryContext(ctx, `SELECT `+globalSchemaCols+` FROM global_schemas ORDER BY promoted_at DESC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list global schemas: %w", err)
	}
	return collectGlobalSchemas(rows)
}

// ListByStatus returns promoted schemas in the given lifecycle status.
func (r GlobalSchemaRepository) ListByStatus(ctx context.Context, status domain.GlobalSchemaStatus) ([]domain.GlobalSchema, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+globalSchemaCols+` FROM global_schemas WHERE status = ? ORDER BY promoted_at DESC, id ASC`, string(status))
	if err != nil {
		return nil, fmt.Errorf("list global schemas by status: %w", err)
	}
	return collectGlobalSchemas(rows)
}

// Approve activates a pending schema (pending → active). Returns an
// sql.ErrNoRows-wrapped error when the id does not exist.
func (r GlobalSchemaRepository) Approve(ctx context.Context, id string) error {
	res, err := r.db.ExecContext(ctx, `UPDATE global_schemas SET status = ? WHERE id = ?`, string(domain.GlobalSchemaStatusActive), id)
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
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM global_schemas`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count global schemas: %w", err)
	}
	return n, nil
}

// DeleteAll wipes every promoted schema row.
func (r GlobalSchemaRepository) DeleteAll(ctx context.Context) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM global_schemas`); err != nil {
		return fmt.Errorf("delete all global schemas: %w", err)
	}
	return nil
}

func collectGlobalSchemas(rows *sql.Rows) ([]domain.GlobalSchema, error) {
	defer func() { _ = rows.Close() }()
	out := make([]domain.GlobalSchema, 0)
	for rows.Next() {
		s, err := scanGlobalSchema(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func scanGlobalSchema(sc rowScanner) (domain.GlobalSchema, error) {
	var (
		s           domain.GlobalSchema
		polarity    string
		status      string
		hasSurprise int
		promotedAt  string
	)
	if err := sc.Scan(
		&s.ID, &s.Statement, &s.Scope.Service, &s.Scope.Env, &s.Scope.Team, &polarity,
		&s.DistinctTenants, &s.EvidenceCount, &s.Confidence, &s.Surprise, &hasSurprise,
		&status, &promotedAt, &s.CreatedBy,
	); err != nil {
		return domain.GlobalSchema{}, err
	}
	s.Polarity = domain.SchemaPolarity(polarity)
	s.Status = domain.GlobalSchemaStatus(status)
	s.HasSurprise = hasSurprise != 0
	if promotedAt != "" {
		parsed, err := time.Parse(time.RFC3339Nano, promotedAt)
		if err != nil {
			return domain.GlobalSchema{}, fmt.Errorf("parse global schema promoted_at: %w", err)
		}
		s.PromotedAt = parsed
	}
	return s, nil
}
