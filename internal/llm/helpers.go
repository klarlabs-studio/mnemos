package llm

import (
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

// closeBody closes an io.ReadCloser (typically http.Response.Body),
// logging any error rather than silently discarding it.
func closeBody(body io.ReadCloser) {
	if err := body.Close(); err != nil {
		log.Printf("close response body: %v", err)
	}
}

// defaultLLMTimeout bounds a single provider call when MNEMOS_LLM_TIMEOUT
// is unset. Local-LLM users (Ollama with reasoning models, large models on
// consumer hardware) routinely exceed 30s for a single completion, so the
// default is generous; users who want a tighter or looser bound should
// set MNEMOS_LLM_TIMEOUT.
const defaultLLMTimeout = 120 * time.Second

// Timeout returns the per-request LLM HTTP timeout, honoring the
// MNEMOS_LLM_TIMEOUT environment variable. Accepted values are Go duration
// strings (e.g. "60s", "2m", "5m"). Invalid or non-positive values fall
// back to the default and emit a warning.
func Timeout() time.Duration {
	return envDuration("MNEMOS_LLM_TIMEOUT", defaultLLMTimeout)
}

// defaultLLMHTTPClient returns an http.Client with the configured
// per-request timeout. The ctx passed through Complete still bounds the
// logical operation; this timeout is the transport-level floor.
func defaultLLMHTTPClient() *http.Client {
	return &http.Client{Timeout: Timeout()}
}

// envDuration parses a duration from envVar, falling back to def when
// unset, malformed, or non-positive. Logs a one-line warning on bad input
// so misconfiguration is visible without being fatal.
func envDuration(envVar string, def time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(envVar))
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		// %q quotes and escapes; envVar is hard-coded by callers.
		log.Printf("invalid %s=%q (want a Go duration like 60s or 2m): %v; using %s", envVar, sanitizeForLog(raw), err, def)
		return def
	}
	if d <= 0 {
		log.Printf("invalid %s=%q (must be > 0); using %s", envVar, sanitizeForLog(raw), def)
		return def
	}
	return d
}

// sanitizeForLog bounds the length of a logged env var value so a
// pathological input cannot blow up the log line.
func sanitizeForLog(s string) string {
	const max = 64
	if len(s) > max {
		return s[:max] + "...[truncated]"
	}
	return s
}
