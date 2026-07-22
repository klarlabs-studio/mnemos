package embedding

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOpenAIEmbedder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("unexpected auth header: %s", r.Header.Get("Authorization"))
		}

		resp := openAIEmbedResponse{
			Data: []struct {
				Embedding []float32 `json:"embedding"`
				Index     int       `json:"index"`
			}{
				{Embedding: []float32{0.1, 0.2, 0.3}, Index: 0},
				{Embedding: []float32{0.4, 0.5, 0.6}, Index: 1},
			},
			Model: "text-embedding-3-small",
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	defer server.Close()

	client := NewOpenAIEmbedder(server.URL, "test-key", "text-embedding-3-small")
	vectors, err := client.Embed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}
	if len(vectors) != 2 {
		t.Fatalf("got %d vectors, want 2", len(vectors))
	}
	if len(vectors[0]) != 3 {
		t.Fatalf("vector[0] dimensions = %d, want 3", len(vectors[0]))
	}
}

func TestOpenAIEmbedderEmpty(t *testing.T) {
	client := NewOpenAIEmbedder("http://unused", "", "model")
	vectors, err := client.Embed(context.Background(), []string{})
	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}
	if vectors != nil {
		t.Fatalf("expected nil for empty input, got %v", vectors)
	}
}

func TestOpenAIEmbedderAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid key","type":"auth_error"}}`))
	}))
	defer server.Close()

	client := NewOpenAIEmbedder(server.URL, "bad-key", "model")
	_, err := client.Embed(context.Background(), []string{"hello"})
	if err == nil {
		t.Fatal("expected error for 401 response")
	}
}

func TestGeminiEmbedder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}

		resp := geminiBatchEmbedResponse{
			Embeddings: []struct {
				Values []float32 `json:"values"`
			}{
				{Values: []float32{0.1, 0.2}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	defer server.Close()

	client := NewGeminiEmbedder(server.URL, "test-key", "text-embedding-004")
	vectors, err := client.Embed(context.Background(), []string{"test"})
	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}
	if len(vectors) != 1 {
		t.Fatalf("got %d vectors, want 1", len(vectors))
	}
	if len(vectors[0]) != 2 {
		t.Fatalf("vector dimensions = %d, want 2", len(vectors[0]))
	}
}

func TestGeminiEmbedderEmpty(t *testing.T) {
	client := NewGeminiEmbedder("http://unused", "", "model")
	vectors, err := client.Embed(context.Background(), []string{})
	if err != nil {
		t.Fatalf("Embed() error = %v", err)
	}
	if vectors != nil {
		t.Fatalf("expected nil for empty input, got %v", vectors)
	}
}

// TestConfigFromEnv_LLMBaseURLDoesNotLeakAcrossProviders pins that a cloud
// MNEMOS_LLM_BASE_URL does not silently become the embedding endpoint when the
// embed provider differs. The embed path falls back to the LLM base URL, so
// without the foreign-default guard an Anthropic LLM URL would be handed to an
// Ollama embedder and every embedding call would fail silently.
func TestConfigFromEnv_LLMBaseURLDoesNotLeakAcrossProviders(t *testing.T) {
	t.Setenv("MNEMOS_EMBED_PROVIDER", "ollama")
	t.Setenv("MNEMOS_EMBED_BASE_URL", "")
	t.Setenv("MNEMOS_LLM_BASE_URL", "https://api.anthropic.com") // a cloud LLM endpoint
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if cfg.BaseURL == "https://api.anthropic.com" {
		t.Fatal("a cloud LLM base URL leaked into the ollama embedder")
	}
	// It should resolve to Ollama's own endpoint.
	if cfg.BaseURL != "http://localhost:11434" {
		t.Fatalf("embed base URL = %q, want the ollama default", cfg.BaseURL)
	}
}

// A shared local server is the case the LLM-base-URL fallback exists for: same
// provider on both sides, one endpoint, and it must be honored.
func TestConfigFromEnv_SharedLocalEndpointIsHonored(t *testing.T) {
	t.Setenv("MNEMOS_EMBED_PROVIDER", "ollama")
	t.Setenv("MNEMOS_EMBED_BASE_URL", "")
	t.Setenv("MNEMOS_LLM_BASE_URL", "http://localhost:11434")
	cfg, err := ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv: %v", err)
	}
	if cfg.BaseURL != "http://localhost:11434" {
		t.Fatalf("a same-provider local endpoint must survive, got %q", cfg.BaseURL)
	}
}
