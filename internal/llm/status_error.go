package llm

import (
	"errors"
	"fmt"
	"net/http"
)

// StatusError is a provider's non-200 HTTP reply. Body has the API key
// removed (withoutSecret) before it is stored.
type StatusError struct {
	Provider string
	Status   int
	Body     string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s returned status %d: %s", e.Provider, e.Status, e.Body)
}

// IsPermanent reports whether err is a provider reply that a retry cannot
// fix: a rejected credential (401), an exhausted account (402), a forbidden
// model (403) or a wrong endpoint (404). Rate limits (429), timeouts and 5xx
// are transient. Retrying a permanent error only multiplies the failed calls
// before anyone notices (#451).
func IsPermanent(err error) bool {
	var se *StatusError
	if !errors.As(err, &se) {
		return false
	}
	switch se.Status {
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden, http.StatusNotFound:
		return true
	}
	return false
}
