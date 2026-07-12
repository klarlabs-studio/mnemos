package domain

import "time"

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
// The magnitude is expressed in tolerance-units when a positive Tolerance is
// set — |predicted − observed| / tolerance — so a value of 1 means "exactly at
// the edge of the confirmed band" and > 1 means the prediction was refuted by
// that many tolerances. This normalization makes surprise comparable across
// predictions of wildly different absolute scale (a latency in ms vs an error
// rate in fractions). With no tolerance, it falls back to the raw absolute
// error.
func (e Expectation) Surprise() (float64, bool) {
	if !e.HasObservation {
		return 0, false
	}
	diff := e.Predicted - e.Observed
	if diff < 0 {
		diff = -diff
	}
	if e.Tolerance > 0 {
		return diff / e.Tolerance, true
	}
	return diff, true
}
