package memory

import (
	"context"
	"sort"

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

// ListBySourceInputIDs returns the events whose source_input_id is one of ids,
// in insertion order (ports.EventSourceLister).
func (r EventRepository) ListBySourceInputIDs(_ context.Context, ids []string) ([]domain.Event, error) {
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	out := make([]domain.Event, 0)
	for _, id := range r.state.eventOrder {
		if e, ok := r.state.events[id]; ok && want[e.SourceInputID] {
			out = append(out, e.toDomain())
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].IngestedAt.Equal(out[j].IngestedAt) {
			return out[i].IngestedAt.Before(out[j].IngestedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}
