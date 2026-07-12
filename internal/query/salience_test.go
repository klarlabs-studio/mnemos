package query

import (
	"context"
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
)

func salienceClaim(id string, s float64) domain.Claim {
	return domain.Claim{
		ID:                   id,
		Text:                 "equal",
		ConfidenceComponents: map[string]float64{domain.SalienceComponentKey: s},
	}
}

// A high-salience belief outranks a low-salience one of EQUAL similarity when the
// bias is on; with the bias off the ranking is unchanged (input order preserved on
// the tie).
func TestRankClaims_SalienceBiasTipsEqualSimilarity(t *testing.T) {
	low := salienceClaim("cl_low", 0.1)
	high := salienceClaim("cl_high", 0.95)
	claims := []domain.Claim{low, high} // low first in input order

	// Both claims get an identical BM25 score → equal similarity.
	engine := NewEngine(fakeEventRepo{}, fakeClaimRepo{}, fakeRelationshipRepo{}).
		WithTextSearch(nil, fakeTextSearcher{hits: []ports.TextHit{
			{ID: "cl_low", Score: 5.0},
			{ID: "cl_high", Score: 5.0},
		}})
	ctx := context.Background()

	// Bias OFF: equal score → tie broken by input order, low stays first.
	off := engine.rankClaimsByHybrid(ctx, "anything", claims, false)
	if off[0].ID != "cl_low" {
		t.Fatalf("bias off should preserve input order on a tie, got %+v", off)
	}

	// Bias ON: the high-stakes belief is preferentially recalled.
	on := engine.rankClaimsByHybrid(ctx, "anything", claims, true)
	if on[0].ID != "cl_high" {
		t.Fatalf("high-salience belief should outrank equal-similarity low-salience one, got %+v", on)
	}
}

// The stakes term is bounded: it tips ties, but a materially better similarity
// match still wins even against a much higher-stakes weak match.
func TestRankClaims_SalienceCannotOvertakeStrongMatch(t *testing.T) {
	weakButHighStakes := salienceClaim("cl_weak", 1.0)
	strongButLowStakes := salienceClaim("cl_strong", 0.0)
	claims := []domain.Claim{weakButHighStakes, strongButLowStakes}

	engine := NewEngine(fakeEventRepo{}, fakeClaimRepo{}, fakeRelationshipRepo{}).
		WithTextSearch(nil, fakeTextSearcher{hits: []ports.TextHit{
			{ID: "cl_strong", Score: 5.0}, // dominant similarity
			{ID: "cl_weak", Score: 1.0},   // weak similarity
		}})

	on := engine.rankClaimsByHybrid(context.Background(), "anything", claims, true)
	if on[0].ID != "cl_strong" {
		t.Fatalf("a materially better match must still win despite lower stakes, got %+v", on)
	}
}

// Neutral / unmarked claims contribute a zero stakes term, so enabling the bias
// over a corpus that never set salience leaves the ranking unchanged.
func TestRankClaims_NeutralSalienceIsInert(t *testing.T) {
	a := domain.Claim{ID: "cl_a", Text: "x"} // no salience component → neutral
	b := domain.Claim{ID: "cl_b", Text: "x"}
	claims := []domain.Claim{a, b}
	engine := NewEngine(fakeEventRepo{}, fakeClaimRepo{}, fakeRelationshipRepo{}).
		WithTextSearch(nil, fakeTextSearcher{hits: []ports.TextHit{
			{ID: "cl_a", Score: 5.0},
			{ID: "cl_b", Score: 5.0},
		}})
	ctx := context.Background()
	off := engine.rankClaimsByHybrid(ctx, "anything", claims, false)
	on := engine.rankClaimsByHybrid(ctx, "anything", claims, true)
	if off[0].ID != on[0].ID || off[1].ID != on[1].ID {
		t.Fatalf("neutral salience should not change ranking: off=%+v on=%+v", off, on)
	}
}
