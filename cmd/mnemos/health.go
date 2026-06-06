package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/embedding"
	"go.klarlabs.de/mnemos/internal/llm"
)

// healthCheck is one row in the deep health response. Status is
// always one of "ok", "skipped" (no provider configured), or
// "failed". Detail carries either the failure message or a brief
// "what we tried" hint when ok.
type healthCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// healthCheckResult buckets the per-check results so a deep-mode
// caller can read pass/fail at a glance.
type healthCheckResult struct {
	Healthy bool          `json:"healthy"`
	Checks  []healthCheck `json:"checks"`
}

// runHealthChecks probes each subsystem with bounded latency. Each
// probe contributes one healthCheck row; the overall Healthy flag is
// false if any probe failed (skipped probes don't fail health — they
// just mean the operator hasn't configured that path).
func runHealthChecks(ctx context.Context, db *sql.DB) healthCheckResult {
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	checks := []healthCheck{
		probeDB(probeCtx, db),
		probeLLM(probeCtx),
		probeEmbedding(probeCtx),
	}

	healthy := true
	for _, c := range checks {
		if c.Status == "failed" {
			healthy = false
		}
	}
	return healthCheckResult{Healthy: healthy, Checks: checks}
}

// probeDB writes a sentinel value into a real but harmless table
// (revoked_tokens — keys are JWT IDs, easy to make a clearly-fake
// one) inside a transaction that's always rolled back, so we
// confirm WAL/journal/file permissions without leaving any data.
func probeDB(ctx context.Context, db *sql.DB) healthCheck {
	if db == nil {
		return healthCheck{Name: "sqlite", Status: "failed", Detail: "no DB handle"}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return healthCheck{Name: "sqlite", Status: "failed", Detail: err.Error()}
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO revoked_tokens (jti, revoked_at, expires_at) VALUES (?, ?, ?)`,
		"healthcheck-"+time.Now().UTC().Format(time.RFC3339Nano),
		time.Now().UTC().Format(time.RFC3339Nano),
		time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		return healthCheck{Name: "sqlite", Status: "failed", Detail: "write probe: " + err.Error()}
	}
	return healthCheck{Name: "sqlite", Status: "ok", Detail: "write probe rolled back"}
}

// probeLLM verifies the configured LLM provider can be constructed.
// We don't actually call out to the provider here — that would
// charge the user money on every health check. A construction-time
// success means env vars decode cleanly and the client picked a
// transport; the first real call still has to validate end-to-end.
//
// "skipped" means no provider env var is set AND no Ollama is
// running — the operator hasn't asked for LLM features. "failed"
// means a provider was requested but the config or client construction
// is broken.
func probeLLM(_ context.Context) healthCheck {
	if !llmExplicitlyConfigured() && !llm.OllamaAvailable() {
		return healthCheck{Name: "llm", Status: "skipped", Detail: "no provider configured (set MNEMOS_LLM_PROVIDER or run Ollama)"}
	}
	cfg, err := llm.ConfigFromEnv()
	if err != nil {
		return healthCheck{Name: "llm", Status: "failed", Detail: err.Error()}
	}
	if _, err := llm.NewClient(cfg); err != nil {
		return healthCheck{Name: "llm", Status: "failed", Detail: err.Error()}
	}
	return healthCheck{Name: "llm", Status: "ok", Detail: fmt.Sprintf("provider=%s model=%s", cfg.Provider, cfg.Model)}
}

// probeEmbedding mirrors probeLLM for the embedding provider.
// Same trade-off: construction-time only, no network call.
func probeEmbedding(_ context.Context) healthCheck {
	if !embeddingExplicitlyConfigured() && !llm.OllamaAvailable() {
		return healthCheck{Name: "embedding", Status: "skipped", Detail: "no provider configured (set MNEMOS_EMBED_PROVIDER or run Ollama)"}
	}
	cfg, err := embedding.ConfigFromEnv()
	if err != nil {
		return healthCheck{Name: "embedding", Status: "failed", Detail: err.Error()}
	}
	if _, err := embedding.NewClient(cfg); err != nil {
		return healthCheck{Name: "embedding", Status: "failed", Detail: err.Error()}
	}
	return healthCheck{Name: "embedding", Status: "ok", Detail: fmt.Sprintf("provider=%s model=%s", cfg.Provider, cfg.Model)}
}

func llmExplicitlyConfigured() bool {
	return strings.TrimSpace(os.Getenv("MNEMOS_LLM_PROVIDER")) != ""
}

func embeddingExplicitlyConfigured() bool {
	return strings.TrimSpace(os.Getenv("MNEMOS_EMBED_PROVIDER")) != "" ||
		strings.TrimSpace(os.Getenv("MNEMOS_LLM_PROVIDER")) != ""
}
