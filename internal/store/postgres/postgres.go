// Package postgres implements a [store] provider backed by Postgres.
//
// The provider connects via pgx/v5/stdlib so the rest of the
// implementation can stay on database/sql. ADR 0001 §3 mandates
// a `?namespace=` query parameter that translates to a Postgres
// schema (CREATE SCHEMA IF NOT EXISTS + SET search_path); the DSN
// parser strips it before handing the libpq-shaped DSN to pgx.
//
// Schema bootstrap runs `sql/postgres/schema.sql` on every Open. It
// is written exclusively with `CREATE TABLE IF NOT EXISTS` /
// `CREATE INDEX IF NOT EXISTS` so re-opens are no-ops.
package postgres

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"database/sql"

	"go.klarlabs.de/mnemos/internal/store"

	"github.com/jackc/pgx/v5"
	// pgx-stdlib registers the "pgx" sql.DB driver in init() and provides
	// the connector + AfterConnect hook used to pin search_path per conn.
	"github.com/jackc/pgx/v5/stdlib"
)

// schemaSQL is the in-binary copy of sql/postgres/schema.sql,
// applied on every Open. The file is idempotent (every CREATE uses
// IF NOT EXISTS) so re-opens after a successful bootstrap are
// no-ops.
//
//go:embed schema.sql
var schemaSQL string

// ErrNotImplemented is a historical sentinel from the provider's
// scaffold phase. The provider is now production-ready and nothing
// here returns it any longer. Deprecated: retained only so downstream
// code that still errors.Is against it compiles; stop testing for it.
// It will be removed in a future major.
var ErrNotImplemented = errors.New("postgres provider: feature not yet implemented")

// Register the postgres provider for both common scheme aliases.
// Both `postgres://` and `postgresql://` appear in the wild
// (libpq, JDBC, Go's pq driver, pgx).
func init() {
	store.Register("postgres", openProvider)
	store.Register("postgresql", openProvider)
}

// DSN holds the parsed components of a Postgres DSN.
type DSN struct {
	// Raw is the original DSN string the operator supplied.
	Raw string

	// LibpqDSN is the Raw DSN minus the namespace query parameter.
	// Pass this to pgx — drivers reject unknown keys.
	LibpqDSN string

	// Namespace is the value of ?namespace= (or the default
	// "mnemos"), validated against `^[a-z][a-z0-9_]{0,62}$` per
	// ADR 0001. Used as the Postgres schema name.
	Namespace string

	// Tenant is the value of ?tenant= (or the default "__default__"),
	// validated against tenantRE. Pinned as the `mnemos.tenant` GUC on
	// every connection; row-level security filters reads/writes to it.
	// See ADR 0007.
	Tenant string
}

// namespaceRE mirrors the contract from ADR 0001 §3: lowercase,
// alphanumeric+underscore, must start with a letter, max 63 bytes
// (Postgres identifier limit).
var namespaceRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// tenantRE validates the tenant id (ADR 0007). Quote/backslash-free so it is safe
// to interpolate into the `SET mnemos.tenant = '<id>'` statement.
var tenantRE = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

const defaultNamespace = "mnemos"

// defaultTenant is the reserved tenant an unscoped connection uses. MUST match the
// mnemos root package's defaultTenant so unscoped data stays reachable.
const defaultTenant = "__default__"

// pgQuerier is the subset of *sql.DB the repositories use. Both *sql.DB (the
// per-tenant-pool model) and *sql.Conn (the shared-pool model, ADR 0007
// Phase 2) satisfy it, so a repository can be bound to either without code
// change. It is exactly the four methods the repositories call on r.db.
type pgQuerier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
}

// dsnPasswordRE matches the "://user:password@" segment of a URL DSN so a
// password can be masked when the DSN is unparseable (best-effort fallback).
var dsnPasswordRE = regexp.MustCompile(`(://[^:/@]+:)[^@/]*(@)`)

// redactDSN masks the password in a URL-form DSN so it is safe to include in
// error messages and logs. Never returns the original password.
func redactDSN(dsn string) string {
	if u, err := url.Parse(dsn); err == nil && u.User != nil {
		if _, has := u.User.Password(); has {
			u.User = url.UserPassword(u.User.Username(), "***")
			return u.String()
		}
		return dsn
	}
	return dsnPasswordRE.ReplaceAllString(dsn, "${1}***${2}")
}

// ParseDSN extracts the namespace + tenant and produces a libpq-compatible
// DSN with those query parameters stripped.
func ParseDSN(dsn string) (DSN, error) {
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		return DSN{}, fmt.Errorf("postgres: not a postgres dsn: %q", dsn)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return DSN{}, fmt.Errorf("postgres: malformed dsn %s", redactDSN(dsn))
	}

	q := u.Query()
	ns := q.Get("namespace")
	if ns == "" {
		ns = defaultNamespace
	}
	if !namespaceRE.MatchString(ns) {
		return DSN{}, fmt.Errorf("postgres: invalid namespace %q (want %s)", ns, namespaceRE.String())
	}
	tenant := q.Get("tenant")
	if tenant == "" {
		tenant = defaultTenant
	}
	if tenant != defaultTenant && !tenantRE.MatchString(tenant) {
		return DSN{}, fmt.Errorf("postgres: invalid tenant %q (want %s)", tenant, tenantRE.String())
	}
	q.Del("namespace")
	q.Del("tenant")
	u.RawQuery = q.Encode()

	return DSN{
		Raw:       dsn,
		LibpqDSN:  u.String(),
		Namespace: ns,
		Tenant:    tenant,
	}, nil
}

// openProvider opens a Postgres connection, bootstraps the schema
// inside the configured namespace, and returns a Conn populated
// with port-typed repositories.
//
// The Conn's Closer disposes of the *sql.DB; callers should
// defer conn.Close().
func openProvider(ctx context.Context, dsn string) (*store.Conn, error) {
	parsed, err := ParseDSN(dsn)
	if err != nil {
		return nil, err
	}

	// Shared-pool mode (ADR 0007 Phase 2, opt-in via MNEMOS_PG_SHARED_POOL):
	// a tenant-scoped request checks out one connection from a single shared
	// pool and pins the tenant on it, instead of opening a whole pool per
	// tenant. The default path below is unchanged.
	if sharedPoolEnabled() && parsed.Tenant != defaultTenant {
		return openSharedTenantConn(ctx, parsed)
	}

	// Open through a connector with an AfterConnect hook that pins
	// search_path to the namespace schema on EVERY pooled connection —
	// so non-tx queries and lazily-created connections all resolve to the
	// right schema, not just the first. (Repositories still qualify their
	// tables with the namespace as a belt-and-suspenders fallback.)
	connConfig, err := pgx.ParseConfig(parsed.LibpqDSN)
	if err != nil {
		return nil, fmt.Errorf("postgres: parse conn config: %w", err)
	}
	ns := parsed.Namespace  // validated by namespaceRE in ParseDSN — safe to interpolate
	tenant := parsed.Tenant // validated by tenantRE (or the reserved default) — safe to interpolate
	connector := stdlib.GetConnector(*connConfig, stdlib.OptionAfterConnect(func(ctx context.Context, conn *pgx.Conn) error {
		// Pin BOTH the schema (namespace) and the tenant on every connection.
		// Row-level security keys off the mnemos.tenant GUC (ADR 0007), so this
		// makes the whole pool tenant-scoped by construction — a missing GUC
		// fails closed (RLS denies, NOT NULL rejects writes), never open.
		if _, err := conn.Exec(ctx, "SET search_path TO "+ns); err != nil {
			return err
		}
		_, err := conn.Exec(ctx, "SET mnemos.tenant = '"+tenant+"'")
		return err
	}))
	db := sql.OpenDB(connector)
	tuneConnPool(db)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres: ping: %w (dsn=%s)", err, redactDSN(parsed.LibpqDSN))
	}

	if err := bootstrap(ctx, db, parsed.Namespace); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres: bootstrap namespace %q: %w", parsed.Namespace, err)
	}

	// search_path is pinned per-connection by the AfterConnect hook wired
	// into the connector above, so every pooled connection resolves to the
	// namespace schema. Repositories additionally qualify their tables with
	// the namespace as a fallback.

	// Detect whether the pgvector accelerator column landed (schema.sql adds
	// it only when the `vector` type is installed). Ground-truth the column's
	// existence rather than re-probing the type, so a race or a manual drop
	// can't leave Upsert writing to a missing column.
	vectorEnabled := embeddingVectorColumnExists(ctx, db, parsed.Namespace)

	return newStoreConn(db, parsed.Namespace, vectorEnabled, db, db.Close), nil
}

// sharedPoolEnabled reports whether the opt-in single-shared-pool mode is on.
func sharedPoolEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MNEMOS_PG_SHARED_POOL"))) {
	case "1", "true", "yes":
		return true
	}
	return false
}

type sharedPool struct {
	db            *sql.DB
	vectorEnabled bool
}

var (
	sharedPoolsMu sync.Mutex
	sharedPools   = map[string]*sharedPool{}
)

// getSharedPool returns the process-wide pool for a (libpq DSN, namespace),
// creating and bootstrapping it once. Its connections carry the reserved
// __default__ tenant as a baseline; per-request tenants are set on checked-out
// connections in openSharedTenantConn.
func getSharedPool(ctx context.Context, parsed DSN) (*sharedPool, error) {
	key := parsed.LibpqDSN + "|" + parsed.Namespace
	sharedPoolsMu.Lock()
	defer sharedPoolsMu.Unlock()
	if sp, ok := sharedPools[key]; ok {
		return sp, nil
	}

	connConfig, err := pgx.ParseConfig(parsed.LibpqDSN)
	if err != nil {
		return nil, fmt.Errorf("postgres: parse conn config: %w", err)
	}
	ns := parsed.Namespace // validated by namespaceRE
	connector := stdlib.GetConnector(*connConfig, stdlib.OptionAfterConnect(func(ctx context.Context, conn *pgx.Conn) error {
		if _, err := conn.Exec(ctx, "SET search_path TO "+ns); err != nil {
			return err
		}
		// Baseline tenant fails closed: a connection used before a request pins
		// its tenant sees the reserved partition, never another tenant's rows.
		_, err := conn.Exec(ctx, "SET mnemos.tenant = '"+defaultTenant+"'")
		return err
	}))
	db := sql.OpenDB(connector)
	tuneConnPool(db)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres: ping: %w (dsn=%s)", err, redactDSN(parsed.LibpqDSN))
	}
	if err := bootstrap(ctx, db, ns); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres: bootstrap namespace %q: %w", ns, err)
	}
	sp := &sharedPool{db: db, vectorEnabled: embeddingVectorColumnExists(ctx, db, ns)}
	sharedPools[key] = sp
	return sp, nil
}

// openSharedTenantConn checks out one connection from the shared pool, pins the
// request's tenant on it (RLS then isolates every query), and returns a Conn
// whose Closer resets the tenant and releases the connection back to the pool —
// so the tenant can never leak to the next borrower.
func openSharedTenantConn(ctx context.Context, parsed DSN) (*store.Conn, error) {
	sp, err := getSharedPool(ctx, parsed)
	if err != nil {
		return nil, err
	}
	conn, err := sp.db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("postgres: checkout connection: %w", err)
	}
	// parsed.Tenant is validated by tenantRE in ParseDSN — safe to interpolate.
	if _, err := conn.ExecContext(ctx, "SET mnemos.tenant = '"+parsed.Tenant+"'"); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("postgres: set tenant: %w", err)
	}
	closer := func() error {
		// RESET clears the tenant GUC (→ NULL → RLS denies everything) before
		// the connection returns to the pool: fail-closed if it is ever reused
		// without a fresh tenant being set. Best-effort; a reset failure means
		// the connection is likely dead and will be discarded on next use.
		rctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(rctx, "RESET mnemos.tenant")
		return conn.Close()
	}
	return newStoreConn(conn, parsed.Namespace, sp.vectorEnabled, conn, closer), nil
}

// newStoreConn wires the port-typed repositories onto a querier (either the
// pooled *sql.DB or a tenant-scoped *sql.Conn) and packages them with the
// namespace, raw handle, and closer. Single source of truth for the repository
// set so the per-pool and shared-pool paths can't drift.
func newStoreConn(db pgQuerier, ns string, vectorEnabled bool, raw any, closer func() error) *store.Conn {
	return &store.Conn{
		Events:        EventRepository{db: db, ns: ns},
		Claims:        ClaimRepository{db: db, ns: ns},
		Relationships: RelationshipRepository{db: db, ns: ns},
		Embeddings:    EmbeddingRepository{db: db, ns: ns, vectorEnabled: vectorEnabled},
		Users:         UserRepository{db: db, ns: ns},
		RevokedTokens: RevokedTokenRepository{db: db, ns: ns},
		Agents:        AgentRepository{db: db, ns: ns},
		Entities:      EntityRepository{db: db, ns: ns},
		Jobs:          CompilationJobRepository{db: db, ns: ns},
		Actions:       ActionRepository{db: db, ns: ns},
		Outcomes:      OutcomeRepository{db: db, ns: ns},
		Lessons:       LessonRepository{db: db, ns: ns},
		Decisions:     DecisionRepository{db: db, ns: ns},
		Playbooks:     PlaybookRepository{db: db, ns: ns},
		EntityRels:    EntityRelationshipRepository{db: db, ns: ns},
		Blocks:        BlockRepository{db: db, ns: ns},
		Expectations:  ExpectationRepository{db: db, ns: ns},
		GlobalSchemas: GlobalSchemaRepository{db: db, ns: ns},
		Journal:       JournalRepository{db: db, ns: ns},
		Raw:           raw,
		Closer:        closer,
	}
}

// bootstrap creates the namespace schema (idempotent) and applies
// schema.sql inside it. Runs in a single transaction so a partial
// failure leaves the database in a consistent state — either every
// CREATE succeeded or none did.
func bootstrap(ctx context.Context, db *sql.DB, namespace string) error {
	if !namespaceRE.MatchString(namespace) {
		return fmt.Errorf("invalid namespace %q", namespace)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Schema name and search_path are identifiers, not values, so
	// they must be interpolated rather than parameter-bound. The
	// regex check above is the only way they reach this exec.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`CREATE SCHEMA IF NOT EXISTS %s`, namespace)); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`SET LOCAL search_path TO %s`, namespace)); err != nil {
		return fmt.Errorf("set search_path: %w", err)
	}
	if _, err := tx.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	// search_path is per-connection. After bootstrap commits, set
	// it on the pool's default connection too so subsequent
	// non-tx queries see the namespace. Subsequent connections
	// created lazily by the pool do NOT inherit this — for those,
	// every repository qualifies its tables with the namespace
	// (handled in the SQL strings below).
	if _, err := db.ExecContext(ctx, fmt.Sprintf(`SET search_path TO %s`, namespace)); err != nil {
		return fmt.Errorf("set search_path on pool: %w", err)
	}
	return nil
}

// qualify returns the namespace-qualified table name. Each repo
// builds its SQL with this so pooled connections that didn't
// receive the SET search_path always hit the right schema.
func qualify(ns, table string) string {
	return ns + "." + table
}

// embeddingVectorColumnExists reports whether the pgvector accelerator
// column `embedding` is present on <ns>.embeddings. schema.sql adds it only
// when the `vector` type is installed, so this is the single source of truth
// for whether the native `<=>` recall path is available — a false keeps the
// repository on the portable bytea + Go-cosine path. Any probe error is
// treated as "not available" (fail safe, never fail open into a bad query).
func embeddingVectorColumnExists(ctx context.Context, db *sql.DB, namespace string) bool {
	var exists bool
	err := db.QueryRowContext(ctx, `
SELECT EXISTS (
  SELECT 1 FROM information_schema.columns
  WHERE table_schema = $1 AND table_name = 'embeddings' AND column_name = 'embedding'
)`, namespace).Scan(&exists)
	return err == nil && exists
}
