package memory

import (
	"context"
	"sort"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/embedding"
	"go.klarlabs.de/mnemos/internal/ports"
)

// EmbeddingRepository is the in-memory implementation of
// [ports.EmbeddingRepository]. Vectors are stored as []float32 — no
// little-endian BLOB encoding needed because we never round-trip
// through SQL.
type EmbeddingRepository struct {
	state *state
}

// Upsert stores or replaces the embedding for (entityID, entityType).
// An empty createdBy is recorded as domain.SystemUser.
func (r EmbeddingRepository) Upsert(_ context.Context, entityID, entityType string, vector []float32, model, createdBy string) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	r.state.embeddings[embeddingKey{EntityID: entityID, EntityType: entityType}] = storedEmbedding{
		EntityID:   entityID,
		EntityType: entityType,
		Vector:     copyFloat32Slice(vector),
		Model:      model,
		Dimensions: len(vector),
		CreatedAt:  time.Now().UTC(),
		CreatedBy:  actorOr(createdBy),
	}
	return nil
}

// Delete removes the embedding for (entityID, entityType). Idempotent.
func (r EmbeddingRepository) Delete(_ context.Context, entityID, entityType string) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	delete(r.state.embeddings, embeddingKey{EntityID: entityID, EntityType: entityType})
	return nil
}

// ListByEntityType returns every stored embedding whose entity type
// matches.
func (r EmbeddingRepository) ListByEntityType(_ context.Context, entityType string) ([]domain.EmbeddingRecord, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	out := make([]domain.EmbeddingRecord, 0)
	for k, v := range r.state.embeddings {
		if k.EntityType != entityType {
			continue
		}
		out = append(out, domain.EmbeddingRecord{
			EntityID:   v.EntityID,
			EntityType: v.EntityType,
			Vector:     copyFloat32Slice(v.Vector),
			Model:      v.Model,
			Dimensions: v.Dimensions,
			CreatedAt:  v.CreatedAt,
			CreatedBy:  v.CreatedBy,
		})
	}
	return out, nil
}

// CountAll returns the total number of embedding rows stored.
func (r EmbeddingRepository) CountAll(_ context.Context) (int64, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	return int64(len(r.state.embeddings)), nil
}

// DeleteAll wipes the embeddings map.
func (r EmbeddingRepository) DeleteAll(_ context.Context) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	r.state.embeddings = map[embeddingKey]storedEmbedding{}
	return nil
}

// SearchClaimsByVector ranks entity_type='claim' embeddings by cosine
// similarity against queryVector and returns up to topK hits with
// similarity >= minSimilarity, ordered by similarity desc.
//
// When candidateClaimIDs is non-nil it acts as a hard allowlist —
// claims not in the set are skipped before scoring. The handler uses
// this to enforce tenant scoping (e.g. via run_id → events →
// claim_evidence) so the searcher never has to know about RunIDs.
// candidateClaimIDs of len 0 (non-nil empty map) matches nothing, by
// design: a tenant whose run_id resolves to zero events must NOT see a
// corpus-wide dump.
func (r EmbeddingRepository) SearchClaimsByVector(
	_ context.Context,
	queryVector []float32,
	candidateClaimIDs map[string]struct{},
	topK int,
	minSimilarity float64,
) ([]ports.ClaimSimilarityHit, error) {
	if len(queryVector) == 0 {
		return nil, nil
	}
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()

	hits := make([]ports.ClaimSimilarityHit, 0)
	for k, v := range r.state.embeddings {
		if k.EntityType != "claim" {
			continue
		}
		if candidateClaimIDs != nil {
			if _, ok := candidateClaimIDs[k.EntityID]; !ok {
				continue
			}
		}
		sim, err := embedding.CosineSimilarity(queryVector, v.Vector)
		if err != nil {
			continue
		}
		score := float64(sim)
		if score < minSimilarity {
			continue
		}
		hits = append(hits, ports.ClaimSimilarityHit{
			ClaimID:    k.EntityID,
			Similarity: score,
			Model:      v.Model,
		})
	}
	sort.Slice(hits, func(i, j int) bool {
		return hits[i].Similarity > hits[j].Similarity
	})
	if topK > 0 && len(hits) > topK {
		hits = hits[:topK]
	}
	return hits, nil
}

// ListAll returns every embedding row, ordered by created_at
// ascending (matching SQLite).
func (r EmbeddingRepository) ListAll(_ context.Context) ([]domain.EmbeddingRecord, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	out := make([]domain.EmbeddingRecord, 0, len(r.state.embeddings))
	for _, v := range r.state.embeddings {
		out = append(out, domain.EmbeddingRecord{
			EntityID:   v.EntityID,
			EntityType: v.EntityType,
			Vector:     copyFloat32Slice(v.Vector),
			Model:      v.Model,
			Dimensions: v.Dimensions,
			CreatedAt:  v.CreatedAt,
			CreatedBy:  v.CreatedBy,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}
