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
	"strconv"
	"strings"

	"go.klarlabs.de/mnemos/internal/brainbench"

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
	)
	flag.StringVar(&scenarioDir, "scenarios", "data/eval/brainbench", "directory of scenario YAML files")
	flag.StringVar(&jsonOut, "json", "", "write the machine-readable report to this path (\"-\" for stdout)")
	flag.StringVar(&workDir, "work", "", "keep experiment databases here instead of a temp dir (for debugging)")
	flag.BoolVar(&human, "human", true, "print the human-readable report to stdout")
	flag.BoolVar(&failOnReg, "fail-on-regression", false, "exit 1 when any scored metric degraded")
	flag.StringVar(&mode, "mode", "ab", "ab: the paired consolidation experiment; ablation: retrieval ranking ablation")
	flag.StringVar(&budgets, "budgets", "", "ablation token budgets, comma-separated (default 32,64,128,256,512,1024,2048)")
	flag.Parse()

	if mode == "ablation" {
		if err := runAblation(scenarioDir, jsonOut, workDir, budgets, human); err != nil {
			fmt.Fprintf(os.Stderr, "brainbench: %v\n", err)
			os.Exit(2)
		}
		return
	}
	if mode != "ab" {
		fmt.Fprintf(os.Stderr, "brainbench: unknown -mode %q (want ab or ablation)\n", mode)
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
