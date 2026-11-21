package brainbench

import (
	"time"

	"go.klarlabs.de/mnemos/internal/clock"
)

// The experiment clock.
//
// Every instant a brain stores or is scored at — event ingest time, claim
// creation, status and validity history, trust freshness, as-of "now" — is
// read through internal/clock. A scenario runs under a simulated clock that
// starts at simEpoch and advances one simTick per read, so the instants a run
// produces are a pure function of the scenario rather than of the machine.
//
// Why this is needed (issue #396): on the real wall clock, two claims from one
// document — identical confidence, evidence count and evidence time, so
// identical trust in principle — were rescored a few microseconds apart, and
// those microseconds of freshness decay made one score larger in the 12th
// decimal. Ranking broke the tie on that difference, so which claim came
// first, and answer_mrr with it, changed from run to run; mean_trust moved in
// the 8th decimal. Rounding would hide it, and a tie-break key cannot fix it
// because the scores were not equal. A clock the experiment owns removes it.
//
// It is the product's own write path running on a different clock source, not
// a reimplementation of it: production reads the same seam and gets
// time.Now. Durations (latency, cost, run time) still use the real clock —
// they measure the machine, and the report keeps them out of every metric.

// simEpoch is the simulated "now" a scenario begins at. A fixed instant keeps a
// run reproducible across days, not only across consecutive runs: age_days
// back-dates from it, and freshness decays toward it.
var simEpoch = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

// simTick is how far each clock read advances the simulated clock. Successive
// reads are strictly increasing, as successive wall-clock writes are, so the
// store's history tables keep their order and recorded-as-of instants still
// fall strictly between writes.
const simTick = time.Microsecond

// withSimulatedClock routes internal/clock through a fresh simulated clock for
// the duration of fn. Installs are serialised process-wide, so two scenarios
// can never share — and interleave — one clock.
func withSimulatedClock(fn func(*clock.Simulated) error) error {
	sim := clock.NewSimulated(simEpoch, simTick)
	restore := clock.Install(sim.Now)
	defer restore()
	return fn(sim)
}
