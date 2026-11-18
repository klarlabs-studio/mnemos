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
type reconciliationDTO struct {
	BeliefID  string  `json:"belief_id"`
	Predicted float64 `json:"predicted"`
	Observed  float64 `json:"observed"`
	Surprise  float64 `json:"surprise"`
	Confirmed bool    `json:"confirmed"`
}

func reconciliationsToDTO(recs []govwrite.Reconciliation) []reconciliationDTO {
	out := make([]reconciliationDTO, 0, len(recs))
	for _, r := range recs {
		out = append(out, reconciliationDTO{r.ClaimID, r.Predicted, r.Observed, r.Surprise, r.Confirmed})
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
