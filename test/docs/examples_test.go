// Package docs compiles every Go example in the published documentation as an
// external consumer would: from a throwaway module that lives outside this
// module's boundary and reaches mnemos only through a replace directive.
//
// That boundary is the point. Inside this module `go.klarlabs.de/mnemos/internal/...`
// imports compile, so a documentation snippet blank-importing
// internal/store/sqlite looked fine to every test in the repo while being
// uncompilable for every reader who copied it. Only a module outside the tree is
// subject to Go's internal-package rule.
//
// Fragments (statements without a package clause) are wrapped, not skipped: the
// harness hoists their imports and top-level declarations, runs the statements
// inside a function that supplies the ambient names a reader is assumed to hold
// (ctx, mem, logger), and blank-uses every variable the fragment declares at its
// top level. Anything else the fragment needs has to be in the fragment. A block
// that genuinely cannot compile (an excerpt of a declaration inside another
// package) opts out with an HTML comment directly above it:
//
//	<!-- doccheck:skip excerpt of the providers package's own declarations -->
//
// The reason is mandatory; a bare skip fails the test.
package docs

import (
	"bufio"
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// documentRoots are the published documentation trees, relative to the repo
// root. docs/adr is excluded on purpose: ADRs are immutable historical records,
// and their snippets describe the code as it was when the decision was made.
var documentRoots = []string{"README.md", "docs", "docs-site/docs"}

var excludedDirs = map[string]string{
	"docs/adr": "ADRs are immutable history; their snippets are not current usage",
}

// ambientImports maps a package qualifier a fragment may use without importing
// it to its import path. A full program (package clause) gets none of these —
// it must be complete as written.
var ambientImports = map[string]string{
	"context":   "context",
	"errors":    "errors",
	"fmt":       "fmt",
	"os":        "os",
	"time":      "time",
	"mnemos":    "go.klarlabs.de/mnemos",
	"client":    "go.klarlabs.de/mnemos/client",
	"providers": "go.klarlabs.de/mnemos/providers",
	"bolt":      "go.klarlabs.de/bolt",
	"retry":     "go.klarlabs.de/fortify/retry",
	"embed":     "github.com/felixgeelhaar/chronos/embed",
}

// ambientParams are the names a fragment may use without declaring them.
const ambientParams = "ctx context.Context, mem mnemos.Memory, logger *bolt.Logger"

type example struct {
	file string // repo-relative
	line int    // line of the opening fence
	body string
	skip string // reason, when opted out
}

func (e example) name() string { return fmt.Sprintf("%s:%d", e.file, e.line) }

func TestDocumentedGoExamplesCompileFromExternalModule(t *testing.T) {
	if testing.Short() {
		t.Skip("builds an external module; skipped in -short")
	}
	root := repoRoot(t)
	examples := collectExamples(t, root)
	if len(examples) == 0 {
		t.Fatal("found no Go examples; the collector is broken, not the docs clean")
	}

	mod := newExternalModule(t, root)
	var built []example
	for i, ex := range examples {
		if ex.skip != "" {
			t.Logf("skip %s: %s", ex.name(), ex.skip)
			continue
		}
		src, err := render(ex, i)
		if err != nil {
			t.Errorf("%s: %v", ex.name(), err)
			continue
		}
		mod.add(t, fmt.Sprintf("ex%03d", i), src)
		built = append(built, ex)
	}
	if out, err := mod.build(); err != nil {
		t.Fatalf("documented Go examples do not compile from an external module:\n%s\nexamples:\n%s",
			mod.attribute(out), listing(built))
	}
	t.Logf("compiled %d documented Go examples from an external module", len(built))
}

// TestExternalModuleEnforcesInternalBoundary proves the harness would have
// caught the original defect: a snippet importing an internal provider must
// fail to build. If this ever passes, the fixture is building from inside the
// module boundary and every green result above is meaningless.
func TestExternalModuleEnforcesInternalBoundary(t *testing.T) {
	if testing.Short() {
		t.Skip("builds an external module; skipped in -short")
	}
	root := repoRoot(t)
	mod := newExternalModule(t, root)
	mod.add(t, "leak", "package leak\n\nimport _ \"go.klarlabs.de/mnemos/internal/store/sqlite\"\n")
	out, err := mod.build()
	if err == nil {
		t.Fatal("an external module imported go.klarlabs.de/mnemos/internal/...; the harness is not outside the module boundary")
	}
	if !strings.Contains(out, "internal package") {
		t.Fatalf("build failed, but not on the internal-package rule:\n%s", out)
	}
}

// --- collection -----------------------------------------------------------

var skipMarker = regexp.MustCompile(`^<!--\s*doccheck:skip\b(.*?)-->\s*$`)

func collectExamples(t *testing.T, root string) []example {
	t.Helper()
	var files []string
	for _, r := range documentRoots {
		p := filepath.Join(root, r)
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("documentation root %s: %v", r, err)
		}
		if !info.IsDir() {
			files = append(files, r)
			continue
		}
		err = filepath.WalkDir(p, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				if _, ok := excludedDirs[rel]; ok {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasSuffix(rel, ".md") {
				files = append(files, rel)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(files)

	var out []example
	for _, f := range files {
		out = append(out, examplesIn(t, root, f)...)
	}
	return out
}

func examplesIn(t *testing.T, root, rel string) []example {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	var (
		out      []example
		cur      *example
		buf      strings.Builder
		lastLine string // previous non-blank line outside a fence
	)
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		switch {
		case cur == nil && trimmed == "```go":
			cur = &example{file: rel, line: n}
			if m := skipMarker.FindStringSubmatch(lastLine); m != nil {
				cur.skip = strings.TrimSpace(m[1])
				if cur.skip == "" {
					t.Errorf("%s:%d: doccheck:skip without a reason excuses nothing", rel, n)
					cur.skip = "(no reason)"
				}
			}
			buf.Reset()
		case cur != nil && trimmed == "```":
			cur.body = buf.String()
			out = append(out, *cur)
			cur = nil
		case cur != nil:
			buf.WriteString(line)
			buf.WriteByte('\n')
		case trimmed != "":
			lastLine = trimmed
		}
	}
	if cur != nil {
		t.Errorf("%s:%d: unterminated ```go fence", rel, cur.line)
	}
	return out
}

// --- rendering ------------------------------------------------------------

var importLine = regexp.MustCompile(`^import\s+(?:(_|\.|[A-Za-z_]\w*)\s+)?("[^"]+")\s*(//.*)?$`)

// render turns one documented example into a compilable Go file in package pkg.
func render(ex example, idx int) (string, error) {
	body := strings.TrimLeft(ex.body, "\n")
	if strings.HasPrefix(body, "package ") {
		// A full program is compiled exactly as published.
		return body, nil
	}
	pkg := fmt.Sprintf("ex%03d", idx)
	imports, rest, err := splitImports(body)
	if err != nil {
		return "", err
	}
	decls, stmts := splitDecls(rest)

	imported := map[string]bool{}
	for _, spec := range imports {
		imported[importPath(spec)] = true
	}
	src := decls + "\n" + stmts
	needed := []string{"context", "go.klarlabs.de/mnemos", "go.klarlabs.de/bolt"} // ambientParams
	for q, path := range ambientImports {
		if regexp.MustCompile(`\b` + q + `\.`).MatchString(src) {
			needed = append(needed, path)
		}
	}
	for _, path := range needed {
		if !imported[path] {
			imported[path] = true
			imports = append(imports, fmt.Sprintf("%q", path))
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "package %s\n\nimport (\n", pkg)
	for _, spec := range imports {
		fmt.Fprintf(&b, "\t%s\n", spec)
	}
	b.WriteString(")\n\n")
	b.WriteString(decls)
	fmt.Fprintf(&b, "\n// documented at %s\nfunc _(%s) {\n\t_, _, _ = ctx, mem, logger\n\t{\n%s\n", ex.name(), ambientParams, stmts)
	uses, err := blankUses(stmts)
	if err != nil {
		return "", fmt.Errorf("parse statements: %w", err)
	}
	b.WriteString(uses)
	b.WriteString("\t}\n}\n")
	return b.String(), nil
}

// splitImports peels leading import declarations off a fragment and returns
// their specs (`_ "path"`, `"path"`) and the remainder.
func splitImports(body string) (specs []string, rest string, err error) {
	lines := strings.Split(body, "\n")
	i := 0
	for i < len(lines) {
		l := strings.TrimSpace(lines[i])
		switch {
		case l == "":
			i++
		case l == "import (":
			i++
			for ; i < len(lines) && strings.TrimSpace(lines[i]) != ")"; i++ {
				s := strings.TrimSpace(lines[i])
				if s == "" || strings.HasPrefix(s, "//") {
					continue
				}
				if c := strings.Index(s, "//"); c > 0 {
					s = strings.TrimSpace(s[:c])
				}
				specs = append(specs, s)
			}
			if i == len(lines) {
				return nil, "", fmt.Errorf("unterminated import block")
			}
			i++
		case importLine.MatchString(l):
			m := importLine.FindStringSubmatch(l)
			specs = append(specs, strings.TrimSpace(m[1]+" "+m[2]))
			i++
		default:
			return specs, strings.Join(lines[i:], "\n"), nil
		}
	}
	return specs, "", nil
}

func importPath(spec string) string {
	f := strings.Fields(spec)
	return strings.Trim(f[len(f)-1], `"`)
}

// splitDecls separates top-level `type`/`func` declarations (which must live at
// package scope) from the statements that go inside the wrapper function.
// Declarations are recognised at column zero and run until braces balance.
func splitDecls(src string) (decls, stmts string) {
	var d, s strings.Builder
	lines := strings.Split(src, "\n")
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		if !strings.HasPrefix(l, "type ") && !strings.HasPrefix(l, "func ") {
			s.WriteString("\t\t" + l + "\n")
			continue
		}
		depth, opened := 0, false
		for ; i < len(lines); i++ {
			d.WriteString(lines[i] + "\n")
			o, c := strings.Count(lines[i], "{"), strings.Count(lines[i], "}")
			depth += o - c
			opened = opened || o > 0
			if opened && depth <= 0 {
				break
			}
		}
		d.WriteString("\n")
	}
	return d.String(), s.String()
}

// blankUses returns `_ = x` for every variable the fragment declares at its own
// top level, so an example that binds a result to show its shape is not
// rejected as "declared and not used". Variables declared in nested blocks are
// not covered: an unused loop variable is a defect in the example.
func blankUses(stmts string) (string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), "", "package p\nfunc _() {\n"+stmts+"\n}\n", 0)
	if err != nil {
		return "", err
	}
	fn := f.Decls[0].(*ast.FuncDecl)
	var names []string
	add := func(id *ast.Ident) {
		if id.Name != "_" {
			names = append(names, id.Name)
		}
	}
	for _, st := range fn.Body.List {
		switch st := st.(type) {
		case *ast.AssignStmt:
			if st.Tok == token.DEFINE {
				for _, lhs := range st.Lhs {
					if id, ok := lhs.(*ast.Ident); ok {
						add(id)
					}
				}
			}
		case *ast.DeclStmt:
			if gd, ok := st.Decl.(*ast.GenDecl); ok && gd.Tok == token.VAR {
				for _, sp := range gd.Specs {
					for _, id := range sp.(*ast.ValueSpec).Names {
						add(id)
					}
				}
			}
		}
	}
	var b strings.Builder
	for _, n := range names {
		fmt.Fprintf(&b, "\t\t_ = %s\n", n)
	}
	return b.String(), nil
}

// --- external module ------------------------------------------------------

type externalModule struct {
	dir   string
	files map[string]example // package dir -> source example, for attribution
}

// newExternalModule creates a module outside the repository that depends on
// mnemos through a replace directive. The repo's go.sum is reused so dependency
// resolution matches the main module and needs no new downloads.
func newExternalModule(t *testing.T, root string) *externalModule {
	t.Helper()
	dir := t.TempDir()
	if rel, err := filepath.Rel(root, dir); err == nil && !strings.HasPrefix(rel, "..") {
		t.Fatalf("temp dir %s is inside the repository; the internal-package rule would not apply", dir)
	}
	gomod := fmt.Sprintf("module example.com/mnemosdocs\n\ngo 1.26\n\nrequire go.klarlabs.de/mnemos v0.0.0\n\nreplace go.klarlabs.de/mnemos => %s\n", root)
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o644); err != nil {
		t.Fatal(err)
	}
	sum, err := os.ReadFile(filepath.Join(root, "go.sum"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.sum"), sum, 0o644); err != nil {
		t.Fatal(err)
	}
	return &externalModule{dir: dir, files: map[string]example{}}
}

func (m *externalModule) add(t *testing.T, pkg, src string) {
	t.Helper()
	p := filepath.Join(m.dir, pkg)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "example.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (m *externalModule) build() (string, error) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		return "", fmt.Errorf("go toolchain not on PATH: %w", err)
	}
	cmd := exec.Command(gobin, "build", "-mod=mod", "./...")
	cmd.Dir = m.dir
	// GOWORK=off: a go.work in a parent directory would pull the example module
	// back into a workspace with mnemos and blur the boundary under test.
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// attribute rewrites generated package paths in build output back to the
// documentation location each one came from.
func (m *externalModule) attribute(out string) string {
	return regexp.MustCompile(`ex\d{3}/example\.go`).ReplaceAllStringFunc(out, func(s string) string {
		src, err := os.ReadFile(filepath.Join(m.dir, s))
		if err != nil {
			return s
		}
		if mm := regexp.MustCompile(`// documented at (\S+)`).FindSubmatch(src); mm != nil {
			return s + " [" + string(mm[1]) + "]"
		}
		return s
	})
}

func listing(exs []example) string {
	var b strings.Builder
	for _, e := range exs {
		fmt.Fprintf(&b, "  %s\n", e.name())
	}
	return b.String()
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for dir := wd; ; dir = filepath.Dir(dir) {
		if data, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil &&
			bytes.HasPrefix(data, []byte("module go.klarlabs.de/mnemos\n")) {
			return dir
		}
		if filepath.Dir(dir) == dir {
			t.Fatal("repository root (module go.klarlabs.de/mnemos) not found")
		}
	}
}

// TestPublicPackageDocsDoNotImportInternal covers the Go doc comments the
// markdown harness above cannot see. Package documentation renders on
// pkg.go.dev as the first thing a consumer copies, and memory.go's storage
// section told readers to blank-import internal/store/memory and
// internal/store/postgres. Only packages a consumer can import are checked.
func TestPublicPackageDocsDoNotImportInternal(t *testing.T) {
	root := repoRoot(t)
	internalImport := regexp.MustCompile(`(?:^|\s)(?:_\s+|import\s+)"go\.klarlabs\.de/mnemos/(?:[\w/]+/)?internal/`)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			base := d.Name()
			if rel != "." && (base == "internal" || base == "testdata" || strings.HasPrefix(base, ".") || base == "cmd" || base == "tools") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments|parser.PackageClauseOnly)
		if err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		for _, cg := range f.Comments {
			for _, c := range cg.List {
				if internalImport.MatchString(c.Text) {
					t.Errorf("%s: documentation tells consumers to import an internal package: %s", rel, strings.TrimSpace(c.Text))
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
