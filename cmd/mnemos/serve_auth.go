package main

import (
	"context"
	"fmt"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"go.klarlabs.de/bolt"
	"go.klarlabs.de/mnemos/internal/auth"
	"go.klarlabs.de/mnemos/internal/domain"
)

// actorContextKey tags the resolved user id on a request's context so
// downstream handlers can stamp it into created_by columns. A distinct
// unexported type keeps us from colliding with context keys from other
// packages.
type actorContextKey struct{}

// scopesContextKey tags the bearer's scope list onto the request
// context so handlers can call requireScope without re-parsing the
// token.
type scopesContextKey struct{}

// runsContextKey tags the bearer's run-id whitelist onto the request
// context. Empty list (no key set) means no run restriction.
type runsContextKey struct{}

// withActor returns a copy of ctx carrying the given user id.
func withActor(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, actorContextKey{}, userID)
}

// withScopes returns a copy of ctx carrying the bearer's scope list.
func withScopes(ctx context.Context, scopes []string) context.Context {
	return context.WithValue(ctx, scopesContextKey{}, scopes)
}

// withAllowedRuns returns a copy of ctx carrying the bearer's
// run-id whitelist. Pass nil/empty to leave the request unrestricted.
func withAllowedRuns(ctx context.Context, runs []string) context.Context {
	return context.WithValue(ctx, runsContextKey{}, runs)
}

// allowedRunsFromContext returns the bearer's run whitelist, or nil
// when no token was presented or the token had no restriction.
func allowedRunsFromContext(ctx context.Context) []string {
	if v, ok := ctx.Value(runsContextKey{}).([]string); ok {
		return v
	}
	return nil
}

// (requireRunAllowed deferred until a single-record write path
// needs it; appendEventsHandler currently does a batch pre-check
// inline because the request shape gives the run ids up front.)

// actorFromContext returns the user id previously installed via withActor.
// When the request is unauthenticated (reads), falls back to SystemUser so
// the caller always has a non-empty string to stamp.
func actorFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(actorContextKey{}).(string); ok && v != "" {
		return v
	}
	return domain.SystemUser
}

// scopesFromContext returns the bearer's scope list, or an empty slice
// when no token was presented (read-only requests).
func scopesFromContext(ctx context.Context) []string {
	if v, ok := ctx.Value(scopesContextKey{}).([]string); ok {
		return v
	}
	return nil
}

// requireScope returns true and writes nothing when the request's
// scope list grants want; otherwise it writes a 403 and returns false.
// Handlers should `if !requireScope(w, r, "events:write") { return }`
// at the very top of their POST path.
func requireScope(w http.ResponseWriter, r *http.Request, want string) bool {
	for _, s := range scopesFromContext(r.Context()) {
		if s == domain.ScopeWildcard || s == want {
			return true
		}
	}
	writeError(w, http.StatusForbidden, "missing required scope: "+want)
	return false
}

// jwtAuthMiddleware enforces JWT auth on mutating methods. Reads stay
// open: the registry is meant to be browsable, and the blast radius of
// an anonymous GET is bounded by the data we chose to expose.
//
// On POST/PUT/DELETE:
//   - Missing or malformed Authorization header → 401
//   - Invalid signature / expired / revoked token → 401
//   - Valid token → user id from the `sub` claim lands on the request
//     context for created_by stamping.
func jwtAuthMiddleware(verifier *auth.Verifier, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/leads" {
			h.ServeHTTP(w, r)
			return
		}

		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			h.ServeHTTP(w, r)
			return
		}

		raw := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(raw, prefix) {
			writeError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		tokenStr := strings.TrimPrefix(raw, prefix)
		if tokenStr == "" {
			writeError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}

		claims, err := verifier.ParseAndValidate(r.Context(), tokenStr)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid or revoked token")
			return
		}

		ctx := withActor(r.Context(), claims.UserID)
		ctx = withScopes(ctx, claims.Scopes)
		ctx = withAllowedRuns(ctx, claims.Runs)
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}

// panicRecover is the outermost middleware: it catches any panic
// raised by a handler, logs the full stack to stderr (so operators
// can debug), and returns a sanitised 500 to the client. Go's
// net/http already recovers panics and closes the connection, but
// the default behaviour swallows the stack and writes nothing —
// operators end up debugging blind. This middleware fixes that.
//
// Must wrap every other middleware so a panic in auth or the access
// log itself still produces a clean response.
func panicRecover(logger *bolt.Logger, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			logger.Error().
				Str("method", r.Method).
				Str("path", r.URL.RequestURI()).
				Str("panic", fmt.Sprintf("%v", rec)).
				Str("stack", string(debug.Stack())).
				Msg("http_panic")
			// The response may already be partially written. Best
			// effort — http.Error will silently no-op in that case.
			writeError(w, http.StatusInternalServerError, "internal error: panic")
		}()
		h.ServeHTTP(w, r)
	})
}

// boltAccessLog returns a middleware that emits one structured access
// log per request. Uses bolt so field names match the rest of the
// codebase; `user_id` is included when authentication resolved an actor
// so we can trace writes back to the issuing identity.
func boltAccessLog(logger *bolt.Logger, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		h.ServeHTTP(rw, r)

		actor := actorFromContext(r.Context())
		logger.Info().
			Str("request_id", requestIDFromContext(r.Context())).
			Str("method", r.Method).
			Str("path", r.URL.RequestURI()).
			Int("status", rw.status).
			Dur("duration", time.Since(start)).
			Str("user_id", actor).
			Msg("http_request")
	})
}
