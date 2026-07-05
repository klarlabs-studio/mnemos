package mnemos

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

func upsertAuthoredClaim(t *testing.T, m *memory, id, text, author string, trust float64) {
	t.Helper()
	now := time.Now().UTC()
	if err := m.conn.Claims.Upsert(context.Background(), []domain.Claim{{
		ID: id, Text: text, Type: domain.ClaimTypeFact, Confidence: 0.9,
		Status: domain.ClaimStatusActive, CreatedBy: author, TrustScore: trust,
		CreatedAt: now, ValidFrom: now,
	}}); err != nil {
		t.Fatalf("upsert claim %s: %v", id, err)
	}
}

// TestWhoKnows_RoutesToExpert verifies the transactive directory: the worker whose
// memory covers the query topic is ranked first; an off-topic worker ranks below
// or is absent; unattributed claims are ignored.
func TestWhoKnows_RoutesToExpert(t *testing.T) {
	m := calibMem(t)
	ctx := context.Background()

	// grace: deployment/rollout expertise (matches the query).
	upsertAuthoredClaim(t, m, "g1", "kubernetes deployment rollout uses version tags", "grace", 0.8)
	upsertAuthoredClaim(t, m, "g2", "a failed deployment triggers an automatic rollback", "grace", 0.8)
	// kai: code-review expertise (does NOT match a deployment query).
	upsertAuthoredClaim(t, m, "k1", "python style prefers explicit error handling", "kai", 0.7)
	// an unattributed claim that matches — must be ignored (no owner to route to).
	upsertAuthoredClaim(t, m, "s1", "deployment rollout notes", "<system>", 0.9)

	experts, err := m.WhoKnows(ctx, "kubernetes deployment rollout failing", 5)
	if err != nil {
		t.Fatalf("WhoKnows: %v", err)
	}
	if len(experts) == 0 {
		t.Fatal("expected at least one expert, got none")
	}
	if experts[0].Worker != "grace" {
		t.Errorf("expected grace routed first; got %+v", experts)
	}
	if experts[0].Affinity < 0.99 {
		t.Errorf("top expert affinity = %.3f, want 1.0 (normalised)", experts[0].Affinity)
	}
	if experts[0].Reliability <= 0 {
		t.Errorf("grace reliability = %.3f, want > 0 (mean trust of matching claims)", experts[0].Reliability)
	}
	if experts[0].ClaimCount != 2 {
		t.Errorf("grace claim count = %d, want 2", experts[0].ClaimCount)
	}
	for _, e := range experts {
		if e.Worker == "<system>" {
			t.Errorf("unattributed <system> claims must not surface as an expert: %+v", experts)
		}
	}
}

// TestWhoKnows_NoMatch verifies an off-topic query routes to nobody.
func TestWhoKnows_NoMatch(t *testing.T) {
	m := calibMem(t)
	ctx := context.Background()
	upsertAuthoredClaim(t, m, "g1", "kubernetes deployment rollout uses version tags", "grace", 0.8)

	experts, err := m.WhoKnows(ctx, "quarterly marketing budget forecast", 5)
	if err != nil {
		t.Fatalf("WhoKnows: %v", err)
	}
	if len(experts) != 0 {
		t.Errorf("expected no experts for an off-topic query; got %+v", experts)
	}
}
