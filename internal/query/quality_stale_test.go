package query

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// TestComputeMemoryQuality_CountsStale guards the fix for the StaleCount-always-0 bug:
// a currently-valid belief unverified past the staleness horizon must be counted (and a
// fresh one must not), so the MaxStaleRatio SLO in CheckSLOs is no longer structurally 0.
func TestComputeMemoryQuality_CountsStale(t *testing.T) {
	now := time.Now().UTC()
	old := now.AddDate(0, 0, -200) // well past the 90-day horizon
	claims := []domain.Claim{
		{ID: "stale", Text: "old", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive, Confidence: 0.8, CreatedAt: old, ValidFrom: old},
		{ID: "fresh", Text: "new", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive, Confidence: 0.8, CreatedAt: now, ValidFrom: now},
	}
	engine := NewEngine(fakeEventRepo{}, fakeClaimRepo{claims: claims}, fakeRelationshipRepo{rels: map[string][]domain.Relationship{}})
	q, err := engine.ComputeMemoryQuality(context.Background())
	if err != nil {
		t.Fatalf("ComputeMemoryQuality: %v", err)
	}
	if q.StaleCount != 1 {
		t.Errorf("StaleCount = %d, want 1 (only the 200-day-old belief)", q.StaleCount)
	}
}
