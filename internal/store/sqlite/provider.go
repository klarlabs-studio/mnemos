package sqlite

import (
	"context"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"

	"go.klarlabs.de/mnemos/internal/store"
)

// Register the SQLite provider with the top-level store factory. Both
// "sqlite" and "sqlite3" are accepted so DSNs from different ecosystems
// (database/sql driver name vs. URL convention) work uniformly.
func init() {
	store.Register("sqlite", openProvider)
	store.Register("sqlite3", openProvider)
}

// namespaceRE mirrors ADR 0001 §3: lowercase, alphanumeric+underscore,
// must start with a letter, max 63 bytes.
var namespaceRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

const defaultNamespace = "mnemos"

// openProvider parses a sqlite[3]:// DSN, opens the underlying database
// via the existing [open] function, and bundles every port-typed
// repository into a [store.Conn].
func openProvider(_ context.Context, dsn string) (*store.Conn, error) {
	parsed, err := parseDSN(dsn)
	if err != nil {
		return nil, err
	}
	db, err := open(parsed.Path)
	if err != nil {
		return nil, err
	}
	return &store.Conn{
		Events:        NewEventRepository(db),
		Claims:        NewClaimRepository(db),
		Relationships: NewRelationshipRepository(db),
		Embeddings:    NewEmbeddingRepository(db),
		Users:         NewUserRepository(db),
		RevokedTokens: NewRevokedTokenRepository(db),
		Agents:        NewAgentRepository(db),
		Entities:      NewEntityRepository(db),
		Jobs:          NewCompilationJobRepository(db),
		Actions:       NewActionRepository(db),
		Outcomes:      NewOutcomeRepository(db),
		Lessons:       NewLessonRepository(db),
		Decisions:     NewDecisionRepository(db),
		Playbooks:     NewPlaybookRepository(db),
		EntityRels:    NewEntityRelationshipRepository(db),
		Incidents:     NewIncidentRepository(db),
		Feedback:      NewFeedbackRepository(db),
		ClaimVersions: NewClaimVersionRepository(db),
		Blocks:        NewBlockRepository(db),
		Expectations:  NewExpectationRepository(db),
		GlobalSchemas: NewGlobalSchemaRepository(db),
		Raw:           db,
		Closer:        db.Close,
	}, nil
}

// dsn holds the parsed components of a sqlite[3]:// DSN.
type dsn struct {
	Path      string // filesystem path to the database file
	Namespace string // validated namespace, default "mnemos"
}

// parseDSN extracts the filesystem path and namespace from a sqlite:// or
// sqlite3:// DSN. Namespace defaults to "mnemos". If an explicit
// non-default namespace is provided, the path is modified to include it
// (e.g. "/path/to/db.db" with namespace "chronos" becomes
// "/path/to/db_chronos.db"), producing distinct files per namespace.
func parseDSN(raw string) (dsn, error) {
	const (
		prefix2 = "sqlite://"
		prefix3 = "sqlite3://"
	)
	var rest string
	switch {
	case strings.HasPrefix(raw, prefix2):
		rest = strings.TrimPrefix(raw, prefix2)
	case strings.HasPrefix(raw, prefix3):
		rest = strings.TrimPrefix(raw, prefix3)
	default:
		return dsn{}, fmt.Errorf("sqlite: not a sqlite dsn: %q", raw)
	}

	u, err := url.Parse("sqlite://" + rest)
	if err != nil {
		return dsn{}, fmt.Errorf("sqlite: parse dsn: %w", err)
	}

	path := u.Path
	if path == "" {
		path = u.Opaque // handle sqlite://relative/path without leading /
	}
	if path == "" {
		return dsn{}, fmt.Errorf("sqlite: dsn %q has no path", raw)
	}

	ns := u.Query().Get("namespace")
	if ns == "" {
		ns = defaultNamespace
	}
	if !namespaceRE.MatchString(ns) {
		return dsn{}, fmt.Errorf("sqlite: invalid namespace %q (want %s)", ns, namespaceRE.String())
	}

	// For non-default namespaces, produce a distinct file per namespace.
	if ns != defaultNamespace {
		ext := filepath.Ext(path)
		base := strings.TrimSuffix(path, ext)
		path = base + "_" + ns + ext
	}

	return dsn{Path: path, Namespace: ns}, nil
}
