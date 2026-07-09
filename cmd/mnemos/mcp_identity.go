package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"go.klarlabs.de/mnemos/internal/auth"
)

// tenantIDRE mirrors the postgres provider's tenantRE (ADR 0007): a
// quote/backslash-free charset safe to interpolate into `SET mnemos.tenant`.
var tenantIDRE = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

// reservedDefaultTenant is the unscoped partition. A bearer token must never be
// scoped to it (that would grant access to the global/default data), so it is
// rejected as an explicit tenant even though it matches the charset.
const reservedDefaultTenant = "__default__"

// validTenantID reports whether id is a well-formed, non-reserved tenant id.
func validTenantID(id string) bool {
	return id != reservedDefaultTenant && tenantIDRE.MatchString(id)
}

// Per-request identity for the HTTP MCP transport. When `mnemos mcp --http`
// authenticates a request, the validated JWT claims are stashed in the request
// context so tool handlers attribute writes to the token's subject (not the
// process-wide MNEMOS_USER_ID) and enforce the token's run allowlist.
//
// On the stdio transport there are no claims, so every accessor falls back to
// the process actor and imposes no run restriction — behavior is unchanged.

type mcpClaimsKey struct{}

// mcpTenantKey carries the EFFECTIVE tenant for a request — set only when the
// server runs in multi-tenant mode (`mcp --http --require-tenant`). It is
// deliberately separate from the claims: a token's `tnt` claim is only honored
// when the server opted into multi-tenancy, so a single-tenant server never
// lets a token silently switch tenants.
type mcpTenantKey struct{}

// withTenant returns a context carrying the effective tenant for the request.
func withTenant(ctx context.Context, tenant string) context.Context {
	if tenant == "" {
		return ctx
	}
	return context.WithValue(ctx, mcpTenantKey{}, tenant)
}

// tenantFromContext returns the effective tenant for the request, if any.
func tenantFromContext(ctx context.Context) (string, bool) {
	t, ok := ctx.Value(mcpTenantKey{}).(string)
	return t, ok && t != ""
}

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

// mcpRunRestricted reports whether the request's token carries a non-empty run
// allowlist (so an unscoped, all-runs operation must be refused).
func mcpRunRestricted(ctx context.Context) bool {
	if c, ok := claimsFromContext(ctx); ok {
		return len(c.Runs) > 0
	}
	return false
}

// enforceRunScope is the single guard every run-carrying tool calls. It denies
// a request that targets a run outside the token's allowlist, AND — fail-closed
// — denies an unscoped (empty run) operation from a run-restricted token, which
// would otherwise read or write across every run. Unauthenticated / unrestricted
// callers pass through unchanged.
func enforceRunScope(ctx context.Context, runID string) error {
	runID = strings.TrimSpace(runID)
	if runID != "" {
		if !mcpRunAllowed(ctx, runID) {
			return fmt.Errorf("not authorized for run %q", runID)
		}
		return nil
	}
	if mcpRunRestricted(ctx) {
		return errors.New("this token is restricted to specific runs; specify a run_id within your allowlist")
	}
	return nil
}
