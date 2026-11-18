package main

import (
	"errors"
	"net/http"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/runscope"
	"go.klarlabs.de/mnemos/internal/store"
)

// expectationDTO is the wire form of a structured forward expectation attached
// to a claim (mnemos T1.1 over HTTP). The library has had Expect /
// RecordObservation / ReconcileExpectations; these endpoints expose the
// predict + observe half to HTTP-service consumers and read them back so a
// consumer can surface the surprise. Reconciliation into validates/refutes
// verdicts runs in the consolidate pass or on demand through
// POST /v1/expectations/reconcile (serve_prediction.go).
type expectationDTO struct {
	ClaimID        string  `json:"claim_id"`
	Predicted      float64 `json:"predicted"`
	Tolerance      float64 `json:"tolerance"`
	Horizon        string  `json:"horizon,omitempty"` // RFC3339
	Observed       float64 `json:"observed,omitempty"`
	HasObservation bool    `json:"has_observation"`
	Resolved       bool    `json:"resolved"`
	CreatedAt      string  `json:"created_at,omitempty"`
}

func expectationToDTO(e domain.Expectation) expectationDTO {
	dto := expectationDTO{
		ClaimID:        e.ClaimID,
		Predicted:      e.Predicted,
		Tolerance:      e.Tolerance,
		Observed:       e.Observed,
		HasObservation: e.HasObservation,
		Resolved:       e.Resolved,
	}
	if !e.Horizon.IsZero() {
		dto.Horizon = e.Horizon.UTC().Format(time.RFC3339)
	}
	if !e.CreatedAt.IsZero() {
		dto.CreatedAt = e.CreatedAt.UTC().Format(time.RFC3339)
	}
	return dto
}

// makeExpectationHandler serves /v1/beliefs/<id>/expectation:
//
//	GET  → the claim's expectation (404 when none)
//	POST → attach/replace the expectation {predicted, tolerance, horizon}
//
// Writes go through the governed writer (govwrite.Writer.SetExpectation), the
// implementation gRPC, MCP and the Go library share. A run-scoped token may
// touch only beliefs whose evidence lies in its runs; any other is a 404.
func makeExpectationHandler(conn *store.Conn, gw *govwrite.Writer, claimID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn := scopedConn(r.Context(), conn)
		gw := scopedWriter(r.Context(), gw)
		if conn.Expectations == nil {
			writeError(w, http.StatusNotImplemented, "expectations not supported by this store")
			return
		}
		switch r.Method {
		case http.MethodGet:
			if !requireBeliefInRuns(w, r, conn, claimID) {
				return
			}
			exp, ok, err := conn.Expectations.Get(r.Context(), claimID)
			if err != nil {
				writeInternalError(w, "get expectation", err)
				return
			}
			if !ok {
				writeError(w, http.StatusNotFound, "no expectation for claim")
				return
			}
			writeJSON(w, http.StatusOK, expectationToDTO(exp))
		case http.MethodPost:
			if !requireScope(w, r, domain.ScopeClaimsWrite) || !requireBeliefInRuns(w, r, conn, claimID) {
				return
			}
			var req struct {
				Predicted float64 `json:"predicted"`
				Tolerance float64 `json:"tolerance"`
				Horizon   string  `json:"horizon"`
			}
			if err := decodeJSON(r, &req); err != nil {
				writeError(w, http.StatusBadRequest, "decode body: "+err.Error())
				return
			}
			var horizon time.Time
			if req.Horizon != "" {
				t, err := parseTimeFlexible(req.Horizon)
				if err != nil {
					writeError(w, http.StatusBadRequest, "horizon: "+err.Error())
					return
				}
				horizon = t
			}
			exp, err := gw.SetExpectation(r.Context(), govwrite.ExpectationRequest{
				ClaimID: claimID, Predicted: req.Predicted, Tolerance: req.Tolerance, Horizon: horizon,
			})
			if err != nil {
				writeExpectationError(w, "set expectation", err)
				return
			}
			writeJSON(w, http.StatusCreated, expectationToDTO(exp))
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

// makeObservationHandler serves POST /v1/beliefs/<id>/observation: record the
// observed value against an existing expectation through the governed writer.
func makeObservationHandler(gw *govwrite.Writer, claimID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		gw := scopedWriter(r.Context(), gw)
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !requireScope(w, r, domain.ScopeClaimsWrite) || !requireBeliefInRuns(w, r, gw.Conn(), claimID) {
			return
		}
		var req struct {
			Observed float64 `json:"observed"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "decode body: "+err.Error())
			return
		}
		exp, err := gw.ObserveExpectation(r.Context(), claimID, req.Observed)
		if err != nil {
			writeExpectationError(w, "record observation", err)
			return
		}
		writeJSON(w, http.StatusOK, expectationToDTO(exp))
	}
}

// requireBeliefInRuns writes a 404 and reports false when a run-scoped token
// may not touch claimID (runscope.ClaimAllowed): out of scope reads exactly
// like absent, so the answer never confirms another run's belief exists.
func requireBeliefInRuns(w http.ResponseWriter, r *http.Request, conn *store.Conn, claimID string) bool {
	ok, err := runscope.ClaimAllowed(r.Context(), conn, claimID, allowedRunsFromContext(r.Context()))
	if err != nil {
		writeInternalError(w, "check run scope", err)
		return false
	}
	if !ok {
		writeError(w, http.StatusNotFound, "belief not found")
		return false
	}
	return true
}

// writeExpectationError maps govwrite's expectation sentinels to statuses.
func writeExpectationError(w http.ResponseWriter, label string, err error) {
	switch {
	case errors.Is(err, govwrite.ErrExpectationsUnsupported):
		writeError(w, http.StatusNotImplemented, "expectations not supported by this store")
	case errors.Is(err, govwrite.ErrExpectationNotFound):
		writeError(w, http.StatusNotFound, "no expectation for claim")
	case errors.Is(err, govwrite.ErrBeliefNotFound):
		writeError(w, http.StatusNotFound, "belief not found")
	case errors.Is(err, govwrite.ErrBeliefIDRequired):
		writeError(w, http.StatusBadRequest, "belief id is required")
	default:
		writeInternalError(w, label, err)
	}
}
