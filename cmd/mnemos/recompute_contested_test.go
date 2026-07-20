package main

import (
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
)

func ev(id, run string) domain.Event { return domain.Event{ID: id, RunID: run} }
func lk(claim, event string) domain.ClaimEvidence {
	return domain.ClaimEvidence{ClaimID: claim, EventID: event}
}

// The core contract: a claim contested by the OLD loose rule, which today's
// rule would not flag, is returned for clearing.
func TestRecomputeContested_ClearsNoLongerFlagged(t *testing.T) {
	claims := []domain.Claim{
		// Unrelated bullets that the old 2-token rule contested; today's
		// same-subject anchor does not.
		{ID: "c1", Text: "Frame: nonce LE, supp-size includes size-byte and marker bytes", Status: domain.ClaimStatusContested},
		{ID: "c2", Text: "Next session should not start the M1 remainder before approval", Status: domain.ClaimStatusContested},
	}
	links := []domain.ClaimEvidence{lk("c1", "e1"), lk("c2", "e1")}
	events := []domain.Event{ev("e1", "run1")}

	cleared := recomputeContestedClaims(claims, links, events)
	if len(cleared) != 2 {
		t.Fatalf("cleared %d claims, want 2 (both are unrelated)", len(cleared))
	}
}

// A genuine contradiction must stay contested — the pass must not launder real
// disagreements into active claims.
func TestRecomputeContested_KeepsGenuineContradiction(t *testing.T) {
	claims := []domain.Claim{
		{ID: "c1", Text: "Revenue decreased after launch", Status: domain.ClaimStatusContested},
		{ID: "c2", Text: "Revenue did not decrease after launch", Status: domain.ClaimStatusContested},
	}
	links := []domain.ClaimEvidence{lk("c1", "e1"), lk("c2", "e1")}
	events := []domain.Event{ev("e1", "run1")}

	if cleared := recomputeContestedClaims(claims, links, events); len(cleared) != 0 {
		t.Errorf("cleared a genuine contradiction: %v", cleared)
	}
}

// The heuristic is pairwise WITHIN an extraction batch. Two contradicting
// claims from different runs were never compared by extraction, so re-deriving
// across the whole store would invent conflicts that never existed.
func TestRecomputeContested_RespectsBatchBoundaries(t *testing.T) {
	claims := []domain.Claim{
		{ID: "c1", Text: "Revenue decreased after launch", Status: domain.ClaimStatusContested},
		{ID: "c2", Text: "Revenue did not decrease after launch", Status: domain.ClaimStatusContested},
	}
	// Same texts, but extracted in DIFFERENT runs.
	links := []domain.ClaimEvidence{lk("c1", "e1"), lk("c2", "e2")}
	events := []domain.Event{ev("e1", "run1"), ev("e2", "run2")}

	cleared := recomputeContestedClaims(claims, links, events)
	if len(cleared) != 2 {
		t.Errorf("claims from separate batches should both clear (never compared at extraction), got %d", len(cleared))
	}
}

// Never touch a claim that isn't contested, and never add contested.
func TestRecomputeContested_OnlyTouchesContested(t *testing.T) {
	claims := []domain.Claim{
		{ID: "a1", Text: "Revenue decreased after launch", Status: domain.ClaimStatusActive},
		{ID: "a2", Text: "Revenue did not decrease after launch", Status: domain.ClaimStatusActive},
		{ID: "d1", Text: "Frame: nonce LE and marker bytes", Status: domain.ClaimStatusDeprecated},
	}
	links := []domain.ClaimEvidence{lk("a1", "e1"), lk("a2", "e1"), lk("d1", "e1")}
	events := []domain.Event{ev("e1", "run1")}

	if cleared := recomputeContestedClaims(claims, links, events); len(cleared) != 0 {
		t.Errorf("touched non-contested claims: %v", cleared)
	}
}

// A claim with no evidence link has an unknown batch; leave it alone rather
// than guess.
func TestRecomputeContested_SkipsClaimsWithoutEvidence(t *testing.T) {
	claims := []domain.Claim{{ID: "orphan", Text: "some contested thing", Status: domain.ClaimStatusContested}}
	if cleared := recomputeContestedClaims(claims, nil, nil); len(cleared) != 0 {
		t.Errorf("cleared a claim whose batch is unknown: %v", cleared)
	}
}
