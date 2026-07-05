package mnemos

import (
	"context"
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
)

func recHas(recs []Recombination, x, y string) bool {
	for _, r := range recs {
		if (r.ClaimA == x && r.ClaimB == y) || (r.ClaimA == y && r.ClaimB == x) {
			return true
		}
	}
	return false
}

// TestRecombinations_FindsUnconnectedSimilar verifies the REM detector surfaces a
// topically-similar high-salience pair that the graph never connected, and skips a
// pair the graph already links.
func TestRecombinations_FindsUnconnectedSimilar(t *testing.T) {
	m := calibMem(t)
	ctx := context.Background()

	// Three similar high-salience decisions; a–c are already connected.
	upsertTypedClaim(t, m, "a", "rollback api deploy on error spike", domain.ClaimTypeDecision)
	upsertTypedClaim(t, m, "b", "rollback api deploy on latency spike", domain.ClaimTypeDecision)
	upsertTypedClaim(t, m, "c", "rollback api deploy on memory spike", domain.ClaimTypeDecision)
	edge(t, m, "eac", "a", "c", domain.RelationshipTypeSupports)

	recs, err := m.Recombinations(ctx, 10)
	if err != nil {
		t.Fatalf("Recombinations: %v", err)
	}
	if !recHas(recs, "a", "b") {
		t.Errorf("unconnected similar pair (a,b) should be recombined; got %+v", recs)
	}
	if recHas(recs, "a", "c") {
		t.Errorf("already-connected pair (a,c) must not be recombined; got %+v", recs)
	}
}

// TestRecombinations_NoneWhenDissimilar verifies unrelated claims yield no pairs.
func TestRecombinations_NoneWhenDissimilar(t *testing.T) {
	m := calibMem(t)
	upsertTypedClaim(t, m, "a", "rollback the payments service", domain.ClaimTypeDecision)
	upsertTypedClaim(t, m, "b", "kubernetes node autoscaling threshold", domain.ClaimTypeDecision)

	recs, err := m.Recombinations(context.Background(), 10)
	if err != nil {
		t.Fatalf("Recombinations: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("dissimilar claims should not recombine; got %+v", recs)
	}
}
