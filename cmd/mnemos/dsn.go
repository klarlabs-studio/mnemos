package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/store"
)

// resolveDSN returns the canonical store DSN for this process. It is
// the single point that translates the environment + on-disk
// conventions into a value the store registry can dispatch on.
//
// Precedence:
//
//  1. MNEMOS_DB_URL — explicit, takes any registered scheme
//     (sqlite://, sqlite3://, memory://, postgres://, mysql://,
//     libsql://). Used as-is without any path interpretation.
//  2. Otherwise: "sqlite://" + resolveDBPath(), which walks the
//     working directory looking for .mnemos/mnemos.db, then falls
//     back to the XDG global default.
//
// Operators who want a non-SQLite backend set MNEMOS_DB_URL
// explicitly.
func resolveDSN() string {
	if u := os.Getenv("MNEMOS_DB_URL"); u != "" {
		return u
	}
	return "sqlite://" + resolveDBPath()
}

// openConn opens a store connection through the registry using the
// resolved DSN. Callers should use this instead of opening a raw
// *sql.DB directly:
//
//	conn, err := openConn(ctx)
//
// Repositories are available via the port-typed fields on Conn.
// Provider-specific access (e.g. the *sql.DB needed for the deep
// health probe) is available through Conn.Raw when required.
func openConn(ctx context.Context) (*store.Conn, error) {
	dsn, err := resolveDSNForContext(ctx)
	if err != nil {
		return nil, err
	}
	return store.Open(ctx, dsn)
}

// resolveDSNForContext returns the DSN for this request, scoped to the
// effective tenant when the request carries one (multi-tenant HTTP MCP mode).
// It appends `?tenant=<id>` so the Postgres provider pins the mnemos.tenant GUC
// and RLS isolates the request (ADR 0007). Tenant scoping requires a Postgres
// backend; anything else fails closed rather than silently sharing data.
func resolveDSNForContext(ctx context.Context) (string, error) {
	dsn := resolveDSN()
	tenant, ok := tenantFromContext(ctx)
	if !ok {
		return dsn, nil
	}
	if !isPostgresDSN(dsn) {
		return "", fmt.Errorf("tenant scoping requires a postgres backend (this store is not postgres)")
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "tenant=" + tenant, nil
}

// isPostgresDSN reports whether a DSN targets the Postgres backend.
func isPostgresDSN(dsn string) bool {
	return strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://")
}

// closeConn closes a *store.Conn, logging any error.
// Use as: defer closeConn(conn)
func closeConn(conn *store.Conn) {
	if err := conn.Close(); err != nil {
		log.Printf("close store conn: %v", err)
	}
}

// openWriter opens a governed daemon-writer over a fresh store
// connection (which it owns). CLI commands that mutate durable state
// use this instead of openConn so every write routes through the axi
// kernel — the spec non-negotiable that no delivery adapter reaches a
// repository directly. Reads are still available via w.Conn().
//
//	w, err := openWriter(ctx)
//	defer closeWriter(w)
func openWriter(ctx context.Context) (*govwrite.Writer, error) {
	dsn, err := resolveDSNForContext(ctx)
	if err != nil {
		return nil, err
	}
	return govwrite.New(ctx, dsn, nil)
}

// closeWriter closes a *govwrite.Writer (and the store connection it
// owns), logging any error. Use as: defer closeWriter(w)
func closeWriter(w *govwrite.Writer) {
	if err := w.Close(); err != nil {
		log.Printf("close governed writer: %v", err)
	}
}
