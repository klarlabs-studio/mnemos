package mnemos

import (
	"context"
	"fmt"
	"time"

	axidomain "go.klarlabs.de/axi/domain"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/retraction"
)

// Event un-retraction on the library surface (ADR 0037 §8).
// [Memory.UnretractEvent] runs the same request checks as the CLI, MCP, REST
// and gRPC (via internal/retraction) and the same un-retraction
// (govwrite.ApplyUnretraction), dispatched through the library's own governed
// kernel.

// actionUnretractEvent maps to the axi action mnemos-unretract-event
// (write-local, idempotent: a repeat writes nothing and reports the stored
// un-retraction).
const actionUnretractEvent = "unretract_event"

// ErrNotRetracted is returned by [Memory.UnretractEvent] when the event was
// never retracted. Match it with errors.Is.
var ErrNotRetracted = retraction.ErrNotRetracted

// EventUnretraction records that an event's retraction was cleared: the event
// counts as evidence again. The retraction stays on record next to it.
type EventUnretraction struct {
	EventID       string
	UnretractedAt time.Time
	UnretractedBy string
	Reason        string
	// Generation is the generation of the cleared retraction (1 for an
	// event's first retraction).
	Generation int
	// RetractedAt and RetractedBy identify the cleared retraction.
	RetractedAt time.Time
	RetractedBy string
	// AlreadyUnretracted is set when the retraction had been cleared before:
	// the stored record is returned and nothing was written.
	AlreadyUnretracted bool
	Impact             UnretractionImpact
}

// UnretractionImpact is what an un-retraction changed, and what it left alone
// and why.
type UnretractionImpact struct {
	// Restored claims were deprecated by the retraction and nothing changed
	// them since; each is back at its earlier status.
	Restored []RestoredClaim
	// Skipped claims were deprecated by the retraction but changed since;
	// they are left as they are.
	Skipped []SkippedClaim
	// Relinked claims were rescored by the retraction: they have the event
	// back as evidence and were rescored. Their status is unchanged.
	Relinked []RelinkedClaim
	// Uncertainty states what the report cannot see or does not claim.
	Uncertainty []string
}

// RestoredClaim is a claim an un-retraction moved back to Status.
type RestoredClaim struct {
	ID     string
	Status string
}

// SkippedClaim is a claim an un-retraction left alone, and why.
type SkippedClaim struct {
	ID     string
	Reason string
}

// RelinkedClaim is a claim that regained its evidence link to the event.
type RelinkedClaim struct {
	ID          string
	Status      string
	TrustBefore float64
	TrustAfter  float64
}

// UnretractEvent implements [Memory.UnretractEvent].
func (m *memory) UnretractEvent(ctx context.Context, eventID, reason string) (EventUnretraction, error) {
	dispatch := func(ctx context.Context, id string, note domain.ChangeNote) (domain.EventUnretraction, error) {
		return dispatchWrite[domain.EventUnretraction](ctx, m, actionUnretractEvent, retractEventInput{EventID: id, Note: note})
	}
	rep, err := retraction.Unretract(ctx, m.conn.Events, m.conn.Journal, dispatch, retraction.Request{
		EventID: eventID, Reason: reason, Actor: m.actorID,
	})
	if err != nil {
		return EventUnretraction{}, fmt.Errorf("mnemos: UnretractEvent: %w", err)
	}
	return toPublicUnretraction(rep), nil
}

type unretractEventExecutor struct{ m *memory }

// Execute un-retracts the event through the shared implementation and records
// the outcome on the evidence chain.
func (e unretractEventExecutor) Execute(ctx context.Context, input any, _ axidomain.CapabilityInvoker) (axidomain.ExecutionResult, []axidomain.EvidenceRecord, error) {
	in, ok := writeInput[retractEventInput](input)
	if !ok {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("unretract_event: unexpected input type %T", input)
	}
	rep, err := govwrite.ApplyUnretraction(ctx, e.m.conn, in.EventID, in.Note)
	if err != nil {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("unretract event %s: %w", in.EventID, err)
	}
	records := []axidomain.EvidenceRecord{
		{Kind: "mnemos.unretract_event", Source: evidenceSourceLibrary, Value: map[string]any{
			"event_id":            in.EventID,
			"already_unretracted": rep.AlreadyUnretracted,
			"generation":          rep.Generation,
			"restored_claims":     len(rep.Impact.Restored),
			"skipped_claims":      len(rep.Impact.Skipped),
			"relinked_claims":     len(rep.Impact.Relinked),
			"reason":              in.Note.Reason,
			"changed_by":          in.Note.ChangedBy,
		}},
	}
	return axidomain.ExecutionResult{
		Data: rep,
		Summary: fmt.Sprintf("un-retracted event %s: %d claim(s) restored, %d skipped, %d relinked (already un-retracted: %v)",
			in.EventID, len(rep.Impact.Restored), len(rep.Impact.Skipped), len(rep.Impact.Relinked), rep.AlreadyUnretracted),
	}, records, nil
}

func toPublicUnretraction(r domain.EventUnretraction) EventUnretraction {
	out := EventUnretraction{
		EventID: r.EventID, UnretractedAt: r.UnretractedAt, UnretractedBy: r.UnretractedBy, Reason: r.Reason,
		Generation: r.Generation, RetractedAt: r.RetractedAt, RetractedBy: r.RetractedBy,
		AlreadyUnretracted: r.AlreadyUnretracted,
		Impact:             UnretractionImpact{Uncertainty: r.Impact.Uncertainty},
	}
	for _, c := range r.Impact.Restored {
		out.Impact.Restored = append(out.Impact.Restored, RestoredClaim{ID: c.ID, Status: string(c.Status)})
	}
	for _, c := range r.Impact.Skipped {
		out.Impact.Skipped = append(out.Impact.Skipped, SkippedClaim{ID: c.ID, Reason: c.Reason})
	}
	for _, c := range r.Impact.Relinked {
		out.Impact.Relinked = append(out.Impact.Relinked, RelinkedClaim{
			ID: c.ID, Status: string(c.Status), TrustBefore: c.TrustBefore, TrustAfter: c.TrustAfter,
		})
	}
	return out
}
