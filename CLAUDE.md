# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What is Mnemos?

Mnemos is a local-first evidence layer that grounds AI in truth. It extracts structured claims from text, detects contradictions between claims, and returns query answers with full evidence traceability.

Pipeline: `ingest → extract → relate → query`

## Build & Development Commands

```bash
make check          # fmt + lint + test + build (CI equivalent)
make build          # Build bin/mnemos
make install        # Install mnemos to $GOPATH/bin
make test           # Run all tests (includes 133 eval cases across 13 suites under data/eval/*.yaml)
make fmt            # go fmt ./...
make lint           # go vet + golangci-lint
make sqlc           # Regenerate sqlc query code from sql/sqlite/
make release-check  # Validate .goreleaser.yaml
```

Run a single test:
```bash
go test -run TestName ./internal/extract/
go test -race -count=1 ./internal/store/sqlite/
```

## Architecture

### Ports & Adapters

Core interfaces live in `internal/ports/interfaces.go`. All repository methods accept `context.Context` as the first parameter for timeout/cancellation propagation:
- `EventRepository`, `ClaimRepository`, `RelationshipRepository`, `EmbeddingRepository` — storage ports
- `ExtractionEngine` — extract claims from events
- `QueryEngine` — answer questions with evidence

All implementations are behind these interfaces, enabling clean testing and provider swapping.

### Domain Model (`internal/domain/`)

- **Event** — immutable, append-only knowledge unit (tagged with `run_id` for isolation)
- **Claim** — derived assertion with type (fact/hypothesis/decision), confidence (0–1), status (active/contested/deprecated), per-claim `LastVerified` / `VerifyCount` / `HalfLifeDays`, optional `Scope{Service, Env, Team}`
- **ClaimEvidence** — links claims to source events (≥1 per claim)
- **Relationship** — claim-to-claim edge: `supports`, `contradicts`, plus the causal+outcome family from Phase 1 (`causes`, `caused_by`, `action_of`, `outcome_of`, `validates`, `refutes`, `derived_from`)
- **EmbeddingRecord** — stored vector embedding with metadata
- **Action** — recorded operational change (kind, subject, actor, at, run_id, metadata)
- **Outcome** — observed result of an Action (action_id, result, metrics map, source push|pull:*)
- **Lesson** — synthesised operational truth (statement, scope, evidence []ActionID, confidence, trigger, kind, source synthesize|human)
- **Decision** — agent decision audit record (statement, plan, reasoning, risk_level, beliefs []ClaimID, alternatives, outcome_id)
- **Playbook** — Agent-ready response (trigger, scope, steps []PlaybookStep, derived_from_lessons, confidence). Consumers run them through whatever execution layer they own.
- **Scope** — multi-tenant filter primitive: {Service, Env, Team} (LessonScope is an alias kept for back-compat)
- **Answer** — query result bundling claims, contradictions, timeline, hop distances, claim provenance, and `StaleClaimIDs`

All domain types have `Validate()` methods. Contradictions are first-class concepts, not afterthoughts.

### Package Responsibilities

| Package | Role |
|---------|------|
| `internal/ingest/` | Multi-format input → events |
| `internal/parser/` | Input normalization |
| `internal/extract/` | Rule-based and LLM-powered claim extraction |
| `internal/relate/` | Pairwise relationship detection with stop-word filtering and overlap thresholds; `DetectCausal` heuristic + `DetectCausalLLM` LLM augmentation for borderline pairs |
| `internal/query/` | Question answering with ranking (embeddings or token overlap fallback); scope filter; stale-claim surfacing |
| `internal/embedding/` | Vector embedding client abstraction (openai, gemini, ollama, openai-compat) |
| `internal/llm/` | LLM client abstraction (anthropic, openai, gemini, ollama, openai-compat) |
| `internal/store/sqlite/` | SQLite repositories with foreign key enforcement; sqlc-generated queries in `sqlcgen/` |
| `internal/pipeline/` | Shared orchestration: `Extractor`, `PersistArtifacts`, `GenerateEmbeddings`, `GenerateClaimEmbeddings` (used by both CLI and MCP) |
| `internal/workflow/` | Job runner with statekit state machine, retry, and timeout |
| `internal/synthesize/` | Cluster action→outcome chains into Lessons; cluster Lessons by trigger into Playbooks |
| `internal/markdown/` | Round-trip Lessons + Playbooks to YAML-frontmatter markdown for the human-editable layer |
| `internal/adapters/outcomes/` | Pull-based Outcome sources; first impl: Prometheus instant-query adapter |

### Entrypoints

- `cmd/mnemos/` — CLI subcommands:
  - Setup: `init` (system-detecting one-command bootstrap: brain + config + MCP registration + Claude Code hooks; `--project`, `--dry-run`, `--yes`, `--no-hooks`, `--hooks`, `--no-mcp`, `--db <dsn>`, `--force`; `--url <endpoint> [--token]` registers a remote HTTP MCP server for a hosted brain **and** installs recall/brief/capture hooks that call the hosted brain over REST (writing the URL/token to the 0600 config, never inlined into Claude settings); `--service [--out]` scaffolds a `mnemos serve` deployment bundle via `scaffoldService`), `setup` (minimal MCP-only registration), `hook <recall|brief|capture>` (internal Claude Code hook handlers, read event JSON on stdin, fail-open). **Hosted vs local hooks:** when `MNEMOS_URL` is set (hosted, via the `server:` config block → `MNEMOS_URL`/`MNEMOS_TOKEN`), the hooks route through the `client` package to REST — recall→`GET /v1/search`, brief→`GET /v1/metrics`, capture→`POST /v1/process` — with a bearer token; otherwise they open the local store (`hostedConfigured()`/`hostedClient()` in `hook.go`). `/v1/process` (`makeProcessHandler`) is the REST analogue of the `process_text` MCP tool — it runs the full ingest→extract→relate pipeline server-side; `use_llm`/`use_embeddings` are tri-state (`*bool`), so an omitting hook lets the server default to its own LLM config instead of hard-failing. `init --db` accepts any store DSN (sqlite/postgres/mysql/libsql); it opens the DSN first (connectivity check + schema bootstrap) and fails fast if unreachable (unless `--force`). Credential hygiene: a credential-free DSN (local SQLite/memory) is inlined into Claude's config; a networked DSN with a password is written to the 0600 config file (via `config.SetValues`) and discovered by the server/hooks, never inlined into `~/.claude.json`/`settings.json`. Setup logic lives in `init.go`/`setup.go`/`detect.go`/`hooks_install.go`; the same plan/apply engine backs the `configure_environment` MCP tool.
  - Core: `ingest`, `extract` (supports `--run`), `relate`, `process`, `query`, `metrics`, `verify`
  - Phase 2: `action record/list`, `outcome record/list`
  - Phase 3: `synthesize`, `lessons [--service|--trigger]`
  - Phase 5: `decision record/list/show/attach-outcome`
  - Phase 6: `playbook synthesize/list/show/<trigger>`
  - Phase 7: `export --kind=lesson|playbook`, `import <file.md>`, `history --kind=lesson|playbook`
- `mnemos mcp` — MCP server exposing `query_knowledge`, `process_text`, `knowledge_metrics`, `configure_environment`, `record_action`, `record_outcome`, `synthesize_lessons`, `query_lessons`, `record_decision`, `query_decisions`, `query_playbook`, `synthesize_playbooks`, `list_claims`, `list_decisions`, `list_contradictions`, `watch_file`, `ingest_git_log`, `ingest_git_prs`. Default transport is stdio (local Claude Code). `mnemos mcp --http <addr> [--auth|--no-auth]` serves the identical tool set over Streamable HTTP so remote MCP clients can reach a hosted brain — `--auth` (default for `--http`) gates every request behind a bearer JWT via the mcp-go `transport.WithAuthorize` hook (`serveMCPHTTP` in `cmd/mnemos/mcp_http.go`, reusing the same `auth.Verifier` as `serve`'s gRPC); TLS from `MNEMOS_TLS_*`. Per-request identity: `transport.WithRequestContextFn` stashes the validated `*auth.Claims` in each request's context (`mcp_identity.go`); write handlers attribute `created_by` to the token subject via `mcpActorFor(ctx, fallback)` (through both the direct and axi-kernel executor paths), and run-scoped reads enforce the token's run allowlist via `mcpRunAllowed`. Stdio has no claims, so both fall back to the process actor / no restriction — unchanged.

**Multi-tenant per server (ADR 0007, Phase 1):** `mcp --http --require-tenant` serves many tenants from one process. The JWT carries a `tnt` claim (`Claims.Tenant`; mint with `token issue --tenant` / `agent token issue --tenant`). In this mode the auth hook **denies** a token without a valid tenant (fail-closed), `withTenant` stashes the effective tenant, and `resolveDSNForContext` scopes the request per backend; the cognitive path routes through a per-tenant `memFacade.Tenant()` cache. **Two isolation models (`internal/store.TenancyModeForDSN`):** Postgres uses **row-level** isolation — `resolveDSNForContext` appends `?tenant=<id>`, the provider pins the `mnemos.tenant` GUC, and RLS isolates the request (needs a non-superuser role — superusers bypass RLS). sqlite / mysql / **local** libSQL use **namespace-per-tenant** physical isolation — `resolveDSNForContext` sets `?namespace=<derived>` where the derived namespace is `store.TenantNamespace(tenant)` (a `t_`-prefixed, hash-suffixed, charset-safe mapping since the tenant charset is broader than the namespace charset), and the provider partitions into a separate schema/database/file created on first open. Backends that cannot isolate — `memory://` and **remote** libSQL (it discards `namespace`) — report `TenancyNone` and `--require-tenant` refuses to start (fail-closed). The reserved default namespace `mnemos` is the namespace-model analogue of the reserved `__default__` tenant: a derived tenant namespace can never equal it. Guardrails: `TestSQLite_CrossTenantIsolation`, `TestLibsql_LocalCrossTenantIsolation`, `TestMySQL_CrossTenantIsolation`, `TestGRPC_CrossTenantIsolation_SQLite`, `TestTenant_SQLiteNamespaceIsolation`, plus the Postgres RLS suite. **Connection reuse:** the MCP server caches one read pool per tenant-scoped DSN (`enableConnCache` in `dsn.go`; keyed by the resolved DSN incl. `?tenant=`, so tenants never share a pool and RLS is unchanged) — reads reuse a pooled conn instead of opening one per request. Writes (`openWriter`) stay per-request. **Phase 2 (opt-in, `MNEMOS_PG_SHARED_POOL`/`db.shared_pool`):** one shared Postgres pool serves all tenants — a per-request `*sql.Conn` is checked out, pinned with `SET mnemos.tenant`, and its Closer runs `RESET mnemos.tenant` before releasing it (fail-closed if ever reused unset). Repos take a `pgQuerier` interface (`*sql.DB` for the default per-tenant-pool path, `*sql.Conn` for shared); `openSharedTenantConn`/`getSharedPool` in `internal/store/postgres/postgres.go`. The cmd conn-cache is disabled in this mode (it would pin one conn per tenant). Default stays the per-tenant-pool model. Verified by `TestPostgres_CrossTenantIsolation` (both modes, skips on an RLS-bypassing role) — the ADR-0007 guardrail — plus an MCP e2e showing 30 mixed-tenant requests share ~2 connections.
- `mnemos serve [--grpc-port N]` — REST + gRPC API service (the "as a service" surface; JWT auth, TLS/mTLS, multi-tenant namespace/RLS). `serve --require-tenant` (ADR 0007) makes every REST **and** gRPC request scope to its token's `tnt` claim via a per-request tenant connection (Postgres RLS or namespace-per-tenant for sqlite/mysql/local libSQL — see the MCP note above): REST auths reads too and a `tenantScopeMiddleware` opens the tenant conn (handlers resolve it through `scopedConn/scopedWriter/scopedMem` in `serve_tenant.go`); gRPC's interceptor opens it and methods resolve via `connFor/writerFor/memFor` (`internal/server/grpc`). Secure by default (v0.85.1+): every data endpoint requires a JWT (reads too); only bare-liveness/static infra endpoints (`/health`, `/healthz`, `/`, `/app`) stay unauthenticated. `/internal/metrics` is **authenticated by default** (opt out with `serve --metrics-public`; it is excluded from the `--public-reads` bypass) and the version/DB-probe report lives behind auth at `/internal/ready`. Anonymous reads are an explicit opt-in (`serve --public-reads`); `mcp --http` defaults to `--auth`. Guarded by `TestREST_ReadsRequireAuthByDefault`, `TestAuthenticate_ReadsRequireTokenByDefault`, `TestPostgres_CrossTenantIsolation`, `TestGRPC_CrossTenantIsolation`, and `TestGRPC_CrossTenantIsolation_SQLite`. Distinct from the MCP surface; API parity across MCP/HTTP/gRPC is enforced by `TestAPISurfaceParity`.

### Internal Libraries (owned by same author)

- **bolt** — structured JSON logging
- **fortify** — retry with exponential backoff and jitter
- **statekit** — declarative state machine (enforces job status transitions: pending → running → ... → completed/failed)
- **mcp-go** — MCP protocol server framework (stdio transport)

### Key Design Decisions

- **Event-sourced core**: events are immutable source of truth; claims/relationships are derived
- **Graceful degradation**: LLM extraction falls back to rule-based on failure; query falls back from cosine similarity to token overlap when no embeddings; grounded generation falls back to template answers when no LLM configured
- **Claim-level embeddings**: both events and claims are embedded when `--embed` is set; claims are reranked by cosine similarity at query time
- **Incremental relationship detection**: new claims are compared against existing knowledge base via `DetectIncremental`, not just within the current batch
- **Grounded generation**: `query --llm` uses the LLM to synthesize answers from retrieved claims with inline citations
- **LLM cache**: extraction results cached in `data/cache/llm-extraction/<hash>.json` (default 1 GiB cap, oldest-mtime eviction; `MNEMOS_LLM_CACHE_MAX_BYTES` overrides). Prompt version tracked at `internal/extract/prompt.go:PromptVersion` (currently `v1.4`, includes entity extraction, junk filters, richer few-shots).
- **Run isolation**: `run_id` on events enables scoped queries and extraction across ingestion runs
- **Contested detection**: happens during rule-based extraction (high token overlap + same polarity), separate from relationship detection
- **CGO_ENABLED=0**: builds are pure Go via modernc.org/sqlite (no C compiler needed)
- **XDG-compliant storage**: database defaults to `~/.local/share/mnemos/mnemos.db`, overridable via `MNEMOS_DB_URL` (any registered backend)
- **Pluggable backends (ADR 0001)**: `internal/store` is a URL-scheme dispatched registry. Providers self-register from init():
  - `sqlite://` (default, modernc.org/sqlite, FTS5)
  - `memory://` (in-process)
  - `postgres://` / `postgresql://` (pgx/v5/stdlib, namespace = Postgres schema, integration tests gated on `TEST_POSTGRES_DSN`). Verified Postgres-wire-compatible engines work through this provider unchanged: **CockroachDB**, **YugabyteDB**, **Neon serverless**, **Crunchy Bridge**, **TimescaleDB**, **AlloyDB Omni** — point `MNEMOS_DB_URL` at any of them.
  - `mysql://` / `mariadb://` (go-sql-driver/mysql, namespace = MySQL database, integration tests gated on `TEST_MYSQL_DSN`). MySQL-wire-compatible engines also work through this provider: **PlanetScale**, **TiDB**, **MariaDB**, **Vitess**.
  - `libsql://` (tursodatabase/libsql-client-go, pure-Go, supports both Turso remote URLs and local file mode). libSQL is wire-compatible with SQLite so the SQLite schema and repository implementations are reused unchanged.

  `cmd/mnemos` blank-imports providers it wants to support. When `MNEMOS_DB_URL` is unset, the resolver walks up from CWD looking for `.mnemos/mnemos.db` and falls back to the XDG global default.

## Database

SQLite with schema at `sql/sqlite/schema.sql`. Tables: `events`, `claims`, `claim_evidence`, `relationships`, `compilation_jobs`, `embeddings`. Foreign keys are enforced via `PRAGMA foreign_keys = ON`.

After modifying SQL queries in `sql/sqlite/query/*.sql`, run `make sqlc` to regenerate `internal/store/sqlite/sqlcgen/`.

Embeddings are stored as little-endian float32 binary BLOBs, encoded/decoded via `EncodeVector`/`DecodeVector` in `internal/embedding/`.

### Adding a new Postgres table — RLS gotcha (per-tenant isolation)

**A new Postgres table LEAKS across tenants unless you register it in the ADR-0007 RLS `scoped` array in `internal/store/postgres/schema.sql` — not just its `CREATE TABLE`.** Per-tenant isolation within a namespace is applied by the `DO $mnemos_rls$` block, which iterates a `scoped text[]` list and, for each table, adds a `tenant` column (defaulted from the `mnemos.tenant` GUC), a tenant index, and a `FORCE ROW LEVEL SECURITY` `tenant_isolation` policy. A table absent from that list gets **no** tenant column and **no** policy, so every tenant sees every row. When you add a table used across tenants (side tables like `working_memory_blocks`, `claim_expectations` did this), add its name to `scoped`. Auth-infra tables (`users`, `revoked_tokens`) are deliberately excluded. Verify with the live-Postgres integration test (a fresh namespace should isolate).

## Configuration (env vars + YAML file)

Every setting below can come from a `MNEMOS_*` environment variable **or** a
YAML config file — mix freely. Precedence is 12-factor: an exported env var
always overrides the file; the file only fills gaps. The loader
(`internal/config`) discovers the file via `--config <path>` → `MNEMOS_CONFIG`
→ nearest `.mnemos/mnemos.yaml` (walked up from CWD) → `~/.config/mnemos/config.yaml`,
then hydrates unset `MNEMOS_*` vars in `main()` before any package reads them —
so all the `os.Getenv("MNEMOS_...")` call sites stay unchanged. An explicit
(`--config`/`MNEMOS_CONFIG`) missing or malformed file is fatal (exit `ExitConfig`);
implicit-discovery misses are silent. Unknown YAML keys are rejected. Each YAML
leaf maps to exactly one env var — see `Config.EnvOverrides` and
`mnemos.example.yaml`.

## Environment Variables

```
MNEMOS_CONFIG          # Explicit path to the YAML config file (overridden by --config).
MNEMOS_DB_URL          # Storage DSN; any registered backend (sqlite://, memory://, postgres://, mysql://, libsql://). When unset: ./.mnemos/mnemos.db (walked up) → ~/.local/share/mnemos/mnemos.db.
MNEMOS_LLM_PROVIDER    # anthropic, openai, gemini, ollama, openai-compat
MNEMOS_LLM_API_KEY     # API key for cloud providers
MNEMOS_LLM_MODEL       # Model name (e.g., llama3.2)
MNEMOS_LLM_BASE_URL    # Custom endpoint (ollama, openai-compat)
MNEMOS_EMBED_PROVIDER  # Falls back to LLM_PROVIDER if unset
MNEMOS_EMBED_API_KEY   # Falls back to LLM_API_KEY if unset
MNEMOS_EMBED_MODEL     # Embedding model name
MNEMOS_EMBED_BASE_URL  # Embedding endpoint
MNEMOS_LLM_CACHE_MAX_BYTES  # LLM extraction cache cap (default 1 GiB; 0 disables eviction)
MNEMOS_CAPTURE_TIMEOUT      # SessionEnd capture budget (Go duration, default 4m). Ceiling, not a reservation; sized for a slow local model. `mnemos init` writes a matching Claude Code hook timeout — re-run it after raising this.
MNEMOS_DB_MAX_CONNS         # Postgres/MySQL pool MaxOpenConns (default 25)
MNEMOS_DB_MAX_IDLE_CONNS    # Postgres/MySQL pool MaxIdleConns (default 5)
MNEMOS_DB_CONN_MAX_LIFETIME # Pool ConnMaxLifetime, e.g. "30m" (default 30m)
MNEMOS_TELEMETRY_OPTIN      # Truthy ("1"/"true"/"yes") to opt in to anonymized usage payload (default off)
MNEMOS_TELEMETRY_ENDPOINT   # POST destination for `mnemos metrics --workspace --telemetry-send` (default unset = no send)
```

Note: Anthropic has no embedding API — use a separate provider for embeddings.

## CI

GitHub Actions (`.github/workflows/ci.yml`): format check → vet → golangci-lint v2.1 → `go test -race -count=1 ./...` → `make build` → `goreleaser check`. Runs on push/PR to main.

Releases via GoReleaser (`.goreleaser.yaml`): builds both binaries for darwin/linux/windows × amd64/arm64.
