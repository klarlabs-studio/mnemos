package llm

import "strings"

// Identified is implemented by clients that can name what produces their
// output: provider, model and endpoint. Anything derived from a client's
// output and kept (the extraction cache) is keyed on it, so a different model
// never serves another model's results (ADR 0029: derived state records its
// producer).
type Identified interface {
	Identity() string
}

func identity(provider, model, baseURL string) string {
	return strings.Join([]string{provider, model, strings.TrimRight(baseURL, "/")}, "|")
}

// Identity implements [Identified].
func (c *OpenAIClient) Identity() string { return identity(c.provider, c.model, c.baseURL) }

// Identity implements [Identified].
func (c *AnthropicClient) Identity() string { return identity("anthropic", c.model, c.baseURL) }

// Identity implements [Identified].
func (c *GeminiClient) Identity() string { return identity("gemini", c.model, c.baseURL) }
