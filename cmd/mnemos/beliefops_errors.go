package main

import (
	"errors"
	"net/http"

	"go.klarlabs.de/mnemos/internal/beliefops"
)

// The transports' mapping of internal/beliefops' sentinel errors. Every
// belief operation a surface exposes reports through one of these, so a
// missing belief is a 404 (REST) or -32602 (MCP) whichever route reached it.

// mcpBeliefOpError marks a not-found or malformed request as the caller's to
// fix (-32602 with the message) rather than an opaque server fault (#355).
func mcpBeliefOpError(err error) error {
	if errors.Is(err, beliefops.ErrNotFound) || errors.Is(err, beliefops.ErrInvalid) {
		return userInputError(err)
	}
	return err
}

// writeBeliefOpError writes the REST status for a beliefops error: 404 for
// an unknown belief, 400 for a malformed request, 501 for a backend without
// the storage, 500 otherwise.
func writeBeliefOpError(w http.ResponseWriter, label string, err error) {
	switch {
	case errors.Is(err, beliefops.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, beliefops.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, beliefops.ErrUnsupported):
		writeError(w, http.StatusNotImplemented, err.Error())
	default:
		writeInternalError(w, label, err)
	}
}
