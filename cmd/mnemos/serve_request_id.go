package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"
)

// requestIDHeader is the canonical inbound + outbound header for the
// per-request correlation id. We accept any reasonable client-supplied
// value (so distributed tracers can pin a trace id end-to-end) and
// emit a fresh id only when no header is present.
const requestIDHeader = "X-Request-ID"

type requestIDKey struct{}

// withRequestID stores a request id in the context. Used by the access
// log middleware (and any future handler that wants to log the id) to
// retrieve the value placed by requestIDMiddleware.
func withRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// requestIDFromContext returns the per-request id, or an empty string
// when the context wasn't decorated by requestIDMiddleware.
func requestIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey{}).(string); ok {
		return v
	}
	return ""
}

// requestIDMiddleware ensures every request carries an X-Request-ID,
// writes it back to the response headers, and stores it in context so
// log lines can reference it. Without this, distributed tracing across
// HTTP, MCP, and pipeline boundaries is impossible — three independent
// log streams with no way to stitch them together.
//
// We accept inbound X-Request-ID so callers behind a tracer or load
// balancer can pin a single id across the whole request graph; if the
// header is missing or malformed (longer than 64 chars, control
// chars), we mint a fresh 16-byte hex id.
func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := sanitiseRequestID(r.Header.Get(requestIDHeader))
		if id == "" {
			id = newRequestID()
		}
		w.Header().Set(requestIDHeader, id)
		ctx := withRequestID(r.Context(), id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func sanitiseRequestID(raw string) string {
	if raw == "" || len(raw) > 64 {
		return ""
	}
	for _, c := range raw {
		// Allow common tracer charsets (alnum + hyphen + underscore).
		// Anything else is suspicious — discard and mint fresh.
		if c == '-' || c == '_' || c == '.' ||
			(c >= '0' && c <= '9') ||
			(c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') {
			continue
		}
		return ""
	}
	return raw
}

// newRequestID returns 16 bytes of crypto-strong randomness as 32
// lowercase hex chars. rand.Read on a modern OS is essentially
// non-blocking; the fallback path uses the wall clock so the function
// never returns an empty string even on the unlikely random failure.
func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Crypto rand failure is extraordinary — degrade to wall
		// clock + a constant suffix so we never return "" and break
		// the access log invariant.
		return time.Now().UTC().Format("20060102T150405.000000000")
	}
	return hex.EncodeToString(b[:])
}
