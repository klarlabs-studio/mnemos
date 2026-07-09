package main

import (
	"context"
	"testing"

	"go.klarlabs.de/mnemos/internal/auth"
)

func TestMCPActorFor(t *testing.T) {
	bg := context.Background()

	// No claims (stdio): fall back to the process actor.
	if got := mcpActorFor(bg, "<system>"); got != "<system>" {
		t.Errorf("no claims: got %q, want fallback", got)
	}

	// Claims with a subject: attribute to the token subject.
	ctx := withClaims(bg, &auth.Claims{UserID: "usr_alice"})
	if got := mcpActorFor(ctx, "<system>"); got != "usr_alice" {
		t.Errorf("with subject: got %q, want usr_alice", got)
	}

	// Claims with an empty subject: fall back.
	ctx = withClaims(bg, &auth.Claims{UserID: ""})
	if got := mcpActorFor(ctx, "fallback"); got != "fallback" {
		t.Errorf("empty subject: got %q, want fallback", got)
	}
}

func TestMCPRunAllowed(t *testing.T) {
	bg := context.Background()

	// No claims: every run allowed (stdio / unauthenticated).
	if !mcpRunAllowed(bg, "anything") {
		t.Error("no claims should allow every run")
	}

	// Empty allowlist: no restriction.
	ctx := withClaims(bg, &auth.Claims{})
	if !mcpRunAllowed(ctx, "any") {
		t.Error("empty allowlist should allow every run")
	}

	// Restricted token: only listed runs.
	ctx = withClaims(bg, &auth.Claims{Runs: []string{"run-alpha"}})
	if !mcpRunAllowed(ctx, "run-alpha") {
		t.Error("run-alpha should be allowed")
	}
	if mcpRunAllowed(ctx, "run-beta") {
		t.Error("run-beta must be denied for an alpha-scoped token")
	}
}

func TestClaimsFromContextAbsent(t *testing.T) {
	if _, ok := claimsFromContext(context.Background()); ok {
		t.Error("expected no claims in a bare context")
	}
	if _, ok := claimsFromContext(withClaims(context.Background(), nil)); ok {
		t.Error("withClaims(nil) should not register claims")
	}
}

func TestValidTenantID(t *testing.T) {
	for _, ok := range []string{"acme", "org-123", "a.b:c_d", "T"} {
		if !validTenantID(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	// Reserved default and malformed ids are rejected.
	for _, bad := range []string{"", "__default__", "has space", "quote'", "back\\slash", string(make([]byte, 129))} {
		if validTenantID(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

func TestEnforceRunScope(t *testing.T) {
	bg := context.Background()

	// Unauthenticated / no restriction: anything allowed, scoped or not.
	if err := enforceRunScope(bg, "anything"); err != nil {
		t.Errorf("unauthenticated should pass: %v", err)
	}
	if err := enforceRunScope(bg, ""); err != nil {
		t.Errorf("unauthenticated unscoped should pass: %v", err)
	}

	// Run-restricted token.
	ctx := withClaims(bg, &auth.Claims{Runs: []string{"alpha"}})
	if err := enforceRunScope(ctx, "alpha"); err != nil {
		t.Errorf("allowed run should pass: %v", err)
	}
	if err := enforceRunScope(ctx, "beta"); err == nil {
		t.Error("disallowed run must be denied")
	}
	if err := enforceRunScope(ctx, ""); err == nil {
		t.Error("unscoped op from a run-restricted token must be denied (fail-closed)")
	}

	// Authenticated but unrestricted (empty Runs): unscoped allowed.
	ctx2 := withClaims(bg, &auth.Claims{})
	if err := enforceRunScope(ctx2, ""); err != nil {
		t.Errorf("unrestricted token unscoped should pass: %v", err)
	}
}

func TestResolveDSNForContextFailClosed(t *testing.T) {
	t.Setenv("MNEMOS_DB_URL", "postgres://h/db")
	mcpTenantRequired = true
	t.Cleanup(func() { mcpTenantRequired = false })

	// require-tenant + no tenant in context → fail closed (no __default__).
	if _, err := resolveDSNForContext(context.Background()); err == nil {
		t.Error("multi-tenant mode with no tenant must fail closed")
	}
	// With a tenant → scoped DSN.
	got, err := resolveDSNForContext(withTenant(context.Background(), "acme"))
	if err != nil || got != "postgres://h/db?tenant=acme" {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestDSNHasTenantParam(t *testing.T) {
	if !dsnHasTenantParam("postgres://h/db?tenant=x") {
		t.Error("should detect tenant param")
	}
	if !dsnHasTenantParam("postgres://h/db?sslmode=require&tenant=x") {
		t.Error("should detect tenant param among others")
	}
	if dsnHasTenantParam("postgres://h/db?sslmode=require") {
		t.Error("should not report tenant when absent")
	}
	if dsnHasTenantParam("postgres://h/db") {
		t.Error("no query string → no tenant")
	}
}

func TestTenantFromContext(t *testing.T) {
	bg := context.Background()
	if _, ok := tenantFromContext(bg); ok {
		t.Error("bare context should carry no tenant")
	}
	if _, ok := tenantFromContext(withTenant(bg, "")); ok {
		t.Error("empty tenant must not register")
	}
	if tn, ok := tenantFromContext(withTenant(bg, "acme")); !ok || tn != "acme" {
		t.Errorf("tenant roundtrip failed: %q %v", tn, ok)
	}
}

func TestResolveDSNForContext(t *testing.T) {
	// No tenant → DSN unchanged.
	t.Setenv("MNEMOS_DB_URL", "sqlite:///tmp/a.db")
	if got, err := resolveDSNForContext(context.Background()); err != nil || got != "sqlite:///tmp/a.db" {
		t.Errorf("no tenant: got %q, %v", got, err)
	}

	// Tenant on postgres → appended.
	t.Setenv("MNEMOS_DB_URL", "postgres://h/db")
	ctx := withTenant(context.Background(), "acme")
	got, err := resolveDSNForContext(ctx)
	if err != nil || got != "postgres://h/db?tenant=acme" {
		t.Errorf("postgres tenant: got %q, %v", got, err)
	}

	// Tenant appended after an existing query string.
	t.Setenv("MNEMOS_DB_URL", "postgres://h/db?sslmode=require")
	got, _ = resolveDSNForContext(ctx)
	if got != "postgres://h/db?sslmode=require&tenant=acme" {
		t.Errorf("existing query: got %q", got)
	}

	// Tenant on a non-postgres backend → fail closed.
	t.Setenv("MNEMOS_DB_URL", "sqlite:///tmp/a.db")
	if _, err := resolveDSNForContext(ctx); err == nil {
		t.Error("tenant on non-postgres must error (fail closed)")
	}
}
