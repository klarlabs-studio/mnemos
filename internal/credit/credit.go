// Package credit implements credit assignment — the outcome→belief-trust loop
// (ADR 0014, the capstone of ADR 0013 §1). It is the missing half of learning:
// evidence-based trust (confidence × corroboration × freshness) answers "is this
// corroborated?", but never "did acting on it work out?". Credit assignment
// closes that loop. When a Decision's forward Expectation resolves against an
// observation, the signed prediction error is propagated back to the beliefs
// that informed the decision — validated predictions credit their beliefs,
// refuted ones blame them — as a bounded, decayed, attributed trust delta.
//
// The core mechanism here is a pure function ([Assign]): given the recorded
// decisions and the expectations attached to their belief claims, it computes
// the attributed credit contributions for each belief. It has no I/O and no
// wall-clock term, so re-running is deterministic and idempotent — the same
// inputs always yield the same contributions, which the persistence layer writes
// by assignment (never accumulation), so a second pass never double-credits.
package credit

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"go.klarlabs.de/mnemos/internal/domain"
)

const (
	// Step bounds the magnitude of one decision's total trust nudge before it is
	// split across the decision's beliefs. Deliberately small: a single outcome
	// nudges trust, it does not overwrite the evidence-based score. This is the
	// "bounded" half of the ADR-0014 update.
	Step = 0.10

	// CreditCap bounds the ACCUMULATED credit a single belief can carry across all
	// the decisions it informed, so trust can move at most ±CreditCap from its
	// evidence-based base. Repeated same-sign outcomes therefore have diminishing
	// marginal effect once the cap is reached — the "decayed" half of the update
	// (a saturating eligibility, not an unbounded gradient).
	CreditCap = 0.30

	// refuteSaturation is the surprise span, in tolerance units BEYOND the
	// confirmed band, over which blame ramps from 0 up to a full -Step. Past it,
	// blame saturates: an extreme outlier can't dominate a merely-wrong belief.
	refuteSaturation = 3.0
)

// componentKeyPrefix marks the keys credit assignment writes into a claim's
// confidence_components map. Each key additionally encodes the decision and the
// prediction that drove the change, so the map doubles as the attribution audit
// trail the ADR-0011 guardrail requires (no silent trust mutation). Defined in the
// domain package so the claim validator and this engine share one source of truth.
const componentKeyPrefix = domain.CreditComponentPrefix

// Contribution is one attributed credit entry applied to a belief. Key records
// WHICH decision and prediction drove the change (the provenance the ADR-0011
// guardrail requires); Delta is the signed, bounded per-belief trust delta.
type Contribution struct {
	Key   string
	Delta float64
}

// IsCreditKey reports whether a confidence_components key was written by credit
// assignment. Callers use it to preserve a claim's other (non-credit) component
// decompositions while rewriting the credit entries on each pass.
func IsCreditKey(k string) bool { return strings.HasPrefix(k, componentKeyPrefix) }

// Assign computes the attributed credit contributions implied by resolved
// expectations on the beliefs of the given decisions. It is pure and
// deterministic — no I/O, no wall clock — so the same inputs always produce the
// same result and re-running is idempotent.
//
// exps holds the expectation for any belief claim that has one; only expectations
// with an observation contribute (an unobserved prediction is neither confirmed
// nor refuted, so its beliefs are left unchanged).
//
// Mechanism (ADR 0014). For each decision, each belief that carries an observed
// expectation is reconciled into a signed error via [decisionDelta]: a validated
// prediction (surprise within tolerance) credits, a refuted one blames, with
// magnitude drawn from the expectation's surprise. That decision-level credit is
// then split EQUALLY across all of the decision's beliefs — every belief was a
// load-bearing input, so v1 attributes blame/credit uniformly. Contributions are
// keyed by (decision, prediction) so distinct decisions and predictions attribute
// independently and overwrite idempotently.
func Assign(decisions []domain.Decision, exps map[string]domain.Expectation) map[string][]Contribution {
	// Copy + sort so the output is deterministic regardless of input order.
	ds := make([]domain.Decision, len(decisions))
	copy(ds, decisions)
	sort.Slice(ds, func(i, j int) bool { return ds[i].ID < ds[j].ID })

	out := map[string][]Contribution{}
	for _, d := range ds {
		n := len(d.Beliefs)
		if n == 0 {
			continue
		}
		for _, b := range d.Beliefs {
			exp, ok := exps[b]
			if !ok || !exp.HasObservation {
				continue
			}
			delta, ok := decisionDelta(exp)
			if !ok {
				continue
			}
			per := delta / float64(n)
			key := fmt.Sprintf("%s%s:%s", componentKeyPrefix, d.ID, b)
			for _, target := range d.Beliefs {
				out[target] = append(out[target], Contribution{Key: key, Delta: per})
			}
		}
	}
	return out
}

// decisionDelta maps one resolved expectation to the signed, bounded credit for
// the decision it informed. Validated predictions (surprise within tolerance)
// yield positive credit, largest for a tight hit and fading to zero at the
// tolerance edge; refuted ones yield negative credit that saturates as the miss
// grows. The magnitude is drawn from [domain.Expectation.Surprise], already
// normalized to tolerance units. Result is in [-Step, +Step].
func decisionDelta(exp domain.Expectation) (float64, bool) {
	s, ok := exp.Surprise()
	if !ok {
		return 0, false
	}
	if s <= 1 { // within tolerance → the prediction was borne out (validated)
		return Step * (1 - s), true // s in [0,1] → magnitude in [0, Step]
	}
	over := s - 1 // tolerance units past the confirmed band
	if over > refuteSaturation {
		over = refuteSaturation
	}
	return -Step * (over / refuteSaturation), true
}

// SumFor returns the net credit a set of contributions imply for one belief,
// clamped to ±CreditCap. The clamp is the accumulation bound: however many
// outcomes reinforce the same belief, credit can shift its trust by at most
// CreditCap from the evidence-based base.
func SumFor(contribs []Contribution) float64 {
	sum := 0.0
	for _, c := range contribs {
		sum += c.Delta
	}
	return math.Max(-CreditCap, math.Min(CreditCap, sum))
}
