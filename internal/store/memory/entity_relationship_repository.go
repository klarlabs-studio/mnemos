package memory

import (
	"context"
	"fmt"
	"sort"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// EntityRelationshipRepository is the in-memory polymorphic-edge
// store. Idempotent on the (kind, from_type, from_id, to_type, to_id)
// tuple.
type EntityRelationshipRepository struct {
	state *state
}

type entityRelKey struct {
	Kind, FromType, FromID, ToType, ToID string
}

// Upsert is idempotent on the composite key.
func (r EntityRelationshipRepository) Upsert(_ context.Context, edges []domain.EntityRelationship) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	for _, e := range edges {
		if err := e.Validate(); err != nil {
			return fmt.Errorf("invalid entity_relationship: %w", err)
		}
		key := entityRelKey{
			Kind: string(e.Kind), FromType: e.FromType, FromID: e.FromID,
			ToType: e.ToType, ToID: e.ToID,
		}
		if _, exists := r.state.entityRelKey[key]; exists {
			continue
		}
		createdAt := e.CreatedAt
		if createdAt.IsZero() {
			createdAt = time.Now().UTC()
		}
		stored := domain.EntityRelationship{
			ID: e.ID, Kind: e.Kind,
			FromID: e.FromID, FromType: e.FromType,
			ToID: e.ToID, ToType: e.ToType,
			CreatedAt: createdAt.UTC(),
			CreatedBy: actorOr(e.CreatedBy),
		}
		r.state.entityRels[e.ID] = stored
		r.state.entityRelOrder = append(r.state.entityRelOrder, e.ID)
		r.state.entityRelKey[key] = e.ID
	}
	return nil
}

// ListByEntity returns edges touching the given (id, type) on either side.
func (r EntityRelationshipRepository) ListByEntity(_ context.Context, entityID, entityType string) ([]domain.EntityRelationship, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	out := make([]domain.EntityRelationship, 0)
	for _, id := range r.state.entityRelOrder {
		e, ok := r.state.entityRels[id]
		if !ok {
			continue
		}
		if (e.FromID == entityID && e.FromType == entityType) || (e.ToID == entityID && e.ToType == entityType) {
			out = append(out, e)
		}
	}
	sortEntityRels(out)
	return out, nil
}

// ListByKind returns edges with the given kind.
func (r EntityRelationshipRepository) ListByKind(_ context.Context, kind string) ([]domain.EntityRelationship, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	out := make([]domain.EntityRelationship, 0)
	for _, id := range r.state.entityRelOrder {
		e, ok := r.state.entityRels[id]
		if !ok || string(e.Kind) != kind {
			continue
		}
		out = append(out, e)
	}
	sortEntityRels(out)
	return out, nil
}

// ListAll returns every edge.
func (r EntityRelationshipRepository) ListAll(_ context.Context) ([]domain.EntityRelationship, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	out := make([]domain.EntityRelationship, 0, len(r.state.entityRelOrder))
	for _, id := range r.state.entityRelOrder {
		if e, ok := r.state.entityRels[id]; ok {
			out = append(out, e)
		}
	}
	sortEntityRels(out)
	return out, nil
}

// CountAll returns the total number of edges.
func (r EntityRelationshipRepository) CountAll(_ context.Context) (int64, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	return int64(len(r.state.entityRels)), nil
}

// DeleteAll wipes every edge.
func (r EntityRelationshipRepository) DeleteAll(_ context.Context) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	r.state.entityRels = map[string]domain.EntityRelationship{}
	r.state.entityRelOrder = nil
	r.state.entityRelKey = map[entityRelKey]string{}
	return nil
}

func sortEntityRels(out []domain.EntityRelationship) {
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
}
