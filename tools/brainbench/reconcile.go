package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"go.klarlabs.de/mnemos/internal/brainbench"
	"go.klarlabs.de/mnemos/internal/llm"
)

type reconcileFlags struct {
	pairs     string
	models    string
	baseURL   string
	bootstrap int
	seed      uint64
}

// runReconcile runs the deterministic-vs-LLM reconciliation experiment
// (ADR 0042). LLM arms run only when -llm-model names models, and only against
// a local Ollama: the experiment is defined as zero-spend, so no cloud provider
// is reachable from here.
func runReconcile(f reconcileFlags, jsonOut string, human bool) error {
	set, err := brainbench.LoadReconcileSet(f.pairs)
	if err != nil {
		return err
	}
	ctx := context.Background()
	var judges []brainbench.NamedJudge
	for _, model := range splitModels(f.models) {
		client := llm.NewOpenAIClient(f.baseURL, "", model, string(llm.ProviderOllama)).
			WithTemperature(0).WithSeed(int(f.seed)) //nolint:gosec // small operator-supplied seed
		judge := brainbench.NewLLMJudge(client)
		// One untimed warm-up call loads the model, so the first pair's
		// latency is not the model load. It doubles as the reachability check:
		// an unreachable Ollama stops the run rather than scoring every pair as
		// a failed call.
		warm := judge.Judge(ctx, "The sky is blue.", "Grass is green.")
		if warm.Err != "" {
			return fmt.Errorf("ollama model %s at %s unreachable: %s", model, f.baseURL, warm.Err)
		}
		fmt.Fprintf(os.Stderr, "brainbench: %s warm (%.0f ms)\n", model, warm.LatencyMS)
		judges = append(judges, brainbench.NamedJudge{Model: model, Judge: judge})
	}
	start := time.Now()
	report, err := brainbench.RunReconcile(ctx, set, brainbench.ReconcileOptions{
		Judges: judges, BootstrapIters: f.bootstrap, Seed: f.seed,
		Progress: func(model string, done, total int) {
			if done%25 == 0 || done == total {
				fmt.Fprintf(os.Stderr, "brainbench: %s %d/%d (%s)\n", model, done, total, time.Since(start).Round(time.Second))
			}
		},
	})
	if err != nil {
		return err
	}
	if human {
		if err := report.WriteHuman(os.Stdout); err != nil {
			return err
		}
	}
	return writeJSON(jsonOut, report.WriteJSON)
}

func splitModels(s string) []string {
	var out []string
	for _, m := range strings.Split(s, ",") {
		if m = strings.TrimSpace(m); m != "" {
			out = append(out, m)
		}
	}
	return out
}
