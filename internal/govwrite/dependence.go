package govwrite

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/retraction"
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
	tainted   map[string]bool // event id -> depends on a retracted episode (proven answers only)
}

// newIndependence returns an independence check. overrides fixes the
// retraction state of the named events, for the retraction being applied
// (true) or the one being cleared (false).
func newIndependence(conn *store.Conn, overrides map[string]bool) *independence {
	x := &independence{conn: conn, retracted: map[string]bool{}, tainted: map[string]bool{}}
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

// prefetch loads the retraction state of every id not known yet in one
// batched journal read, so evaluating an episode set costs a query per batch
// rather than one per episode (ADR 0043).
func (x *independence) prefetch(ctx context.Context, eventIDs []string) error {
	var missing []string
	seen := map[string]bool{}
	for _, id := range eventIDs {
		if _, known := x.retracted[id]; !known && !seen[id] {
			seen[id] = true
			missing = append(missing, id)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	if x.conn == nil {
		return errors.New("this backend has no journal to record retractions in")
	}
	histories, err := retraction.LoadHistories(ctx, x.conn.Journal, missing)
	if err != nil {
		return err
	}
	for _, id := range missing {
		_, active := histories[id].Active()
		x.retracted[id] = active
	}
	return nil
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

// dependsOnRetracted reports whether eventID reaches a retracted episode
// through its dependencies (outgoing, transitively, cycles included).
//
// It is a breadth-first reachability search, one dependency read per level,
// and it memoises only answers it has proven: "tainted" for eventID when a
// retracted episode is reached, and "independent" for every episode the
// search saw when none is, since everything those episodes reach was seen
// too. A depth-first walk that cuts cycles cannot memoise its intermediate
// answers this way: an episode cut off by the cycle looks independent before
// the rest of the cycle has been explored, and serving that answer later is
// what kept a belief resting on a copy of a retracted source (ADR 0043).
func (x *independence) dependsOnRetracted(ctx context.Context, eventID string) (bool, error) {
	if v, ok := x.tainted[eventID]; ok {
		return v, nil
	}
	if x.conn.Dependencies == nil {
		return false, nil
	}
	seen := map[string]bool{eventID: true}
	frontier := []string{eventID}
	for len(frontier) > 0 {
		deps, err := x.conn.Dependencies.ListByEventIDs(ctx, frontier)
		if err != nil {
			return false, fmt.Errorf("dependencies of %v: %w", frontier, err)
		}
		targets := make([]string, len(deps))
		for i, d := range deps {
			targets[i] = d.DependsOn
		}
		if err := x.prefetch(ctx, targets); err != nil {
			return false, err
		}
		var next []string
		for _, d := range deps {
			target := d.DependsOn
			dead, err := x.isRetracted(ctx, target)
			if err != nil {
				return false, err
			}
			if dead || x.tainted[target] {
				x.tainted[eventID] = true
				return true, nil
			}
			if seen[target] {
				continue
			}
			seen[target] = true
			if _, known := x.tainted[target]; known {
				// Proven independent: nothing it reaches is retracted.
				continue
			}
			next = append(next, target)
		}
		frontier = next
	}
	for id := range seen {
		x.tainted[id] = false
	}
	return false, nil
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
	events := make([]string, len(links))
	for i, l := range links {
		events[i] = l.EventID
	}
	if err := x.prefetch(ctx, events); err != nil {
		return nil, err
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
