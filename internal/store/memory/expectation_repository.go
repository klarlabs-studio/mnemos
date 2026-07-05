package memory

import (
	"context"
	"sort"

	"go.klarlabs.de/mnemos/internal/domain"
)

// ExpectationRepository is the in-memory implementation of
// [ports.ExpectationRepository].
type ExpectationRepository struct {
	state *state
}

// Upsert writes the expectation under its claim id.
func (r ExpectationRepository) Upsert(_ context.Context, exp domain.Expectation) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	r.state.expectations[exp.ClaimID] = exp
	return nil
}

// Get returns the expectation for claimID, or ok=false when none exists.
func (r ExpectationRepository) Get(_ context.Context, claimID string) (domain.Expectation, bool, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	e, ok := r.state.expectations[claimID]
	return e, ok, nil
}

// ListOpen returns every unresolved expectation, claim-id ordered.
func (r ExpectationRepository) ListOpen(_ context.Context) ([]domain.Expectation, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	out := make([]domain.Expectation, 0)
	for _, e := range r.state.expectations {
		if !e.Resolved {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ClaimID < out[j].ClaimID })
	return out, nil
}
