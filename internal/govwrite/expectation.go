package govwrite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	axidomain "go.klarlabs.de/axi/domain"

	"go.klarlabs.de/mnemos/internal/clock"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
)

// Structured expectations (forward predictions on a belief, or on the outcome
// of a decision) and their resolution. This is the one implementation: the Go
// library (through its own governed kernel and the Apply* functions), REST,
// gRPC and the MCP tools (through [Writer]) all set, observe and reconcile
// here, so every surface validates, normalises, audits and reads back the
// same way.
//
// Request checks run BEFORE dispatch ([CheckExpectation], [CheckObservation],
// [CheckDecisionExpectation], [CheckDecisionObservation]) because the kernel
// flattens an executor's error into a string: a sentinel returned from inside
// an executor would reach the caller as an opaque failure, and a transport
// could no longer tell "no such belief" from a storage fault.

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
	// ErrReservedExpectationKey is returned when a belief id starts with the
	// prefix that namespaces decision-tied expectations (ADR 0049).
	ErrReservedExpectationKey = errors.New("belief id uses the reserved decision-expectation prefix")
	// ErrDecisionIDRequired is returned when a decision expectation names no decision.
	ErrDecisionIDRequired = errors.New("decision id is required")
	// ErrExpectationRestsOnNothing is returned when a decision expectation
	// names no belief to rest on: its surprise could be attributed to nothing.
	ErrExpectationRestsOnNothing = errors.New("a decision expectation must rest on at least one belief")
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

// DecisionExpectationRequest records the outcome an agent expects from a
// decision, resting on the beliefs it acted on (ADR 0049).
type DecisionExpectationRequest struct {
	DecisionID string
	// RestsOn are the claim ids the decision rested on: its Beliefs.
	RestsOn   []string
	Predicted float64
	// Tolerance is the accepted |observed − predicted|; a negative value is
	// taken as its magnitude.
	Tolerance float64
	// Horizon is when the outcome should be judged; zero means none.
	Horizon time.Time
}

// Reconciliation is one expectation closed against its observation.
type Reconciliation struct {
	// ClaimID is the expectation's key: the belief it was set on, or the
	// decision key for a decision-tied expectation.
	ClaimID   string
	Predicted float64
	Observed  float64
	Surprise  float64
	Confirmed bool
	// DecisionID is set for a decision-tied expectation (ADR 0049).
	DecisionID string
	// AttributedTo are the beliefs the decision rested on: where its surprise
	// is attributed. Empty for an expectation on a single belief.
	AttributedTo []string
	// Revisions are what the surprise did to those beliefs in this pass.
	Revisions []Revision
	// Rechecked marks an expectation reconciled in an EARLIER pass whose
	// surprise was revised again in this one, because evidence arrived since.
	Rechecked bool
}

// Revision is one change surprise-triggered revision made to a belief.
type Revision struct {
	ClaimID string
	// Kind is "flag" (active → contested), "supersede" (valid time ended by
	// a newer contradicting belief) or "clear" (a surprise flag lifted because
	// another belief explained the surprise).
	Kind string
	// By is the superseding belief, for "supersede".
	By string
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
	if domain.IsDecisionExpectationKey(id) {
		return domain.Expectation{}, fmt.Errorf("%w: %s", ErrReservedExpectationKey, id)
	}
	found, err := conn.Claims.ListByIDs(ctx, []string{id})
	if err != nil {
		return domain.Expectation{}, fmt.Errorf("look up belief %s: %w", id, err)
	}
	if len(found) == 0 {
		return domain.Expectation{}, fmt.Errorf("%w: %s", ErrBeliefNotFound, id)
	}
	return domain.Expectation{ClaimID: id, Predicted: req.Predicted, Tolerance: magnitude(req.Tolerance), Horizon: req.Horizon, CreatedAt: now}, nil
}

// CheckDecisionExpectation validates a decision expectation and returns the
// expectation to store under the decision's key. Every rested-on belief must
// exist: a surprise is attributed to them, so an unknown id would silently
// absorb blame that belongs elsewhere.
func CheckDecisionExpectation(ctx context.Context, conn *store.Conn, req DecisionExpectationRequest, now time.Time) (domain.Expectation, error) {
	if conn.Expectations == nil {
		return domain.Expectation{}, ErrExpectationsUnsupported
	}
	id := strings.TrimSpace(req.DecisionID)
	if id == "" {
		return domain.Expectation{}, ErrDecisionIDRequired
	}
	restsOn := nonBlankIDs(req.RestsOn)
	if len(restsOn) == 0 {
		return domain.Expectation{}, ErrExpectationRestsOnNothing
	}
	found, err := conn.Claims.ListByIDs(ctx, restsOn)
	if err != nil {
		return domain.Expectation{}, fmt.Errorf("look up rested-on beliefs: %w", err)
	}
	have := make(map[string]bool, len(found))
	for _, c := range found {
		have[c.ID] = true
	}
	for _, b := range restsOn {
		if !have[b] {
			return domain.Expectation{}, fmt.Errorf("%w: %s", ErrBeliefNotFound, b)
		}
	}
	return domain.Expectation{
		ClaimID: domain.DecisionExpectationKey(id), Predicted: req.Predicted,
		Tolerance: magnitude(req.Tolerance), Horizon: req.Horizon, CreatedAt: now,
	}, nil
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
	if domain.IsDecisionExpectationKey(id) {
		return fmt.Errorf("%w: %s", ErrReservedExpectationKey, id)
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

// CheckDecisionObservation reports whether decisionID has an expectation to
// observe and returns its store key.
func CheckDecisionObservation(ctx context.Context, conn *store.Conn, decisionID string) (string, error) {
	if conn.Expectations == nil {
		return "", ErrExpectationsUnsupported
	}
	id := strings.TrimSpace(decisionID)
	if id == "" {
		return "", ErrDecisionIDRequired
	}
	key := domain.DecisionExpectationKey(id)
	_, ok, err := conn.Expectations.Get(ctx, key)
	if err != nil {
		return "", fmt.Errorf("get expectation for decision %s: %w", id, err)
	}
	if !ok {
		return "", fmt.Errorf("%w: decision %s", ErrExpectationNotFound, id)
	}
	return key, nil
}

// SetExpectation attaches (or replaces) a belief's expectation and returns it
// as stored.
func (w *Writer) SetExpectation(ctx context.Context, req ExpectationRequest) (domain.Expectation, error) {
	exp, err := CheckExpectation(ctx, w.conn, req, clock.Now().UTC())
	if err != nil {
		return domain.Expectation{}, err
	}
	return dispatch[domain.Expectation](ctx, w, actionSetExpectation, exp)
}

// SetDecisionExpectation stores the outcome expected from a decision (ADR
// 0049) and returns it as stored: the set_expectation governed write, under
// the decision's key.
func (w *Writer) SetDecisionExpectation(ctx context.Context, req DecisionExpectationRequest) (domain.Expectation, error) {
	exp, err := CheckDecisionExpectation(ctx, w.conn, req, clock.Now().UTC())
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

// ObserveDecision records the outcome value observed for a decision's
// expectation and returns the expectation as stored.
func (w *Writer) ObserveDecision(ctx context.Context, decisionID string, observed float64) (domain.Expectation, error) {
	key, err := CheckDecisionObservation(ctx, w.conn, decisionID)
	if err != nil {
		return domain.Expectation{}, err
	}
	return dispatch[domain.Expectation](ctx, w, actionObserveExpectation,
		observeExpectationInput{ClaimID: key, Observed: observed})
}

// ReconcileExpectations closes every observed-but-open expectation into a
// validates/refutes verdict and runs surprise-triggered revision (ADR 0049).
// A backend with no expectation store has nothing to reconcile.
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
//
// An expectation on a belief is emitted as a validates/refutes edge to that
// belief, which the surprise routing (ForgetRefuted / ReinforceValidated)
// consumes. A decision-tied expectation (ADR 0049) is emitted as an edge to
// the DECISION, never to its beliefs: its surprise is evidence against the
// conjunction of the beliefs, not against each one, so it must not reach the
// per-belief verdict consumers (calibration, ForgetRefuted). Its surprise is
// attributed to the beliefs through credit assignment and revised through
// [reviseSurprised], which runs after every pass. actor is recorded on the
// status and validity changes revision makes.
//
// An unobserved expectation, even past its horizon, stays open: absence of
// evidence never refutes.
func ApplyReconcileExpectations(ctx context.Context, conn *store.Conn, now time.Time, actor string) ([]Reconciliation, error) {
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
		rec, err := closeExpectation(ctx, conn, exp, now)
		if err != nil {
			return out, err
		}
		out = append(out, rec)
	}
	return reviseSurprised(ctx, conn, out, now, actor)
}

// closeExpectation emits one observed expectation's verdict edge and marks it
// resolved.
func closeExpectation(ctx context.Context, conn *store.Conn, exp domain.Expectation, now time.Time) (Reconciliation, error) {
	confirmed, _ := exp.Confirmed()
	surprise, _ := exp.Surprise()
	rec := Reconciliation{
		ClaimID: exp.ClaimID, Predicted: exp.Predicted, Observed: exp.Observed, Surprise: surprise, Confirmed: confirmed,
	}
	kind := domain.RelationshipTypeRefutes
	if confirmed {
		kind = domain.RelationshipTypeValidates
	}
	toID, toType := exp.ClaimID, domain.RelEntityClaim
	if decisionID, ok := exp.DecisionID(); ok {
		rec.DecisionID = decisionID
		toID, toType = decisionID, domain.RelEntityDecision
		if conn.Decisions != nil {
			beliefs, err := conn.Decisions.ListBeliefs(ctx, decisionID)
			if err != nil {
				return rec, fmt.Errorf("list beliefs of decision %s: %w", decisionID, err)
			}
			rec.AttributedTo = beliefs
		}
	}
	if conn.EntityRels != nil {
		edge := domain.EntityRelationship{
			ID: expectationVerdictEdgePrefix + exp.ClaimID, Kind: kind,
			FromID: expectationSourcePrefix + exp.ClaimID, FromType: domain.RelEntityOutcome,
			ToID: toID, ToType: toType, CreatedAt: now,
		}
		if err := conn.EntityRels.Upsert(ctx, []domain.EntityRelationship{edge}); err != nil {
			return rec, fmt.Errorf("emit verdict for %s: %w", exp.ClaimID, err)
		}
	}
	exp.Resolved = true
	if err := conn.Expectations.Upsert(ctx, exp); err != nil {
		return rec, fmt.Errorf("mark resolved %s: %w", exp.ClaimID, err)
	}
	return rec, nil
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

func magnitude(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func nonBlankIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
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

// Execute closes observed expectations, revises what their surprise rested
// on, and records the verdict and revision counts.
func (e reconcileExpectationsExecutor) Execute(ctx context.Context, _ any, _ axidomain.CapabilityInvoker) (axidomain.ExecutionResult, []axidomain.EvidenceRecord, error) {
	recs, err := ApplyReconcileExpectations(ctx, e.conn, clock.Now().UTC(), domain.SystemUser)
	if err != nil {
		return axidomain.ExecutionResult{}, nil, err
	}
	sum := SummarizeReconciliations(recs)
	return axidomain.ExecutionResult{Data: recs, Summary: sum.String()},
		ev("mnemos.write.reconcile_expectations", sum.Evidence()), nil
}

// ReconcileSummary counts what one reconcile pass did, for the audit trail.
type ReconcileSummary struct {
	Reconciled, Confirmed, Refuted int
	Flagged, Superseded, Cleared   int
}

// SummarizeReconciliations counts verdicts and revisions. A rechecked
// expectation was reconciled earlier, so it counts only its revisions.
func SummarizeReconciliations(recs []Reconciliation) ReconcileSummary {
	var s ReconcileSummary
	for _, r := range recs {
		if !r.Rechecked {
			s.Reconciled++
			if r.Confirmed {
				s.Confirmed++
			} else {
				s.Refuted++
			}
		}
		for _, rv := range r.Revisions {
			switch rv.Kind {
			case RevisionFlag:
				s.Flagged++
			case RevisionSupersede:
				s.Superseded++
			case RevisionClear:
				s.Cleared++
			}
		}
	}
	return s
}

// String is the session summary line.
func (s ReconcileSummary) String() string {
	return fmt.Sprintf("reconciled %d expectation(s), %d confirmed; revised: %d flagged, %d superseded, %d cleared",
		s.Reconciled, s.Confirmed, s.Flagged, s.Superseded, s.Cleared)
}

// Evidence is the evidence-record value.
func (s ReconcileSummary) Evidence() map[string]any {
	return map[string]any{
		"reconciled": s.Reconciled, "confirmed": s.Confirmed, "refuted": s.Refuted,
		"flagged": s.Flagged, "superseded": s.Superseded, "cleared": s.Cleared,
	}
}
