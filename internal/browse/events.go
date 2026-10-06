package browse

import (
	"context"
	"errors"
	"sort"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/page"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/store"
)

// Events returns one page of the episode browse, newest first by (timestamp,
// id), in the store when it can page (ports.EventPager) and otherwise in Go.
// The legacy offset form always pages in Go.
func Events(ctx context.Context, conn *store.Conn, runID string, after *page.Key, limit, offset int) (ports.Page[domain.Event], error) {
	if offset == 0 {
		if p, ok := conn.Events.(ports.EventPager); ok {
			pg, err := p.PageEvents(ctx, runID, after, limit)
			if !errors.Is(err, ports.ErrPageUnsupported) {
				return pg, err
			}
		}
	}
	return EventsInGo(ctx, conn, runID, after, limit, offset)
}

// EventsInGo pages over the full list: the fallback and the definition the
// store pagers are tested against.
func EventsInGo(ctx context.Context, conn *store.Conn, runID string, after *page.Key, limit, offset int) (ports.Page[domain.Event], error) {
	var all []domain.Event
	var err error
	if runID != "" {
		all, err = conn.Events.ListByRunID(ctx, runID)
	} else {
		all, err = conn.Events.ListAll(ctx)
	}
	if err != nil {
		return ports.Page[domain.Event]{}, err
	}
	sort.Slice(all, func(i, j int) bool {
		return page.Newer(all[i].Timestamp, all[i].ID, all[j].Timestamp, all[j].ID)
	})
	start := offset
	if after != nil {
		start = sort.Search(len(all), func(i int) bool { return after.After(all[i].Timestamp, all[i].ID) })
	}
	items, more := window(all, start, limit)
	return ports.Page[domain.Event]{Items: items, Total: len(all), More: more}, nil
}
