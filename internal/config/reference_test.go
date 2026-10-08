package config

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The committed reference is what tools/configdoc would write now.
func TestReference_CommittedFileIsCurrent(t *testing.T) {
	want, err := Reference()
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join("..", "..", "docs", "reference", "configuration.md"))
	if err != nil {
		t.Fatalf("%v (run: go run ./tools/configdoc)", err)
	}
	if string(got) != want {
		t.Fatal("docs/reference/configuration.md is stale: run go run ./tools/configdoc")
	}
}

// Every setting is documented, and its documented YAML key really sets its
// variable: a one-line mnemos.yaml at that key must produce that variable
// through EnvOverrides.
func TestSettings_DocumentedAndRoundTrip(t *testing.T) {
	settings, err := Settings()
	if err != nil {
		t.Fatal(err)
	}
	if len(settings) < 40 {
		t.Fatalf("only %d settings parsed; the reader lost the Config struct", len(settings))
	}
	for _, s := range settings {
		if len(strings.Fields(s.Description)) < 3 {
			t.Errorf("%s (%s) has no description: give the Config field a doc comment", s.Env, s.YAML)
		}
		path := strings.Split(s.YAML, ".")
		var b strings.Builder
		for i, p := range path {
			b.WriteString(strings.Repeat("  ", i) + p + ":")
			if i == len(path)-1 {
				b.WriteString(" probe-value")
			}
			b.WriteString("\n")
		}
		file := filepath.Join(t.TempDir(), "mnemos.yaml")
		if err := os.WriteFile(file, []byte(b.String()), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, err := Load(file)
		if err != nil {
			t.Fatalf("%s: %v", s.YAML, err)
		}
		if got := cfg.EnvOverrides()[s.Env]; got != "probe-value" {
			t.Errorf("yaml key %s does not set %s (got %q)", s.YAML, s.Env, got)
		}
	}
}

var envName = regexp.MustCompile(`^MNEMOS_[A-Z0-9_]*[A-Z0-9]$`)

// Every MNEMOS_* name a non-test source file spells out is either a setting
// or listed in InternalEnv, so a new variable cannot ship undocumented. 27 of
// 74 had, before this test. Names ending in "_" are prefixes for families
// matched at runtime and are not variables. Every InternalEnv entry must also
// still be read somewhere, so the list cannot outlive the code.
func TestEveryEnvNameTheCodeReadsIsDocumented(t *testing.T) {
	settings, err := Settings()
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	for _, s := range settings {
		known[s.Env] = true
	}
	internal := map[string]bool{}
	for _, v := range InternalEnv {
		if len(strings.Fields(v.Description)) < 3 {
			t.Errorf("InternalEnv %s has no description", v.Name)
		}
		internal[v.Name] = true
	}
	seen := map[string][]string{}
	root := filepath.Join("..", "..")
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "testdata", "docs-site", "benchmarks":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err == nil && envName.MatchString(s) {
				rel, _ := filepath.Rel(root, path)
				seen[s] = append(seen[s], rel)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) < 40 {
		t.Fatalf("only %d MNEMOS_* names found; the scan is broken", len(seen))
	}
	var missing []string
	for name, files := range seen {
		if !known[name] && !internal[name] {
			missing = append(missing, name+" ("+files[0]+")")
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("undocumented environment variable %s: add a Config field or an InternalEnv entry", m)
	}
	for name := range internal {
		if len(seen[name]) == 0 {
			t.Errorf("InternalEnv %s is no longer read by any source file; remove it", name)
		}
		if known[name] {
			t.Errorf("%s is both a setting and in InternalEnv", name)
		}
	}
}
