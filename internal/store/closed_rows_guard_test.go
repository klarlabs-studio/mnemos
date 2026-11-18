package store_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// ADR 0036 §8: closed evidence links and closed associations must never leak
// into live behaviour. They are kept out of the live tables, so every existing
// reader of claim_evidence and relationships is unaffected by construction;
// what could still leak them is new code reading the closed tables. This guard
// fails when production code outside the allowed places names a closed table
// in a string literal, or calls a closed lister outside internal/recorded.
//
// Both checks read the Go AST (string literals and call selectors), never
// regex over source text, so a comment mentioning a table is not a hit and a
// computed query cannot hide in a construct the guard skips.
func TestClosedRows_OnlyRecordedReadsSeeThem(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	tables := []string{"closed_claim_evidence", "closed_relationships"}
	// Files that may name a closed table: the stores that write, list and
	// destroy closed rows, and SQLite's embedded schema.
	tableAllowed := map[string]bool{}
	for _, backend := range []string{"sqlite", "postgres", "mysql"} {
		for _, f := range []string{"closed.go", "claim_repository.go", "relationship_repository.go", "event_repository.go"} {
			tableAllowed[filepath.Join("internal", "store", backend, f)] = true
		}
	}
	tableAllowed[filepath.Join("internal", "store", "sqlite", "db.go")] = true

	listers := map[string]bool{
		"ListClosedEvidenceByClaimIDs": true, "ListClosedEvidenceByEventIDs": true,
		"ListClosedByClaimIDs": true, "ListAllClosed": true,
	}
	callerAllowed := func(rel string) bool {
		return strings.HasPrefix(rel, filepath.Join("internal", "recorded")+string(filepath.Separator))
	}

	var leaks []string
	fset := token.NewFileSet()
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (strings.HasPrefix(name, ".") || name == "vendor" || name == "testdata" || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.BasicLit:
				if n.Kind != token.STRING || tableAllowed[rel] {
					return true
				}
				s, err := strconv.Unquote(n.Value)
				if err != nil {
					s = n.Value
				}
				for _, table := range tables {
					if strings.Contains(s, table) {
						leaks = append(leaks, rel+": names "+table)
					}
				}
			case *ast.CallExpr:
				if sel, ok := n.Fun.(*ast.SelectorExpr); ok && listers[sel.Sel.Name] && !callerAllowed(rel) {
					leaks = append(leaks, rel+": calls "+sel.Sel.Name)
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	sort.Strings(leaks)
	for _, l := range leaks {
		t.Errorf("closed rows read outside internal/recorded (ADR 0036 §8): %s", l)
	}
}
