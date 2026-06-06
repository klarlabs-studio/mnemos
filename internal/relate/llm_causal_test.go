package relate

import (
	"context"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/llm"
)

type stubLLM struct {
	answer string
	calls  int
}

func (s *stubLLM) Complete(_ context.Context, _ []llm.Message) (llm.Response, error) {
	s.calls++
	return llm.Response{Content: s.answer}, nil
}

func TestDetectCausalLLM_NilClientFallsBack(t *testing.T) {
	e := NewEngine()
	at := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	claims := []domain.Claim{
		{ID: "a", Text: "deployed payments service", ValidFrom: at},
		{ID: "b", Text: "payments latency spiked", ValidFrom: at.Add(time.Minute)},
	}
	rels, err := e.DetectCausalLLM(context.Background(), claims, nil)
	if err != nil {
		t.Fatalf("DetectCausalLLM: %v", err)
	}
	if len(rels) != 1 || rels[0].Type != domain.RelationshipTypeCauses {
		t.Fatalf("nil client should match heuristic, got %+v", rels)
	}
}

func TestDetectCausalLLM_AddsBorderlineWhenLLMSaysYes(t *testing.T) {
	e := NewEngine()
	at := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	// Zero shared non-noise tokens after noise filter, so the
	// heuristic drops the pair. LLM disambiguates by reading the
	// language semantics ("indexer" implied by "search worker").
	claims := []domain.Claim{
		{ID: "a", Text: "restarted search worker", ValidFrom: at},
		{ID: "b", Text: "indexer latency dropped", ValidFrom: at.Add(2 * time.Minute)},
	}
	stub := &stubLLM{answer: `{"causes": true}`}
	rels, err := e.DetectCausalLLM(context.Background(), claims, stub)
	if err != nil {
		t.Fatalf("DetectCausalLLM: %v", err)
	}
	if stub.calls == 0 {
		t.Fatal("expected at least one LLM call for borderline pair")
	}
	found := false
	for _, r := range rels {
		if r.Type == domain.RelationshipTypeCauses && r.FromClaimID == "a" && r.ToClaimID == "b" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("LLM yes should yield causes edge a->b, got %+v", rels)
	}
}

func TestDetectCausalLLM_RejectsWhenLLMSaysNo(t *testing.T) {
	e := NewEngine()
	at := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	// Same zero-overlap borderline pair; LLM rejects.
	claims := []domain.Claim{
		{ID: "a", Text: "restarted search worker", ValidFrom: at},
		{ID: "b", Text: "indexer latency dropped", ValidFrom: at.Add(2 * time.Minute)},
	}
	stub := &stubLLM{answer: `{"causes": false}`}
	rels, err := e.DetectCausalLLM(context.Background(), claims, stub)
	if err != nil {
		t.Fatalf("DetectCausalLLM: %v", err)
	}
	for _, r := range rels {
		if r.FromClaimID == "a" && r.ToClaimID == "b" {
			t.Fatalf("LLM no should suppress causes edge a->b, got %+v", r)
		}
	}
}

func TestParseCausesYesNo_HandlesProseWrappedJSON(t *testing.T) {
	if !parseCausesYesNo(`Sure, here is my answer: {"causes": true}.`) {
		t.Fatal("expected true for prose-wrapped json")
	}
	if parseCausesYesNo(`{"causes": false, "reason": "unrelated"}`) {
		t.Fatal("expected false")
	}
	if parseCausesYesNo("") {
		t.Fatal("empty should be false")
	}
}
