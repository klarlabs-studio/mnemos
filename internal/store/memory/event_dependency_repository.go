package memory

import (
	"context"
	"fmt"

	"go.klarlabs.de/mnemos/internal/domain"
)

// EventDependencyRepository is the in-memory store of source dependence
// between episodes (ADR 0038).
type EventDependencyRepository struct {
	state *state
}

// Append implements [ports.EventDependencyRepository.Append].
func (r EventDependencyRepository) Append(_ context.Context, deps []domain.EventDependency) error {
	for _, d := range deps {
		if err := d.Validate(); err != nil {
			return fmt.Errorf("invalid event dependency: %w", err)
		}
	}
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	seen := make(map[[2]string]bool, len(r.state.dependencies))
	for _, d := range r.state.dependencies {
		seen[[2]string{d.EventID, d.DependsOn}] = true
	}
	for _, d := range deps {
		key := [2]string{d.EventID, d.DependsOn}
		if seen[key] {
			continue
		}
		seen[key] = true
		d.CreatedAt = d.CreatedAt.UTC()
		d.CreatedBy = actorOr(d.CreatedBy)
		r.state.dependencies = append(r.state.dependencies, d)
	}
	return nil
}

// ListByEventIDs implements [ports.EventDependencyRepository.ListByEventIDs].
func (r EventDependencyRepository) ListByEventIDs(_ context.Context, ids []string) ([]domain.EventDependency, error) {
	return r.matching(ids, func(d domain.EventDependency) string { return d.EventID }), nil
}

// ListDependents implements [ports.EventDependencyRepository.ListDependents].
func (r EventDependencyRepository) ListDependents(_ context.Context, ids []string) ([]domain.EventDependency, error) {
	return r.matching(ids, func(d domain.EventDependency) string { return d.DependsOn }), nil
}

func (r EventDependencyRepository) matching(ids []string, key func(domain.EventDependency) string) []domain.EventDependency {
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	out := make([]domain.EventDependency, 0)
	for _, d := range r.state.dependencies {
		if want[key(d)] {
			out = append(out, d)
		}
	}
	return out
}

// dropDependenciesLocked removes every dependency drop matches. Callers hold
// state.mu.
func (s *state) dropDependenciesLocked(drop func(domain.EventDependency) bool) {
	kept := s.dependencies[:0]
	for _, d := range s.dependencies {
		if !drop(d) {
			kept = append(kept, d)
		}
	}
	s.dependencies = kept
}
