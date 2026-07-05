package mnemos

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

func containsID(rs []Result, id string) bool {
	for _, r := range rs {
		if r.ClaimID == id {
			return true
		}
	}
	return false
}

// TestRecallWithConflicts_SurfacesContested verifies the contested frontier: a
// recalled claim contradicted by a currently-valid claim is surfaced as a
// Conflict (its live counter-evidence), and once the challenger is superseded the
// contradiction is resolved and no longer surfaces.
func TestRecallWithConflicts_SurfacesContested(t *testing.T) {
	m := calibMem(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// A matches the query. B does NOT (it's the external challenger, reached only
	// via the contradiction edge).
	aID := seedTextClaim(t, m, "the deploy caused the checkout latency spike")
	bID := seedTextClaim(t, m, "redis eviction settings and cache ttl notes")

	if err := m.conn.Relationships.Upsert(ctx, []domain.Relationship{
		{ID: "r_ab", Type: domain.RelationshipTypeContradicts, FromClaimID: aID, ToClaimID: bID, CreatedAt: now},
	}); err != nil {
		t.Fatalf("seed relationship: %v", err)
	}

	q := Query{Text: "deploy checkout latency spike"}
	results, conflicts, err := m.RecallWithConflicts(ctx, q)
	if err != nil {
		t.Fatalf("RecallWithConflicts: %v", err)
	}
	if !containsID(results, aID) {
		t.Fatalf("expected recalled claim A (%s) in results; got %v", aID, resultIDs(results))
	}
	if len(conflicts) != 1 || conflicts[0].ClaimID != aID || conflicts[0].ContradictingID != bID {
		t.Fatalf("expected exactly one conflict A<-B; got %+v", conflicts)
	}

	// Supersede the challenger → the contradiction is resolved → no conflict.
	if err := m.SetClaimLifecycle(ctx, bID, ClaimLifecycleSuperseded); err != nil {
		t.Fatalf("SetClaimLifecycle: %v", err)
	}
	_, conflicts2, err := m.RecallWithConflicts(ctx, q)
	if err != nil {
		t.Fatalf("RecallWithConflicts (post-supersede): %v", err)
	}
	if len(conflicts2) != 0 {
		t.Errorf("a superseded challenger must not surface as counter-evidence; got %+v", conflicts2)
	}
}

// TestRecallWithConflicts_NoContradictions verifies a clean recall carries no
// conflicts.
func TestRecallWithConflicts_NoContradictions(t *testing.T) {
	m := calibMem(t)
	ctx := context.Background()
	seedTextClaim(t, m, "the deploy caused the checkout latency spike")

	_, conflicts, err := m.RecallWithConflicts(ctx, Query{Text: "deploy checkout latency spike"})
	if err != nil {
		t.Fatalf("RecallWithConflicts: %v", err)
	}
	if len(conflicts) != 0 {
		t.Errorf("expected no conflicts on an uncontested recall; got %+v", conflicts)
	}
}
