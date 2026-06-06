# ADR 0001: Multi-Backend Storage with Pluggable Providers

- **Status:** Accepted
- **Implemented:** 2026-04-29 (v0.11.0)
- **Date:** 2026-04-27
- **Deciders:** Felix Geelhaar
- **Scope:** Mnemos primarily; contract intended to be mirrored across the
  cognitive stack (Chronos, Praxis, Nous).

## Context

Mnemos today supports a single persistence backend: SQLite, wired directly
into the engine via `internal/store/sqlite/`. This is a reasonable default
for a local-first evidence layer, but it forecloses two real deployment
shapes that are now in scope:

1. **In-process / memory backend** — needed for fast tests, ephemeral MCP
   demos, and embedding Mnemos as a library inside Nous (the coordination
   layer in the cognitive stack).
2. **Shared-cluster / multi-tenant backend** — a company running the full
   cognitive stack (Mnemos + Chronos + Praxis + Nous) wants to point all
   four tools at one database server while keeping each tool's data
   logically isolated.

Chronos has already taken a first step toward (2) with a `memory | sqlite |
postgres` factory in `internal/store/store.go`. That pattern is sound but
is hard-coded to a switch statement, has no namespace isolation, and
treats the provider list as closed. Before extending the pattern across
four repos we want to commit to a single contract.

The constraint stated by the user: **do not fix on Postgres**. The contract
must accommodate any reasonable persistence provider — relational, document,
embedded, in-memory — and the isolation primitive must translate to each.

## Decision

We will introduce a stack-wide storage contract built on three pillars:

1. **A driver registry** — providers register themselves; the factory
   dispatches by URL scheme rather than a hard-coded switch.
2. **A `namespace` parameter** — a single isolation primitive each provider
   translates into its native mechanism (Postgres schema, MySQL database,
   SQLite file, in-memory map prefix, etc.).
3. **Capability interfaces** — optional features (vector search, full-text
   search, transactions) are advertised via interfaces; engines type-assert
   and fall back when a provider does not implement them.

This contract applies to all four tools in the cognitive stack. The shape
of the `Conn` struct (which repositories it bundles) is per-tool; the
factory pattern, URL convention, and capability negotiation are shared.

## Detailed Design

### 1. Driver Registry

Each provider lives in its own subpackage and registers a factory function
in `init()`:

```go
// internal/store/store.go
package store

type OpenFunc func(ctx context.Context, dsn string) (*Conn, error)

var registry = map[string]OpenFunc{}

func Register(scheme string, fn OpenFunc) {
    if _, dup := registry[scheme]; dup {
        panic("store: duplicate provider " + scheme)
    }
    registry[scheme] = fn
}

func Open(ctx context.Context, dsn string) (*Conn, error) {
    scheme, _, _ := strings.Cut(dsn, "://")
    fn, ok := registry[scheme]
    if !ok {
        return nil, fmt.Errorf("store: unknown provider %q (known: %v)",
            scheme, SupportedSchemes())
    }
    return fn(ctx, dsn)
}
```

Providers self-register:

```go
// internal/store/postgres/postgres.go
func init() { store.Register("postgres", Open) }
func init() { store.Register("postgresql", Open) }  // alias
```

A consumer never imports a specific backend; they import `internal/store`
and the wanted providers as blank-imported side effects in `cmd/mnemos`:

```go
import (
    _ "go.klarlabs.de/mnemos/internal/store/memory"
    _ "go.klarlabs.de/mnemos/internal/store/sqlite"
    _ "go.klarlabs.de/mnemos/internal/store/postgres"
)
```

Builds that want to drop a provider (smaller binary, no Postgres driver)
simply omit the blank import. Third-party providers register the same way.

### 2. Connection String Convention

A DSN is a URL. The scheme picks the provider; query parameters carry
provider-specific config; **the `namespace` query parameter is the
universal isolation primitive**.

```
memory://?namespace=mnemos
sqlite:///var/lib/mnemos/mnemos.db
sqlite:///var/lib/cogstack.db?namespace=mnemos
postgres://user:pw@host:5432/cogstack?namespace=mnemos
postgresql://user:pw@host/cogstack?sslmode=require&namespace=mnemos
mysql://user:pw@host:3306/?namespace=mnemos
```

If `namespace` is omitted it defaults to the tool's name (`mnemos`,
`chronos`, etc.). The default makes single-tenant local use frictionless;
the explicit form makes shared-cluster use unambiguous.

### 3. Namespace Translation per Provider

Each provider owns the translation from `namespace` to its native isolation
mechanism. The contract is: **after `Open` returns, all reads and writes
issued through the returned `Conn` are confined to the namespace.**

| Provider       | Translation                                                  |
| -------------- | ------------------------------------------------------------ |
| `memory`       | Map key prefix (cheap, no real isolation across processes).  |
| `sqlite`       | Distinct file per namespace, *or* `ATTACH DATABASE` aliasing.|
| `postgres`     | `CREATE SCHEMA IF NOT EXISTS <ns>; SET search_path TO <ns>`. |
| `mysql`        | Distinct database per namespace (MySQL has no schemas).      |
| `mongo` (T3)   | Database name = namespace, or collection prefix.             |
| `duckdb` (T3)  | Same as SQLite (distinct file or `ATTACH`).                  |

Namespace identifiers are validated against `^[a-z][a-z0-9_]{0,62}$` to
keep them safe across all dialects without quoting.

### 4. Capability Interfaces

Not every provider can do FTS or vector search. Rather than a lowest-common-
denominator schema, we keep the existing pattern from
`internal/ports/interfaces.go:55-67` (where the query engine type-asserts a
`TextSearcher`) and generalise it:

```go
// internal/ports/interfaces.go
type TextSearcher interface {
    SearchByText(ctx context.Context, query string, limit int) ([]TextHit, error)
}

type VectorSearcher interface {
    SearchByVector(ctx context.Context, q []float32, limit int) ([]VectorHit, error)
}

type Transactional interface {
    InTx(ctx context.Context, fn func(context.Context) error) error
}
```

Repositories implement what they can; engines type-assert and fall back.
This is exactly how Mnemos already degrades cosine → token overlap when no
embeddings are present, formalised.

| Capability     | postgres   | sqlite       | mysql      | memory       |
| -------------- | ---------- | ------------ | ---------- | ------------ |
| TextSearcher   | `tsvector` | FTS5         | `FULLTEXT` | tokenised scan |
| VectorSearcher | `pgvector` | `sqlite-vss` | (none → fallback) | brute cosine |
| Transactional  | yes        | yes          | yes        | best-effort  |

### 5. Migrations

Each provider owns its migration set under `internal/store/<provider>/migrations/`.
Migrations run against the configured namespace at startup (`Open` performs
both connectivity check and migration). The schema-version table lives
inside the namespace, so two tools in the same Postgres database track
their migrations independently.

### 6. The `Conn` Struct (per tool)

Each tool's `internal/store/store.go` defines the `Conn` shape it needs.
For Mnemos, current ports suggest:

```go
type Conn struct {
    Events        ports.EventRepository
    Claims        ports.ClaimRepository
    Relationships ports.RelationshipRepository
    Embeddings    ports.EmbeddingRepository
    Entities      ports.EntityRepository
    Agents        ports.AgentRepository
    Users         ports.UserRepository
    RevokedTokens ports.RevokedTokenRepository
    Jobs          ports.CompilationJobRepository

    close func() error
}
```

Provider subpackages return their own concrete `Conn` types; the top-level
factory adapts them into the port-typed view. The shape is per-tool but the
*pattern* is shared.

## Provider Tiers

To bound the v1 commitment without baking in single-provider assumptions:

- **Tier 1 (shipped in v1):** `memory`, `sqlite`, `postgres`.
  - `memory` first — it forces port purity and unblocks tests.
  - `sqlite` is the existing implementation, repackaged behind the registry.
  - `postgres` validates the shared-cluster story end-to-end.
- **Tier 2 (design-validated, not built in v1):** `mysql`, `mongo`.
  - We will not ship these, but we will sanity-check the contract against
    them before locking it. If it doesn't work for MySQL (no schemas, no
    FTS5, no `pgvector`), the contract is wrong.
- **Tier 3 (extension only):** anything else (`duckdb`, cloud KVs, custom).
  - No core changes required; community / future-self adds via the registry.

## Consequences

**Positive:**

- Mnemos becomes embeddable in-process (Nous) without spinning up a DB.
- A company can run one Postgres for the whole cognitive stack, with each
  tool isolated to its own schema, governed by per-schema permissions.
- The provider list is open: new backends ship without core changes.
- The contract is consistent across all four tools, simplifying onboarding
  and operations.

**Negative / risks:**

- More surface area to test (provider matrix in CI).
- Capability negotiation is more subtle than a uniform schema; engines must
  be careful to fall back gracefully and not assume FTS or vector search.
- SQLite's `namespace` semantics (distinct file vs. `ATTACH`) are awkward —
  the cognitive stack's "shared DB" story is genuinely Postgres/MySQL-flavoured.
  We accept that local SQLite deployments will tend to be one-tool-per-file.

**Migration path for current Mnemos users:**

- Existing `MNEMOS_DB_PATH=/path/to/mnemos.db` continues to work, mapped
  internally to `sqlite:///path/to/mnemos.db`.
- New `MNEMOS_DB_URL` takes precedence when set.
- No data migration required; SQLite repositories are repackaged, not
  rewritten.

## Alternatives Considered

**1. Hard-coded switch statement (Chronos's current approach).**
Rejected: closed provider list, every new backend touches core, doesn't
generalise across four repos.

**2. Table-name prefixes (`mnemos_events`, `chronos_insights`).**
Rejected: invasive across every SQL file and every sqlc query, breaks
sqlc's static analysis, ugly. Schemas (Postgres) and databases (MySQL)
are the idiomatic answer.

**3. Separate databases per tool on a shared cluster.**
Rejected: violates the user's "same DB" framing; cross-tool queries
(Nous reading both Mnemos and Chronos) require FDW or app-level joins.
The `namespace` model lets each tool stay logically isolated while still
sharing a database when the provider supports schemas.

**4. ORM-driven abstraction (GORM, ent, sqlx).**
Rejected: Mnemos uses sqlc for type-safe queries against SQLite-specific
features (FTS5, BLOB embeddings). An ORM would either flatten those
features or duplicate them poorly. The capability-interface approach
preserves provider-native strengths.

## Open Questions

- **Vector search in SQLite without `sqlite-vss`?** v1 SQLite likely keeps
  the current binary-BLOB + Go-side cosine. `sqlite-vss` is a CGO dep and
  the project pins `CGO_ENABLED=0`.
- **Transaction scope across repositories.** The `Transactional` interface
  needs concrete semantics: does a tx span all repos in a `Conn`? For
  Postgres yes (single connection), for memory we fake it best-effort.
- **Cross-tool queries.** If Nous wants to join Mnemos claims with Chronos
  signals at the SQL level, both must be in the same Postgres instance and
  Nous needs read grants on both schemas. ADR for the Nous side will pick
  this up.

## Related Work

- Chronos `internal/store/store.go` — initial multi-backend pattern.
- Mnemos `internal/ports/interfaces.go:55-67` — `TextSearcher` capability
  type-assert (the precedent generalised here).
- Go `database/sql` driver registry — direct inspiration for the registry
  pattern.

## Implementation Plan (separate from this ADR)

This ADR commits to the contract, not the schedule. Suggested ordering:

1. Land registry + `memory` provider in Mnemos. No behaviour change for
   existing users; tests start using `memory` instead of temp SQLite files.
2. Repackage existing SQLite store behind the registry.
3. Add Postgres provider with `pgvector` and `tsvector`. CI matrix gains a
   Postgres job.
4. Mirror the contract into Chronos. Praxis and Nous adopt at build time.
5. Sanity-check against MySQL (Tier 2) — design pass only, no shipping code.
