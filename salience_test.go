package mnemos

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// seedClaimWithSalience seeds a valid fact claim carrying an explicit salience
// stakes component.
func seedClaimWithSalience(t *testing.T, m *memory, id string, confidence, sal float64) {
	t.Helper()
	now := time.Now().UTC()
	if err := m.conn.Claims.Upsert(context.Background(), []domain.Claim{{
		ID: id, Text: "claim " + id, Type: domain.ClaimTypeFact,
		Confidence: confidence, Status: domain.ClaimStatusActive,
		CreatedAt: now, ValidFrom: now,
		ConfidenceComponents: map[string]float64{domain.SalienceComponentKey: sal},
	}}); err != nil {
		t.Fatalf("seed salient claim %s: %v", id, err)
	}
}

func seedRiskyDecision(t *testing.T, m *memory, id string, risk domain.RiskLevel, beliefs ...string) {
	t.Helper()
	if err := m.conn.Decisions.Append(context.Background(), domain.Decision{
		ID: id, Statement: "decision " + id, RiskLevel: risk,
		Beliefs: beliefs, ChosenAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed decision %s: %v", id, err)
	}
}

// TestConsolidate_DeriveSalience is the ADR-0013 §4 sourcing test: a belief that
// informed a HIGH-risk decision is tagged with an above-neutral salience derived
// from the risk level, written into confidence_components (no schema change); a
// belief touched only by a medium/low-risk decision is left neutral; an explicit
// override is never lowered; default-neutral when AssignSalience is off.
func TestConsolidate_DeriveSalience(t *testing.T) {
	m := calibMem(t)
	ctx := context.Background()

	seedClaim(t, m, "b_critical", 0.8)                  // → tagged high from critical decision
	seedClaim(t, m, "b_medium", 0.8)                    // medium risk → left neutral (no signal)
	seedClaim(t, m, "b_control", 0.8)                   // no decision → untouched
	seedClaimWithSalience(t, m, "b_override", 0.8, 0.9) // explicit high; a low-risk decision must not lower it

	seedRiskyDecision(t, m, "d_crit", domain.RiskLevelCritical, "b_critical")
	seedRiskyDecision(t, m, "d_med", domain.RiskLevelMedium, "b_medium")
	seedRiskyDecision(t, m, "d_low", domain.RiskLevelLow, "b_override")

	// Off by default: nothing tagged.
	if res, err := m.Consolidate(ctx, ConsolidateOptions{}); err != nil {
		t.Fatalf("Consolidate (no salience): %v", err)
	} else if res.SalienceTagged != 0 {
		t.Fatalf("SalienceTagged should be 0 when AssignSalience off, got %d", res.SalienceTagged)
	}
	if _, ok := trustOf(t, m, "b_critical").Salience(); ok {
		t.Fatalf("b_critical should carry no salience before AssignSalience runs")
	}

	res, err := m.Consolidate(ctx, ConsolidateOptions{AssignSalience: true})
	if err != nil {
		t.Fatalf("Consolidate (AssignSalience): %v", err)
	}

	crit := trustOf(t, m, "b_critical")
	if v, ok := crit.Salience(); !ok || v != 1.0 {
		t.Fatalf("critical-risk belief should be tagged salience 1.0: v=%.2f ok=%v", v, ok)
	}
	if _, ok := trustOf(t, m, "b_medium").Salience(); ok {
		t.Fatalf("medium-risk belief should be left neutral (no stakes signal)")
	}
	if _, ok := trustOf(t, m, "b_control").Salience(); ok {
		t.Fatalf("belief with no decision should carry no salience")
	}
	if v, _ := trustOf(t, m, "b_override").Salience(); v != 0.9 {
		t.Fatalf("explicit override 0.9 must not be lowered by a low-risk decision, got %.2f", v)
	}
	// Only the critical belief was newly tagged (override already had it; medium/low skipped).
	if res.SalienceTagged != 1 {
		t.Fatalf("SalienceTagged = %d, want 1", res.SalienceTagged)
	}

	// Idempotent: re-running writes the same value and re-tags nothing new.
	res2, err := m.Consolidate(ctx, ConsolidateOptions{AssignSalience: true})
	if err != nil {
		t.Fatalf("Consolidate (2nd): %v", err)
	}
	if v, _ := trustOf(t, m, "b_critical").Salience(); v != 1.0 {
		t.Fatalf("salience not idempotent, got %.2f", v)
	}
	if res2.SalienceTagged != 0 {
		t.Fatalf("2nd pass should re-tag nothing (max is a fixpoint), got %d", res2.SalienceTagged)
	}
}

// TestConsolidate_SalienceRaisesReplayPriority: with two beliefs of equal
// intrinsic importance and recency, the higher-STAKES one is rehearsed first by
// prioritized replay — the consolidation bias — while ineligible (invalidated)
// beliefs are never rehearsed no matter how high their stakes (no gate bypass).
func TestConsolidate_SalienceRaisesReplayPriority(t *testing.T) {
	m := calibMem(t)
	ctx := context.Background()

	seedClaimWithSalience(t, m, "valid_high", 0.8, 0.95)
	seedClaimWithSalience(t, m, "valid_low", 0.8, 0.05)

	// A high-stakes but INVALIDATED belief: eligibility must exclude it from replay.
	now := time.Now().UTC()
	if err := m.conn.Claims.Upsert(ctx, []domain.Claim{{
		ID: "invalid_high", Text: "claim invalid_high", Type: domain.ClaimTypeFact,
		Confidence: 0.8, Status: domain.ClaimStatusActive,
		CreatedAt: now, ValidFrom: now, ValidTo: now, // invalidated
		ConfidenceComponents: map[string]float64{domain.SalienceComponentKey: 0.99},
	}}); err != nil {
		t.Fatalf("seed invalid_high: %v", err)
	}

	// Rehearse exactly one: stakes must pick the high-salience VALID belief.
	res, err := m.Consolidate(ctx, ConsolidateOptions{ReplayTopK: 1})
	if err != nil {
		t.Fatalf("Consolidate (replay): %v", err)
	}
	if res.Replayed != 1 {
		t.Fatalf("Replayed = %d, want 1", res.Replayed)
	}

	// replay bumps last_verified on the rehearsed claim only.
	if trustOf(t, m, "valid_high").LastVerified.IsZero() {
		t.Fatalf("high-stakes valid belief should have been rehearsed (last_verified bumped)")
	}
	if !trustOf(t, m, "valid_low").LastVerified.IsZero() {
		t.Fatalf("lower-stakes belief should not have been the one rehearsed with top-K=1")
	}
	// Eligibility gate holds: the invalidated high-stakes belief was never rehearsed.
	if !trustOf(t, m, "invalid_high").LastVerified.IsZero() {
		t.Fatalf("invalidated belief must not be rehearsed despite highest stakes (no gate bypass)")
	}
}
