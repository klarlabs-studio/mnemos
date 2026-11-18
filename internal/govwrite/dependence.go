package govwrite

import (
	"context"
	"fmt"
	"sort"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
)

// Source dependence in retraction (ADR 0038 §3, §4).
//
// A belief's evidence from an episode counts only while that episode is
// independent of every retracted source: it is not retracted itself and it
// does not depend, directly or through other episodes, on a retracted one. A
// copy of a retracted source is therefore not live evidence, which is what
// lets a retraction reach a belief that was folded onto a copy at ingest.

// independence answers "does evidence from this episode still count?" with
// memoised store reads. The zero value is not usable; use newIndependence.
type independence struct {
	conn      *store.Conn
	retracted map[string]bool // event id -> retracted now (seeded with overrides)
	tainted   map[string]bool // event id -> depends on a retracted episode
	visiting  map[string]bool
}

// newIndependence returns an independence check. overrides fixes the
// retraction state of the named events, for the retraction being applied
// (true) or the one being cleared (false).
func newIndependence(conn *store.Conn, overrides map[string]bool) *independence {
	x := &independence{conn: conn, retracted: map[string]bool{}, tainted: map[string]bool{}, visiting: map[string]bool{}}
	for id, v := range overrides {
		x.retracted[id] = v
	}
	return x
}

// counts reports whether evidence from eventID is live independent evidence.
func (x *independence) counts(ctx context.Context, eventID string) (bool, error) {
	dead, err := x.isRetracted(ctx, eventID)
	if err != nil || dead {
		return false, err
	}
	tainted, err := x.dependsOnRetracted(ctx, eventID)
	return !tainted, err
}

func (x *independence) isRetracted(ctx context.Context, eventID string) (bool, error) {
	if v, ok := x.retracted[eventID]; ok {
		return v, nil
	}
	_, v, err := LookupRetraction(ctx, x.conn, eventID)
	if err != nil {
		return false, err
	}
	x.retracted[eventID] = v
	return v, nil
}

// dependsOnRetracted walks eventID's dependencies (outgoing, transitively).
// A cycle is cut where it closes: an episode on the current path is not
// counted as depending on anything through itself.
func (x *independence) dependsOnRetracted(ctx context.Context, eventID string) (bool, error) {
	if v, ok := x.tainted[eventID]; ok {
		return v, nil
	}
	if x.conn.Dependencies == nil || x.visiting[eventID] {
		return false, nil
	}
	x.visiting[eventID] = true
	defer delete(x.visiting, eventID)
	deps, err := x.conn.Dependencies.ListByEventIDs(ctx, []string{eventID})
	if err != nil {
		return false, fmt.Errorf("dependencies of %s: %w", eventID, err)
	}
	result := false
	for _, d := range deps {
		dead, err := x.isRetracted(ctx, d.DependsOn)
		if err != nil {
			return false, err
		}
		if !dead {
			if dead, err = x.dependsOnRetracted(ctx, d.DependsOn); err != nil {
				return false, err
			}
		}
		if dead {
			result = true
			break
		}
	}
	x.tainted[eventID] = result
	return result, nil
}

// liveIndependentEvidence counts, per belief, its evidence links that count
// under x.
func liveIndependentEvidence(ctx context.Context, conn *store.Conn, claims []domain.Claim, x *independence) (map[string]int, error) {
	out := make(map[string]int, len(claims))
	if len(claims) == 0 {
		return out, nil
	}
	ids := make([]string, len(claims))
	for i, c := range claims {
		ids[i] = c.ID
	}
	links, err := conn.Claims.ListEvidenceByClaimIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("list evidence of dependent beliefs: %w", err)
	}
	for _, l := range links {
		ok, err := x.counts(ctx, l.EventID)
		if err != nil {
			return nil, err
		}
		if ok {
			out[l.ClaimID]++
		}
	}
	return out, nil
}

// dependencyClosure is every episode that depends on root, directly or
// through other episodes, each with the hop that first reached it
// (breadth-first, ties broken by id so the report is deterministic).
type dependencyClosure struct {
	root  string
	order []string
	hop   map[string]domain.EventDependency
}

func closureOf(ctx context.Context, conn *store.Conn, root string) (dependencyClosure, error) {
	c := dependencyClosure{root: root, hop: map[string]domain.EventDependency{}}
	if conn.Dependencies == nil {
		return c, nil
	}
	frontier := []string{root}
	for len(frontier) > 0 {
		deps, err := conn.Dependencies.ListDependents(ctx, frontier)
		if err != nil {
			return dependencyClosure{}, fmt.Errorf("dependents of %v: %w", frontier, err)
		}
		sort.Slice(deps, func(i, j int) bool {
			if deps[i].EventID != deps[j].EventID {
				return deps[i].EventID < deps[j].EventID
			}
			return deps[i].DependsOn < deps[j].DependsOn
		})
		var next []string
		for _, d := range deps {
			if d.EventID == root {
				continue
			}
			if _, seen := c.hop[d.EventID]; seen {
				continue
			}
			c.hop[d.EventID] = d
			c.order = append(c.order, d.EventID)
			next = append(next, d.EventID)
		}
		frontier = next
	}
	return c, nil
}

// path is the episode chain from the root to eventID: [root, ..., eventID].
func (c dependencyClosure) path(eventID string) []string {
	var rev []string
	for id := eventID; id != c.root; {
		rev = append(rev, id)
		d, ok := c.hop[id]
		if !ok {
			break
		}
		id = d.DependsOn
	}
	out := []string{c.root}
	for i := len(rev) - 1; i >= 0; i-- {
		out = append(out, rev[i])
	}
	return out
}

// hops is nil when nothing depends on the root, so the report's omitempty
// field reads back exactly as it was written.
func (c dependencyClosure) hops() []domain.DependencyHop {
	if len(c.order) == 0 {
		return nil
	}
	out := make([]domain.DependencyHop, len(c.order))
	for i, id := range c.order {
		out[i] = c.hop[id].Hop()
	}
	return out
}
