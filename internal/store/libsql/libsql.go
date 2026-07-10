// Package libsql implements a [store] provider backed by libSQL —
// the SQLite-compatible engine behind Turso. The wire SQL dialect
// matches SQLite, so this provider reuses the SQLite schema and the
// existing SQLite repository implementations: only the registration,
// DSN passthrough, and database/sql driver name change.
//
// Two deployment shapes are supported:
//
//  1. Remote (Turso, custom libSQL servers):
//     libsql://my-db.turso.io?authToken=eyJ...
//
//  2. Local file (libSQL embedded; behaves like SQLite for
//     compatibility tests and offline-first deployments):
//     libsql:///absolute/path/to/local.db
//
// The pure-Go libsql-client-go driver keeps CGO_ENABLED=0 — the
// project's pinned default. Operators who want an embedded libSQL
// build with CGO can blank-import a different driver in their fork;
// this provider does not depend on CGO.
//
// Per ADR 0001 §3, every provider exposes a `?namespace=` query param.
//   - Local file mode partitions by file, exactly like SQLite: a non-default
//     namespace rewrites the path (…/db.db + namespace "chronos" → …/db_chronos.db),
//     which is what makes namespace-per-tenant isolation work for local libSQL.
//   - Remote mode ignores namespace — each remote database already represents a
//     tenant boundary, provisioned out of band — so tenancy is gated off for it
//     (see store.TenancyModeForDSN).
package libsql

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"go.klarlabs.de/mnemos/internal/store"
	"go.klarlabs.de/mnemos/internal/store/sqlite"

	// libsql-client-go registers the "libsql" sql.DB driver in init().
	_ "github.com/tursodatabase/libsql-client-go/libsql"
)

// driverName is the database/sql driver name registered by
// libsql-client-go.
const driverName = "libsql"

// namespaceRE and defaultNamespace mirror the SQLite provider's contract
// (ADR 0001 §3). Local libSQL reuses SQLite's file-per-namespace scheme.
var namespaceRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

const defaultNamespace = "mnemos"

// Register the libsql provider. Only one scheme — Turso documentation
// uses libsql:// uniformly; we don't add an alias to avoid
// overlapping with the eventual https:// HTTP-only mode.
func init() {
	store.Register("libsql", openProvider)
}

// openProvider parses a libsql:// URL, opens the database via the
// libSQL driver, and applies the SQLite schema (since libSQL is
// wire-compatible). Returns a Conn populated with the existing
// SQLite repository types.
func openProvider(ctx context.Context, dsn string) (*store.Conn, error) {
	driverDSN, err := translateDSN(dsn)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open(driverName, driverDSN)
	if err != nil {
		return nil, fmt.Errorf("libsql: sql.Open: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("libsql: ping: %w (dsn=%s)", err, redactDSN(driverDSN))
	}
	if err := sqlite.Bootstrap(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("libsql: bootstrap schema: %w", err)
	}

	return &store.Conn{
		Events:        sqlite.NewEventRepository(db),
		Claims:        sqlite.NewClaimRepository(db),
		Relationships: sqlite.NewRelationshipRepository(db),
		Embeddings:    sqlite.NewEmbeddingRepository(db),
		Users:         sqlite.NewUserRepository(db),
		RevokedTokens: sqlite.NewRevokedTokenRepository(db),
		Agents:        sqlite.NewAgentRepository(db),
		Entities:      sqlite.NewEntityRepository(db),
		Jobs:          sqlite.NewCompilationJobRepository(db),
		Actions:       sqlite.NewActionRepository(db),
		Outcomes:      sqlite.NewOutcomeRepository(db),
		Lessons:       sqlite.NewLessonRepository(db),
		Decisions:     sqlite.NewDecisionRepository(db),
		Playbooks:     sqlite.NewPlaybookRepository(db),
		EntityRels:    sqlite.NewEntityRelationshipRepository(db),
		Feedback:      sqlite.NewFeedbackRepository(db),
		ClaimVersions: sqlite.NewClaimVersionRepository(db),
		Raw:           db,
		Closer:        db.Close,
	}, nil
}

// translateDSN converts a Mnemos libsql:// URL into the form the
// libsql-client-go driver expects.
//
//   - libsql://my-db.turso.io?authToken=xxx → passes through (the
//     driver speaks this URL form natively).
//   - libsql:///absolute/path.db → "file:/absolute/path.db" so the
//     driver opens an embedded local file.
//
// For a LOCAL file DSN a non-default `?namespace=` rewrites the file path
// (…/db.db → …/db_<ns>.db), giving physical isolation per namespace — the basis
// for namespace-per-tenant. For a REMOTE DSN the namespace param is dropped
// (libSQL has no schema-namespace concept there); tenancy is gated off for
// remote libSQL by store.TenancyModeForDSN.
func translateDSN(dsn string) (string, error) {
	if !strings.HasPrefix(dsn, "libsql://") {
		return "", fmt.Errorf("libsql: not a libsql dsn: %q", dsn)
	}
	// Extract the namespace before stripping it, so local file mode can apply
	// the file-per-namespace suffix.
	ns := queryParam(dsn, "namespace")
	// Strip the namespace query param if present, keeping the rest
	// of the URL intact. We do this with simple string surgery to
	// avoid net/url's re-encoding of legitimate driver params.
	stripped := stripQueryParam(dsn, "namespace")

	// Local-file form: libsql:///absolute/path.db. The driver wants
	// file:/absolute/path.db. Detect by the empty host between the
	// scheme's `//` and the path's leading `/`.
	if strings.HasPrefix(stripped, "libsql:///") {
		path := strings.TrimPrefix(stripped, "libsql://")
		// Drop any query string for local files — libsql file mode
		// doesn't accept libsql query params.
		if i := strings.Index(path, "?"); i >= 0 {
			path = path[:i]
		}
		// Non-default namespace → distinct file (mirrors the SQLite provider).
		if ns != "" && ns != defaultNamespace {
			if !namespaceRE.MatchString(ns) {
				return "", fmt.Errorf("libsql: invalid namespace %q (want %s)", ns, namespaceRE.String())
			}
			ext := filepath.Ext(path)
			path = strings.TrimSuffix(path, ext) + "_" + ns + ext
		}
		return "file:" + path, nil
	}
	// Remote form: pass through unchanged. The driver parses
	// libsql://… directly.
	return stripped, nil
}

// queryParam returns the value of a single named query parameter, or "" if
// absent. String surgery (not net/url) to avoid re-encoding driver tokens.
func queryParam(rawURL, name string) string {
	_, query, ok := strings.Cut(rawURL, "?")
	if !ok {
		return ""
	}
	for p := range strings.SplitSeq(query, "&") {
		if v, ok := strings.CutPrefix(p, name+"="); ok {
			return v
		}
	}
	return ""
}

// stripQueryParam removes a single named query parameter from a URL
// without otherwise touching the encoding. We avoid net/url here
// because round-tripping through u.Query().Encode() can re-encode
// legitimate driver tokens (authToken, etc.) in surprising ways.
func stripQueryParam(rawURL, name string) string {
	q := strings.Index(rawURL, "?")
	if q < 0 {
		return rawURL
	}
	prefix := rawURL[:q]
	queryPart := rawURL[q+1:]
	parts := strings.Split(queryPart, "&")
	kept := make([]string, 0, len(parts))
	prefixMatch := name + "="
	for _, p := range parts {
		if p == name || strings.HasPrefix(p, prefixMatch) {
			continue
		}
		kept = append(kept, p)
	}
	if len(kept) == 0 {
		return prefix
	}
	return prefix + "?" + strings.Join(kept, "&")
}

// redactDSN drops sensitive query params (authToken) when echoing a
// DSN back to operators in error messages.
func redactDSN(dsn string) string {
	q := strings.Index(dsn, "?")
	if q < 0 {
		return dsn
	}
	prefix := dsn[:q]
	parts := strings.Split(dsn[q+1:], "&")
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.HasPrefix(p, "authToken=") {
			kept = append(kept, "authToken=REDACTED")
			continue
		}
		kept = append(kept, p)
	}
	if len(kept) == 0 {
		return prefix
	}
	return prefix + "?" + strings.Join(kept, "&")
}
