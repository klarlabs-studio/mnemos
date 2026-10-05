package scalebench

import (
	"context"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/store"
)

// LoadResult reports how a corpus was loaded.
type LoadResult struct {
	Stats   Stats         `json:"stats"`
	Elapsed time.Duration `json:"elapsed_ns"`
}

// Load generates the corpus described by p and writes it to the store at dsn
// through the storage ports, in batches of batchSize beliefs. Order within a
// batch is episodes, beliefs, evidence, associations, so every foreign key
// points at a row that already exists.
//
// Trust is not computed here. Bulk-loaded beliefs carry a zero trust_score
// until the caller runs the official recompute, which is itself one of the
// measured operations.
func Load(ctx context.Context, dsn string, p Params, batchSize int, progress func(Stats)) (LoadResult, error) {
	g, err := NewGenerator(p)
	if err != nil {
		return LoadResult{}, err
	}
	conn, err := store.Open(ctx, dsn)
	if err != nil {
		return LoadResult{}, fmt.Errorf("scalebench: open %s: %w", dsn, err)
	}
	defer func() { _ = conn.Close() }()

	start := time.Now()
	for !g.Done() {
		if err := ctx.Err(); err != nil {
			return LoadResult{}, err
		}
		b := g.Next(batchSize)
		for _, ev := range b.Events {
			if err := conn.Events.Append(ctx, ev); err != nil {
				return LoadResult{}, fmt.Errorf("scalebench: append episode %s: %w", ev.ID, err)
			}
		}
		if err := conn.Claims.Upsert(ctx, b.Claims); err != nil {
			return LoadResult{}, fmt.Errorf("scalebench: upsert beliefs: %w", err)
		}
		if err := conn.Claims.UpsertEvidence(ctx, b.Evidence); err != nil {
			return LoadResult{}, fmt.Errorf("scalebench: upsert evidence: %w", err)
		}
		if len(b.Relationships) > 0 {
			if err := conn.Relationships.Upsert(ctx, b.Relationships); err != nil {
				return LoadResult{}, fmt.Errorf("scalebench: upsert associations: %w", err)
			}
		}
		if progress != nil {
			progress(g.Stats())
		}
	}
	return LoadResult{Stats: g.Stats(), Elapsed: time.Since(start)}, nil
}
