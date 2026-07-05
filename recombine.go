package mnemos

import (
	"context"
	"fmt"
	"sort"

	"go.klarlabs.de/mnemos/internal/trust"
)

// Recombination constants govern [Memory.Recombinations].
const (
	// RecombineSalienceFloor is the minimum salience for a claim to be a
	// recombination candidate — REM recombines what matters, not the mundane.
	RecombineSalienceFloor = 0.5
	// RecombineSimilarityFloor is the minimum topical similarity for a pair to be
	// a candidate — close enough to be worth connecting.
	RecombineSimilarityFloor = 0.34
)

// Recombination is a proposed novel connection: two high-salience claims that are
// topically related but that the waking graph never linked — the REM-stage
// juxtaposition a human (or an LLM) can turn into a schema or hypothesis.
type Recombination struct {
	ClaimA, TextA string
	ClaimB, TextB string
	Similarity    float64 // [0,1] topical (Jaccard) similarity of the pair
}

// jaccard is the symmetric token overlap of two claims in [0,1].
func jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for t := range a {
		if _, ok := b[t]; ok {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// Recombinations implements [Memory.Recombinations]: the REM-like recombination
// detector. It finds pairs of high-salience, currently-valid claims that are
// topically similar yet NOT directly connected in the epistemic graph — novel
// juxtapositions the waking graph never made — ranked by similarity. Deterministic
// detection; naming the emergent schema/hypothesis is left to a human or an LLM
// (these are proposals, never auto-promoted).
//
// Cost note: O(candidates²) over high-salience claims — bounded by the salience
// floor; a large store would block by topic first.
func (m *memory) Recombinations(ctx context.Context, limit int) ([]Recombination, error) {
	all, err := m.conn.Claims.ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("mnemos: Recombinations: list claims: %w", err)
	}
	evidence, err := m.conn.Claims.ListAllEvidence(ctx)
	if err != nil {
		return nil, fmt.Errorf("mnemos: Recombinations: list evidence: %w", err)
	}
	evidenceCount := make(map[string]int, len(all))
	for _, e := range evidence {
		evidenceCount[e.ClaimID]++
	}
	// High-salience candidates + their token sets.
	type cand struct {
		id, text string
		tokens   map[string]struct{}
	}
	var cands []cand
	for _, c := range all {
		if !c.ValidTo.IsZero() {
			continue
		}
		if trust.SalienceOf(c, evidenceCount[c.ID]) < RecombineSalienceFloor {
			continue
		}
		cands = append(cands, cand{c.ID, c.Text, tokenizeSet(c.Text)})
	}
	// Already-connected pairs (either direction).
	rels, err := m.conn.Relationships.ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("mnemos: Recombinations: list relationships: %w", err)
	}
	connected := map[string]struct{}{}
	for _, r := range rels {
		connected[pairKey(r.FromClaimID, r.ToClaimID)] = struct{}{}
	}

	var out []Recombination
	for i := 0; i < len(cands); i++ {
		for j := i + 1; j < len(cands); j++ {
			if _, ok := connected[pairKey(cands[i].id, cands[j].id)]; ok {
				continue // the waking graph already juxtaposed these
			}
			sim := jaccard(cands[i].tokens, cands[j].tokens)
			if sim < RecombineSimilarityFloor {
				continue
			}
			out = append(out, Recombination{
				ClaimA: cands[i].id, TextA: cands[i].text,
				ClaimB: cands[j].id, TextB: cands[j].text,
				Similarity: sim,
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Similarity > out[j].Similarity })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func pairKey(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + "\x00" + b
}
