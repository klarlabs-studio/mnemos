package query

import (
	"context"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/llm"
)

// countingLLM is an llm.Client that counts completions. The grounded answer
// is the only text-generation call the query engine makes, so this counter is
// the number of paid model round-trips a query cost.
type countingLLM struct {
	calls atomic.Int64
}

func (c *countingLLM) Complete(_ context.Context, _ []llm.Message) (llm.Response, error) {
	c.calls.Add(1)
	return llm.Response{Content: "grounded answer [1]"}, nil
}

func answerTextFixture() Engine {
	now := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	events := fakeEventRepo{events: []domain.Event{
		{ID: "ev_1", RunID: "run_1", Content: "Revenue decreased after launch", Timestamp: now},
		{ID: "ev_2", RunID: "run_1", Content: "Churn increased in Q2", Timestamp: now.Add(time.Minute)},
	}}
	claims := []domain.Claim{
		{ID: "cl_1", Text: "Revenue decreased after launch", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive, Confidence: 0.8, CreatedAt: now},
		{ID: "cl_2", Text: "Churn increased in Q2", Type: domain.ClaimTypeFact, Status: domain.ClaimStatusActive, Confidence: 0.7, CreatedAt: now},
	}
	return NewEngine(events, fakeClaimRepo{claims: claims}, fakeRelationshipRepo{rels: map[string][]domain.Relationship{}})
}

func answerClaimIDs(ans domain.Answer) []string {
	ids := make([]string, 0, len(ans.Claims))
	for _, c := range ans.Claims {
		ids = append(ids, c.ID)
	}
	return ids
}

// The answer path keeps its behaviour: with an LLM wired, one query makes
// exactly one grounded-answer completion and returns its text.
func TestAnswerWithOptions_LLMGroundsAnswerTextOnce(t *testing.T) {
	client := &countingLLM{}
	engine := answerTextFixture().WithLLM(client)

	ans, err := engine.AnswerWithOptions(context.Background(), "what happened to revenue", AnswerOptions{})
	if err != nil {
		t.Fatalf("AnswerWithOptions: %v", err)
	}
	if got := client.calls.Load(); got != 1 {
		t.Fatalf("completions = %d, want 1", got)
	}
	if ans.AnswerText != "grounded answer [1]" {
		t.Fatalf("AnswerText = %q, want the grounded answer", ans.AnswerText)
	}
}

// SkipAnswerText is the retrieval-only form (#472): no completion, no answer
// text, and the SAME ranked claims the answer path returns.
func TestAnswerWithOptions_SkipAnswerTextMakesNoCompletion(t *testing.T) {
	ctx := context.Background()
	const question = "what happened to revenue"

	for _, tc := range []struct {
		name string
		run  func(Engine, AnswerOptions) (domain.Answer, error)
	}{
		{"AnswerWithOptions", func(e Engine, o AnswerOptions) (domain.Answer, error) {
			return e.AnswerWithOptions(ctx, question, o)
		}},
		{"AnswerForRunWithOptions", func(e Engine, o AnswerOptions) (domain.Answer, error) {
			return e.AnswerForRunWithOptions(ctx, question, "run_1", o)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withAnswer, err := tc.run(answerTextFixture().WithLLM(&countingLLM{}), AnswerOptions{})
			if err != nil {
				t.Fatalf("answer path: %v", err)
			}

			client := &countingLLM{}
			retrieved, err := tc.run(answerTextFixture().WithLLM(client), AnswerOptions{SkipAnswerText: true})
			if err != nil {
				t.Fatalf("retrieval-only path: %v", err)
			}
			if got := client.calls.Load(); got != 0 {
				t.Fatalf("completions = %d, want 0", got)
			}
			if retrieved.AnswerText != "" {
				t.Fatalf("AnswerText = %q, want empty when SkipAnswerText is set", retrieved.AnswerText)
			}
			want, got := answerClaimIDs(withAnswer), answerClaimIDs(retrieved)
			if len(want) == 0 {
				t.Fatal("fixture returned no claims; the ranking comparison would be vacuous")
			}
			if len(want) != len(got) {
				t.Fatalf("claims = %v, want %v", got, want)
			}
			for i := range want {
				if want[i] != got[i] {
					t.Fatalf("claims = %v, want %v (ranking changed)", got, want)
				}
			}
			// Confidence decays with wall-clock time, and the two answers are
			// computed moments apart, so equal means equal to within that drift.
			if math.Abs(retrieved.Confidence-withAnswer.Confidence) > 1e-6 {
				t.Fatalf("Confidence = %v, want %v", retrieved.Confidence, withAnswer.Confidence)
			}
		})
	}
}
