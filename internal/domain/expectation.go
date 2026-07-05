package domain

import "time"

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
