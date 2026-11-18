package query

import (
	"context"
	"fmt"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
)

// retractionScanLimit bounds the one journal read a recall makes for
// retractions. Retraction is a deliberate, audited act, so a brain holds few;
// reaching the bound is reported as an error rather than silently treating the
// overflow as live evidence.
const retractionScanLimit = 100_000

// WithRetractions wires the journal that records event retractions (ADR 0037),
// so recall stops ranking a retracted episode. Without it every episode is live.
func (e Engine) WithRetractions(journal ports.JournalRepository) Engine {
	e.journal = journal
	return e
}

// withoutRetracted returns a copy of e whose event reads hide every episode
// retracted at or before at. Retraction already retires the beliefs that rested
// on an episode alone; this keeps the episode itself from taking a top event
// slot, and so from displacing a live episode, and from the answer's timeline.
// at is now, or the recorded-as-of instant, so a read of the past still sees an
// episode that was retracted later (ADR 0036).
func (e Engine) withoutRetracted(ctx context.Context, at time.Time) (Engine, error) {
	if e.journal == nil {
		return e, nil
	}
	// Retractions and un-retractions (ADR 0037 §8): an episode is hidden when
	// its last transition at or before at is a retraction, so a read inside
	// the retracted window hides it and a read after its restoration shows it.
	var entries []domain.JournalEntry
	for _, kind := range []string{domain.JournalKindEventRetraction, domain.JournalKindEventUnretraction} {
		rows, err := e.journal.List(ctx, kind, retractionScanLimit)
		if err != nil {
			return e, fmt.Errorf("list %s rows: %w", kind, err)
		}
		if len(rows) >= retractionScanLimit {
			return e, fmt.Errorf("list %s rows: %d or more recorded; raise retractionScanLimit", kind, retractionScanLimit)
		}
		entries = append(entries, rows...)
	}
	histories, err := domain.FoldRetractionHistory(entries)
	if err != nil {
		return e, err
	}
	hidden := make(map[string]struct{}, len(histories))
	for id, h := range histories {
		if h.RetractedAt(at) {
			hidden[id] = struct{}{}
		}
	}
	if len(hidden) == 0 {
		return e, nil
	}
	e.events = liveEvents{eventLister: e.events, retracted: hidden}
	return e, nil
}

// liveEvents hides retracted episodes from every event read the engine makes.
type liveEvents struct {
	eventLister
	retracted map[string]struct{}
}

func (l liveEvents) keep(events []domain.Event, err error) ([]domain.Event, error) {
	if err != nil {
		return nil, err
	}
	out := events[:0:0]
	for _, ev := range events {
		if _, gone := l.retracted[ev.ID]; !gone {
			out = append(out, ev)
		}
	}
	return out, nil
}

// ListAll returns the episodes that are not retracted.
func (l liveEvents) ListAll(ctx context.Context) ([]domain.Event, error) {
	return l.keep(l.eventLister.ListAll(ctx))
}

// ListByIDs returns those of ids that are not retracted.
func (l liveEvents) ListByIDs(ctx context.Context, ids []string) ([]domain.Event, error) {
	return l.keep(l.eventLister.ListByIDs(ctx, ids))
}

// ListByRunID returns the run's episodes that are not retracted.
func (l liveEvents) ListByRunID(ctx context.Context, runID string) ([]domain.Event, error) {
	return l.keep(l.eventLister.ListByRunID(ctx, runID))
}

// liveAt is the instant whose retractions a recall honours: the recorded-as-of
// instant on a read of the past, otherwise now.
func liveAt(opts AnswerOptions) time.Time {
	if !opts.RecordedAsOf.IsZero() {
		return opts.RecordedAsOf
	}
	return time.Now().UTC()
}
