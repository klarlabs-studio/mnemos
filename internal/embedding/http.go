package embedding

import (
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// defaultEmbeddingTimeout bounds a single embedding call when
// MNEMOS_EMBED_TIMEOUT is unset. Embeddings are usually fast but local
// providers on large batches (e.g. Ollama nomic-embed-text on first call
// with a cold model) can exceed 30s, so the default is bumped to 60s.
// Set MNEMOS_EMBED_TIMEOUT for finer control.
const defaultEmbeddingTimeout = 60 * time.Second

// Timeout returns the per-request embedding HTTP timeout, honoring the
// MNEMOS_EMBED_TIMEOUT environment variable. Accepts Go duration strings
// (e.g. "30s", "2m"). Invalid values fall back to the default with a
// logged warning.
func Timeout() time.Duration {
	return envDuration("MNEMOS_EMBED_TIMEOUT", defaultEmbeddingTimeout)
}

// defaultEmbeddingHTTPClient returns an http.Client with the configured
// per-request timeout baked in. Use from every provider constructor so
// no embedder ever runs with the zero-value timeout.
func defaultEmbeddingHTTPClient() *http.Client {
	return &http.Client{Timeout: Timeout()}
}

func envDuration(envVar string, def time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(envVar))
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		log.Printf("invalid %s=%q (want a Go duration like 60s or 2m): %v; using %s", envVar, sanitizeForLog(raw), err, def) //nolint:gosec // value is %q-quoted and length-bounded
		return def
	}
	if d <= 0 {
		log.Printf("invalid %s=%q (must be > 0); using %s", envVar, sanitizeForLog(raw), def) //nolint:gosec // value is %q-quoted and length-bounded
		return def
	}
	return d
}

func sanitizeForLog(s string) string {
	const max = 64
	if len(s) > max {
		return s[:max] + "...[truncated]"
	}
	return s
}

// withoutSecret removes secret from text before it becomes error text. Provider
// error bodies are copied into errors for diagnosis, and a provider — or a
// proxy in front of it — that echoes the request back would otherwise hand the
// API key to every log and MCP reply that formats the error.
func withoutSecret(text, secret string) string {
	if secret == "" {
		return text
	}
	return strings.ReplaceAll(text, secret, "[REDACTED]")
}
