package providers

import "context"

// EmbedInput is the input to an [Embedder]. Texts is a batch of strings;
// implementations should batch them in a single provider call when
// possible. Returns one vector per input string in the same order.
type EmbedInput struct {
	Texts []string
}

// EmbedOutput is the output of an [Embedder]. Vectors[i] corresponds to
// Texts[i] in the input. Dimensions are provider-specific and constant
// within a single Embedder instance.
type EmbedOutput struct {
	Vectors [][]float32
	Model   string
}

// Embedder is the framework-neutral abstraction Mnemos uses for vector
// embeddings (event embeddings, claim embeddings, query reranking).
// Consumers implement this interface around their own embedding clients.
//
// Implementations MUST be safe for concurrent use. Vector dimension MUST
// be constant for a given Embedder instance — Mnemos relies on this for
// cosine-similarity ranking.
type Embedder interface {
	Embed(ctx context.Context, in EmbedInput) (EmbedOutput, error)
}
