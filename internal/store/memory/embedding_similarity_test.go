package memory_test

import (
	"context"
	"testing"

	"go.klarlabs.de/mnemos/internal/ports"
	"go.klarlabs.de/mnemos/internal/store"

	_ "go.klarlabs.de/mnemos/internal/store/memory"
)

func openSim(t *testing.T) *store.Conn {
	t.Helper()
	conn, err := store.Open(context.Background(), "memory://")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func seedClaimEmbedding(t *testing.T, conn *store.Conn, claimID string, vec []float32) {
	t.Helper()
	if err := conn.Embeddings.Upsert(context.Background(), claimID, "claim", vec, "test-model", "test-actor"); err != nil {
		t.Fatalf("seed embedding for %s: %v", claimID, err)
	}
}

// searcher type-asserts the embedding repository onto the optional
// capability so a missing implementation fails fast and loudly rather
// than silently disabling semantic search.
func searcher(t *testing.T, conn *store.Conn) ports.ClaimSimilaritySearcher {
	t.Helper()
	s, ok := conn.Embeddings.(ports.ClaimSimilaritySearcher)
	if !ok {
		t.Fatal("memory EmbeddingRepository does not implement ports.ClaimSimilaritySearcher")
	}
	return s
}

// TestMemory_SearchClaimsByVector_RanksByCosine pins the core contract:
// hits return ordered by similarity desc and the query vector's own
// direction wins.
func TestMemory_SearchClaimsByVector_RanksByCosine(t *testing.T) {
	conn := openSim(t)
	seedClaimEmbedding(t, conn, "claim-aligned", []float32{1.0, 0.0, 0.0})
	seedClaimEmbedding(t, conn, "claim-mid", []float32{1.0, 1.0, 0.0})
	seedClaimEmbedding(t, conn, "claim-orthogonal", []float32{0.0, 1.0, 0.0})

	hits, err := searcher(t, conn).SearchClaimsByVector(
		context.Background(),
		[]float32{1.0, 0.0, 0.0},
		nil,
		10,
		0.0,
	)
	if err != nil {
		t.Fatalf("SearchClaimsByVector: %v", err)
	}
	if len(hits) != 3 {
		t.Fatalf("hits len = %d, want 3", len(hits))
	}
	if hits[0].ClaimID != "claim-aligned" {
		t.Errorf("rank[0] = %q, want claim-aligned (cosine 1.0 must rank first)", hits[0].ClaimID)
	}
	if hits[2].ClaimID != "claim-orthogonal" {
		t.Errorf("rank[2] = %q, want claim-orthogonal (cosine 0.0 must rank last)", hits[2].ClaimID)
	}
	if hits[0].Similarity < hits[1].Similarity || hits[1].Similarity < hits[2].Similarity {
		t.Errorf("hits not sorted similarity desc: %+v", hits)
	}
}

// TestMemory_SearchClaimsByVector_RespectsCandidateAllowlist proves the
// tenant boundary seam: only claim ids in the allowlist may surface,
// even when other claims would score higher.
func TestMemory_SearchClaimsByVector_RespectsCandidateAllowlist(t *testing.T) {
	conn := openSim(t)
	seedClaimEmbedding(t, conn, "claim-allowed-perfect", []float32{1.0, 0.0, 0.0})
	seedClaimEmbedding(t, conn, "claim-allowed-meh", []float32{1.0, 1.0, 0.0})
	seedClaimEmbedding(t, conn, "claim-FORBIDDEN-perfect", []float32{1.0, 0.0, 0.0})

	allow := map[string]struct{}{
		"claim-allowed-perfect": {},
		"claim-allowed-meh":     {},
	}
	hits, err := searcher(t, conn).SearchClaimsByVector(
		context.Background(),
		[]float32{1.0, 0.0, 0.0},
		allow,
		10,
		0.0,
	)
	if err != nil {
		t.Fatalf("SearchClaimsByVector: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("hits len = %d, want 2 (allowlist enforced)", len(hits))
	}
	for _, h := range hits {
		if _, ok := allow[h.ClaimID]; !ok {
			t.Errorf("hit %q leaked outside allowlist", h.ClaimID)
		}
	}
}

// TestMemory_SearchClaimsByVector_MinSimilarityFilters drops sub-
// threshold rows so callers get a clean cutoff and don't have to
// post-filter.
func TestMemory_SearchClaimsByVector_MinSimilarityFilters(t *testing.T) {
	conn := openSim(t)
	seedClaimEmbedding(t, conn, "claim-high", []float32{1.0, 0.0, 0.0})
	seedClaimEmbedding(t, conn, "claim-mid", []float32{1.0, 1.0, 0.0})
	seedClaimEmbedding(t, conn, "claim-low", []float32{0.0, 1.0, 0.0})

	hits, err := searcher(t, conn).SearchClaimsByVector(
		context.Background(),
		[]float32{1.0, 0.0, 0.0},
		nil,
		10,
		0.9,
	)
	if err != nil {
		t.Fatalf("SearchClaimsByVector: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits len = %d, want 1 (only similarity >= 0.9 survives)", len(hits))
	}
	if hits[0].ClaimID != "claim-high" {
		t.Errorf("survivor = %q, want claim-high", hits[0].ClaimID)
	}
}

// TestMemory_SearchClaimsByVector_TopKBounded caps result size so a
// caller asking top_k=2 never gets the long tail by accident.
func TestMemory_SearchClaimsByVector_TopKBounded(t *testing.T) {
	conn := openSim(t)
	seedClaimEmbedding(t, conn, "claim-a", []float32{1.0, 0.0, 0.0})
	seedClaimEmbedding(t, conn, "claim-b", []float32{1.0, 0.1, 0.0})
	seedClaimEmbedding(t, conn, "claim-c", []float32{1.0, 0.5, 0.0})
	seedClaimEmbedding(t, conn, "claim-d", []float32{0.0, 1.0, 0.0})

	hits, err := searcher(t, conn).SearchClaimsByVector(
		context.Background(),
		[]float32{1.0, 0.0, 0.0},
		nil,
		2,
		0.0,
	)
	if err != nil {
		t.Fatalf("SearchClaimsByVector: %v", err)
	}
	if len(hits) != 2 {
		t.Errorf("hits len = %d, want 2 (top_k cap)", len(hits))
	}
}

// TestMemory_SearchClaimsByVector_IgnoresNonClaimEntities asserts
// vectors for other entity_types (events, lessons, etc.) do not leak
// into a claim-scoped search.
func TestMemory_SearchClaimsByVector_IgnoresNonClaimEntities(t *testing.T) {
	conn := openSim(t)
	seedClaimEmbedding(t, conn, "claim-x", []float32{1.0, 0.0, 0.0})
	if err := conn.Embeddings.Upsert(context.Background(), "event-y", "event", []float32{1.0, 0.0, 0.0}, "test-model", "test-actor"); err != nil {
		t.Fatalf("seed event embedding: %v", err)
	}

	hits, err := searcher(t, conn).SearchClaimsByVector(
		context.Background(),
		[]float32{1.0, 0.0, 0.0},
		nil,
		10,
		0.0,
	)
	if err != nil {
		t.Fatalf("SearchClaimsByVector: %v", err)
	}
	if len(hits) != 1 || hits[0].ClaimID != "claim-x" {
		t.Errorf("entity_type='event' leaked into claim search: %+v", hits)
	}
}

// TestMemory_SearchClaimsByVector_EmptyAllowlistMatchesNothing pins the
// crucial difference between nil (no filter) and empty (filter that
// matches nothing). The handler will pass an empty set when a run_id
// resolves to zero events; an accidental corpus dump there would be a
// tenant leak.
func TestMemory_SearchClaimsByVector_EmptyAllowlistMatchesNothing(t *testing.T) {
	conn := openSim(t)
	seedClaimEmbedding(t, conn, "claim-x", []float32{1.0, 0.0, 0.0})

	hits, err := searcher(t, conn).SearchClaimsByVector(
		context.Background(),
		[]float32{1.0, 0.0, 0.0},
		map[string]struct{}{},
		10,
		0.0,
	)
	if err != nil {
		t.Fatalf("SearchClaimsByVector: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("empty allowlist must match nothing, got %+v", hits)
	}
}
