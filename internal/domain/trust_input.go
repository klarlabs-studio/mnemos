package domain

import "time"

// CreditAppliedComponentKey is the reserved confidence_components entry that
// carries the NET credit a belief currently holds (ADR 0026 §3): the sum of its
// per-decision credit:* contributions after the ADR-0015 resistance and gain
// modulation. Canonical trust re-adds it on every recompute, so outcome credit
// survives ingest, `recompute-trust` and post-dedupe rescoring instead of being
// silently reset to the evidence base. The per-decision credit:* keys remain
// the attribution audit trail; this is the one value scoring reads.
//
// It shares [CreditComponentPrefix] so the validator admits its signed range.
// credit.Assign keys are credit:<decision>:<belief>, so it cannot collide.
const CreditAppliedComponentKey = CreditComponentPrefix + "applied"

// TrustInput is everything canonical trust is a function of (ADR 0026 §1),
// assembled by a storage backend from one claim row and its evidence.
type TrustInput struct {
	// Confidence is the claim's extraction confidence, expected in [0, 1].
	Confidence float64
	// EvidenceCount is the INDEPENDENCE-graded corroboration count
	// ([EffectiveEvidenceCount]), not the raw number of evidence links.
	EvidenceCount int
	// LatestEvidence is the timestamp of the freshest supporting episode; zero
	// when the claim has none.
	LatestEvidence time.Time
	// LastConfirmed is when the claim was last EXPLICITLY confirmed (verify, or
	// a validated outcome); zero when never. Not LastVerified: recall and replay
	// bump that as rehearsal, and rehearsal must not refresh trust.
	LastConfirmed time.Time
	// HalfLifeDays is the claim's freshness time constant; <= 0 means the
	// default. Despite the name it is an e-folding time (ADR 0026 §4).
	HalfLifeDays float64
	// Credit is the applied net credit ([CreditAppliedComponentKey]).
	Credit float64
}

// AppliedCredit returns the net credit stored in a claim's components, or 0.
func AppliedCredit(components map[string]float64) float64 {
	return components[CreditAppliedComponentKey]
}

// TrustScoring is one trust recompute pass: the scoring function, the instant it
// scores against, and the model version it implements. A backend writes the
// score together with At and ModelVersion (trust_computed_at,
// trust_model_version), so a stored trust_score is always readable as "trust.At
// under ModelVersion at At" — a cache with a provenance, not a free-standing
// fact (ADR 0026 §5).
type TrustScoring struct {
	At           time.Time
	ModelVersion string
	Score        func(TrustInput) float64
}
