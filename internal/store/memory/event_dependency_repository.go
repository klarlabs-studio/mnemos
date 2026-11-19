package memory

import (
	"context"
	"fmt"
	"time"

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
	return r.matching(ids, func(d domain.EventDependency) string { return d.EventID }, false), nil
}

// ListDependents implements [ports.EventDependencyRepository.ListDependents].
func (r EventDependencyRepository) ListDependents(_ context.Context, ids []string) ([]domain.EventDependency, error) {
	return r.matching(ids, func(d domain.EventDependency) string { return d.DependsOn }, false), nil
}

// ListByEventIDsWithRemoved implements [ports.EventDependencyRepository.ListByEventIDsWithRemoved].
func (r EventDependencyRepository) ListByEventIDsWithRemoved(_ context.Context, ids []string) ([]domain.EventDependency, error) {
	return r.matching(ids, func(d domain.EventDependency) string { return d.EventID }, true), nil
}

// ListDependentsWithRemoved implements [ports.EventDependencyRepository.ListDependentsWithRemoved].
func (r EventDependencyRepository) ListDependentsWithRemoved(_ context.Context, ids []string) ([]domain.EventDependency, error) {
	return r.matching(ids, func(d domain.EventDependency) string { return d.DependsOn }, true), nil
}

// Declare implements [ports.EventDependencyRepository.Declare].
func (r EventDependencyRepository) Declare(_ context.Context, d domain.EventDependency) (domain.EventDependency, error) {
	if err := d.Validate(); err != nil {
		return domain.EventDependency{}, fmt.Errorf("invalid event dependency: %w", err)
	}
	d.CreatedAt = d.CreatedAt.UTC()
	d.CreatedBy = actorOr(d.CreatedBy)
	d.RemovedAt, d.RemovedBy, d.RemovalReason = nil, "", ""
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	for i, cur := range r.state.dependencies {
		if cur.EventID != d.EventID || cur.DependsOn != d.DependsOn {
			continue
		}
		if cur.Live() {
			return domain.EventDependency{}, fmt.Errorf("%w: %s -> %s", domain.ErrDependencyExists, d.EventID, d.DependsOn)
		}
		// The tombstone is a closed interval of the pair's record: keep it.
		r.state.dependencyHistory = append(r.state.dependencyHistory, cur)
		r.state.dependencies[i] = d
		return d, nil
	}
	r.state.dependencies = append(r.state.dependencies, d)
	return d, nil
}

// Remove implements [ports.EventDependencyRepository.Remove].
func (r EventDependencyRepository) Remove(_ context.Context, eventID, dependsOn, removedBy, reason string, at time.Time) (domain.EventDependency, error) {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	for i, cur := range r.state.dependencies {
		if cur.EventID != eventID || cur.DependsOn != dependsOn || !cur.Live() {
			continue
		}
		removed := at.UTC()
		cur.RemovedAt, cur.RemovedBy, cur.RemovalReason = &removed, actorOr(removedBy), reason
		r.state.dependencies[i] = cur
		return cur, nil
	}
	return domain.EventDependency{}, fmt.Errorf("%w: %s -> %s", domain.ErrDependencyNotFound, eventID, dependsOn)
}

func (r EventDependencyRepository) matching(ids []string, key func(domain.EventDependency) string, withRemoved bool) []domain.EventDependency {
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	out := make([]domain.EventDependency, 0)
	for _, d := range r.state.dependencies {
		if want[key(d)] && (withRemoved || d.Live()) {
			out = append(out, d)
		}
	}
	if withRemoved {
		for _, d := range r.state.dependencyHistory {
			if want[key(d)] {
				out = append(out, d)
			}
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
	history := s.dependencyHistory[:0]
	for _, d := range s.dependencyHistory {
		if !drop(d) {
			history = append(history, d)
		}
	}
	s.dependencyHistory = history
}
