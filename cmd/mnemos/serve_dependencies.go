package main

import (
	"errors"
	"net/http"

	"go.klarlabs.de/mnemos/internal/dependency"
	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/govwrite"
)

// REST binding for editing source dependence between episodes (ADR 0038):
//
//	GET    /v1/episodes/{id}/dependencies[?include_removed=true]
//	POST   /v1/episodes/{id}/dependencies               body: {"depends_on", "kind", "note"}
//	DELETE /v1/episodes/{id}/dependencies/{depends_on}
//
// Each goes through the same internal/dependency call as the CLI and the MCP
// tools, so the bodies are the same dependency.ListReport / ChangeReport
// documents. The writes need claims:write (what changes is belief trust, the
// scope retract_episode requires), resolve the tenant's writer, and confine a
// run-scoped token to its runs: an episode outside them is a 404, exactly like
// one that does not exist.

// declareDependencyRequest is the body of POST /v1/episodes/{id}/dependencies.
type declareDependencyRequest struct {
	DependsOn string `json:"depends_on"`
	Kind      string `json:"kind"`
	Note      string `json:"note"`
}

func makeEpisodeDependenciesHandler(gw *govwrite.Writer, episodeID, dependsOn string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		gw := scopedWriter(r.Context(), gw)
		switch {
		case dependsOn == "" && r.Method == http.MethodGet:
			listEpisodeDependencies(w, r, gw, episodeID)
		case dependsOn == "" && r.Method == http.MethodPost:
			declareEpisodeDependency(w, r, gw, episodeID)
		case dependsOn != "" && r.Method == http.MethodDelete:
			removeEpisodeDependency(w, r, gw, episodeID, dependsOn)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

func listEpisodeDependencies(w http.ResponseWriter, r *http.Request, gw *govwrite.Writer, episodeID string) {
	conn := scopedConn(r.Context(), nil)
	if conn == nil && gw != nil {
		conn = gw.Conn()
	}
	if conn == nil {
		writeError(w, http.StatusServiceUnavailable, "store unavailable")
		return
	}
	includeRemoved := false
	switch r.URL.Query().Get("include_removed") {
	case "", "false", "0":
	case "true", "1":
		includeRemoved = true
	default:
		writeError(w, http.StatusBadRequest, "include_removed must be true or false")
		return
	}
	view, err := runDependencyList(r.Context(), conn, episodeID, includeRemoved,
		func(runID string) bool { return runAllowedForRequest(r, runID) })
	if writeDependencyError(w, err, "list episode dependencies") {
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func declareEpisodeDependency(w http.ResponseWriter, r *http.Request, gw *govwrite.Writer, episodeID string) {
	if !requireScope(w, r, domain.ScopeClaimsWrite) {
		return
	}
	if gw == nil {
		writeError(w, http.StatusServiceUnavailable, "writer unavailable")
		return
	}
	var req declareDependencyRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "decode body: "+err.Error())
		return
	}
	view, err := runDependencyDeclare(r.Context(), gw, episodeID, req.DependsOn, req.Kind, req.Note, actorFromContext(r.Context()),
		func(runID string) bool { return runAllowedForRequest(r, runID) })
	if writeDependencyError(w, err, "declare episode dependency") {
		return
	}
	writeJSON(w, http.StatusCreated, view)
}

func removeEpisodeDependency(w http.ResponseWriter, r *http.Request, gw *govwrite.Writer, episodeID, dependsOn string) {
	if !requireScope(w, r, domain.ScopeClaimsWrite) {
		return
	}
	if gw == nil {
		writeError(w, http.StatusServiceUnavailable, "writer unavailable")
		return
	}
	// r.URL.Path is already decoded, like the episode id segment.
	view, err := runDependencyRemove(r.Context(), gw, episodeID, dependsOn, actorFromContext(r.Context()),
		func(runID string) bool { return runAllowedForRequest(r, runID) })
	if writeDependencyError(w, err, "remove episode dependency") {
		return
	}
	writeJSON(w, http.StatusOK, view)
}

// writeDependencyError maps the internal/dependency errors to statuses and
// reports whether it wrote a response.
func writeDependencyError(w http.ResponseWriter, err error, label string) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, dependency.ErrEventNotFound):
		writeError(w, http.StatusNotFound, "episode not found")
	case errors.Is(err, dependency.ErrNotFound):
		writeError(w, http.StatusNotFound, "episode dependency not found")
	case errors.Is(err, dependency.ErrExists):
		writeError(w, http.StatusConflict, "episode dependency already exists")
	case errors.Is(err, dependency.ErrInvalid), errors.Is(err, dependency.ErrEventIDRequired):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeInternalError(w, label, err)
	}
	return true
}
