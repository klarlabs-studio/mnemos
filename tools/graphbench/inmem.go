package main

import (
	"context"
	"fmt"
	"slices"

	"go.klarlabs.de/mnemos/internal/domain"
)

// memGraph is the in-process adjacency-map baseline: every workload answered
// from Go maps with no I/O, serialisation or query planning. It is an UPPER
// BOUND on what any graph engine could achieve on this machine, not a
// candidate backend, and it doubles as the test oracle for the store paths.
type memGraph struct {
	claims       map[string]memClaim
	adj          map[string][]memEdge // undirected: both ends list the edge
	contradicts  []domain.Relationship
	evByClaim    map[string][]string
	claimsByEv   map[string][]string
	depsOut      map[string][]string // episode -> episodes it depends on
	depsIn       map[string][]string // episode -> episodes depending on it
	contradictBy map[string][]string // belief -> contradicts edge ids touching it
}

type memClaim struct {
	status     domain.ClaimStatus
	live       bool
	mergedInto string
}

type memEdge struct {
	other string
	relID string
}

func newMemGraph(c *Corpus) *memGraph {
	g := &memGraph{
		claims: map[string]memClaim{}, adj: map[string][]memEdge{},
		evByClaim: map[string][]string{}, claimsByEv: map[string][]string{},
		depsOut: map[string][]string{}, depsIn: map[string][]string{}, contradictBy: map[string][]string{},
	}
	for _, b := range c.Batches {
		g.add(b)
	}
	return g
}

func (g *memGraph) add(b Batch) {
	for _, cl := range b.Claims {
		g.claims[cl.ID] = memClaim{status: cl.Status, live: isLive(cl), mergedInto: cl.MergedInto}
	}
	for _, l := range b.Evidence {
		g.evByClaim[l.ClaimID] = append(g.evByClaim[l.ClaimID], l.EventID)
		g.claimsByEv[l.EventID] = append(g.claimsByEv[l.EventID], l.ClaimID)
	}
	for _, r := range b.Relationships {
		g.adj[r.FromClaimID] = append(g.adj[r.FromClaimID], memEdge{other: r.ToClaimID, relID: r.ID})
		g.adj[r.ToClaimID] = append(g.adj[r.ToClaimID], memEdge{other: r.FromClaimID, relID: r.ID})
		if r.Type == domain.RelationshipTypeContradicts {
			g.contradicts = append(g.contradicts, r)
			g.contradictBy[r.FromClaimID] = append(g.contradictBy[r.FromClaimID], r.ID)
			g.contradictBy[r.ToClaimID] = append(g.contradictBy[r.ToClaimID], r.ID)
		}
	}
	for _, d := range b.Dependencies {
		g.depsOut[d.EventID] = append(g.depsOut[d.EventID], d.DependsOn)
		g.depsIn[d.DependsOn] = append(g.depsIn[d.DependsOn], d.EventID)
	}
}

// sortContradictions orders the contradictions as ListAll returns them
// (created_at ascending, insertion order on ties).
func (g *memGraph) sortContradictions() {
	slices.SortStableFunc(g.contradicts, func(a, b domain.Relationship) int { return a.CreatedAt.Compare(b.CreatedAt) })
}

// isLive is the live test resolveRedirect applies to a chain's next claim.
func isLive(c domain.Claim) bool {
	return c.Status != domain.ClaimStatusDeprecated && c.ValidTo.IsZero()
}

func (g *memGraph) expand(_ context.Context, in ExpandInput) ([]string, error) {
	dist := map[string]int{}
	frontier := slices.Clone(in.Seeds)
	for _, s := range in.Seeds {
		dist[s] = 0
	}
	var out []string
	for hop := 1; hop <= in.Hops && len(frontier) > 0; hop++ {
		next := map[string]struct{}{}
		for _, id := range frontier {
			for _, e := range g.adj[id] {
				if _, seen := dist[e.other]; !seen {
					next[e.other] = struct{}{}
				}
			}
		}
		frontier = frontier[:0]
		for id := range next {
			if _, ok := g.claims[id]; ok {
				dist[id] = hop
				out = append(out, expandLine(id, hop))
			}
			frontier = append(frontier, id)
		}
	}
	return out, nil
}

func (g *memGraph) retract(_ context.Context, in RetractInput) ([]string, error) {
	root := in.Root
	level := map[string]int{}
	var order []string
	frontier := []string{root}
	for lvl := 1; len(frontier) > 0; lvl++ {
		var next []string
		for _, id := range frontier {
			for _, d := range g.depsIn[id] {
				if d == root {
					continue
				}
				if _, seen := level[d]; !seen {
					level[d] = lvl
					order = append(order, d)
					next = append(next, d)
				}
			}
		}
		frontier = next
	}
	var out []string
	for _, id := range order {
		out = append(out, closureLine(id, level[id]))
	}
	direct := map[string]bool{}
	dependents := map[string]bool{}
	for _, c := range g.claimsByEv[root] {
		direct[c] = true
		dependents[c] = true
	}
	for _, ep := range order {
		for _, c := range g.claimsByEv[ep] {
			dependents[c] = true
		}
	}
	// With only root retracted, an episode is tainted exactly when it
	// reaches root through its dependencies, i.e. when it is in the closure.
	for c := range dependents {
		live := 0
		for _, e := range g.evByClaim[c] {
			if _, tainted := level[e]; e != root && !tainted {
				live++
			}
		}
		out = append(out, outcomeLine(c, retractOutcome(direct[c], live, g.claims[c].status)))
	}
	return out, nil
}

// retractOutcome is the per-belief decision of govwrite.retractEvent.
func retractOutcome(direct bool, live int, status domain.ClaimStatus) string {
	switch {
	case live > 0 && direct:
		return "rescored"
	case live > 0:
		return "kept"
	case status == domain.ClaimStatusDeprecated:
		return "already-deprecated"
	default:
		return "retracted"
	}
}

func (g *memGraph) dissonancePage(_ context.Context, in PageInput) ([]string, error) {
	n := len(g.contradicts)
	out := []string{totalLine(n)}
	for i := in.Offset; i < in.Offset+in.Limit && i < n; i++ {
		out = append(out, relLine(g.contradicts[n-1-i].ID))
	}
	return out, nil
}

func (g *memGraph) neighbourhood(_ context.Context, in IDsInput) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, id := range in.IDs {
		for _, r := range g.contradictBy[id] {
			if !seen[r] {
				seen[r] = true
				out = append(out, relLine(r))
			}
		}
	}
	return out, nil
}

func (g *memGraph) redirects(_ context.Context, in IDsInput) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, id := range in.IDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		c, ok := g.claims[id]
		if !ok || c.mergedInto == "" {
			continue
		}
		target := ""
		next := c.mergedInto
		for hop := 0; hop < maxRedirectHops && next != ""; hop++ {
			n, ok := g.claims[next]
			if !ok {
				break
			}
			if n.live {
				target = next
				break
			}
			next = n.mergedInto
		}
		if target != "" && target != id {
			out = append(out, redirectLine(id, target))
		}
	}
	return out, nil
}

func (g *memGraph) provenance(_ context.Context, in IDsInput) ([]string, error) {
	depth := map[string]int{}
	var frontier []string
	for _, b := range in.IDs {
		for _, e := range g.evByClaim[b] {
			if _, ok := depth[e]; !ok {
				depth[e] = 0
				frontier = append(frontier, e)
			}
		}
	}
	for d := 1; len(frontier) > 0; d++ {
		var next []string
		for _, e := range frontier {
			for _, on := range g.depsOut[e] {
				if _, ok := depth[on]; !ok {
					depth[on] = d
					next = append(next, on)
				}
			}
		}
		frontier = next
	}
	out := make([]string, 0, len(depth))
	for e, d := range depth {
		out = append(out, episodeLine(e, d))
	}
	return out, nil
}

// maxRedirectHops mirrors internal/pipeline's bound on merge chains.
const maxRedirectHops = 8

// Canonical result lines. Every store path renders its answer through these,
// so parity is a byte comparison of sorted lines.
func expandLine(id string, hop int) string    { return fmt.Sprintf("belief %s hop %d", id, hop) }
func closureLine(id string, lvl int) string   { return fmt.Sprintf("closure %s level %d", id, lvl) }
func outcomeLine(id, outcome string) string   { return fmt.Sprintf("belief %s %s", id, outcome) }
func totalLine(n int) string                  { return fmt.Sprintf("total %d", n) }
func relLine(id string) string                { return "rel " + id }
func redirectLine(id, target string) string   { return fmt.Sprintf("redirect %s -> %s", id, target) }
func episodeLine(id string, depth int) string { return fmt.Sprintf("episode %s depth %d", id, depth) }
func writeLine(beliefs, edges int) string {
	return fmt.Sprintf("wrote %d beliefs %d edges", beliefs, edges)
}
