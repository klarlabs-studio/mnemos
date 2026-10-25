package domain

import (
	"math"
	"time"
)

// CreditComponentPrefix marks the entries that credit assignment (ADR 0014)
// writes into a Claim's ConfidenceComponents map. Such entries are SIGNED trust
// deltas (a refuted prediction blames its beliefs with a negative value) and their
// key additionally encodes the driving decision and prediction, so the map doubles
// as the credit audit trail. The prefix lives here — the single package both the
// domain validator and the credit engine can reference without an import cycle.
const CreditComponentPrefix = "credit:"

// Expectation is a structured forward prediction attached to a claim (typically a
// decision or hypothesis): "this numeric quantity will be Predicted, within
// Tolerance, by Horizon". A reconciliation pass closes it against an observed
// value, emits a validates/refutes verdict, and yields a surprise scalar — the
// mechanism that mints prediction-error as a first-class signal. One per claim.
type Expectation struct {
	ClaimID   string
	Predicted float64   // the expected value
	Tolerance float64   // |predicted − observed| ≤ tolerance counts as confirmed
	Horizon   time.Time // when the prediction should have resolved

	Observed       float64 // the value that arrived (valid only when HasObservation)
	HasObservation bool
	Resolved       bool // reconciled into a verdict already

	CreatedAt time.Time
}

// Surprise returns the prediction-error scalar and whether it is meaningful.
// It is only defined once an observation has arrived; without one there is no
// error to measure and ok is false.
//
// The magnitude is |predicted − observed| divided by a scale: the Tolerance
// when one is set, so 1 means "exactly at the edge of the confirmed band" and
// > 1 means refuted by that many tolerances; otherwise |predicted| (a relative
// error); otherwise 1. Normalising keeps surprise comparable across
// predictions of very different absolute scale (a latency in ms vs an error
// rate in fractions).
//
// Surprise is a magnitude, not a verdict: whether the prediction held is
// Confirmed. Every consumer reads both from here (#395) — reconciliation,
// credit and the surprise series used to define them three ways, so one
// observation could be refuted by one and credited by another.
func (e Expectation) Surprise() (float64, bool) {
	if !e.HasObservation {
		return 0, false
	}
	return e.absError() / e.errorScale(), true
}

// Confirmed reports whether the observation fell within Tolerance of the
// prediction, and whether there is an observation to judge. With a zero
// Tolerance only an exact match confirms.
func (e Expectation) Confirmed() (confirmed, ok bool) {
	if !e.HasObservation {
		return false, false
	}
	return e.absError() <= e.Tolerance, true
}

// ExcessSurprise is how far past the confirmed band the observation landed,
// in the same scale as Surprise; 0 when it was confirmed.
func (e Expectation) ExcessSurprise() (float64, bool) {
	if !e.HasObservation {
		return 0, false
	}
	return math.Max(0, e.absError()-e.Tolerance) / e.errorScale(), true
}

func (e Expectation) absError() float64 {
	return math.Abs(e.Predicted - e.Observed)
}

func (e Expectation) errorScale() float64 {
	if e.Tolerance > 0 {
		return e.Tolerance
	}
	if p := math.Abs(e.Predicted); p > 0 {
		return p
	}
	return 1
}
