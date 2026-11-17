package query_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// searchedDirs are the packages that build AnswerOptions on a production read
// path. Relative to internal/query.
var searchedDirs = []string{"../..", "../../cmd/mnemos"}

// TestAnswerOptionsApplyCognitiveDefaults fails if any production construction
// of query.AnswerOptions is not followed by .WithCognitiveDefaults().
//
// # WHY THIS EXISTS
//
// The cognitive behaviours (priming, salience, Hebbian strengthening,
// reconsolidation, inhibition) used to be set in exactly ONE of the five places
// AnswerOptions is built — the CLI. MCP, REST and the embedded store each
// constructed the struct with only the fields they cared about, so all five
// behaviours sat at their zero value on every surface anyone actually uses.
//
// Nothing surfaced it. A brain that silently stops strengthening associations
// returns results that look completely normal; there is no error, no empty
// response, no metric that moves. The features were implemented, tested,
// documented and ADR'd, and unreachable. The bug was not in any of them — it
// was in the four call sites that never mentioned them.
//
// A struct literal with optional fields cannot be made safe by the type system,
// so the guard is here instead: build AnswerOptions on a read path and you must
// say what happens to the cognitive fields.
//
// A literal handed to query.Engine.Recall as RecallRequest.Options is covered:
// Recall applies the defaults itself, and TestEngineRecallAppliesCognitiveDefaults
// keeps it that way.
//
// If this fires, append .WithCognitiveDefaults() to the literal. If the site is
// genuinely not a retrieval path (a test fixture, a projection), move it out of
// the scanned packages or mark the line `cognitive-defaults-ok: <reason>`.
func TestAnswerOptionsApplyCognitiveDefaults(t *testing.T) {
	const exemptMarker = "cognitive-defaults-ok"

	found := 0

	for _, dir := range searchedDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}

		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || filepath.Ext(name) != ".go" || strings.HasSuffix(name, "_test.go") {
				continue
			}

			path := filepath.Join(dir, name)
			fset := token.NewFileSet()

			file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}

			exempt := map[int]bool{}
			for _, g := range file.Comments {
				for _, c := range g.List {
					if strings.Contains(c.Text, exemptMarker) {
						exempt[fset.Position(c.Pos()).Line] = true
					}
				}
			}

			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok || !isAnswerOptions(lit.Type) {
					return true
				}
				found++
				pos := fset.Position(lit.Pos())
				if exempt[pos.Line] || litHasCognitiveCall(file, lit) || litIsRecallRequestOptions(file, lit) {
					return true
				}
				t.Errorf("%s: query.AnswerOptions built without .WithCognitiveDefaults() — this read path "+
					"gets no priming, no salience, no Hebbian strengthening, no reconsolidation and no "+
					"inhibition, silently.", pos)

				return true
			})
		}
	}

	// Guard the guard: if the detector stops recognising the literal, it passes
	// vacuously and the class it exists to catch returns unnoticed.
	if found == 0 {
		t.Fatal("no query.AnswerOptions literals found in the scanned packages — the detector no longer " +
			"matches the code it is meant to check")
	}
}

func isAnswerOptions(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)

	return ok && pkg.Name == "query" && sel.Sel.Name == "AnswerOptions"
}

// litHasCognitiveCall reports whether the literal is the receiver of a
// .WithCognitiveDefaults() call, i.e. `query.AnswerOptions{...}.WithCognitiveDefaults()`.
func litHasCognitiveCall(file *ast.File, lit *ast.CompositeLit) bool {
	hit := false

	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "WithCognitiveDefaults" {
			return true
		}
		if inner, ok := sel.X.(*ast.CompositeLit); ok && inner == lit {
			hit = true
		}

		return true
	})

	return hit
}

// litIsRecallRequestOptions reports whether lit is the Options field of a
// query.RecallRequest literal, i.e. it reaches the engine through Recall.
func litIsRecallRequestOptions(file *ast.File, lit *ast.CompositeLit) bool {
	hit := false
	ast.Inspect(file, func(n ast.Node) bool {
		outer, ok := n.(*ast.CompositeLit)
		if !ok || !isQuerySelector(outer.Type, "RecallRequest") {
			return true
		}
		for _, elt := range outer.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Options" && kv.Value == lit {
				hit = true
			}
		}
		return true
	})
	return hit
}

func isQuerySelector(e ast.Expr, name string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "query" && sel.Sel.Name == name
}

// TestEngineRecallAppliesCognitiveDefaults is what makes the RecallRequest
// exemption above sound: the one recall entry point applies the defaults.
func TestEngineRecallAppliesCognitiveDefaults(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "recall.go", nil, 0)
	if err != nil {
		t.Fatalf("parse recall.go: %v", err)
	}
	for _, d := range file.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "Recall" || fn.Recv == nil {
			continue
		}
		applies := false
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && sel.Sel.Name == "WithCognitiveDefaults" {
				applies = true
			}
			return true
		})
		if !applies {
			t.Fatal("Engine.Recall no longer calls WithCognitiveDefaults: every surface would recall without the cognitive behaviours")
		}
		return
	}
	t.Fatal("Engine.Recall not found in recall.go")
}

// TestRecallSurfacesGoThroughEngineRecall fails if production code outside
// internal/query calls the engine's answer methods directly.
//
// Every recall surface — the CLI, MCP, REST, gRPC and the library — goes
// through query.Engine.Recall, so validation, the hop bound, the cognitive
// defaults and run scoping are decided once. A surface that calls
// AnswerWithOptions directly has its own copy of those decisions, which is how
// the CLI and MCP came to disagree (YYYY-MM-DD vs RFC3339, reject vs clamp
// hops, time bounds on one surface only). The fix for a hit is to call Recall.
func TestRecallSurfacesGoThroughEngineRecall(t *testing.T) {
	bypass := map[string]bool{"AnswerWithOptions": true, "AnswerForRunWithOptions": true, "Answer": true, "AnswerForRun": true}
	root := "../.."
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir // .git, .claude worktrees, other checkouts
			}
			switch d.Name() {
			case "node_modules", "vendor", "testdata":
				return filepath.SkipDir
			}
			if filepath.Clean(path) == filepath.Clean(filepath.Join(root, "internal", "query")) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !bypass[sel.Sel.Name] {
				return true
			}
			// Only the engine's methods: they take (ctx, question, ...).
			if len(call.Args) < 2 {
				return true
			}
			t.Errorf("%s: %s bypasses query.Engine.Recall; recall surfaces must share one entry point",
				fset.Position(call.Pos()), sel.Sel.Name)
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}
