package mnemos

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"sort"
	"strings"
)

// Structural-retrieval constants govern [Memory.AnalogousClaims].
const (
	// StructuralHops is the radius of the typed subgraph fingerprinted around a
	// claim — its local "causal skeleton".
	StructuralHops = 2

	// StructuralIterations is the number of Weisfeiler-Lehman label-propagation
	// rounds. 1–2 captures local relational structure without over-smoothing.
	StructuralIterations = 2

	// MinStructuralSimilarity is the cosine floor below which two subgraphs are not
	// considered analogous — filters incidental single-role overlap.
	MinStructuralSimilarity = 0.5
)

// Analogy is a claim whose surrounding typed subgraph is structurally similar to
// the query subgraph — the anchor of a past situation with the same relational
// shape (causal skeleton), even when the claims themselves differ.
type Analogy struct {
	ClaimID    string
	Text       string
	Similarity float64 // [0,1] Weisfeiler-Lehman structural similarity to the query subgraph
}

// typedEdge is an undirected epistemic edge with its relationship type.
type typedEdge struct {
	from, to, typ string
}

// hashLabel compresses a Weisfeiler-Lehman label to a short, stable token so
// labels don't grow unboundedly across iterations while staying comparable.
func hashLabel(s string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return fmt.Sprintf("%x", h.Sum64())
}

// structuralFingerprint computes a Weisfeiler-Lehman fingerprint of a typed
// subgraph: a histogram of structural labels accumulated over
// [StructuralIterations] rounds of label propagation. Node content/identity is
// IGNORED — only node ROLES (nodeRoles, e.g. claim types) and EDGE TYPES shape the
// fingerprint. So two subgraphs with the same relational skeleton fingerprint
// alike even when their claims are entirely different. Deterministic.
func structuralFingerprint(nodeRoles map[string]string, edges []typedEdge) map[string]int {
	type neighbor struct{ typ, id string }
	adj := make(map[string][]neighbor, len(nodeRoles))
	for _, e := range edges {
		adj[e.from] = append(adj[e.from], neighbor{e.typ, e.to})
		adj[e.to] = append(adj[e.to], neighbor{e.typ, e.from})
	}
	labels := make(map[string]string, len(nodeRoles))
	for n, role := range nodeRoles {
		labels[n] = role
	}
	fp := map[string]int{}
	for _, l := range labels { // iteration 0: raw roles
		fp[l]++
	}
	for it := 0; it < StructuralIterations; it++ {
		next := make(map[string]string, len(labels))
		for n := range nodeRoles {
			tags := make([]string, 0, len(adj[n]))
			for _, a := range adj[n] {
				tags = append(tags, a.typ+"|"+labels[a.id])
			}
			sort.Strings(tags)
			next[n] = hashLabel(labels[n] + "#" + strings.Join(tags, ","))
		}
		labels = next
		for _, l := range labels {
			fp[l]++
		}
	}
	return fp
}

// cosineHist is cosine similarity between two label histograms in [0,1].
func cosineHist(a, b map[string]int) float64 {
	var dot, na, nb float64
	for k, v := range a {
		na += float64(v) * float64(v)
		if w, ok := b[k]; ok {
			dot += float64(v) * float64(w)
		}
	}
	for _, v := range b {
		nb += float64(v) * float64(v)
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// localSubgraph builds the typed subgraph within `hops` epistemic edges of seedID:
// node roles (claim types) and the edges among them.
func (m *memory) localSubgraph(ctx context.Context, seedID string, hops int) (map[string]string, []typedEdge, error) {
	visited := map[string]struct{}{seedID: {}}
	frontier := []string{seedID}
	var edges []typedEdge
	edgeSeen := map[string]struct{}{}
	for h := 0; h < hops && len(frontier) > 0; h++ {
		rels, err := m.conn.Relationships.ListByClaimIDs(ctx, frontier)
		if err != nil {
			return nil, nil, err
		}
		var next []string
		for _, r := range rels {
			ek := r.FromClaimID + "|" + r.ToClaimID + "|" + string(r.Type)
			if _, ok := edgeSeen[ek]; !ok {
				edgeSeen[ek] = struct{}{}
				edges = append(edges, typedEdge{r.FromClaimID, r.ToClaimID, string(r.Type)})
			}
			for _, id := range []string{r.FromClaimID, r.ToClaimID} {
				if _, ok := visited[id]; !ok {
					visited[id] = struct{}{}
					next = append(next, id)
				}
			}
		}
		frontier = next
	}
	ids := make([]string, 0, len(visited))
	for id := range visited {
		ids = append(ids, id)
	}
	claims, err := m.conn.Claims.ListByIDs(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	roles := make(map[string]string, len(visited))
	for _, c := range claims {
		roles[c.ID] = string(c.Type)
	}
	for id := range visited { // dangling endpoints (claim missing) get a generic role
		if _, ok := roles[id]; !ok {
			roles[id] = "unknown"
		}
	}
	return roles, edges, nil
}

// AnalogousClaims implements [Memory.AnalogousClaims]: find claims whose local
// typed subgraph is structurally similar to the one around claimID — retrieval by
// relational SHAPE (Weisfeiler-Lehman), not content. One representative per
// distinct neighbourhood, strongest similarity first.
//
// Cost note: this fingerprints candidate neighbourhoods at query time (O(claims
// with structure)). Fine at investigation scale; a large store would index
// fingerprints on write.
func (m *memory) AnalogousClaims(ctx context.Context, claimID string, limit int) ([]Analogy, error) {
	claimID = strings.TrimSpace(claimID)
	if claimID == "" {
		return nil, errors.New("mnemos: AnalogousClaims: claimID is required")
	}
	anchorRoles, anchorEdges, err := m.localSubgraph(ctx, claimID, StructuralHops)
	if err != nil {
		return nil, fmt.Errorf("mnemos: AnalogousClaims: anchor subgraph: %w", err)
	}
	if len(anchorEdges) == 0 {
		return nil, nil // no structure around the anchor — nothing to be analogous to
	}
	anchorFP := structuralFingerprint(anchorRoles, anchorEdges)

	allRels, err := m.conn.Relationships.ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("mnemos: AnalogousClaims: list relationships: %w", err)
	}
	inAnchor := make(map[string]struct{}, len(anchorRoles))
	for id := range anchorRoles {
		inAnchor[id] = struct{}{}
	}
	candidates := map[string]struct{}{}
	for _, r := range allRels {
		for _, id := range []string{r.FromClaimID, r.ToClaimID} {
			if _, ok := inAnchor[id]; !ok {
				candidates[id] = struct{}{}
			}
		}
	}

	type scored struct {
		id  string
		sim float64
	}
	var out []scored
	done := map[string]struct{}{} // one representative per neighbourhood
	for cand := range candidates {
		if _, seen := done[cand]; seen {
			continue
		}
		roles, edges, serr := m.localSubgraph(ctx, cand, StructuralHops)
		if serr != nil || len(edges) == 0 {
			done[cand] = struct{}{}
			continue
		}
		for id := range roles { // this whole neighbourhood is now represented by cand
			done[id] = struct{}{}
		}
		sim := cosineHist(anchorFP, structuralFingerprint(roles, edges))
		if sim < MinStructuralSimilarity {
			continue
		}
		out = append(out, scored{cand, sim})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].sim > out[j].sim })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}

	ids := make([]string, len(out))
	for i, s := range out {
		ids[i] = s.id
	}
	claims, _ := m.conn.Claims.ListByIDs(ctx, ids)
	textByID := make(map[string]string, len(claims))
	for _, c := range claims {
		textByID[c.ID] = c.Text
	}
	analogies := make([]Analogy, len(out))
	for i, s := range out {
		analogies[i] = Analogy{ClaimID: s.id, Text: textByID[s.id], Similarity: s.sim}
	}
	return analogies, nil
}
