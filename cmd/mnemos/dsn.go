package main

import (
	"context"
	"log"
	"os"

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
	return store.Open(ctx, resolveDSN())
}

// closeConn closes a *store.Conn, logging any error.
// Use as: defer closeConn(conn)
func closeConn(conn *store.Conn) {
	if err := conn.Close(); err != nil {
		log.Printf("close store conn: %v", err)
	}
}
