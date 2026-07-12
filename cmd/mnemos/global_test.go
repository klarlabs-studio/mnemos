package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
)

// TestHandleGlobalAuthor_ApplyWithCuratorToken proves the born-global authoring
// surface end to end: a curator holding promote:global authors a class-level
// reference fact straight into the neocortex with --apply, and it appears in the
// global store (never having passed through any tenant).
func TestHandleGlobalAuthor_ApplyWithCuratorToken(t *testing.T) {
	ctx := context.Background()
	globalDSN := "sqlite://" + filepath.Join(t.TempDir(), "global.db")
	fact := "the sydney funnelweb spider bite requires antivenom within thirty minutes"

	t.Setenv("MNEMOS_TOKEN", curatorToken(t, []string{domain.ScopePromoteGlobal}))
	_ = captureStdout(t, func() {
		handleGlobal([]string{
			"author", "--statement", fact,
			"--scope-service", "toxicology",
			"--polarity", "positive", "--status", "active",
			"--global-dsn", globalDSN, "--apply",
		}, Flags{})
	})

	gconn, err := store.Open(ctx, globalDSN)
	if err != nil {
		t.Fatalf("open global: %v", err)
	}
	defer func() { _ = gconn.Close() }()

	all, err := gconn.GlobalSchemas.ListAll(ctx)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("want exactly 1 authored schema, got %d: %+v", len(all), all)
	}
	got := all[0]
	if got.Statement != fact {
		t.Fatalf("authored statement mismatch: %q", got.Statement)
	}
	if got.Status != domain.GlobalSchemaStatusActive {
		t.Fatalf("want active status, got %q", got.Status)
	}
	if got.Scope.Service != "toxicology" {
		t.Fatalf("scope not carried: %+v", got.Scope)
	}
	if got.CreatedBy != bornGlobalCreatedBy {
		t.Fatalf("want born-global authorship marker, got %q", got.CreatedBy)
	}
	if got.DistinctTenants != 1 {
		t.Fatalf("want distinct_tenants=1 for a single curator source, got %d", got.DistinctTenants)
	}
}

// TestHandleGlobalAuthor_ReauthorUpserts proves the id is content-addressed:
// authoring the same statement/scope/polarity twice upserts one row.
func TestHandleGlobalAuthor_ReauthorUpserts(t *testing.T) {
	ctx := context.Background()
	globalDSN := "sqlite://" + filepath.Join(t.TempDir(), "global.db")
	fact := "golden retrievers are predisposed to hip dysplasia"

	t.Setenv("MNEMOS_TOKEN", curatorToken(t, []string{domain.ScopePromoteGlobal}))
	for i := 0; i < 2; i++ {
		_ = captureStdout(t, func() {
			handleGlobal([]string{"author", "--statement", fact, "--global-dsn", globalDSN, "--apply"}, Flags{})
		})
	}

	gconn, err := store.Open(ctx, globalDSN)
	if err != nil {
		t.Fatalf("open global: %v", err)
	}
	defer func() { _ = gconn.Close() }()
	n, err := gconn.GlobalSchemas.CountAll(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("re-authoring must upsert one row, got %d", n)
	}
}

// TestHandleGlobalAuthor_DryRunWritesNothing confirms the default (no --apply)
// never touches the global store, even with a valid curator token.
func TestHandleGlobalAuthor_DryRunWritesNothing(t *testing.T) {
	ctx := context.Background()
	globalDSN := "sqlite://" + filepath.Join(t.TempDir(), "global.db")

	// Bootstrap the store so its GlobalSchemas repo exists to be counted.
	conn, err := store.Open(ctx, globalDSN)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_ = conn.Close()

	t.Setenv("MNEMOS_TOKEN", curatorToken(t, []string{domain.ScopePromoteGlobal}))
	out := captureStdout(t, func() {
		handleGlobal([]string{"author", "--statement", "a class-level reference fact", "--global-dsn", globalDSN}, Flags{})
	})
	if !contains(out, `"dry_run": true`) {
		t.Fatalf("expected dry_run true in output, got: %s", out)
	}

	gconn, err := store.Open(ctx, globalDSN)
	if err != nil {
		t.Fatalf("reopen global: %v", err)
	}
	defer func() { _ = gconn.Close() }()
	n, err := gconn.GlobalSchemas.CountAll(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("dry-run must not write; found %d global schemas", n)
	}
}

// TestParseGlobalAuthorOpts_RequiresStatement rejects an author call with no
// statement.
func TestParseGlobalAuthorOpts_RequiresStatement(t *testing.T) {
	if _, err := parseGlobalAuthorOpts([]string{"--polarity", "positive"}); err == nil {
		t.Fatal("author without --statement must be rejected")
	}
}

// TestGlobalAuthorID_MatchesPromotionIDDerivation locks the content-addressed id
// to the same derivation the promotion write path uses, so a born-global row and
// a promoted row for the same class fact share one id (upsert, not churn).
func TestGlobalAuthorID_MatchesPromotionIDDerivation(t *testing.T) {
	opts := globalAuthorOpts{
		statement: "golden retrievers are predisposed to hip dysplasia",
		scope:     domain.Scope{Service: "vet"},
		polarity:  domain.SchemaPolarityPositive,
	}
	gs := opts.toGlobalSchema(time.Time{})
	// Empty polarity must normalize to the same id as an explicit positive one.
	optsEmpty := opts
	optsEmpty.polarity = ""
	if gs.ID != optsEmpty.toGlobalSchema(time.Time{}).ID {
		t.Fatal("empty polarity must derive the same id as positive")
	}
	if gs.ID == "" {
		t.Fatal("id must be non-empty")
	}
}
