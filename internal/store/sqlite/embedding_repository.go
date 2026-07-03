package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/embedding"
	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/store/sqlite/sqlcgen"
)

// SearchClaimsByVector ranks entity_type='claim' embeddings by cosine
// similarity against queryVector and returns up to topK hits with
// similarity >= minSimilarity, ordered by similarity desc. Scoring is
// performed in Go after the rows are fetched — sqlite has no native
// vector ops; a future sqlite-vec backend can push this down without
// changing the port contract.
//
// candidateClaimIDs acts as a hard allowlist when non-nil; an empty
// (but non-nil) map matches nothing, by design (a tenant scoped to
// zero claims must not see a corpus-wide dump).
func (r EmbeddingRepository) SearchClaimsByVector(
	ctx context.Context,
	queryVector []float32,
	candidateClaimIDs map[string]struct{},
	model string,
	topK int,
	minSimilarity float64,
) ([]ports.ClaimSimilarityHit, error) {
	if len(queryVector) == 0 {
		return nil, nil
	}
	if candidateClaimIDs != nil && len(candidateClaimIDs) == 0 {
		return nil, nil
	}
	rows, err := r.q.ListEmbeddingsByEntityType(ctx, "claim")
	if err != nil {
		return nil, fmt.Errorf("list claim embeddings: %w", err)
	}
	hits := make([]ports.ClaimSimilarityHit, 0, len(rows))
	for _, row := range rows {
		if candidateClaimIDs != nil {
			if _, ok := candidateClaimIDs[row.EntityID]; !ok {
				continue
			}
		}
		// Confine to the query embedder's model space (empty = no filter).
		if model != "" && row.Model != model {
			continue
		}
		vec, err := embedding.DecodeVector(row.Vector)
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
			ClaimID:    row.EntityID,
			Similarity: score,
			Model:      row.Model,
		})
	}
	sortHitsDesc(hits)
	if topK > 0 && len(hits) > topK {
		hits = hits[:topK]
	}
	return hits, nil
}

// EmbeddingRepository provides SQLite-backed storage for vector embeddings.
type EmbeddingRepository struct {
	db *sql.DB
	q  *sqlcgen.Queries
}

// NewEmbeddingRepository returns an EmbeddingRepository backed by the given database.
func NewEmbeddingRepository(db *sql.DB) EmbeddingRepository {
	return EmbeddingRepository{db: db, q: sqlcgen.New(db)}
}

// Upsert stores or updates a vector embedding for the given entity.
// An empty createdBy is recorded as domain.SystemUser via actorOr.
func (r EmbeddingRepository) Upsert(ctx context.Context, entityID, entityType string, vector []float32, model, createdBy string) error {
	blob := embedding.EncodeVector(vector)
	return r.q.UpsertEmbedding(ctx, sqlcgen.UpsertEmbeddingParams{
		EntityID:   entityID,
		EntityType: entityType,
		Vector:     blob,
		Model:      model,
		Dimensions: int64(len(vector)),
		CreatedAt:  time.Now().UTC().Format(time.RFC3339Nano),
		CreatedBy:  actorOr(createdBy),
	})
}

// GetByEntityID retrieves a single embedding by entity ID and type.
func (r EmbeddingRepository) GetByEntityID(ctx context.Context, entityID, entityType string) (domain.EmbeddingRecord, error) {
	row, err := r.q.GetEmbeddingByEntityID(ctx, sqlcgen.GetEmbeddingByEntityIDParams{
		EntityID:   entityID,
		EntityType: entityType,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.EmbeddingRecord{}, fmt.Errorf("embedding not found for %s/%s", entityID, entityType)
		}
		return domain.EmbeddingRecord{}, fmt.Errorf("get embedding: %w", err)
	}
	return mapSQLEmbedding(row)
}

// Delete removes the embedding for (entityID, entityType). Idempotent.
func (r EmbeddingRepository) Delete(ctx context.Context, entityID, entityType string) error {
	return r.q.DeleteEmbeddingByEntity(ctx, sqlcgen.DeleteEmbeddingByEntityParams{
		EntityID:   entityID,
		EntityType: entityType,
	})
}

// ListByEntityType returns all embeddings of the given type (e.g. "event").
func (r EmbeddingRepository) ListByEntityType(ctx context.Context, entityType string) ([]domain.EmbeddingRecord, error) {
	rows, err := r.q.ListEmbeddingsByEntityType(ctx, entityType)
	if err != nil {
		return nil, fmt.Errorf("list embeddings by type: %w", err)
	}

	records := make([]domain.EmbeddingRecord, 0, len(rows))
	for _, row := range rows {
		rec, err := mapSQLEmbedding(row)
		if err != nil {
			return nil, err
		}
		records = append(records, rec)
	}
	return records, nil
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

// ListAll returns every embedding row, ordered by created_at ascending.
func (r EmbeddingRepository) ListAll(ctx context.Context) ([]domain.EmbeddingRecord, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT entity_id, entity_type, vector, model, dimensions, created_at, created_by
		 FROM embeddings
		 ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list all embeddings: %w", err)
	}
	defer closeRows(rows)

	out := make([]domain.EmbeddingRecord, 0)
	for rows.Next() {
		var (
			entityID, entityType, model, createdAt, createdBy string
			blob                                              []byte
			dims                                              int64
		)
		if err := rows.Scan(&entityID, &entityType, &blob, &model, &dims, &createdAt, &createdBy); err != nil {
			return nil, fmt.Errorf("scan embedding row: %w", err)
		}
		vec, err := embedding.DecodeVector(blob)
		if err != nil {
			return nil, fmt.Errorf("decode embedding vector for %s/%s: %w", entityID, entityType, err)
		}
		t, err := time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse embedding created_at: %w", err)
		}
		out = append(out, domain.EmbeddingRecord{
			EntityID:   entityID,
			EntityType: entityType,
			Vector:     vec,
			Model:      model,
			Dimensions: int(dims),
			CreatedAt:  t,
			CreatedBy:  createdBy,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate embedding rows: %w", err)
	}
	return out, nil
}

func sortHitsDesc(hits []ports.ClaimSimilarityHit) {
	for i := 1; i < len(hits); i++ {
		for j := i; j > 0 && hits[j].Similarity > hits[j-1].Similarity; j-- {
			hits[j-1], hits[j] = hits[j], hits[j-1]
		}
	}
}

func mapSQLEmbedding(row sqlcgen.Embedding) (domain.EmbeddingRecord, error) {
	vector, err := embedding.DecodeVector(row.Vector)
	if err != nil {
		return domain.EmbeddingRecord{}, fmt.Errorf("decode embedding vector: %w", err)
	}
	createdAt, err := time.Parse(time.RFC3339Nano, row.CreatedAt)
	if err != nil {
		return domain.EmbeddingRecord{}, fmt.Errorf("parse embedding created_at: %w", err)
	}
	return domain.EmbeddingRecord{
		EntityID:   row.EntityID,
		EntityType: row.EntityType,
		Vector:     vector,
		Model:      row.Model,
		Dimensions: int(row.Dimensions),
		CreatedAt:  createdAt,
		CreatedBy:  row.CreatedBy,
	}, nil
}
