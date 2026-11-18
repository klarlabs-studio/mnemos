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

// Event retraction on the library surface (ADR 0037). [Memory.RetractEvent]
// runs the same request checks as the CLI, MCP, REST and gRPC (via
// internal/retraction) and the same retraction (govwrite.ApplyRetraction),
// dispatched through the library's own governed kernel so it lands on
// [Memory.LastWriteSession] like every other public write.

// actionRetractEvent maps to the axi action mnemos-retract-event
// (write-local, idempotent: a repeat writes nothing and reports the stored
// retraction).
const actionRetractEvent = "retract_event"

// ErrEventNotFound is returned by [Memory.RetractEvent] when the event does
// not exist. Match it with errors.Is.
var ErrEventNotFound = retraction.ErrEventNotFound

// EventRetraction records that an event was withdrawn as evidence. The event
// itself is immutable and stays stored; the retraction is the append-only
// fact that it no longer counts, plus what that changed.
type EventRetraction struct {
	EventID     string
	RetractedAt time.Time
	RetractedBy string
	Reason      string
	// AlreadyRetracted is set when the event had been retracted before: the
	// stored record is returned and nothing was written.
	AlreadyRetracted bool
	Impact           RetractionImpact
}

// RetractionImpact is the inspectable account of what a retraction touched.
// It lists what changed and what MAY be affected; it never asserts that a
// downstream decision or claim was wrong.
type RetractionImpact struct {
	// RetractedClaims had no live evidence left and were deprecated.
	RetractedClaims []string
	// AlreadyDeprecated were linked to the event but already deprecated;
	// their status was left alone.
	AlreadyDeprecated []string
	// Rescored kept other live evidence: they lost the link to the event and
	// their trust was recomputed on what remains.
	Rescored []RescoredClaim
	// Relationships are claim-to-claim edges touching a retracted claim,
	// kept for the audit trail.
	Relationships []string
	// Decisions cited a retracted claim. Listed for review, not judged.
	Decisions []DecisionExposure
	// OutcomeEdges are validates/refutes edges from outcomes onto a
	// retracted claim, kept as recorded.
	OutcomeEdges []string
	// Uncertainty states what the report cannot see or does not claim.
	Uncertainty []string
}

// RescoredClaim is a claim that survived a retraction on other evidence.
type RescoredClaim struct {
	ID                string
	TrustBefore       float64
	TrustAfter        float64
	RemainingEvidence int
}

// DecisionExposure names a decision and the retracted claims it cited.
type DecisionExposure struct {
	DecisionID string
	Claims     []string
}

type retractEventInput struct {
	EventID string
	Note    domain.ChangeNote
}

// RetractEvent implements [Memory.RetractEvent].
func (m *memory) RetractEvent(ctx context.Context, eventID, reason string) (EventRetraction, error) {
	dispatch := func(ctx context.Context, id string, note domain.ChangeNote) (domain.EventRetraction, error) {
		return dispatchWrite[domain.EventRetraction](ctx, m, actionRetractEvent, retractEventInput{EventID: id, Note: note})
	}
	rep, err := retraction.Run(ctx, m.conn.Events, dispatch, retraction.Request{
		EventID: eventID, Reason: reason, Actor: m.actorID,
	})
	if err != nil {
		return EventRetraction{}, fmt.Errorf("mnemos: RetractEvent: %w", err)
	}
	return toPublicRetraction(rep), nil
}

type retractEventExecutor struct{ m *memory }

// Execute retracts the event through the shared implementation and records
// the outcome on the evidence chain.
func (e retractEventExecutor) Execute(ctx context.Context, input any, _ axidomain.CapabilityInvoker) (axidomain.ExecutionResult, []axidomain.EvidenceRecord, error) {
	in, ok := writeInput[retractEventInput](input)
	if !ok {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("retract_event: unexpected input type %T", input)
	}
	rep, err := govwrite.ApplyRetraction(ctx, e.m.conn, in.EventID, in.Note)
	if err != nil {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("retract event %s: %w", in.EventID, err)
	}
	records := []axidomain.EvidenceRecord{
		{Kind: "mnemos.retract_event", Source: evidenceSourceLibrary, Value: map[string]any{
			"event_id":          in.EventID,
			"already_retracted": rep.AlreadyRetracted,
			"retracted_claims":  len(rep.Impact.RetractedBeliefs),
			"rescored_claims":   len(rep.Impact.Rescored),
			"reason":            in.Note.Reason,
			"changed_by":        in.Note.ChangedBy,
		}},
	}
	return axidomain.ExecutionResult{
		Data: rep,
		Summary: fmt.Sprintf("retracted event %s: %d claim(s) retracted, %d rescored (already retracted: %v)",
			in.EventID, len(rep.Impact.RetractedBeliefs), len(rep.Impact.Rescored), rep.AlreadyRetracted),
	}, records, nil
}

func toPublicRetraction(r domain.EventRetraction) EventRetraction {
	im := r.Impact
	out := EventRetraction{
		EventID: r.EventID, RetractedAt: r.RetractedAt, RetractedBy: r.RetractedBy,
		Reason: r.Reason, AlreadyRetracted: r.AlreadyRetracted,
		Impact: RetractionImpact{
			RetractedClaims:   im.RetractedBeliefs,
			AlreadyDeprecated: im.AlreadyDeprecated,
			Relationships:     im.Associations,
			OutcomeEdges:      im.OutcomeEdges,
			Uncertainty:       im.Uncertainty,
		},
	}
	for _, rs := range im.Rescored {
		out.Impact.Rescored = append(out.Impact.Rescored, RescoredClaim{
			ID: rs.ID, TrustBefore: rs.TrustBefore, TrustAfter: rs.TrustAfter, RemainingEvidence: rs.RemainingEvidence,
		})
	}
	for _, d := range im.Decisions {
		out.Impact.Decisions = append(out.Impact.Decisions, DecisionExposure{DecisionID: d.DecisionID, Claims: d.Beliefs})
	}
	return out
}
