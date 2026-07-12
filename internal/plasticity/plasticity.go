// Package plasticity implements the global neuromodulatory gain (ADR 0015 §2) —
// the brain's acetylcholine/norepinephrine mode switch expressed as a scalar over
// the learning rate. It distinguishes EXPECTED uncertainty (steady, known noise —
// stay the course) from UNEXPECTED uncertainty (the environment CHANGED — learn
// faster) by contrasting recent prediction-error against the run's baseline.
//
// The output is a multiplier consumed by credit assignment ([credit.SumForModulated]):
// a regime where surprise is rising above its baseline yields gain > 1 (encode mode);
// one where surprise is settling below baseline yields gain < 1 (consolidate mode).
// It is pure — a function of a time-ordered surprise series and one sensitivity knob
// — so consolidation stays deterministic and idempotent.
package plasticity

import "go.klarlabs.de/mnemos/internal/credit"

// DefaultRecentWindow is how many of the most recent surprise samples define the
// "recent" regime contrasted against the whole-series baseline.
const DefaultRecentWindow = 5

// Gain maps a time-ordered surprise series (oldest→newest) to the global plasticity
// multiplier, bounded to [credit.MinGain, credit.MaxGain].
//
// It compares the mean surprise of the most recent recentWindow samples against the
// mean of the whole series (the baseline): ratio > 1 means the world became less
// predictable than usual (unexpected uncertainty) and gain rises; ratio < 1 means it
// settled and gain falls. sensitivity scales the response about the neutral point
// (gain = 1); sensitivity <= 0 disables neuromodulation entirely (gain = 1), which is
// the default so existing behaviour is unchanged until opted into.
//
// Degrades to a neutral 1.0 when there is too little history to judge a change (fewer
// than two samples) or the baseline surprise is ~0 (nothing to be surprised about).
func Gain(series []float64, recentWindow int, sensitivity float64) float64 {
	if sensitivity <= 0 || len(series) < 2 {
		return 1
	}
	if recentWindow <= 0 || recentWindow > len(series) {
		recentWindow = DefaultRecentWindow
	}
	if recentWindow > len(series) {
		recentWindow = len(series)
	}

	baseline := mean(series)
	const eps = 1e-9
	if baseline < eps {
		return 1 // a flat, near-zero series carries no volatility signal
	}
	recent := mean(series[len(series)-recentWindow:])

	// gain = 1 + sensitivity·(recent/baseline − 1): neutral when recent == baseline,
	// above 1 when recent surprise exceeds baseline, below 1 when it falls short.
	g := 1 + sensitivity*(recent/baseline-1)
	if g < credit.MinGain {
		g = credit.MinGain
	}
	if g > credit.MaxGain {
		g = credit.MaxGain
	}
	return g
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sum := 0.0
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}
