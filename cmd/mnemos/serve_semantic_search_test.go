package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/embedding"
)

// stubEmbedder returns a pre-canned vector for every Embed call,
// letting the handler test the search wiring without a real provider.
type stubEmbedder struct {
	vector []float32
	err    error
	calls  int
}

func (s *stubEmbedder) Embed(_ context.Context, _ []string) ([][]float32, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return [][]float32{s.vector}, nil
}

// withStubEmbedder swaps the package-level resolver with one returning
// `c`, restoring the previous resolver on cleanup. Centralises the
// override so tests stay focused on the contract under test.
func withStubEmbedder(t *testing.T, c embedding.Client) {
	t.Helper()
	prev := embedderResolver
	embedderResolver = func() (embedding.Client, error) { return c, nil }
	t.Cleanup(func() { embedderResolver = prev })
}

// withEmbedderError forces the resolver to fail, simulating a missing
// MNEMOS_EMBED_PROVIDER in tests that prove the fail-closed contract.
func withEmbedderError(t *testing.T, err error) {
	t.Helper()
	prev := embedderResolver
	embedderResolver = func() (embedding.Client, error) { return nil, err }
	t.Cleanup(func() { embedderResolver = prev })
}

// TestSemanticSearch_RanksByCosine_RespectsRunID is the end-to-end
// happy path: the handler embeds the query, scopes by run_id, ranks
// claims by cosine, and returns them in descending-similarity order.
// "Cross-tenant" claims (run B) must not appear even if they would
// score higher in absolute terms.
func TestSemanticSearch_RanksByCosine_RespectsRunID(t *testing.T) {
	_, conn := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// Run A events + claims.
	seedEventConn(t, conn, "ev_A1", "tenant:A", "content A1", "src", "{}", now)
	seedClaimConn(t, conn, "cl_A_perfect", "Aligned", "fact", "active", 0.9, now)
	seedClaimConn(t, conn, "cl_A_mid", "Diagonal", "fact", "active", 0.9, now)
	if err := conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{
		{ClaimID: "cl_A_perfect", EventID: "ev_A1"},
		{ClaimID: "cl_A_mid", EventID: "ev_A1"},
	}); err != nil {
		t.Fatalf("UpsertEvidence A: %v", err)
	}
	if err := conn.Embeddings.Upsert(ctx, "cl_A_perfect", "claim", []float32{1, 0, 0}, "m", ""); err != nil {
		t.Fatalf("emb A perfect: %v", err)
	}
	if err := conn.Embeddings.Upsert(ctx, "cl_A_mid", "claim", []float32{1, 1, 0}, "m", ""); err != nil {
		t.Fatalf("emb A mid: %v", err)
	}

	// Run B — perfect-cosine claim that MUST NOT leak into run A's response.
	seedEventConn(t, conn, "ev_B1", "tenant:B", "content B1", "src", "{}", now)
	seedClaimConn(t, conn, "cl_B_perfect", "Other tenant", "fact", "active", 0.9, now)
	if err := conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{
		{ClaimID: "cl_B_perfect", EventID: "ev_B1"},
	}); err != nil {
		t.Fatalf("UpsertEvidence B: %v", err)
	}
	if err := conn.Embeddings.Upsert(ctx, "cl_B_perfect", "claim", []float32{1, 0, 0}, "m", ""); err != nil {
		t.Fatalf("emb B: %v", err)
	}

	withStubEmbedder(t, &stubEmbedder{vector: []float32{1, 0, 0}})

	srv := httptest.NewServer(newServerMux(conn))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/beliefs?similar_to=anything&run_id=tenant:A&top_k=10")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body claimsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Claims) != 2 {
		t.Fatalf("got %d claims, want 2 (only run A)", len(body.Claims))
	}
	if body.Claims[0].ID != "cl_A_perfect" {
		t.Errorf("rank[0] = %q, want cl_A_perfect", body.Claims[0].ID)
	}
	if body.Claims[1].ID != "cl_A_mid" {
		t.Errorf("rank[1] = %q, want cl_A_mid", body.Claims[1].ID)
	}
	for _, c := range body.Claims {
		if c.ID == "cl_B_perfect" {
			t.Fatal("tenant B claim leaked into tenant A semantic search")
		}
		if c.Similarity <= 0 {
			t.Errorf("claim %q missing similarity", c.ID)
		}
	}
	if body.Claims[0].Similarity < body.Claims[1].Similarity {
		t.Errorf("not sorted desc: %+v", body.Claims)
	}
}

// TestSemanticSearch_RequiresRunID enforces the load-bearing tenant
// boundary: similar_to without run_id is 400 fail-closed.
func TestSemanticSearch_RequiresRunID(t *testing.T) {
	_, conn := openTestStore(t)
	withStubEmbedder(t, &stubEmbedder{vector: []float32{1, 0, 0}})
	srv := httptest.NewServer(newServerMux(conn))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/beliefs?similar_to=anything")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (similar_to without run_id)", resp.StatusCode)
	}
}

// TestSemanticSearch_NoEmbedderConfigured returns 501 (not 500) so
// clients can distinguish "feature unavailable on this deployment" from
// "transient error".
func TestSemanticSearch_NoEmbedderConfigured(t *testing.T) {
	_, conn := openTestStore(t)
	withEmbedderError(t, errors.New("MNEMOS_EMBED_PROVIDER unset"))
	srv := httptest.NewServer(newServerMux(conn))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/beliefs?similar_to=anything&run_id=tenant:A")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", resp.StatusCode)
	}
}

// TestSemanticSearch_MinSimilarityAndTopK pins the two knobs that
// shape the response: min_similarity drops sub-threshold matches and
// top_k caps the list size.
func TestSemanticSearch_MinSimilarityAndTopK(t *testing.T) {
	_, conn := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	seedEventConn(t, conn, "ev_X", "tenant:X", "content", "src", "{}", now)
	seeds := []struct {
		id  string
		vec []float32
	}{
		{"cl_perfect", []float32{1, 0, 0}},    // sim 1.0
		{"cl_near", []float32{0.95, 0.31, 0}}, // sim ≈ 0.95
		{"cl_mid", []float32{1, 1, 0}},        // sim ≈ 0.71
		{"cl_orthogonal", []float32{0, 1, 0}}, // sim 0
	}
	for _, s := range seeds {
		seedClaimConn(t, conn, s.id, s.id, "fact", "active", 0.9, now)
		if err := conn.Claims.UpsertEvidence(ctx, []domain.ClaimEvidence{
			{ClaimID: s.id, EventID: "ev_X"},
		}); err != nil {
			t.Fatalf("UpsertEvidence %s: %v", s.id, err)
		}
		if err := conn.Embeddings.Upsert(ctx, s.id, "claim", s.vec, "m", ""); err != nil {
			t.Fatalf("emb %s: %v", s.id, err)
		}
	}

	withStubEmbedder(t, &stubEmbedder{vector: []float32{1, 0, 0}})
	srv := httptest.NewServer(newServerMux(conn))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/beliefs?similar_to=q&run_id=tenant:X&top_k=2&min_similarity=0.9")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	var body claimsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Two qualify (>= 0.9) but top_k=2 keeps the cap.
	if len(body.Claims) != 2 {
		t.Fatalf("got %d, want 2 (min_similarity + top_k)", len(body.Claims))
	}
	for _, c := range body.Claims {
		if c.Similarity < 0.9 {
			t.Errorf("claim %q similarity %.3f below min_similarity 0.9", c.ID, c.Similarity)
		}
		if c.ID == "cl_orthogonal" || c.ID == "cl_mid" {
			t.Errorf("%q should have been filtered by min_similarity", c.ID)
		}
	}
}

// TestSemanticSearch_UnknownRunIDReturnsEmpty mirrors the standard
// list path: a run_id with no events resolves to no candidate claims
// and the response is an empty list, not 500.
func TestSemanticSearch_UnknownRunIDReturnsEmpty(t *testing.T) {
	_, conn := openTestStore(t)
	withStubEmbedder(t, &stubEmbedder{vector: []float32{1, 0, 0}})
	srv := httptest.NewServer(newServerMux(conn))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/beliefs?similar_to=q&run_id=tenant:nobody")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var body claimsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Claims) != 0 {
		t.Errorf("unknown run_id returned %d claims: %+v", len(body.Claims), body.Claims)
	}
}

// TestSemanticSearch_RejectsBadTopK / MinSimilarity prove the
// validation contract — out-of-range knobs yield 400, not silent
// clamping (which would hide bugs).
func TestSemanticSearch_RejectsBadKnobs(t *testing.T) {
	_, conn := openTestStore(t)
	withStubEmbedder(t, &stubEmbedder{vector: []float32{1, 0, 0}})
	srv := httptest.NewServer(newServerMux(conn))
	defer srv.Close()

	cases := []struct {
		name string
		url  string
	}{
		{"top_k zero", "/v1/beliefs?similar_to=q&run_id=t:A&top_k=0"},
		{"top_k negative", "/v1/beliefs?similar_to=q&run_id=t:A&top_k=-1"},
		{"top_k too large", "/v1/beliefs?similar_to=q&run_id=t:A&top_k=999"},
		{"top_k garbage", "/v1/beliefs?similar_to=q&run_id=t:A&top_k=abc"},
		{"min_similarity negative", "/v1/beliefs?similar_to=q&run_id=t:A&min_similarity=-0.1"},
		{"min_similarity > 1", "/v1/beliefs?similar_to=q&run_id=t:A&min_similarity=1.5"},
		{"min_similarity garbage", "/v1/beliefs?similar_to=q&run_id=t:A&min_similarity=high"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Get(srv.URL + tc.url)
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", resp.StatusCode)
			}
		})
	}
}
