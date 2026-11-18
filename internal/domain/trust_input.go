package domain

import (
	"strings"
	"time"
)

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

// CreditChange is one change to a belief's applied net credit
// ([CreditAppliedComponentKey]). The claims row holds the current value; these
// rows are the history behind it, so the credit a belief held at an earlier
// instant can be recovered (ADR 0036 step 3). From is the value before the
// change, To the value after.
type CreditChange struct {
	ClaimID   string
	From, To  float64
	ChangedAt time.Time
}

// TrustInputChange is one change to the trust inputs a claims row stores
// itself: confidence, the per-claim half-life and the last explicit
// confirmation (ADR 0036 §7). It holds the values the claim carried BEFORE
// ChangedAt, when the brain recorded the change; the claims row holds the
// values after the last change. A belief's inputs at an instant T are
// therefore the prior values of its first change after T, or the stored values
// when nothing changed after T. Credit and evidence have their own histories
// ([CreditChange], claim_evidence.created_at).
type TrustInputChange struct {
	ClaimID       string
	Confidence    float64
	HalfLifeDays  float64
	LastConfirmed time.Time
	ChangedAt     time.Time
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

// UpsertComponents returns the confidence_components an upsert stores, given
// the incoming map and the stored one (ADR 0036 §7). It is the conflict rule
// for the column, applied identically by every backend in the upsert's
// transaction, and the same rule #334 applies to half_life_days: a write that
// carries nothing must not erase curation nothing can reconstruct.
//
//   - incoming empty: the stored map stands. Re-extraction rebuilds a claim from
//     text and carries no components, so a blind assignment erased applied
//     credit, salience and inhibition on every re-ingest.
//   - incoming carries credit (any credit:* key): incoming wins as given.
//   - otherwise: incoming, plus the stored credit:* keys. A write that sets only
//     a salience or a global tag does not reset the belief's credit.
//
// Clearing credit is not expressible through an upsert. The statement that owns
// the column is ports.BeliefCreditWriter.ApplyBeliefCredit, which writes the
// map exactly as given (credit assignment, salience tagging, inhibition decay).
func UpsertComponents(incoming, stored map[string]float64) map[string]float64 {
	if len(incoming) == 0 {
		return stored
	}
	for k := range incoming {
		if strings.HasPrefix(k, CreditComponentPrefix) {
			return incoming
		}
	}
	var out map[string]float64
	for k, v := range stored {
		if !strings.HasPrefix(k, CreditComponentPrefix) {
			continue
		}
		if out == nil {
			out = make(map[string]float64, len(incoming)+1)
			for ik, iv := range incoming {
				out[ik] = iv
			}
		}
		out[k] = v
	}
	if out == nil {
		return incoming
	}
	return out
}
