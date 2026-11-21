package mnemos

import (
	"context"
	"fmt"
	"time"

	axidomain "go.klarlabs.de/axi/domain"

	"go.klarlabs.de/mnemos/internal/clock"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
)

// Structured expectations on the library surface. The request checks and the
// writes are govwrite's (CheckExpectation / CheckObservation and the Apply*
// functions), the one implementation REST, gRPC and MCP use through
// govwrite.Writer; here they run inside the library's own governed kernel so
// each write records a session on the evidence chain like every other public
// write.

// Write action names for the expectation loop (see writeActions).
const (
	actionSetExpectation        = "set_expectation"
	actionObserveExpectation    = "observe_expectation"
	actionReconcileExpectations = "reconcile_expectations"
)

type observeExpectationInput struct {
	ClaimID  string
	Observed float64
}

// Expect implements [Memory.Expect].
func (m *memory) Expect(ctx context.Context, claimID string, predicted, tolerance float64, horizon time.Time) error {
	exp, err := govwrite.CheckExpectation(ctx, m.conn, govwrite.ExpectationRequest{
		ClaimID: claimID, Predicted: predicted, Tolerance: tolerance, Horizon: horizon,
	}, clock.Now().UTC())
	if err != nil {
		return fmt.Errorf("mnemos: Expect: %w", err)
	}
	if _, err := dispatchWrite[domain.Expectation](ctx, m, actionSetExpectation, exp); err != nil {
		return fmt.Errorf("mnemos: Expect: %w", err)
	}
	return nil
}

// RecordObservation implements [Memory.RecordObservation].
func (m *memory) RecordObservation(ctx context.Context, claimID string, observed float64) error {
	if err := govwrite.CheckObservation(ctx, m.conn, claimID); err != nil {
		return fmt.Errorf("mnemos: RecordObservation: %w", err)
	}
	in := observeExpectationInput{ClaimID: claimID, Observed: observed}
	if _, err := dispatchWrite[domain.Expectation](ctx, m, actionObserveExpectation, in); err != nil {
		return fmt.Errorf("mnemos: RecordObservation: %w", err)
	}
	return nil
}

// ReconcileExpectations implements [Memory.ReconcileExpectations]: close every
// observed-but-open expectation into a verdict + surprise, emitting the
// validates/refutes edge that the surprise routing consumes.
func (m *memory) ReconcileExpectations(ctx context.Context) ([]Reconciliation, error) {
	if m.conn.Expectations == nil {
		return nil, nil
	}
	recs, err := dispatchWrite[[]govwrite.Reconciliation](ctx, m, actionReconcileExpectations, struct{}{})
	if err != nil {
		return nil, fmt.Errorf("mnemos: ReconcileExpectations: %w", err)
	}
	var out []Reconciliation
	for _, r := range recs {
		out = append(out, Reconciliation{
			ClaimID: r.ClaimID, Predicted: r.Predicted, Observed: r.Observed, Surprise: r.Surprise, Confirmed: r.Confirmed,
		})
	}
	return out, nil
}

type setExpectationExecutor struct{ m *memory }

// Execute stores a checked expectation and records the write.
func (e setExpectationExecutor) Execute(ctx context.Context, input any, _ axidomain.CapabilityInvoker) (axidomain.ExecutionResult, []axidomain.EvidenceRecord, error) {
	in, ok := writeInput[domain.Expectation](input)
	if !ok {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("set_expectation: unexpected input type %T", input)
	}
	stored, err := govwrite.ApplySetExpectation(ctx, e.m.conn, in)
	if err != nil {
		return axidomain.ExecutionResult{}, nil, err
	}
	return axidomain.ExecutionResult{Data: stored, Summary: "set expectation on " + stored.ClaimID},
		[]axidomain.EvidenceRecord{{Kind: "mnemos.set_expectation", Source: evidenceSourceLibrary, Value: map[string]any{
			"claim_id": stored.ClaimID, "predicted": stored.Predicted, "tolerance": stored.Tolerance,
		}}}, nil
}

type observeExpectationExecutor struct{ m *memory }

// Execute records an observation and records the write.
func (e observeExpectationExecutor) Execute(ctx context.Context, input any, _ axidomain.CapabilityInvoker) (axidomain.ExecutionResult, []axidomain.EvidenceRecord, error) {
	in, ok := writeInput[observeExpectationInput](input)
	if !ok {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("observe_expectation: unexpected input type %T", input)
	}
	stored, err := govwrite.ApplyObserveExpectation(ctx, e.m.conn, in.ClaimID, in.Observed)
	if err != nil {
		return axidomain.ExecutionResult{}, nil, err
	}
	return axidomain.ExecutionResult{Data: stored, Summary: "observed expectation on " + stored.ClaimID},
		[]axidomain.EvidenceRecord{{Kind: "mnemos.observe_expectation", Source: evidenceSourceLibrary, Value: map[string]any{
			"claim_id": stored.ClaimID, "predicted": stored.Predicted, "observed": stored.Observed,
		}}}, nil
}

type reconcileExpectationsExecutor struct{ m *memory }

// Execute closes observed expectations and records the verdict counts.
func (e reconcileExpectationsExecutor) Execute(ctx context.Context, _ any, _ axidomain.CapabilityInvoker) (axidomain.ExecutionResult, []axidomain.EvidenceRecord, error) {
	recs, err := govwrite.ApplyReconcileExpectations(ctx, e.m.conn, clock.Now().UTC())
	if err != nil {
		return axidomain.ExecutionResult{}, nil, err
	}
	confirmed := 0
	for _, r := range recs {
		if r.Confirmed {
			confirmed++
		}
	}
	return axidomain.ExecutionResult{Data: recs, Summary: fmt.Sprintf("reconciled %d expectation(s), %d confirmed", len(recs), confirmed)},
		[]axidomain.EvidenceRecord{{Kind: "mnemos.reconcile_expectations", Source: evidenceSourceLibrary, Value: map[string]any{
			"reconciled": len(recs), "confirmed": confirmed, "refuted": len(recs) - confirmed,
		}}}, nil
}
