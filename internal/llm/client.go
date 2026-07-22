// Package llm provides a provider-agnostic interface for LLM completions.
//
// Supported providers: Anthropic, OpenAI (and compatible APIs like Groq,
// Together, Fireworks, Mistral, vLLM), Google Gemini, and Ollama.
package llm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Role represents a message role in a conversation.
type Role string

// Conversation roles for LLM messages.
const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// Message is a single turn in a conversation.
type Message struct {
	Role    Role
	Content string
}

// Response wraps an LLM completion result.
type Response struct {
	Content      string
	Model        string
	InputTokens  int
	OutputTokens int
}

// Client is the port for LLM completions. Implementations must be safe for
// concurrent use.
type Client interface {
	Complete(ctx context.Context, messages []Message) (Response, error)
}

// Provider identifies a supported LLM provider.
type Provider string

// Supported LLM providers.
const (
	ProviderAnthropic    Provider = "anthropic"
	ProviderOpenAI       Provider = "openai"
	ProviderGemini       Provider = "gemini"
	ProviderOllama       Provider = "ollama"
	ProviderOpenAICompat Provider = "openai-compat"
)

// Config holds the parameters needed to construct a Client.
type Config struct {
	Provider Provider
	APIKey   string
	Model    string
	BaseURL  string
}

// DefaultModel returns the default model for a given provider.
func DefaultModel(p Provider) string {
	switch p {
	case ProviderAnthropic:
		return "claude-sonnet-4-20250514"
	case ProviderOpenAI:
		return "gpt-4o-mini"
	case ProviderGemini:
		return "gemini-2.0-flash"
	case ProviderOllama:
		return "llama3.2"
	default:
		return ""
	}
}

// DefaultBaseURL returns the default API base URL for a given provider.
func DefaultBaseURL(p Provider) string {
	switch p {
	case ProviderAnthropic:
		return "https://api.anthropic.com"
	case ProviderOpenAI:
		return "https://api.openai.com"
	case ProviderGemini:
		return "https://generativelanguage.googleapis.com"
	case ProviderOllama:
		return "http://localhost:11434"
	default:
		return ""
	}
}

// ConfigFromEnv reads LLM configuration from environment variables:
//
//	MNEMOS_LLM_PROVIDER  — anthropic, openai, gemini, ollama, openai-compat
//	MNEMOS_LLM_API_KEY   — API key (required for cloud providers)
//	MNEMOS_LLM_MODEL     — model override (optional, defaults per provider)
//	MNEMOS_LLM_BASE_URL  — endpoint override (required for openai-compat)
func ConfigFromEnv() (Config, error) {
	raw := strings.TrimSpace(os.Getenv("MNEMOS_LLM_PROVIDER"))
	if raw == "" {
		// Auto-detect Ollama running locally.
		if OllamaAvailable() {
			raw = string(ProviderOllama)
		} else {
			return Config{}, errors.New("MNEMOS_LLM_PROVIDER is not set (tip: install Ollama for zero-config local inference)")
		}
	}

	p := Provider(strings.ToLower(raw))
	switch p {
	case ProviderAnthropic, ProviderOpenAI, ProviderGemini, ProviderOllama, ProviderOpenAICompat:
	default:
		return Config{}, fmt.Errorf("unsupported LLM provider %q (want anthropic, openai, gemini, ollama, or openai-compat)", raw)
	}

	cfg := Config{
		Provider: p,
		APIKey:   strings.TrimSpace(os.Getenv("MNEMOS_LLM_API_KEY")),
		Model:    strings.TrimSpace(os.Getenv("MNEMOS_LLM_MODEL")),
		BaseURL:  strings.TrimSpace(os.Getenv("MNEMOS_LLM_BASE_URL")),
	}

	// Apply defaults.
	if cfg.Model == "" {
		cfg.Model = DefaultModel(p)
	}
	cfg.BaseURL = ResolveBaseURL(p, cfg.BaseURL)

	// Validate.
	switch p {
	case ProviderAnthropic, ProviderOpenAI, ProviderGemini:
		if cfg.APIKey == "" {
			return Config{}, fmt.Errorf("MNEMOS_LLM_API_KEY is required for provider %q", p)
		}
	case ProviderOpenAICompat:
		if cfg.BaseURL == "" {
			return Config{}, errors.New("MNEMOS_LLM_BASE_URL is required for openai-compat provider")
		}
		if cfg.Model == "" {
			return Config{}, errors.New("MNEMOS_LLM_MODEL is required for openai-compat provider")
		}
	}

	return cfg, nil
}

// ResolveBaseURL returns the endpoint to use for provider p given an explicit
// value from config or env (possibly empty).
//
// It exists to close a silent failure. Config files pin provider and base_url
// together, but 12-factor precedence lets an env var override the provider
// ALONE — e.g. a config with provider=ollama + base_url=localhost:11434, then
// MNEMOS_LLM_PROVIDER=anthropic exported without a matching base_url. The
// Anthropic client would then POST to localhost:11434 and every call fails;
// because extraction folds LLM errors to a safe empty result, it fails
// SILENTLY (every verdict comes back Unknown, which reads as "the model
// classified nothing"). So a base URL that is some OTHER provider's default is
// treated as a stale pairing and dropped in favour of p's own default.
//
// A genuine custom endpoint (one that is no built-in provider's default) is
// always kept, as is openai-compat's endpoint — it is deliberate and may
// legitimately point at another provider's address, e.g. an OpenAI-compatible
// gateway in front of Ollama.
func ResolveBaseURL(p Provider, explicit string) string {
	explicit = strings.TrimSpace(explicit)
	if p != ProviderOpenAICompat && explicit != "" && isForeignProviderDefault(p, explicit) {
		explicit = ""
	}
	if explicit == "" {
		return DefaultBaseURL(p)
	}
	return explicit
}

// isForeignProviderDefault reports whether url is the default endpoint of some
// provider OTHER than sel — a stale provider pairing rather than a chosen
// custom endpoint. A URL that matches sel's own default, or that matches no
// built-in provider's default, returns false.
func isForeignProviderDefault(sel Provider, url string) bool {
	for _, p := range []Provider{ProviderAnthropic, ProviderOpenAI, ProviderGemini, ProviderOllama} {
		if p == sel {
			continue
		}
		if DefaultBaseURL(p) == url {
			return true
		}
	}
	return false
}

// NewClient constructs a Client from the given Config.
func NewClient(cfg Config) (Client, error) {
	switch cfg.Provider {
	case ProviderAnthropic:
		return NewAnthropicClient(cfg.BaseURL, cfg.APIKey, cfg.Model), nil
	case ProviderOpenAI, ProviderOllama, ProviderOpenAICompat:
		return NewOpenAIClient(cfg.BaseURL, cfg.APIKey, cfg.Model, string(cfg.Provider)), nil
	case ProviderGemini:
		return NewGeminiClient(cfg.BaseURL, cfg.APIKey, cfg.Model), nil
	default:
		return nil, fmt.Errorf("unsupported provider %q", cfg.Provider)
	}
}
