package relate

import (
	"cmp"
	"slices"

	"go.klarlabs.de/mnemos/internal/domain"
)

// DefaultSupportsBudget is the most supports edges one claim emits in a single
// detection pass: its strongest, by token overlap.
//
// Previous behaviour: every pair that passed the overlap rule (two shared
// content tokens covering 30% of the shorter claim) became a supports edge,
// with no limit. Generic vocabulary passes that rule, so the graph grew with
// the corpus: a real 233k-claim brain held 32.4M supports edges, a median of
// 111 per claim and over 1,000 for 5,307 claims, and at 100k beliefs one
// Remember of two claims wrote ~33,400 edges. Persisting them was 60% of the
// write, and every hop expansion fanned out across them.
//
// New behaviour: a claim keeps at most this many supports edges per pass, the
// strongest by Jaccard overlap of content tokens. Contradictions, citations
// and the other edge types are never budgeted: they are rare and each one is
// signal (ADR 0027).
const DefaultSupportsBudget = 20

// WithSupportsBudget returns an engine whose passes keep at most n supports
// edges per source claim. n < 0 removes the limit (the pre-budget behaviour);
// 0 means DefaultSupportsBudget.
func (e Engine) WithSupportsBudget(n int) Engine {
	e.supportsBudget = n
	return e
}

func (e Engine) budget() int {
	if e.supportsBudget == 0 {
		return DefaultSupportsBudget
	}
	return e.supportsBudget
}

// candidateEdge is a relationship a pass found, before the budget decides
// whether it is kept. Source and target index into the pass's own slices.
type candidateEdge struct {
	from, to int
	relType  domain.RelationshipType
	strength float64
}

// supportsStrength is the Jaccard overlap of two token sets of sizes a and b
// sharing overlap tokens. It ranks a claim's supports candidates. Measured
// against the union, not the shorter claim, so a three-token claim cannot
// score as a perfect match against everything that contains its words.
func supportsStrength(overlap, a, b int) float64 {
	union := a + b - overlap
	if union <= 0 {
		return 0
	}
	return float64(overlap) / float64(union)
}

// keepWithinBudget reports, for each candidate in order, whether the budget
// keeps it. Per source claim, the budget strongest supports edges survive;
// ties go to the lower target index, so the outcome is deterministic and the
// same whichever pass (scan or index) produced the candidates. Every other
// edge type is kept. Candidates keep their original relative order.
func keepWithinBudget(edges []candidateEdge, budget int) []bool {
	keep := make([]bool, len(edges))
	bySource := map[int][]int{}
	for k, e := range edges {
		if e.relType != domain.RelationshipTypeSupports || budget < 0 {
			keep[k] = true
			continue
		}
		bySource[e.from] = append(bySource[e.from], k)
	}
	for _, ks := range bySource {
		if len(ks) > budget {
			slices.SortFunc(ks, func(x, y int) int {
				if c := cmp.Compare(edges[y].strength, edges[x].strength); c != 0 {
					return c
				}
				return cmp.Compare(edges[x].to, edges[y].to)
			})
			ks = ks[:budget]
		}
		for _, k := range ks {
			keep[k] = true
		}
	}
	return keep
}

// SupportsPruner applies the supports budget to edges already stored, for
// brains written before it existed (`mnemos relate --prune-supports`). It
// ranks a claim's stored supports edges exactly as a detection pass would rank
// them as candidates: Jaccard overlap of content tokens, ties to the target
// that comes first in the claim order it was built from.
//
// Token sets are interned to sorted ids, so a pruner over a few hundred
// thousand claims costs tens of megabytes rather than a map per claim.
type SupportsPruner struct {
	budget int
	index  map[string]int // claim id -> position in the build order
	tokens [][]uint32
}

// NewSupportsPruner prepares a pruner over claims, in the order ties resolve.
// budget follows WithSupportsBudget: 0 is the default, < 0 keeps everything.
func NewSupportsPruner(claims []domain.Claim, budget int) *SupportsPruner {
	p := &SupportsPruner{budget: Engine{supportsBudget: budget}.budget(),
		index: make(map[string]int, len(claims)), tokens: make([][]uint32, len(claims))}
	dict := map[string]uint32{}
	scratch := make(map[string]struct{}, 32)
	for i, c := range claims {
		p.index[c.ID] = i
		clear(scratch)
		tokenizeContentInto(scratch, c.Text)
		ids := make([]uint32, 0, len(scratch))
		for tok := range scratch {
			id, ok := dict[tok]
			if !ok {
				id = uint32(len(dict))
				dict[tok] = id
			}
			ids = append(ids, id)
		}
		slices.Sort(ids)
		p.tokens[i] = ids
	}
	return p
}

// OverBudget returns the IDs of from's outgoing supports edges among rels that
// the budget would not keep. Edges of other types, edges from other claims,
// and edges to claims the pruner does not know are ignored: they are neither
// ranked nor counted against the budget.
func (p *SupportsPruner) OverBudget(from string, rels []domain.Relationship) []string {
	fi, ok := p.index[from]
	if !ok || p.budget < 0 {
		return nil
	}
	var cands []candidateEdge
	var ids []string
	for _, r := range rels {
		if r.Type != domain.RelationshipTypeSupports || r.FromClaimID != from {
			continue
		}
		ti, ok := p.index[r.ToClaimID]
		if !ok {
			continue
		}
		overlap := sortedOverlap(p.tokens[fi], p.tokens[ti])
		cands = append(cands, candidateEdge{from: fi, to: ti, relType: r.Type,
			strength: supportsStrength(overlap, len(p.tokens[fi]), len(p.tokens[ti]))})
		ids = append(ids, r.ID)
	}
	if len(cands) <= p.budget {
		return nil
	}
	keep := keepWithinBudget(cands, p.budget)
	var drop []string
	for k, kept := range keep {
		if !kept {
			drop = append(drop, ids[k])
		}
	}
	return drop
}

func sortedOverlap(a, b []uint32) int {
	n, i, j := 0, 0, 0
	for i < len(a) && j < len(b) {
		switch {
		case a[i] == b[j]:
			n++
			i++
			j++
		case a[i] < b[j]:
			i++
		default:
			j++
		}
	}
	return n
}
