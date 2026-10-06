package browse

import (
	"context"
	"sort"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/store"
)

// EmbeddingKey is an embedding's position in the browse: entity type, entity
// id, model, in that order, ascending. It is the cursor for the embedding
// browse, which pages in Go: listing embeddings already reads by entity type,
// and their volume tracks the belief count, not the edge count.
func EmbeddingKey(r domain.EmbeddingRecord) string {
	return r.EntityType + "\x00" + r.EntityID + "\x00" + r.Model
}

// Embeddings returns one page of the embedding browse for the given entity
// types, after the record whose EmbeddingKey is afterKey ("" for the first).
func Embeddings(ctx context.Context, conn *store.Conn, entityTypes []string, afterKey string, limit, offset int) (ports.Page[domain.EmbeddingRecord], error) {
	var all []domain.EmbeddingRecord
	for _, t := range entityTypes {
		recs, err := conn.Embeddings.ListByEntityType(ctx, t)
		if err != nil {
			return ports.Page[domain.EmbeddingRecord]{}, err
		}
		all = append(all, recs...)
	}
	sort.Slice(all, func(i, j int) bool { return EmbeddingKey(all[i]) < EmbeddingKey(all[j]) })
	start := offset
	if afterKey != "" {
		start = sort.Search(len(all), func(i int) bool { return EmbeddingKey(all[i]) > afterKey })
	}
	items, more := window(all, start, limit)
	return ports.Page[domain.EmbeddingRecord]{Items: items, Total: len(all), More: more}, nil
}
