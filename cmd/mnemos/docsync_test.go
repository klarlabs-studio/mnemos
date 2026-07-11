package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureRepoGitignore(t *testing.T) {
	dir := t.TempDir()
	ensureRepoGitignore(dir)
	first, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		t.Fatalf("gitignore not written: %v", err)
	}
	for _, want := range []string{".mnemos/mnemos.db", ".mnemos/.*.sha"} {
		if !strings.Contains(string(first), want) {
			t.Errorf("gitignore missing %q:\n%s", want, first)
		}
	}
	// Idempotent: a second call changes nothing.
	ensureRepoGitignore(dir)
	second, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if string(second) != string(first) {
		t.Errorf("not idempotent:\nfirst:\n%s\nsecond:\n%s", first, second)
	}

	// Preserves existing entries.
	dir2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir2, ".gitignore"), []byte("node_modules/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ensureRepoGitignore(dir2)
	got, _ := os.ReadFile(filepath.Join(dir2, ".gitignore"))
	if !strings.Contains(string(got), "node_modules/") || !strings.Contains(string(got), ".mnemos/mnemos.db") {
		t.Errorf("should preserve existing and add mnemos patterns:\n%s", got)
	}
}

func TestUpsertManagedBlock_InsertsIntoEmpty(t *testing.T) {
	got := upsertManagedBlock("", "hello")
	if !strings.Contains(got, docBeginMarker) || !strings.Contains(got, docEndMarker) || !strings.Contains(got, "hello") {
		t.Fatalf("empty upsert wrong:\n%s", got)
	}
}

func TestUpsertManagedBlock_PreservesHumanContent(t *testing.T) {
	human := "# My Project\n\nHand-written guidance the human owns.\n"
	got := upsertManagedBlock(human, "generated learnings")
	if !strings.Contains(got, "Hand-written guidance the human owns.") {
		t.Errorf("human content lost:\n%s", got)
	}
	if !strings.Contains(got, "generated learnings") {
		t.Errorf("block not inserted:\n%s", got)
	}
	if !strings.HasPrefix(got, "# My Project") {
		t.Errorf("human content should stay at the top:\n%s", got)
	}
}

func TestExtractManagedBlock(t *testing.T) {
	content := "# Title\n\nhuman\n\n" + docBeginMarker + "\ninner learnings\n" + docEndMarker + "\n\nafter\n"
	inner, ok := extractManagedBlock(content)
	if !ok {
		t.Fatal("expected a block")
	}
	if inner != "inner learnings" {
		t.Errorf("inner = %q, want %q", inner, "inner learnings")
	}
	if _, ok := extractManagedBlock("no markers here"); ok {
		t.Error("expected no block")
	}
}

func TestBlockBulletsText_StripsAnnotationsAndHeaders(t *testing.T) {
	inner := strings.Join([]string{
		"## Repo learnings (mnemos)",
		"",
		"### Decisions",
		"- We use PostgreSQL for payments",
		"### Conventions & facts",
		"- The event backbone is Kafka (trust 0.80)",
		"- Contested thing ⚠ contested",
		"- We use Terraform for infra", // a human-added bullet, no annotation
	}, "\n")
	got := blockBulletsText(inner)
	for _, want := range []string{"We use PostgreSQL for payments", "The event backbone is Kafka", "We use Terraform for infra", "Contested thing"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "trust 0.80") || strings.Contains(got, "⚠") || strings.Contains(got, "###") || strings.Contains(got, "Repo learnings") {
		t.Errorf("annotations/headers not stripped:\n%s", got)
	}
}

func TestBlockHash_StableAndSensitive(t *testing.T) {
	if blockHash("a\n") != blockHash(" a ") {
		t.Error("hash should ignore surrounding whitespace")
	}
	if blockHash("a") == blockHash("b") {
		t.Error("different content should hash differently")
	}
}

func TestUpsertManagedBlock_ReplacesInPlaceAndIsIdempotent(t *testing.T) {
	human := "# Title\n\nbefore\n\n" + docBeginMarker + "\nOLD\n" + docEndMarker + "\n\nafter\n"
	got := upsertManagedBlock(human, "NEW")
	if strings.Contains(got, "OLD") {
		t.Errorf("old block not replaced:\n%s", got)
	}
	if !strings.Contains(got, "NEW") || !strings.Contains(got, "before") || !strings.Contains(got, "after") {
		t.Errorf("surrounding content or new block wrong:\n%s", got)
	}
	// Idempotent: re-applying the same block yields identical output.
	if again := upsertManagedBlock(got, "NEW"); again != got {
		t.Errorf("not idempotent:\nfirst:\n%s\nsecond:\n%s", got, again)
	}
	// Exactly one managed block.
	if strings.Count(got, docBeginMarker) != 1 || strings.Count(got, docEndMarker) != 1 {
		t.Errorf("expected exactly one managed block:\n%s", got)
	}
}
