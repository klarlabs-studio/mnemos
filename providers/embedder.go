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
// Embedder is the canonical, batch-shaped interface: it embeds a slice of
// texts in one call so adapters can issue a single provider request rather
// than N round-trips. Consumers who only have a single-text embedding
// function can implement [SingleEmbedder] instead and wrap it with
// [EmbedderFromSingle].
//
// Implementations MUST be safe for concurrent use. Vector dimension MUST
// be constant for a given Embedder instance — Mnemos relies on this for
// cosine-similarity ranking.
type Embedder interface {
	Embed(ctx context.Context, in EmbedInput) (EmbedOutput, error)
}

// ModelIdentifier is an optional capability an [Embedder] may implement to
// report the stable identifier of the model it produces vectors with (e.g.
// "voyage-3-large-256"). Mnemos stamps this id on every stored vector and
// filters recall to the current model, so vectors produced by different
// models are never compared (cosine across model spaces is noise). An
// Embedder that does not implement this is treated as a single unnamed model
// space — recall behaves exactly as before.
type ModelIdentifier interface {
	ModelID() string
}

// SingleEmbedder is the simple, single-text embedding signature: embed one
// string, get one vector. It is provided for callers whose embedding
// client only exposes a per-text call. Wrap it with [EmbedderFromSingle]
// to obtain the batch [Embedder] Mnemos consumes.
//
// Prefer implementing [Embedder] directly when your provider supports
// batching — a single batched request is materially cheaper than one
// request per text.
type SingleEmbedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

// EmbedderFromSingle adapts a [SingleEmbedder] into the batch [Embedder]
// Mnemos consumes. It calls the underlying single-text Embed once per
// input string, preserving order. Returns nil when s is nil.
//
// The reported Model is left empty (a single-text embedder does not
// surface a model name); ranking does not depend on it. The vector
// dimension must still be constant across calls, per the [Embedder]
// contract. Use [EmbedderFromSingleWithModel] to attach a model id so
// recall can filter to the current model space.
func EmbedderFromSingle(s SingleEmbedder) Embedder {
	return EmbedderFromSingleWithModel(s, "")
}

// EmbedderFromSingleWithModel is [EmbedderFromSingle] plus a stable model
// identifier. The returned Embedder reports model on every [EmbedOutput] and
// implements [ModelIdentifier], so Mnemos stamps it on stored vectors and
// filters recall to it. Pass the id your embedding model is versioned by
// (e.g. "voyage-3-large-256"); change it whenever the model changes so old
// vectors stop matching until re-embedded. An empty model keeps the
// single-unnamed-space behavior of [EmbedderFromSingle].
func EmbedderFromSingleWithModel(s SingleEmbedder, model string) Embedder {
	if s == nil {
		return nil
	}
	return singleEmbedderAdapter{single: s, model: model}
}

// singleEmbedderAdapter bridges a SingleEmbedder to the batch Embedder.
type singleEmbedderAdapter struct {
	single SingleEmbedder
	model  string
}

// Embed satisfies [Embedder] by invoking the wrapped single-text embedder
// once per input, short-circuiting on the first error.
func (a singleEmbedderAdapter) Embed(ctx context.Context, in EmbedInput) (EmbedOutput, error) {
	vectors := make([][]float32, len(in.Texts))
	for i, text := range in.Texts {
		v, err := a.single.Embed(ctx, text)
		if err != nil {
			return EmbedOutput{}, err
		}
		vectors[i] = v
	}
	return EmbedOutput{Vectors: vectors, Model: a.model}, nil
}

// ModelID satisfies [ModelIdentifier]. Empty when no model id was supplied.
func (a singleEmbedderAdapter) ModelID() string { return a.model }
