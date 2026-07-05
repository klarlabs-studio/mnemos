package mnemos

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// TestKnowledgeGaps_DetectsWeakSpots verifies the curiosity detector: an
// unresolved hypothesis and a contested claim surface as gaps; a hypothesis that
// already has a validates verdict does not.
func TestKnowledgeGaps_DetectsWeakSpots(t *testing.T) {
	m := calibMem(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Unresolved hypothesis → gap.
	upsertTypedClaim(t, m, "h1", "maybe redis eviction causes the stall", domain.ClaimTypeHypothesis)
	// Resolved hypothesis (carries a validates verdict) → NOT a gap.
	upsertTypedClaim(t, m, "h2", "the deploy caused the incident", domain.ClaimTypeHypothesis)
	adjudicate(t, m, "v_h2", "h2", true, now)

	// A contested fact: c1 is contradicted by two others → gap.
	upsertTypedClaim(t, m, "c1", "service A owns billing", domain.ClaimTypeFact)
	upsertTypedClaim(t, m, "c2", "service B owns billing", domain.ClaimTypeFact)
	upsertTypedClaim(t, m, "c3", "service C owns billing", domain.ClaimTypeFact)
	edge(t, m, "e1", "c1", "c2", domain.RelationshipTypeContradicts)
	edge(t, m, "e2", "c1", "c3", domain.RelationshipTypeContradicts)

	gaps, err := m.KnowledgeGaps(ctx, 10)
	if err != nil {
		t.Fatalf("KnowledgeGaps: %v", err)
	}
	kind := map[string]string{}
	for _, g := range gaps {
		kind[g.ClaimID] = g.Kind
	}
	if kind["h1"] != GapUnresolvedHypothesis {
		t.Errorf("h1 (unresolved hypothesis) should be a gap; got kinds %v", kind)
	}
	if _, ok := kind["h2"]; ok {
		t.Errorf("h2 (resolved hypothesis) must not be a gap; got %v", kind)
	}
	if kind["c1"] != GapContested {
		t.Errorf("c1 (contradicted twice) should be a contested gap; got %v", kind)
	}
	// c2/c3 have only one contradiction each — below the threshold.
	if _, ok := kind["c2"]; ok {
		t.Errorf("c2 (one contradiction) should be below the contested threshold; got %v", kind)
	}
}

// TestKnowledgeGaps_CleanStore verifies a store with no weak spots yields none.
func TestKnowledgeGaps_CleanStore(t *testing.T) {
	m := calibMem(t)
	upsertTypedClaim(t, m, "f1", "the api runs in eu-west-1", domain.ClaimTypeFact)

	gaps, err := m.KnowledgeGaps(context.Background(), 10)
	if err != nil {
		t.Fatalf("KnowledgeGaps: %v", err)
	}
	if len(gaps) != 0 {
		t.Errorf("a clean store should have no gaps; got %+v", gaps)
	}
}
