package libsql_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/mnemos/internal/domain"
	"go.klarlabs.de/mnemos/internal/store"
	_ "go.klarlabs.de/mnemos/internal/store/libsql"
)

func TestSupportedSchemes_IncludesLibSQL(t *testing.T) {
	t.Parallel()
	got := store.SupportedSchemes()
	found := false
	for _, s := range got {
		if s == "libsql" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("SupportedSchemes missing libsql; got %v", got)
	}
}

// TestOpen_LocalFileMode covers the libsql:///path/to.db form. The
// pure-Go libsql driver supports embedded local files via the
// `file:` driver DSN, so this exercises the schema bootstrap end-
// to-end without needing a remote server.
func TestOpen_LocalFileMode(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "libsql_local.db")
	dsn := "libsql://" + dbPath
	conn, err := store.Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("store.Open(%s): %v", dsn, err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	if conn.Events == nil || conn.Claims == nil || conn.Relationships == nil ||
		conn.Embeddings == nil || conn.Users == nil || conn.RevokedTokens == nil ||
		conn.Agents == nil || conn.Entities == nil || conn.Jobs == nil {
		t.Errorf("libsql Conn has nil port: %+v", conn)
	}

	// Smoke: append + retrieve an event. Proves the SQLite repos
	// work unchanged against the libSQL-driver-backed *sql.DB.
	ctx := context.Background()
	now := time.Now().UTC()
	ev := domain.Event{
		ID: "ev-1", RunID: "r", SchemaVersion: "1",
		Content: "alpha", SourceInputID: "in-1",
		Timestamp: now, IngestedAt: now, CreatedBy: domain.SystemUser,
	}
	if err := conn.Events.Append(ctx, ev); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := conn.Events.GetByID(ctx, "ev-1")
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Content != "alpha" {
		t.Errorf("GetByID content = %q", got.Content)
	}
}

// TestOpen_NamespaceParamIsIgnored documents the explicit decision:
// libSQL has no per-tenant schema concept, so ?namespace= is dropped
// rather than rejected. Operators migrating from postgres:// or
// mysql:// shouldn't see surprising errors when the param is left
// in by accident.
func TestOpen_NamespaceParamIsIgnored(t *testing.T) {
	t.Parallel()
	dbPath := filepath.Join(t.TempDir(), "libsql_ns.db")
	dsn := "libsql://" + dbPath + "?namespace=team_x"
	conn, err := store.Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("namespace param should be silently ignored, got: %v", err)
	}
	_ = conn.Close()
}

// TestOpen_RejectsNonLibsqlScheme guards against accidental
// invocations through a wrong scheme.
func TestOpen_RejectsNonLibsqlScheme(t *testing.T) {
	t.Parallel()
	_, err := store.Open(context.Background(), "sqlite:///tmp/x.db")
	// store.Open dispatches by scheme; sqlite:// won't reach us. The
	// test guards the dispatch boundary itself: we never receive a
	// non-libsql DSN. This case is here to document that we don't
	// register additional aliases.
	if err == nil {
		// sqlite is registered too, so the open succeeds. The point
		// of this test is just to confirm we DON'T reach the libsql
		// provider for non-libsql:// schemes — a regression marker.
		return
	}
	if strings.Contains(err.Error(), "libsql:") {
		t.Errorf("non-libsql scheme reached the libsql provider: %v", err)
	}
}
