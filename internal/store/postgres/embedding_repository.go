package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
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
	queryModel string,
	topK int,
	minSimilarity float64,
) ([]ports.ClaimSimilarityHit, error) {
	if len(queryVector) == 0 {
		return nil, nil
	}
	if candidateClaimIDs != nil && len(candidateClaimIDs) == 0 {
		return nil, nil
	}
	// The model filter ($2) confines the scan to the query embedder's model
	// space so vectors from a different model are never compared; an empty
	// $2 disables it (single unnamed space — backward compatible).
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(
		`SELECT entity_id, vector, model FROM %s WHERE entity_type = $1 AND ($2 = '' OR model = $2)`,
		qualify(r.ns, "embeddings"),
	), "claim", queryModel)
	if err != nil {
		return nil, fmt.Errorf("list claim embeddings: %w", err)
	}
	defer func() { _ = rows.Close() }()

	hits := make([]ports.ClaimSimilarityHit, 0)
	for rows.Next() {
		var (
			entityID string
			blob     []byte
			rowModel string
		)
		if err := rows.Scan(&entityID, &blob, &rowModel); err != nil {
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
			Model:      rowModel,
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
//
// vectorEnabled is set at Open when the pgvector `vector` type is
// installed and the `embedding` accelerator column exists on the
// embeddings table (see schema.sql $mnemos_pgvector$). When true, Upsert
// mirrors each vector into that column and SearchEventsByVector serves
// recall with the C-native `<=>` operator; when false, the repository
// behaves exactly as before (bytea only) and SearchEventsByVector reports
// ErrVectorSearchUnavailable so the query engine falls back to Go cosine.
type EmbeddingRepository struct {
	db            pgQuerier
	ns            string
	vectorEnabled bool
}

// SearchEventsByVector ranks entity_type='event' embeddings by cosine
// similarity against queryVector using pgvector's `<=>` distance operator,
// returning the top-K hits with similarity >= minSimilarity. Row-level
// security scopes the scan to the connection's tenant transparently.
//
// The operator and type are fully qualified (`OPERATOR(public.<=>)`,
// `::public.vector`) so they resolve regardless of the connection's
// search_path (pinned to the namespace, not public). Rows are filtered to
// the query vector's dimensionality so a mixed-model corpus never trips the
// operator's same-dimension requirement.
//
// Returns ErrVectorSearchUnavailable when the backend has no native vector
// path (extension absent / column not provisioned) — the caller falls back.
func (r EmbeddingRepository) SearchEventsByVector(
	ctx context.Context,
	queryVector []float32,
	model string,
	topK int,
	minSimilarity float64,
) ([]ports.EventSimilarityHit, error) {
	if !r.vectorEnabled {
		return nil, ports.ErrVectorSearchUnavailable
	}
	if len(queryVector) == 0 {
		return nil, nil
	}
	if topK <= 0 {
		topK = 32
	}
	// 1 - cosine_distance = cosine_similarity. Order by distance ascending
	// (nearest first); the LIMIT keeps the scan result bounded. entity_type
	// and dimensions are indexed/cheap filters that also guarantee the
	// operator only ever compares same-length vectors. The model filter
	// ($4) confines the scan to the query embedder's model space so vectors
	// from a different model are never compared; an empty $4 disables it
	// (single unnamed space — backward compatible).
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(
		`SELECT entity_id, model, 1 - (embedding OPERATOR(public.<=>) $1::public.vector) AS similarity
		 FROM %s
		 WHERE entity_type = 'event' AND embedding IS NOT NULL AND dimensions = $2
		   AND ($4 = '' OR model = $4)
		 ORDER BY embedding OPERATOR(public.<=>) $1::public.vector
		 LIMIT $3`,
		qualify(r.ns, "embeddings"),
	), formatVector(queryVector), len(queryVector), topK, model)
	if err != nil {
		return nil, fmt.Errorf("vector search events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	hits := make([]ports.EventSimilarityHit, 0, topK)
	for rows.Next() {
		var (
			entityID string
			model    string
			sim      float64
		)
		if err := rows.Scan(&entityID, &model, &sim); err != nil {
			return nil, fmt.Errorf("scan event vector hit: %w", err)
		}
		if sim < minSimilarity {
			continue
		}
		hits = append(hits, ports.EventSimilarityHit{
			EventID:    entityID,
			Similarity: sim,
			Model:      model,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate event vector hits: %w", err)
	}
	return hits, nil
}

// formatVector renders a float32 slice as a pgvector text literal
// (`[1,2,3]`), suitable for binding as text and casting to `public.vector`.
// strconv with -1 precision emits the shortest round-trippable form, so the
// literal decodes back to the same float32 the bytea path stored.
func formatVector(v []float32) string {
	var b strings.Builder
	b.Grow(len(v)*8 + 2)
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// Upsert satisfies the corresponding ports method. The bytea `vector`
// column is always written (the portable source of truth). When the
// pgvector accelerator column is present, the same vector is mirrored into
// `embedding` in the same statement so the native `<=>` path stays in sync
// with no separate backfill step.
func (r EmbeddingRepository) Upsert(ctx context.Context, entityID, entityType string, vector []float32, model, createdBy string) error {
	blob := embedding.EncodeVector(vector)
	if r.vectorEnabled {
		_, err := r.db.ExecContext(ctx, fmt.Sprintf(`
INSERT INTO %s (entity_id, entity_type, vector, model, dimensions, created_at, created_by, embedding)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8::public.vector)
ON CONFLICT (entity_id, entity_type) DO UPDATE SET
  vector = EXCLUDED.vector,
  model = EXCLUDED.model,
  dimensions = EXCLUDED.dimensions,
  created_at = EXCLUDED.created_at,
  embedding = EXCLUDED.embedding`, qualify(r.ns, "embeddings")),
			entityID, entityType, blob, model, len(vector),
			time.Now().UTC(), actorOr(createdBy), formatVector(vector),
		)
		if err != nil {
			return fmt.Errorf("upsert embedding (pgvector): %w", err)
		}
		return nil
	}
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
