package main

import (
	"net/http"

	"go.klarlabs.de/mnemos"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
)

// makePredictiveErrorHandler serves GET /v1/predictive-error: the hierarchical
// prediction-error report (ADR 0017) from Memory.PredictiveError, the same
// call the predictive_error MCP tool and gRPC PredictiveError make. Read-only.
func makePredictiveErrorHandler(mem mnemos.Memory) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mem := scopedMem(r.Context(), mem)
		if !requireGET(w, r) || memUnavailable(w, mem) {
			return
		}
		pe, err := mem.PredictiveError(r.Context())
		if err != nil {
			writeInternalError(w, "predictive error", err)
			return
		}
		if pe.Levels == nil {
			pe.Levels = []mnemos.PredictiveErrorLevel{}
		}
		writeJSON(w, http.StatusOK, pe)
	}
}

// reconciliationDTO is one closed prediction on POST /v1/expectations/reconcile.
//
// A decision-tied expectation (ADR 0049) also names the decision, the beliefs
// its surprise is attributed to and what revision did to them; belief_id is
// then the expectation's store key.
type reconciliationDTO struct {
	BeliefID     string        `json:"belief_id"`
	Predicted    float64       `json:"predicted"`
	Observed     float64       `json:"observed"`
	Surprise     float64       `json:"surprise"`
	Confirmed    bool          `json:"confirmed"`
	DecisionID   string        `json:"decision_id,omitempty"`
	AttributedTo []string      `json:"attributed_to,omitempty"`
	Revisions    []revisionDTO `json:"revisions,omitempty"`
	Rechecked    bool          `json:"rechecked,omitempty"`
}

// revisionDTO is one surprise-triggered revision of a belief.
type revisionDTO struct {
	ClaimID string `json:"claim_id"`
	Kind    string `json:"kind"`
	By      string `json:"by,omitempty"`
}

func reconciliationsToDTO(recs []govwrite.Reconciliation) []reconciliationDTO {
	out := make([]reconciliationDTO, 0, len(recs))
	for _, r := range recs {
		dto := reconciliationDTO{
			BeliefID: r.ClaimID, Predicted: r.Predicted, Observed: r.Observed, Surprise: r.Surprise, Confirmed: r.Confirmed,
			DecisionID: r.DecisionID, AttributedTo: r.AttributedTo, Rechecked: r.Rechecked,
		}
		for _, rv := range r.Revisions {
			dto.Revisions = append(dto.Revisions, revisionDTO{ClaimID: rv.ClaimID, Kind: rv.Kind, By: rv.By})
		}
		out = append(out, dto)
	}
	return out
}

// makeReconcileExpectationsHandler serves POST /v1/expectations/reconcile:
// close every observed-but-open expectation into a validates/refutes verdict
// through the governed writer (govwrite.Writer.ReconcileExpectations), the one
// implementation the Go library, gRPC and the reconcile_expectations MCP tool
// share. Requires claims:write. Reconciliation spans every run, so a
// run-scoped token is refused.
func makeReconcileExpectationsHandler(gw *govwrite.Writer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		gw := scopedWriter(r.Context(), gw)
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !requireScope(w, r, domain.ScopeClaimsWrite) {
			return
		}
		if len(allowedRunsFromContext(r.Context())) > 0 {
			writeError(w, http.StatusForbidden, "token is run-scoped: reconciliation spans every run")
			return
		}
		recs, err := gw.ReconcileExpectations(r.Context())
		if err != nil {
			writeInternalError(w, "reconcile expectations", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"reconciliations": reconciliationsToDTO(recs)})
	}
}
