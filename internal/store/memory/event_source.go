package memory

import (
	"context"

	"go.klarlabs.de/mnemos/internal/domain"
)

// ListBySourcePath returns the events ingested from the file at path, matched
// on the `input_source_path` metadata key, in insertion order
// (ports.EventSourceLister).
func (r EventRepository) ListBySourcePath(_ context.Context, path string) ([]domain.Event, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	out := make([]domain.Event, 0)
	for _, id := range r.state.eventOrder {
		e, ok := r.state.events[id]
		if !ok {
			continue
		}
		ev := e.toDomain()
		if ev.Metadata["input_source_path"] == path {
			out = append(out, ev)
		}
	}
	return out, nil
}
