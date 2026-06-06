package query

import (
	"context"
	"strings"
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
)

func TestBuildContextBlock_RequiresRunID(t *testing.T) {
	e := newTestEngine(t, nil, nil, nil)
	if _, err := e.BuildContextBlock(context.Background(), ContextBlockOptions{}); err == nil {
		t.Errorf("expected error when RunID is empty")
	}
}

func TestBuildContextBlock_EmptyRunReturnsStableHeader(t *testing.T) {
	e := newTestEngine(t, nil, nil, nil)
	got, err := e.BuildContextBlock(context.Background(), ContextBlockOptions{RunID: "r-empty"})
	if err != nil {
		t.Fatalf("BuildContextBlock: %v", err)
	}
	if !strings.Contains(got, "# Memory context (run r-empty)") {
		t.Errorf("missing header in:\n%s", got)
	}
	if !strings.Contains(got, "## Active claims (0)") {
		t.Errorf("missing zero-claims section")
	}
	if !strings.Contains(got, "## Contradictions (0)") {
		t.Errorf("missing zero-contradictions section")
	}
}

func TestBuildContextBlock_OrdersByTrustDescending(t *testing.T) {
	events := []domain.Event{{ID: "e1", RunID: "r1"}}
	claims := []domain.Claim{
		{ID: "cl_low", Text: "low trust", Type: domain.ClaimTypeFact, TrustScore: 0.3, Status: domain.ClaimStatusActive},
		{ID: "cl_high", Text: "high trust", Type: domain.ClaimTypeFact, TrustScore: 0.9, Status: domain.ClaimStatusActive},
		{ID: "cl_mid", Text: "mid trust", Type: domain.ClaimTypeFact, TrustScore: 0.6, Status: domain.ClaimStatusActive},
	}
	e := newTestEngine(t, events, claims, nil)
	out, err := e.BuildContextBlock(context.Background(), ContextBlockOptions{RunID: "r1"})
	if err != nil {
		t.Fatalf("BuildContextBlock: %v", err)
	}
	highIdx := strings.Index(out, "cl_high")
	midIdx := strings.Index(out, "cl_mid")
	lowIdx := strings.Index(out, "cl_low")
	if highIdx < 0 || midIdx < 0 || lowIdx < 0 {
		t.Fatalf("missing one of the claim ids in output:\n%s", out)
	}
	if highIdx >= midIdx || midIdx >= lowIdx {
		t.Errorf("claims not ordered by trust desc: high=%d mid=%d low=%d\n%s", highIdx, midIdx, lowIdx, out)
	}
}

func TestBuildContextBlock_DropsDeprecatedClaims(t *testing.T) {
	events := []domain.Event{{ID: "e1", RunID: "r1"}}
	claims := []domain.Claim{
		{ID: "cl_active", Text: "kept", Type: domain.ClaimTypeFact, TrustScore: 0.5, Status: domain.ClaimStatusActive},
		{ID: "cl_dep", Text: "dropped", Type: domain.ClaimTypeFact, TrustScore: 0.5, Status: domain.ClaimStatusDeprecated},
	}
	e := newTestEngine(t, events, claims, nil)
	out, _ := e.BuildContextBlock(context.Background(), ContextBlockOptions{RunID: "r1"})
	if !strings.Contains(out, "cl_active") {
		t.Errorf("expected cl_active kept")
	}
	if strings.Contains(out, "cl_dep") {
		t.Errorf("expected deprecated cl_dep dropped")
	}
}

func TestBuildContextBlock_SurfacesContradictionsBetweenInRunClaims(t *testing.T) {
	events := []domain.Event{{ID: "e1", RunID: "r1"}}
	claims := []domain.Claim{
		{ID: "cl_a", Text: "X", Type: domain.ClaimTypeFact, TrustScore: 0.5, Status: domain.ClaimStatusActive},
		{ID: "cl_b", Text: "not X", Type: domain.ClaimTypeFact, TrustScore: 0.5, Status: domain.ClaimStatusActive},
	}
	rels := []domain.Relationship{
		{ID: "rel_1", Type: domain.RelationshipTypeContradicts, FromClaimID: "cl_a", ToClaimID: "cl_b"},
	}
	e := newTestEngine(t, events, claims, rels)
	out, _ := e.BuildContextBlock(context.Background(), ContextBlockOptions{RunID: "r1"})
	if !strings.Contains(out, "cl_a ⊥ cl_b") {
		t.Errorf("expected contradiction line in:\n%s", out)
	}
	if !strings.Contains(out, "## Contradictions (1)") {
		t.Errorf("expected count=1 in:\n%s", out)
	}
}

func TestBuildContextBlock_RespectsMaxTokensBudget(t *testing.T) {
	events := []domain.Event{{ID: "e1", RunID: "r1"}}
	long := strings.Repeat("X", 200)
	claims := []domain.Claim{
		{ID: "cl_a", Text: long, Type: domain.ClaimTypeFact, TrustScore: 0.9, Status: domain.ClaimStatusActive},
		{ID: "cl_b", Text: long, Type: domain.ClaimTypeFact, TrustScore: 0.5, Status: domain.ClaimStatusActive},
	}
	e := newTestEngine(t, events, claims, nil)
	out, _ := e.BuildContextBlock(context.Background(), ContextBlockOptions{RunID: "r1", MaxTokens: 80})
	if strings.Contains(out, "cl_b") {
		t.Errorf("expected cl_b truncated at MaxTokens=80, got:\n%s", out)
	}
}

// newTestEngine wires the existing fakeEventRepo / fakeClaimRepo /
// fakeRelationshipRepo (declared in engine_test.go) into a query
// Engine for context-block tests. Reusing the same fakes keeps the
// test surface aligned with the rest of the package.
func newTestEngine(_ *testing.T, events []domain.Event, claims []domain.Claim, rels []domain.Relationship) Engine {
	relMap := map[string][]domain.Relationship{}
	for _, r := range rels {
		relMap[r.FromClaimID] = append(relMap[r.FromClaimID], r)
		relMap[r.ToClaimID] = append(relMap[r.ToClaimID], r)
	}
	return NewEngine(
		fakeEventRepo{events: events},
		fakeClaimRepo{claims: claims},
		fakeRelationshipRepo{rels: relMap},
	)
}
