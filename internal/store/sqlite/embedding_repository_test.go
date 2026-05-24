package sqlite

import (
	"context"
	"testing"
)

func TestEmbeddingRepositoryUpsertAndGet(t *testing.T) {
	db := openTestDB(t)
	defer closeDB(db)

	repo := NewEmbeddingRepository(db)
	vector := []float32{0.1, 0.2, 0.3, 0.4}

	if err := repo.Upsert(context.Background(), "ev_1", "event", vector, "test-model", ""); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}

	got, err := repo.GetByEntityID(context.Background(), "ev_1", "event")
	if err != nil {
		t.Fatalf("GetByEntityID() error = %v", err)
	}
	if got.EntityID != "ev_1" {
		t.Errorf("EntityID = %q, want %q", got.EntityID, "ev_1")
	}
	if got.EntityType != "event" {
		t.Errorf("EntityType = %q, want %q", got.EntityType, "event")
	}
	if got.Model != "test-model" {
		t.Errorf("Model = %q, want %q", got.Model, "test-model")
	}
	if got.Dimensions != 4 {
		t.Errorf("Dimensions = %d, want 4", got.Dimensions)
	}
	if len(got.Vector) != 4 {
		t.Fatalf("Vector length = %d, want 4", len(got.Vector))
	}
	for i, v := range vector {
		if got.Vector[i] != v {
			t.Errorf("Vector[%d] = %f, want %f", i, got.Vector[i], v)
		}
	}
}

func TestEmbeddingRepositoryUpsertOverwrite(t *testing.T) {
	db := openTestDB(t)
	defer closeDB(db)

	repo := NewEmbeddingRepository(db)

	if err := repo.Upsert(context.Background(), "ev_1", "event", []float32{0.1, 0.2}, "model-a", ""); err != nil {
		t.Fatalf("first Upsert() error = %v", err)
	}
	if err := repo.Upsert(context.Background(), "ev_1", "event", []float32{0.9, 0.8, 0.7}, "model-b", ""); err != nil {
		t.Fatalf("second Upsert() error = %v", err)
	}

	got, err := repo.GetByEntityID(context.Background(), "ev_1", "event")
	if err != nil {
		t.Fatalf("GetByEntityID() error = %v", err)
	}
	if got.Model != "model-b" {
		t.Errorf("Model = %q, want %q (should be overwritten)", got.Model, "model-b")
	}
	if len(got.Vector) != 3 {
		t.Errorf("Vector length = %d, want 3 (should be overwritten)", len(got.Vector))
	}
}

func TestEmbeddingRepositoryListByEntityType(t *testing.T) {
	db := openTestDB(t)
	defer closeDB(db)

	repo := NewEmbeddingRepository(db)

	if err := repo.Upsert(context.Background(), "ev_1", "event", []float32{0.1}, "model", ""); err != nil {
		t.Fatalf("Upsert ev_1 error = %v", err)
	}
	if err := repo.Upsert(context.Background(), "ev_2", "event", []float32{0.2}, "model", ""); err != nil {
		t.Fatalf("Upsert ev_2 error = %v", err)
	}
	if err := repo.Upsert(context.Background(), "cl_1", "claim", []float32{0.3}, "model", ""); err != nil {
		t.Fatalf("Upsert cl_1 error = %v", err)
	}

	events, err := repo.ListByEntityType(context.Background(), "event")
	if err != nil {
		t.Fatalf("ListByEntityType(event) error = %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d event embeddings, want 2", len(events))
	}

	claims, err := repo.ListByEntityType(context.Background(), "claim")
	if err != nil {
		t.Fatalf("ListByEntityType(claim) error = %v", err)
	}
	if len(claims) != 1 {
		t.Fatalf("got %d claim embeddings, want 1", len(claims))
	}
}

func TestEmbeddingRepositoryGetNotFound(t *testing.T) {
	db := openTestDB(t)
	defer closeDB(db)

	repo := NewEmbeddingRepository(db)
	_, err := repo.GetByEntityID(context.Background(), "nonexistent", "event")
	if err == nil {
		t.Fatal("expected error for nonexistent embedding")
	}
}

// TestSearchClaimsByVector_RanksByCosine pins the sqlite backend's
// parity with the memory backend's similarity contract: blob-decoded
// vectors must rank correctly under cosine.
func TestSearchClaimsByVector_RanksByCosine(t *testing.T) {
	db := openTestDB(t)
	defer closeDB(db)

	ctx := context.Background()
	repo := NewEmbeddingRepository(db)
	if err := repo.Upsert(ctx, "claim-aligned", "claim", []float32{1.0, 0.0, 0.0}, "m", ""); err != nil {
		t.Fatalf("upsert aligned: %v", err)
	}
	if err := repo.Upsert(ctx, "claim-mid", "claim", []float32{1.0, 1.0, 0.0}, "m", ""); err != nil {
		t.Fatalf("upsert mid: %v", err)
	}
	if err := repo.Upsert(ctx, "claim-orthogonal", "claim", []float32{0.0, 1.0, 0.0}, "m", ""); err != nil {
		t.Fatalf("upsert orthogonal: %v", err)
	}
	// Same vector as the perfect match but stored as a different
	// entity_type — must NOT leak into a claim search.
	if err := repo.Upsert(ctx, "event-aligned", "event", []float32{1.0, 0.0, 0.0}, "m", ""); err != nil {
		t.Fatalf("upsert event: %v", err)
	}

	hits, err := repo.SearchClaimsByVector(ctx, []float32{1.0, 0.0, 0.0}, nil, 10, 0.0)
	if err != nil {
		t.Fatalf("SearchClaimsByVector: %v", err)
	}
	if len(hits) != 3 {
		t.Fatalf("hits len = %d, want 3 (entity_type='event' must not surface)", len(hits))
	}
	if hits[0].ClaimID != "claim-aligned" {
		t.Errorf("rank[0] = %q, want claim-aligned", hits[0].ClaimID)
	}
	if hits[2].ClaimID != "claim-orthogonal" {
		t.Errorf("rank[2] = %q, want claim-orthogonal", hits[2].ClaimID)
	}
	if hits[0].Similarity < hits[1].Similarity || hits[1].Similarity < hits[2].Similarity {
		t.Errorf("hits not sorted desc: %+v", hits)
	}
}

// TestSearchClaimsByVector_AllowlistAndMinSimilarity pins the two
// tenant-safety levers in one test: candidateClaimIDs hard-restricts
// the candidate set and minSimilarity drops sub-threshold rows.
func TestSearchClaimsByVector_AllowlistAndMinSimilarity(t *testing.T) {
	db := openTestDB(t)
	defer closeDB(db)
	ctx := context.Background()
	repo := NewEmbeddingRepository(db)

	if err := repo.Upsert(ctx, "claim-allowed-perfect", "claim", []float32{1.0, 0.0, 0.0}, "m", ""); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := repo.Upsert(ctx, "claim-allowed-low", "claim", []float32{0.0, 1.0, 0.0}, "m", ""); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := repo.Upsert(ctx, "claim-FORBIDDEN-perfect", "claim", []float32{1.0, 0.0, 0.0}, "m", ""); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	allow := map[string]struct{}{
		"claim-allowed-perfect": {},
		"claim-allowed-low":     {},
	}
	hits, err := repo.SearchClaimsByVector(ctx, []float32{1.0, 0.0, 0.0}, allow, 10, 0.9)
	if err != nil {
		t.Fatalf("SearchClaimsByVector: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("hits len = %d, want 1 (allowlist + min similarity)", len(hits))
	}
	if hits[0].ClaimID != "claim-allowed-perfect" {
		t.Errorf("survivor = %q, want claim-allowed-perfect", hits[0].ClaimID)
	}
}

// TestSearchClaimsByVector_EmptyAllowlistFailsClosed pins that an
// empty (but non-nil) allowlist matches nothing — guards against a
// run_id that resolves to zero events accidentally dumping the corpus.
func TestSearchClaimsByVector_EmptyAllowlistFailsClosed(t *testing.T) {
	db := openTestDB(t)
	defer closeDB(db)
	ctx := context.Background()
	repo := NewEmbeddingRepository(db)
	if err := repo.Upsert(ctx, "claim-x", "claim", []float32{1.0, 0.0, 0.0}, "m", ""); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	hits, err := repo.SearchClaimsByVector(ctx, []float32{1.0, 0.0, 0.0}, map[string]struct{}{}, 10, 0.0)
	if err != nil {
		t.Fatalf("SearchClaimsByVector: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("empty allowlist must match nothing, got %+v", hits)
	}
}
