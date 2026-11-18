package main

import (
	"errors"
	"net/http"
	"strings"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/retraction"
)

// REST binding for event retraction (ADR 0037):
//
//	POST /v1/episodes/{id}/retract   body (optional): {"reason": "..."}
//
// It goes through runRetraction like the CLI and the retract_episode MCP tool,
// so the response body is the same retraction.Report document. A write: it
// needs claims:write (the scope retract_episode requires, since what changes
// is beliefs), resolves the tenant's writer via scopedWriter, and confines a
// run-scoped token to its runs — an episode outside them is a 404, exactly
// like one that does not exist.

// retractRequest is the optional body of POST /v1/episodes/{id}/retract.
type retractRequest struct {
	Reason string `json:"reason"`
}

// makeEpisodeSubresourceHandler routes /v1/episodes/<id>/<subresource>.
func makeEpisodeSubresourceHandler(gw *govwrite.Writer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tail := strings.TrimPrefix(r.URL.Path, "/v1/episodes/")
		parts := strings.SplitN(tail, "/", 2)
		if len(parts) != 2 || parts[0] == "" {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		switch parts[1] {
		case "retract":
			makeRetractEpisodeHandler(gw, parts[0])(w, r)
		case "unretract":
			makeUnretractEpisodeHandler(gw, parts[0])(w, r)
		default:
			writeError(w, http.StatusNotFound, "not found")
		}
	}
}

// makeUnretractEpisodeHandler serves POST /v1/episodes/{id}/unretract (ADR
// 0037 §8), body (optional): {"reason": "..."}. Same scope, tenant writer and
// run confinement as retraction; an episode that was never retracted is a 409.
func makeUnretractEpisodeHandler(gw *govwrite.Writer, episodeID string) http.HandlerFunc {
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
		var req retractRequest
		if r.ContentLength != 0 {
			if err := decodeJSON(r, &req); err != nil {
				writeError(w, http.StatusBadRequest, "decode body: "+err.Error())
				return
			}
		}
		view, err := runUnretraction(r.Context(), gw, episodeID, req.Reason, actorFromContext(r.Context()),
			func(runID string) bool { return runAllowedForRequest(r, runID) })
		switch {
		case errors.Is(err, retraction.ErrEventNotFound):
			writeError(w, http.StatusNotFound, "episode not found")
			return
		case errors.Is(err, retraction.ErrNotRetracted):
			writeError(w, http.StatusConflict, "episode was never retracted")
			return
		case errors.Is(err, retraction.ErrEventIDRequired):
			writeError(w, http.StatusBadRequest, err.Error())
			return
		case err != nil:
			writeInternalError(w, "un-retract episode", err)
			return
		}
		writeJSON(w, http.StatusOK, view)
	}
}

func makeRetractEpisodeHandler(gw *govwrite.Writer, episodeID string) http.HandlerFunc {
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
		var req retractRequest
		if r.ContentLength != 0 {
			if err := decodeJSON(r, &req); err != nil {
				writeError(w, http.StatusBadRequest, "decode body: "+err.Error())
				return
			}
		}
		view, err := runRetraction(r.Context(), gw, episodeID, req.Reason, actorFromContext(r.Context()),
			func(runID string) bool { return runAllowedForRequest(r, runID) })
		switch {
		case errors.Is(err, retraction.ErrEventNotFound):
			writeError(w, http.StatusNotFound, "episode not found")
			return
		case errors.Is(err, retraction.ErrEventIDRequired):
			writeError(w, http.StatusBadRequest, err.Error())
			return
		case err != nil:
			writeInternalError(w, "retract episode", err)
			return
		}
		writeJSON(w, http.StatusOK, view)
	}
}
