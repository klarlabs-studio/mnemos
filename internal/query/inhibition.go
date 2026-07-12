package query

import "go.klarlabs.de/mnemos/internal/domain"

// Competitive inhibition / retrieval-induced forgetting (ADR 0016). When a query
// resolves a contradiction into a decisive winner and loser, recalling the winner
// suppresses the loser's retrievability: a bounded penalty subtracted from the loser's
// ranking score on THIS and later recalls (the penalty is persisted in the loser's
// `inhibition` confidence-component). Mirror of salienceScoreDelta, but a one-sided,
// negative term — and, unlike salience, always applied (not gated by a query flag),
// because a suppression established once must keep lowering the loser's rank whether or
// not a later recall opts into inhibition. It is still inert when the component is
// absent (magnitude 0), so recall is unchanged until inhibition is actually used.

const (
	// inhibitionRetrievalWeight bounds the ranking penalty. Inhibition magnitude is in
	// [0,1], so the term is in [−inhibitionRetrievalWeight, 0] = [−0.2, 0] at the cap —
	// enough to lose a tie against an equal-similarity rival, never enough to sink a
	// materially better match (the same "tips ties" guarantee salience carries).
	inhibitionRetrievalWeight = 0.20

	// inhibitionDelta is how much one decisive win adds to a loser's suppression, and
	// inhibitionMax caps the accumulation. Small so inhibition builds over repeated
	// wins rather than in a single query, and capped so it stays a tie-breaker.
	inhibitionDelta = 0.1
	inhibitionMax   = 0.5

	// inhibitionSalienceProtectFloor shields a high-stakes loser from inhibition: a
	// consequential belief is never made harder to recall just because it lost a
	// contradiction (mirrors the forgetStaleClaims salience protection).
	inhibitionSalienceProtectFloor = 0.66
)

// inhibitionScoreDelta is the bounded (non-positive) ranking penalty for one claim: a
// suppressed loser loses score proportional to its stored inhibition magnitude; an
// unsuppressed claim is unchanged.
func inhibitionScoreDelta(c domain.Claim) float64 {
	return -inhibitionRetrievalWeight * c.EffectiveInhibition()
}
