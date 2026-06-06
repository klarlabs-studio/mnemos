package relate

import (
	"strconv"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

func TestDetectFindsSupportAndContradiction(t *testing.T) {
	engine := Engine{
		now: func() time.Time {
			return time.Date(2026, 4, 12, 15, 0, 0, 0, time.UTC)
		},
		nextID: seqRelationshipIDs(),
	}

	claims := []domain.Claim{
		{ID: "cl_1", Text: "Revenue increased in Q2"},
		{ID: "cl_2", Text: "Revenue increased in Q2 after release"},
		{ID: "cl_3", Text: "Revenue did not increase in Q2"},
	}

	rels, err := engine.Detect(claims)
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if len(rels) == 0 {
		t.Fatal("Detect() expected relationships, got none")
	}

	hasSupport := false
	hasContradiction := false
	for _, rel := range rels {
		if rel.Type == domain.RelationshipTypeSupports {
			hasSupport = true
		}
		if rel.Type == domain.RelationshipTypeContradicts {
			hasContradiction = true
		}
	}

	if !hasSupport {
		t.Fatal("Detect() expected at least one supports relationship")
	}
	if !hasContradiction {
		t.Fatal("Detect() expected at least one contradicts relationship")
	}
}

func TestDetectIgnoresUnrelatedClaims(t *testing.T) {
	engine := Engine{
		now: func() time.Time {
			return time.Date(2026, 4, 12, 15, 0, 0, 0, time.UTC)
		},
		nextID: seqRelationshipIDs(),
	}

	claims := []domain.Claim{
		{ID: "cl_1", Text: "Revenue increased in Q2"},
		{ID: "cl_2", Text: "The team increased headcount"},
	}

	rels, err := engine.Detect(claims)
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}

	// "Revenue increased in Q2" and "The team increased headcount" share
	// "increased" after stop-word filtering. But overlap ratio is below
	// threshold since each has multiple unique content tokens.
	// With content tokens: {revenue, increas, q2} vs {team, increas, headcount}
	// Overlap = 1 (increas), which is below minContentTokenOverlap of 2.
	if len(rels) != 0 {
		t.Fatalf("Detect() expected 0 relationships for unrelated claims, got %d", len(rels))
	}
}

func TestDetectContractionNegation(t *testing.T) {
	engine := Engine{
		now: func() time.Time {
			return time.Date(2026, 4, 12, 15, 0, 0, 0, time.UTC)
		},
		nextID: seqRelationshipIDs(),
	}

	claims := []domain.Claim{
		{ID: "cl_1", Text: "The service deployment succeeded in production"},
		{ID: "cl_2", Text: "The service deployment didn't succeed in production"},
	}

	rels, err := engine.Detect(claims)
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}

	hasContradiction := false
	for _, rel := range rels {
		if rel.Type == domain.RelationshipTypeContradicts {
			hasContradiction = true
		}
	}
	if !hasContradiction {
		t.Fatal("Detect() expected contradiction for contraction negation (didn't)")
	}
}

func TestDetectIncrementalFindsRelationships(t *testing.T) {
	engine := Engine{
		now: func() time.Time {
			return time.Date(2026, 4, 12, 15, 0, 0, 0, time.UTC)
		},
		nextID: seqRelationshipIDs(),
	}

	existing := []domain.Claim{
		{ID: "cl_old_1", Text: "Revenue increased in Q2"},
	}

	newClaims := []domain.Claim{
		{ID: "cl_new_1", Text: "Revenue increased in Q2 after the product launch"},
		{ID: "cl_new_2", Text: "Revenue did not increase in Q2"},
	}

	rels, err := engine.DetectIncremental(newClaims, existing)
	if err != nil {
		t.Fatalf("DetectIncremental() error = %v", err)
	}
	if len(rels) == 0 {
		t.Fatal("DetectIncremental() expected relationships, got none")
	}

	hasSupport := false
	hasContradiction := false
	for _, rel := range rels {
		// All relationships should be from new claims to existing claims.
		if rel.FromClaimID != "cl_new_1" && rel.FromClaimID != "cl_new_2" {
			t.Fatalf("DetectIncremental() unexpected FromClaimID %q, expected a new claim", rel.FromClaimID)
		}
		if rel.ToClaimID != "cl_old_1" {
			t.Fatalf("DetectIncremental() unexpected ToClaimID %q, expected cl_old_1", rel.ToClaimID)
		}
		if rel.Type == domain.RelationshipTypeSupports {
			hasSupport = true
		}
		if rel.Type == domain.RelationshipTypeContradicts {
			hasContradiction = true
		}
	}

	if !hasSupport {
		t.Fatal("DetectIncremental() expected at least one supports relationship")
	}
	if !hasContradiction {
		t.Fatal("DetectIncremental() expected at least one contradicts relationship")
	}
}

func TestDetectIncrementalDoesNotCompareExistingPairs(t *testing.T) {
	engine := Engine{
		now: func() time.Time {
			return time.Date(2026, 4, 12, 15, 0, 0, 0, time.UTC)
		},
		nextID: seqRelationshipIDs(),
	}

	existing := []domain.Claim{
		{ID: "cl_old_1", Text: "Revenue increased in Q2"},
		{ID: "cl_old_2", Text: "Revenue increased in Q2 significantly"},
	}

	// New claim is unrelated to existing claims.
	newClaims := []domain.Claim{
		{ID: "cl_new_1", Text: "The weather was sunny today"},
	}

	rels, err := engine.DetectIncremental(newClaims, existing)
	if err != nil {
		t.Fatalf("DetectIncremental() error = %v", err)
	}
	// The two existing claims are related to each other, but DetectIncremental
	// should NOT detect that — only new vs existing comparisons.
	if len(rels) != 0 {
		t.Fatalf("DetectIncremental() expected 0 relationships (only new vs existing), got %d", len(rels))
	}
}

func TestDetectIncrementalEmptyInputs(t *testing.T) {
	engine := Engine{
		now: func() time.Time {
			return time.Date(2026, 4, 12, 15, 0, 0, 0, time.UTC)
		},
		nextID: seqRelationshipIDs(),
	}

	// Empty new claims.
	rels, err := engine.DetectIncremental(nil, []domain.Claim{{ID: "cl_1", Text: "test"}})
	if err != nil {
		t.Fatalf("DetectIncremental() error = %v", err)
	}
	if rels != nil {
		t.Fatalf("DetectIncremental() with empty newClaims expected nil, got %v", rels)
	}

	// Empty existing claims.
	rels, err = engine.DetectIncremental([]domain.Claim{{ID: "cl_1", Text: "test"}}, nil)
	if err != nil {
		t.Fatalf("DetectIncremental() error = %v", err)
	}
	if rels != nil {
		t.Fatalf("DetectIncremental() with empty existingClaims expected nil, got %v", rels)
	}
}

func TestDetectBuildsCitationEdgesFromClaimIDReferences(t *testing.T) {
	engine := Engine{
		now: func() time.Time {
			return time.Date(2026, 4, 12, 15, 0, 0, 0, time.UTC)
		},
		nextID: seqRelationshipIDs(),
	}

	claims := []domain.Claim{
		{ID: "cl_1", Text: "Primary source"},
		{ID: "cl_2", Text: "Corroborates cl_1 and references cl_1 again"},
		{ID: "cl_3", Text: "Unrelated"},
	}

	rels, err := engine.Detect(claims)
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}

	citationCount := 0
	for _, rel := range rels {
		if rel.Type == domain.RelationshipTypeCites {
			citationCount++
			if rel.FromClaimID != "cl_2" || rel.ToClaimID != "cl_1" {
				t.Fatalf("unexpected cites edge %s -> %s", rel.FromClaimID, rel.ToClaimID)
			}
		}
	}
	if citationCount != 1 {
		t.Fatalf("expected exactly 1 cites relationship, got %d", citationCount)
	}
}

func TestDetectIncrementalBuildsCitationEdgesFromNewClaims(t *testing.T) {
	engine := Engine{
		now: func() time.Time {
			return time.Date(2026, 4, 12, 15, 0, 0, 0, time.UTC)
		},
		nextID: seqRelationshipIDs(),
	}

	existing := []domain.Claim{{ID: "cl_old_1", Text: "Existing"}}
	newClaims := []domain.Claim{{ID: "cl_new_1", Text: "See cl_old_1 for evidence"}}

	rels, err := engine.DetectIncremental(newClaims, existing)
	if err != nil {
		t.Fatalf("DetectIncremental() error = %v", err)
	}

	hasCitation := false
	for _, rel := range rels {
		if rel.Type == domain.RelationshipTypeCites && rel.FromClaimID == "cl_new_1" && rel.ToClaimID == "cl_old_1" {
			hasCitation = true
		}
	}
	if !hasCitation {
		t.Fatalf("expected DetectIncremental to emit cites edge cl_new_1 -> cl_old_1, got %+v", rels)
	}
}

func TestDetectTestConflicts_ConflictingResults(t *testing.T) {
	engine := Engine{
		now:    func() time.Time { return time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC) },
		nextID: seqRelationshipIDs(),
	}

	claims := []domain.Claim{
		{
			ID:                 "cl_t1",
			Type:               domain.ClaimTypeTestResult,
			Text:               "Login flow test passed",
			TestRequirementRef: "REQ-AUTH-001",
			TestPassCount:      5,
			TestFailCount:      0,
		},
		{
			ID:                 "cl_t2",
			Type:               domain.ClaimTypeTestResult,
			Text:               "Login flow test failed",
			TestRequirementRef: "REQ-AUTH-001",
			TestPassCount:      0,
			TestFailCount:      3,
		},
	}

	rels, err := engine.DetectTestConflicts(claims)
	if err != nil {
		t.Fatalf("DetectTestConflicts() error = %v", err)
	}
	if len(rels) != 1 {
		t.Fatalf("expected 1 relationship, got %d: %+v", len(rels), rels)
	}
	if rels[0].Type != domain.RelationshipTypeContradicts {
		t.Fatalf("expected contradicts, got %q", rels[0].Type)
	}
	if rels[0].FromClaimID != "cl_t1" || rels[0].ToClaimID != "cl_t2" {
		t.Fatalf("unexpected edge %s -> %s", rels[0].FromClaimID, rels[0].ToClaimID)
	}
}

func TestDetectTestConflicts_AgreeingResults(t *testing.T) {
	engine := Engine{
		now:    func() time.Time { return time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC) },
		nextID: seqRelationshipIDs(),
	}

	claims := []domain.Claim{
		{
			ID:                 "cl_t1",
			Type:               domain.ClaimTypeTestResult,
			Text:               "Checkout flow passes",
			TestRequirementRef: "REQ-CHECKOUT-002",
			TestPassCount:      10,
			TestFailCount:      1,
		},
		{
			ID:                 "cl_t2",
			Type:               domain.ClaimTypeTestResult,
			Text:               "Checkout flow also passes",
			TestRequirementRef: "REQ-CHECKOUT-002",
			TestPassCount:      8,
			TestFailCount:      0,
		},
	}

	rels, err := engine.DetectTestConflicts(claims)
	if err != nil {
		t.Fatalf("DetectTestConflicts() error = %v", err)
	}
	if len(rels) != 1 {
		t.Fatalf("expected 1 relationship, got %d", len(rels))
	}
	if rels[0].Type != domain.RelationshipTypeSupports {
		t.Fatalf("expected supports, got %q", rels[0].Type)
	}
}

func TestDetectTestConflicts_DifferentRequirementsNoRelationship(t *testing.T) {
	engine := Engine{
		now:    func() time.Time { return time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC) },
		nextID: seqRelationshipIDs(),
	}

	claims := []domain.Claim{
		{
			ID:                 "cl_t1",
			Type:               domain.ClaimTypeTestResult,
			Text:               "Login test passed",
			TestRequirementRef: "REQ-AUTH-001",
			TestPassCount:      5,
			TestFailCount:      0,
		},
		{
			ID:                 "cl_t2",
			Type:               domain.ClaimTypeTestResult,
			Text:               "Checkout test failed",
			TestRequirementRef: "REQ-CHECKOUT-002",
			TestPassCount:      0,
			TestFailCount:      3,
		},
	}

	rels, err := engine.DetectTestConflicts(claims)
	if err != nil {
		t.Fatalf("DetectTestConflicts() error = %v", err)
	}
	if len(rels) != 0 {
		t.Fatalf("expected 0 relationships for different requirements, got %d", len(rels))
	}
}

func TestDetectTestConflicts_DifferentScopesNoRelationship(t *testing.T) {
	engine := Engine{
		now:    func() time.Time { return time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC) },
		nextID: seqRelationshipIDs(),
	}

	claims := []domain.Claim{
		{
			ID:                 "cl_t1",
			Type:               domain.ClaimTypeTestResult,
			Text:               "Auth test passed in staging",
			TestRequirementRef: "REQ-AUTH-001",
			Scope:              domain.Scope{Env: "staging"},
			TestPassCount:      5,
			TestFailCount:      0,
		},
		{
			ID:                 "cl_t2",
			Type:               domain.ClaimTypeTestResult,
			Text:               "Auth test failed in prod",
			TestRequirementRef: "REQ-AUTH-001",
			Scope:              domain.Scope{Env: "prod"},
			TestPassCount:      0,
			TestFailCount:      2,
		},
	}

	rels, err := engine.DetectTestConflicts(claims)
	if err != nil {
		t.Fatalf("DetectTestConflicts() error = %v", err)
	}
	if len(rels) != 0 {
		t.Fatalf("expected 0 relationships for different scopes, got %d", len(rels))
	}
}

func TestDetectTestConflicts_IndeterminateNoRelationship(t *testing.T) {
	engine := Engine{
		now:    func() time.Time { return time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC) },
		nextID: seqRelationshipIDs(),
	}

	// Both have zero counts — indeterminate, no relationship.
	claims := []domain.Claim{
		{
			ID:                 "cl_t1",
			Type:               domain.ClaimTypeTestResult,
			Text:               "Auth test recorded",
			TestRequirementRef: "REQ-AUTH-001",
			TestPassCount:      0,
			TestFailCount:      0,
		},
		{
			ID:                 "cl_t2",
			Type:               domain.ClaimTypeTestResult,
			Text:               "Auth test also recorded",
			TestRequirementRef: "REQ-AUTH-001",
			TestPassCount:      0,
			TestFailCount:      0,
		},
	}

	rels, err := engine.DetectTestConflicts(claims)
	if err != nil {
		t.Fatalf("DetectTestConflicts() error = %v", err)
	}
	if len(rels) != 0 {
		t.Fatalf("expected 0 relationships for indeterminate counts, got %d", len(rels))
	}
}

func TestDetect_WiresTestConflicts(t *testing.T) {
	engine := Engine{
		now:    func() time.Time { return time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC) },
		nextID: seqRelationshipIDs(),
	}

	// A mix: one conflicting test pair + one unrelated fact claim.
	claims := []domain.Claim{
		{
			ID:                 "cl_t1",
			Type:               domain.ClaimTypeTestResult,
			Text:               "payment integration passes",
			TestRequirementRef: "REQ-PAY-001",
			TestPassCount:      4,
			TestFailCount:      0,
		},
		{
			ID:                 "cl_t2",
			Type:               domain.ClaimTypeTestResult,
			Text:               "payment integration fails",
			TestRequirementRef: "REQ-PAY-001",
			TestPassCount:      0,
			TestFailCount:      2,
		},
		{ID: "cl_f1", Type: domain.ClaimTypeFact, Text: "Deployment completed on Monday"},
	}

	rels, err := engine.Detect(claims)
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}

	var found bool
	for _, r := range rels {
		if r.Type == domain.RelationshipTypeContradicts &&
			((r.FromClaimID == "cl_t1" && r.ToClaimID == "cl_t2") ||
				(r.FromClaimID == "cl_t2" && r.ToClaimID == "cl_t1")) {
			found = true
		}
	}
	if !found {
		t.Fatalf("Detect() expected a contradicts edge between cl_t1 and cl_t2, got: %+v", rels)
	}
}

func seqRelationshipIDs() func() (string, error) {
	i := 0
	return func() (string, error) {
		id := "rl_test_" + strconv.Itoa(i)
		i++
		return id, nil
	}
}
