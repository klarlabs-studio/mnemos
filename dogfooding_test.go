package mnemos_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.klarlabs.de/mnemos"

	_ "go.klarlabs.de/mnemos/internal/store/memory"
	_ "go.klarlabs.de/mnemos/internal/store/sqlite"
)

// TestRememberClaimWithEvidence covers the convenience wrapper: it records
// an observation event per evidence string and links a fact claim to them.
func TestRememberClaimWithEvidence(t *testing.T) {
	t.Parallel()
	clearMnemosEnv(t)

	mem, err := mnemos.New(
		mnemos.WithStorage("memory://?namespace=rcwe_test"),
		mnemos.WithPassiveMode(),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = mem.Close() }()
	ctx := context.Background()

	id, err := mem.RememberClaimWithEvidence(ctx,
		"the checkout latency regression came from commit abc123",
		[]string{"github:commit:abc123", "prometheus:p99"},
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("RememberClaimWithEvidence: %v", err)
	}
	if id == "" {
		t.Fatal("expected a non-empty claim id")
	}

	// Fail-closed: no evidence and blank text are rejected, nothing persisted.
	if _, err := mem.RememberClaimWithEvidence(ctx, "claim without evidence", nil, time.Time{}); err == nil {
		t.Fatal("expected error for evidence-less claim")
	}
	if _, err := mem.RememberClaimWithEvidence(ctx, "  ", []string{"x"}, time.Time{}); err == nil {
		t.Fatal("expected error for blank text")
	}
	if _, err := mem.RememberClaimWithEvidence(ctx, "blank evidence only", []string{"", "  "}, time.Time{}); err == nil {
		t.Fatal("expected error when all evidence is blank")
	}
}

// TestInfo reports backend / namespace / mode / embedder for operators.
func TestInfo(t *testing.T) {
	t.Parallel()
	clearMnemosEnv(t)

	mem, err := mnemos.New(
		mnemos.WithStorage("memory://?namespace=info_test"),
		mnemos.WithPassiveMode(),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = mem.Close() }()

	got := mem.Info()
	if got.Backend != "memory" {
		t.Errorf("Backend = %q, want memory", got.Backend)
	}
	if got.Namespace != "info_test" {
		t.Errorf("Namespace = %q, want info_test", got.Namespace)
	}
	if got.Mode != "passive" {
		t.Errorf("Mode = %q, want passive", got.Mode)
	}
	if got.Embedder != "none" {
		t.Errorf("Embedder = %q, want none (passive/lexical)", got.Embedder)
	}
}

// TestWithSQLite_ResolvesRelativePath verifies the relative-path fix: a
// relative path is resolved against the working directory, not turned into
// an absolute "/path" by URL host parsing.
func TestWithSQLite_ResolvesRelativePath(t *testing.T) {
	clearMnemosEnv(t)

	dir := t.TempDir()
	t.Chdir(dir) // run this test with cwd = dir

	mem, err := mnemos.New(
		mnemos.WithSQLite("rel_mnemos.db"),
		mnemos.WithPassiveMode(),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = mem.Close() }()

	if got := mem.Info().Backend; got != "sqlite" {
		t.Errorf("Backend = %q, want sqlite", got)
	}
	// The DB must be created under the working directory (dir), not at the
	// filesystem root — the bug turned "rel_mnemos.db" into "/rel_mnemos.db".
	if _, err := os.Stat(filepath.Join(dir, "rel_mnemos.db")); err != nil {
		t.Fatalf("expected sqlite db at %s: %v", filepath.Join(dir, "rel_mnemos.db"), err)
	}
	if _, err := os.Stat("/rel_mnemos.db"); err == nil {
		t.Fatal("relative path leaked to filesystem root (/rel_mnemos.db)")
	}
}
