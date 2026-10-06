package browse

import (
	"context"
	"errors"
	"sort"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/store"
)

// Relationships returns one page of the association browse, by id ascending,
// in the store when it can page (ports.RelationshipPager) and otherwise in
// Go. The legacy offset form always pages in Go.
func Relationships(ctx context.Context, conn *store.Conn, relType, afterID string, limit, offset int) (ports.Page[domain.Relationship], error) {
	if offset == 0 {
		if p, ok := conn.Relationships.(ports.RelationshipPager); ok {
			pg, err := p.PageRelationships(ctx, relType, afterID, limit)
			if !errors.Is(err, ports.ErrPageUnsupported) {
				return pg, err
			}
		}
	}
	return RelationshipsInGo(ctx, conn, relType, afterID, limit, offset)
}

// RelationshipsInGo pages over the full list: the fallback and the definition
// the store pagers are tested against.
func RelationshipsInGo(ctx context.Context, conn *store.Conn, relType, afterID string, limit, offset int) (ports.Page[domain.Relationship], error) {
	all, err := conn.Relationships.ListAll(ctx)
	if err != nil {
		return ports.Page[domain.Relationship]{}, err
	}
	filtered := all[:0]
	for _, r := range all {
		if relType == "" || string(r.Type) == relType {
			filtered = append(filtered, r)
		}
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].ID < filtered[j].ID })
	start := offset
	if afterID != "" {
		start = sort.Search(len(filtered), func(i int) bool { return filtered[i].ID > afterID })
	}
	items, more := window(filtered, start, limit)
	return ports.Page[domain.Relationship]{Items: items, Total: len(filtered), More: more}, nil
}
