package trust

import (
	"math"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// The forward-looking decay projection behind the brain-health trust_decay
// vital (ADR 0032). It lives here, beside [At], so every reader of it — the
// vital itself and the brainbench harness that scores it per belief identity —
// projects with one function instead of a copy that can drift.
const (
	// LowTrustFloor is the age-neutral trust below which a belief is
	// untrusted on its merits (ADR 0032): the low_trust vital counts it, and
	// the decay projection excludes it, since it has no trust left to lose.
	LowTrustFloor = 0.30
	// DecayHorizonDays is how far ahead the trust_decay projection looks. A
	// month: the natural cadence at which anyone reviews a brain, and shorter
	// than every half-life in play, so the vital leads staleness rather than
	// restating it after the fact.
	DecayHorizonDays = 30.0
	// DecayMinExpected is the fewest expected crossings for which the
	// crossing/expected ratio means anything. Below it the ratio is noise (one
	// crossing against 0.2 expected reads 5×) and is reported unknown.
	DecayMinExpected = 1.0
)

// Reference is the instant a belief's freshness is measured from: its latest
// dated evidence or its last explicit confirmation, whichever is later (ADR
// 0026). Zero when it has neither.
func Reference(in domain.TrustInput) time.Time {
	if in.LastConfirmed.After(in.LatestEvidence) {
		return in.LastConfirmed
	}
	return in.LatestEvidence
}

// AgeNeutral is a belief's trust at its own reference instant: [At] with
// freshness 1, so only confidence, corroboration and credit remain (ADR 0032).
func AgeNeutral(in domain.TrustInput, now time.Time) float64 {
	ref := Reference(in)
	if ref.IsZero() || ref.After(now) {
		ref = now
	}
	return At(in, ref)
}

// FloorAgeDays is how many days after its reference a belief's freshness
// reaches FreshnessFloor: τ·ln(1/floor), with τ its own time constant.
func FloorAgeDays(in domain.TrustInput) float64 {
	tau := in.HalfLifeDays
	if tau <= 0 {
		tau = FreshnessHalfLifeDays
	}
	return tau * math.Log(1/FreshnessFloor)
}

// ProjectDecay places a belief on its way to the freshness floor (ADR 0032).
// approaching: it has a dated reference, is not low-trust on its merits, and
// has not reached the floor yet. crossing: it reaches the floor within
// DecayHorizonDays. expected: the chance a belief of its time constant crosses
// within the horizon when beliefs arrive at a steady rate (horizon / floor age,
// capped at 1), so summing it gives the crossings a steady brain would show.
//
// A belief with no dated evidence and no confirmation cannot be projected: its
// freshness is undefined rather than perfect, so it is excluded.
func ProjectDecay(in domain.TrustInput, now time.Time) (approaching, crossing bool, expected float64) {
	ref := Reference(in)
	if ref.IsZero() || AgeNeutral(in, now) < LowTrustFloor {
		return false, false, 0
	}
	age := math.Max(0, now.Sub(ref).Hours()/24)
	floorAge := FloorAgeDays(in)
	if age >= floorAge {
		return false, false, 0 // already on the floor: staleness owns it
	}
	return true, age+DecayHorizonDays >= floorAge, math.Min(1, DecayHorizonDays/floorAge)
}

// DecayTally accumulates [ProjectDecay] over a set of beliefs.
type DecayTally struct {
	Approaching, Crossing int
	Expected              float64
}

// Add projects one belief into the tally.
func (t *DecayTally) Add(in domain.TrustInput, now time.Time) {
	approaching, crossing, expected := ProjectDecay(in, now)
	if !approaching {
		return
	}
	t.Approaching++
	t.Expected += expected
	if crossing {
		t.Crossing++
	}
}

// Ratio is observed over steady-state crossings. ok is false when too few
// beliefs are on their way to the floor for the ratio to mean anything; the
// caller must then report the value as unknown, never as a healthy number.
func (t DecayTally) Ratio() (ratio float64, ok bool) {
	if t.Approaching == 0 || t.Expected < DecayMinExpected {
		return 0, false
	}
	return float64(t.Crossing) / t.Expected, true
}
