package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	mcp "go.klarlabs.de/mcp"
	mcptransport "go.klarlabs.de/mcp/transport"
	"go.klarlabs.de/mnemos/internal/auth"
)

// serveMCPHTTP serves the assembled MCP server over Streamable HTTP (+ SSE)
// instead of stdio, so a hosted Mnemos can be reached by remote MCP clients
// like Claude Code via `claude mcp add --transport http`. It reuses the exact
// same tool set as the stdio path.
//
// requireAuth gates every request behind a bearer JWT (the same tokens
// `mnemos token`/`mnemos agent token issue` mint, validated with the shared
// signing secret). TLS is enabled when MNEMOS_TLS_CERT_FILE / _KEY_FILE are
// set, matching `mnemos serve`.
func serveMCPHTTP(ctx context.Context, srv *mcp.Server, addr string, requireAuth, requireTenant bool, mw mcp.ServeOption) error {
	var opts []mcp.HTTPOption

	if requireAuth {
		verifier, err := buildMCPVerifier(ctx)
		if err != nil {
			return fmt.Errorf("mcp http auth: %w", err)
		}
		// Reject unauthenticated/invalid requests before they reach a handler.
		// In multi-tenant mode a token without a valid `tnt` claim is rejected
		// too — fail closed rather than fall back to the default tenant.
		opts = append(opts, mcptransport.WithAuthorize(func(r *http.Request) error {
			tok := bearerToken(r.Header.Get("Authorization"))
			if tok == "" {
				return errors.New("missing bearer token (Authorization: Bearer <jwt>)")
			}
			claims, err := verifier.ParseAndValidate(r.Context(), tok)
			if err != nil {
				return fmt.Errorf("invalid token: %w", err)
			}
			if requireTenant {
				t := strings.TrimSpace(claims.Tenant)
				if t == "" {
					return errors.New("token has no tenant (tnt) claim; this server requires one")
				}
				if !validTenantID(t) {
					return fmt.Errorf("token tenant %q is malformed", t)
				}
			}
			return nil
		}))
		// Thread the validated claims into each request's context so handlers
		// attribute writes to the token subject and honor its run allowlist.
		// In multi-tenant mode also stash the EFFECTIVE tenant so the request's
		// connection is scoped (RLS) to it.
		opts = append(opts, mcptransport.WithRequestContextFn(func(reqCtx context.Context, r *http.Request) context.Context {
			tok := bearerToken(r.Header.Get("Authorization"))
			if tok == "" {
				return reqCtx
			}
			claims, err := verifier.ParseAndValidate(r.Context(), tok)
			if err != nil {
				return reqCtx
			}
			reqCtx = withClaims(reqCtx, claims)
			if requireTenant && validTenantID(strings.TrimSpace(claims.Tenant)) {
				reqCtx = withTenant(reqCtx, strings.TrimSpace(claims.Tenant))
			}
			return reqCtx
		}))
	}

	if cert, key := os.Getenv("MNEMOS_TLS_CERT_FILE"), os.Getenv("MNEMOS_TLS_KEY_FILE"); cert != "" && key != "" {
		tlsCfg, err := buildServerTLS(cert, key, os.Getenv("MNEMOS_MTLS_CLIENT_CA_FILE"))
		if err != nil {
			return fmt.Errorf("mcp http tls: %w", err)
		}
		opts = append(opts, mcptransport.WithTLSConfig(tlsCfg))
	}

	scheme := "http"
	if os.Getenv("MNEMOS_TLS_CERT_FILE") != "" {
		scheme = "https"
	}
	fmt.Fprintf(os.Stderr, "mcp: serving over %s at %s (auth=%v)\n", scheme, addr, requireAuth)
	if !requireAuth {
		fmt.Fprintln(os.Stderr, "mcp: WARNING running without auth — anyone who can reach this address can read/write the brain; use --auth for a network listener")
	}

	return mcp.ServeHTTPWithMiddleware(ctx, srv, addr, opts, mw)
}

// buildMCPVerifier builds a JWT verifier from the shared signing secret (active
// + previous, for rotation) and the revoked-token repository, mirroring how
// `mnemos serve` authenticates its gRPC surface.
func buildMCPVerifier(ctx context.Context) (*auth.Verifier, error) {
	_, projectRoot, _ := findProjectDB()
	secretPath := auth.DefaultSecretPath(projectRoot)
	secret, _, err := auth.LoadOrCreateSecret(secretPath)
	if err != nil {
		return nil, fmt.Errorf("load signing secret: %w", err)
	}
	prev, err := auth.LoadPreviousSecret(secretPath)
	if err != nil {
		return nil, fmt.Errorf("load previous signing secret: %w", err)
	}
	// Held for the life of the server so revocation checks stay live. Uses the
	// base (unscoped) connection: revoked_tokens is auth-infra, excluded from
	// RLS (ADR 0007), so it must not be tenant-scoped or fail-closed.
	conn, err := openBaseConn(ctx)
	if err != nil {
		return nil, fmt.Errorf("open store for revocation checks: %w", err)
	}
	return auth.NewVerifierWithPrevious(secret, prev, conn.RevokedTokens), nil
}

// bearerToken extracts the token from an "Authorization: Bearer <token>"
// header value, case-insensitively. Returns "" when absent or malformed.
func bearerToken(header string) string {
	const prefix = "bearer "
	h := strings.TrimSpace(header)
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

// mcpServeConfig captures the transport selection parsed from `mnemos mcp`
// flags.
type mcpServeConfig struct {
	httpAddr      string // non-empty => serve over HTTP at this addr
	requireAuth   bool   // require a bearer JWT (default true for --http)
	requireTenant bool   // multi-tenant: every request must carry a `tnt` claim
}

// parseMCPArgs reads the transport flags for `mnemos mcp`:
//
//	mnemos mcp                       stdio (default)
//	mnemos mcp --http :8081          HTTP, auth required
//	mnemos mcp --http :8081 --no-auth   HTTP, no auth (trusted networks only)
func parseMCPArgs(args []string) (mcpServeConfig, error) {
	cfg := mcpServeConfig{requireAuth: true}
	authExplicit := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--http":
			if i+1 >= len(args) {
				return cfg, NewUserError("--http requires an address (e.g. --http :8081)")
			}
			cfg.httpAddr = args[i+1]
			i++
		case "--auth":
			cfg.requireAuth = true
			authExplicit = true
		case "--no-auth":
			cfg.requireAuth = false
			authExplicit = true
		case "--require-tenant":
			// Multi-tenant mode: every request must present a validated token
			// carrying a `tnt` claim. Implies auth.
			cfg.requireTenant = true
			cfg.requireAuth = true
			authExplicit = true
		default:
			return cfg, NewUserError("unknown mcp flag %q", args[i])
		}
	}
	// Auth/tenancy only apply to the HTTP listener; stdio is inherently local.
	if cfg.httpAddr == "" {
		cfg.requireAuth = false
		cfg.requireTenant = false
	}
	if cfg.requireTenant && !cfg.requireAuth {
		return cfg, NewUserError("--require-tenant cannot be combined with --no-auth")
	}
	_ = authExplicit
	return cfg, nil
}
