package extract

import (
	"context"
	"fmt"
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/llm"
)

// #451: a failed LLM extraction falls back to rule-based claims for that
// batch, deliberately, so a capture still stores something. The fallback must
// be visible: counted for `mnemos serve` to export, and logged.
func TestLLMEngineFallbackIsCounted(t *testing.T) {
	before := Fallbacks()
	engine := newTestLLMEngine(&mockLLMClient{err: fmt.Errorf("network timeout")})
	events := []domain.Event{{ID: "ev_1", Content: "We will use React for the frontend"}}
	if _, _, err := engine.Extract(context.Background(), events); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if got := Fallbacks() - before; got != 1 {
		t.Fatalf("fallbacks counted = %d, want 1", got)
	}
}

// A permanent provider error is not retried: the account will not have more
// credit in 200 ms.
func TestLLMEngineDoesNotRetryAPermanentError(t *testing.T) {
	client := &mockLLMClient{err: fmt.Errorf("complete: %w", &llm.StatusError{Provider: "anthropic", Status: 402, Body: "in_flight_budget_exhausted"})}
	engine := newTestLLMEngine(client)
	events := []domain.Event{{ID: "ev_1", Content: "We will use React for the frontend"}}
	claims, _, err := engine.Extract(context.Background(), events)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if client.calls != 1 {
		t.Errorf("a 402 was attempted %d times, want 1", client.calls)
	}
	if len(claims) == 0 {
		t.Error("the batch should still fall back to rule-based claims")
	}
}

// A transient error is still retried.
func TestLLMEngineRetriesATransientError(t *testing.T) {
	client := &mockLLMClient{err: fmt.Errorf("complete: %w", &llm.StatusError{Provider: "anthropic", Status: 503, Body: "overloaded"})}
	engine := newTestLLMEngine(client)
	events := []domain.Event{{ID: "ev_1", Content: "We will use React for the frontend"}}
	if _, _, err := engine.Extract(context.Background(), events); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if client.calls != 3 {
		t.Errorf("a 503 was attempted %d times, want 3", client.calls)
	}
}
