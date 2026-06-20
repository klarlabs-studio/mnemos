package mnemos

import (
	"testing"

	"go.klarlabs.de/mnemos/internal/extract"
)

// TestLLMEvidence_NilAndZeroReturnsNil pins the rule-based / cache-hit
// path: no token usage means no "llm" evidence record, so a passive write
// never advances the token budget.
func TestLLMEvidence_NilAndZeroReturnsNil(t *testing.T) {
	t.Parallel()
	if recs := llmEvidence(nil); recs != nil {
		t.Errorf("llmEvidence(nil) = %v, want nil", recs)
	}
	if recs := llmEvidence(&extract.TokenUsage{}); recs != nil {
		t.Errorf("llmEvidence(zero usage) = %v, want nil", recs)
	}
}

// TestLLMEvidence_ReportsTokens covers the populated path including the
// empty-model -> "unknown" source fallback.
func TestLLMEvidence_ReportsTokens(t *testing.T) {
	t.Parallel()
	recs := llmEvidence(&extract.TokenUsage{InputTokens: 5, OutputTokens: 7})
	if len(recs) != 1 {
		t.Fatalf("want 1 evidence record, got %d", len(recs))
	}
	if recs[0].Kind != "llm" {
		t.Errorf("Kind = %q, want llm", recs[0].Kind)
	}
	if recs[0].Source != "unknown" {
		t.Errorf("empty model should map Source to %q, got %q", "unknown", recs[0].Source)
	}
	if recs[0].TokensUsed != 12 {
		t.Errorf("TokensUsed = %d, want 12 (5+7)", recs[0].TokensUsed)
	}

	named := llmEvidence(&extract.TokenUsage{InputTokens: 1, Model: "claude"})
	if named[0].Source != "claude" {
		t.Errorf("Source = %q, want claude", named[0].Source)
	}
}

// TestWriteInput_TypeMismatch covers the unbox guard: a payload of the
// wrong type returns ok=false rather than panicking.
func TestWriteInput_TypeMismatch(t *testing.T) {
	t.Parallel()
	// Not a map at all.
	if _, ok := writeInput[rememberInput]("nope"); ok {
		t.Error("writeInput should reject a non-map input")
	}
	// Map, but the payload is the wrong concrete type.
	in := map[string]any{inputPayloadKey: rememberEventInput{}}
	if _, ok := writeInput[rememberInput](in); ok {
		t.Error("writeInput should reject a payload of the wrong type")
	}
	// Correct shape unboxes.
	good := map[string]any{inputPayloadKey: rememberInput{}}
	if _, ok := writeInput[rememberInput](good); !ok {
		t.Error("writeInput should accept a correctly-boxed payload")
	}
}
