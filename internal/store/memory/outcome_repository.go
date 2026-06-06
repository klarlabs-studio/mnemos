package memory

import (
	"context"
	"fmt"

	"go.klarlabs.de/mnemos/internal/domain"
)

// OutcomeRepository is the in-memory implementation of
// [ports.OutcomeRepository]. Outcome insertion does NOT enforce the
// SQLite FK contract — callers are expected to insert the parent
// Action first; isolated tests that skip that step continue to work.
type OutcomeRepository struct {
	state *state
}

// Append validates and stores an outcome. Idempotent on id.
func (r OutcomeRepository) Append(_ context.Context, outcome domain.Outcome) error {
	if err := outcome.Validate(); err != nil {
		return fmt.Errorf("invalid outcome: %w", err)
	}
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	if _, exists := r.state.outcomes[outcome.ID]; exists {
		return nil
	}
	r.state.outcomes[outcome.ID] = storedOutcomeFromDomain(outcome)
	r.state.outcomeOrder = append(r.state.outcomeOrder, outcome.ID)
	return nil
}

// GetByID returns the outcome with the given id.
func (r OutcomeRepository) GetByID(_ context.Context, id string) (domain.Outcome, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	o, ok := r.state.outcomes[id]
	if !ok {
		return domain.Outcome{}, fmt.Errorf("outcome %s not found", id)
	}
	return o.toDomain(), nil
}

// ListByActionID returns the outcomes reported for the given action id.
func (r OutcomeRepository) ListByActionID(_ context.Context, actionID string) ([]domain.Outcome, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	out := make([]domain.Outcome, 0)
	for _, id := range r.state.outcomeOrder {
		o, ok := r.state.outcomes[id]
		if !ok || o.ActionID != actionID {
			continue
		}
		out = append(out, o.toDomain())
	}
	sortOutcomesByObservedAt(out)
	return out, nil
}

// ListAll returns every outcome ordered by observed_at ascending.
func (r OutcomeRepository) ListAll(_ context.Context) ([]domain.Outcome, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	out := make([]domain.Outcome, 0, len(r.state.outcomeOrder))
	for _, id := range r.state.outcomeOrder {
		if o, ok := r.state.outcomes[id]; ok {
			out = append(out, o.toDomain())
		}
	}
	sortOutcomesByObservedAt(out)
	return out, nil
}

// CountAll returns the total number of outcomes stored.
func (r OutcomeRepository) CountAll(_ context.Context) (int64, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	return int64(len(r.state.outcomes)), nil
}

// DeleteAll wipes every outcome row.
func (r OutcomeRepository) DeleteAll(_ context.Context) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	r.state.outcomes = map[string]storedOutcome{}
	r.state.outcomeOrder = nil
	return nil
}

func sortOutcomesByObservedAt(out []domain.Outcome) {
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].ObservedAt.After(out[j].ObservedAt); j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
}
