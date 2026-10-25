package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/mnemos/internal/store"
)

const projectedBlock = `## Learnings
- Billing retries failed charges three times (trust 0.91)
- The search index rebuilds nightly (trust 0.84)`

// #391: the managed block is a projection of the brain's own beliefs. When a
// human edits it, only what the human added may flow back; re-ingesting the
// projected lines gave each of them fresh corroboration from a new origin on
// every edit, so the brain confirmed itself.
func TestAddedBullets_OnlyWhatMnemosDidNotWrite(t *testing.T) {
	edited := projectedBlock + "\n- We pin Postgres to 16 for the billing database"
	got := addedBullets(edited, projectedBlock)
	if got != "We pin Postgres to 16 for the billing database" {
		t.Fatalf("addedBullets = %q, want only the human's line", got)
	}
	// Annotation and case changes are not additions.
	reannotated := strings.ReplaceAll(projectedBlock, "(trust 0.91)", "(trust 0.95)")
	reannotated = strings.ReplaceAll(reannotated, "Billing", "billing")
	if got := addedBullets(reannotated, projectedBlock); got != "" {
		t.Fatalf("a re-annotated projection counted as an addition: %q", got)
	}
}

func TestSyncBackFromDocs_IngestsOnlyTheHumanAddition(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".mnemos"), 0o755); err != nil {
		t.Fatal(err)
	}
	dsn := "sqlite://" + filepath.Join(t.TempDir(), "repo.db")
	writeDocBaseline(repo, "AGENTS.md", projectedBlock)

	edited := upsertManagedBlock("# Agents\n", projectedBlock+"\n- We pin Postgres to 16 for the billing database")
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	syncBackFromDocs(context.Background(), repo, "AGENTS.md", dsn, false)

	conn, err := store.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	claims, err := conn.Claims.ListAll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, c := range claims {
		texts = append(texts, c.Text)
		if strings.Contains(c.Text, "retries failed charges") || strings.Contains(c.Text, "search index") {
			t.Errorf("sync-back re-ingested a projected belief: %q", c.Text)
		}
	}
	if !strings.Contains(strings.Join(texts, "\n"), "Postgres") {
		t.Errorf("the human's addition was not ingested; claims = %q", texts)
	}
}

// A checkout that predates the stored block cannot tell its own lines from a
// human's, so it adopts the current block as the baseline and ingests nothing,
// as a fresh clone already does.
func TestSyncBackFromDocs_NoStoredBlockAdoptsWithoutIngesting(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, ".mnemos"), 0o755); err != nil {
		t.Fatal(err)
	}
	dsn := "sqlite://" + filepath.Join(t.TempDir(), "repo.db")
	writeStoredDocHash(repo, "AGENTS.md", blockHash(projectedBlock)) // hash only: an older binary's state
	edited := upsertManagedBlock("# Agents\n", projectedBlock+"\n- We pin Postgres to 16 for the billing database")
	if err := os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	syncBackFromDocs(context.Background(), repo, "AGENTS.md", dsn, false)

	conn, err := store.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if n, err := conn.Claims.CountAll(context.Background()); err != nil || n != 0 {
		t.Fatalf("ingested %d claims without a stored block (%v); want 0", n, err)
	}
	inner, _ := extractManagedBlock(edited)
	if got := readDocBaselineBlock(repo, "AGENTS.md"); got != strings.TrimSpace(inner) {
		t.Fatalf("baseline block not adopted: %q", got)
	}
}
