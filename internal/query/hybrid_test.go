package query

import (
	"context"
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
)

// fakeTextSearcher returns the canned hits regardless of query, so
// tests can drive the hybrid ranker with deterministic BM25 scores.
type fakeTextSearcher struct {
	hits []ports.TextHit
}

func (f fakeTextSearcher) SearchByText(_ context.Context, _ string, _ int) ([]ports.TextHit, error) {
	return f.hits, nil
}

func TestHybridRanker_BM25OnlyOrdersByScore(t *testing.T) {
	events := []domain.Event{
		{ID: "ev_a", Content: "alpha"},
		{ID: "ev_b", Content: "beta"},
		{ID: "ev_c", Content: "gamma"},
	}
	cos := map[string]float64{} // no embeddings
	bm25 := map[string]float64{
		"ev_a": 1.5,
		"ev_b": 3.0, // best
		"ev_c": 0.5,
	}
	got := rankEventsByHybridScore(events, cos, bm25, 3)
	if got[0].ID != "ev_b" || got[1].ID != "ev_a" || got[2].ID != "ev_c" {
		t.Fatalf("BM25-only ordering wrong: %+v", got)
	}
}

func TestHybridRanker_CosineOnlyOrdersByScore(t *testing.T) {
	events := []domain.Event{
		{ID: "ev_a"},
		{ID: "ev_b"},
		{ID: "ev_c"},
	}
	cos := map[string]float64{
		"ev_a": 0.4,
		"ev_b": 0.9, // best
		"ev_c": 0.6,
	}
	got := rankEventsByHybridScore(events, cos, nil, 3)
	if got[0].ID != "ev_b" || got[1].ID != "ev_c" || got[2].ID != "ev_a" {
		t.Fatalf("cosine-only ordering wrong: %+v", got)
	}
}

func TestHybridRanker_CombinesBothSignals(t *testing.T) {
	events := []domain.Event{
		{ID: "ev_strong_lex"}, // dominates BM25
		{ID: "ev_strong_sem"}, // dominates cosine
		{ID: "ev_balanced"},   // medium on both -> wins overall
		{ID: "ev_silent"},     // no signal -> dropped
	}
	cos := map[string]float64{
		"ev_strong_sem": 1.0,
		"ev_balanced":   0.7,
		"ev_strong_lex": 0.1,
	}
	bm25 := map[string]float64{
		"ev_strong_lex": 1.0,
		"ev_balanced":   0.7,
		"ev_strong_sem": 0.1,
	}
	got := rankEventsByHybridScore(events, cos, bm25, 4)
	// silent event must not appear at all
	for _, e := range got {
		if e.ID == "ev_silent" {
			t.Fatalf("silent event should be dropped, got %+v", got)
		}
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 hits (silent dropped), got %d: %+v", len(got), got)
	}
	// balanced should beat the lopsided ones because hybrid sums
	// max-normalised scores: balanced gets 0.7+0.7=1.4, the others
	// get 0.1+1.0=1.1 each.
	if got[0].ID != "ev_balanced" {
		t.Fatalf("balanced should win, got %+v", got)
	}
}

func TestHybridRanker_NoSignalReturnsEmpty(t *testing.T) {
	got := rankEventsByHybridScore([]domain.Event{{ID: "x"}}, nil, nil, 5)
	if len(got) != 0 {
		t.Fatalf("no-signal ranking should return empty, got %+v", got)
	}
}

// End-to-end check: when the engine has a TextSearcher wired (and no
// embeddings), claim ranking should reflect the BM25 hits even
// though cosine isn't available.
func TestRankClaims_BM25Wired_OrdersByBM25(t *testing.T) {
	claims := []domain.Claim{
		{ID: "cl_x", Text: "irrelevant"},
		{ID: "cl_y", Text: "matches well"},
		{ID: "cl_z", Text: "vague"},
	}
	engine := NewEngine(fakeEventRepo{}, fakeClaimRepo{}, fakeRelationshipRepo{}).
		WithTextSearch(nil, fakeTextSearcher{hits: []ports.TextHit{
			{ID: "cl_y", Score: 5.0},
			{ID: "cl_z", Score: 1.0},
		}})

	got := engine.rankClaimsByCosine(context.Background(), "anything", claims)
	if got[0].ID != "cl_y" {
		t.Fatalf("cl_y should rank first under BM25-only, got %+v", got)
	}
	if got[1].ID != "cl_z" {
		t.Fatalf("cl_z should rank second, got %+v", got)
	}
	// cl_x has no signal — pinned at the end with the score-less
	// sentinel, but still present.
	if got[2].ID != "cl_x" {
		t.Fatalf("signal-less claim should sink to bottom, got %+v", got)
	}
}
