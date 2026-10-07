package docs

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// docs/README.md classifies every document as a decision, generated
// reference, guide or working note. It fails when a guide, ADR or note is not
// linked from it, or when reference/ holds a file that is not generated, so
// the separation cannot erode one unindexed file at a time.
func TestDocsIndex(t *testing.T) {
	root := filepath.Join("..", "..", "docs")
	index, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	linked := string(index)
	checked := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if rel == "README.md" {
			return nil
		}
		checked++
		if strings.HasPrefix(rel, "reference/") {
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if !strings.Contains(strings.SplitN(string(body), "\n", 2)[0], "Code generated") {
				t.Errorf("docs/%s is in reference/ but not generated: reference/ holds generated files only", rel)
			}
		}
		if !strings.Contains(linked, "("+rel+")") {
			t.Errorf("docs/%s is not linked from docs/README.md: classify it as a guide, decision or working note", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 30 {
		t.Fatalf("only %d documents checked; the walk is broken", checked)
	}
}
