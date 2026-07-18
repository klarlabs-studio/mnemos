package query

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
)

func TestInhibitionScoreDelta(t *testing.T) {
	// Absent component → inert.
	if d := inhibitionScoreDelta(domain.Claim{}); d != 0 {
		t.Errorf("unsuppressed claim should contribute 0, got %v", d)
	}
	// Present → bounded, negative, proportional.
	c := domain.Claim{ConfidenceComponents: map[string]float64{domain.InhibitionComponentKey: 0.5}}
	got := inhibitionScoreDelta(c)
	if got >= 0 {
		t.Errorf("suppressed claim should lose score, got %v", got)
	}
	if want := -inhibitionRetrievalWeight * 0.5; got != want {
		t.Errorf("delta = %v, want %v", got, want)
	}
	// A maximally suppressed claim can't exceed the bound (tips ties, not sinks matches).
	full := inhibitionScoreDelta(domain.Claim{ConfidenceComponents: map[string]float64{domain.InhibitionComponentKey: 1}})
	if full < -inhibitionRetrievalWeight {
		t.Errorf("penalty must stay within the bound, got %v", full)
	}
}

// TestInhibition_SuppressesLoserOnAgentResolution verifies the ADR-0016 write-back:
// an agent-mode query that decisively resolves a contradiction suppresses the loser's
// retrievability (writes an inhibition component) — but only with --inhibit, and never
// the winner.
func TestInhibition_SuppressesLoserOnAgentResolution(t *testing.T) {
	now := time.Now().UTC()
	events, claimRepo, relRepo := makeContradictingSetup(now, 0.92, 0.71)
	writes := map[string]map[string]float64{}
	claimRepo.creditWrites = &writes
	engine := NewEngine(events, claimRepo, relRepo)

	// Off: no suppression.
	if _, err := engine.AnswerWithOptions(context.Background(), "deployment strategy", AnswerOptions{Consumer: domain.ConsumerAgent}); err != nil {
		t.Fatalf("answer: %v", err)
	}
	if len(writes) != 0 {
		t.Fatalf("inhibit off must not write, got %v", writes)
	}

	// On: the beaten loser is suppressed; the winner is not.
	if _, err := engine.AnswerWithOptions(context.Background(), "deployment strategy", AnswerOptions{Consumer: domain.ConsumerAgent, Inhibit: true}); err != nil {
		t.Fatalf("answer: %v", err)
	}
	comps, ok := writes["cl_low"]
	if !ok {
		t.Fatalf("loser cl_low should be inhibited; writes=%v", writes)
	}
	if comps[domain.InhibitionComponentKey] <= 0 {
		t.Errorf("cl_low inhibition should be positive, got %v", comps[domain.InhibitionComponentKey])
	}
	if _, w := writes["cl_high"]; w {
		t.Error("winner cl_high must never be inhibited")
	}
}

// TestInhibition_ProtectsPromotedLoser verifies the guardrail: a human-endorsed
// (promoted) loser is never suppressed.
func TestInhibition_ProtectsPromotedLoser(t *testing.T) {
	now := time.Now().UTC()
	events, claimRepo, relRepo := makeContradictingSetup(now, 0.92, 0.71)
	for i := range claimRepo.claims {
		if claimRepo.claims[i].ID == "cl_low" {
			claimRepo.claims[i].Lifecycle = domain.ClaimLifecyclePromoted
		}
	}
	writes := map[string]map[string]float64{}
	claimRepo.creditWrites = &writes
	engine := NewEngine(events, claimRepo, relRepo)

	if _, err := engine.AnswerWithOptions(context.Background(), "deployment strategy", AnswerOptions{Consumer: domain.ConsumerAgent, Inhibit: true}); err != nil {
		t.Fatalf("answer: %v", err)
	}
	if _, w := writes["cl_low"]; w {
		t.Errorf("a promoted loser must never be inhibited; writes=%v", writes)
	}
}
