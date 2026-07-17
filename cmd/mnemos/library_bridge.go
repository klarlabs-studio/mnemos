package main

import (
	"context"
	"fmt"

	"go.klarlabs.de/mnemos"
	"go.klarlabs.de/mnemos/internal/embedding"
	"go.klarlabs.de/mnemos/internal/llm"
)

// newLibraryMemory constructs a [mnemos.Memory] mirroring the env-driven
// configuration the rest of cmd/mnemos uses. Tries enhanced mode when
// MNEMOS_LLM_PROVIDER (and friends) are set; falls back to passive mode
// otherwise.
//
// Most of cmd/mnemos still calls store.Open + the internal engines
// directly because it needs flags + outputs the public Memory interface
// doesn't expose (e.g. Contradictions on the recall path, useLLM /
// useEmbeddings switches on process_text). New code where the public
// surface is sufficient — currently the simpler temporal MCP tools —
// routes through this helper instead.
func newLibraryMemory(ctx context.Context, actor string) (mnemos.Memory, error) {
	// Tenant-scope the DSN when the request carries a tenant (multi-tenant HTTP
	// MCP mode), so a library-bridge Memory (temporal tools, etc.) is isolated
	// by RLS exactly like the openConn path — and fails closed if a request in
	// multi-tenant mode somehow lacks a tenant. With no tenant in ctx (CLI,
	// serve) this is the plain resolveDSN.
	dsn, err := resolveDSNForContext(ctx)
	if err != nil {
		return nil, err
	}
	return newLibraryMemoryForDSN(dsn, actor)
}

// newLibraryMemoryForDSN builds a library Memory bound to an explicit DSN,
// bypassing the request-scoped fail-closed logic. Used to construct the base
// (unscoped) facade that the multi-tenant cognitive path calls `.Tenant(id)`
// on — that facade must build even under --require-tenant, where a
// tenant-less request DSN is (correctly) refused.
func newLibraryMemoryForDSN(dsn, actor string) (mnemos.Memory, error) {
	opts := []mnemos.Option{mnemos.WithStorage(dsn)}
	if actor != "" {
		opts = append(opts, mnemos.WithActor(actor))
	}
	// Structured stderr logger (ADR 0021): lights up the brain's operational logs
	// (consolidation, health, promotion, the axi write-audit) for Loki/Grafana. Level
	// via MNEMOS_LOG_LEVEL (default info).
	opts = append(opts, mnemos.WithLogger(newStderrLogger()))

	if llmCfg, err := llm.ConfigFromEnv(); err == nil && llmCfg.Provider != "" {
		// Enhanced mode: Mnemos uses its own LLM + embedding config
		// from MNEMOS_LLM_* / MNEMOS_EMBED_* env vars.
		embCfg, _ := embedding.ConfigFromEnv()
		opts = append(opts, mnemos.WithEnhancedMode(mnemos.ProviderConfig{
			LLMProvider:   string(llmCfg.Provider),
			LLMAPIKey:     llmCfg.APIKey,
			LLMModel:      llmCfg.Model,
			LLMBaseURL:    llmCfg.BaseURL,
			EmbedProvider: string(embCfg.Provider),
			EmbedAPIKey:   embCfg.APIKey,
			EmbedModel:    embCfg.Model,
			EmbedBaseURL:  embCfg.BaseURL,
		}))
	} else {
		opts = append(opts, mnemos.WithPassiveMode())
	}

	mem, err := mnemos.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("library bridge: %w", err)
	}
	return mem, nil
}
