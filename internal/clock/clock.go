// Package clock is the one seam through which mnemos reads the wall clock as
// DATA: the instant a row is stamped with, and the instant a time-dependent
// score (trust freshness, staleness, salience decay, as-of "now") is evaluated
// at.
//
// Production never overrides it, so [Now] is [time.Now]. It exists because a
// reproducible experiment needs every such instant to be a function of its
// inputs: brainbench (issue #396) re-ran the same scenario and got a different
// answer_mrr, because two claims that should score identically were rescored a
// few microseconds apart, and those microseconds of freshness decay decided
// their order. Only a clock the experiment controls removes that at the root.
//
// What does NOT go through here: durations and deadlines (time.Since,
// time.Until, a budget's deadline). Those measure the machine, not the data,
// and faking them would let a simulated clock starve a real timeout.
package clock

import (
	"sync"
	"sync/atomic"
	"time"
)

// source is the override, nil in production.
var source atomic.Pointer[func() time.Time]

// Now returns the current instant: [time.Now] unless an experiment installed
// a clock with [Install].
func Now() time.Time {
	if f := source.Load(); f != nil {
		return (*f)()
	}
	return time.Now()
}

// installMu serialises installs, so two experiments can never interleave
// their clocks. An install holds it until its restore runs.
var installMu sync.Mutex

// Install routes [Now] through f until the returned restore is called. It
// blocks while another install is active: a clock is process-global, so two
// concurrent experiments sharing one would read each other's ticks.
//
// Install is for experiments and tests. Production code must not call it.
func Install(f func() time.Time) (restore func()) {
	installMu.Lock()
	source.Store(&f)
	var once sync.Once
	return func() {
		once.Do(func() {
			source.Store(nil)
			installMu.Unlock()
		})
	}
}

// Simulated is a deterministic clock: it starts at a fixed instant and every
// read advances it by a fixed tick, so successive reads are strictly
// increasing — as wall-clock writes are — while the sequence of instants is a
// pure function of the sequence of reads.
type Simulated struct {
	mu   sync.Mutex
	at   time.Time
	tick time.Duration
}

// NewSimulated returns a clock whose first read is start.
func NewSimulated(start time.Time, tick time.Duration) *Simulated {
	return &Simulated{at: start.UTC(), tick: tick}
}

// Now returns the current simulated instant and advances by one tick.
func (s *Simulated) Now() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	at := s.at
	s.at = s.at.Add(s.tick)
	return at
}

// Advance moves the clock forward by d without returning an instant: the
// simulated counterpart of sleeping.
func (s *Simulated) Advance(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.at = s.at.Add(d)
}

// AdvanceTo moves the clock forward to t. It never moves backwards; a t at or
// before the current instant is a no-op and reports false.
func (s *Simulated) AdvanceTo(t time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !t.After(s.at) {
		return false
	}
	s.at = t.UTC()
	return true
}
