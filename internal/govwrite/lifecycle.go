package govwrite

import (
	"context"
	"fmt"
	"time"

	axidomain "go.klarlabs.de/axi/domain"

	"go.klarlabs.de/mnemos/internal/store"
)

// The executors below carry the claim-lifecycle mutations, entity
// canonicalisation, and destructive deletes the daemon performs. Like
// every other governed write they run inside the axi kernel — the only
// place this package reaches a conn.<Repo> mutator — and each returns an
// []axidomain.EvidenceRecord so the destructive op leaves exactly one
// auditable record on the session's evidence chain. A delete with no
// audit trail is the worst case the spec guards against; routing the
// whole multi-step cascade through ONE governed action makes the
// destructive op a single atomic entry.

// --- Mark verified ---

type markVerifiedInput struct {
	ClaimID      string
	VerifiedAt   time.Time
	HalfLifeDays float64
}

type markVerifiedExecutor struct{ conn *store.Conn }

func (e markVerifiedExecutor) Execute(ctx context.Context, input any, _ axidomain.CapabilityInvoker) (axidomain.ExecutionResult, []axidomain.EvidenceRecord, error) {
	in, ok := payload[markVerifiedInput](input)
	if !ok {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("mark_verified: unexpected input %T", input)
	}
	if err := e.conn.Claims.MarkVerified(ctx, in.ClaimID, in.VerifiedAt, in.HalfLifeDays); err != nil {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("mark verified: %w", err)
	}
	return axidomain.ExecutionResult{
			Data:    in.ClaimID,
			Summary: fmt.Sprintf("verified claim %s", in.ClaimID),
		}, ev("mnemos.write.mark_verified", map[string]any{
			"claim_id": in.ClaimID, "half_life_days": in.HalfLifeDays,
		}), nil
}

// MarkVerified bumps a claim's last_verified and increments verify_count.
// An optional halfLifeDays > 0 also rewrites the per-claim freshness
// override.
func (w *Writer) MarkVerified(ctx context.Context, claimID string, verifiedAt time.Time, halfLifeDays float64) error {
	_, err := dispatch[string](ctx, w, actionMarkVerified, markVerifiedInput{
		ClaimID: claimID, VerifiedAt: verifiedAt, HalfLifeDays: halfLifeDays,
	})
	return err
}

// --- Set validity ---

type setValidityInput struct {
	ClaimID string
	ValidTo time.Time
}

type setValidityExecutor struct{ conn *store.Conn }

func (e setValidityExecutor) Execute(ctx context.Context, input any, _ axidomain.CapabilityInvoker) (axidomain.ExecutionResult, []axidomain.EvidenceRecord, error) {
	in, ok := payload[setValidityInput](input)
	if !ok {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("set_validity: unexpected input %T", input)
	}
	if err := e.conn.Claims.SetValidity(ctx, in.ClaimID, in.ValidTo); err != nil {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("set validity: %w", err)
	}
	return axidomain.ExecutionResult{
			Data:    in.ClaimID,
			Summary: fmt.Sprintf("set valid_to on claim %s", in.ClaimID),
		}, ev("mnemos.write.set_validity", map[string]any{
			"claim_id": in.ClaimID, "valid_to": in.ValidTo.Format(time.RFC3339),
		}), nil
}

// SetValidity closes a claim's validity interval at validTo (or clears
// it when validTo is the zero value).
func (w *Writer) SetValidity(ctx context.Context, claimID string, validTo time.Time) error {
	_, err := dispatch[string](ctx, w, actionSetValidity, setValidityInput{
		ClaimID: claimID, ValidTo: validTo,
	})
	return err
}

// --- Merge entities ---

type mergeEntitiesInput struct {
	WinnerID string
	LoserID  string
}

type mergeEntitiesExecutor struct{ conn *store.Conn }

func (e mergeEntitiesExecutor) Execute(ctx context.Context, input any, _ axidomain.CapabilityInvoker) (axidomain.ExecutionResult, []axidomain.EvidenceRecord, error) {
	in, ok := payload[mergeEntitiesInput](input)
	if !ok {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("merge_entities: unexpected input %T", input)
	}
	if err := e.conn.Entities.Merge(ctx, in.WinnerID, in.LoserID); err != nil {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("merge entities: %w", err)
	}
	return axidomain.ExecutionResult{
			Data:    in.WinnerID,
			Summary: fmt.Sprintf("merged %s into %s", in.LoserID, in.WinnerID),
		}, ev("mnemos.write.merge_entities", map[string]any{
			"winner_id": in.WinnerID, "loser_id": in.LoserID,
		}), nil
}

// MergeEntities folds the loser entity into the winner and deletes the
// loser. Destructive: routed through the kernel so the merge leaves an
// auditable record.
func (w *Writer) MergeEntities(ctx context.Context, winnerID, loserID string) error {
	_, err := dispatch[string](ctx, w, actionMergeEntities, mergeEntitiesInput{
		WinnerID: winnerID, LoserID: loserID,
	})
	return err
}

// --- Delete claim cascade (single atomic governed entry) ---

type deleteClaimCascadeInput struct{ ClaimID string }

type deleteClaimCascadeExecutor struct{ conn *store.Conn }

// Execute performs the full per-claim delete sequence — relationships,
// embedding, then the claim cascade (claim_evidence + status_history +
// the claim row) — as ONE governed action so the destructive op is a
// single entry on the evidence chain rather than four ungoverned reaches
// into storage. Order matters under FK enforcement: edges and the
// embedding (which reference the claim) go before the claim itself.
func (e deleteClaimCascadeExecutor) Execute(ctx context.Context, input any, _ axidomain.CapabilityInvoker) (axidomain.ExecutionResult, []axidomain.EvidenceRecord, error) {
	in, ok := payload[deleteClaimCascadeInput](input)
	if !ok {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("delete_claim_cascade: unexpected input %T", input)
	}
	if err := e.conn.Relationships.DeleteByClaim(ctx, in.ClaimID); err != nil {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("delete relationships for %s: %w", in.ClaimID, err)
	}
	if err := e.conn.Embeddings.Delete(ctx, in.ClaimID, "claim"); err != nil {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("delete embedding for %s: %w", in.ClaimID, err)
	}
	if err := e.conn.Claims.DeleteCascade(ctx, in.ClaimID); err != nil {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("delete claim %s: %w", in.ClaimID, err)
	}
	return axidomain.ExecutionResult{
			Data:    in.ClaimID,
			Summary: fmt.Sprintf("deleted claim %s (relationships + embedding + cascade)", in.ClaimID),
		}, ev("mnemos.write.delete_claim_cascade", map[string]any{
			"claim_id": in.ClaimID,
		}), nil
}

// DeleteClaimCascade deletes a claim and every row it owns
// (relationships touching it, its claim embedding, claim_evidence,
// claim_status_history, the claim row) as one governed, audited action.
func (w *Writer) DeleteClaimCascade(ctx context.Context, claimID string) error {
	_, err := dispatch[string](ctx, w, actionDeleteClaimCascade, deleteClaimCascadeInput{ClaimID: claimID})
	return err
}

// --- Delete event cascade ---

type deleteEventCascadeInput struct{ EventID string }

type deleteEventCascadeExecutor struct{ conn *store.Conn }

// Execute cascades an event delete: every claim whose evidence points at
// the event is deleted (relationships + embedding + claim cascade), then
// the event's own embedding and the event row go. One governed entry for
// the whole destructive sequence.
func (e deleteEventCascadeExecutor) Execute(ctx context.Context, input any, _ axidomain.CapabilityInvoker) (axidomain.ExecutionResult, []axidomain.EvidenceRecord, error) {
	in, ok := payload[deleteEventCascadeInput](input)
	if !ok {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("delete_event_cascade: unexpected input %T", input)
	}
	dependent, err := e.conn.Claims.ListByEventIDs(ctx, []string{in.EventID})
	if err != nil {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("list dependent claims for %s: %w", in.EventID, err)
	}
	cascaded := 0
	for _, c := range dependent {
		if err := e.conn.Relationships.DeleteByClaim(ctx, c.ID); err != nil {
			return axidomain.ExecutionResult{}, nil, fmt.Errorf("delete relationships for %s: %w", c.ID, err)
		}
		if err := e.conn.Embeddings.Delete(ctx, c.ID, "claim"); err != nil {
			return axidomain.ExecutionResult{}, nil, fmt.Errorf("delete claim embedding %s: %w", c.ID, err)
		}
		if err := e.conn.Claims.DeleteCascade(ctx, c.ID); err != nil {
			return axidomain.ExecutionResult{}, nil, fmt.Errorf("delete claim %s: %w", c.ID, err)
		}
		cascaded++
	}
	if err := e.conn.Embeddings.Delete(ctx, in.EventID, "event"); err != nil {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("delete event embedding %s: %w", in.EventID, err)
	}
	if err := e.conn.Events.DeleteByID(ctx, in.EventID); err != nil {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("delete event %s: %w", in.EventID, err)
	}
	return axidomain.ExecutionResult{
			Data:    cascaded,
			Summary: fmt.Sprintf("deleted event %s; cascaded %d claim(s)", in.EventID, cascaded),
		}, ev("mnemos.write.delete_event_cascade", map[string]any{
			"event_id": in.EventID, "cascaded_claims": cascaded,
		}), nil
}

// DeleteEventCascade deletes an event, its dependent claims (each fully
// cascaded), and the related embeddings as one governed, audited action.
// Returns the number of claims cascaded.
func (w *Writer) DeleteEventCascade(ctx context.Context, eventID string) (int, error) {
	return dispatch[int](ctx, w, actionDeleteEventCascade, deleteEventCascadeInput{EventID: eventID})
}

// --- Delete single event ---

type deleteEventInput struct{ EventID string }

type deleteEventExecutor struct{ conn *store.Conn }

func (e deleteEventExecutor) Execute(ctx context.Context, input any, _ axidomain.CapabilityInvoker) (axidomain.ExecutionResult, []axidomain.EvidenceRecord, error) {
	in, ok := payload[deleteEventInput](input)
	if !ok {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("delete_event: unexpected input %T", input)
	}
	if err := e.conn.Events.DeleteByID(ctx, in.EventID); err != nil {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("delete event %s: %w", in.EventID, err)
	}
	return axidomain.ExecutionResult{
			Data:    in.EventID,
			Summary: fmt.Sprintf("deleted event %s", in.EventID),
		}, ev("mnemos.write.delete_event", map[string]any{
			"event_id": in.EventID,
		}), nil
}

// DeleteEvent removes a single event row by id. Used by the HTTP
// delete-by-run path, which deletes events last (after their claims) so
// a partial failure leaves them referenceable for an idempotent re-run.
func (w *Writer) DeleteEvent(ctx context.Context, eventID string) error {
	_, err := dispatch[string](ctx, w, actionDeleteEvent, deleteEventInput{EventID: eventID})
	return err
}

// --- Reset (purge) ---

// ResetCounts reports what a [Writer.Reset] removed so the operator-facing
// summary reflects what was actually purged.
type ResetCounts struct {
	Claims        int64
	Evidence      int64
	StatusHistory int64
	Relationships int64
	Embeddings    int64
	Events        int64
}

type resetInput struct{ KeepEvents bool }

type resetExecutor struct{ conn *store.Conn }

// Execute reads the pre-purge counts, then deletes all derived memory
// state through the port-typed DeleteAll methods. Order matters under FK
// enforcement: relationships and embeddings (which reference claims /
// events) go first, claims (including claim_evidence) next, events last.
func (e resetExecutor) Execute(ctx context.Context, input any, _ axidomain.CapabilityInvoker) (axidomain.ExecutionResult, []axidomain.EvidenceRecord, error) {
	in, ok := payload[resetInput](input)
	if !ok {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("reset: unexpected input %T", input)
	}
	conn := e.conn
	var counts ResetCounts
	var err error
	if counts.Claims, err = conn.Claims.CountAll(ctx); err != nil {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("count claims: %w", err)
	}
	evidence, err := conn.Claims.ListAllEvidence(ctx)
	if err != nil {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("list claim evidence: %w", err)
	}
	counts.Evidence = int64(len(evidence))
	history, err := conn.Claims.ListAllStatusHistory(ctx)
	if err != nil {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("list status history: %w", err)
	}
	counts.StatusHistory = int64(len(history))
	if counts.Relationships, err = conn.Relationships.CountAll(ctx); err != nil {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("count relationships: %w", err)
	}
	if counts.Embeddings, err = conn.Embeddings.CountAll(ctx); err != nil {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("count embeddings: %w", err)
	}
	if !in.KeepEvents {
		if counts.Events, err = conn.Events.CountAll(ctx); err != nil {
			return axidomain.ExecutionResult{}, nil, fmt.Errorf("count events: %w", err)
		}
	}

	if err := conn.Relationships.DeleteAll(ctx); err != nil {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("delete relationships: %w", err)
	}
	if err := conn.Embeddings.DeleteAll(ctx); err != nil {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("delete embeddings: %w", err)
	}
	if err := conn.Claims.DeleteAll(ctx); err != nil {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("delete claims: %w", err)
	}
	if !in.KeepEvents {
		if err := conn.Events.DeleteAll(ctx); err != nil {
			return axidomain.ExecutionResult{}, nil, fmt.Errorf("delete events: %w", err)
		}
	}
	return axidomain.ExecutionResult{
			Data:    counts,
			Summary: fmt.Sprintf("reset: purged %d claim(s), %d relationship(s), %d embedding(s)", counts.Claims, counts.Relationships, counts.Embeddings),
		}, ev("mnemos.write.reset", map[string]any{
			"claims": counts.Claims, "relationships": counts.Relationships,
			"embeddings": counts.Embeddings, "events": counts.Events,
			"keep_events": in.KeepEvents,
		}), nil
}

// Reset purges all derived memory state (claims, relationships,
// embeddings, and — unless keepEvents — events) as one governed, audited
// action. Returns the counts removed for the operator-facing summary.
func (w *Writer) Reset(ctx context.Context, keepEvents bool) (ResetCounts, error) {
	return dispatch[ResetCounts](ctx, w, actionReset, resetInput{KeepEvents: keepEvents})
}
