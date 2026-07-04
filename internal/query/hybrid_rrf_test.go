package query

import (
	"context"
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/ports"
)

// fakeTextSearcher (the sparse/full-text recall leg) is defined in hybrid_test.go.

func idsOf(events []domain.Event) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.ID
	}
	return out
}

func eqStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestFuseRRF verifies the fusion math: an id that ranks in BOTH legs outscores
// one that only tops a single leg, ranks read from each list are 1-based, and
// the fusion consumes only ranks (never scores).
func TestFuseRRF(t *testing.T) {
	dense := []string{"e1", "e2"}  // e1 rank1, e2 rank2
	sparse := []string{"e2", "e3"} // e2 rank1, e3 rank2

	// e2 = 1/(60+2) + 1/(60+1) = highest (appears in both).
	// e1 = 1/(60+1) ≈ 0.016393 > e3 = 1/(60+2) ≈ 0.016129.
	got := fuseRRF([][]string{dense, sparse})
	want := []string{"e2", "e1", "e3"}
	if !eqStrings(got, want) {
		t.Fatalf("fuseRRF = %v, want %v", got, want)
	}

	// A single ranker returns its own order unchanged.
	if solo := fuseRRF([][]string{dense}); !eqStrings(solo, dense) {
		t.Fatalf("fuseRRF single = %v, want %v", solo, dense)
	}
	// Empty input yields empty output (no panic).
	if empty := fuseRRF(nil); len(empty) != 0 {
		t.Fatalf("fuseRRF nil = %v, want empty", empty)
	}
}

// TestEventsByHybrid_PullsInLexicalOnlyHit is the core R1 guarantee: an event
// the dense (cosine) leg misses entirely but the sparse (full-text) leg finds
// still lands in the candidate set, fused by RRF. Without the sparse leg it
// would never be recalled — exactly the exact-token weakness R1 fixes.
func TestEventsByHybrid_PullsInLexicalOnlyHit(t *testing.T) {
	var searchCalls, byIDsCalls int
	e1 := domain.Event{ID: "e1", Content: "payments latency spike"}
	e2 := domain.Event{ID: "e2", Content: "deploy abc123 rolled out"}
	e3 := domain.Event{ID: "e3", Content: "commit abc123 reverted"} // only FTS finds this
	events := spyEventRepo{
		byID:         map[string]domain.Event{"e1": e1, "e2": e2, "e3": e3},
		all:          []domain.Event{e1, e2, e3},
		listAllCount: new(int),
		byIDsCount:   &byIDsCalls,
	}
	dense := spyVectorRepo{
		hits:   []ports.EventSimilarityHit{{EventID: "e1", Similarity: 0.9}, {EventID: "e2", Similarity: 0.7}},
		called: &searchCalls,
	}
	sparse := fakeTextSearcher{
		hits: []ports.TextHit{{ID: "e2", Score: 5}, {ID: "e3", Score: 4}},
	}
	engine := NewEngine(events, fakeClaimRepo{}, fakeRelationshipRepo{rels: map[string][]domain.Relationship{}}).
		WithEmbeddings(dense, fakeEmbedClient{}).
		WithTextSearch(sparse, nil)

	ranked, ok := engine.eventsByHybrid(context.Background(), "abc123 revert")
	if !ok {
		t.Fatal("eventsByHybrid returned ok=false; expected the fast-path to serve")
	}
	// Fused order: e2 (both legs) > e1 (dense only) > e3 (sparse only).
	if got := idsOf(ranked); !eqStrings(got, []string{"e2", "e1", "e3"}) {
		t.Fatalf("fused recall order = %v, want [e2 e1 e3]", got)
	}
}

// TestEventsByHybrid_NoTextSearcher_PureDense proves the path degrades exactly
// to the old vector-only fast-path when no full-text leg is wired: dense order,
// no fusion, no regression.
func TestEventsByHybrid_NoTextSearcher_PureDense(t *testing.T) {
	var searchCalls, byIDsCalls int
	e1 := domain.Event{ID: "e1", Content: "payments latency spike"}
	e2 := domain.Event{ID: "e2", Content: "unrelated"}
	events := spyEventRepo{
		byID:         map[string]domain.Event{"e1": e1, "e2": e2},
		all:          []domain.Event{e1, e2},
		listAllCount: new(int),
		byIDsCount:   &byIDsCalls,
	}
	dense := spyVectorRepo{
		hits:   []ports.EventSimilarityHit{{EventID: "e1", Similarity: 0.9}, {EventID: "e2", Similarity: 0.7}},
		called: &searchCalls,
	}
	engine := NewEngine(events, fakeClaimRepo{}, fakeRelationshipRepo{rels: map[string][]domain.Relationship{}}).
		WithEmbeddings(dense, fakeEmbedClient{})

	ranked, ok := engine.eventsByHybrid(context.Background(), "why is payments slow?")
	if !ok {
		t.Fatal("eventsByHybrid returned ok=false; expected dense-only fast-path to serve")
	}
	if got := idsOf(ranked); !eqStrings(got, []string{"e1", "e2"}) {
		t.Fatalf("dense-only recall order = %v, want [e1 e2]", got)
	}
}
