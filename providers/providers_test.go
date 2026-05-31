package providers_test

import (
	"context"
	"testing"

	"github.com/felixgeelhaar/mnemos/providers"
)

// stubTextGenerator demonstrates the minimal adapter pattern for an
// agent runtime consuming Mnemos with a shared model provider.
type stubTextGenerator struct {
	calls int
}

func (g *stubTextGenerator) GenerateText(
	_ context.Context,
	in providers.GenerateTextInput,
) (providers.GenerateTextOutput, error) {
	g.calls++
	last := ""
	if n := len(in.Messages); n > 0 {
		last = in.Messages[n-1].Content
	}
	return providers.GenerateTextOutput{
		Content:      "echo: " + last,
		Model:        "stub-v1",
		InputTokens:  len(last),
		OutputTokens: len(last) + 6,
	}, nil
}

type stubEmbedder struct {
	dim int
}

func (e *stubEmbedder) Embed(
	_ context.Context,
	in providers.EmbedInput,
) (providers.EmbedOutput, error) {
	vecs := make([][]float32, len(in.Texts))
	for i, t := range in.Texts {
		v := make([]float32, e.dim)
		for j := range v {
			v[j] = float32((len(t) + i + j) % 7)
		}
		vecs[i] = v
	}
	return providers.EmbedOutput{Vectors: vecs, Model: "stub-emb-v1"}, nil
}

func TestTextGenerator_Adapter_Satisfies_Interface(t *testing.T) {
	t.Parallel()
	var tg providers.TextGenerator = &stubTextGenerator{}
	out, err := tg.GenerateText(context.Background(), providers.GenerateTextInput{
		Messages: []providers.Message{
			{Role: providers.RoleSystem, Content: "you are a helper"},
			{Role: providers.RoleUser, Content: "hello"},
		},
	})
	if err != nil {
		t.Fatalf("GenerateText: %v", err)
	}
	if out.Content != "echo: hello" {
		t.Errorf("Content = %q, want 'echo: hello'", out.Content)
	}
	if out.Model != "stub-v1" {
		t.Errorf("Model = %q, want stub-v1", out.Model)
	}
}

func TestEmbedder_Adapter_Satisfies_Interface(t *testing.T) {
	t.Parallel()
	var emb providers.Embedder = &stubEmbedder{dim: 4}
	out, err := emb.Embed(context.Background(), providers.EmbedInput{
		Texts: []string{"alpha", "beta"},
	})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(out.Vectors) != 2 {
		t.Fatalf("len(Vectors) = %d, want 2", len(out.Vectors))
	}
	for i, v := range out.Vectors {
		if len(v) != 4 {
			t.Errorf("Vectors[%d] dim = %d, want 4", i, len(v))
		}
	}
	if out.Model != "stub-emb-v1" {
		t.Errorf("Model = %q, want stub-emb-v1", out.Model)
	}
}
