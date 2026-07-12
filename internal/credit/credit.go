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
	"time"

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

// Learning-dynamics modulation bounds (ADR 0015). These govern the two adaptive
// learning-rate controls layered over the base credit update by [SumForModulated]:
// per-belief metaplasticity (crystallization) and the global neuromodulatory gain.
const (
	// MinResistance is the strongest crystallization a long-stable, often-verified
	// belief reaches: it still updates at MinResistance of the nominal rate, so a
	// crystallized belief resists churn without ever fully fossilizing.
	MinResistance = 0.25

	// BlameBreakability keeps a crystallized belief disconfirmable. Metaplasticity
	// resists CREDIT fully (a stable belief shouldn't keep inflating) but resists
	// BLAME only partway toward MinResistance, so strong disconfirmation still moves
	// a long-held belief — the asymmetry that disconfirmation is more informative
	// than confirmation. 1.0 = blame ignores resistance entirely; 0.0 = blame
	// resisted as much as credit.
	BlameBreakability = 0.6

	// MinGain / MaxGain bound the global neuromodulatory plasticity gain (ADR 0015
	// §2). A high-volatility regime raises the effective learning rate toward MaxGain
	// (encode mode); a stable one lowers it toward MinGain (consolidate mode). The
	// gain is hard-bounded here so no volatility signal can move trust past ±CreditCap.
	MinGain = 0.5
	MaxGain = 2.0

	// resistanceAgeHalfLifeDays / resistanceVerifyHalf set how fast a belief
	// crystallizes with age and repeated verification: each saturates at its
	// half-value at these points (30 days old, 3 verifications).
	resistanceAgeHalfLifeDays = 30.0
	resistanceVerifyHalf      = 3.0
)

// ResistanceFor returns a belief's metaplastic update-resistance in
// [MinResistance, 1] (ADR 0015 §1): how much its crystallization damps an incoming
// credit delta. Stability grows with age since creation, verification count, and
// recency of the last verification — a young, rarely-verified, or long-unverified
// belief is fully plastic (resistance → 1); an old, often-verified, recently
// confirmed one resists (→ MinResistance). Pure and deterministic given now.
func ResistanceFor(c domain.Claim, now time.Time) float64 {
	ref := c.CreatedAt
	ageDays := now.Sub(ref).Hours() / 24
	if ageDays < 0 {
		ageDays = 0
	}
	ageF := ageDays / (ageDays + resistanceAgeHalfLifeDays)

	verifyF := float64(c.VerifyCount) / (float64(c.VerifyCount) + resistanceVerifyHalf)

	// A belief not re-verified in a while is losing its crystallization, so recency
	// pulls stability back down (falls back to creation time when never verified).
	lv := c.LastVerified
	if lv.IsZero() || lv.Before(ref) {
		lv = ref
	}
	recencyDays := now.Sub(lv).Hours() / 24
	if recencyDays < 0 {
		recencyDays = 0
	}
	recencyF := resistanceAgeHalfLifeDays / (recencyDays + resistanceAgeHalfLifeDays)

	// All three must be high to crystallize (product, not sum). stability 0 →
	// resistance 1 (fully plastic); stability 1 → MinResistance.
	stability := ageF * verifyF * recencyF
	return 1 - stability*(1-MinResistance)
}

// SumForModulated is [SumFor] with the two ADR-0015 learning-rate controls applied:
// per-belief metaplasticity (resistance, from [ResistanceFor]) and the global
// neuromodulatory gain. resistance down-scales updates for stable beliefs — but
// blame is resisted less than credit ([BlameBreakability]) so a crystallized belief
// stays breakable. gain scales the whole update by the global volatility signal. The
// result is still hard-clamped to ±CreditCap regardless of gain, so neuromodulation
// can change how FAST trust moves but never how FAR. Passing resistance=1, gain=1
// recovers [SumFor] exactly (the neutral, backward-compatible path).
func SumForModulated(contribs []Contribution, resistance, gain float64) float64 {
	resistance = clampf(resistance, MinResistance, 1)
	gain = clampf(gain, MinGain, MaxGain)
	sum := 0.0
	for _, c := range contribs {
		d := c.Delta * gain
		if d >= 0 {
			d *= resistance // crystallization resists credit fully
		} else {
			d *= resistance + (1-resistance)*BlameBreakability // blame resisted less
		}
		sum += d
	}
	return math.Max(-CreditCap, math.Min(CreditCap, sum))
}

func clampf(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

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
