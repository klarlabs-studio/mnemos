package main

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"sync"

	"go.klarlabs.de/mnemos/internal/govwrite"
	"go.klarlabs.de/mnemos/internal/store"
)

// Read-connection cache. A long-lived server (the MCP transport) opens a store
// connection per request; for Postgres that means creating and pinging a fresh
// pool every time. When enabled, openConn reuses one cached *store.Conn per
// resolved DSN — and the resolved DSN includes ?tenant=<id>, so each tenant
// gets its own pool (RLS isolation is unchanged) that is reused across the
// tenant's requests instead of recreated. Only reads are cached; writes
// (openWriter) stay per-request. Cached conns are closed at server shutdown.
//
// The CLI never enables this (short-lived commands), so its behavior is
// unchanged.
// connCacheMax bounds the number of cached pools (≈ number of hot tenants).
// Beyond it, additional tenants are served with per-request connections rather
// than evicting an in-use pool.
const connCacheMax = 512

var (
	connCacheEnabled bool
	connCacheMu      sync.Mutex
	connCache        = map[string]*store.Conn{}
	cachedConns      = map[*store.Conn]bool{}
)

// enableConnCache turns on read-connection caching for the life of the process.
// Call once, before serving requests.
//
// No-op under the Postgres shared-pool mode (MNEMOS_PG_SHARED_POOL): there,
// openConn checks out a connection from one shared pool and the store.Conn's
// Close resets+releases it — caching the store.Conn would pin one connection
// per tenant and never release it, defeating the shared pool.
func enableConnCache() {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MNEMOS_PG_SHARED_POOL"))) {
	case "1", "true", "yes":
		return
	}
	connCacheEnabled = true
}

// closeConnCache closes every cached connection and resets the cache. Call at
// server shutdown.
func closeConnCache() {
	connCacheMu.Lock()
	defer connCacheMu.Unlock()
	for _, c := range connCache {
		if err := c.Close(); err != nil {
			log.Printf("close cached conn: %v", err)
		}
	}
	connCache = map[string]*store.Conn{}
	cachedConns = map[*store.Conn]bool{}
}

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

// displayDSN returns the DSN actually in effect for this process, with any
// password redacted, for human display (confirmations, log lines). Prefer it
// over resolveDBPath() in user-facing messages: resolveDBPath() ignores
// MNEMOS_DB_URL, so it can name a different store than the one a destructive
// command will operate on.
func displayDSN() string {
	dsn := resolveDSN()
	if u, err := url.Parse(dsn); err == nil && u.User != nil {
		if _, hasPassword := u.User.Password(); hasPassword {
			// "redacted" is alphanumeric so url.String() won't percent-encode it.
			u.User = url.UserPassword(u.User.Username(), "redacted")
			return u.String()
		}
	}
	return dsn
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
	if !connCacheEnabled {
		return store.Open(ctx, dsn)
	}

	// Fast path: return a cached pool. The lock guards only the map, never the
	// blocking store.Open below — so a slow dial for one tenant cannot stall
	// connection setup for others (#150).
	connCacheMu.Lock()
	if c, ok := connCache[dsn]; ok {
		connCacheMu.Unlock()
		return c, nil
	}
	full := len(connCache) >= connCacheMax
	connCacheMu.Unlock()

	// Bound: when the cache is full, serve this (likely cold) tenant with an
	// uncached per-request connection rather than evict an in-use pool. It is
	// not in cachedConns, so the caller's closeConn closes it normally.
	if full {
		return store.Open(ctx, dsn)
	}

	c, err := store.Open(ctx, dsn) // opened WITHOUT the lock held
	if err != nil {
		return nil, err
	}
	connCacheMu.Lock()
	if existing, ok := connCache[dsn]; ok {
		// Lost the race to open the same DSN concurrently — keep the winner,
		// discard our redundant pool.
		connCacheMu.Unlock()
		_ = c.Close()
		return existing, nil
	}
	connCache[dsn] = c
	cachedConns[c] = true
	connCacheMu.Unlock()
	return c, nil
}

// openBaseConn opens the process's base store connection (the unscoped DSN, no
// request tenant), for infrastructure that is NOT tenant-scoped — e.g. the auth
// revocation store, whose tables are excluded from RLS (ADR 0007). Unlike
// openConn it never fail-closes on the multi-tenant requirement.
func openBaseConn(ctx context.Context) (*store.Conn, error) {
	return store.Open(ctx, resolveDSN())
}

// dsnOverrideKey carries an explicit per-request store DSN. It lets a
// concurrent, long-lived server (the MCP transport) point ONE request at a
// second brain (e.g. the repo overlay for `scope=repo`) without mutating the
// process-wide MNEMOS_DB_URL — the env-swap the one-shot hooks use is unsafe
// under concurrency. Highest precedence: it bypasses tenant scoping (a full,
// explicit local DSN).
type dsnOverrideKey struct{}

func withDSNOverride(ctx context.Context, dsn string) context.Context {
	if dsn == "" {
		return ctx
	}
	return context.WithValue(ctx, dsnOverrideKey{}, dsn)
}

func dsnOverrideFromContext(ctx context.Context) (string, bool) {
	d, ok := ctx.Value(dsnOverrideKey{}).(string)
	return d, ok && d != ""
}

// resolveDSNForContext returns the DSN for this request, scoped to the
// effective tenant when the request carries one (multi-tenant serve/MCP mode).
//
// The scoping mechanism depends on the backend (store.TenancyModeForDSN):
//   - Postgres (row-level): append `?tenant=<id>` so the provider pins the
//     mnemos.tenant GUC and RLS isolates the request (ADR 0007).
//   - sqlite / mysql / local libSQL (namespace): set `?namespace=<derived>` so
//     the request lands in a physically separate schema/database/file, created
//     on first open.
//   - anything else (memory, remote libSQL): fail closed rather than silently
//     sharing data across tenants.
//
// A per-request dsnOverride (above) takes precedence over all of this.
func resolveDSNForContext(ctx context.Context) (string, error) {
	if o, ok := dsnOverrideFromContext(ctx); ok {
		return o, nil // explicit per-request brain; concurrency-safe, no env swap
	}
	dsn := resolveDSN()
	tenant, ok := tenantFromContext(ctx)
	if !ok {
		// Fail closed: in multi-tenant mode a request that reached here without
		// a tenant must NOT fall back to the reserved default partition.
		if tenantRequired {
			return "", fmt.Errorf("multi-tenant mode: request has no tenant; refusing to serve the default partition")
		}
		return dsn, nil
	}
	// Defense in depth: never interpolate an unvalidated tenant into the DSN /
	// the `SET mnemos.tenant` the provider runs / a derived namespace.
	if !validTenantID(tenant) {
		return "", fmt.Errorf("invalid tenant id in request context")
	}
	switch store.TenancyModeForDSN(dsn) {
	case store.TenancyRowLevel:
		sep := "?"
		if strings.Contains(dsn, "?") {
			sep = "&"
		}
		return dsn + sep + "tenant=" + tenant, nil
	case store.TenancyNamespace:
		// The derived namespace REPLACES any base namespace: providers read the
		// first `namespace=` value, so a duplicate would silently keep the base.
		return setDSNParam(dsn, "namespace", store.TenantNamespace(tenant)), nil
	default:
		return "", fmt.Errorf("tenant scoping is not supported by this backend (need postgres, sqlite, mysql, or local libsql)")
	}
}

// setDSNParam returns dsn with the query parameter key set to value, removing any
// existing occurrence first. String surgery (not net/url) to avoid re-encoding
// credentials in the DSN; key/value here are charset-safe (validated ids).
func setDSNParam(dsn, key, value string) string {
	stripped := stripDSNParam(dsn, key)
	sep := "?"
	if strings.Contains(stripped, "?") {
		sep = "&"
	}
	return stripped + sep + key + "=" + value
}

// stripDSNParam removes a single named query parameter from a DSN, leaving the
// rest untouched.
func stripDSNParam(dsn, key string) string {
	base, query, ok := strings.Cut(dsn, "?")
	if !ok {
		return dsn
	}
	kept := make([]string, 0)
	for p := range strings.SplitSeq(query, "&") {
		if p == key || strings.HasPrefix(p, key+"=") {
			continue
		}
		kept = append(kept, p)
	}
	if len(kept) == 0 {
		return base
	}
	return base + "?" + strings.Join(kept, "&")
}

// tenantRequired records that the process is serving in multi-tenant mode (MCP
// --require-tenant or serve --require-tenant).
// (`mcp --http --require-tenant`). It makes resolveDSNForContext fail closed
// instead of defaulting to the __default__ partition when a request somehow
// lacks a tenant.
var tenantRequired bool

// setTenantRequired latches multi-tenant mode on. Call once at startup.
func setTenantRequired() { tenantRequired = true }

// dsnHasTenantParam reports whether a DSN already carries a ?tenant= query
// parameter. In multi-tenant mode this must be rejected: Postgres ParseDSN
// reads the FIRST tenant value, so a base-DSN tenant would override every
// request's tenant and collapse all tenants into one.
func dsnHasTenantParam(dsn string) bool {
	_, query, ok := strings.Cut(dsn, "?")
	if !ok {
		return false
	}
	q, err := url.ParseQuery(query)
	if err != nil {
		return false
	}
	_, has := q["tenant"]
	return has
}

// closeConn closes a *store.Conn, logging any error.
// Use as: defer closeConn(conn)
func closeConn(conn *store.Conn) {
	// A cached connection is owned by the cache and closed at shutdown; a
	// per-request closeConn on it must be a no-op or later requests would reuse
	// a closed pool.
	if connCacheEnabled {
		connCacheMu.Lock()
		cached := cachedConns[conn]
		connCacheMu.Unlock()
		if cached {
			return
		}
	}
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
