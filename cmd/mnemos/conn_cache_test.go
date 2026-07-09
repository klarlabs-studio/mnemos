package main

import (
	"context"
	"testing"
)

// TestConnCache verifies the read-connection cache: same DSN reuses one conn,
// closeConn is a no-op on cached conns, and closeConnCache resets state. Uses
// the in-process memory backend so no external DB is needed.
func TestConnCache(t *testing.T) {
	t.Setenv("MNEMOS_DB_URL", "memory://conncache")
	connCacheEnabled = false
	closeConnCache()
	t.Cleanup(func() {
		closeConnCache()
		connCacheEnabled = false
	})
	ctx := context.Background()

	enableConnCache()

	a, err := openConn(ctx)
	if err != nil {
		t.Fatalf("openConn: %v", err)
	}
	b, err := openConn(ctx)
	if err != nil {
		t.Fatalf("openConn: %v", err)
	}
	if a != b {
		t.Fatal("same DSN should return the same cached conn")
	}

	// closeConn on a cached conn must be a no-op — the conn stays usable.
	closeConn(a)
	if _, err := a.Events.ListAll(ctx); err != nil {
		t.Fatalf("cached conn should stay open after closeConn: %v", err)
	}

	// After closeConnCache, a subsequent open builds a fresh conn.
	closeConnCache()
	c, err := openConn(ctx)
	if err != nil {
		t.Fatalf("openConn after reset: %v", err)
	}
	if c == a {
		t.Error("closeConnCache should have dropped the old conn")
	}
}

// TestConnCacheDisabledIsPerCall verifies the CLI path (cache off) opens a
// fresh conn each call — i.e. caching is opt-in and does not change CLI
// behavior.
func TestConnCacheDisabledIsPerCall(t *testing.T) {
	t.Setenv("MNEMOS_DB_URL", "memory://nocache")
	connCacheEnabled = false
	closeConnCache()
	ctx := context.Background()

	a, err := openConn(ctx)
	if err != nil {
		t.Fatalf("openConn: %v", err)
	}
	b, err := openConn(ctx)
	if err != nil {
		t.Fatalf("openConn: %v", err)
	}
	if a == b {
		t.Error("with the cache disabled, each openConn should be a distinct conn")
	}
	closeConn(a)
	closeConn(b)
}
