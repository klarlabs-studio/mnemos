package mnemos

import (
	"context"
	"testing"
)

func levelByName(pe PredictiveError, name string) PredictiveErrorLevel {
	for _, l := range pe.Levels {
		if l.Level == name {
			return l
		}
	}
	return PredictiveErrorLevel{Level: "MISSING"}
}

// TestPredictiveError_EmptyStore verifies the ADR-0017 surface degrades gracefully: an
// empty brain reports every level with zero samples, a zero total, and no hotspot —
// absence of evidence is never reported as a well-predicted (low-error) model.
func TestPredictiveError_EmptyStore(t *testing.T) {
	m := calibMem(t)
	pe, err := m.PredictiveError(context.Background())
	if err != nil {
		t.Fatalf("PredictiveError: %v", err)
	}
	if len(pe.Levels) != 4 {
		t.Fatalf("want 4 levels, got %d", len(pe.Levels))
	}
	for _, l := range pe.Levels {
		if l.Samples != 0 {
			t.Errorf("empty store: level %s should have 0 samples, got %d", l.Level, l.Samples)
		}
	}
	if pe.Total != 0 {
		t.Errorf("empty store total = %v, want 0", pe.Total)
	}
	if pe.Hotspot != "" {
		t.Errorf("empty store hotspot = %q, want empty", pe.Hotspot)
	}
}

// TestPredictiveError_OutcomeSurpriseDrivesHotspot verifies a refuted prediction raises
// the outcome level and makes it the hotspot, and that no-data levels are excluded from
// the aggregate.
func TestPredictiveError_OutcomeSurpriseDrivesHotspot(t *testing.T) {
	m := calibMem(t)
	ctx := context.Background()
	seedClaim(t, m, "b", 0.8)
	seedDecision(t, m, "d", "b")
	seedObservedExpectation(t, m, "b", 10, 100, 5) // far miss → refuted → high surprise

	pe, err := m.PredictiveError(ctx)
	if err != nil {
		t.Fatalf("PredictiveError: %v", err)
	}
	outcome := levelByName(pe, "outcome")
	if outcome.Samples != 1 {
		t.Fatalf("outcome samples = %d, want 1", outcome.Samples)
	}
	if outcome.Error <= 0 {
		t.Errorf("a refuted prediction should raise outcome error, got %v", outcome.Error)
	}
	// calibration has no adjudicated verdicts here → excluded from the total.
	if cal := levelByName(pe, "calibration"); cal.Samples != 0 {
		t.Errorf("calibration should have 0 samples, got %d", cal.Samples)
	}
	if pe.Total <= 0 {
		t.Errorf("total should reflect the outcome error, got %v", pe.Total)
	}
	if pe.Hotspot != "outcome" {
		t.Errorf("hotspot = %q, want outcome (the only high-error level)", pe.Hotspot)
	}
}

// TestPredictiveError_ValidatedPredictionLowersOutcome sanity-checks the direction: a
// borne-out prediction yields low outcome error.
func TestPredictiveError_ValidatedPredictionLowersOutcome(t *testing.T) {
	m := calibMem(t)
	ctx := context.Background()
	seedClaim(t, m, "b", 0.8)
	seedDecision(t, m, "d", "b")
	seedObservedExpectation(t, m, "b", 10, 10, 5) // exact hit → validated → ~0 surprise

	pe, err := m.PredictiveError(ctx)
	if err != nil {
		t.Fatalf("PredictiveError: %v", err)
	}
	outcome := levelByName(pe, "outcome")
	if outcome.Samples != 1 {
		t.Fatalf("outcome samples = %d, want 1", outcome.Samples)
	}
	if outcome.Error > 0.2 {
		t.Errorf("a validated prediction should have low outcome error, got %v", outcome.Error)
	}
}
