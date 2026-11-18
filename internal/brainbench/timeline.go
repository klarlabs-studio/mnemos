package brainbench

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

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
// validity history, relationship and claim creation all stamp time.Now() inside
// the repositories, on every backend. Faking it — back-dating rows, or
// injecting a clock the shipped write path does not have — would measure a
// harness reimplementation of the history tables rather than the history the
// product records. So the phases run in real time, a few milliseconds apart,
// and each phase boundary is read from the same wall clock the store stamps
// with, strictly between the last write of one phase and the first of the
// next. That is the method internal/query/recorded_as_of_test.go uses.
//
// The run stays deterministic where it matters: the instants differ between
// runs, but every arm is a byte-copy of one seed measured against the same
// instants, so the noise arm still has to reproduce the control exactly.

// PhaseWindow is the real write-clock span of one timeline phase.
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
// Store timestamps carry nanoseconds, so a millisecond is ample; it is a
// constant rather than a tuning knob because nothing reads durations off it.
const phaseGap = 2 * time.Millisecond

// boundary returns an instant strictly after every write so far and strictly
// before every write that follows.
func boundary() time.Time {
	time.Sleep(phaseGap)
	at := time.Now().UTC()
	time.Sleep(phaseGap)
	return at
}

// timeline seeds the store phase by phase on the real clock.
func (s *seeder) timeline(ctx context.Context, phases []Phase) (SeedStats, error) {
	s.stats.Phases = make(PhaseTimes, len(phases))
	for _, ph := range phases {
		start := boundary()
		s.stats.Phases[ph.Name] = PhaseWindow{Start: start}
		for _, doc := range ph.Docs {
			observed := start
			if doc.ValidFrom != "" {
				observed = s.stats.Phases[doc.ValidFrom].Start
			}
			if err := s.ingest(ctx, doc.ID, doc.Source, doc.Text, observed, time.Now().UTC()); err != nil {
				return s.stats, err
			}
		}
		for i, ch := range ph.Changes {
			if err := s.apply(ctx, ch); err != nil {
				return s.stats, fmt.Errorf("brainbench: phase %s change[%d]: %w", ph.Name, i, err)
			}
			s.stats.Changes++
		}
		s.stats.Phases[ph.Name] = PhaseWindow{Start: start, End: boundary()}
	}
	return s.stats, nil
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
		return s.conn.Claims.Archive(ctx, c.ID, time.Now().UTC(), note)
	case ch.Supersede != "":
		c, err := s.findCurrent(ctx, ch.Supersede)
		if err != nil {
			return err
		}
		return s.conn.Claims.SetValidity(ctx, c.ID, time.Now().UTC(), note)
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
			CreatedAt:   time.Now().UTC(),
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
