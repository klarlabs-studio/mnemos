package embedding

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// openAIStub serves a canned /embeddings response so we can reproduce the
// malformed payloads real providers emit — vLLM, LM Studio, Groq, Together and
// any other openai-compat endpoint reachable via MNEMOS_EMBED_BASE_URL.
func openAIStub(t *testing.T, body string) *OpenAIEmbedder {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return NewOpenAIEmbedder(srv.URL, "test-key", "text-embedding-3-small")
}

// A negative index panics on an unchecked slice write, killing the process —
// on a hosted server that is every tenant's requests, not just this one.
func TestOpenAIEmbed_NegativeIndexIsRejectedNotPanic(t *testing.T) {
	c := openAIStub(t, `{"data":[{"index":-1,"embedding":[0.1,0.2]}]}`)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panicked on a negative index instead of returning an error: %v", r)
		}
	}()

	vectors, err := c.Embed(context.Background(), []string{"one"})
	if err == nil {
		t.Fatalf("accepted a negative index, returned %d vectors", len(vectors))
	}
	if !errors.Is(err, ErrIncompleteResponse) {
		t.Errorf("error = %v, want ErrIncompleteResponse", err)
	}
}

func TestOpenAIEmbed_OutOfRangeIndexRejected(t *testing.T) {
	c := openAIStub(t, `{"data":[{"index":7,"embedding":[0.1]}]}`)
	if _, err := c.Embed(context.Background(), []string{"one"}); err == nil {
		t.Fatal("accepted an index past the request size")
	}
}

// Indices 5..9 for a 5-input request left every slot nil under the old
// size-by-response logic: five rows committed with empty vectors, reported as
// five successful embeddings, invisible to recall forever.
func TestOpenAIEmbed_HoleyResponseRejected(t *testing.T) {
	type item struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	}
	var items []item
	for i := 5; i < 10; i++ {
		items = append(items, item{Index: i, Embedding: []float32{0.5}})
	}
	body, _ := json.Marshal(map[string]any{"data": items})

	c := openAIStub(t, string(body))
	vectors, err := c.Embed(context.Background(), []string{"a", "b", "c", "d", "e"})
	if err == nil {
		t.Fatalf("accepted a fully out-of-range response as %d vectors", len(vectors))
	}
	for i, v := range vectors {
		if len(v) == 0 {
			t.Errorf("vector %d is empty and would have been stored with dimensions=0", i)
		}
	}
}

func TestOpenAIEmbed_DuplicateIndexRejected(t *testing.T) {
	c := openAIStub(t, `{"data":[{"index":0,"embedding":[0.1]},{"index":0,"embedding":[0.2]}]}`)
	if _, err := c.Embed(context.Background(), []string{"a", "b"}); err == nil {
		t.Fatal("accepted a duplicate index, which leaves another slot empty")
	}
}

// The common real-world case: the provider truncates the batch and answers 200.
func TestOpenAIEmbed_ShortResponseRejected(t *testing.T) {
	c := openAIStub(t, `{"data":[{"index":0,"embedding":[0.1]}]}`)
	_, err := c.Embed(context.Background(), []string{"a", "b", "c"})
	if err == nil {
		t.Fatal("accepted 1 vector for a 3-input request")
	}
	if !errors.Is(err, ErrIncompleteResponse) {
		t.Errorf("error = %v, want ErrIncompleteResponse", err)
	}
	// Filling by index means the shortfall surfaces as the unfilled slot rather
	// than a count mismatch; either way the message must name what is missing.
	if !strings.Contains(err.Error(), "index 1") {
		t.Errorf("error should identify the missing vector, got: %v", err)
	}
}

func TestOpenAIEmbed_WellFormedResponseAccepted(t *testing.T) {
	c := openAIStub(t, `{"data":[{"index":1,"embedding":[0.3,0.4]},{"index":0,"embedding":[0.1,0.2]}]}`)
	vectors, err := c.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("rejected a valid (out-of-order) response: %v", err)
	}
	if len(vectors) != 2 {
		t.Fatalf("got %d vectors, want 2", len(vectors))
	}
	// Out-of-order data must land in request order, not arrival order.
	if vectors[0][0] != 0.1 || vectors[1][0] != 0.3 {
		t.Errorf("vectors not ordered by index: %v", vectors)
	}
}

func TestValidateVectors(t *testing.T) {
	tests := []struct {
		name    string
		vectors [][]float32
		want    int
		wantErr bool
	}{
		{"exact match", [][]float32{{1}, {2}}, 2, false},
		{"short", [][]float32{{1}}, 2, true},
		{"long", [][]float32{{1}, {2}, {3}}, 2, true},
		{"empty vector", [][]float32{{1}, {}}, 2, true},
		{"nil vector", [][]float32{{1}, nil}, 2, true},
		{"none requested", nil, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateVectors(tt.vectors, tt.want, "test")
			if (err != nil) != tt.wantErr {
				t.Errorf("validateVectors = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
