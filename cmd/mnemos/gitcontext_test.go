package main

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitInit(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available on PATH")
	}
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Test User")
	runGit(t, dir, "config", "commit.gpgsign", "false")
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func gitCommit(t *testing.T, dir, file, content, msg string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, file), content)
	runGit(t, dir, "add", file)
	runGit(t, dir, "commit", "-q", "-m", msg)
}

func TestRepoIsGit(t *testing.T) {
	notRepo := t.TempDir()
	if repoIsGit(notRepo) {
		t.Fatal("expected non-repo to return false")
	}
	repo := t.TempDir()
	gitInit(t, repo)
	if !repoIsGit(repo) {
		t.Fatal("expected initialized repo to return true")
	}
}

func TestRunGitLog_ParsesCommitsNewestFirst(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	gitCommit(t, repo, "a.txt", "alpha", "feat: add alpha")
	gitCommit(t, repo, "b.txt", "beta", "fix: handle beta edge case")
	gitCommit(t, repo, "c.txt", "gamma", "docs: explain gamma")

	commits, err := runGitLog(context.Background(), repo, 10, "")
	if err != nil {
		t.Fatalf("runGitLog: %v", err)
	}
	if len(commits) != 3 {
		t.Fatalf("got %d commits, want 3", len(commits))
	}
	if commits[0].Subject != "docs: explain gamma" {
		t.Fatalf("newest subject = %q, want 'docs: explain gamma'", commits[0].Subject)
	}
	if commits[2].Subject != "feat: add alpha" {
		t.Fatalf("oldest subject = %q, want 'feat: add alpha'", commits[2].Subject)
	}
	if commits[0].AuthorEmail != "test@example.com" {
		t.Fatalf("author email = %q, want test@example.com", commits[0].AuthorEmail)
	}
	if len(commits[0].SHA) != 40 {
		t.Fatalf("SHA length = %d, want 40", len(commits[0].SHA))
	}
}

func TestRunGitLog_RespectsLimit(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	for i := 0; i < 5; i++ {
		gitCommit(t, repo, "f.txt", strings.Repeat("x", i+1), "commit "+string(rune('A'+i)))
	}

	commits, err := runGitLog(context.Background(), repo, 2, "")
	if err != nil {
		t.Fatalf("runGitLog: %v", err)
	}
	if len(commits) != 2 {
		t.Fatalf("got %d, want 2", len(commits))
	}
}

func TestRunGitLog_EmptyRepoReturnsNothing(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	commits, err := runGitLog(context.Background(), repo, 10, "")
	if err != nil {
		t.Fatalf("runGitLog: %v", err)
	}
	if len(commits) != 0 {
		t.Fatalf("got %d commits in empty repo, want 0", len(commits))
	}
}

func TestRunGitLog_NotARepoFails(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available on PATH")
	}
	notRepo := t.TempDir()
	commits, err := runGitLog(context.Background(), notRepo, 10, "")
	// Either error or empty result is acceptable — git's behavior varies.
	if err == nil && len(commits) != 0 {
		t.Fatalf("expected empty/error for non-repo, got %d commits", len(commits))
	}
}

func TestIngestGitLog_PersistsCommitsAsEvents(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	gitCommit(t, repo, "a.txt", "alpha", "feat: introduce alpha module")
	gitCommit(t, repo, "b.txt", "beta", "fix: resolve beta race condition")

	db, conn := openTestStore(t)

	ctx := context.Background()
	ingested, skipped, err := ingestGitLog(ctx, wrapTestWriter(t, conn), repo, 10, "", "")
	if err != nil {
		t.Fatalf("ingestGitLog: %v", err)
	}
	if ingested != 2 || skipped != 0 {
		t.Fatalf("first run ingested=%d skipped=%d, want 2/0", ingested, skipped)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM events WHERE json_extract(metadata_json, '$.source') = 'git'`).Scan(&n); err != nil {
		t.Fatalf("count git events: %v", err)
	}
	if n != 2 {
		t.Fatalf("git events in DB = %d, want 2", n)
	}

	// Second run is fully deduped by SHA.
	ingested2, skipped2, err := ingestGitLog(ctx, wrapTestWriter(t, conn), repo, 10, "", "")
	if err != nil {
		t.Fatalf("ingestGitLog second: %v", err)
	}
	if ingested2 != 0 || skipped2 != 2 {
		t.Fatalf("second run ingested=%d skipped=%d, want 0/2", ingested2, skipped2)
	}
}

func TestIngestGitLog_NewCommitsOnlyOnRerun(t *testing.T) {
	repo := t.TempDir()
	gitInit(t, repo)
	gitCommit(t, repo, "a.txt", "alpha", "feat: add alpha")

	_, conn := openTestStore(t)

	ctx := context.Background()
	if _, _, err := ingestGitLog(ctx, wrapTestWriter(t, conn), repo, 10, "", ""); err != nil {
		t.Fatalf("first ingest: %v", err)
	}

	gitCommit(t, repo, "b.txt", "beta", "feat: add beta")

	ingested, skipped, err := ingestGitLog(ctx, wrapTestWriter(t, conn), repo, 10, "", "")
	if err != nil {
		t.Fatalf("second ingest: %v", err)
	}
	if ingested != 1 || skipped != 1 {
		t.Fatalf("second run ingested=%d skipped=%d, want 1/1", ingested, skipped)
	}
}

func TestExistingGitCommitSHAs_LoadsFromMetadata(t *testing.T) {
	db, _ := openTestStore(t)

	ctx := context.Background()
	if _, err := db.ExecContext(ctx,
		`INSERT INTO events (id, run_id, schema_version, content, source_input_id, timestamp, metadata_json, ingested_at) VALUES
		 ('e1', 'r', 'v', 'c', 'i1', '2026-04-17T00:00:00Z', '{"git_commit_sha":"abc123"}', '2026-04-17T00:00:00Z'),
		 ('e2', 'r', 'v', 'c', 'i2', '2026-04-17T00:00:00Z', '{"git_commit_sha":"def456"}', '2026-04-17T00:00:00Z'),
		 ('e3', 'r', 'v', 'c', 'i3', '2026-04-17T00:00:00Z', '{"source":"raw_text"}',       '2026-04-17T00:00:00Z')`,
	); err != nil {
		t.Fatalf("insert: %v", err)
	}

	got, err := existingGitCommitSHAs(ctx, db)
	if err != nil {
		t.Fatalf("existingGitCommitSHAs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d SHAs, want 2 (%v)", len(got), got)
	}
	if _, ok := got["abc123"]; !ok {
		t.Errorf("missing abc123")
	}
	if _, ok := got["def456"]; !ok {
		t.Errorf("missing def456")
	}
}
