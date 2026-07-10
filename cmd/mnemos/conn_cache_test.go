package main

import (
	"context"
	"sync"
	"testing"

	"go.klarlabs.de/mnemos/internal/store"
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

// TestConnCacheConcurrent hammers openConn from many goroutines on the same
// DSN; with the race detector this asserts the double-checked open is
// race-free and converges on a single cached conn.
func TestConnCacheConcurrent(t *testing.T) {
	t.Setenv("MNEMOS_DB_URL", "memory://concurrent")
	connCacheEnabled = false
	closeConnCache()
	t.Cleanup(func() { closeConnCache(); connCacheEnabled = false })
	enableConnCache()

	ctx := context.Background()
	var wg sync.WaitGroup
	conns := make([]*store.Conn, 20)
	for i := range conns {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c, err := openConn(ctx)
			if err != nil {
				t.Errorf("openConn: %v", err)
				return
			}
			conns[i] = c
		}(i)
	}
	wg.Wait()
	// All goroutines must observe the same cached conn.
	for i := 1; i < len(conns); i++ {
		if conns[i] != conns[0] {
			t.Fatalf("conn %d differs from conn 0 — cache did not converge", i)
		}
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
