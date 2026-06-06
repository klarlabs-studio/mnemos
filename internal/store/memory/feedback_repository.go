package memory

import (
	"context"

	"go.klarlabs.de/mnemos/internal/domain"
)

// FeedbackRepository is the in-memory implementation of
// [ports.FeedbackRepository]. Backed by the shared state map so a
// memory Conn shares feedback state across every repo handle that
// holds the same state pointer.
type FeedbackRepository struct {
	state *state
}

// Get returns the per-claim feedback state, or ok=false when no row
// has been recorded yet.
func (r FeedbackRepository) Get(_ context.Context, claimID string) (domain.ClaimFeedback, bool, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	row, ok := r.state.feedback[claimID]
	return row, ok, nil
}

// Upsert writes the supplied state under the claim id.
func (r FeedbackRepository) Upsert(_ context.Context, state domain.ClaimFeedback) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	r.state.feedback[state.ClaimID] = state
	return nil
}
