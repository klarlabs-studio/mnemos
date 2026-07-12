package query

import "go.klarlabs.de/mnemos/internal/domain"

// Salience-biased retrieval (ADR 0013 §4). Ranking blends BM25 + cosine into a
// normalized similarity score in [0,1] (rankClaimsByHybrid). Salience adds a
// small, BOUNDED additive term on top of that score so a high-stakes belief is
// preferentially recalled — a life-threatening fact outranks a trivial one of
// EQUAL similarity, while a materially better match still wins on its own merit.
//
// It is opt-in (AnswerOptions.Salient); with salience off, or for a claim at the
// neutral baseline (no salience component), the term is zero, so ordinary recall
// is byte-for-byte unchanged. This mirrors the spreading-activation weight (ADR
// 0013 §2): a read-only ranking re-weight, no new storage, deliberately small.

const (
	// salienceRetrievalWeight bounds the similarity-score adjustment salience can
	// apply. The stakes delta is (EffectiveSalience − NeutralSalience) ∈ [−0.5,
	// +0.5], so the term is in [−0.5·w, +0.5·w] = ±0.1 here. Two claims of equal
	// similarity are therefore ordered by stakes, but a claim more than ±0.1 better
	// on normalized similarity can never be overtaken on salience alone — the
	// "stakes tips ties, not genuinely better matches" guarantee is structural.
	salienceRetrievalWeight = 0.20
)

// salienceScoreDelta is the bounded similarity-score adjustment for one claim: a
// belief above the neutral stakes baseline gains score, one below loses it, and a
// neutral (or unmarked) belief is unchanged.
func salienceScoreDelta(c domain.Claim) float64 {
	return salienceRetrievalWeight * (c.EffectiveSalience() - domain.NeutralSalience)
}
