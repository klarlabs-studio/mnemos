package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"go.klarlabs.de/mnemos/internal/domain"
)

// CompilationJobRepository persists workflow job state. The runner
// upserts at every status transition, so this is just an id-keyed
// table.
type CompilationJobRepository struct {
	db *sql.DB
	ns string
}

// Upsert satisfies the corresponding ports method.
func (r CompilationJobRepository) Upsert(ctx context.Context, job domain.CompilationJob) error {
	scopeJSON, err := json.Marshal(job.Scope)
	if err != nil {
		return fmt.Errorf("marshal job scope: %w", err)
	}
	_, err = r.db.ExecContext(ctx, fmt.Sprintf(`
INSERT INTO %s (id, kind, status, scope_json, started_at, updated_at, error)
VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7)
ON CONFLICT (id) DO UPDATE SET
  kind = EXCLUDED.kind,
  status = EXCLUDED.status,
  scope_json = EXCLUDED.scope_json,
  started_at = EXCLUDED.started_at,
  updated_at = EXCLUDED.updated_at,
  error = EXCLUDED.error`, qualify(r.ns, "compilation_jobs")),
		job.ID, job.Kind, job.Status, string(scopeJSON),
		job.StartedAt.UTC(), job.UpdatedAt.UTC(), job.Error,
	)
	if err != nil {
		return fmt.Errorf("upsert compilation job %s: %w", job.ID, err)
	}
	return nil
}

// GetByID satisfies the corresponding ports method.
func (r CompilationJobRepository) GetByID(ctx context.Context, id string) (domain.CompilationJob, error) {
	row := r.db.QueryRowContext(ctx, fmt.Sprintf(`
SELECT id, kind, status, scope_json::text, started_at, updated_at, error
FROM %s WHERE id = $1`, qualify(r.ns, "compilation_jobs")), id)

	var job domain.CompilationJob
	var scopeRaw string
	err := row.Scan(&job.ID, &job.Kind, &job.Status, &scopeRaw, &job.StartedAt, &job.UpdatedAt, &job.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.CompilationJob{}, fmt.Errorf("compilation job %s: %w", id, sql.ErrNoRows)
	}
	if err != nil {
		return domain.CompilationJob{}, fmt.Errorf("get compilation job %s: %w", id, err)
	}
	if err := json.Unmarshal([]byte(scopeRaw), &job.Scope); err != nil {
		return domain.CompilationJob{}, fmt.Errorf("unmarshal job scope: %w", err)
	}
	return job, nil
}
