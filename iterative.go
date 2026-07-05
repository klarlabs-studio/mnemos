package mnemos

import (
	"context"
	"sort"
)

// RecallMaxRounds is the default budget for [Memory.RecallIterative] when the
// caller passes maxRounds <= 0 — a small bound that keeps the fixpoint cheap.
const RecallMaxRounds = 3

// RecallIterative implements [Memory.RecallIterative]: a bounded retrieve↔reason
// fixpoint. Each round retrieves, checks the sufficiency (CRAG) signal, and — while
// still thin — reasons a follow-up by expanding the query toward the strongest new
// finding and deepening a hop, then retrieves again. Stops on coverage, saturation
// (a round adds nothing new), or budget.
func (m *memory) RecallIterative(ctx context.Context, q Query, maxRounds int) ([]Result, int, error) {
	if maxRounds <= 0 {
		maxRounds = RecallMaxRounds
	}
	seen := map[string]Result{}
	queryText := q.Text
	queryTokens := tokenizeSet(queryText)
	hops := q.Hops
	rounds := 0
	for rounds < maxRounds {
		rounds++
		rq := q
		rq.Text = queryText
		rq.Hops = hops
		results, suf, err := m.RecallWithSufficiency(ctx, rq)
		if err != nil {
			return nil, rounds, err
		}
		added := 0
		var topNew *Result
		for i := range results {
			if _, ok := seen[results[i].ClaimID]; ok {
				continue
			}
			seen[results[i].ClaimID] = results[i]
			added++
			if topNew == nil {
				topNew = &results[i] // highest-ranked new result — what we reason from
			}
		}
		if suf.Sufficient {
			break // coverage reached — CRAG says we have enough
		}
		if added == 0 || topNew == nil {
			break // saturation — nothing new to reason from
		}
		// Reason: fold the strongest new finding's terms into the query and deepen a
		// hop, so what we just retrieved steers what we seek next.
		for tok := range tokenizeSet(topNew.Text) {
			if _, has := queryTokens[tok]; !has {
				queryTokens[tok] = struct{}{}
				queryText += " " + tok
			}
		}
		hops++
	}
	out := make([]Result, 0, len(seen))
	for _, r := range seen {
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].TrustScore > out[j].TrustScore })
	return out, rounds, nil
}
