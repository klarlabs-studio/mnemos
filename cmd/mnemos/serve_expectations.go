package main

import (
	"net/http"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
)

// expectationDTO is the wire form of a structured forward expectation attached
// to a claim (mnemos T1.1 over HTTP). The library has had Expect /
// RecordObservation / ReconcileExpectations; these endpoints expose the
// predict + observe half to HTTP-service consumers. Reconciliation into
// validates/refutes verdicts (and the trust feedback) still runs in the nightly
// consolidate pass — these endpoints only record the prediction and its
// observed value, and read them back so a consumer can surface the surprise.
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
func makeExpectationHandler(conn *store.Conn, claimID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn := scopedConn(r.Context(), conn)
		if conn.Expectations == nil {
			writeError(w, http.StatusNotImplemented, "expectations not supported by this store")
			return
		}
		switch r.Method {
		case http.MethodGet:
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
			if !requireScope(w, r, domain.ScopeClaimsWrite) {
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
			if req.Tolerance < 0 {
				req.Tolerance = -req.Tolerance
			}
			exp := domain.Expectation{
				ClaimID:   claimID,
				Predicted: req.Predicted,
				Tolerance: req.Tolerance,
				Horizon:   horizon,
				CreatedAt: time.Now().UTC(),
			}
			if err := conn.Expectations.Upsert(r.Context(), exp); err != nil {
				writeInternalError(w, "upsert expectation", err)
				return
			}
			writeJSON(w, http.StatusCreated, expectationToDTO(exp))
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

// makeObservationHandler serves POST /v1/beliefs/<id>/observation: record the
// observed value against an existing expectation (mirrors RecordObservation).
func makeObservationHandler(conn *store.Conn, claimID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn := scopedConn(r.Context(), conn)
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if conn.Expectations == nil {
			writeError(w, http.StatusNotImplemented, "expectations not supported by this store")
			return
		}
		if !requireScope(w, r, domain.ScopeClaimsWrite) {
			return
		}
		var req struct {
			Observed float64 `json:"observed"`
		}
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "decode body: "+err.Error())
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
		exp.Observed = req.Observed
		exp.HasObservation = true
		if err := conn.Expectations.Upsert(r.Context(), exp); err != nil {
			writeInternalError(w, "upsert observation", err)
			return
		}
		writeJSON(w, http.StatusOK, expectationToDTO(exp))
	}
}
