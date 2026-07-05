package mnemos

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

func upsertTypedClaim(t *testing.T, m *memory, id, text string, typ domain.ClaimType) {
	t.Helper()
	now := time.Now().UTC()
	if err := m.conn.Claims.Upsert(context.Background(), []domain.Claim{{
		ID: id, Text: text, Type: typ, Confidence: 0.9,
		Status: domain.ClaimStatusActive, CreatedAt: now, ValidFrom: now,
	}}); err != nil {
		t.Fatalf("upsert claim %s: %v", id, err)
	}
}

func edge(t *testing.T, m *memory, id, from, to string, typ domain.RelationshipType) {
	t.Helper()
	if err := m.conn.Relationships.Upsert(context.Background(), []domain.Relationship{
		{ID: id, Type: typ, FromClaimID: from, ToClaimID: to, CreatedAt: time.Now().UTC()},
	}); err != nil {
		t.Fatalf("upsert edge %s: %v", id, err)
	}
}

// TestStructuralFingerprint_ByShape verifies the pure WL fingerprint: two
// subgraphs with the same role/edge skeleton fingerprint identically (content
// ignored), while a different shape does not.
func TestStructuralFingerprint_ByShape(t *testing.T) {
	// Shape: decision -supports-> fact -contradicts-> hypothesis.
	g1r := map[string]string{"a1": "decision", "a2": "fact", "a3": "hypothesis"}
	g1e := []typedEdge{{"a1", "a2", "supports"}, {"a2", "a3", "contradicts"}}
	// Same shape, different node ids.
	g2r := map[string]string{"b1": "decision", "b2": "fact", "b3": "hypothesis"}
	g2e := []typedEdge{{"b1", "b2", "supports"}, {"b2", "b3", "contradicts"}}
	// Different shape: fact -supports-> fact.
	g3r := map[string]string{"c1": "fact", "c2": "fact"}
	g3e := []typedEdge{{"c1", "c2", "supports"}}

	fp1 := structuralFingerprint(g1r, g1e)
	fp2 := structuralFingerprint(g2r, g2e)
	fp3 := structuralFingerprint(g3r, g3e)

	if s := cosineHist(fp1, fp2); s < 0.999 {
		t.Errorf("isomorphic subgraphs should fingerprint identically; cosine=%v", s)
	}
	if s := cosineHist(fp1, fp3); s >= 0.999 {
		t.Errorf("different-shape subgraphs should differ; cosine=%v", s)
	}
}

// TestAnalogousClaims_FindsSameShape verifies end-to-end structural retrieval: the
// claim whose subgraph shares the anchor's causal skeleton is surfaced first, and
// a differently-shaped subgraph does not outrank it.
func TestAnalogousClaims_FindsSameShape(t *testing.T) {
	m := calibMem(t)
	ctx := context.Background()

	// Anchor A: decision -supports-> fact -contradicts-> hypothesis.
	upsertTypedClaim(t, m, "a1", "alpha one", domain.ClaimTypeDecision)
	upsertTypedClaim(t, m, "a2", "alpha two", domain.ClaimTypeFact)
	upsertTypedClaim(t, m, "a3", "alpha three", domain.ClaimTypeHypothesis)
	edge(t, m, "ea1", "a1", "a2", domain.RelationshipTypeSupports)
	edge(t, m, "ea2", "a2", "a3", domain.RelationshipTypeContradicts)

	// Analog B: SAME shape, entirely different content.
	upsertTypedClaim(t, m, "b1", "bravo one", domain.ClaimTypeDecision)
	upsertTypedClaim(t, m, "b2", "bravo two", domain.ClaimTypeFact)
	upsertTypedClaim(t, m, "b3", "bravo three", domain.ClaimTypeHypothesis)
	edge(t, m, "eb1", "b1", "b2", domain.RelationshipTypeSupports)
	edge(t, m, "eb2", "b2", "b3", domain.RelationshipTypeContradicts)

	// Distractor C: different shape (fact -supports-> fact).
	upsertTypedClaim(t, m, "c1", "charlie one", domain.ClaimTypeFact)
	upsertTypedClaim(t, m, "c2", "charlie two", domain.ClaimTypeFact)
	edge(t, m, "ec1", "c1", "c2", domain.RelationshipTypeSupports)

	got, err := m.AnalogousClaims(ctx, "a1", 5)
	if err != nil {
		t.Fatalf("AnalogousClaims: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected structural analogies, got none")
	}
	top := got[0]
	if top.ClaimID == "" || top.ClaimID[0] != 'b' {
		t.Errorf("expected a B-component analog (same shape) on top; got %q (sim %.3f); all=%+v", top.ClaimID, top.Similarity, got)
	}
	if top.Similarity < 0.99 {
		t.Errorf("isomorphic analog similarity = %.3f, want ~1.0", top.Similarity)
	}
	for _, a := range got {
		if a.ClaimID != "" && a.ClaimID[0] == 'c' && a.Similarity >= top.Similarity {
			t.Errorf("different-shape C ranked >= same-shape B: %+v", got)
		}
	}
}
