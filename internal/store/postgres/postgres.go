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
	"regexp"
	"strings"

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

// ParseDSN extracts the namespace + tenant and produces a libpq-compatible
// DSN with those query parameters stripped.
func ParseDSN(dsn string) (DSN, error) {
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		return DSN{}, fmt.Errorf("postgres: not a postgres dsn: %q", dsn)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return DSN{}, fmt.Errorf("postgres: parse dsn: %w", err)
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
		return nil, fmt.Errorf("postgres: ping: %w (dsn=%s)", err, parsed.LibpqDSN)
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

	return &store.Conn{
		Events:        EventRepository{db: db, ns: parsed.Namespace},
		Claims:        ClaimRepository{db: db, ns: parsed.Namespace},
		Relationships: RelationshipRepository{db: db, ns: parsed.Namespace},
		Embeddings:    EmbeddingRepository{db: db, ns: parsed.Namespace, vectorEnabled: vectorEnabled},
		Users:         UserRepository{db: db, ns: parsed.Namespace},
		RevokedTokens: RevokedTokenRepository{db: db, ns: parsed.Namespace},
		Agents:        AgentRepository{db: db, ns: parsed.Namespace},
		Entities:      EntityRepository{db: db, ns: parsed.Namespace},
		Jobs:          CompilationJobRepository{db: db, ns: parsed.Namespace},
		Actions:       ActionRepository{db: db, ns: parsed.Namespace},
		Outcomes:      OutcomeRepository{db: db, ns: parsed.Namespace},
		Lessons:       LessonRepository{db: db, ns: parsed.Namespace},
		Decisions:     DecisionRepository{db: db, ns: parsed.Namespace},
		Playbooks:     PlaybookRepository{db: db, ns: parsed.Namespace},
		EntityRels:    EntityRelationshipRepository{db: db, ns: parsed.Namespace},
		Blocks:        BlockRepository{db: db, ns: parsed.Namespace},
		Raw:           db,
		Closer:        db.Close,
	}, nil
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
