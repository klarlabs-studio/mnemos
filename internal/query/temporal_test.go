package query

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

func TestAnswer_TemporalFilter_DefaultsToCurrentlyValid(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	events := fakeEventRepo{events: []domain.Event{
		{ID: "ev1", Content: "Felix's role: senior engineer", Timestamp: now.Add(-24 * time.Hour)},
	}}
	claims := []domain.Claim{
		{
			ID: "old", Text: "Felix is a junior engineer at Acme", Type: domain.ClaimTypeFact,
			Confidence: 0.8, Status: domain.ClaimStatusActive,
			ValidFrom: now.Add(-365 * 24 * time.Hour),
			ValidTo:   now.Add(-30 * 24 * time.Hour), // closed in the past → filtered out
		},
		{
			ID: "current", Text: "Felix is a senior engineer at Acme", Type: domain.ClaimTypeFact,
			Confidence: 0.9, Status: domain.ClaimStatusActive,
			ValidFrom: now.Add(-30 * 24 * time.Hour),
		},
	}
	engine := NewEngine(events, fakeClaimRepo{claims: claims}, fakeRelationshipRepo{})

	got, err := engine.AnswerWithOptions(context.Background(), "Felix engineer role", AnswerOptions{AsOf: now})
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	for _, c := range got.Claims {
		if c.ID == "old" {
			t.Fatalf("default temporal filter should hide superseded claim 'old', got %+v", got.Claims)
		}
	}
	if !containsClaimID(got.Claims, "current") {
		t.Fatalf("currently-valid claim 'current' should be present, got %+v", got.Claims)
	}
}

func TestAnswer_IncludeHistory_ReturnsSuperseded(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	events := fakeEventRepo{events: []domain.Event{
		{ID: "ev1", Content: "Felix engineer", Timestamp: now},
	}}
	claims := []domain.Claim{
		{
			ID: "old", Text: "junior", Type: domain.ClaimTypeFact, Confidence: 0.8,
			Status:  domain.ClaimStatusActive,
			ValidTo: now.Add(-30 * 24 * time.Hour),
		},
	}
	engine := NewEngine(events, fakeClaimRepo{claims: claims}, fakeRelationshipRepo{})

	got, err := engine.AnswerWithOptions(context.Background(), "Felix", AnswerOptions{AsOf: now, IncludeHistory: true})
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	if !containsClaimID(got.Claims, "old") {
		t.Fatalf("--include-history should expose superseded claims, got %+v", got.Claims)
	}
}

func TestAnswer_AsOf_ReturnsHistoricalState(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	march := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	may := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

	events := fakeEventRepo{events: []domain.Event{
		{ID: "ev1", Content: "Felix engineer role", Timestamp: now},
	}}
	claims := []domain.Claim{
		{
			ID: "old", Text: "junior", Type: domain.ClaimTypeFact, Confidence: 0.8,
			Status:    domain.ClaimStatusActive,
			ValidFrom: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
			ValidTo:   may, // valid until May
		},
		{
			ID: "new", Text: "senior", Type: domain.ClaimTypeFact, Confidence: 0.9,
			Status:    domain.ClaimStatusActive,
			ValidFrom: may,
		},
	}
	engine := NewEngine(events, fakeClaimRepo{claims: claims}, fakeRelationshipRepo{})

	// As of March, only `old` was true.
	got, err := engine.AnswerWithOptions(context.Background(), "Felix role", AnswerOptions{AsOf: march})
	if err != nil {
		t.Fatalf("answer: %v", err)
	}
	if !containsClaimID(got.Claims, "old") || containsClaimID(got.Claims, "new") {
		t.Fatalf("--at March 2026 should return only 'old', got %+v", got.Claims)
	}
}

func containsClaimID(claims []domain.Claim, id string) bool {
	for _, c := range claims {
		if c.ID == id {
			return true
		}
	}
	return false
}
