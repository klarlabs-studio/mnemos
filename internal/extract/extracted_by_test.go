package extract

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/llm"
)

// producerClient is a fake LLM client that reports a provider identity, as
// every real client does, including a base URL the producer must not store.
type producerClient struct{ model string }

func (c producerClient) Complete(context.Context, []llm.Message) (llm.Response, error) {
	body, _ := json.Marshal([]llmClaim{{Text: "The billing service retries webhooks three times", Type: "fact", Confidence: 0.9}})
	return llm.Response{Content: string(body), Model: c.model}, nil
}

func (c producerClient) Identity() string {
	return "anthropic|claude-test|https://internal.example.com"
}

type downClient struct{}

func (downClient) Complete(context.Context, []llm.Message) (llm.Response, error) {
	return llm.Response{}, errors.New("provider down")
}

// TestExtractedBy pins #445 item 3: every extractor records itself on the
// claims it produces (ADR 0029 §3), so a bad extractor's claims can be found
// and re-extracted. The LLM stamp names provider, model and prompt version,
// never the endpoint; a batch that fell back to the rules carries the rules'
// stamp, because that is what produced it.
func TestExtractedBy(t *testing.T) {
	events := []domain.Event{{ID: "ev_1", Content: "The billing service retries webhooks three times."}}
	tests := []struct {
		name   string
		engine func() interface {
			Extract(context.Context, []domain.Event) ([]domain.Claim, []domain.ClaimEvidence, error)
		}
		want string
	}{
		{
			name: "rule-based",
			engine: func() interface {
				Extract(context.Context, []domain.Event) ([]domain.Claim, []domain.ClaimEvidence, error)
			} {
				return NewEngine()
			},
			want: RuleExtractorVersion,
		},
		{
			name: "llm with identity",
			engine: func() interface {
				Extract(context.Context, []domain.Event) ([]domain.Claim, []domain.ClaimEvidence, error)
			} {
				return newTestLLMEngine(producerClient{model: "claude-test-20261101"})
			},
			want: "llm/anthropic:claude-test/" + PromptVersion,
		},
		{
			name: "llm without identity uses the response model",
			engine: func() interface {
				Extract(context.Context, []domain.Event) ([]domain.Claim, []domain.ClaimEvidence, error)
			} {
				return newTestLLMEngine(&countingLLMClient{})
			},
			want: "llm/test-model/" + PromptVersion,
		},
		{
			name: "llm fallback carries the rules stamp",
			engine: func() interface {
				Extract(context.Context, []domain.Event) ([]domain.Claim, []domain.ClaimEvidence, error)
			} {
				return newTestLLMEngine(downClient{})
			},
			want: RuleExtractorVersion,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			claims, _, err := tc.engine().Extract(context.Background(), events)
			if err != nil {
				t.Fatalf("Extract() error = %v", err)
			}
			if len(claims) == 0 {
				t.Fatal("no claims extracted")
			}
			for _, c := range claims {
				if c.ExtractedBy != tc.want {
					t.Errorf("claim %q ExtractedBy = %q, want %q", c.Text, c.ExtractedBy, tc.want)
				}
			}
		})
	}
}
