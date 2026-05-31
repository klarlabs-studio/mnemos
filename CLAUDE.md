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
  - Core: `ingest`, `extract` (supports `--run`), `relate`, `process`, `query`, `metrics`, `verify`
  - Phase 2: `action record/list`, `outcome record/list`
  - Phase 3: `synthesize`, `lessons [--service|--trigger]`
  - Phase 5: `decision record/list/show/attach-outcome`
  - Phase 6: `playbook synthesize/list/show/<trigger>`
  - Phase 7: `export --kind=lesson|playbook`, `import <file.md>`, `history --kind=lesson|playbook`
- `mnemos mcp` — MCP server exposing `query_knowledge`, `process_text`, `knowledge_metrics`, `record_action`, `record_outcome`, `synthesize_lessons`, `query_lessons`, `record_decision`, `query_decisions`, `query_playbook`, `synthesize_playbooks`, `list_claims`, `list_decisions`, `list_contradictions`, `watch_file`, `ingest_git_log`, `ingest_git_prs` over stdio
- `mnemos serve [--grpc-port N]` — HTTP REST + optional gRPC API alongside

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

## Environment Variables

```
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
