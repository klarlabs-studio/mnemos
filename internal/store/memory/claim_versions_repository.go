package memory

import (
	"context"
	"sort"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// ClaimVersionRepository is the in-memory implementation of
// [ports.ClaimVersionRepository]. Backed by the shared state.
type ClaimVersionRepository struct {
	state *state
}

// Append writes the next version row for the claim. Version numbers
// are 1-based and monotonic per claim id.
func (r ClaimVersionRepository) Append(_ context.Context, v domain.ClaimVersion) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	existing := r.state.claimVersions[v.ClaimID]
	v.Version = len(existing) + 1
	if v.WrittenAt.IsZero() {
		v.WrittenAt = time.Now().UTC()
	}
	if v.WrittenBy == "" {
		v.WrittenBy = "<system>"
	}
	r.state.claimVersions[v.ClaimID] = append(existing, v)
	return nil
}

// ListByClaim returns every version newest-first.
func (r ClaimVersionRepository) ListByClaim(_ context.Context, claimID string) ([]domain.ClaimVersion, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	existing := r.state.claimVersions[claimID]
	out := make([]domain.ClaimVersion, len(existing))
	copy(out, existing)
	sort.Slice(out, func(i, j int) bool { return out[i].Version > out[j].Version })
	return out, nil
}
