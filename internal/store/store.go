// Package store wires Mnemos's persistence backends behind a single
// scheme-dispatched factory.
//
// Each provider (memory, sqlite, postgres, ...) lives in a subpackage
// and registers itself in init() with [Register]. Consumers blank-
// import the providers they want to support and open connections by
// DSN through [Open]. The factory dispatches on the URL scheme so new
// providers can be added without touching this package.
//
// See docs/adr/0001-multi-backend-storage.md for the full contract.
package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"go.klarlabs.de/mnemos/internal/ports"
)

// OpenFunc is the provider-side factory signature. Each provider
// registers one of these per scheme it handles.
type OpenFunc func(ctx context.Context, dsn string) (*Conn, error)

// Conn is the engine's port-typed view of an open backend. Every
// provider returns a Conn populated with concrete repositories that
// satisfy the [ports] interfaces. Concrete repositories that have not
// yet been lifted to ports (entity, compilation_job) are reachable
// through Raw when callers need provider-specific access.
type Conn struct {
	Events        ports.EventRepository
	Claims        ports.ClaimRepository
	Relationships ports.RelationshipRepository
	Embeddings    ports.EmbeddingRepository
	Users         ports.UserRepository
	RevokedTokens ports.RevokedTokenRepository
	Agents        ports.AgentRepository
	Entities      ports.EntityRepository
	Jobs          ports.CompilationJobRepository
	Actions       ports.ActionRepository
	Outcomes      ports.OutcomeRepository
	Lessons       ports.LessonRepository
	Decisions     ports.DecisionRepository
	Playbooks     ports.PlaybookRepository
	EntityRels    ports.EntityRelationshipRepository
	Incidents     ports.IncidentRepository
	// Feedback is the per-claim feedback state side table (Refs #40).
	// nil on providers that have not implemented it yet; callers
	// type-check before use.
	Feedback ports.FeedbackRepository
	// ClaimVersions is the append-only version chain for claims
	// (Refs #38). nil on providers without an implementation.
	ClaimVersions ports.ClaimVersionRepository

	// Blocks is the working-memory block store (agent "core memory":
	// bounded, labeled, mutable text per owner). nil on providers
	// without an implementation; callers type-check before use.
	Blocks ports.BlockRepository

	// Expectations is the structured-forward-expectation store (one per
	// claim). nil on providers without an implementation.
	Expectations ports.ExpectationRepository

	// Raw is the provider's underlying handle (e.g. *sql.DB for
	// SQLite/Postgres, an in-memory state struct for memory). It is
	// nil-safe to ignore. Some call sites still issue raw SQL or
	// reach for SQLite-specific helpers (entity merge transaction
	// internals, FTS5 search) — they type-assert Raw to the
	// provider's native handle. Phase 2b will further reduce that
	// surface.
	Raw any

	// Closer releases backend resources. Providers populate it; Close
	// invokes it. Repositories must not be used after Close returns.
	Closer func() error
}

// Close releases backend resources. Safe to call on a nil Conn or one
// without a Closer set.
func (c *Conn) Close() error {
	if c == nil || c.Closer == nil {
		return nil
	}
	return c.Closer()
}

var (
	registryMu sync.RWMutex
	registry   = map[string]OpenFunc{}
)

// Register associates a URL scheme with a provider factory. Providers
// invoke this from init(); duplicate registration panics so collisions
// surface at startup rather than silently shadowing.
func Register(scheme string, fn OpenFunc) {
	if scheme == "" {
		panic("store: cannot register provider with empty scheme")
	}
	if fn == nil {
		panic("store: cannot register nil OpenFunc for " + scheme)
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := registry[scheme]; dup {
		panic("store: duplicate provider registration for " + scheme)
	}
	registry[scheme] = fn
}

// Open opens a backend by DSN. The DSN's URL scheme picks the
// provider; everything after the scheme is provider-specific.
//
// Examples:
//
//	sqlite:///var/lib/mnemos/mnemos.db
//	sqlite:///tmp/test.db?namespace=mnemos
//	postgres://user:pw@host:5432/cogstack?namespace=mnemos
//	memory://?namespace=mnemos
//
// The matching provider must have been blank-imported by the binary
// (e.g. _ "go.klarlabs.de/mnemos/internal/store/sqlite") so
// that its init() runs before Open is called.
func Open(ctx context.Context, dsn string) (*Conn, error) {
	scheme, _, ok := strings.Cut(dsn, "://")
	if !ok || scheme == "" {
		return nil, fmt.Errorf("store: dsn %q missing scheme://", dsn)
	}
	registryMu.RLock()
	fn, found := registry[scheme]
	registryMu.RUnlock()
	if !found {
		return nil, fmt.Errorf("store: unknown provider %q (registered: %v)",
			scheme, SupportedSchemes())
	}
	return fn(ctx, dsn)
}

// SupportedSchemes returns the registered scheme names in sorted
// order. Useful for error messages and CLI help.
func SupportedSchemes() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]string, 0, len(registry))
	for k := range registry {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
