package embedding

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// EncodeVector serializes a float32 vector to a compact binary BLOB
// using little-endian encoding (4 bytes per dimension).
func EncodeVector(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

// DecodeVector deserializes a binary BLOB back to a float32 vector.
func DecodeVector(data []byte) ([]float32, error) {
	if len(data)%4 != 0 {
		return nil, fmt.Errorf("invalid vector blob: length %d is not a multiple of 4", len(data))
	}
	v := make([]float32, len(data)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
	}
	return v, nil
}

// CosineSimilarity computes the cosine similarity between two vectors.
// Returns 0.0 if either vector has zero magnitude. Returns an error if
// the vectors have different dimensions.
func CosineSimilarity(a, b []float32) (float32, error) {
	if len(a) != len(b) {
		return 0, fmt.Errorf("embedding dimension mismatch: got %d and %d", len(a), len(b))
	}
	if len(a) == 0 {
		return 0, nil
	}
	var dot, normA, normB float32
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0, nil
	}
	return dot / (float32(math.Sqrt(float64(normA))) * float32(math.Sqrt(float64(normB)))), nil
}

// ErrIncompleteResponse reports an embedding provider response that does not
// carry exactly one usable vector per input text.
var ErrIncompleteResponse = errors.New("incomplete embedding response")

// validateVectors checks a provider's response before it reaches storage.
//
// This is a load-bearing check, not defensive padding. Storage accepts an
// empty vector without complaint — EncodeVector(nil) yields a zero-length blob
// and the row commits with dimensions=0 — and the pipeline reports the number
// of vectors returned as the number of embeddings created. So a provider that
// answers short, out of order, or with a hole produces claims that look
// embedded to every consumer and are permanently invisible to semantic recall,
// with a success message and exit code 0.
//
// Catching it here means the caller's existing retry/error path handles it,
// instead of silent corruption spreading into the store.
func validateVectors(vectors [][]float32, want int, provider string) error {
	if len(vectors) != want {
		return fmt.Errorf("%w: %s returned %d vectors for %d input(s)",
			ErrIncompleteResponse, provider, len(vectors), want)
	}
	for i, v := range vectors {
		if len(v) == 0 {
			return fmt.Errorf("%w: %s returned an empty vector at index %d",
				ErrIncompleteResponse, provider, i)
		}
	}
	return nil
}
