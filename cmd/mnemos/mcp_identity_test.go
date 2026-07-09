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
