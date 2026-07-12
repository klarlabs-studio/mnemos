package domain

// Competitive inhibition / retrieval-induced forgetting (ADR 0016). Recalling a belief
// that decisively beats a rival on the same topic actively suppresses the rival's
// RETRIEVABILITY — the brain's retrieval-induced forgetting. Because a contradiction
// loser is contested, not disproven, inhibition is deliberately weak: it lowers ranking
// only, never trust or status, is bounded so it tips ties rather than sinking better
// matches, and decays toward zero during consolidation so a suppression fades unless
// renewed (and a loser that later earns evidence recovers).
//
// Like salience (ADR 0013 §4) and credit (ADR 0014), inhibition reuses the existing
// [Belief.ConfidenceComponents] map under a reserved key — no schema migration. Its
// neutral value is 0 (unlike salience's 0.5) because inhibition is a one-sided penalty,
// not a two-sided bias: an unset claim carries no suppression and ranks exactly as before.

const (
	// InhibitionComponentKey is the reserved confidence_components entry carrying a
	// claim's retrieval-suppression magnitude in [0,1]. It is an ordinary (non-credit)
	// component, so the validator already constrains it to [0,1].
	InhibitionComponentKey = "inhibition"
)

// Inhibition returns the claim's stored retrieval-suppression magnitude and whether one
// was set. A value outside [0,1] is clamped defensively. When no component is present,
// 0 (no suppression) is returned with ok=false.
func (c Belief) Inhibition() (float64, bool) {
	v, ok := c.ConfidenceComponents[InhibitionComponentKey]
	if !ok {
		return 0, false
	}
	switch {
	case v < 0:
		v = 0
	case v > 1:
		v = 1
	}
	return v, true
}

// EffectiveInhibition is the convenience form: the stored suppression magnitude, or 0
// when unset. Retrieval ranking reads this so an unsuppressed claim contributes nothing.
func (c Belief) EffectiveInhibition() float64 {
	v, _ := c.Inhibition()
	return v
}
