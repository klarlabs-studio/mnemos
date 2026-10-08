package mnemos

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

// retiredCorpus adds n beliefs that are retired: half deprecated, half with
// valid time closed. None takes part in a contradiction.
func retiredCorpus(t *testing.T, m *memory, n int) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	cs := make([]domain.Claim, 0, n)
	for i := 0; i < n; i++ {
		c := domain.Claim{
			ID: fmt.Sprintf("retired-%04d", i), Text: fmt.Sprintf("retired belief %d", i),
			Type: domain.ClaimTypeFact, Confidence: 0.6, TrustScore: 0.5,
			Status: domain.ClaimStatusActive, CreatedAt: now, ValidFrom: now,
		}
		if i%2 == 0 {
			c.Status = domain.ClaimStatusDeprecated
		}
		cs = append(cs, c)
	}
	putClaims(t, m, cs)
	for i := 1; i < n; i += 2 {
		if err := m.conn.Claims.SetValidity(ctx, fmt.Sprintf("retired-%04d", i), now); err != nil {
			t.Fatalf("close validity: %v", err)
		}
	}
}

func dissonanceLevel(t *testing.T, m *memory) PredictiveErrorLevel {
	t.Helper()
	pe, err := m.PredictiveError(context.Background())
	if err != nil {
		t.Fatalf("PredictiveError: %v", err)
	}
	for _, l := range pe.Levels {
		if l.Level == "dissonance" {
			return l
		}
	}
	t.Fatal("no dissonance level")
	return PredictiveErrorLevel{}
}

// The dissonance rate is live contradictions per LIVE belief. Retiring
// unrelated beliefs must not move it: the numerator already ignores them, and
// a denominator that counted them made the rate fall as a brain retired
// knowledge, while the contradictions it reports were unchanged.
func TestPredictiveError_DissonanceIgnoresRetiredBeliefs(t *testing.T) {
	clean := boundsMem(t, "diss_clean")
	hyperCorpus(t, clean, 10) // 10 alerts over 20 live beliefs
	withRetired := boundsMem(t, "diss_retired")
	hyperCorpus(t, withRetired, 10)
	retiredCorpus(t, withRetired, 180)

	a, b := dissonanceLevel(t, clean), dissonanceLevel(t, withRetired)
	if a.Error != 0.5 {
		t.Fatalf("baseline dissonance = %v, want 10/20 = 0.5", a.Error)
	}
	if b.Error != a.Error || b.Samples != a.Samples {
		t.Errorf("retiring 180 unrelated beliefs moved dissonance from %v (n=%d) to %v (n=%d); want unchanged",
			a.Error, a.Samples, b.Error, b.Samples)
	}
}

// A brain whose every belief is retired has no live population to measure
// dissonance over: unknown, not a clean 0.
func TestBrainHealth_DissonanceUnknownWhenNothingIsLive(t *testing.T) {
	m := boundsMem(t, "diss_all_retired")
	retiredCorpus(t, m, 20)
	h, err := m.BrainHealth(context.Background())
	if err != nil {
		t.Fatalf("BrainHealth: %v", err)
	}
	if d := vitalByName(h, "dissonance"); d.Status != HealthUnknown {
		t.Errorf("dissonance with no live beliefs = %+v, want unknown", d)
	}
}
