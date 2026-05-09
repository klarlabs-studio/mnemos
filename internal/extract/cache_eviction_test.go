package extract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEvictCacheIfOverCap_RemovesOldestUntilUnderCap(t *testing.T) {
	dir := t.TempDir()

	// Seed five files of 100 bytes each. Older files get an older
	// mtime via os.Chtimes so the oldest-first sort is deterministic.
	now := time.Now()
	for i := 0; i < 5; i++ {
		name := filepath.Join(dir, "f"+string(rune('0'+i))+".json")
		if err := os.WriteFile(name, []byte(strings.Repeat("x", 100)), 0o600); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
		mt := now.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(name, mt, mt); err != nil {
			t.Fatalf("chtimes %d: %v", i, err)
		}
	}

	// Cap = 250 bytes → must keep at most 2 files (oldest 3 evicted).
	evictCacheIfOverCap(dir, 250)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) > 2 {
		t.Errorf("after eviction: %d files, want ≤ 2", len(entries))
	}
	// Oldest files (f0, f1, f2) should be gone; newest (f4) must
	// remain.
	for _, name := range []string{"f0.json", "f1.json", "f2.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Errorf("oldest file %s should have been evicted", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "f4.json")); err != nil {
		t.Errorf("newest file f4.json should have been kept: %v", err)
	}
}

func TestEvictCacheIfOverCap_NoopUnderCap(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		name := filepath.Join(dir, "f"+string(rune('0'+i))+".json")
		if err := os.WriteFile(name, []byte("x"), 0o600); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
	evictCacheIfOverCap(dir, 1<<20) // 1 MiB cap, total ~3 bytes

	entries, _ := os.ReadDir(dir)
	if len(entries) != 3 {
		t.Errorf("under-cap directory unexpectedly modified: %d files, want 3", len(entries))
	}
}

func TestEvictCacheIfOverCap_DisabledWhenCapZero(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		name := filepath.Join(dir, "f"+string(rune('0'+i))+".json")
		if err := os.WriteFile(name, []byte(strings.Repeat("x", 1024)), 0o600); err != nil {
			t.Fatalf("seed %d: %v", i, err)
		}
	}
	evictCacheIfOverCap(dir, 0) // disabled
	entries, _ := os.ReadDir(dir)
	if len(entries) != 3 {
		t.Errorf("cap=0 should disable eviction: %d files left", len(entries))
	}
}

func TestLLMCacheMaxBytes_EnvOverride(t *testing.T) {
	t.Setenv("MNEMOS_LLM_CACHE_MAX_BYTES", "12345")
	if got := llmCacheMaxBytes(); got != 12345 {
		t.Errorf("env override: got %d, want 12345", got)
	}

	t.Setenv("MNEMOS_LLM_CACHE_MAX_BYTES", "")
	if got := llmCacheMaxBytes(); got != 1<<30 {
		t.Errorf("default: got %d, want %d", got, 1<<30)
	}

	t.Setenv("MNEMOS_LLM_CACHE_MAX_BYTES", "garbage")
	if got := llmCacheMaxBytes(); got != 1<<30 {
		t.Errorf("garbage falls back to default: got %d", got)
	}
}
