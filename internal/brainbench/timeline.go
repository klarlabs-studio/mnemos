package brainbench

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/clock"
	"go.klarlabs.de/mnemos/internal/domain"
)

// Temporal reconstruction track.
//
// A timeline scenario asks the two bitemporal questions an incident review
// asks: "what did the brain believe at T?" (transaction time,
// Query.RecordedAsOf) and "what is known now about T?" (valid time,
// Query.AsOf). Both need instants that sit strictly between writes.
//
// Transaction time comes from the store's own write clock: status history,
// validity history, relationship and claim creation all stamp clock.Now()
// inside the repositories. Back-dating rows would measure a harness
// reimplementation of the history tables rather than the history the product
// records, so the seeder does not; it lets the shipped write path stamp, and
// reads each phase boundary from the same clock, strictly between the last
// write of one phase and the first of the next. That is the method
// internal/query/recorded_as_of_test.go uses.
//
// The clock is the scenario's simulated clock (clock.go), not the wall clock:
// a phase gap is a clock advance rather than a sleep, and the instants are the
// same on every run (issue #396).

// PhaseWindow is the write-clock span of one timeline phase.
type PhaseWindow struct {
	// Start is read before the phase's first write.
	Start time.Time `json:"start"`
	// End is read after the phase's last write. As-of probes ask about End:
	// "as of the end of Tuesday".
	End time.Time `json:"end"`
}

// PhaseTimes maps a phase name to its window.
type PhaseTimes map[string]PhaseWindow

// phaseGap separates a boundary instant from the writes on either side of it.
// Every clock read is a distinct instant already; the gap makes phases easy to
// tell apart in a report. It is a constant rather than a tuning knob because
// nothing reads durations off it.
const phaseGap = 2 * time.Millisecond

// boundary returns an instant strictly after every write so far and strictly
// before every write that follows.
func (s *seeder) boundary() time.Time {
	s.sim.Advance(phaseGap)
	at := clock.Now().UTC()
	s.sim.Advance(phaseGap)
	return at
}

// forwardReservation is how far ahead of the write clock a later phase's
// start is reserved when a document announces facts that take effect then.
//
// A document ingested in February whose facts hold from March needs March's
// start instant as its valid time at ingest, before March has begun. The
// seeder reserves an instant this far ahead, and when March begins it waits
// for that instant and uses it as the phase's start. If the phases in between
// take longer than the reservation, the seed fails rather than silently
// placing March's start before February's end. On the simulated clock a
// phase consumes one tick per clock read, far below this; it costs nothing to
// wait for, since waiting is a clock advance.
const forwardReservation = 2 * time.Second

// timeline seeds the store phase by phase on the simulated clock.
func (s *seeder) timeline(ctx context.Context, phases []Phase) (SeedStats, error) {
	s.stats.Phases = make(PhaseTimes, len(phases))
	order := make(map[string]int, len(phases))
	for i, ph := range phases {
		order[ph.Name] = i
	}
	reserved := make(map[string]time.Time)
	for i, ph := range phases {
		start, err := s.phaseStart(ph.Name, reserved)
		if err != nil {
			return s.stats, err
		}
		s.stats.Phases[ph.Name] = PhaseWindow{Start: start}
		for _, doc := range ph.Docs {
			observed := start
			if doc.ValidFrom != "" {
				if order[doc.ValidFrom] > i {
					observed = reserve(reserved, doc.ValidFrom)
				} else {
					observed = s.stats.Phases[doc.ValidFrom].Start
				}
			}
			if err := s.ingest(ctx, doc.ID, doc.Source, doc.Text, nil, observed, clock.Now().UTC()); err != nil {
				return s.stats, err
			}
		}
		began := time.Now()
		for j, ch := range ph.Changes {
			if err := s.apply(ctx, ch); err != nil {
				return s.stats, fmt.Errorf("brainbench: phase %s change[%d]: %w", ph.Name, j, err)
			}
			s.stats.Changes++
		}
		s.stats.CurationDuration += time.Since(began)
		s.stats.Phases[ph.Name] = PhaseWindow{Start: start, End: s.boundary()}
	}
	return s.stats, nil
}

// reserve returns the instant reserved for phase's start, reserving one
// forwardReservation ahead of now on first use.
func reserve(reserved map[string]time.Time, phase string) time.Time {
	if at, ok := reserved[phase]; ok {
		return at
	}
	at := clock.Now().UTC().Add(forwardReservation)
	reserved[phase] = at
	return at
}

// phaseStart returns the instant a phase begins at: a fresh boundary, or the
// instant reserved for it, waited for. A reserved instant already in the past
// would put this phase's start before the previous phase's last write, so it
// is an error, never a silent misordering.
func (s *seeder) phaseStart(phase string, reserved map[string]time.Time) (time.Time, error) {
	start := s.boundary()
	at, ok := reserved[phase]
	if !ok {
		return start, nil
	}
	if !start.Before(at) {
		return time.Time{}, fmt.Errorf("brainbench: phase %s began at %s, after the instant %s reserved for it; "+
			"the phases before it took longer than %s", phase, start.Format(time.RFC3339Nano),
			at.Format(time.RFC3339Nano), forwardReservation)
	}
	s.sim.AdvanceTo(at.Add(phaseGap))
	return at, nil
}

// apply performs one curation change through the store's own write methods,
// so the history rows are the ones the product records.
func (s *seeder) apply(ctx context.Context, ch Change) error {
	note := domain.ChangeNote{Reason: ch.Reason, ChangedBy: SeedActor}
	if note.Reason == "" {
		note.Reason = "brainbench timeline change"
	}
	switch {
	case ch.Retract != "":
		c, err := s.findCurrent(ctx, ch.Retract)
		if err != nil {
			return err
		}
		c.Status = domain.ClaimStatusDeprecated
		return s.conn.Claims.UpsertWithReasonAs(ctx, []domain.Claim{c}, note.Reason, SeedActor)
	case ch.Archive != "":
		c, err := s.findCurrent(ctx, ch.Archive)
		if err != nil {
			return err
		}
		return s.conn.Claims.Archive(ctx, c.ID, clock.Now().UTC(), note)
	case ch.Supersede != "":
		c, err := s.findCurrent(ctx, ch.Supersede)
		if err != nil {
			return err
		}
		return s.conn.Claims.SetValidity(ctx, c.ID, clock.Now().UTC(), note)
	case ch.SupersedeBy != nil:
		old, err := s.findCurrent(ctx, ch.SupersedeBy.Old)
		if err != nil {
			return err
		}
		successor, err := s.findCurrent(ctx, ch.SupersedeBy.New)
		if err != nil {
			return err
		}
		// The cutoff of `mnemos resolve --supersedes`: the successor's valid
		// start, falling back to its creation.
		cutoff := successor.ValidFrom
		if cutoff.IsZero() {
			cutoff = successor.CreatedAt
		}
		return s.conn.Claims.SetValidity(ctx, old.ID, cutoff, note)
	case ch.Contradict != nil:
		from, err := s.findCurrent(ctx, ch.Contradict.From)
		if err != nil {
			return err
		}
		to, err := s.findCurrent(ctx, ch.Contradict.To)
		if err != nil {
			return err
		}
		sum := sha256.Sum256([]byte(s.scenario + "\x00" + from.ID + "\x00" + to.ID))
		return s.conn.Relationships.Upsert(ctx, []domain.Relationship{{
			ID:          "rel_bb_" + hex.EncodeToString(sum[:8]),
			Type:        domain.RelationshipTypeContradicts,
			FromClaimID: from.ID,
			ToClaimID:   to.ID,
			CreatedAt:   clock.Now().UTC(),
			CreatedBy:   SeedActor,
		}})
	default:
		return fmt.Errorf("empty change")
	}
}

// findCurrent returns the one current, non-deprecated claim whose text
// contains target. Zero or several matches is an authoring error: applying a
// change to the wrong claim, or to none, would leave the scenario testing a
// different timeline than its author wrote while still producing a report.
func (s *seeder) findCurrent(ctx context.Context, target string) (domain.Claim, error) {
	claims, err := s.conn.Claims.ListAll(ctx)
	if err != nil {
		return domain.Claim{}, fmt.Errorf("list claims: %w", err)
	}
	var matches []domain.Claim
	for _, c := range claims {
		if c.IsCurrent() && c.Status != domain.ClaimStatusDeprecated && containsFold(c.Text, target) {
			matches = append(matches, c)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return domain.Claim{}, fmt.Errorf("no current claim contains %q", target)
	default:
		texts := make([]string, len(matches))
		for i, m := range matches {
			texts[i] = m.Text
		}
		return domain.Claim{}, fmt.Errorf("%d current claims contain %q: %s",
			len(matches), target, strings.Join(texts, " | "))
	}
}
