// Package salience implements the unified salience / stakes dimension (ADR 0013
// §4). It is the consequence-severity axis the cognitive core lacked: trust and
// prediction-error say whether a belief is corroborated and still true, but not
// how much it MATTERS if acting on it goes wrong. Salience supplies that — a
// bounded weight in [0,1], derived from a decision's risk level, an outcome's
// severity, or an explicit override — that biases BOTH retrieval and
// consolidation so a high-stakes belief outranks a trivial one of equal
// similarity or trust (the amygdala's salience-tagging, borrowed as dynamics).
//
// The core here is a pure function ([Compute]): given the available consequence
// signals it returns the salience weight, defaulting to [domain.NeutralSalience]
// when none is present. It has no I/O and no wall clock, so it is deterministic
// and re-running is idempotent. The value is persisted by reusing the existing
// confidence_components map ([domain.SalienceComponentKey]) — the same
// no-new-column pattern credit assignment (ADR 0014) established — so there is no
// store-schema change.
package salience

import "go.klarlabs.de/mnemos/internal/domain"

// Neutral re-exports the domain baseline so callers in this package can reason in
// salience terms without importing two packages.
const Neutral = domain.NeutralSalience

// Inputs are the consequence-severity signals available for one belief. Any field
// may be absent (an unset RiskLevel, no linked outcome, no override): [Compute]
// takes the strongest present signal and falls back to neutral when none is set.
type Inputs struct {
	// Override is an explicit operator/agent-supplied stakes weight. When
	// HasOverride is true it takes precedence over the derived signals — a human
	// marking a fact life-threatening should not be second-guessed by a heuristic.
	Override    float64
	HasOverride bool

	// RiskLevel is the risk of a decision that took this belief as a load-bearing
	// input (higher risk ⇒ higher stakes). Empty means no linked decision.
	RiskLevel domain.RiskLevel

	// Outcome is the observed result of the action a decision drove (a failure is a
	// severe consequence ⇒ higher stakes). HasOutcome guards it.
	Outcome    domain.OutcomeResult
	HasOutcome bool
}

// RiskSalience maps a decision's risk level to a stakes weight in [0,1]. The
// ladder is monotonic in the ordinal risk scale (low < medium < high < critical),
// with medium sitting at the neutral baseline so an unremarkable decision neither
// raises nor lowers priority. ok is false for an unset/unknown level.
func RiskSalience(r domain.RiskLevel) (float64, bool) {
	switch r {
	case domain.RiskLevelLow:
		return 0.3, true
	case domain.RiskLevelMedium:
		return Neutral, true
	case domain.RiskLevelHigh:
		return 0.75, true
	case domain.RiskLevelCritical:
		return 1.0, true
	default:
		return Neutral, false
	}
}

// OutcomeSalience maps an observed outcome to a stakes weight in [0,1]. Only
// adverse results carry a signal: a failure is a severe consequence (the belief
// that informed it mattered), a partial result moderately so. Success and unknown
// return ok=false — a decision working out is not evidence the belief was
// high-stakes, and salience must never be LOWERED by a good outcome (that is
// trust/credit's job, not stakes').
func OutcomeSalience(res domain.OutcomeResult) (float64, bool) {
	switch res {
	case domain.OutcomeResultFailure:
		return 0.9, true
	case domain.OutcomeResultPartial:
		return 0.7, true
	default:
		return Neutral, false
	}
}

// Compute returns the salience weight implied by the available signals. An
// explicit override wins outright; otherwise the strongest (max) of the derived
// risk and outcome-severity signals is taken — "higher risk/severity ⇒ higher
// salience". When no signal is present it returns [domain.NeutralSalience]. The
// result is always clamped to [0,1].
func Compute(in Inputs) float64 {
	if in.HasOverride {
		return clamp01(in.Override)
	}
	best := Neutral
	found := false
	if v, ok := RiskSalience(in.RiskLevel); ok {
		if !found || v > best {
			best = v
		}
		found = true
	}
	if in.HasOutcome {
		if v, ok := OutcomeSalience(in.Outcome); ok {
			if !found || v > best {
				best = v
			}
			found = true
		}
	}
	if !found {
		return Neutral
	}
	return clamp01(best)
}

func clamp01(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 1:
		return 1
	default:
		return v
	}
}
