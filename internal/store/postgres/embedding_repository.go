package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/embedding"
	"go.klarlabs.de/mnemos/internal/ports"
)

// SearchClaimsByVector ranks entity_type='claim' embeddings by cosine
// similarity against queryVector. The scoring is performed in Go since
// the embedding column is stored as raw bytea; a future pgvector
// migration can push the cosine into SQL without changing the port
// contract.
//
// candidateClaimIDs acts as a hard allowlist when non-nil; an empty
// (but non-nil) map matches nothing, by design.
func (r EmbeddingRepository) SearchClaimsByVector(
	ctx context.Context,
	queryVector []float32,
	candidateClaimIDs map[string]struct{},
	topK int,
	minSimilarity float64,
) ([]ports.ClaimSimilarityHit, error) {
	if len(queryVector) == 0 {
		return nil, nil
	}
	if candidateClaimIDs != nil && len(candidateClaimIDs) == 0 {
		return nil, nil
	}
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(
		`SELECT entity_id, vector, model FROM %s WHERE entity_type = $1`,
		qualify(r.ns, "embeddings"),
	), "claim")
	if err != nil {
		return nil, fmt.Errorf("list claim embeddings: %w", err)
	}
	defer func() { _ = rows.Close() }()

	hits := make([]ports.ClaimSimilarityHit, 0)
	for rows.Next() {
		var (
			entityID string
			blob     []byte
			model    string
		)
		if err := rows.Scan(&entityID, &blob, &model); err != nil {
			return nil, fmt.Errorf("scan claim embedding: %w", err)
		}
		if candidateClaimIDs != nil {
			if _, ok := candidateClaimIDs[entityID]; !ok {
				continue
			}
		}
		vec, err := embedding.DecodeVector(blob)
		if err != nil {
			continue
		}
		sim, err := embedding.CosineSimilarity(queryVector, vec)
		if err != nil {
			continue
		}
		score := float64(sim)
		if score < minSimilarity {
			continue
		}
		hits = append(hits, ports.ClaimSimilarityHit{
			ClaimID:    entityID,
			Similarity: score,
			Model:      model,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate claim embeddings: %w", err)
	}
	for i := 1; i < len(hits); i++ {
		for j := i; j > 0 && hits[j].Similarity > hits[j-1].Similarity; j-- {
			hits[j-1], hits[j] = hits[j], hits[j-1]
		}
	}
	if topK > 0 && len(hits) > topK {
		hits = hits[:topK]
	}
	return hits, nil
}

// EmbeddingRepository persists vector embeddings as bytea, using the
// same little-endian float32 encoding as the SQLite backend
// (internal/embedding.EncodeVector / DecodeVector). This keeps
// federation push/pull byte-compatible across providers.
type EmbeddingRepository struct {
	db *sql.DB
	ns string
}

// Upsert satisfies the corresponding ports method.
func (r EmbeddingRepository) Upsert(ctx context.Context, entityID, entityType string, vector []float32, model, createdBy string) error {
	blob := embedding.EncodeVector(vector)
	_, err := r.db.ExecContext(ctx, fmt.Sprintf(`
INSERT INTO %s (entity_id, entity_type, vector, model, dimensions, created_at, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (entity_id, entity_type) DO UPDATE SET
  vector = EXCLUDED.vector,
  model = EXCLUDED.model,
  dimensions = EXCLUDED.dimensions,
  created_at = EXCLUDED.created_at`, qualify(r.ns, "embeddings")),
		entityID, entityType, blob, model, len(vector),
		time.Now().UTC(), actorOr(createdBy),
	)
	if err != nil {
		return fmt.Errorf("upsert embedding: %w", err)
	}
	return nil
}

// Delete removes the embedding for (entityID, entityType). Idempotent.
func (r EmbeddingRepository) Delete(ctx context.Context, entityID, entityType string) error {
	if _, err := r.db.ExecContext(ctx,
		fmt.Sprintf(`DELETE FROM %s WHERE entity_id = $1 AND entity_type = $2`, qualify(r.ns, "embeddings")),
		entityID, entityType,
	); err != nil {
		return fmt.Errorf("delete embedding (%s, %s): %w", entityID, entityType, err)
	}
	return nil
}

// ListByEntityType satisfies the corresponding ports method.
func (r EmbeddingRepository) ListByEntityType(ctx context.Context, entityType string) ([]domain.EmbeddingRecord, error) {
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`
SELECT entity_id, entity_type, vector, model, dimensions, created_at, created_by
FROM %s WHERE entity_type = $1`, qualify(r.ns, "embeddings")), entityType)
	if err != nil {
		return nil, fmt.Errorf("list embeddings by type: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectEmbeddingRows(rows)
}

// CountAll satisfies the corresponding ports method.
func (r EmbeddingRepository) CountAll(ctx context.Context) (int64, error) {
	var n int64
	if err := r.db.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT COUNT(*) FROM %s`, qualify(r.ns, "embeddings"),
	)).Scan(&n); err != nil {
		return 0, fmt.Errorf("count embeddings: %w", err)
	}
	return n, nil
}

// DeleteAll satisfies the corresponding ports method.
func (r EmbeddingRepository) DeleteAll(ctx context.Context) error {
	if _, err := r.db.ExecContext(ctx, fmt.Sprintf(
		`DELETE FROM %s`, qualify(r.ns, "embeddings"),
	)); err != nil {
		return fmt.Errorf("delete all embeddings: %w", err)
	}
	return nil
}

// ListAll satisfies the corresponding ports method.
func (r EmbeddingRepository) ListAll(ctx context.Context) ([]domain.EmbeddingRecord, error) {
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`
SELECT entity_id, entity_type, vector, model, dimensions, created_at, created_by
FROM %s ORDER BY created_at ASC`, qualify(r.ns, "embeddings")))
	if err != nil {
		return nil, fmt.Errorf("list all embeddings: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectEmbeddingRows(rows)
}

func collectEmbeddingRows(rows *sql.Rows) ([]domain.EmbeddingRecord, error) {
	out := make([]domain.EmbeddingRecord, 0)
	for rows.Next() {
		var rec domain.EmbeddingRecord
		var vec []byte
		var dims int64
		if err := rows.Scan(&rec.EntityID, &rec.EntityType, &vec, &rec.Model, &dims, &rec.CreatedAt, &rec.CreatedBy); err != nil {
			return nil, fmt.Errorf("scan embedding row: %w", err)
		}
		v, err := embedding.DecodeVector(vec)
		if err != nil {
			return nil, fmt.Errorf("decode embedding: %w", err)
		}
		rec.Vector = v
		rec.Dimensions = int(dims)
		out = append(out, rec)
	}
	return out, rows.Err()
}
