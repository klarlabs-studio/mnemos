package memory

import (
	"context"
	"fmt"

	"go.klarlabs.de/mnemos/internal/domain"
)

// AppendPending implements [ports.EventDependencyRepository.AppendPending].
func (r EventDependencyRepository) AppendPending(_ context.Context, refs []domain.PendingReference) error {
	for _, p := range refs {
		if err := p.Validate(); err != nil {
			return fmt.Errorf("invalid pending reference: %w", err)
		}
	}
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	if r.state.pendingRefs == nil {
		r.state.pendingRefs = map[[2]string]domain.PendingReference{}
	}
	for _, p := range refs {
		key := [2]string{p.EventID, p.LookupKey}
		if _, ok := r.state.pendingRefs[key]; ok {
			continue
		}
		p.CreatedAt = p.CreatedAt.UTC()
		r.state.pendingRefs[key] = p
	}
	return nil
}

// ListPendingByKeys implements [ports.EventDependencyRepository.ListPendingByKeys].
func (r EventDependencyRepository) ListPendingByKeys(_ context.Context, keys []string) ([]domain.PendingReference, error) {
	want := make(map[string]bool, len(keys))
	for _, k := range keys {
		want[k] = true
	}
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	out := make([]domain.PendingReference, 0)
	for _, p := range r.state.pendingRefs {
		if want[p.LookupKey] {
			out = append(out, p)
		}
	}
	domain.SortPendingReferences(out)
	return out, nil
}

// DeletePending implements [ports.EventDependencyRepository.DeletePending].
func (r EventDependencyRepository) DeletePending(_ context.Context, refs []domain.PendingReference) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	for _, p := range refs {
		delete(r.state.pendingRefs, [2]string{p.EventID, p.LookupKey})
	}
	return nil
}

// dropPendingLocked removes every pending reference eventID made. Callers
// hold state.mu.
func (s *state) dropPendingLocked(eventID string) {
	for k := range s.pendingRefs {
		if k[0] == eventID {
			delete(s.pendingRefs, k)
		}
	}
}
