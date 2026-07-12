package query

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// hebbianFixture is primingFixture with a recorder wired onto the relationship repo
// so a test can observe the Hebbian write-back. Returns the engine, the question, the
// live edge map, and the strengthen-call recorder.
func hebbianFixture(edges map[string][]domain.Relationship) (Engine, string, fakeRelationshipRepo, *[][]string) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	const question = "alpha widget beta gamma"
	events := fakeEventRepo{events: []domain.Event{
		{ID: "ev_1", RunID: "run_1", Content: "alpha widget beta gamma delta", Timestamp: now},
	}}
	claims := make([]domain.Claim, 0, 8)
	for i := 0; i < 8; i++ {
		claims = append(claims, domain.Claim{
			ID: claimID(i), Text: claimID(i) + " content", Type: domain.ClaimTypeFact,
			Status: domain.ClaimStatusActive, Confidence: 0.8, CreatedAt: now,
		})
	}
	recorder := &[][]string{}
	rels := fakeRelationshipRepo{rels: edges, strengthenedWith: recorder}
	engine := NewEngine(events, fakeClaimRepo{claims: claims}, rels)
	return engine, question, rels, recorder
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// TestHebbianCoactivation_OffByDefault verifies the write-back is a strict opt-in:
// a plain query never strengthens.
func TestHebbianCoactivation_OffByDefault(t *testing.T) {
	edges := map[string][]domain.Relationship{}
	edge("r_0_1", domain.RelationshipTypeSupports, "c0", "c1", edges)
	engine, question, _, recorder := hebbianFixture(edges)

	if _, err := engine.AnswerWithOptions(question, AnswerOptions{}); err != nil {
		t.Fatalf("AnswerWithOptions: %v", err)
	}
	if len(*recorder) != 0 {
		t.Fatalf("Hebbian off must not strengthen, got %d calls", len(*recorder))
	}
}

// TestHebbianCoactivation_StrengthensCoRetrievedEdges verifies that with --hebbian the
// existing edge among co-retrieved beliefs is strengthened exactly once, with the
// co-retrieved set, and the edge's stored strength is incremented from the base 1.0.
func TestHebbianCoactivation_StrengthensCoRetrievedEdges(t *testing.T) {
	edges := map[string][]domain.Relationship{}
	edge("r_0_1", domain.RelationshipTypeSupports, "c0", "c1", edges)
	engine, question, rels, recorder := hebbianFixture(edges)

	if _, err := engine.AnswerWithOptions(question, AnswerOptions{Hebbian: true}); err != nil {
		t.Fatalf("AnswerWithOptions: %v", err)
	}
	if len(*recorder) != 1 {
		t.Fatalf("Hebbian on should strengthen once, got %d calls", len(*recorder))
	}
	set := (*recorder)[0]
	if !contains(set, "c0") || !contains(set, "c1") {
		t.Errorf("strengthen set %v should include the co-retrieved c0 and c1", set)
	}

	// The stored edge strength rose from the base 1.0 to 1+delta.
	after, err := rels.ListByClaimIDs(context.Background(), []string{"c0"})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, r := range after {
		if r.ID == "r_0_1" {
			found = true
			if r.EffectiveStrength() <= 1 {
				t.Errorf("co-retrieved edge should be strengthened above base, got %v", r.EffectiveStrength())
			}
		}
	}
	if !found {
		t.Fatal("edge r_0_1 not found after strengthening")
	}
}
