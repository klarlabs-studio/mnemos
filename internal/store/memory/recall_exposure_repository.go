package memory

import (
	"context"
	"fmt"

	"go.klarlabs.de/mnemos/internal/domain"
)

// RecordRecallExposures implements [ports.RecallExposureRepository]. The first
// exposure of a (session, belief) is kept.
func (r ClaimRepository) RecordRecallExposures(_ context.Context, exposures []domain.RecallExposure) error {
	for _, x := range exposures {
		if err := x.Validate(); err != nil {
			return fmt.Errorf("invalid recall exposure: %w", err)
		}
	}
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	if r.state.recallExposures == nil {
		r.state.recallExposures = map[[2]string]domain.RecallExposure{}
	}
	for _, x := range exposures {
		key := [2]string{x.SessionID, x.ClaimID}
		if _, ok := r.state.recallExposures[key]; ok {
			continue
		}
		x.ExposedAt = x.ExposedAt.UTC()
		r.state.recallExposures[key] = x
	}
	return nil
}

// ListRecallExposures implements [ports.RecallExposureRepository].
func (r ClaimRepository) ListRecallExposures(_ context.Context, claimIDs []string) ([]domain.RecallExposure, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	return r.recallExposuresLocked(claimIDs), nil
}

// recallExposuresLocked returns the exposures of the given beliefs. Callers
// hold state.mu.
func (r ClaimRepository) recallExposuresLocked(claimIDs []string) []domain.RecallExposure {
	want := make(map[string]bool, len(claimIDs))
	for _, id := range claimIDs {
		want[id] = true
	}
	out := make([]domain.RecallExposure, 0)
	for _, x := range r.state.recallExposures {
		if want[x.ClaimID] {
			out = append(out, x)
		}
	}
	domain.SortRecallExposures(out)
	return out
}
