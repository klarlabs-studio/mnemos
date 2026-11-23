package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/llm"
)

// probeNarrationJudge checks that the model the capture-time narration judge
// runs on exists. The judge fails open, so a missing model never breaks a
// capture: it keeps every claim, and the filter quietly stops filtering. On
// Ollama the judge does not inherit the extraction model (see
// defaultNarrationModel), so a machine that never pulled that model ran
// without a narration filter and nothing said so.
//
// Only Ollama is checked: listing its local models is one loopback request,
// while a hosted provider would need a paid call to prove the same.
func probeNarrationJudge(ctx context.Context) healthCheck {
	return checkNarrationJudge(ctx, listOllamaModels)
}

func checkNarrationJudge(ctx context.Context, list func(context.Context, string) ([]string, error)) healthCheck {
	const name = "narration_judge"
	if !narrationFilterEnabled() {
		return healthCheck{Name: name, Status: "skipped", Detail: "MNEMOS_NARRATION_FILTER is off"}
	}
	if !llmExplicitlyConfigured() && !llm.OllamaAvailable() {
		return healthCheck{Name: name, Status: "skipped", Detail: "no LLM provider configured"}
	}
	cfg, err := narrationJudgeConfig()
	if err != nil {
		return healthCheck{Name: name, Status: "skipped", Detail: err.Error()}
	}
	if cfg.Provider != llm.ProviderOllama {
		return healthCheck{Name: name, Status: "ok", Detail: fmt.Sprintf("provider=%s model=%s (not probed: a hosted check costs a call)", cfg.Provider, cfg.Model)}
	}
	installed, err := list(ctx, cfg.BaseURL)
	if err != nil {
		return healthCheck{Name: name, Status: "warn", Detail: fmt.Sprintf("could not list Ollama models at %s: %v", cfg.BaseURL, err)}
	}
	if !ollamaHasModel(installed, cfg.Model) {
		return healthCheck{Name: name, Status: "warn", Detail: fmt.Sprintf(
			"model %q is not installed, so capture keeps every claim unjudged; `ollama pull %s`, or set llm.narration_model (MNEMOS_NARRATION_MODEL) to one of: %s",
			cfg.Model, cfg.Model, strings.Join(installed, ", "))}
	}
	return healthCheck{Name: name, Status: "ok", Detail: "provider=ollama model=" + cfg.Model}
}

// ollamaHasModel matches Ollama's naming: a name without a tag means ":latest".
func ollamaHasModel(installed []string, model string) bool {
	want := model
	if !strings.Contains(want, ":") {
		want += ":latest"
	}
	for _, m := range installed {
		if m == want || m == model {
			return true
		}
	}
	return false
}

// listOllamaModels returns the names of the models an Ollama server has pulled.
func listOllamaModels(ctx context.Context, baseURL string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /api/tags: %s", resp.Status)
	}
	var body struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode /api/tags: %w", err)
	}
	names := make([]string, len(body.Models))
	for i, m := range body.Models {
		names[i] = m.Name
	}
	return names, nil
}
