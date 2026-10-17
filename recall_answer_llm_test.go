package mnemos_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"go.klarlabs.de/mnemos"
	"go.klarlabs.de/mnemos/providers"
)

// recallCountingTextGen counts every text-generation call. It answers extraction
// prompts with one deterministic claim, which is also what it would "answer" a
// grounded-answer prompt with — the content does not matter, the count does.
type recallCountingTextGen struct {
	calls atomic.Int64
}

func (g *recallCountingTextGen) GenerateText(_ context.Context, _ providers.GenerateTextInput) (providers.GenerateTextOutput, error) {
	g.calls.Add(1)
	return providers.GenerateTextOutput{
		Content:      `[{"text": "the user prefers Go for backend services", "type": "fact", "confidence": 0.9}]`,
		Model:        "stub-v1",
		InputTokens:  10,
		OutputTokens: 20,
	}, nil
}

// TestRecall_MakesNoTextGenerationCalls pins #472: every Recall variant returns
// ranked beliefs only, so none of them may pay for a grounded LLM answer whose
// text they discard. Recall used to make one completion per call (two for an
// escalated RecallWithEffort) whenever the memory had a language model.
func TestRecall_MakesNoTextGenerationCalls(t *testing.T) {
	clearMnemosEnv(t)

	tg := &recallCountingTextGen{}
	mem, err := mnemos.New(
		mnemos.WithStorage(fmt.Sprintf("memory://?namespace=mnemos_recall_llm_%d", time.Now().UnixNano())),
		mnemos.WithSharedProvider(tg, nil),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = mem.Close() }()

	ctx := context.Background()
	// Unique content keeps the on-disk extraction cache from short-circuiting
	// the extractor, so the brain really holds a belief to recall.
	if err := mem.Remember(ctx, mnemos.Item{
		Type:    "fact",
		Content: fmt.Sprintf("Recall cost test %d: I prefer Go for backend services.", time.Now().UnixNano()),
	}); err != nil {
		t.Fatalf("Remember: %v", err)
	}

	q := mnemos.Query{Text: "which language does the user prefer for backend services", Limit: 5}
	for _, tc := range []struct {
		name string
		run  func() (int, error)
	}{
		{"Recall", func() (int, error) {
			r, err := mem.Recall(ctx, q)
			return len(r), err
		}},
		{"RecallWithSufficiency", func() (int, error) {
			r, _, err := mem.RecallWithSufficiency(ctx, q)
			return len(r), err
		}},
		{"RecallWithEffort", func() (int, error) {
			r, _, _, err := mem.RecallWithEffort(ctx, q, 1.0)
			return len(r), err
		}},
		{"RecallWithContext", func() (int, error) {
			r, err := mem.RecallWithContext(ctx, q, "backend services")
			return len(r), err
		}},
		{"RecallWithConflicts", func() (int, error) {
			r, _, err := mem.RecallWithConflicts(ctx, q)
			return len(r), err
		}},
		{"RecallIterative", func() (int, error) {
			r, _, err := mem.RecallIterative(ctx, q, 2)
			return len(r), err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := tg.calls.Load()
			n, err := tc.run()
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if n == 0 {
				t.Fatal("recalled nothing; with no claims the engine skips the answer call anyway, so the assertion below would be vacuous")
			}
			if got := tg.calls.Load() - before; got != 0 {
				t.Fatalf("%s made %d text-generation call(s), want 0", tc.name, got)
			}
		})
	}
}
