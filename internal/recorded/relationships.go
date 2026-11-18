package recorded

import (
	"context"
	"fmt"
	"sort"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
)

// Relationships hides edges created after At and puts edges that a merge moved
// back on the claim that held them at At.
//
// Semantic dedupe repoints a loser's edges onto the winner in place: the edge
// keeps its id and created_at, only the endpoint changes. An endpoint recorded
// after the edge was created cannot be the edge's original endpoint, so the
// edge must have arrived by a merge; the view moves it back to the claim
// merged into that endpoint which existed when the edge was created (the
// oldest, when several did), as that claim stood at At. An edge whose
// endpoint already existed when it was created stays where it is: it may have
// been the endpoint's own or a loser's, and nothing recorded says which.
//
// Edges a merge dropped (a self-loop between loser and winner, or a duplicate
// of an edge the winner already had) are closed, not deleted (ADR 0036 §8): an
// edge closed after At is in the view, with the endpoints it had when it was
// closed, and goes through the same rewind.
type Relationships struct {
	ports.RelationshipRepository
	// Claims is the unbounded claim repository the merge walk reads.
	Claims ports.ClaimRepository
	At     time.Time
}

func (r Relationships) claims() Claims {
	return Claims{ClaimRepository: r.Claims, At: r.At}
}

// rewind keeps the edges created by At, with merged endpoints moved back.
func (r Relationships) rewind(ctx context.Context, rels []domain.Relationship) ([]domain.Relationship, error) {
	kept := rels[:0:0]
	endpointSet := map[string]bool{}
	for _, rel := range rels {
		if rel.CreatedAt.After(r.At) {
			continue
		}
		kept = append(kept, rel)
		endpointSet[rel.FromClaimID] = true
		endpointSet[rel.ToClaimID] = true
	}
	if len(kept) == 0 || r.Claims == nil {
		return kept, nil
	}
	ids := make([]string, 0, len(endpointSet))
	for id := range endpointSet {
		ids = append(ids, id)
	}
	endpoints, err := r.Claims.ListByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	created := make(map[string]time.Time, len(endpoints))
	for _, c := range endpoints {
		created[c.ID] = c.CreatedAt
	}
	res := resolver{view: r.claims(), trees: map[string][]member{}}
	for i := range kept {
		rel := &kept[i]
		for _, end := range []*string{&rel.FromClaimID, &rel.ToClaimID} {
			at, ok := created[*end]
			if !ok || !at.After(rel.CreatedAt) {
				continue
			}
			holder, err := res.holderOf(ctx, *end, rel.CreatedAt)
			if err != nil {
				return nil, err
			}
			*end = holder
		}
	}
	return kept, nil
}

// member is a claim merged into a tree's root, with when it was merged.
type member struct {
	claim    domain.Claim
	mergedAt time.Time
	dated    bool
}

// resolver finds, per call, which claim an edge sat on at At.
type resolver struct {
	view  Claims
	trees map[string][]member
}

// holderOf returns the claim that held, at At, an edge now on root and created
// at edgeAt: of the claims merged into root that existed at edgeAt, the
// oldest, followed forward through any merge that happened by At. root itself
// when there is none.
func (s resolver) holderOf(ctx context.Context, root string, edgeAt time.Time) (string, error) {
	tree, ok := s.trees[root]
	if !ok {
		merged, err := s.view.mergedAway(ctx, []domain.Claim{{ID: root}})
		if err != nil {
			return "", err
		}
		for _, c := range merged {
			h, err := s.view.historyOf(ctx, c.ID)
			if err != nil {
				return "", err
			}
			at, dated := MergedAt(h.status)
			tree = append(tree, member{claim: c, mergedAt: at, dated: dated})
		}
		s.trees[root] = tree
	}
	var origin *member
	for i := range tree {
		m := &tree[i]
		if m.claim.CreatedAt.After(edgeAt) {
			continue
		}
		if origin == nil || m.claim.CreatedAt.Before(origin.claim.CreatedAt) ||
			(m.claim.CreatedAt.Equal(origin.claim.CreatedAt) && m.claim.ID < origin.claim.ID) {
			origin = m
		}
	}
	if origin == nil {
		return root, nil
	}
	byID := make(map[string]*member, len(tree))
	for i := range tree {
		byID[tree[i].claim.ID] = &tree[i]
	}
	// Follow forward through merges that had already happened at At.
	cur := origin
	for depth := 0; depth < maxMergeDepth; depth++ {
		if cur.dated && cur.mergedAt.After(s.view.At) {
			return cur.claim.ID, nil
		}
		next, ok := byID[cur.claim.MergedInto]
		if !ok {
			return cur.claim.MergedInto, nil
		}
		cur = next
	}
	return cur.claim.ID, nil
}

// ListByClaimIDs returns the edges touching ids at At. For a claim merged
// after At that includes the edges a merge moved onto the claim holding it
// now.
func (r Relationships) ListByClaimIDs(ctx context.Context, ids []string) ([]domain.Relationship, error) {
	query := ids
	if r.Claims != nil {
		roots, err := r.claims().heldRoots(ctx, ids)
		if err != nil {
			return nil, err
		}
		if len(roots) > 0 {
			query = append([]string(nil), ids...)
			for _, root := range roots {
				query = append(query, root)
			}
		}
	}
	rels, err := r.RelationshipRepository.ListByClaimIDs(ctx, query)
	if err != nil {
		return nil, err
	}
	closed, err := r.ListClosedByClaimIDs(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("closed edges: %w", err)
	}
	rels, err = r.rewind(ctx, r.withClosed(rels, closed))
	if err != nil {
		return nil, err
	}
	asked := make(map[string]bool, len(ids))
	for _, id := range ids {
		asked[id] = true
	}
	out := rels[:0]
	for _, rel := range rels {
		if asked[rel.FromClaimID] || asked[rel.ToClaimID] {
			out = append(out, rel)
		}
	}
	return out, nil
}

// ListByClaim returns the edges touching id at At.
func (r Relationships) ListByClaim(ctx context.Context, id string) ([]domain.Relationship, error) {
	return r.ListByClaimIDs(ctx, []string{id})
}

// ListAll returns every edge at At.
func (r Relationships) ListAll(ctx context.Context) ([]domain.Relationship, error) {
	rels, err := r.RelationshipRepository.ListAll(ctx)
	if err != nil {
		return nil, err
	}
	closed, err := r.ListAllClosed(ctx)
	if err != nil {
		return nil, fmt.Errorf("closed edges: %w", err)
	}
	return r.rewind(ctx, r.withClosed(rels, closed))
}

// withClosed adds to rels the closed edges that were in force at At. An edge
// id names one row at a time, but a live edge can reuse the id of one closed
// earlier (an edge re-detected after a merge dropped it); the closed one wins
// at At when the live one was recorded after At. When closed edges are added,
// the result is ordered by created_at, as ListAll is.
func (r Relationships) withClosed(rels []domain.Relationship, closed []domain.ClosedRelationship) []domain.Relationship {
	if len(closed) == 0 {
		return rels
	}
	out := append([]domain.Relationship(nil), rels...)
	changed := false
	at := make(map[string]int, len(out))
	for i, rel := range out {
		at[rel.ID] = i
	}
	for _, c := range closed {
		if !c.InForceAt(r.At) {
			continue
		}
		i, ok := at[c.ID]
		switch {
		case !ok:
			at[c.ID] = len(out)
			out = append(out, c.Association)
			changed = true
		case out[i].CreatedAt.After(r.At):
			out[i] = c.Association
			changed = true
		}
	}
	if !changed {
		return rels
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}
