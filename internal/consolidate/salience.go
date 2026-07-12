package consolidate

import "go.klarlabs.de/mnemos/internal/domain"

// Salience-biased consolidation (ADR 0013 §4). Prioritized replay rehearses the
// highest-priority memories so they resist the decay that prunes the mundane. A
// high-STAKES belief should be rehearsed preferentially — the amygdala tags
// consequential memories for stronger consolidation. SaliencePriorityMultiplier
// supplies that as a bounded factor on the existing priority score.
//
// It is a re-WEIGHTING, never a gate: it multiplies a claim's priority, so a
// claim that is already ineligible (invalidated, refuted, or below a floor the
// caller enforces) is unaffected — salience can raise WHERE a memory sits in the
// rehearsal order but never smuggles an ineligible one in. That keeps the ADR-0012
// promotion eligibility and ADR-0011 no-leak gates authoritative; stakes only
// reorders what those gates already admitted. A claim at the neutral baseline
// yields a multiplier of exactly 1.0, so an unmarked corpus consolidates
// identically.

// SaliencePriorityWeight bounds how far stakes can swing a claim's consolidation
// priority. At maximum stakes (salience 1.0) priority is scaled by 1+weight; at
// minimum (0.0) by 1−weight. Deliberately below 1 so the multiplier stays
// strictly positive across the whole [0,1] range (no claim's priority is ever
// zeroed or sign-flipped by stakes).
const SaliencePriorityWeight = 0.5

// SaliencePriorityMultiplier maps a claim's stakes salience in [0,1] to a bounded
// multiplier on its consolidation-priority score. Neutral (0.5) → 1.0, maximum
// stakes → 1+SaliencePriorityWeight, minimum → 1−SaliencePriorityWeight. The
// input is clamped defensively.
func SaliencePriorityMultiplier(salience float64) float64 {
	switch {
	case salience < 0:
		salience = 0
	case salience > 1:
		salience = 1
	}
	// (salience − neutral) / (1 − neutral) ∈ [−1, 1]; scaled by the weight and
	// centred on 1 gives a strictly-positive, bounded multiplier.
	norm := (salience - domain.NeutralSalience) / (1 - domain.NeutralSalience)
	return 1 + SaliencePriorityWeight*norm
}
