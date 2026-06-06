package store_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/mnemos/internal/store"

	// Register every provider for the smoke tests.
	// Real binaries do the same blank imports in cmd/mnemos.
	_ "go.klarlabs.de/mnemos/internal/store/memory"
	_ "go.klarlabs.de/mnemos/internal/store/postgres"
	_ "go.klarlabs.de/mnemos/internal/store/sqlite"
)

// TestOpen_SQLite_PopulatesAllRepositories is the Phase 1a smoke
// test: opening a sqlite:// DSN through the registry must yield a
// Conn with every port-typed repository populated and a working
// Closer. We don't exercise the repositories here — that's covered
// by the existing per-repo tests in internal/store/sqlite/. This
// test only verifies the wiring.
func TestOpen_SQLite_PopulatesAllRepositories(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dsn := "sqlite://" + filepath.Join(dir, "smoke.db")

	conn, err := store.Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("store.Open(%q): %v", dsn, err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Errorf("conn.Close: %v", err)
		}
	})

	if conn.Events == nil {
		t.Error("Conn.Events is nil")
	}
	if conn.Claims == nil {
		t.Error("Conn.Claims is nil")
	}
	if conn.Relationships == nil {
		t.Error("Conn.Relationships is nil")
	}
	if conn.Embeddings == nil {
		t.Error("Conn.Embeddings is nil")
	}
	if conn.Users == nil {
		t.Error("Conn.Users is nil")
	}
	if conn.RevokedTokens == nil {
		t.Error("Conn.RevokedTokens is nil")
	}
	if conn.Agents == nil {
		t.Error("Conn.Agents is nil")
	}
	if conn.Entities == nil {
		t.Error("Conn.Entities is nil")
	}
	if conn.Jobs == nil {
		t.Error("Conn.Jobs is nil")
	}
	if conn.Raw == nil {
		t.Error("Conn.Raw is nil; SQLite provider must still expose *sql.DB while raw-SQL call sites remain")
	}
}

func TestOpen_RejectsUnknownScheme(t *testing.T) {
	t.Parallel()
	_, err := store.Open(context.Background(), "wat://nope")
	if err == nil {
		t.Fatal("expected error for unknown scheme, got nil")
	}
	if !strings.Contains(err.Error(), "unknown provider") {
		t.Errorf("error message should mention unknown provider, got: %v", err)
	}
}

func TestOpen_RejectsMissingScheme(t *testing.T) {
	t.Parallel()
	_, err := store.Open(context.Background(), "/just/a/path.db")
	if err == nil {
		t.Fatal("expected error for missing scheme, got nil")
	}
	if !strings.Contains(err.Error(), "missing scheme") {
		t.Errorf("error message should mention missing scheme, got: %v", err)
	}
}

func TestSupportedSchemes_IncludesRegisteredProviders(t *testing.T) {
	t.Parallel()
	got := store.SupportedSchemes()
	want := map[string]bool{"sqlite": false, "sqlite3": false, "memory": false, "postgres": false, "postgresql": false}
	for _, s := range got {
		if _, ok := want[s]; ok {
			want[s] = true
		}
	}
	for s, seen := range want {
		if !seen {
			t.Errorf("SupportedSchemes missing %q (got %v)", s, got)
		}
	}
}

func TestOpen_MemoryProviderReturnsPopulatedConn(t *testing.T) {
	t.Parallel()
	conn, err := store.Open(context.Background(), "memory://")
	if err != nil {
		t.Fatalf("store.Open(memory://): %v", err)
	}
	t.Cleanup(func() {
		if err := conn.Close(); err != nil {
			t.Errorf("conn.Close: %v", err)
		}
	})

	if conn.Events == nil || conn.Claims == nil || conn.Relationships == nil ||
		conn.Embeddings == nil || conn.Users == nil || conn.RevokedTokens == nil ||
		conn.Agents == nil || conn.Entities == nil || conn.Jobs == nil {
		t.Errorf("memory Conn has nil port: %+v", conn)
	}
}
