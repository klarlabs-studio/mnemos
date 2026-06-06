package memory

import (
	"context"
	"fmt"

	"go.klarlabs.de/mnemos/internal/domain"
)

// EventRepository is the in-memory implementation of
// [ports.EventRepository]. Events are append-only at the SQLite layer;
// here we mimic that by rejecting Append on an existing id with a
// clear error.
type EventRepository struct {
	state *state
}

// Append validates and stores an event. Mirrors the SQLite repo's
// rejection of malformed events at the storage boundary.
func (r EventRepository) Append(_ context.Context, event domain.Event) error {
	if err := event.Validate(); err != nil {
		return fmt.Errorf("invalid event: %w", err)
	}
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	if _, exists := r.state.events[event.ID]; exists {
		// SQLite uses INSERT … ON CONFLICT(id) DO NOTHING after the
		// reliability sweep so retried pushes are idempotent. Mirror
		// that semantics: re-appending the same id is a no-op, not an
		// error.
		return nil
	}
	r.state.events[event.ID] = storedEventFromDomain(event)
	r.state.eventOrder = append(r.state.eventOrder, event.ID)
	return nil
}

// GetByID returns the event with the given id or an error if not found.
func (r EventRepository) GetByID(_ context.Context, id string) (domain.Event, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	e, ok := r.state.events[id]
	if !ok {
		return domain.Event{}, fmt.Errorf("event %s not found", id)
	}
	return e.toDomain(), nil
}

// ListByIDs returns events matching the given ids, preserving caller-
// supplied order. Missing ids are silently dropped (parity with SQLite).
func (r EventRepository) ListByIDs(_ context.Context, ids []string) ([]domain.Event, error) {
	if len(ids) == 0 {
		return []domain.Event{}, nil
	}
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	out := make([]domain.Event, 0, len(ids))
	for _, id := range ids {
		if e, ok := r.state.events[id]; ok {
			out = append(out, e.toDomain())
		}
	}
	return out, nil
}

// ListAll returns every event in insertion order.
func (r EventRepository) ListAll(_ context.Context) ([]domain.Event, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	out := make([]domain.Event, 0, len(r.state.eventOrder))
	for _, id := range r.state.eventOrder {
		if e, ok := r.state.events[id]; ok {
			out = append(out, e.toDomain())
		}
	}
	return out, nil
}

// CountAll returns the total number of events stored.
func (r EventRepository) CountAll(_ context.Context) (int64, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	return int64(len(r.state.events)), nil
}

// DeleteByID removes the event with the given id. Idempotent.
func (r EventRepository) DeleteByID(_ context.Context, id string) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	if _, ok := r.state.events[id]; !ok {
		return nil
	}
	delete(r.state.events, id)
	r.state.eventOrder = removeStringFromSlice(r.state.eventOrder, id)
	return nil
}

// DeleteAll wipes the events map.
func (r EventRepository) DeleteAll(_ context.Context) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	r.state.events = map[string]storedEvent{}
	r.state.eventOrder = nil
	return nil
}

// ListByRunID returns every event tagged with the given run id, in
// insertion order.
func (r EventRepository) ListByRunID(_ context.Context, runID string) ([]domain.Event, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	out := make([]domain.Event, 0)
	for _, id := range r.state.eventOrder {
		e, ok := r.state.events[id]
		if !ok {
			continue
		}
		if e.RunID == runID {
			out = append(out, e.toDomain())
		}
	}
	return out, nil
}
