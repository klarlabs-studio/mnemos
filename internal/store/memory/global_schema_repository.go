package memory

import (
	"context"
	"database/sql"
	"fmt"
	"sort"

	"go.klarlabs.de/mnemos/internal/domain"
)

// GlobalSchemaRepository is the in-memory implementation of
// [ports.GlobalSchemaRepository] — the neocortex (shared/global) tier. It is
// deliberately not tenant-scoped: promoted schemas are the shared tier.
type GlobalSchemaRepository struct {
	state *state
}

// Upsert writes (or replaces) the promoted schema under its id.
func (r GlobalSchemaRepository) Upsert(_ context.Context, s domain.GlobalSchema) error {
	if err := s.Validate(); err != nil {
		return fmt.Errorf("invalid global schema: %w", err)
	}
	if s.CreatedBy == "" {
		s.CreatedBy = domain.SystemUser
	}
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	r.state.globalSchemas[s.ID] = s
	return nil
}

// GetByID returns the schema with the given id, or ok=false when none exists.
func (r GlobalSchemaRepository) GetByID(_ context.Context, id string) (domain.GlobalSchema, bool, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	s, ok := r.state.globalSchemas[id]
	return s, ok, nil
}

// ListAll returns every promoted schema, newest promotion first.
func (r GlobalSchemaRepository) ListAll(_ context.Context) ([]domain.GlobalSchema, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	return r.sortedLocked(func(domain.GlobalSchema) bool { return true }), nil
}

// ListByStatus returns promoted schemas in the given lifecycle status.
func (r GlobalSchemaRepository) ListByStatus(_ context.Context, status domain.GlobalSchemaStatus) ([]domain.GlobalSchema, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	return r.sortedLocked(func(s domain.GlobalSchema) bool { return s.Status == status }), nil
}

// Approve activates a pending schema (pending → active).
func (r GlobalSchemaRepository) Approve(_ context.Context, id string) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	s, ok := r.state.globalSchemas[id]
	if !ok {
		return fmt.Errorf("approve global schema %s: %w", id, sql.ErrNoRows)
	}
	s.Status = domain.GlobalSchemaStatusActive
	r.state.globalSchemas[id] = s
	return nil
}

// CountAll returns the total number of promoted schema rows.
func (r GlobalSchemaRepository) CountAll(_ context.Context) (int64, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	return int64(len(r.state.globalSchemas)), nil
}

// DeleteAll wipes every promoted schema row.
func (r GlobalSchemaRepository) DeleteAll(_ context.Context) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	r.state.globalSchemas = map[string]domain.GlobalSchema{}
	return nil
}

// sortedLocked returns the schemas matching keep, ordered by promoted_at
// descending then id ascending. Callers hold state.mu.
func (r GlobalSchemaRepository) sortedLocked(keep func(domain.GlobalSchema) bool) []domain.GlobalSchema {
	out := make([]domain.GlobalSchema, 0, len(r.state.globalSchemas))
	for _, s := range r.state.globalSchemas {
		if keep(s) {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].PromotedAt.Equal(out[j].PromotedAt) {
			return out[i].PromotedAt.After(out[j].PromotedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}
