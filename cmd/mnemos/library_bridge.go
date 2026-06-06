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
func newLibraryMemory(_ context.Context, actor string) (mnemos.Memory, error) {
	opts := []mnemos.Option{}
	if actor != "" {
		opts = append(opts, mnemos.WithActor(actor))
	}

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
