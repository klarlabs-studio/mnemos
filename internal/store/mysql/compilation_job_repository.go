package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"go.klarlabs.de/mnemos/internal/domain"
)

// CompilationJobRepository implements ports.CompilationJobRepository.
type CompilationJobRepository struct {
	db *sql.DB
}

// Upsert inserts or replaces a job.
func (r CompilationJobRepository) Upsert(ctx context.Context, job domain.CompilationJob) error {
	scopeJSON, err := json.Marshal(job.Scope)
	if err != nil {
		return fmt.Errorf("marshal job scope: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `
INSERT INTO compilation_jobs (id, kind, status, scope_json, started_at, updated_at, error)
VALUES (?, ?, ?, CAST(? AS JSON), ?, ?, ?)
ON DUPLICATE KEY UPDATE
  kind = VALUES(kind),
  status = VALUES(status),
  scope_json = VALUES(scope_json),
  started_at = VALUES(started_at),
  updated_at = VALUES(updated_at),
  error = VALUES(error)`,
		job.ID, job.Kind, job.Status, string(scopeJSON),
		job.StartedAt.UTC(), job.UpdatedAt.UTC(), job.Error,
	)
	if err != nil {
		return fmt.Errorf("upsert compilation job %s: %w", job.ID, err)
	}
	return nil
}

// GetByID fetches a job by id.
func (r CompilationJobRepository) GetByID(ctx context.Context, id string) (domain.CompilationJob, error) {
	row := r.db.QueryRowContext(ctx, `
SELECT id, kind, status, scope_json, started_at, updated_at, error
FROM compilation_jobs WHERE id = ?`, id)
	var job domain.CompilationJob
	var scopeRaw []byte
	err := row.Scan(&job.ID, &job.Kind, &job.Status, &scopeRaw, &job.StartedAt, &job.UpdatedAt, &job.Error)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.CompilationJob{}, fmt.Errorf("compilation job %s: %w", id, sql.ErrNoRows)
	}
	if err != nil {
		return domain.CompilationJob{}, fmt.Errorf("get compilation job %s: %w", id, err)
	}
	if err := json.Unmarshal(scopeRaw, &job.Scope); err != nil {
		return domain.CompilationJob{}, fmt.Errorf("unmarshal job scope: %w", err)
	}
	return job, nil
}
