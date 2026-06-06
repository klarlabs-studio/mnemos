package memory

import (
	"context"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// ActionRepository is the in-memory implementation of
// [ports.ActionRepository]. Append is idempotent on the primary key
// to mirror the SQLite ON CONFLICT DO NOTHING contract.
type ActionRepository struct {
	state *state
}

// Append validates and stores an action. Idempotent on id (mirrors
// the SQLite ON CONFLICT DO NOTHING contract).
func (r ActionRepository) Append(_ context.Context, action domain.Action) error {
	if err := action.Validate(); err != nil {
		return fmt.Errorf("invalid action: %w", err)
	}
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	if _, exists := r.state.actions[action.ID]; exists {
		return nil
	}
	r.state.actions[action.ID] = storedActionFromDomain(action)
	r.state.actionOrder = append(r.state.actionOrder, action.ID)
	return nil
}

// GetByID returns the action with the given id or an error if not found.
func (r ActionRepository) GetByID(_ context.Context, id string) (domain.Action, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	a, ok := r.state.actions[id]
	if !ok {
		return domain.Action{}, fmt.Errorf("action %s not found", id)
	}
	return a.toDomain(), nil
}

// ListByRunID returns actions tagged with the given run id.
func (r ActionRepository) ListByRunID(_ context.Context, runID string) ([]domain.Action, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	out := make([]domain.Action, 0)
	for _, id := range r.state.actionOrder {
		a, ok := r.state.actions[id]
		if !ok || a.RunID != runID {
			continue
		}
		out = append(out, a.toDomain())
	}
	return out, nil
}

// ListBySubject returns actions taken on the given operational subject.
func (r ActionRepository) ListBySubject(_ context.Context, subject string) ([]domain.Action, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	out := make([]domain.Action, 0)
	for _, id := range r.state.actionOrder {
		a, ok := r.state.actions[id]
		if !ok || a.Subject != subject {
			continue
		}
		out = append(out, a.toDomain())
	}
	return out, nil
}

// ListAll returns every action ordered by action time ascending.
func (r ActionRepository) ListAll(_ context.Context) ([]domain.Action, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	out := make([]domain.Action, 0, len(r.state.actionOrder))
	for _, id := range r.state.actionOrder {
		if a, ok := r.state.actions[id]; ok {
			out = append(out, a.toDomain())
		}
	}
	// SQLite orders by at ASC; mirror that for portable callers.
	sortActionsByAt(out)
	return out, nil
}

// CountAll returns the total number of actions stored.
func (r ActionRepository) CountAll(_ context.Context) (int64, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	return int64(len(r.state.actions)), nil
}

// DeleteAll wipes every action row.
func (r ActionRepository) DeleteAll(_ context.Context) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	r.state.actions = map[string]storedAction{}
	r.state.actionOrder = nil
	return nil
}

func sortActionsByAt(out []domain.Action) {
	// Insertion sort suffices: tests stay tiny and readers don't have
	// to reach for sort.Slice tear-downs.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].At.After(out[j].At); j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	_ = time.Now // keep import non-empty if helpers grow; harmless
}
