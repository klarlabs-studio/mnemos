package mnemos

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
)

// BrainHealth reads the claims and relationship tables once each, whatever
// helpers its vitals go through. It used to load claims five times (itself,
// Calibration twice, hypercorrectionList, liveBeliefCount): most of a 57 s
// health check at 1M beliefs.
func TestBrainHealth_ReadsEachTableOnce(t *testing.T) {
	m := calibMem(t)
	ctx := context.Background()
	seedClaim(t, m, "a", 0.8)
	seedClaim(t, m, "b", 0.8)
	// A contradiction makes hypercorrectionList need the claims too.
	if err := m.conn.Relationships.Upsert(ctx, []domain.Relationship{
		{ID: "r1", Type: domain.RelationshipTypeContradicts, FromClaimID: "a", ToClaimID: "b", CreatedAt: time.Now().UTC()},
	}); err != nil {
		t.Fatal(err)
	}
	before, err := m.BrainHealth(ctx)
	if err != nil {
		t.Fatal(err)
	}

	counts := &storeCounts{}
	// Keep the optional trust-input capability: hiding it would send health
	// down its fallback path and change the verdict being compared.
	lister, ok := m.conn.Claims.(ports.TrustInputLister)
	if !ok {
		t.Fatal("test store lost TrustInputLister")
	}
	m.conn.Claims = struct {
		countingClaims
		ports.TrustInputLister
	}{countingClaims{ClaimRepository: m.conn.Claims, c: counts}, lister}
	m.conn.Relationships = countingRels{RelationshipRepository: m.conn.Relationships, c: counts}
	after, err := m.BrainHealth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c, r := counts.claimsListAll.Load(), counts.relsListAll.Load(); c != 1 || r != 1 {
		t.Errorf("BrainHealth listed claims %d times and relationships %d times; want once each", c, r)
	}
	if before.Status != after.Status || len(before.Vitals) != len(after.Vitals) {
		t.Fatalf("wrapping the repositories changed the verdict: %v vs %v", before.Status, after.Status)
	}
	for i := range before.Vitals {
		if before.Vitals[i].Name != after.Vitals[i].Name || before.Vitals[i].Value != after.Vitals[i].Value {
			t.Errorf("vital %s: %v vs %v", before.Vitals[i].Name, before.Vitals[i].Value, after.Vitals[i].Value)
		}
	}
}
