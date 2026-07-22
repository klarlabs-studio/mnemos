// Package embedding provides a provider-agnostic interface for text embeddings.
//
// Supported providers: OpenAI (and compatible APIs), Google Gemini, and Ollama.
// Anthropic does not offer an embedding endpoint, so when the LLM provider is
// Anthropic, a separate MNEMOS_EMBED_PROVIDER must be configured.
package embedding

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"go.klarlabs.de/mnemos/internal/llm"
)

// Client generates vector embeddings from text.
type Client interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// ModelIdentifier is an optional capability a [Client] may implement to
// report the stable id of the embedding model it produces vectors with.
// Recall stamps this on stored vectors and filters to it so vectors from
// different models are never compared. Discover it via [ModelIDOf].
type ModelIdentifier interface {
	ModelID() string
}

// ModelIDOf returns the model id a Client reports, or "" when it does not
// implement [ModelIdentifier] (treated as a single unnamed model space).
func ModelIDOf(c Client) string {
	if c == nil {
		return ""
	}
	if m, ok := c.(ModelIdentifier); ok {
		return m.ModelID()
	}
	return ""
}

// Config holds the parameters needed to construct a Client.
type Config struct {
	Provider llm.Provider
	APIKey   string
	Model    string
	BaseURL  string
}

// DefaultEmbeddingModel returns the default embedding model for a given provider.
func DefaultEmbeddingModel(p llm.Provider) string {
	switch p {
	case llm.ProviderOpenAI:
		return "text-embedding-3-small"
	case llm.ProviderGemini:
		return "text-embedding-004"
	case llm.ProviderOllama:
		return "nomic-embed-text"
	case llm.ProviderOpenAICompat:
		return "" // must be specified
	default:
		return ""
	}
}

// ConfigFromEnv reads embedding configuration from environment variables.
// It first checks MNEMOS_EMBED_PROVIDER for a dedicated embedding provider,
// then falls back to MNEMOS_LLM_PROVIDER. Anthropic has no embedding API,
// so it is rejected unless overridden.
//
//	MNEMOS_EMBED_PROVIDER  — optional override (openai, gemini, ollama, openai-compat)
//	MNEMOS_EMBED_API_KEY   — API key override (falls back to MNEMOS_LLM_API_KEY)
//	MNEMOS_EMBED_MODEL     — model override (falls back to provider default)
//	MNEMOS_EMBED_BASE_URL  — endpoint override
func ConfigFromEnv() (Config, error) {
	raw := strings.TrimSpace(os.Getenv("MNEMOS_EMBED_PROVIDER"))
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("MNEMOS_LLM_PROVIDER"))
	}
	if raw == "" {
		// Auto-detect Ollama running locally.
		if llm.OllamaAvailable() {
			raw = string(llm.ProviderOllama)
		} else {
			return Config{}, errors.New("MNEMOS_EMBED_PROVIDER (or MNEMOS_LLM_PROVIDER) is not set (tip: install Ollama for zero-config local inference)")
		}
	}

	p := llm.Provider(strings.ToLower(raw))
	if p == llm.ProviderAnthropic {
		return Config{}, errors.New("anthropic does not offer an embedding API; set MNEMOS_EMBED_PROVIDER to openai, gemini, or ollama")
	}

	switch p {
	case llm.ProviderOpenAI, llm.ProviderGemini, llm.ProviderOllama, llm.ProviderOpenAICompat:
	default:
		return Config{}, fmt.Errorf("unsupported embedding provider %q", raw)
	}

	apiKey := strings.TrimSpace(os.Getenv("MNEMOS_EMBED_API_KEY"))
	if apiKey == "" {
		apiKey = strings.TrimSpace(os.Getenv("MNEMOS_LLM_API_KEY"))
	}

	model := strings.TrimSpace(os.Getenv("MNEMOS_EMBED_MODEL"))
	if model == "" {
		model = DefaultEmbeddingModel(p)
	}

	// Prefer an embed-specific endpoint, then the LLM one (a shared local
	// server commonly serves both), then the provider default. The LLM
	// fallback makes the foreign-default guard load-bearing here: an
	// MNEMOS_LLM_BASE_URL set for a cloud LLM must not silently become the
	// embedding endpoint for a different embed provider. ResolveBaseURL drops a
	// base URL that is some other provider's default (see its doc).
	baseURL := strings.TrimSpace(os.Getenv("MNEMOS_EMBED_BASE_URL"))
	if baseURL == "" {
		baseURL = strings.TrimSpace(os.Getenv("MNEMOS_LLM_BASE_URL"))
	}
	baseURL = llm.ResolveBaseURL(p, baseURL)

	cfg := Config{
		Provider: p,
		APIKey:   apiKey,
		Model:    model,
		BaseURL:  baseURL,
	}

	switch p {
	case llm.ProviderOpenAI, llm.ProviderGemini:
		if cfg.APIKey == "" {
			return Config{}, fmt.Errorf("API key required for embedding provider %q", p)
		}
	case llm.ProviderOpenAICompat:
		if cfg.BaseURL == "" {
			return Config{}, errors.New("MNEMOS_EMBED_BASE_URL is required for openai-compat")
		}
		if cfg.Model == "" {
			return Config{}, errors.New("MNEMOS_EMBED_MODEL is required for openai-compat")
		}
	}

	return cfg, nil
}

// NewClient constructs a Client from the given Config.
func NewClient(cfg Config) (Client, error) {
	switch cfg.Provider {
	case llm.ProviderOpenAI, llm.ProviderOllama, llm.ProviderOpenAICompat:
		return NewOpenAIEmbedder(cfg.BaseURL, cfg.APIKey, cfg.Model), nil
	case llm.ProviderGemini:
		return NewGeminiEmbedder(cfg.BaseURL, cfg.APIKey, cfg.Model), nil
	default:
		return nil, fmt.Errorf("unsupported embedding provider %q", cfg.Provider)
	}
}
