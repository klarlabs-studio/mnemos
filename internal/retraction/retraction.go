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
	// ListByIDs returns only the events that exist, on every backend, so an
	// empty result is "not found" without matching a backend's error text.
	found, err := events.ListByIDs(ctx, []string{id})
	if err != nil {
		return domain.EventRetraction{}, fmt.Errorf("look up episode %s: %w", id, err)
	}
	if len(found) == 0 || (req.RunAllowed != nil && !req.RunAllowed(found[0].RunID)) {
		return domain.EventRetraction{}, fmt.Errorf("%w: %s", ErrEventNotFound, id)
	}
	return retract(ctx, id, domain.ChangeNote{Reason: strings.TrimSpace(req.Reason), ChangedBy: req.Actor})
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
