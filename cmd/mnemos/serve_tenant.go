package main

import (
	"context"
	"net/http"
	"sync"

	"go.klarlabs.de/mnemos"
	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/store"
)

// Per-request tenant scoping for the `serve` REST + gRPC surfaces
// (`serve --require-tenant`, ADR 0007). The handlers capture the process's
// shared single-tenant conn/writer/mem at construction; in multi-tenant mode a
// middleware opens a tenant-scoped connection per request and stashes it, and
// every handler resolves its dependencies through scopedConn/scopedWriter/
// scopedMem — which return the request-scoped values when present and the
// captured fallback otherwise (so single-tenant mode is unchanged).

type reqConnKey struct{}
type reqWriterKey struct{}

func withReqConn(ctx context.Context, c *store.Conn) context.Context {
	return context.WithValue(ctx, reqConnKey{}, c)
}
func withReqWriter(ctx context.Context, w *govwrite.Writer) context.Context {
	return context.WithValue(ctx, reqWriterKey{}, w)
}

// scopedConn returns the request's tenant-scoped conn, or the fallback (the
// shared conn) when the request carries no tenant.
func scopedConn(ctx context.Context, fallback *store.Conn) *store.Conn {
	if c, ok := ctx.Value(reqConnKey{}).(*store.Conn); ok && c != nil {
		return c
	}
	return fallback
}

// scopedWriter mirrors scopedConn for the governed writer (writes).
func scopedWriter(ctx context.Context, fallback *govwrite.Writer) *govwrite.Writer {
	if w, ok := ctx.Value(reqWriterKey{}).(*govwrite.Writer); ok && w != nil {
		return w
	}
	return fallback
}

// tenant mem cache: Memory.Tenant(id) opens its own view; cache one per tenant
// for the life of the server (closed by closeTenantMems at shutdown).
var (
	tenantMemMu   sync.Mutex
	tenantMemsSrv = map[string]mnemos.Memory{}
)

// scopedMem returns a per-tenant Memory view (cached) when the request carries a
// tenant, else the shared fallback. When a tenant IS present but its view can't
// be opened, it returns nil (fail closed) so the cognitive endpoints' nil-guard
// yields a 503 — never the shared __default__ facade, which would serve the
// wrong partition's data.
func scopedMem(ctx context.Context, fallback mnemos.Memory) mnemos.Memory {
	tenant, ok := tenantFromContext(ctx)
	if !ok || fallback == nil {
		return fallback
	}
	tenantMemMu.Lock()
	defer tenantMemMu.Unlock()
	if m, cached := tenantMemsSrv[tenant]; cached {
		return m
	}
	tm, err := fallback.Tenant(tenant)
	if err != nil {
		return nil
	}
	tenantMemsSrv[tenant] = tm
	return tm
}

// closeTenantMems closes every cached per-tenant Memory. Call at serve shutdown.
func closeTenantMems() {
	tenantMemMu.Lock()
	defer tenantMemMu.Unlock()
	for _, m := range tenantMemsSrv {
		_ = m.Close()
	}
	tenantMemsSrv = map[string]mnemos.Memory{}
}

// tenantScopeMiddleware opens one tenant-scoped connection per request (and a
// governed writer borrowing it) and stashes both, closing the connection after
// the handler. Requests without a tenant in context pass through untouched.
func tenantScopeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := tenantFromContext(r.Context()); !ok {
			next.ServeHTTP(w, r)
			return
		}
		conn, err := openConn(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "tenant store unavailable")
			return
		}
		defer closeConn(conn)
		ctx := withReqConn(r.Context(), conn)
		// Wrap borrows the conn (ownConn=false), so the writer's Close only
		// releases the kernel's evidence sink — a no-op when
		// MNEMOS_AXI_EVIDENCE_LOG is unset, but a real file-descriptor release
		// when it is set. Close it per request either way; the conn itself is
		// closed separately above. A Wrap failure must fail the request closed
		// rather than let handlers fall back to the __default__ writer.
		gw, err := govwrite.Wrap(conn, nil)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "tenant store unavailable")
			return
		}
		defer func() { _ = gw.Close() }()
		ctx = withReqWriter(ctx, gw)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
