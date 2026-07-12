package domain

// Unified salience / stakes (ADR 0013 §4). Prediction-error and trust answer
// "is this corroborated?" and "is this still true?"; neither captures how much a
// belief MATTERS if acting on it goes wrong. Salience is the amygdala's
// consequence-severity tag: a life-threatening fact should consolidate more
// strongly and be recalled preferentially over a trivial one of equal similarity
// or trust. It is a weight in [0,1] sourced from a linked decision's risk level,
// an outcome's severity, or an explicit override.
//
// Following the credit-assignment pattern (ADR 0014, [CreditComponentPrefix]),
// salience reuses the existing [Belief.ConfidenceComponents] map — it is stored
// under the reserved [SalienceComponentKey] rather than as a new claims-table
// column, so there is no cross-backend schema migration. A claim with no salience
// component is treated as [NeutralSalience]: absent means "no stakes signal", not
// "zero stakes".

const (
	// SalienceComponentKey is the reserved confidence_components entry carrying a
	// claim's unified salience / stakes weight in [0,1]. It is an ordinary
	// (non-credit) component, so the validator already constrains it to [0,1].
	SalienceComponentKey = "salience"

	// NeutralSalience is the baseline stakes weight for a claim with no salience
	// component. It is the midpoint, so an unset claim neither gains nor loses
	// priority: the retrieval and consolidation biases are inert at neutral, which
	// keeps behaviour byte-for-byte unchanged for the corpus that never sets it.
	NeutralSalience = 0.5
)

// Salience returns the claim's explicit stakes weight and whether one was set.
// A stored value outside [0,1] is clamped defensively (the validator rejects such
// writes, but a hand-edited store should still read sanely). When no component is
// present the neutral baseline is returned with ok=false.
func (c Belief) Salience() (float64, bool) {
	v, ok := c.ConfidenceComponents[SalienceComponentKey]
	if !ok {
		return NeutralSalience, false
	}
	switch {
	case v < 0:
		v = 0
	case v > 1:
		v = 1
	}
	return v, true
}

// EffectiveSalience is the convenience form: the stored salience, or
// [NeutralSalience] when unset. Retrieval and consolidation ranking read this so
// an unmarked claim contributes a neutral, inert weight.
func (c Belief) EffectiveSalience() float64 {
	v, _ := c.Salience()
	return v
}
