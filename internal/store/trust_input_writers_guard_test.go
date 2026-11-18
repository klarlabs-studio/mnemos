package store_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// ADR 0036 §7: a write that changes confidence, half_life_days or
// last_confirmed without recording the values it replaced is the defect class
// that made a recorded-as-of read report today's trust inputs as yesterday's.
// TestTrustInputHistory_RecordsEveryWritePath proves the writers that exist
// record; this proves no other writer exists, so a statement added later
// cannot change one of the columns unversioned.
//
// The analysis is over the Go AST of each SQL backend (string literals and
// package-level string constants, including sqlc's generated queries), never a
// regex over Go source. Only the SQL text inside a literal is matched by
// pattern. A function is a writer when it holds SQL that assigns one of the
// columns on the claims table (an UPDATE ... SET col =, or an INSERT INTO
// claims naming it, whose conflict clause rewrites it). Taint flows from a
// writer to its callers until it reaches a function that records: one that
// calls recordTrustInputChange, or calls a helper that does. A tainted
// function nothing in the package calls is an unversioned entry point.
func TestTrustInputWriters_RecordHistory(t *testing.T) {
	backends := map[string][]string{
		"sqlite":   {"sqlite", "sqlite/sqlcgen"},
		"postgres": {"postgres"},
		"mysql":    {"mysql"},
	}
	for name, dirs := range backends {
		t.Run(name, func(t *testing.T) {
			g := loadCallGraph(t, dirs)
			if len(g.writers) < 3 {
				t.Fatalf("found %d trust-input writers (%v); the upsert, MarkVerified and MarkConfirmed statements at least must be found, so the guard is reading nothing",
					len(g.writers), g.writerNames())
			}
			for _, f := range g.unversionedEntryPoints() {
				t.Errorf("%s writes confidence, half_life_days or last_confirmed on claims without reaching recordTrustInputChange (ADR 0036 §7)", f)
			}
		})
	}
}

// Memory has no SQL: its one write of a claim row is putClaimLocked, which
// versions the trust inputs. Any other assignment into the claims map would
// bypass it.
func TestMemoryClaimWritesGoThroughPutClaimLocked(t *testing.T) {
	fset := token.NewFileSet()
	files := parseDir(t, fset, "memory")
	puts := 0
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				as, ok := n.(*ast.AssignStmt)
				if !ok {
					return true
				}
				for _, lhs := range as.Lhs {
					idx, ok := lhs.(*ast.IndexExpr)
					if !ok {
						continue
					}
					sel, ok := idx.X.(*ast.SelectorExpr)
					if !ok || sel.Sel.Name != "claims" {
						continue
					}
					if fn.Name.Name == "putClaimLocked" {
						puts++
						continue
					}
					t.Errorf("%s: %s assigns into the claims map directly; write through putClaimLocked so trust-input changes are versioned",
						fset.Position(as.Pos()), fn.Name.Name)
				}
				return true
			})
		}
	}
	if puts != 1 {
		t.Errorf("putClaimLocked assigns into the claims map %d times, want 1: the guard is not reading what it guards", puts)
	}
}

const recorderName = "recordTrustInputChange"

var (
	trustColumn  = `(confidence|half_life_days|last_confirmed)`
	updateAssign = regexp.MustCompile(`(?is)\bUPDATE\s+(\S+)\s.*\bSET\b.*\b` + trustColumn + `\s*=`)
	insertCols   = regexp.MustCompile(`(?is)\bINSERT\s+INTO\s+(\S+)\s*\(([^)]*)\)`)
	columnWord   = regexp.MustCompile(`(?i)\b` + trustColumn + `\b`)
)

// writesTrustColumn reports whether sql assigns a trust-input column on the
// claims table. A table spelled %s is qualified at runtime; the function's
// other literals must then name "claims".
func writesTrustColumn(sql string, namesClaims bool) bool {
	onClaims := func(table string) bool {
		table = strings.Trim(table, "`\"")
		return table == "claims" || (table == "%s" && namesClaims)
	}
	if m := updateAssign.FindStringSubmatch(sql); m != nil && onClaims(m[1]) {
		return true
	}
	if m := insertCols.FindStringSubmatch(sql); m != nil && onClaims(m[1]) && columnWord.MatchString(m[2]) {
		return true
	}
	return false
}

type funcInfo struct {
	name    string
	sql     []string
	calls   map[string]bool
	callers int
}

type callGraph struct {
	funcs   map[string]*funcInfo
	writers map[string]bool
}

func parseDir(t *testing.T, fset *token.FileSet, dir string) []*ast.File {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var files []*ast.File
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, e.Name()), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		files = append(files, f)
	}
	return files
}

func stringValue(lit *ast.BasicLit) (string, bool) {
	if lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	return s, err == nil
}

// concat renders a string expression built with +: literals as themselves,
// known constants as their value, anything else (a call, a variable) as %s,
// the way a runtime-qualified table name appears. ok is false when the
// expression holds no string literal at all.
func concat(expr ast.Expr, consts map[string]string) (string, bool) {
	switch x := expr.(type) {
	case *ast.BasicLit:
		return stringValue(x)
	case *ast.ParenExpr:
		return concat(x.X, consts)
	case *ast.Ident:
		s, ok := consts[x.Name]
		return s, ok
	case *ast.BinaryExpr:
		if x.Op != token.ADD {
			return "", false
		}
		l, lok := concat(x.X, consts)
		r, rok := concat(x.Y, consts)
		if !lok && !rok {
			return "", false
		}
		if !lok {
			l = "%s"
		}
		if !rok {
			r = "%s"
		}
		return l + r, true
	}
	return "", false
}

// loadCallGraph reads every function of dirs (one call namespace: sqlite calls
// sqlcgen's generated methods by name) and marks the writers.
func loadCallGraph(t *testing.T, dirs []string) callGraph {
	t.Helper()
	fset := token.NewFileSet()
	var files []*ast.File
	for _, d := range dirs {
		files = append(files, parseDir(t, fset, filepath.FromSlash(d))...)
	}
	consts := map[string]string{}
	for _, f := range files {
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				for i, n := range vs.Names {
					if i >= len(vs.Values) {
						continue
					}
					if s, ok := concat(vs.Values[i], consts); ok {
						consts[n.Name] = s
					} else if holdsStringLiteral(vs.Values[i]) {
						// A constant the guard cannot read is a constant drift
						// could hide in.
						t.Fatalf("%s: cannot evaluate string constant %s", fset.Position(n.Pos()), n.Name)
					}
				}
			}
		}
	}
	g := callGraph{funcs: map[string]*funcInfo{}, writers: map[string]bool{}}
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			info := g.funcs[fn.Name.Name]
			if info == nil {
				info = &funcInfo{name: fn.Name.Name, calls: map[string]bool{}}
				g.funcs[fn.Name.Name] = info
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.BasicLit:
					if s, ok := stringValue(x); ok {
						info.sql = append(info.sql, s)
					}
				case *ast.BinaryExpr:
					if s, ok := concat(x, consts); ok {
						info.sql = append(info.sql, s)
					}
				case *ast.Ident:
					if s, ok := consts[x.Name]; ok {
						info.sql = append(info.sql, s)
					}
				case *ast.CallExpr:
					switch fun := x.Fun.(type) {
					case *ast.Ident:
						info.calls[fun.Name] = true
					case *ast.SelectorExpr:
						info.calls[fun.Sel.Name] = true
					}
				}
				return true
			})
		}
	}
	for _, info := range g.funcs {
		namesClaims := false
		for _, s := range info.sql {
			if s == "claims" {
				namesClaims = true
			}
		}
		for _, s := range info.sql {
			if writesTrustColumn(s, namesClaims) {
				g.writers[info.name] = true
			}
		}
		for callee := range info.calls {
			if c, ok := g.funcs[callee]; ok && callee != info.name {
				c.callers++
			}
		}
	}
	return g
}

func holdsStringLiteral(expr ast.Expr) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			found = true
		}
		return !found
	})
	return found
}

func (g callGraph) writerNames() []string {
	out := make([]string, 0, len(g.writers))
	for n := range g.writers {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// records reports whether f calls the recorder, directly or through a helper
// that calls it directly.
func (g callGraph) records(f *funcInfo) bool {
	if f.calls[recorderName] || f.name == recorderName {
		return true
	}
	for callee := range f.calls {
		if c, ok := g.funcs[callee]; ok && c.calls[recorderName] {
			return true
		}
	}
	return false
}

// unversionedEntryPoints propagates taint from the writers to their callers,
// stopping at functions that record, and returns the tainted functions nothing
// in the package calls.
func (g callGraph) unversionedEntryPoints() []string {
	tainted := map[string]bool{}
	var queue []string
	for w := range g.writers {
		if !g.records(g.funcs[w]) {
			tainted[w] = true
			queue = append(queue, w)
		}
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, f := range g.funcs {
			if !f.calls[cur] || tainted[f.name] || g.records(f) {
				continue
			}
			tainted[f.name] = true
			queue = append(queue, f.name)
		}
	}
	var out []string
	for name := range tainted {
		if g.funcs[name].callers == 0 {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
