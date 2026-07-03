package postgres_test

import (
	"context"
	"errors"
	"testing"

	"go.klarlabs.de/mnemos/internal/ports"
)

// requireVectorSearcher returns the connection's EventVectorSearcher, skipping
// the test when the backend has no native vector path (pgvector extension not
// installed in the target Postgres, so the accelerator column was never added).
// This keeps the suite green on a plain postgres:17 while exercising the real
// `<=>` operator on a pgvector/pgvector image.
func requireVectorSearcher(t *testing.T, s ports.EventVectorSearcher) {
	t.Helper()
	_, err := s.SearchEventsByVector(context.Background(), []float32{0, 0, 0}, 1, 0)
	if errors.Is(err, ports.ErrVectorSearchUnavailable) {
		t.Skip("pgvector not installed on TEST_POSTGRES_DSN; skipping native vector search test")
	}
	if err != nil {
		t.Fatalf("probe SearchEventsByVector: %v", err)
	}
}

// TestSearchEventsByVector_RanksByCosine proves the native pgvector recall path:
// Upsert writes the accelerator column, and SearchEventsByVector ranks events by
// cosine distance (`<=>`) with the nearest vector first.
func TestSearchEventsByVector_RanksByCosine(t *testing.T) {
	conn := withConn(t)
	searcher, ok := conn.Embeddings.(ports.EventVectorSearcher)
	if !ok {
		t.Fatal("postgres EmbeddingRepository must implement ports.EventVectorSearcher")
	}
	requireVectorSearcher(t, searcher)

	ctx := context.Background()
	// Three unit-ish vectors in 3-space: near is the query, far is orthogonal.
	upsert := func(id string, vec []float32) {
		if err := conn.Embeddings.Upsert(ctx, id, "event", vec, "test-3", "tester"); err != nil {
			t.Fatalf("Upsert(%s): %v", id, err)
		}
	}
	upsert("e_near", []float32{1, 0, 0})
	upsert("e_mid", []float32{0.7, 0.7, 0})
	upsert("e_far", []float32{0, 0, 1})

	hits, err := searcher.SearchEventsByVector(ctx, []float32{1, 0, 0}, 10, 0)
	if err != nil {
		t.Fatalf("SearchEventsByVector: %v", err)
	}
	if len(hits) != 3 {
		t.Fatalf("expected 3 event hits, got %d: %+v", len(hits), hits)
	}
	if hits[0].EventID != "e_near" {
		t.Fatalf("nearest vector should rank first, got %+v", hits)
	}
	if hits[len(hits)-1].EventID != "e_far" {
		t.Fatalf("orthogonal vector should rank last, got %+v", hits)
	}
	// Similarity must be monotonically non-increasing (ORDER BY distance asc).
	for i := 1; i < len(hits); i++ {
		if hits[i].Similarity > hits[i-1].Similarity {
			t.Fatalf("similarities not ordered desc: %+v", hits)
		}
	}
	if hits[0].Model != "test-3" {
		t.Fatalf("stored model should round-trip, got %q", hits[0].Model)
	}
}

// TestSearchEventsByVector_FiltersByDimension proves the dimension guard: an
// embedding of a different width is excluded rather than tripping pgvector's
// same-dimension requirement on `<=>`.
func TestSearchEventsByVector_FiltersByDimension(t *testing.T) {
	conn := withConn(t)
	searcher, ok := conn.Embeddings.(ports.EventVectorSearcher)
	if !ok {
		t.Fatal("postgres EmbeddingRepository must implement ports.EventVectorSearcher")
	}
	requireVectorSearcher(t, searcher)

	ctx := context.Background()
	if err := conn.Embeddings.Upsert(ctx, "e_3d", "event", []float32{1, 0, 0}, "m3", "tester"); err != nil {
		t.Fatalf("Upsert 3d: %v", err)
	}
	if err := conn.Embeddings.Upsert(ctx, "e_5d", "event", []float32{1, 0, 0, 0, 0}, "m5", "tester"); err != nil {
		t.Fatalf("Upsert 5d: %v", err)
	}

	hits, err := searcher.SearchEventsByVector(ctx, []float32{1, 0, 0}, 10, 0)
	if err != nil {
		t.Fatalf("SearchEventsByVector (3d query): %v", err)
	}
	if len(hits) != 1 || hits[0].EventID != "e_3d" {
		t.Fatalf("dimension filter should return only the 3d event, got %+v", hits)
	}
}
