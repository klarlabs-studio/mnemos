// Command mutate runs an in-tree mutation testing harness.
//
// For each Go file in the target package(s), parse the AST, locate
// every binary operator that can be flipped to a meaningful sibling
// (>, <, >=, <=, ==, !=, &&, ||) and run `go test -overlay=...`
// against a copy whose operator was swapped. A mutation is "killed"
// when the test fails (exit non-zero), and "survived" when tests
// still pass — surviving mutants are coverage holes.
//
// The harness uses -overlay so the on-disk files are never modified.
//
// Usage:
//
//	go run ./tools/mutate -pkg ./internal/trust -threshold 0.70 -json mutation.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type mutator struct {
	Name string
	From token.Token
	To   token.Token
}

var mutators = []mutator{
	{"gt-to-lt", token.GTR, token.LSS},
	{"lt-to-gt", token.LSS, token.GTR},
	{"geq-to-leq", token.GEQ, token.LEQ},
	{"leq-to-geq", token.LEQ, token.GEQ},
	{"eq-to-neq", token.EQL, token.NEQ},
	{"neq-to-eq", token.NEQ, token.EQL},
	{"and-to-or", token.LAND, token.LOR},
	{"or-to-and", token.LOR, token.LAND},
}

type candidate struct {
	File   string
	Offset int
	Op     token.Token
	Line   int
	Column int
}

type result struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Column   int    `json:"column"`
	Mutator  string `json:"mutator"`
	From     string `json:"from"`
	To       string `json:"to"`
	Killed   bool   `json:"killed"`
	Duration string `json:"duration"`
}

func main() {
	var pkgFlag string
	var threshold float64
	var jsonOut string
	var verbose bool
	flag.StringVar(&pkgFlag, "pkg", "./internal/trust", "package path to mutate (single package)")
	flag.Float64Var(&threshold, "threshold", 0.70, "minimum kill rate; exit 1 if below")
	flag.StringVar(&jsonOut, "json", "", "write JSON report to this path")
	flag.BoolVar(&verbose, "v", false, "verbose progress")
	flag.Parse()

	pkgAbs, err := filepath.Abs(pkgFlag)
	if err != nil {
		fatal(err)
	}

	files, err := collectGoFiles(pkgAbs)
	if err != nil {
		fatal(err)
	}
	if len(files) == 0 {
		fatal(fmt.Errorf("no .go files in %s", pkgAbs))
	}

	fset := token.NewFileSet()
	srcs := map[string][]byte{}
	var cands []candidate
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			fatal(err)
		}
		srcs[f] = b
		af, err := parser.ParseFile(fset, f, b, parser.ParseComments)
		if err != nil {
			fatal(err)
		}
		ast.Inspect(af, func(n ast.Node) bool {
			be, ok := n.(*ast.BinaryExpr)
			if !ok {
				return true
			}
			for _, m := range mutators {
				if be.Op == m.From {
					p := fset.Position(be.OpPos)
					cands = append(cands, candidate{
						File:   f,
						Offset: p.Offset,
						Op:     be.Op,
						Line:   p.Line,
						Column: p.Column,
					})
					break
				}
			}
			return true
		})
	}

	if len(cands) == 0 {
		fmt.Println("no mutation candidates found")
		os.Exit(0)
	}

	if verbose {
		fmt.Fprintf(os.Stderr, "baseline test run for %s\n", pkgFlag)
	}
	if rc := goTest(pkgFlag, ""); rc != 0 {
		fatal(fmt.Errorf("baseline tests fail (rc=%d) — fix before mutating", rc))
	}

	tmp, err := os.MkdirTemp("", "mnemos-mutate-")
	if err != nil {
		fatal(err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	var results []result
	killed, total := 0, 0
	start := time.Now()
	for i, c := range cands {
		for _, m := range mutators {
			if m.From != c.Op {
				continue
			}
			modified, ok := mutateBytes(srcs[c.File], c.Offset, m.From, m.To)
			if !ok {
				continue
			}
			tmpFile := filepath.Join(tmp, fmt.Sprintf("mut_%04d_%s.go", i, m.Name))
			if err := os.WriteFile(tmpFile, modified, 0o600); err != nil {
				fatal(err)
			}
			ovPath := filepath.Join(tmp, fmt.Sprintf("overlay_%04d.json", i))
			ov := map[string]any{"Replace": map[string]string{c.File: tmpFile}}
			ob, _ := json.Marshal(ov)
			if err := os.WriteFile(ovPath, ob, 0o600); err != nil {
				fatal(err)
			}
			t0 := time.Now()
			rc := goTest(pkgFlag, ovPath)
			d := time.Since(t0)
			r := result{
				File:     relPath(c.File),
				Line:     c.Line,
				Column:   c.Column,
				Mutator:  m.Name,
				From:     m.From.String(),
				To:       m.To.String(),
				Killed:   rc != 0,
				Duration: d.Round(time.Millisecond).String(),
			}
			results = append(results, r)
			total++
			if r.Killed {
				killed++
			}
			if verbose {
				status := "SURVIVED"
				if r.Killed {
					status = "killed  "
				}
				fmt.Fprintf(os.Stderr, "[%3d/%3d] %s %s:%d:%d %s->%s (%s)\n",
					total, len(cands), status, r.File, r.Line, r.Column, r.From, r.To, r.Duration)
			}
			_ = os.Remove(tmpFile)
			_ = os.Remove(ovPath)
		}
	}
	elapsed := time.Since(start)

	rate := 0.0
	if total > 0 {
		rate = float64(killed) / float64(total)
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].Killed != results[j].Killed {
			return !results[i].Killed
		}
		if results[i].File != results[j].File {
			return results[i].File < results[j].File
		}
		return results[i].Line < results[j].Line
	})

	fmt.Printf("\nmutation report for %s\n", pkgFlag)
	fmt.Printf("  total:     %d\n", total)
	fmt.Printf("  killed:    %d\n", killed)
	fmt.Printf("  survived:  %d\n", total-killed)
	fmt.Printf("  kill_rate: %.4f\n", rate)
	fmt.Printf("  threshold: %.4f\n", threshold)
	fmt.Printf("  elapsed:   %s\n", elapsed.Round(time.Second))

	survivors := 0
	for _, r := range results {
		if r.Killed {
			continue
		}
		if survivors == 0 {
			fmt.Println("\nsurvivors:")
		}
		fmt.Printf("  %s:%d:%d  %s->%s  (%s)\n",
			r.File, r.Line, r.Column, r.From, r.To, r.Mutator)
		survivors++
	}

	if jsonOut != "" {
		out := map[string]any{
			"package":   pkgFlag,
			"total":     total,
			"killed":    killed,
			"survived":  total - killed,
			"kill_rate": rate,
			"threshold": threshold,
			"elapsed":   elapsed.String(),
			"results":   results,
		}
		b, _ := json.MarshalIndent(out, "", "  ")
		if err := os.WriteFile(jsonOut, b, 0o600); err != nil {
			fatal(err)
		}
	}

	if rate < threshold {
		fmt.Fprintf(os.Stderr, "\nFAIL: kill_rate %.4f below threshold %.4f\n", rate, threshold)
		os.Exit(1)
	}
}

func mutateBytes(src []byte, offset int, from, to token.Token) ([]byte, bool) {
	fromStr := from.String()
	toStr := to.String()
	if offset+len(fromStr) > len(src) {
		return nil, false
	}
	if string(src[offset:offset+len(fromStr)]) != fromStr {
		return nil, false
	}
	out := make([]byte, 0, len(src)+len(toStr)-len(fromStr))
	out = append(out, src[:offset]...)
	out = append(out, []byte(toStr)...)
	out = append(out, src[offset+len(fromStr):]...)
	return out, true
}

func collectGoFiles(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != dir {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") {
			return nil
		}
		if strings.HasSuffix(p, "_test.go") {
			return nil
		}
		out = append(out, p)
		return nil
	})
	return out, err
}

func goTest(pkg, overlay string) int {
	args := []string{"test", "-count=1", "-timeout=60s"}
	if overlay != "" {
		args = append(args, "-overlay="+overlay)
	}
	args = append(args, pkg)
	cmd := exec.Command("go", args...)
	// Output is intentionally discarded; only the exit code drives the
	// mutation kill/survive decision.
	_ = cmd.Run()
	if cmd.ProcessState == nil {
		return 1
	}
	return cmd.ProcessState.ExitCode()
}

func relPath(p string) string {
	wd, err := os.Getwd()
	if err != nil {
		return p
	}
	r, err := filepath.Rel(wd, p)
	if err != nil {
		return p
	}
	return r
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "mutate:", err)
	os.Exit(2)
}
