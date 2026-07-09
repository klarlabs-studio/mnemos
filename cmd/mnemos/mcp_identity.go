package main

import (
	"context"
	"strings"

	"go.klarlabs.de/mnemos/internal/auth"
)

// Per-request identity for the HTTP MCP transport. When `mnemos mcp --http`
// authenticates a request, the validated JWT claims are stashed in the request
// context so tool handlers attribute writes to the token's subject (not the
// process-wide MNEMOS_USER_ID) and enforce the token's run allowlist.
//
// On the stdio transport there are no claims, so every accessor falls back to
// the process actor and imposes no run restriction — behavior is unchanged.

type mcpClaimsKey struct{}

// withClaims returns a context carrying the request's validated JWT claims.
func withClaims(ctx context.Context, claims *auth.Claims) context.Context {
	if claims == nil {
		return ctx
	}
	return context.WithValue(ctx, mcpClaimsKey{}, claims)
}

// claimsFromContext returns the request's claims when present.
func claimsFromContext(ctx context.Context) (*auth.Claims, bool) {
	c, ok := ctx.Value(mcpClaimsKey{}).(*auth.Claims)
	return c, ok && c != nil
}

// mcpActorFor resolves the actor to attribute a write to: the token subject
// when the request is authenticated, otherwise the process fallback.
func mcpActorFor(ctx context.Context, fallback string) string {
	if c, ok := claimsFromContext(ctx); ok {
		if sub := strings.TrimSpace(c.UserID); sub != "" {
			return sub
		}
	}
	return fallback
}

// mcpRunAllowed reports whether the request may touch runID. Unauthenticated
// requests (stdio) and tokens without a run allowlist allow every run.
func mcpRunAllowed(ctx context.Context, runID string) bool {
	if c, ok := claimsFromContext(ctx); ok {
		return c.AllowsRun(runID)
	}
	return true
}
