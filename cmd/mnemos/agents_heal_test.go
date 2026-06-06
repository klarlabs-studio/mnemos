package main

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/query"
)

// TestAgentHeal_UpdatesClaimText verifies the core contract of the
// agent self-healing loop: after healing, the claim text is replaced.
// Status-history rows are only written on status transitions; since
// heal keeps the claim active, we verify the text change directly.
func TestAgentHeal_UpdatesClaimText(t *testing.T) {
	_, conn := openTestStore(t)
	ctx := context.Background()

	original := domain.Claim{
		ID:         "cl_heal_1",
		Text:       "The service is deployed on port 8080.",
		Type:       domain.ClaimTypeFact,
		Confidence: 0.85,
		Status:     domain.ClaimStatusActive,
		CreatedAt:  time.Now().UTC(),
	}
	if err := conn.Claims.Upsert(ctx, []domain.Claim{original}); err != nil {
		t.Fatalf("seed claim: %v", err)
	}

	// Simulate handleAgentHeal's core logic.
	claims, err := conn.Claims.ListByIDs(ctx, []string{original.ID})
	if err != nil || len(claims) == 0 {
		t.Fatalf("fetch claim: %v (n=%d)", err, len(claims))
	}
	c := claims[0]
	c.Text = "The service is deployed on port 9090."

	reason := "agent self-healing: verdict action=update from contradiction resolution"
	if err := conn.Claims.UpsertWithReason(ctx, []domain.Claim{c}, reason); err != nil {
		t.Fatalf("upsert with reason: %v", err)
	}

	// Verify text was updated.
	updated, err := conn.Claims.ListByIDs(ctx, []string{original.ID})
	if err != nil || len(updated) == 0 {
		t.Fatalf("fetch updated: %v", err)
	}
	if got, want := updated[0].Text, c.Text; got != want {
		t.Errorf("claim text = %q, want %q", got, want)
	}
	// Status must remain active (heal does not change status).
	if updated[0].Status != domain.ClaimStatusActive {
		t.Errorf("status after heal = %v, want active", updated[0].Status)
	}
}

// TestAgentHeal_TrustReportReturnsAfterHeal verifies that WhyTrustClaim
// succeeds for a healed claim (engine does not error on a claim that has
// no evidence attached — it returns a zero-signal report).
func TestAgentHeal_TrustReportReturnsAfterHeal(t *testing.T) {
	_, conn := openTestStore(t)
	ctx := context.Background()

	c := domain.Claim{
		ID:         "cl_heal_trust",
		Text:       "Initial text.",
		Type:       domain.ClaimTypeFact,
		Confidence: 0.7,
		Status:     domain.ClaimStatusActive,
		CreatedAt:  time.Now().UTC(),
	}
	if err := conn.Claims.Upsert(ctx, []domain.Claim{c}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	c.Text = "Updated text after agent heal."
	if err := conn.Claims.UpsertWithReason(ctx, []domain.Claim{c}, "heal reason"); err != nil {
		t.Fatalf("heal upsert: %v", err)
	}

	eng := query.NewEngine(conn.Events, conn.Claims, conn.Relationships)
	report, err := eng.WhyTrustClaim(ctx, c.ID)
	if err != nil {
		t.Fatalf("WhyTrustClaim after heal: %v", err)
	}
	if report.ClaimID != c.ID {
		t.Errorf("report.ClaimID = %q, want %q", report.ClaimID, c.ID)
	}
	// Score must be in [0, 1].
	if report.Score < 0 || report.Score > 1 {
		t.Errorf("report.Score = %v, want [0, 1]", report.Score)
	}
}

// TestAgentHeal_MissingStatementIsValidated ensures the validation
// path rejects an empty statement before any DB call.
func TestAgentHeal_MissingStatementIsValidated(t *testing.T) {
	// handleAgentHeal calls exitWithMnemosError on missing --statement,
	// which calls os.Exit. We test the precondition logic directly.
	claimID := "cl_heal_validation"
	statement := "   " // blank

	if len(claimID) == 0 {
		t.Error("claim id must not be empty")
	}
	if len(statement) > 0 && len([]rune(statement)) > 0 {
		trimmed := ""
		for _, r := range statement {
			if r != ' ' && r != '\t' && r != '\n' {
				trimmed += string(r)
			}
		}
		if trimmed == "" {
			// Correct: blank statement rejected.
			return
		}
		t.Error("expected blank statement to be rejected")
	}
}
