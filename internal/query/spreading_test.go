package query

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// primingFixture builds a deterministic 8-claim knowledge base with no
// embedding/text signal wired, so the direct ranking is exactly the insertion
// order c0..c7 (rankClaimsByCosine is a no-op without signal and the question
// carries no intent keyword). Callers add relationship edges to exercise
// spreading activation. The single event's content matches the question so the
// BM25 event ranker selects it and every claim reaches the answer set.
func primingFixture(edges map[string][]domain.Relationship) (Engine, string) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	const question = "alpha widget beta gamma"

	events := fakeEventRepo{events: []domain.Event{
		{ID: "ev_1", RunID: "run_1", Content: "alpha widget beta gamma delta", Timestamp: now},
	}}

	claims := make([]domain.Claim, 0, 8)
	for i := 0; i < 8; i++ {
		claims = append(claims, domain.Claim{
			ID:         claimID(i),
			Text:       claimID(i) + " content",
			Type:       domain.ClaimTypeFact,
			Status:     domain.ClaimStatusActive,
			Confidence: 0.8,
			CreatedAt:  now,
		})
	}

	rels := fakeRelationshipRepo{rels: edges}
	engine := NewEngine(events, fakeClaimRepo{claims: claims}, rels)
	return engine, question
}

func claimID(i int) string {
	return "c" + string(rune('0'+i))
}

// edge builds a relationship indexed under both endpoints, matching the store
// convention that ListByClaimIDs finds an edge from either side.
func edge(id string, t domain.RelationshipType, from, to string, dst map[string][]domain.Relationship) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	r := domain.Relationship{ID: id, Type: t, FromClaimID: from, ToClaimID: to, CreatedAt: now}
	dst[from] = append(dst[from], r)
	dst[to] = append(dst[to], r)
}

func indexOf(claims []domain.Claim, id string) int {
	for i, c := range claims {
		if c.ID == id {
			return i
		}
	}
	return -1
}

// TestSpreadingActivation_BoostsAssociatedBelief verifies that a belief which
// ranks low on direct similarity but is strongly associated (supports edges)
// with the top hits is primed up the results — and that with priming disabled
// the ranking is exactly the pre-existing order.
func TestSpreadingActivation_BoostsAssociatedBelief(t *testing.T) {
	edges := map[string][]domain.Relationship{}
	// c6 (rank 6, a weak direct match) is strongly associated with the two
	// strongest direct hits c0 and c1.
	edge("r_0_6", domain.RelationshipTypeSupports, "c0", "c6", edges)
	edge("r_1_6", domain.RelationshipTypeSupports, "c1", "c6", edges)

	engine, question := primingFixture(edges)

	// Baseline: priming OFF → ranking unchanged from today (insertion order).
	base, err := engine.AnswerWithOptions(context.Background(), question, AnswerOptions{})
	if err != nil {
		t.Fatalf("baseline AnswerWithOptions error = %v", err)
	}
	for i := 0; i < 8; i++ {
		if got := base.Claims[i].ID; got != claimID(i) {
			t.Fatalf("baseline order at %d = %q, want %q (priming-off must not reorder)", i, got, claimID(i))
		}
	}
	baseC6 := indexOf(base.Claims, "c6")

	// Priming ON → c6 is boosted above lower-associated neighbours.
	primed, err := engine.AnswerWithOptions(context.Background(), question, AnswerOptions{Prime: true})
	if err != nil {
		t.Fatalf("primed AnswerWithOptions error = %v", err)
	}
	if len(primed.Claims) != 8 {
		t.Fatalf("primed claim count = %d, want 8 (priming must not add/drop claims)", len(primed.Claims))
	}
	primedC6 := indexOf(primed.Claims, "c6")
	if primedC6 >= baseC6 {
		t.Fatalf("c6 index with priming = %d, was %d without — expected a boost up the ranking", primedC6, baseC6)
	}
	// It should now outrank c5 and c4 (unassociated, originally ranked above it)…
	if primedC6 >= indexOf(primed.Claims, "c5") {
		t.Fatalf("c6 (%d) should be primed above c5 (%d)", primedC6, indexOf(primed.Claims, "c5"))
	}
	if primedC6 >= indexOf(primed.Claims, "c4") {
		t.Fatalf("c6 (%d) should be primed above c4 (%d)", primedC6, indexOf(primed.Claims, "c4"))
	}
	// …but the strong direct hit c0 must remain #1.
	if primed.Claims[0].ID != "c0" {
		t.Fatalf("top claim with priming = %q, want c0 (strong direct match keeps the lead)", primed.Claims[0].ID)
	}
}

// TestSpreadingActivation_BoundedBelowStrongMatch verifies the bound: even the
// weakest direct match, associated as strongly as possible (a supports edge to
// every one of the top-K seeds, saturating the activation cap), cannot outrank
// the strong #1 direct hit on association alone.
func TestSpreadingActivation_BoundedBelowStrongMatch(t *testing.T) {
	edges := map[string][]domain.Relationship{}
	// c7 is the weakest direct match (last), yet maximally associated: a
	// supports edge from every seed (c0..c4).
	for i := 0; i < 5; i++ {
		edge("r_"+claimID(i)+"_7", domain.RelationshipTypeSupports, claimID(i), "c7", edges)
	}

	engine, question := primingFixture(edges)

	primed, err := engine.AnswerWithOptions(context.Background(), question, AnswerOptions{Prime: true})
	if err != nil {
		t.Fatalf("primed AnswerWithOptions error = %v", err)
	}
	if primed.Claims[0].ID != "c0" {
		t.Fatalf("top claim = %q, want c0 — a weak direct match must not outrank a strong one via association", primed.Claims[0].ID)
	}
	// c7 got a real boost (it climbs above some unassociated neighbours) but
	// stays well below the top tier — the cap keeps priming an augmentation.
	c7 := indexOf(primed.Claims, "c7")
	if c7 == 0 {
		t.Fatal("c7 must never reach #1 on association alone")
	}
}

// TestSpreadingActivation_NoEdgesIsNoOp verifies that with priming enabled but
// no relationship edges, the ranking is identical to priming-off (there is
// nothing to spread, so the associative term is zero for every claim).
func TestSpreadingActivation_NoEdgesIsNoOp(t *testing.T) {
	engine, question := primingFixture(map[string][]domain.Relationship{})

	primed, err := engine.AnswerWithOptions(context.Background(), question, AnswerOptions{Prime: true})
	if err != nil {
		t.Fatalf("primed AnswerWithOptions error = %v", err)
	}
	for i := 0; i < 8; i++ {
		if got := primed.Claims[i].ID; got != claimID(i) {
			t.Fatalf("order at %d = %q, want %q (no edges → priming is a no-op)", i, got, claimID(i))
		}
	}
}

// TestEdgeActivationWeight_Ordering pins the edge-type weighting: corroborating
// edges prime most strongly, the causal/provenance family moderately, and
// contradiction edges least (but still non-zero).
func TestEdgeActivationWeight_Ordering(t *testing.T) {
	supports := edgeActivationWeight(domain.RelationshipTypeSupports)
	causal := edgeActivationWeight(domain.RelationshipTypeCauses)
	contradicts := edgeActivationWeight(domain.RelationshipTypeContradicts)
	other := edgeActivationWeight(domain.RelationshipType("some_future_kind"))

	if !(supports > causal && causal > other && other > contradicts) {
		t.Fatalf("edge weight ordering wrong: supports=%.2f causal=%.2f other=%.2f contradicts=%.2f",
			supports, causal, other, contradicts)
	}
	if contradicts <= 0 {
		t.Fatalf("contradiction edges should still prime (weight > 0), got %.2f", contradicts)
	}
}
