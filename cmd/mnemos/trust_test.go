package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// TestMcpRunWhichTestToTrust_PicksFreshHigherPassRatio is the happy
// path: two test_result claims under one requirement, one stale and
// flaky, one fresh and decisive — the fresh one wins.
func TestMcpRunWhichTestToTrust_PicksFreshHigherPassRatio(t *testing.T) {
	t.Setenv("MNEMOS_DB_URL", "sqlite://"+filepath.Join(t.TempDir(), "mnemos.db"))

	// Seed via openConn so the trust path uses the same backend the
	// real MCP handler will hit.
	ctx := context.Background()
	conn, err := openConn(ctx)
	if err != nil {
		t.Fatalf("openConn: %v", err)
	}
	defer closeConn(conn)

	now := time.Now().UTC()
	stale := domain.Claim{
		ID: "cl_stale", Text: "test_login.sh failed once", Type: domain.ClaimTypeTestResult,
		Status: domain.ClaimStatusActive, Confidence: 0.85,
		TestID: "test_login_stale", TestRequirementRef: "REQ-LOGIN",
		TestPassCount: 5, TestFailCount: 5,
		TestLastRunAt: now.Add(-30 * 24 * time.Hour),
		CreatedAt:     now,
	}
	fresh := domain.Claim{
		ID: "cl_fresh", Text: "test_login.sh passed", Type: domain.ClaimTypeTestResult,
		Status: domain.ClaimStatusActive, Confidence: 0.85,
		TestID: "test_login_fresh", TestRequirementRef: "REQ-LOGIN",
		TestPassCount: 10, TestFailCount: 0,
		TestLastRunAt: now.Add(-2 * time.Hour),
		CreatedAt:     now,
	}
	if err := conn.Claims.Upsert(ctx, []domain.Claim{stale, fresh}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	out, err := mcpRunWhichTestToTrust(ctx, mcpWhichTestToTrustInput{RequirementRef: "REQ-LOGIN"})
	if err != nil {
		t.Fatalf("mcpRunWhichTestToTrust: %v", err)
	}

	if out.Verdict != "winner" {
		t.Errorf("verdict = %q, want winner", out.Verdict)
	}
	if out.WinnerClaimID != "cl_fresh" {
		t.Errorf("winner = %q, want cl_fresh (fresher + decisive)", out.WinnerClaimID)
	}
	if len(out.Candidates) != 2 {
		t.Errorf("candidates = %d, want 2", len(out.Candidates))
	}
	if out.WinnerScore <= 0 || out.WinnerScore > 1 {
		t.Errorf("winner_score %.3f outside [0,1]", out.WinnerScore)
	}
}

// TestMcpRunWhichTestToTrust_AmbiguousVerdictWhenMargingTight asserts
// that two near-identical candidates surface as "ambiguous" rather than
// silently picking a weak winner.
func TestMcpRunWhichTestToTrust_AmbiguousVerdictWhenMarginTight(t *testing.T) {
	t.Setenv("MNEMOS_DB_URL", "sqlite://"+filepath.Join(t.TempDir(), "mnemos.db"))
	ctx := context.Background()
	conn, err := openConn(ctx)
	if err != nil {
		t.Fatalf("openConn: %v", err)
	}
	defer closeConn(conn)

	now := time.Now().UTC()
	a := domain.Claim{
		ID: "cl_a", Text: "test_a passed", Type: domain.ClaimTypeTestResult,
		Status: domain.ClaimStatusActive, Confidence: 0.85,
		TestID: "ta", TestRequirementRef: "REQ-PARITY",
		TestPassCount: 5, TestFailCount: 5,
		TestLastRunAt: now.Add(-1 * time.Hour),
		CreatedAt:     now,
	}
	b := domain.Claim{
		ID: "cl_b", Text: "test_b passed", Type: domain.ClaimTypeTestResult,
		Status: domain.ClaimStatusActive, Confidence: 0.85,
		TestID: "tb", TestRequirementRef: "REQ-PARITY",
		TestPassCount: 5, TestFailCount: 5,
		TestLastRunAt: now.Add(-1 * time.Hour),
		CreatedAt:     now,
	}
	if err := conn.Claims.Upsert(ctx, []domain.Claim{a, b}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	out, err := mcpRunWhichTestToTrust(ctx, mcpWhichTestToTrustInput{RequirementRef: "REQ-PARITY"})
	if err != nil {
		t.Fatalf("mcpRunWhichTestToTrust: %v", err)
	}
	if out.Verdict != "ambiguous" {
		t.Errorf("verdict = %q, want ambiguous (gap < 0.05 between identical inputs)", out.Verdict)
	}
}

// TestMcpRunWhichTestToTrust_NoCandidates returns the no_candidates
// verdict for a requirement_ref with no matching test_result claims.
func TestMcpRunWhichTestToTrust_NoCandidates(t *testing.T) {
	t.Setenv("MNEMOS_DB_URL", "sqlite://"+filepath.Join(t.TempDir(), "mnemos.db"))
	ctx := context.Background()
	if _, err := openConn(ctx); err != nil {
		t.Fatalf("openConn: %v", err)
	}

	out, err := mcpRunWhichTestToTrust(ctx, mcpWhichTestToTrustInput{RequirementRef: "REQ-DOES-NOT-EXIST"})
	if err != nil {
		t.Fatalf("mcpRunWhichTestToTrust: %v", err)
	}
	if out.Verdict != "no_candidates" {
		t.Errorf("verdict = %q, want no_candidates", out.Verdict)
	}
	if len(out.Candidates) != 0 {
		t.Errorf("candidates = %d, want 0", len(out.Candidates))
	}
}

// TestMcpRunWhichTestToTrust_RequiresRequirementRef rejects empty input.
func TestMcpRunWhichTestToTrust_RequiresRequirementRef(t *testing.T) {
	t.Setenv("MNEMOS_DB_URL", "sqlite://"+filepath.Join(t.TempDir(), "mnemos.db"))
	ctx := context.Background()
	_, err := mcpRunWhichTestToTrust(ctx, mcpWhichTestToTrustInput{RequirementRef: ""})
	if err == nil {
		t.Fatal("expected error for empty requirement_ref")
	}
	if !strings.Contains(err.Error(), "requirement_ref") {
		t.Errorf("error = %q, want it to mention requirement_ref", err)
	}
}

// TestMcpRunWhichTestToTrust_ScopeFilter ensures the per-call scope
// filter narrows the candidate set so a low-privilege caller cannot
// enumerate test claims across services / envs / teams it shouldn't
// see — closes the security gap flagged in the audit.
func TestMcpRunWhichTestToTrust_ScopeFilter(t *testing.T) {
	t.Setenv("MNEMOS_DB_URL", "sqlite://"+filepath.Join(t.TempDir(), "mnemos.db"))
	ctx := context.Background()
	conn, err := openConn(ctx)
	if err != nil {
		t.Fatalf("openConn: %v", err)
	}
	defer closeConn(conn)

	now := time.Now().UTC()
	prod := domain.Claim{
		ID: "cl_prod", Text: "test_x in prod passed", Type: domain.ClaimTypeTestResult,
		Status: domain.ClaimStatusActive, Confidence: 0.85,
		TestID: "tx_prod", TestRequirementRef: "REQ-MULTI",
		TestPassCount: 5, TestFailCount: 0, TestLastRunAt: now,
		Scope:     domain.Scope{Service: "billing", Env: "prod"},
		CreatedAt: now,
	}
	staging := domain.Claim{
		ID: "cl_stg", Text: "test_x in staging failed", Type: domain.ClaimTypeTestResult,
		Status: domain.ClaimStatusActive, Confidence: 0.85,
		TestID: "tx_stg", TestRequirementRef: "REQ-MULTI",
		TestPassCount: 0, TestFailCount: 5, TestLastRunAt: now,
		Scope:     domain.Scope{Service: "billing", Env: "staging"},
		CreatedAt: now,
	}
	if err := conn.Claims.Upsert(ctx, []domain.Claim{prod, staging}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Without filter, both candidates appear.
	all, err := mcpRunWhichTestToTrust(ctx, mcpWhichTestToTrustInput{RequirementRef: "REQ-MULTI"})
	if err != nil {
		t.Fatalf("all: %v", err)
	}
	if len(all.Candidates) != 2 {
		t.Errorf("unfiltered candidates = %d, want 2", len(all.Candidates))
	}

	// With env=prod, only the prod claim should appear.
	prodOnly, err := mcpRunWhichTestToTrust(ctx, mcpWhichTestToTrustInput{
		RequirementRef: "REQ-MULTI",
		Env:            "prod",
	})
	if err != nil {
		t.Fatalf("prod-only: %v", err)
	}
	if len(prodOnly.Candidates) != 1 || prodOnly.Candidates[0].ClaimID != "cl_prod" {
		t.Errorf("env=prod filter wrong: got %+v", prodOnly.Candidates)
	}
}

// TestMcpRunWhichTestToTrust_FilterScopedToTestResultOnly proves the
// underlying ListByTestRequirementRef path ignores non-test claims even
// when their text mentions the requirement_ref. Guards against future
// regressions if someone changes the SQL filter.
func TestMcpRunWhichTestToTrust_FilterScopedToTestResultOnly(t *testing.T) {
	t.Setenv("MNEMOS_DB_URL", "sqlite://"+filepath.Join(t.TempDir(), "mnemos.db"))
	ctx := context.Background()
	conn, err := openConn(ctx)
	if err != nil {
		t.Fatalf("openConn: %v", err)
	}
	defer closeConn(conn)

	now := time.Now().UTC()
	// A non-test claim that happens to share the requirement_ref
	// column shouldn't appear (other claim types don't populate that
	// column anyway, but the SQL filter must enforce type=test_result).
	nonTest := domain.Claim{
		ID: "cl_doc", Text: "Spec REQ-X mentions login flow",
		Type:       domain.ClaimTypeFact,
		Status:     domain.ClaimStatusActive,
		Confidence: 0.9,
		CreatedAt:  now,
	}
	tc := domain.Claim{
		ID: "cl_t", Text: "test_x passed", Type: domain.ClaimTypeTestResult,
		Status: domain.ClaimStatusActive, Confidence: 0.85,
		TestID: "tx", TestRequirementRef: "REQ-X",
		TestPassCount: 1, TestFailCount: 0,
		TestLastRunAt: now,
		CreatedAt:     now,
	}
	if err := conn.Claims.Upsert(ctx, []domain.Claim{nonTest, tc}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	out, err := mcpRunWhichTestToTrust(ctx, mcpWhichTestToTrustInput{RequirementRef: "REQ-X"})
	if err != nil {
		t.Fatalf("mcpRunWhichTestToTrust: %v", err)
	}
	if len(out.Candidates) != 1 {
		t.Fatalf("candidates = %d, want 1 (test_result only)", len(out.Candidates))
	}
	if out.Candidates[0].ClaimID != "cl_t" {
		t.Errorf("candidate id = %q, want cl_t", out.Candidates[0].ClaimID)
	}
}
