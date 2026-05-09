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

	"github.com/felixgeelhaar/mnemos/internal/store"

	// pgx-stdlib registers the "pgx" sql.DB driver in init().
	_ "github.com/jackc/pgx/v5/stdlib"
)

// schemaSQL is the in-binary copy of sql/postgres/schema.sql,
// applied on every Open. The file is idempotent (every CREATE uses
// IF NOT EXISTS) so re-opens after a successful bootstrap are
// no-ops.
//
//go:embed schema.sql
var schemaSQL string

// ErrNotImplemented is the historical sentinel from the scaffold
// phase. Kept exported so existing callers that errors.Is against
// it still compile during the cutover; once the provider proves
// out, downstream code can stop testing for it.
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
}

// namespaceRE mirrors the contract from ADR 0001 §3: lowercase,
// alphanumeric+underscore, must start with a letter, max 63 bytes
// (Postgres identifier limit).
var namespaceRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

const defaultNamespace = "mnemos"

// ParseDSN extracts the namespace and produces a libpq-compatible
// DSN with the namespace query parameter stripped.
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
	q.Del("namespace")
	u.RawQuery = q.Encode()

	return DSN{
		Raw:       dsn,
		LibpqDSN:  u.String(),
		Namespace: ns,
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

	db, err := sql.Open("pgx", parsed.LibpqDSN)
	if err != nil {
		return nil, fmt.Errorf("postgres: sql.Open: %w", err)
	}
	tuneConnPool(db)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres: ping: %w (dsn=%s)", err, parsed.LibpqDSN)
	}

	if err := bootstrap(ctx, db, parsed.Namespace); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres: bootstrap namespace %q: %w", parsed.Namespace, err)
	}

	// search_path applies per-connection; setting it on the *sql.DB
	// after pool creation only affects the first checked-out
	// connection. Use a SET command in a default ConnMaxIdleTime
	// hook? Simpler: encode it in the DSN options — the pgx stdlib
	// accepts `search_path` in the standard connection string.
	// For now we set it in bootstrap via SET LOCAL inside the
	// migration tx; the pool's per-conn search_path is set by
	// applying SET search_path on every Open below as well so any
	// pooled connection (created lazily by database/sql) sees the
	// right schema. The cleanest fix is to wire pgx's
	// AfterConnect hook; that's a follow-up.

	return &store.Conn{
		Events:        EventRepository{db: db, ns: parsed.Namespace},
		Claims:        ClaimRepository{db: db, ns: parsed.Namespace},
		Relationships: RelationshipRepository{db: db, ns: parsed.Namespace},
		Embeddings:    EmbeddingRepository{db: db, ns: parsed.Namespace},
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
