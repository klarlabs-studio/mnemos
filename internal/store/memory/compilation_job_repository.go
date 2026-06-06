package memory

import (
	"context"
	"database/sql"
	"fmt"

	"go.klarlabs.de/mnemos/internal/domain"
)

// CompilationJobRepository is the in-memory implementation of
// [ports.CompilationJobRepository]. The workflow runner upserts the
// job at every transition, so this is just an id-keyed map.
type CompilationJobRepository struct {
	state *state
}

// Upsert inserts or replaces the job record. Idempotent.
func (r CompilationJobRepository) Upsert(_ context.Context, job domain.CompilationJob) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	r.state.jobs[job.ID] = storedCompilationJob{
		ID:        job.ID,
		Kind:      job.Kind,
		Status:    job.Status,
		Scope:     copyStringMap(job.Scope),
		StartedAt: job.StartedAt.UTC(),
		UpdatedAt: job.UpdatedAt.UTC(),
		Error:     job.Error,
	}
	return nil
}

// GetByID returns the job with the given id or an error wrapping
// sql.ErrNoRows when the id is unknown — matches the SQLite repo's
// surface so callers handle the missing-row case the same way.
func (r CompilationJobRepository) GetByID(_ context.Context, id string) (domain.CompilationJob, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	j, ok := r.state.jobs[id]
	if !ok {
		return domain.CompilationJob{}, fmt.Errorf("compilation job %s: %w", id, sql.ErrNoRows)
	}
	return j.toDomain(), nil
}
