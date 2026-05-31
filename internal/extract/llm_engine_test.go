package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/felixgeelhaar/mnemos/internal/domain"
	"github.com/felixgeelhaar/mnemos/internal/llm"
)

// mockLLMClient is a test double for llm.Client.
type mockLLMClient struct {
	response string
	err      error
	calls    int
}

func (m *mockLLMClient) Complete(_ context.Context, _ []llm.Message) (llm.Response, error) {
	m.calls++
	if m.err != nil {
		return llm.Response{}, m.err
	}
	return llm.Response{Content: m.response, Model: "test-model", InputTokens: 10, OutputTokens: 5}, nil
}

func TestLLMEngineExtractFromJSON(t *testing.T) {
	claims := []llmClaim{
		{Text: "Revenue grew 15% in Q3", Type: "fact", Confidence: 0.92},
		{Text: "We will migrate to PostgreSQL", Type: "decision", Confidence: 0.88},
		{Text: "Users might prefer dark mode", Type: "hypothesis", Confidence: 0.6},
	}
	responseJSON, _ := json.Marshal(claims)

	client := &mockLLMClient{response: string(responseJSON)}
	engine := newTestLLMEngine(client)

	events := []domain.Event{
		{ID: "ev_1", Content: "Revenue grew 15% in Q3. We will migrate to PostgreSQL. Users might prefer dark mode."},
	}

	gotClaims, gotEvidence, err := engine.Extract(events)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(gotClaims) != 3 {
		t.Fatalf("expected 3 claims, got %d", len(gotClaims))
	}
	if len(gotEvidence) != 3 {
		t.Fatalf("expected 3 evidence links, got %d", len(gotEvidence))
	}

	// Verify types.
	expectedTypes := []domain.ClaimType{domain.ClaimTypeFact, domain.ClaimTypeDecision, domain.ClaimTypeHypothesis}
	for i, claim := range gotClaims {
		if claim.Type != expectedTypes[i] {
			t.Errorf("claim %d: got type %q, want %q", i, claim.Type, expectedTypes[i])
		}
	}

	// Verify confidence clamping.
	if gotClaims[2].Confidence < 0.5 {
		t.Errorf("hypothesis confidence %f should be clamped to >= 0.5", gotClaims[2].Confidence)
	}

	// Verify evidence links back to the source event.
	for _, ev := range gotEvidence {
		if ev.EventID != "ev_1" {
			t.Errorf("evidence should link to ev_1, got %q", ev.EventID)
		}
	}
}

func TestLLMEngineDeduplicates(t *testing.T) {
	claims := []llmClaim{
		{Text: "Revenue grew 15%", Type: "fact", Confidence: 0.9},
		{Text: "Revenue grew 15%", Type: "fact", Confidence: 0.9}, // duplicate
	}
	responseJSON, _ := json.Marshal(claims)

	client := &mockLLMClient{response: string(responseJSON)}
	engine := newTestLLMEngine(client)

	events := []domain.Event{{ID: "ev_1", Content: "Revenue grew 15%."}}
	gotClaims, _, err := engine.Extract(events)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gotClaims) != 1 {
		t.Fatalf("expected 1 claim after dedup, got %d", len(gotClaims))
	}
}

func TestLLMEngineFallsBackOnError(t *testing.T) {
	client := &mockLLMClient{err: fmt.Errorf("network timeout")}
	engine := newTestLLMEngine(client)

	events := []domain.Event{
		{ID: "ev_1", Content: "We will use React for the frontend"},
	}

	gotClaims, gotEvidence, err := engine.Extract(events)
	if err != nil {
		t.Fatalf("should not error on fallback: %v", err)
	}

	// Rule-based engine should still produce at least one claim.
	if len(gotClaims) == 0 {
		t.Fatal("expected at least 1 claim from rule-based fallback")
	}
	if len(gotEvidence) == 0 {
		t.Fatal("expected at least 1 evidence link from rule-based fallback")
	}
}

func TestLLMEngineFallsBackOnBadJSON(t *testing.T) {
	client := &mockLLMClient{response: "this is not JSON"}
	engine := newTestLLMEngine(client)

	events := []domain.Event{
		{ID: "ev_1", Content: "We decided to use Go for the backend"},
	}

	gotClaims, _, err := engine.Extract(events)
	if err != nil {
		t.Fatalf("should not error on fallback: %v", err)
	}
	if len(gotClaims) == 0 {
		t.Fatal("expected at least 1 claim from rule-based fallback")
	}
}

func TestLLMEngineStripsMarkdownFences(t *testing.T) {
	claims := []llmClaim{
		{Text: "The server runs on port 8080", Type: "fact", Confidence: 0.85},
	}
	responseJSON, _ := json.Marshal(claims)
	fenced := "```json\n" + string(responseJSON) + "\n```"

	client := &mockLLMClient{response: fenced}
	engine := newTestLLMEngine(client)

	events := []domain.Event{{ID: "ev_1", Content: "The server runs on port 8080."}}
	gotClaims, _, err := engine.Extract(events)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gotClaims) != 1 {
		t.Fatalf("expected 1 claim, got %d", len(gotClaims))
	}
}

func TestLLMEngineContestedDetection(t *testing.T) {
	claims := []llmClaim{
		{Text: "The database supports transactions", Type: "fact", Confidence: 0.9},
		{Text: "The database does not support transactions", Type: "fact", Confidence: 0.85},
	}
	responseJSON, _ := json.Marshal(claims)

	client := &mockLLMClient{response: string(responseJSON)}
	engine := newTestLLMEngine(client)

	events := []domain.Event{{ID: "ev_1", Content: "The database supports transactions. The database does not support transactions."}}
	gotClaims, _, err := engine.Extract(events)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gotClaims) != 2 {
		t.Fatalf("expected 2 claims, got %d", len(gotClaims))
	}

	for _, claim := range gotClaims {
		if claim.Status != domain.ClaimStatusContested {
			t.Errorf("claim %q should be contested", claim.Text)
		}
	}
}

func TestLLMEngineMultiEventMatching(t *testing.T) {
	claims := []llmClaim{
		{Text: "Revenue grew 15%", Type: "fact", Confidence: 0.9},
		{Text: "We chose PostgreSQL", Type: "decision", Confidence: 0.88},
	}
	responseJSON, _ := json.Marshal(claims)

	client := &mockLLMClient{response: string(responseJSON)}
	engine := newTestLLMEngine(client)

	events := []domain.Event{
		{ID: "ev_1", Content: "Revenue grew 15% this quarter"},
		{ID: "ev_2", Content: "We chose PostgreSQL for the database"},
	}

	_, gotEvidence, err := engine.Extract(events)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gotEvidence) != 2 {
		t.Fatalf("expected 2 evidence links, got %d", len(gotEvidence))
	}

	// Revenue claim should link to ev_1, PostgreSQL to ev_2.
	if gotEvidence[0].EventID != "ev_1" {
		t.Errorf("first claim should link to ev_1, got %q", gotEvidence[0].EventID)
	}
	if gotEvidence[1].EventID != "ev_2" {
		t.Errorf("second claim should link to ev_2, got %q", gotEvidence[1].EventID)
	}
}

func TestLLMEngineEmptyResponse(t *testing.T) {
	client := &mockLLMClient{response: "[]"}
	engine := newTestLLMEngine(client)

	events := []domain.Event{{ID: "ev_1", Content: "Hello world"}}
	gotClaims, gotEvidence, err := engine.Extract(events)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gotClaims) != 0 {
		t.Fatalf("expected 0 claims, got %d", len(gotClaims))
	}
	if len(gotEvidence) != 0 {
		t.Fatalf("expected 0 evidence, got %d", len(gotEvidence))
	}
}

func TestLLMEngineUsesPersistentCache(t *testing.T) {
	claims := []llmClaim{{Text: "Revenue grew 15% in Q3", Type: "fact", Confidence: 0.92}}
	responseJSON, _ := json.Marshal(claims)

	client := &mockLLMClient{response: string(responseJSON)}
	engine := newTestLLMEngine(client)
	engine.cacheDir = t.TempDir()
	t.Setenv("MNEMOS_LLM_PROVIDER", "openai")
	t.Setenv("MNEMOS_LLM_MODEL", "gpt-4o-mini")

	events := []domain.Event{{ID: "ev_1", Content: "Revenue grew 15% in Q3."}}
	if _, _, err := engine.Extract(events); err != nil {
		t.Fatalf("first extract error: %v", err)
	}
	if _, _, err := engine.Extract(events); err != nil {
		t.Fatalf("second extract error: %v", err)
	}
	if client.calls != 1 {
		t.Fatalf("expected 1 LLM call with cache hit, got %d", client.calls)
	}
}

// TestLLMEngineUsageSinkFires verifies WithUsageSink receives the
// token counts from the underlying provider on a successful call. The
// pipeline layer relies on this to surface tokens as axi-go capability
// evidence; without it, MaxTokens budgets silently never engage.
func TestLLMEngineUsageSinkFires(t *testing.T) {
	claims := []llmClaim{{Text: "Revenue grew 15%", Type: "fact", Confidence: 0.9}}
	responseJSON, _ := json.Marshal(claims)
	client := &mockLLMClient{response: string(responseJSON)}

	var got TokenUsage
	var hits int
	engine := newTestLLMEngine(client).WithUsageSink(func(u TokenUsage) {
		got = u
		hits++
	})

	events := []domain.Event{{ID: "ev_1", Content: "Revenue grew 15%."}}
	if _, _, _, err := engine.ExtractWithEntities(events); err != nil {
		t.Fatalf("ExtractWithEntities: %v", err)
	}

	if hits != 1 {
		t.Fatalf("sink invocations = %d, want 1", hits)
	}
	if got.InputTokens != 10 || got.OutputTokens != 5 {
		t.Errorf("usage = %+v, want {Input:10 Output:5}", got)
	}
	if got.Model != "test-model" {
		t.Errorf("model = %q, want test-model", got.Model)
	}
}

// TestLLMEngineUsageSinkSkippedOnFallback verifies the sink does NOT
// fire when the LLM call errors and the rule-based fallback runs.
// Pinning this behaviour keeps the token-evidence path honest: zero
// tokens reported means zero tokens actually consumed.
func TestLLMEngineUsageSinkSkippedOnFallback(t *testing.T) {
	client := &mockLLMClient{err: fmt.Errorf("provider down")}
	var hits int
	engine := newTestLLMEngine(client).WithUsageSink(func(_ TokenUsage) {
		hits++
	})

	events := []domain.Event{{ID: "ev_1", Content: "We will use React for the frontend"}}
	if _, _, _, err := engine.ExtractWithEntities(events); err != nil {
		t.Fatalf("ExtractWithEntities: %v", err)
	}

	if hits != 0 {
		t.Errorf("sink invocations = %d, want 0 on fallback", hits)
	}
}

// newTestLLMEngine creates an LLMEngine with deterministic IDs and clock.
func newTestLLMEngine(client llm.Client) LLMEngine {
	seq := 0
	engine := NewLLMEngine(client)
	engine.now = func() time.Time { return time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC) }
	engine.cacheDir = ""
	engine.nextID = func() (string, error) {
		seq++
		return fmt.Sprintf("cl_test_%03d", seq), nil
	}
	return engine
}
