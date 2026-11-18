package govwrite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	axidomain "go.klarlabs.de/axi/domain"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
)

// Structured expectations (forward predictions on a belief) and their
// resolution. This is the one implementation: the Go library (through its own
// governed kernel and the Apply* functions), REST, gRPC and the MCP tools
// (through [Writer]) all set, observe and reconcile here, so every surface
// validates, normalises, audits and reads back the same way.
//
// Request checks run BEFORE dispatch ([CheckExpectation], [CheckObservation])
// because the kernel flattens an executor's error into a string: a sentinel
// returned from inside an executor would reach the caller as an opaque
// failure, and a transport could no longer tell "no such belief" from a
// storage fault.

const (
	actionSetExpectation         = "set_expectation"
	actionObserveExpectation     = "observe_expectation"
	actionReconcileExpectations  = "reconcile_expectations"
	expectationVerdictEdgePrefix = "expverdict_"
	expectationSourcePrefix      = "exp_"
)

var (
	// ErrExpectationsUnsupported is returned when the backend has no expectation store.
	ErrExpectationsUnsupported = errors.New("structured expectations are not supported by this store")
	// ErrExpectationNotFound is returned when the belief carries no expectation to observe.
	ErrExpectationNotFound = errors.New("no expectation for belief")
	// ErrBeliefNotFound is returned when an expectation names a belief that does not exist.
	ErrBeliefNotFound = errors.New("no such belief")
	// ErrBeliefIDRequired is returned when the request names no belief.
	ErrBeliefIDRequired = errors.New("belief id is required")
)

// ExpectationRequest attaches (or replaces) a forward prediction on a belief.
type ExpectationRequest struct {
	ClaimID   string
	Predicted float64
	// Tolerance is the accepted |observed − predicted|; a negative value is
	// taken as its magnitude.
	Tolerance float64
	// Horizon is when the prediction should be judged; zero means none.
	Horizon time.Time
}

// Reconciliation is one expectation closed against its observation.
type Reconciliation struct {
	ClaimID   string
	Predicted float64
	Observed  float64
	Surprise  float64
	Confirmed bool
}

type observeExpectationInput struct {
	ClaimID  string
	Observed float64
}

// CheckExpectation validates req against conn and returns the expectation to
// store: the belief must exist, tolerance is normalised to its magnitude.
func CheckExpectation(ctx context.Context, conn *store.Conn, req ExpectationRequest, now time.Time) (domain.Expectation, error) {
	if conn.Expectations == nil {
		return domain.Expectation{}, ErrExpectationsUnsupported
	}
	id := strings.TrimSpace(req.ClaimID)
	if id == "" {
		return domain.Expectation{}, ErrBeliefIDRequired
	}
	found, err := conn.Claims.ListByIDs(ctx, []string{id})
	if err != nil {
		return domain.Expectation{}, fmt.Errorf("look up belief %s: %w", id, err)
	}
	if len(found) == 0 {
		return domain.Expectation{}, fmt.Errorf("%w: %s", ErrBeliefNotFound, id)
	}
	tol := req.Tolerance
	if tol < 0 {
		tol = -tol
	}
	return domain.Expectation{ClaimID: id, Predicted: req.Predicted, Tolerance: tol, Horizon: req.Horizon, CreatedAt: now}, nil
}

// CheckObservation reports whether claimID has an expectation to observe.
func CheckObservation(ctx context.Context, conn *store.Conn, claimID string) error {
	if conn.Expectations == nil {
		return ErrExpectationsUnsupported
	}
	id := strings.TrimSpace(claimID)
	if id == "" {
		return ErrBeliefIDRequired
	}
	_, ok, err := conn.Expectations.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("get expectation %s: %w", id, err)
	}
	if !ok {
		return fmt.Errorf("%w: %s", ErrExpectationNotFound, id)
	}
	return nil
}

// SetExpectation attaches (or replaces) a belief's expectation and returns it
// as stored.
func (w *Writer) SetExpectation(ctx context.Context, req ExpectationRequest) (domain.Expectation, error) {
	exp, err := CheckExpectation(ctx, w.conn, req, time.Now().UTC())
	if err != nil {
		return domain.Expectation{}, err
	}
	return dispatch[domain.Expectation](ctx, w, actionSetExpectation, exp)
}

// ObserveExpectation records the value that arrived for a belief's
// expectation and returns the expectation as stored.
func (w *Writer) ObserveExpectation(ctx context.Context, claimID string, observed float64) (domain.Expectation, error) {
	if err := CheckObservation(ctx, w.conn, claimID); err != nil {
		return domain.Expectation{}, err
	}
	return dispatch[domain.Expectation](ctx, w, actionObserveExpectation,
		observeExpectationInput{ClaimID: strings.TrimSpace(claimID), Observed: observed})
}

// ReconcileExpectations closes every observed-but-open expectation into a
// validates/refutes verdict. A backend with no expectation store has nothing
// to reconcile.
func (w *Writer) ReconcileExpectations(ctx context.Context) ([]Reconciliation, error) {
	if w.conn.Expectations == nil {
		return nil, nil
	}
	return dispatch[[]Reconciliation](ctx, w, actionReconcileExpectations, struct{}{})
}

// ApplySetExpectation stores a checked expectation directly against conn and
// reads it back. It exists for the root library, whose writes are governed by
// its own kernel; every other caller goes through [Writer.SetExpectation].
func ApplySetExpectation(ctx context.Context, conn *store.Conn, exp domain.Expectation) (domain.Expectation, error) {
	if err := conn.Expectations.Upsert(ctx, exp); err != nil {
		return domain.Expectation{}, fmt.Errorf("upsert expectation %s: %w", exp.ClaimID, err)
	}
	return readExpectation(ctx, conn, exp.ClaimID)
}

// ApplyObserveExpectation records an observation directly against conn; the
// library-kernel counterpart of [Writer.ObserveExpectation].
func ApplyObserveExpectation(ctx context.Context, conn *store.Conn, claimID string, observed float64) (domain.Expectation, error) {
	exp, ok, err := conn.Expectations.Get(ctx, claimID)
	if err != nil {
		return domain.Expectation{}, fmt.Errorf("get expectation %s: %w", claimID, err)
	}
	if !ok {
		return domain.Expectation{}, fmt.Errorf("%w: %s", ErrExpectationNotFound, claimID)
	}
	exp.Observed = observed
	exp.HasObservation = true
	if err := conn.Expectations.Upsert(ctx, exp); err != nil {
		return domain.Expectation{}, fmt.Errorf("upsert observation %s: %w", claimID, err)
	}
	return readExpectation(ctx, conn, claimID)
}

// ApplyReconcileExpectations closes observed expectations directly against
// conn; the library-kernel counterpart of [Writer.ReconcileExpectations].
// Each verdict is emitted as a validates/refutes edge, which the surprise
// routing (ForgetRefuted / ReinforceValidated) consumes, and the expectation
// is marked resolved. An unobserved expectation, even past its horizon, stays
// open: absence of evidence never refutes.
func ApplyReconcileExpectations(ctx context.Context, conn *store.Conn, now time.Time) ([]Reconciliation, error) {
	if conn.Expectations == nil {
		return nil, nil
	}
	open, err := conn.Expectations.ListOpen(ctx)
	if err != nil {
		return nil, fmt.Errorf("list open expectations: %w", err)
	}
	out := []Reconciliation{}
	for _, exp := range open {
		if !exp.HasObservation {
			continue
		}
		confirmed, _ := exp.Confirmed()
		surprise, _ := exp.Surprise()
		kind := domain.RelationshipTypeRefutes
		if confirmed {
			kind = domain.RelationshipTypeValidates
		}
		if conn.EntityRels != nil {
			edge := domain.EntityRelationship{
				ID: expectationVerdictEdgePrefix + exp.ClaimID, Kind: kind,
				FromID: expectationSourcePrefix + exp.ClaimID, FromType: domain.RelEntityOutcome,
				ToID: exp.ClaimID, ToType: domain.RelEntityClaim, CreatedAt: now,
			}
			if err := conn.EntityRels.Upsert(ctx, []domain.EntityRelationship{edge}); err != nil {
				return out, fmt.Errorf("emit verdict for %s: %w", exp.ClaimID, err)
			}
		}
		exp.Resolved = true
		if err := conn.Expectations.Upsert(ctx, exp); err != nil {
			return out, fmt.Errorf("mark resolved %s: %w", exp.ClaimID, err)
		}
		out = append(out, Reconciliation{
			ClaimID: exp.ClaimID, Predicted: exp.Predicted, Observed: exp.Observed, Surprise: surprise, Confirmed: confirmed,
		})
	}
	return out, nil
}

func readExpectation(ctx context.Context, conn *store.Conn, claimID string) (domain.Expectation, error) {
	stored, ok, err := conn.Expectations.Get(ctx, claimID)
	if err != nil {
		return domain.Expectation{}, fmt.Errorf("read back expectation %s: %w", claimID, err)
	}
	if !ok {
		return domain.Expectation{}, fmt.Errorf("expectation %s did not persist", claimID)
	}
	return stored, nil
}

type setExpectationExecutor struct{ conn *store.Conn }

// Execute stores a checked expectation and records the write.
func (e setExpectationExecutor) Execute(ctx context.Context, input any, _ axidomain.CapabilityInvoker) (axidomain.ExecutionResult, []axidomain.EvidenceRecord, error) {
	in, ok := payload[domain.Expectation](input)
	if !ok {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("set_expectation: unexpected input %T", input)
	}
	stored, err := ApplySetExpectation(ctx, e.conn, in)
	if err != nil {
		return axidomain.ExecutionResult{}, nil, err
	}
	return axidomain.ExecutionResult{Data: stored, Summary: "set expectation on " + stored.ClaimID},
		ev("mnemos.write.set_expectation", map[string]any{
			"claim_id": stored.ClaimID, "predicted": stored.Predicted, "tolerance": stored.Tolerance,
		}), nil
}

type observeExpectationExecutor struct{ conn *store.Conn }

// Execute records an observation and records the write.
func (e observeExpectationExecutor) Execute(ctx context.Context, input any, _ axidomain.CapabilityInvoker) (axidomain.ExecutionResult, []axidomain.EvidenceRecord, error) {
	in, ok := payload[observeExpectationInput](input)
	if !ok {
		return axidomain.ExecutionResult{}, nil, fmt.Errorf("observe_expectation: unexpected input %T", input)
	}
	stored, err := ApplyObserveExpectation(ctx, e.conn, in.ClaimID, in.Observed)
	if err != nil {
		return axidomain.ExecutionResult{}, nil, err
	}
	return axidomain.ExecutionResult{Data: stored, Summary: "observed expectation on " + stored.ClaimID},
		ev("mnemos.write.observe_expectation", map[string]any{
			"claim_id": stored.ClaimID, "predicted": stored.Predicted, "observed": stored.Observed,
		}), nil
}

type reconcileExpectationsExecutor struct{ conn *store.Conn }

// Execute closes observed expectations and records the verdict counts.
func (e reconcileExpectationsExecutor) Execute(ctx context.Context, _ any, _ axidomain.CapabilityInvoker) (axidomain.ExecutionResult, []axidomain.EvidenceRecord, error) {
	recs, err := ApplyReconcileExpectations(ctx, e.conn, time.Now().UTC())
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
		ev("mnemos.write.reconcile_expectations", map[string]any{
			"reconciled": len(recs), "confirmed": confirmed, "refuted": len(recs) - confirmed,
		}), nil
}
