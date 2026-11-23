// Command brainbench runs the cognitive-process evaluation harness: a paired
// A/B experiment that measures whether consolidation actually improves what a
// brain knows, rather than merely that it ran.
//
// Usage:
//
//	go run ./tools/brainbench                                  # human report
//	go run ./tools/brainbench -json brainbench.json            # machine-readable
//	go run ./tools/brainbench -scenarios data/eval/brainbench  # explicit fixtures
//	go run ./tools/brainbench -fail-on-regression              # exit 1 on any degraded metric
//	go run ./tools/brainbench -mode ablation                   # retrieval ablation at fixed token budgets
//	go run ./tools/brainbench -mode ablation -budgets 64,256   # explicit budgets
//	go run ./tools/brainbench -mode reconcile                  # deterministic reconciliation arm only
//	go run ./tools/brainbench -mode taskstate                  # gated task-state block vs FIFO block (ADR 0050)
//	go run ./tools/brainbench -mode reconcile -llm-model gemma3:12b,qwen2.5:3b  # + llm and hybrid arms (local Ollama)
//
// See internal/brainbench for the method and its limitations; the limitations
// are also embedded in every report it emits.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"go.klarlabs.de/mnemos/internal/brainbench"
	"go.klarlabs.de/mnemos/internal/llm"

	// The harness opens sqlite:// DSNs for every arm.
	_ "go.klarlabs.de/mnemos/internal/store/sqlite"
)

func main() {
	var (
		scenarioDir string
		jsonOut     string
		workDir     string
		human       bool
		failOnReg   bool
		mode        string
		budgets     string
		recon       reconcileFlags
	)
	flag.StringVar(&scenarioDir, "scenarios", "data/eval/brainbench", "directory of scenario YAML files")
	flag.StringVar(&jsonOut, "json", "", "write the machine-readable report to this path (\"-\" for stdout)")
	flag.StringVar(&workDir, "work", "", "keep experiment databases here instead of a temp dir (for debugging)")
	flag.BoolVar(&human, "human", true, "print the human-readable report to stdout")
	flag.BoolVar(&failOnReg, "fail-on-regression", false, "exit 1 when any scored metric degraded")
	flag.StringVar(&mode, "mode", "ab", "ab: the paired consolidation experiment; ablation: retrieval ranking ablation; reconcile: deterministic vs LLM reconciliation (ADR 0042); taskstate: gated task-state block vs FIFO block (ADR 0050)")
	flag.StringVar(&budgets, "budgets", "", "ablation token budgets, comma-separated (default 32,64,128,256,512,1024,2048)")
	flag.StringVar(&recon.pairs, "pairs", "data/eval/reconciliation/pairs.yaml", "reconcile: labelled pair set")
	flag.StringVar(&recon.models, "llm-model", "", "reconcile: comma-separated local Ollama models for the llm and hybrid arms (empty: deterministic arm only)")
	flag.StringVar(&recon.baseURL, "llm-base-url", llm.DefaultBaseURL(llm.ProviderOllama), "reconcile: Ollama endpoint")
	flag.IntVar(&recon.bootstrap, "bootstrap", 2000, "reconcile: bootstrap resamples")
	flag.Uint64Var(&recon.seed, "seed", 42, "reconcile: bootstrap and sampling seed")
	flag.Parse()

	if mode == "reconcile" {
		if err := runReconcile(recon, jsonOut, human); err != nil {
			fmt.Fprintf(os.Stderr, "brainbench: %v\n", err)
			os.Exit(2)
		}
		return
	}
	if mode == "taskstate" {
		if err := runTaskState(filepath.Join(scenarioDir, "taskstate"), jsonOut, workDir, human); err != nil {
			fmt.Fprintf(os.Stderr, "brainbench: %v\n", err)
			os.Exit(2)
		}
		return
	}
	if mode == "ablation" {
		if err := runAblation(scenarioDir, jsonOut, workDir, budgets, human); err != nil {
			fmt.Fprintf(os.Stderr, "brainbench: %v\n", err)
			os.Exit(2)
		}
		return
	}
	if mode != "ab" {
		fmt.Fprintf(os.Stderr, "brainbench: unknown -mode %q (want ab, ablation, reconcile or taskstate)\n", mode)
		os.Exit(2)
	}
	if err := run(scenarioDir, jsonOut, workDir, human, failOnReg); err != nil {
		fmt.Fprintf(os.Stderr, "brainbench: %v\n", err)
		os.Exit(2)
	}
}

func run(scenarioDir, jsonOut, workDir string, human, failOnReg bool) error {
	scenarios, err := brainbench.LoadScenarios(scenarioDir)
	if err != nil {
		return err
	}

	workDir, cleanup, err := prepareWorkDir(workDir)
	if err != nil {
		return err
	}
	defer cleanup()

	ctx := context.Background()
	results := make([]brainbench.ScenarioResult, 0, len(scenarios))
	for _, sc := range scenarios {
		res, err := brainbench.Run(ctx, sc, workDir)
		if err != nil {
			return err
		}
		results = append(results, res)
	}

	report := brainbench.BuildReport(results)

	if human {
		if err := report.WriteHuman(os.Stdout); err != nil {
			return err
		}
	}
	if err := writeJSON(jsonOut, report.WriteJSON); err != nil {
		return err
	}

	// Exit 1 is opt-in. A regression is a finding the harness must SHOW; making
	// it break the build by default would create pressure to weaken scenarios
	// until they stop reporting one, which is precisely the failure this
	// harness exists to prevent.
	if failOnReg && report.HasRegression() {
		return fmt.Errorf("%d scored metric(s) degraded under the process set", report.Summary.Worse)
	}
	return nil
}

// runAblation runs the retrieval ablation and writes its report.
// runTaskState runs the gated task-state experiment (ADR 0050). Like the other
// modes it reports and never gates.
func runTaskState(dir, jsonOut, workDir string, human bool) error {
	scenarios, err := brainbench.LoadTaskStateScenarios(dir)
	if err != nil {
		return err
	}
	workDir, cleanup, err := prepareWorkDir(workDir)
	if err != nil {
		return err
	}
	defer cleanup()
	report, err := brainbench.RunTaskState(context.Background(), scenarios, workDir)
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

func runAblation(scenarioDir, jsonOut, workDir, budgetList string, human bool) error {
	budgets, err := parseBudgets(budgetList)
	if err != nil {
		return err
	}
	scenarios, err := brainbench.LoadScenarios(scenarioDir)
	if err != nil {
		return err
	}
	workDir, cleanup, err := prepareWorkDir(workDir)
	if err != nil {
		return err
	}
	defer cleanup()

	report, err := brainbench.RunAblation(context.Background(), scenarios, workDir, budgets)
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

// parseBudgets reads a comma-separated list of positive token budgets; empty
// selects the defaults.
func parseBudgets(s string) ([]int, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var out []int
	for _, f := range strings.Split(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(f))
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("budget %q is not a positive integer", f)
		}
		out = append(out, n)
	}
	return out, nil
}

// prepareWorkDir returns the directory experiment databases go in. They are
// throwaway by default: each arm is written to, measured once, and never valid
// to reuse. Keeping them around is a debugging affordance, not the norm.
func prepareWorkDir(workDir string) (string, func(), error) {
	if workDir == "" {
		dir, err := os.MkdirTemp("", "brainbench-")
		if err != nil {
			return "", nil, fmt.Errorf("create work dir: %w", err)
		}
		return dir, func() { _ = os.RemoveAll(dir) }, nil
	}
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return "", nil, fmt.Errorf("create work dir: %w", err)
	}
	return workDir, func() {}, nil
}

// writeJSON writes a report to path ("-" for stdout; empty writes nothing).
func writeJSON(path string, write func(io.Writer) error) error {
	if path == "" {
		return nil
	}
	if path == "-" {
		return write(os.Stdout)
	}
	f, err := os.Create(path) //nolint:gosec // operator-supplied output path
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if err := write(f); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "brainbench: wrote %s\n", path)
	return nil
}
