package main

import (
	"os"
	"strings"

	"go.klarlabs.de/bolt"
)

// mnemosLogLevel reads MNEMOS_LOG_LEVEL (trace|debug|info|warn|error; default info) for
// the structured stderr logger that carries the brain's operational logs (ADR 0021).
func mnemosLogLevel() bolt.Level {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MNEMOS_LOG_LEVEL"))) {
	case "trace":
		return bolt.TRACE
	case "debug":
		return bolt.DEBUG
	case "warn", "warning":
		return bolt.WARN
	case "error":
		return bolt.ERROR
	default:
		return bolt.INFO
	}
}

// newStderrLogger builds the structured JSON stderr logger at the configured level.
// Used for the library Memory (WithLogger), the metrics sampler, and promotion — every
// place that emits the brain's operational logs.
func newStderrLogger() *bolt.Logger {
	return bolt.New(bolt.NewJSONHandler(os.Stderr)).SetLevel(mnemosLogLevel())
}
