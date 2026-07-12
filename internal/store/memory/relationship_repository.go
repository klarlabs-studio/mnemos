package memory

import (
	"context"
	"fmt"
	"sort"

	"go.klarlabs.de/mnemos/internal/domain"
)

// RelationshipRepository is the in-memory implementation of
// [ports.RelationshipRepository]. The (id) is the dedup key — re-
// upserting the same id replaces the existing row, matching SQLite's
// ON CONFLICT(id) DO UPDATE semantics.
type RelationshipRepository struct {
	state *state
}

// Upsert validates and stores the relationships. Self-references are
// rejected at the domain layer via Relationship.Validate, and that
// check is repeated here for storage-boundary safety.
func (r RelationshipRepository) Upsert(_ context.Context, relationships []domain.Relationship) error {
	if len(relationships) == 0 {
		return nil
	}
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	for _, rel := range relationships {
		if err := rel.Validate(); err != nil {
			return fmt.Errorf("invalid relationship %s: %w", rel.ID, err)
		}
		r.state.relationships[rel.ID] = storedRelationship{
			ID:          rel.ID,
			Type:        rel.Type,
			FromClaimID: rel.FromClaimID,
			ToClaimID:   rel.ToClaimID,
			CreatedAt:   rel.CreatedAt.UTC(),
			CreatedBy:   actorOr(rel.CreatedBy),
			Strength:    rel.Strength,
		}
	}
	return nil
}

// ListByClaim returns every relationship that touches the given claim
// (as source or target).
func (r RelationshipRepository) ListByClaim(_ context.Context, claimID string) ([]domain.Relationship, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	out := make([]domain.Relationship, 0)
	for _, rel := range r.state.relationships {
		if rel.FromClaimID == claimID || rel.ToClaimID == claimID {
			out = append(out, rel.toDomain())
		}
	}
	return out, nil
}

// RepointEndpoint rewrites every relationship whose endpoints
// equal oldID to point at newID. Self-loops and unique-edge
// duplicates (same type + from + to) are dropped.
func (r RelationshipRepository) RepointEndpoint(_ context.Context, oldID, newID string) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	type edgeKey struct{ Type, From, To string }
	// Build the set of edges that already exist after rewrites,
	// keyed on (type, from, to). When a rewrite would introduce a
	// duplicate, drop the redundant row.
	existing := make(map[edgeKey]string, len(r.state.relationships))
	for id, rel := range r.state.relationships {
		from, to := rel.FromClaimID, rel.ToClaimID
		if from == oldID {
			from = newID
		}
		if to == oldID {
			to = newID
		}
		if from == to {
			delete(r.state.relationships, id)
			continue
		}
		key := edgeKey{Type: string(rel.Type), From: from, To: to}
		if _, dup := existing[key]; dup {
			delete(r.state.relationships, id)
			continue
		}
		existing[key] = id
		rel.FromClaimID = from
		rel.ToClaimID = to
		r.state.relationships[id] = rel
	}
	return nil
}

// DeleteByClaim removes every relationship that touches claimID.
func (r RelationshipRepository) DeleteByClaim(_ context.Context, claimID string) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	for id, rel := range r.state.relationships {
		if rel.FromClaimID == claimID || rel.ToClaimID == claimID {
			delete(r.state.relationships, id)
		}
	}
	return nil
}

// ListByClaimIDs returns every relationship touching any of the given
// claim ids. Used by hop-expansion in the query engine.
func (r RelationshipRepository) ListByClaimIDs(_ context.Context, claimIDs []string) ([]domain.Relationship, error) {
	if len(claimIDs) == 0 {
		return []domain.Relationship{}, nil
	}
	wanted := make(map[string]struct{}, len(claimIDs))
	for _, id := range claimIDs {
		wanted[id] = struct{}{}
	}
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	out := make([]domain.Relationship, 0)
	for _, rel := range r.state.relationships {
		_, fromHit := wanted[rel.FromClaimID]
		_, toHit := wanted[rel.ToClaimID]
		if fromHit || toHit {
			out = append(out, rel.toDomain())
		}
	}
	return out, nil
}

// CountAll returns the total number of relationships stored.
func (r RelationshipRepository) CountAll(_ context.Context) (int64, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	return int64(len(r.state.relationships)), nil
}

// CountByType returns the number of relationships with the given type.
func (r RelationshipRepository) CountByType(_ context.Context, relType string) (int64, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	var n int64
	for _, rel := range r.state.relationships {
		if string(rel.Type) == relType {
			n++
		}
	}
	return n, nil
}

// DeleteAll wipes the relationships map.
func (r RelationshipRepository) DeleteAll(_ context.Context) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	r.state.relationships = map[string]storedRelationship{}
	return nil
}

// ListAll returns every relationship stored, ordered by created_at
// ascending (matching SQLite).
func (r RelationshipRepository) ListAll(_ context.Context) ([]domain.Relationship, error) {
	r.state.mu.RLock()
	defer r.state.mu.RUnlock()
	out := make([]domain.Relationship, 0, len(r.state.relationships))
	for _, rel := range r.state.relationships {
		out = append(out, rel.toDomain())
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

func (s storedRelationship) toDomain() domain.Relationship {
	return domain.Relationship{
		ID:          s.ID,
		Type:        s.Type,
		FromClaimID: s.FromClaimID,
		ToClaimID:   s.ToClaimID,
		CreatedAt:   s.CreatedAt,
		CreatedBy:   s.CreatedBy,
		Strength:    s.Strength,
	}
}

// StrengthenAssociations implements [ports.RelationshipStrengthener] (ADR 0015 §4):
// it raises the strength of every stored edge whose BOTH endpoints are in claimIDs
// (either direction) by delta, capped at maxStrength. The current strength is read
// through the base-1.0 convention (a zero/unset weight counts as 1) so a pre-strength
// edge lands at 1+delta, matching the SQL backends' DEFAULT 1. Only existing edges are
// touched. Returns the number strengthened.
func (r RelationshipRepository) StrengthenAssociations(_ context.Context, claimIDs []string, delta, maxStrength float64) (int, error) {
	if delta <= 0 || len(claimIDs) < 2 {
		return 0, nil
	}
	wanted := make(map[string]struct{}, len(claimIDs))
	for _, id := range claimIDs {
		wanted[id] = struct{}{}
	}
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	n := 0
	for id, rel := range r.state.relationships {
		_, fromHit := wanted[rel.FromClaimID]
		_, toHit := wanted[rel.ToClaimID]
		if !fromHit || !toHit {
			continue
		}
		s := rel.Strength
		if s <= 0 {
			s = 1 // base convention, matches domain.Association.EffectiveStrength
		}
		s += delta
		if s > maxStrength {
			s = maxStrength
		}
		rel.Strength = s
		r.state.relationships[id] = rel
		n++
	}
	return n, nil
}

// DecayAssociations implements [ports.RelationshipStrengthener] (ADR 0015 §5): it pulls
// every over-base edge's strength toward the base 1.0, keeping the given fraction of
// its excess. Base edges (stored 0 or 1) are untouched and no edge is deleted.
func (r RelationshipRepository) DecayAssociations(_ context.Context, retain float64) (int, error) {
	if retain < 0 || retain >= 1 {
		return 0, nil
	}
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	n := 0
	for id, rel := range r.state.relationships {
		if rel.Strength <= 1 {
			continue // base / unset — nothing to decay
		}
		rel.Strength = 1 + (rel.Strength-1)*retain
		r.state.relationships[id] = rel
		n++
	}
	return n, nil
}
