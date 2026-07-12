package query

import (
	"context"
	"fmt"
	"sort"

	"go.klarlabs.de/mnemos/internal/domain"
)

// Spreading-activation priming at retrieval (ADR 0013 §2; Collins & Loftus,
// "A Spreading-Activation Theory of Semantic Processing", 1975).
//
// Today retrieval ranks claims by embedding/BM25 similarity (rankClaimsByCosine)
// plus optional --hops graph expansion. That surfaces what directly matches the
// query, but recalling one belief does nothing to raise the accessibility of its
// associated beliefs — there is no priming. Spreading activation adds exactly
// that: seed activation on the top directly-retrieved beliefs and let it flow
// through the claim-to-claim relationship graph, decaying per hop and weighted
// by edge type, then blend the accumulated activation into the ranking score.
// A belief strongly associated with a direct hit is thereby primed up the
// results even when it matches the query only weakly on its own text.
//
// This is a read-only ranking re-weight over the claims already in hand — it
// adds no storage and pulls in no new claims (it reuses the same
// RelationshipRepository.ListByClaimIDs graph traversal that --hops uses to
// discover edges). It is opt-in (AnswerOptions.Prime); with priming disabled the
// ranking is byte-for-byte the pre-existing order.

const (
	// spreadSeedK is how many of the top directly-ranked claims seed the
	// activation spread. Matches answerEventLimit's "top few" intuition.
	spreadSeedK = 5
	// spreadHops is how far activation flows from the seeds. 1–2 hops is the
	// psychologically-plausible range (activation attenuates fast); 1 is the
	// conservative default.
	spreadHops = 2
	// spreadDecay attenuates activation per hop (activation *= decay each hop),
	// so a 2-hop neighbour is primed a quarter as strongly as a 1-hop one.
	spreadDecay = 0.5
	// spreadWeightSimilarity and spreadWeightActivation blend the direct
	// similarity rank and the associative activation into the final score:
	//   final = wSim*baseScore + wAct*activation.
	// wAct is deliberately small so priming augments rather than dominates
	// similarity.
	spreadWeightSimilarity = 1.0
	spreadWeightActivation = 0.35
	// spreadActivationCap bounds a single claim's accumulated activation. With
	// the cap and wAct below, the maximum associative boost any claim can
	// receive is wAct*cap = 0.35. A claim's baseScore is (n-rank)/n in (0,1],
	// so the top direct hit scores 1.0. Therefore a claim ranked in the bottom
	// ~65% by direct similarity (baseScore < 0.65) can never overtake the top
	// direct hit on association alone — the "a weak association can't outrank a
	// strong direct match" guarantee is structural, not tuned.
	spreadActivationCap = 1.0
)

// edgeActivationWeight scales how strongly activation flows across a given
// relationship type. Corroborating / entailment edges (supports, validates)
// prime most strongly; the causal+provenance family primes moderately;
// contradiction edges still prime (a contradicting belief is highly relevant to
// surface) but at a lower weight. Unknown/other edges get a mild default.
func edgeActivationWeight(t domain.RelationshipType) float64 {
	switch t {
	case domain.RelationshipTypeSupports, domain.RelationshipTypeValidates:
		return 1.0
	case domain.RelationshipTypeCauses, domain.RelationshipTypeCausedBy,
		domain.RelationshipTypeDerivedFrom, domain.RelationshipTypeActionOf,
		domain.RelationshipTypeOutcomeOf:
		return 0.8
	case domain.RelationshipTypeContradicts, domain.RelationshipTypeRefutes:
		return 0.5
	default:
		return 0.6
	}
}

// Hebbian strength weighting (ADR 0015 §4) for spreading activation. A base edge
// (strength 1) contributes factor 1.0 — so a graph whose edges have never been
// co-activated primes exactly as before this feature — and a well-worn edge primes up
// to (1 + strengthActivationBoost)× as strongly, saturating at strengthActivationCap.
const (
	strengthActivationBoost = 0.5
	strengthActivationCap   = 10.0
)

// strengthActivationFactor maps an edge's Hebbian strength to a bounded spreading
// multiplier in [1, 1+strengthActivationBoost], neutral (1.0) at the base strength 1.
func strengthActivationFactor(strength float64) float64 {
	if strength <= 1 {
		return 1
	}
	if strength > strengthActivationCap {
		strength = strengthActivationCap
	}
	return 1 + strengthActivationBoost*(strength-1)/(strengthActivationCap-1)
}

// applySpreadingActivation re-ranks claims by blending each claim's direct
// similarity rank with an associative activation spread over the relationship
// graph from the top-ranked seeds. The input claims are assumed already ordered
// by direct relevance (rankClaimsByCosine + intent boost + hop expansion). The
// returned slice contains exactly the same claims, re-sorted; no claim is added
// or dropped, so all downstream bookkeeping (hopDistance, provenance, contradiction
// collection) stays valid.
func (e Engine) applySpreadingActivation(ctx context.Context, claims []domain.Claim) ([]domain.Claim, error) {
	n := len(claims)
	if n <= 1 {
		return claims, nil
	}

	// 1. Direct-similarity base score from the incoming rank order: the top
	//    claim scores 1.0 and scores decrease monotonically to 1/n. Values stay
	//    strictly positive so even a signal-less trailing claim keeps a stable,
	//    non-zero base.
	baseScore := make(map[string]float64, n)
	origIdx := make(map[string]int, n)
	present := make(map[string]struct{}, n)
	for i, c := range claims {
		baseScore[c.ID] = float64(n-i) / float64(n)
		origIdx[c.ID] = i
		present[c.ID] = struct{}{}
	}

	// 2. Seed activation on the top-K claims, weighted by their own base score
	//    so a stronger seed primes its neighbours more (a #1 hit spreads more
	//    activation than the #5 hit).
	seedK := spreadSeedK
	if seedK > n {
		seedK = n
	}
	activation := make(map[string]float64, n)
	frontier := make(map[string]float64, seedK)
	visited := make(map[string]struct{}, n)
	for i := 0; i < seedK; i++ {
		id := claims[i].ID
		frontier[id] = baseScore[id]
		visited[id] = struct{}{}
	}

	// 3. Spread activation outward, one hop at a time, decaying per hop and
	//    weighting by edge type. Intermediate nodes that are not in the answer
	//    set still carry activation forward (so a 2-hop path through an
	//    off-set claim works), but only claims present in the answer set
	//    accumulate a boost — priming re-weights what we have, it does not
	//    fetch new claims.
	for hop := 1; hop <= spreadHops && len(frontier) > 0; hop++ {
		frontierIDs := make([]string, 0, len(frontier))
		for id := range frontier {
			frontierIDs = append(frontierIDs, id)
		}
		rels, err := e.relationships.ListByClaimIDs(ctx, frontierIDs)
		if err != nil {
			return nil, fmt.Errorf("list relationships for priming hop %d: %w", hop, err)
		}
		next := make(map[string]float64)
		for _, rel := range rels {
			// Type weight scaled by the edge's Hebbian strength (ADR 0015 §4): a
			// frequently co-retrieved association primes more strongly. Base-strength
			// edges keep their exact type weight, so this is a no-op until edges wire.
			w := edgeActivationWeight(rel.Type) * strengthActivationFactor(rel.EffectiveStrength())
			// Priming is associative, so an edge cues in both directions.
			spreadAcross(rel.FromClaimID, rel.ToClaimID, w, frontier, visited, present, activation, next)
			spreadAcross(rel.ToClaimID, rel.FromClaimID, w, frontier, visited, present, activation, next)
		}
		for id := range next {
			visited[id] = struct{}{}
		}
		frontier = next
	}

	if len(activation) == 0 {
		// No edges reached any present claim — nothing to re-weight. Return the
		// input order untouched so priming is a strict no-op here.
		return claims, nil
	}

	// 4. Blend and re-sort. final = wSim*baseScore + wAct*min(activation, cap).
	final := make(map[string]float64, n)
	for _, c := range claims {
		act := activation[c.ID]
		if act > spreadActivationCap {
			act = spreadActivationCap
		}
		final[c.ID] = spreadWeightSimilarity*baseScore[c.ID] + spreadWeightActivation*act
	}
	ranked := make([]domain.Claim, len(claims))
	copy(ranked, claims)
	sort.SliceStable(ranked, func(i, j int) bool {
		fi, fj := final[ranked[i].ID], final[ranked[j].ID]
		if fi == fj {
			return origIdx[ranked[i].ID] < origIdx[ranked[j].ID]
		}
		return fi > fj
	})
	return ranked, nil
}

// spreadAcross propagates activation from src to dst along one edge of the
// given weight. It fires only when src is a member of the current frontier and
// dst has not already been activated on an earlier (closer) hop — this keeps
// the spread acyclic and bounds each claim's accumulation. dst's forward
// activation (carried into the next hop) is the strongest incoming path; dst's
// boost (accumulated in activation) accrues only when dst is a claim the answer
// set actually holds, since priming re-weights held claims rather than fetching
// new ones.
func spreadAcross(
	src, dst string,
	weight float64,
	frontier map[string]float64,
	visited, present map[string]struct{},
	activation, next map[string]float64,
) {
	srcAct, ok := frontier[src]
	if !ok {
		return
	}
	if _, seen := visited[dst]; seen {
		return
	}
	delta := srcAct * spreadDecay * weight
	if delta <= 0 {
		return
	}
	if _, held := present[dst]; held {
		activation[dst] += delta
	}
	if cur := next[dst]; delta > cur {
		next[dst] = delta
	}
}
