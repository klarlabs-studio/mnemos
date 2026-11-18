package mnemos

import (
	"context"
	"fmt"

	"go.klarlabs.de/mnemos/internal/browse"
)

// EventListMaxResults caps [Memory.ListEvents], and is what a zero
// [EventListQuery.Limit] means: the listing is reachable from remote
// surfaces, where "no limit" would make a full log dump a one-line request.
const EventListMaxResults = 1000

// EventListQuery selects a page of stored events for [Memory.ListEvents].
type EventListQuery struct {
	// RunID, when set, lists only that run's events.
	RunID string
	// Limit caps the result (0 or above EventListMaxResults means
	// EventListMaxResults).
	Limit int
	// Offset skips that many events of the newest-first listing.
	Offset int
}

// ListEvents implements [Memory.ListEvents]. It is the same browse
// (internal/browse.Events, newest first by timestamp then id) that REST
// GET /v1/episodes and the list_episodes MCP tool page through.
func (m *memory) ListEvents(ctx context.Context, q EventListQuery) ([]Event, error) {
	limit := q.Limit
	if limit <= 0 || limit > EventListMaxResults {
		limit = EventListMaxResults
	}
	offset := max(q.Offset, 0)
	pg, err := browse.Events(ctx, m.conn, q.RunID, nil, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("mnemos: ListEvents: %w", err)
	}
	out := make([]Event, 0, len(pg.Items))
	for _, ev := range pg.Items {
		out = append(out, Event{
			ID:       ev.ID,
			At:       ev.Timestamp,
			Type:     ev.Metadata["event_type"],
			Content:  ev.Content,
			Metadata: copyMetaWithout(ev.Metadata, "event_type"),
			RunID:    ev.RunID,
		})
	}
	return out, nil
}
