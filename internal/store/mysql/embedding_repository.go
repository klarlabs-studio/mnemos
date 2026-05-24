package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/felixgeelhaar/mnemos/internal/domain"
	"github.com/felixgeelhaar/mnemos/internal/embedding"
	"github.com/felixgeelhaar/mnemos/internal/ports"
)

// SearchClaimsByVector ranks entity_type='claim' embeddings by cosine
// similarity against queryVector. Scoring is performed in Go since the
// vector is stored as a raw LONGBLOB; behaviour mirrors the sqlite and
// postgres backends.
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
	rows, err := r.db.QueryContext(ctx, `SELECT entity_id, vector, model FROM embeddings WHERE entity_type = ?`, "claim")
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

// EmbeddingRepository persists vector embeddings as LONGBLOB using
// the same little-endian float32 encoding as SQLite/Postgres so
// federation push/pull stays byte-compatible across backends.
type EmbeddingRepository struct {
	db *sql.DB
}

// Upsert stores or replaces an embedding for (entityID, entityType).
func (r EmbeddingRepository) Upsert(ctx context.Context, entityID, entityType string, vector []float32, model, createdBy string) error {
	blob := embedding.EncodeVector(vector)
	_, err := r.db.ExecContext(ctx, `
INSERT INTO embeddings (entity_id, entity_type, vector, model, dimensions, created_at, created_by)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
  vector = VALUES(vector),
  model = VALUES(model),
  dimensions = VALUES(dimensions),
  created_at = VALUES(created_at)`,
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
		`DELETE FROM embeddings WHERE entity_id = ? AND entity_type = ?`,
		entityID, entityType,
	); err != nil {
		return fmt.Errorf("delete embedding (%s, %s): %w", entityID, entityType, err)
	}
	return nil
}

// ListByEntityType returns every embedding whose type matches.
func (r EmbeddingRepository) ListByEntityType(ctx context.Context, entityType string) ([]domain.EmbeddingRecord, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT entity_id, entity_type, vector, model, dimensions, created_at, created_by
FROM embeddings WHERE entity_type = ?`, entityType)
	if err != nil {
		return nil, fmt.Errorf("list embeddings by type: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectEmbeddingRows(rows)
}

// CountAll returns the total number of embedding rows stored.
func (r EmbeddingRepository) CountAll(ctx context.Context) (int64, error) {
	var n int64
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM embeddings`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count embeddings: %w", err)
	}
	return n, nil
}

// DeleteAll wipes the embeddings table.
func (r EmbeddingRepository) DeleteAll(ctx context.Context) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM embeddings`); err != nil {
		return fmt.Errorf("delete all embeddings: %w", err)
	}
	return nil
}

// ListAll returns every embedding row ordered by created_at ascending.
func (r EmbeddingRepository) ListAll(ctx context.Context) ([]domain.EmbeddingRecord, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT entity_id, entity_type, vector, model, dimensions, created_at, created_by
FROM embeddings ORDER BY created_at ASC`)
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
