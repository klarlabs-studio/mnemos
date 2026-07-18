package query

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// TestReconsolidation verifies the ADR-0015 §5 retrieval freshness touch: off by
// default no belief is re-verified; with --reconsolidate the recalled beliefs are
// MarkVerified'd (the testing effect), bounded to the top-N focus.
func TestReconsolidation(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	const question = "alpha widget beta gamma"
	events := fakeEventRepo{events: []domain.Event{
		{ID: "ev_1", RunID: "run_1", Content: "alpha widget beta gamma delta", Timestamp: now},
	}}
	claims := make([]domain.Claim, 0, 3)
	for i := 0; i < 3; i++ {
		claims = append(claims, domain.Claim{
			ID: claimID(i), Text: claimID(i) + " content", Type: domain.ClaimTypeFact,
			Status: domain.ClaimStatusActive, Confidence: 0.8, CreatedAt: now,
		})
	}
	recorder := &[]string{}
	engine := NewEngine(events, fakeClaimRepo{claims: claims, verifiedRecorder: recorder}, fakeRelationshipRepo{rels: map[string][]domain.Relationship{}})

	// Off: no reconsolidation.
	if _, err := engine.AnswerWithOptions(context.Background(), question, AnswerOptions{}); err != nil {
		t.Fatalf("AnswerWithOptions: %v", err)
	}
	if len(*recorder) != 0 {
		t.Fatalf("reconsolidation off must not re-verify, got %v", *recorder)
	}

	// On: every recalled belief is refreshed.
	if _, err := engine.AnswerWithOptions(context.Background(), question, AnswerOptions{Reconsolidate: true}); err != nil {
		t.Fatalf("AnswerWithOptions: %v", err)
	}
	if len(*recorder) == 0 {
		t.Fatal("reconsolidation on should re-verify the recalled beliefs")
	}
	seen := map[string]bool{}
	for _, id := range *recorder {
		seen[id] = true
	}
	if !seen["c0"] {
		t.Errorf("recalled c0 should have been reconsolidated; recorder=%v", *recorder)
	}
}
