package providers_test

import (
	"context"
	"testing"

	"go.klarlabs.de/mnemos/providers"
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

// stubSingleEmbedder implements the spec-shaped single-text
// SingleEmbedder signature: Embed(ctx, text) ([]float32, error).
type stubSingleEmbedder struct {
	dim   int
	calls int
}

func (e *stubSingleEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	e.calls++
	v := make([]float32, e.dim)
	for j := range v {
		v[j] = float32((len(text) + j) % 7)
	}
	return v, nil
}

// TestSingleEmbedder_AsEmbedder verifies a single-text embedder (the
// spec's Embed(ctx, text string) ([]float32, error) signature) can be
// adapted into the batch providers.Embedder Mnemos consumes, with one
// call per input text and order preserved.
func TestSingleEmbedder_AsEmbedder(t *testing.T) {
	t.Parallel()
	single := &stubSingleEmbedder{dim: 3}

	// EmbedderFromSingle returns providers.Embedder, so this also proves
	// the adapter satisfies the batch interface at compile time.
	emb := providers.EmbedderFromSingle(single)
	out, err := emb.Embed(context.Background(), providers.EmbedInput{
		Texts: []string{"alpha", "beta", "gamma"},
	})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(out.Vectors) != 3 {
		t.Fatalf("len(Vectors) = %d, want 3", len(out.Vectors))
	}
	for i, v := range out.Vectors {
		if len(v) != 3 {
			t.Errorf("Vectors[%d] dim = %d, want 3", i, len(v))
		}
	}
	if single.calls != 3 {
		t.Errorf("single embedder called %d times, want 3 (one per text)", single.calls)
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

// singleVec is a trivial SingleEmbedder for the model-id tests.
type singleVec struct{}

func (singleVec) Embed(_ context.Context, _ string) ([]float32, error) {
	return []float32{1, 0, 0}, nil
}

// TestEmbedderFromSingleWithModel_ReportsModel verifies the model id flows
// through both EmbedOutput.Model and the ModelIdentifier capability, so
// Mnemos can stamp + filter recall by it. The plain EmbedderFromSingle
// remains unnamed (empty model) for backward compatibility.
func TestEmbedderFromSingleWithModel_ReportsModel(t *testing.T) {
	t.Parallel()
	withModel := providers.EmbedderFromSingleWithModel(singleVec{}, "voyage-3-large-256")
	out, err := withModel.Embed(context.Background(), providers.EmbedInput{Texts: []string{"x"}})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if out.Model != "voyage-3-large-256" {
		t.Fatalf("EmbedOutput.Model = %q, want voyage-3-large-256", out.Model)
	}
	mi, ok := withModel.(providers.ModelIdentifier)
	if !ok {
		t.Fatal("model-aware embedder must implement providers.ModelIdentifier")
	}
	if mi.ModelID() != "voyage-3-large-256" {
		t.Fatalf("ModelID() = %q, want voyage-3-large-256", mi.ModelID())
	}

	// Plain constructor: unnamed space (empty model).
	plain := providers.EmbedderFromSingle(singleVec{})
	if pm, ok := plain.(providers.ModelIdentifier); ok && pm.ModelID() != "" {
		t.Fatalf("EmbedderFromSingle must report empty model, got %q", pm.ModelID())
	}
}
