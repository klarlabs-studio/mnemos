package mnemos

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"go.klarlabs.de/mnemos/internal/domain"
)

// ErrActionsUnsupported is returned by the action-recording methods on a backend
// without the action/outcome layer.
var ErrActionsUnsupported = errors.New("mnemos: action recording not supported on this backend")

// ActionItem is an operational action an agent took — the raw material the skill
// layer (Synthesize) clusters, by (subject, kind), into lessons and playbooks.
type ActionItem struct {
	// ID is the stable identifier; generated when empty.
	ID string
	// Kind is the operation — "deploy", "rollback", "post", "investigate", …
	Kind string
	// Subject is the operational target — a service, component, or channel. Same
	// subject + kind across actions is what forms a corroborating cluster.
	Subject string
	// Actor is the agent/worker that took the action.
	Actor string
	// At is when it happened; defaults to now.
	At time.Time
	// Metadata carries opaque context (commit sha, PR number, …).
	Metadata map[string]string
}

// OutcomeItem is the observed result of a recorded action.
type OutcomeItem struct {
	// ID is the stable identifier; generated when empty.
	ID string
	// ActionID is the action this is the outcome of. Required.
	ActionID string
	// Result is "success", "failure", "partial", or "unknown" (the default).
	Result string
	// Metrics / Notes carry the observed detail.
	Metrics map[string]float64
	Notes   string
	// At is when it was observed; defaults to now.
	At time.Time
}

// RecordAction implements [Memory.RecordAction].
func (m *memory) RecordAction(ctx context.Context, item ActionItem) (string, error) {
	if m.conn.Actions == nil {
		return "", ErrActionsUnsupported
	}
	if strings.TrimSpace(item.Kind) == "" || strings.TrimSpace(item.Subject) == "" {
		return "", errors.New("mnemos: RecordAction: kind and subject are required")
	}
	id := item.ID
	if id == "" {
		id = "act_" + uuid.NewString()
	}
	at := item.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	if err := m.conn.Actions.Append(ctx, domain.Action{
		ID:        id,
		Kind:      domain.ActionKind(item.Kind),
		Subject:   item.Subject,
		Actor:     item.Actor,
		At:        at,
		Metadata:  item.Metadata,
		CreatedBy: item.Actor,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		return "", fmt.Errorf("mnemos: RecordAction: %w", err)
	}
	return id, nil
}

// RecordActionOutcome implements [Memory.RecordActionOutcome].
func (m *memory) RecordActionOutcome(ctx context.Context, item OutcomeItem) error {
	if m.conn.Outcomes == nil {
		return ErrActionsUnsupported
	}
	if strings.TrimSpace(item.ActionID) == "" {
		return errors.New("mnemos: RecordActionOutcome: ActionID is required")
	}
	id := item.ID
	if id == "" {
		id = "out_" + uuid.NewString()
	}
	at := item.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	result := domain.OutcomeResult(item.Result)
	if result == "" {
		result = domain.OutcomeResultUnknown
	}
	return m.conn.Outcomes.Append(ctx, domain.Outcome{
		ID:         id,
		ActionID:   item.ActionID,
		Result:     result,
		Metrics:    item.Metrics,
		Notes:      item.Notes,
		ObservedAt: at,
		Source:     "push",
		CreatedAt:  time.Now().UTC(),
	})
}
