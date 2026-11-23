package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/runscope"
)

// Decision-tied expectations (ADR 0049) on the daemon transports. The checks
// and writes are govwrite's (CheckDecisionExpectation, SetDecisionExpectation,
// ObserveDecision), the implementation the Go library, gRPC, REST and MCP
// share.
//
// Run scope: a decision expectation's surprise is attributed to every belief
// the decision rested on, so a run-scoped token may set or observe one only
// when EVERY rested-on belief lies in its runs (runscope.ClaimAllowed), the
// rule expectation.set applies to a single belief. Any other reads as "no such
// belief", so the answer never confirms another run's belief exists.

// decisionExpectationInput is the wire form of an expected outcome, shared by
// the record_decision MCP input and the POST /v1/decisions body.
type decisionExpectationInput struct {
	Predicted float64 `json:"predicted" jsonschema:"required,description=The outcome value the decision is expected to produce"`
	Tolerance float64 `json:"tolerance,omitempty" jsonschema:"description=Accepted |observed - predicted|; 0 confirms only an exact match"`
	Horizon   string  `json:"horizon,omitempty" jsonschema:"description=When the outcome should be judged (YYYY-MM-DD or RFC3339)"`
}

// requestAllowedRuns is the run allowlist of the request's token on either
// transport: MCP stashes the token's claims, REST the run list. nil means
// unrestricted.
func requestAllowedRuns(ctx context.Context) []string {
	if c, ok := claimsFromContext(ctx); ok && len(c.Runs) > 0 {
		return c.Runs
	}
	return allowedRunsFromContext(ctx)
}

// requireRestedOnInRuns refuses, as "no such belief" (govwrite.ErrBeliefNotFound),
// a run-scoped request naming a rested-on belief outside its runs.
func requireRestedOnInRuns(ctx context.Context, w *govwrite.Writer, beliefs []string) error {
	allowed := requestAllowedRuns(ctx)
	if len(allowed) == 0 {
		return nil
	}
	for _, b := range beliefs {
		ok, err := runscope.ClaimAllowed(ctx, w.Conn(), b, allowed)
		if err != nil {
			return fmt.Errorf("check run scope: %w", err)
		}
		if !ok {
			return fmt.Errorf("%w: %s", govwrite.ErrBeliefNotFound, b)
		}
	}
	return nil
}

// checkDecisionExpectationInput validates an optional expected outcome for d
// before d is written and returns the request to store after it, or nil when
// there is none. Every failure is a caller mistake.
func checkDecisionExpectationInput(ctx context.Context, w *govwrite.Writer, d domain.Decision, in *decisionExpectationInput) (*govwrite.DecisionExpectationRequest, error) {
	if in == nil {
		return nil, nil
	}
	var horizon time.Time
	if strings.TrimSpace(in.Horizon) != "" {
		t, err := parseInstant(in.Horizon)
		if err != nil {
			return nil, userInputError(fmt.Errorf("expectation.horizon: %w", err))
		}
		horizon = t
	}
	req := govwrite.DecisionExpectationRequest{
		DecisionID: d.ID, RestsOn: d.Beliefs, Predicted: in.Predicted, Tolerance: in.Tolerance, Horizon: horizon,
	}
	if err := requireRestedOnInRuns(ctx, w, d.Beliefs); err != nil {
		return nil, mcpExpectationError(err)
	}
	if _, err := govwrite.CheckDecisionExpectation(ctx, w.Conn(), req, time.Now().UTC()); err != nil {
		return nil, mcpExpectationError(fmt.Errorf("expectation: %w", err))
	}
	return &req, nil
}

// observeDecision records the observed outcome of a decision's expectation,
// confined to the token's runs like recording it. Errors keep govwrite's
// sentinels; each transport maps them.
func observeDecision(ctx context.Context, w *govwrite.Writer, decisionID string, observed float64) (domain.Expectation, error) {
	decisionID = strings.TrimSpace(decisionID)
	if len(requestAllowedRuns(ctx)) > 0 && w.Conn().Decisions != nil {
		beliefs, err := w.Conn().Decisions.ListBeliefs(ctx, decisionID)
		if err != nil {
			return domain.Expectation{}, fmt.Errorf("list decision beliefs: %w", err)
		}
		if len(beliefs) == 0 {
			// Nothing to confine against: no rested-on belief means no
			// expectation could have been recorded, and a run-scoped token
			// learns nothing more from this than from a 404.
			return domain.Expectation{}, fmt.Errorf("%w: decision %s", govwrite.ErrExpectationNotFound, decisionID)
		}
		if err := requireRestedOnInRuns(ctx, w, beliefs); err != nil {
			return domain.Expectation{}, err
		}
	}
	return w.ObserveDecision(ctx, decisionID, observed)
}

// makeDecisionObservationHandler serves POST /v1/decisions/<id>/observation:
// record the observed outcome of a decision recorded with an expectation.
func makeDecisionObservationHandler(gw *govwrite.Writer, decisionID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		gw := scopedWriter(r.Context(), gw)
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !requireScope(w, r, domain.ScopeClaimsWrite) {
			return
		}
		if gw == nil {
			writeError(w, http.StatusServiceUnavailable, "writer unavailable")
			return
		}
		var req struct {
			Observed *float64 `json:"observed"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "decode body: "+err.Error())
			return
		}
		if req.Observed == nil {
			writeError(w, http.StatusBadRequest, "observed is required")
			return
		}
		exp, err := observeDecision(r.Context(), gw, decisionID, *req.Observed)
		if err != nil {
			writeDecisionExpectationError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, decisionExpectationToDTO(exp))
	}
}

// decisionExpectationDTO is a decision's expectation on the wire.
type decisionExpectationDTO struct {
	DecisionID string `json:"decision_id"`
	expectationDTO
}

func decisionExpectationToDTO(e domain.Expectation) decisionExpectationDTO {
	id, _ := e.DecisionID()
	return decisionExpectationDTO{DecisionID: id, expectationDTO: expectationToDTO(e)}
}

// writeDecisionExpectationError maps govwrite's sentinels to statuses.
func writeDecisionExpectationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, govwrite.ErrExpectationsUnsupported):
		writeError(w, http.StatusNotImplemented, "expectations not supported by this store")
	case errors.Is(err, govwrite.ErrExpectationNotFound):
		writeError(w, http.StatusNotFound, "no expectation for decision")
	case errors.Is(err, govwrite.ErrBeliefNotFound):
		writeError(w, http.StatusNotFound, "belief not found")
	case errors.Is(err, govwrite.ErrDecisionIDRequired):
		writeError(w, http.StatusBadRequest, "decision id is required")
	default:
		writeInternalError(w, "record decision observation", err)
	}
}
