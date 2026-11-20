// Package retraction is the one call path from a delivery surface to event
// retraction (ADR 0037).
//
// The retraction itself is govwrite's: [govwrite.Writer.RetractEvent] for the
// daemon surfaces and [govwrite.ApplyRetraction] inside the library's own
// governed kernel. What every surface ALSO has to agree on — and used to
// re-derive per surface — is what happens before that call and how its report
// is rendered:
//
//   - the id is trimmed and required ([ErrEventIDRequired]);
//   - the event must exist, and an event the caller's run scope does not
//     cover is reported exactly like one that does not exist
//     ([ErrEventNotFound]), so a run-scoped token cannot probe other runs;
//   - the reason is trimmed and the actor is recorded as the change author.
//
// [Run] does those checks once, for the CLI, MCP, REST, gRPC and the Go
// library alike, and [NewReport] is the JSON wire form the CLI, MCP and REST
// return byte for byte. A sentinel error rather than a message match is what
// lets REST answer 404 and gRPC NotFound for an unknown episode instead of 500.
package retraction

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
)

// ErrEventIDRequired is returned when the caller named no event.
var ErrEventIDRequired = errors.New("an episode id is required")

// ErrEventNotFound is returned when the event does not exist, or exists in a
// run the caller may not touch. The two are deliberately indistinguishable.
var ErrEventNotFound = errors.New("episode not found")

// RetractFunc performs the governed retraction. It is
// govwrite.Writer.RetractEvent on the daemon surfaces and the library's own
// governed dispatch in the root package.
type RetractFunc func(ctx context.Context, eventID string, note domain.ChangeNote) (domain.EventRetraction, error)

// Request is one surface's retraction request, already translated from its
// own input.
type Request struct {
	EventID string
	Reason  string
	// Actor is recorded as who retracted the event (and who deprecated each
	// belief it retracts). Empty records the system sentinel.
	Actor string
	// RunAllowed, when non-nil, confines the caller to events of the runs it
	// returns true for. An event outside that scope is ErrEventNotFound.
	RunAllowed func(runID string) bool
}

// Run validates req against the event store and performs the retraction.
func Run(ctx context.Context, events ports.EventRepository, retract RetractFunc, req Request) (domain.EventRetraction, error) {
	id := strings.TrimSpace(req.EventID)
	if id == "" {
		return domain.EventRetraction{}, ErrEventIDRequired
	}
	if events == nil || retract == nil {
		return domain.EventRetraction{}, errors.New("retraction: no event store or retraction to run")
	}
	if err := checkEvent(ctx, events, id, req.RunAllowed); err != nil {
		return domain.EventRetraction{}, err
	}
	return retract(ctx, id, domain.ChangeNote{Reason: strings.TrimSpace(req.Reason), ChangedBy: req.Actor})
}

// CheckEvent reports ErrEventNotFound unless id exists and runAllowed (when
// set) covers its run. It is the existence and run-scope check every
// episode-addressed write shares (retraction, dependency edits).
func CheckEvent(ctx context.Context, events ports.EventRepository, id string, runAllowed func(string) bool) error {
	return checkEvent(ctx, events, id, runAllowed)
}

// checkEvent reports ErrEventNotFound unless id exists and runAllowed (when
// set) covers its run.
func checkEvent(ctx context.Context, events ports.EventRepository, id string, runAllowed func(string) bool) error {
	// ListByIDs returns only the events that exist, on every backend, so an
	// empty result is "not found" without matching a backend's error text.
	found, err := events.ListByIDs(ctx, []string{id})
	if err != nil {
		return fmt.Errorf("look up episode %s: %w", id, err)
	}
	if len(found) == 0 || (runAllowed != nil && !runAllowed(found[0].RunID)) {
		return fmt.Errorf("%w: %s", ErrEventNotFound, id)
	}
	return nil
}

// Report is the JSON wire form of a retraction, shared by the CLI's --json
// output, the MCP retract_episode tool and REST POST
// /v1/episodes/{id}/retract. Times are RFC 3339 strings.
type Report struct {
	EpisodeID        string                  `json:"episode_id"`
	RetractedAt      string                  `json:"retracted_at"`
	RetractedBy      string                  `json:"retracted_by"`
	Reason           string                  `json:"reason,omitempty"`
	AlreadyRetracted bool                    `json:"already_retracted"`
	Impact           domain.RetractionImpact `json:"impact"`
}

// NewReport renders a retraction record as its wire form.
func NewReport(rep domain.EventRetraction) Report {
	return Report{
		EpisodeID:        rep.EventID,
		RetractedAt:      rep.RetractedAt.UTC().Format(time.RFC3339Nano),
		RetractedBy:      rep.RetractedBy,
		Reason:           rep.Reason,
		AlreadyRetracted: rep.AlreadyRetracted,
		Impact:           rep.Impact,
	}
}

// --- Un-retraction (ADR 0037 §8) ---

// ErrNotRetracted is returned when un-retraction names an event that was never
// retracted. It is the caller's mistake, not a failure: REST answers 409 and
// gRPC FailedPrecondition. (An event whose retraction was already cleared is
// not an error: the stored un-retraction comes back with AlreadyUnretracted.)
var ErrNotRetracted = errors.New("episode was never retracted")

// historyScanLimit bounds the journal read for one event's retraction history.
// An event has one row per retraction and per un-retraction; reaching the
// bound is an error rather than a silently truncated history.
const historyScanLimit = 1000

// LoadHistory reads eventID's retractions and un-retractions from the journal.
// It is the one reader of an event's retraction state, shared by govwrite and
// every surface's pre-checks.
func LoadHistory(ctx context.Context, journal ports.JournalRepository, eventID string) (domain.RetractionHistory, error) {
	if journal == nil {
		return domain.RetractionHistory{}, errors.New("this backend has no journal to record retractions in")
	}
	entries, err := journal.ListBySubject(ctx, eventID, historyScanLimit)
	if err != nil {
		return domain.RetractionHistory{}, fmt.Errorf("read retraction history of %s: %w", eventID, err)
	}
	if len(entries) >= historyScanLimit {
		return domain.RetractionHistory{}, fmt.Errorf("read retraction history of %s: %d or more journal rows; raise historyScanLimit", eventID, historyScanLimit)
	}
	folded, err := domain.FoldRetractionHistory(entries)
	if err != nil {
		return domain.RetractionHistory{}, err
	}
	if h := folded[eventID]; h != nil {
		return *h, nil
	}
	return domain.RetractionHistory{}, nil
}

// historyBatch bounds the ids in one ListBySubjects query, well under every
// backend's bind-parameter limit.
const historyBatch = 500

// LoadHistories is [LoadHistory] for many events: one journal query per
// historyBatch ids where the backend implements
// [ports.JournalSubjectsLister], one per event otherwise. Every requested id
// is in the result; an event never retracted has an empty history.
func LoadHistories(ctx context.Context, journal ports.JournalRepository, eventIDs []string) (map[string]domain.RetractionHistory, error) {
	if journal == nil {
		return nil, errors.New("this backend has no journal to record retractions in")
	}
	out := make(map[string]domain.RetractionHistory, len(eventIDs))
	lister, batched := journal.(ports.JournalSubjectsLister)
	if !batched {
		for _, id := range eventIDs {
			h, err := LoadHistory(ctx, journal, id)
			if err != nil {
				return nil, err
			}
			out[id] = h
		}
		return out, nil
	}
	kinds := []string{domain.JournalKindEventRetraction, domain.JournalKindEventUnretraction}
	for start := 0; start < len(eventIDs); start += historyBatch {
		chunk := eventIDs[start:min(len(eventIDs), start+historyBatch)]
		entries, err := lister.ListBySubjects(ctx, chunk, kinds)
		if err != nil {
			return nil, fmt.Errorf("read retraction histories: %w", err)
		}
		folded, err := domain.FoldRetractionHistory(entries)
		if err != nil {
			return nil, err
		}
		for _, id := range chunk {
			if h := folded[id]; h != nil {
				out[id] = *h
			} else {
				out[id] = domain.RetractionHistory{}
			}
		}
	}
	return out, nil
}

// UnretractFunc performs the governed un-retraction:
// govwrite.Writer.UnretractEvent on the daemon surfaces and the library's own
// governed dispatch in the root package.
type UnretractFunc func(ctx context.Context, eventID string, note domain.ChangeNote) (domain.EventUnretraction, error)

// Unretract validates req exactly as [Run] does, refuses an event that was
// never retracted ([ErrNotRetracted]), and performs the un-retraction.
func Unretract(ctx context.Context, events ports.EventRepository, journal ports.JournalRepository, unretract UnretractFunc, req Request) (domain.EventUnretraction, error) {
	id := strings.TrimSpace(req.EventID)
	if id == "" {
		return domain.EventUnretraction{}, ErrEventIDRequired
	}
	if events == nil || unretract == nil {
		return domain.EventUnretraction{}, errors.New("un-retraction: no event store or un-retraction to run")
	}
	if err := checkEvent(ctx, events, id, req.RunAllowed); err != nil {
		return domain.EventUnretraction{}, err
	}
	history, err := LoadHistory(ctx, journal, id)
	if err != nil {
		return domain.EventUnretraction{}, err
	}
	if len(history.Retractions) == 0 {
		return domain.EventUnretraction{}, fmt.Errorf("%w: %s", ErrNotRetracted, id)
	}
	return unretract(ctx, id, domain.ChangeNote{Reason: strings.TrimSpace(req.Reason), ChangedBy: req.Actor})
}

// UnretractionReport is the JSON wire form of an un-retraction, shared by the
// CLI's --json output, the MCP unretract_episode tool and REST POST
// /v1/episodes/{id}/unretract. Times are RFC 3339 strings.
type UnretractionReport struct {
	EpisodeID          string                    `json:"episode_id"`
	UnretractedAt      string                    `json:"unretracted_at"`
	UnretractedBy      string                    `json:"unretracted_by"`
	Reason             string                    `json:"reason,omitempty"`
	Generation         int                       `json:"generation"`
	RetractedAt        string                    `json:"retracted_at"`
	RetractedBy        string                    `json:"retracted_by"`
	AlreadyUnretracted bool                      `json:"already_unretracted"`
	Impact             domain.UnretractionImpact `json:"impact"`
}

// NewUnretractionReport renders an un-retraction record as its wire form.
func NewUnretractionReport(rep domain.EventUnretraction) UnretractionReport {
	return UnretractionReport{
		EpisodeID:          rep.EventID,
		UnretractedAt:      rep.UnretractedAt.UTC().Format(time.RFC3339Nano),
		UnretractedBy:      rep.UnretractedBy,
		Reason:             rep.Reason,
		Generation:         rep.Generation,
		RetractedAt:        rep.RetractedAt.UTC().Format(time.RFC3339Nano),
		RetractedBy:        rep.RetractedBy,
		AlreadyUnretracted: rep.AlreadyUnretracted,
		Impact:             rep.Impact,
	}
}
