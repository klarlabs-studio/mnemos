package extract

import (
	"log/slog"
	"sync/atomic"

	"go.klarlabs.de/mnemos/internal/llm"
)

var fallbacks atomic.Int64

// Fallbacks returns how many LLM extraction batches in this process fell
// back to rule-based extraction. `mnemos serve` exports it as
// mnemos_extract_fallback_total.
//
// The fallback is deliberate: a capture that cannot reach the model still
// stores something. It used to be silent, so a wrong base URL (every call a
// 404) or an exhausted account (every call a 402) degraded a whole brain to
// rule-based claims while every write reported success (#451).
func Fallbacks() int64 { return fallbacks.Load() }

// recordFallback counts one batch that fell back and logs why at warning
// level. A rejected request (llm.IsPermanent) means every later batch will
// fail the same way, so the log says to fix the configuration.
func recordFallback(reason string, cause error, events int) {
	fallbacks.Add(1)
	msg := "llm extraction failed; this batch used rule-based extraction"
	if llm.IsPermanent(cause) {
		msg = "llm extraction rejected by the provider (check the API key, account credit and base URL); this batch used rule-based extraction"
	}
	slog.Warn(msg, "reason", reason, "events", events, "error", cause)
}
