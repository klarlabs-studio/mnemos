# Changelog

All notable changes to Mnemos are documented here. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Releases are tagged and published via GoReleaser; this file is the human-readable summary.

## [Unreleased]

### Added
- **`Memory.RememberClaim(ClaimItem)`** — third input mode for the
  library API. Agent runtimes that have already derived structured
  claims (with their own model or from parsed structured data) can
  hand them to Mnemos verbatim, bypassing the extraction pipeline.
  Companions to the existing rule-based and LLM-driven text-ingestion
  paths. See [`docs/library.md`](docs/library.md) "Three input modes"
  + `ExampleMemory_rememberClaim` in `example_test.go`.

## [0.17.0] — 2026-05-31

Cognitive-stack simplification + embeddable library release. Mnemos
becomes a first-class in-process Go library while keeping the CLI /
MCP / HTTP surfaces unchanged. Chronos is bundled by default so
temporal memory works out of the box. Four ADRs land that close the
loop on retiring the dedicated reasoning / action / orchestration
repos (nous / praxis / olymp) in favour of agent runtimes plus a
small `decisionkit` library.

### Added
- **Root `mnemos` package** — `mnemos.New(opts...)` returns a
  `Memory` interface with `Remember(ctx, Item)`, `Recall(ctx, Query)`,
  `RememberEvent(ctx, Event)`, `Timeline(ctx, TimelineQuery)`, and
  `Close()`. Additive: every existing CLI / MCP / HTTP surface is
  unaffected. See [`docs/library.md`](docs/library.md).
- **`mnemos/providers/` subpackage** — framework-neutral
  `TextGenerator` and `Embedder` interfaces. Agent runtimes (Claude
  Code, Codex, Hermes, Nomi, OpenClaw, NanoClaw, ...) implement these
  as thin adapters around their own provider clients. Internal
  `llm.Client` / `embedding.Client` are wrapped behind the public
  surface.
- **Three operating modes via Option builders** —
  `WithPassiveMode()` (no LLM, rule-based extraction + token-overlap
  ranking, zero env vars), `WithSharedProvider(tg, embedder)` (agent
  runtime supplies the model), `WithEnhancedMode(cfg)` (dedicated
  provider config).
- **Chronos bundled by default** — `mnemos.New()` boots an in-process
  Chronos engine (memory storage) so `RememberEvent` + `Timeline`
  work out of the box. Power users supply their own configured
  engine via `WithChronos(*embed.Engine)` for durable storage / custom
  detectors. Requires `github.com/felixgeelhaar/chronos v0.6.0+`.
- **Temporal MCP tools** — `remember_event`, `timeline_query`, and
  `recall_at_time` (`query.AnswerOptions.AsOf`) join the existing
  MCP surface; documented in the API parity matrix as MCP-only.
- **Storage option** — `WithStorage(dsn)` overrides DSN resolution
  for callers that don't want the default `MNEMOS_DB_URL > project
  .mnemos > XDG default` precedence.
- **Actor option** — `WithActor(userID)` overrides the actor stamp
  on writes (default reads `MNEMOS_USER_ID` env, falls back to
  `domain.SystemUser`).
- **Godoc examples** — `Example_passive`, `Example_sharedProvider`,
  `Example_enhanced`, `Example_withChronos` in `example_test.go`.

### Documentation
- **ADR 0003 — Archive Olymp** (zero Go importers; orchestration patterns
  preserved at the `v0.1.5-final` tag).
- **ADR 0004 — Extract decisionkit** (risk + intervention engines lifted
  from `nous/internal/` into a standalone
  [decisionkit](https://github.com/felixgeelhaar/decisionkit) module at
  v0.1.0; Obvia and future programmatic consumers depend on it directly).
- **ADR 0005 — Archive Nous** (only consumer was Olymp; LLM extraction
  moves to agent runtimes; risk + intervention survive in decisionkit).
- **ADR 0006 — Archive Praxis** (vendor handlers are the wrong shape for
  agent-driven workflows; Obvia inlines orchestration primitives;
  agents reach vendors via MCP).
- **`docs/library.md`** — three-mode walkthrough with copy-pasteable
  examples + Chronos bundling notes + storage backend matrix.

### Changed
- **README** — added an in-process Go library section above the HTTP
  client section; refreshed the Playbook description (no longer
  references Praxis as the executor since Praxis is archived).
- **CLAUDE.md** — same Playbook refresh; Playbook is now described as
  an "agent-ready response" consumers run through whatever execution
  layer they own.

### Removed
- Nothing user-visible. The library is fully additive over v0.16.x;
  CLI / MCP / HTTP surfaces are unchanged.

## [0.16.0] — 2026-05-24

Agent-memory release. Eleven issues land that turn mnemos from a
"claim store + query engine" into the full agent-side memory layer:
LLM-callable tools, semantic recall, reaction loop, bitemporal
queries, audit trail, GDPR cascade delete, federation export.

### Added
- **Embedding-aware semantic search** (#36) —
  `GET /v1/claims?similar_to=<text>&run_id=<scope>` ranks claims by
  cosine similarity against the embedding of the query text. New
  optional port `ports.ClaimSimilaritySearcher` (memory + sqlite +
  postgres + mysql implementations); libsql inherits via sqlite.
  Scoring runs in Go over the existing float32 blob/JSONB format;
  a future pgvector / sqlite-vec swap can push the cosine into SQL
  without touching the port contract. `run_id` is REQUIRED on
  `similar_to` — ranked retrieval is a tenant-leak vector without a
  hard scope gate (fail-closed, not audit-after).
- **MCP memory-management tools** (#41) — `remember` (claim + event
  + evidence link, optional `valid_until`), `forget` (status flips
  to `deprecated`, audit preserved), `update` (rewrite text +
  optional confidence), `search_memory` (semantic recall via #36).
  Registered next to the existing `memory_*` governance tools;
  `parityMatrix` carries them so future drift fails the surface-
  drift test.
- **OpenAI / Anthropic tool-call schemas** (#37) — `client/tools`
  Go package returns ready-to-attach `OpenAITool` /
  `AnthropicTool` definitions for the four memory verbs;
  `client/tools/snapshots/{openai,anthropic}.json` ships the same
  payload as data so non-Go callers can fetch the schemas without
  binding the Go package. The two vendor formats are projected
  from one `definition()` source so they can never drift apart.
- **gRPC `as_of` / `recorded_as_of` parity on `ListClaims`** (#35)
  — Bitemporal time-travel queries reach gRPC: `ListClaimsRequest`
  grows `AsOf` and `RecordedAsOf` `Timestamp` fields, the handler
  wires both into the existing filter loop via
  `IsValidAt` + `CreatedAt` comparison. Non-HTTP callers can now
  ask "what was true on date X" and "what did the store look like
  at moment Y".
- **GDPR Art.17 cascading delete** (#42) —
  `DELETE /v1/claims?run_id=<prefix>` removes claims (and their
  evidence links, embeddings, status history, claim-only
  relationships) plus the events themselves. Per-table counts +
  request_id in the response so compliance audits get concrete
  numbers. Idempotent: a second call returns 200 with zero counts.
  Shared-claim handling preserves a claim linked to multiple runs
  when only one run is wiped.
- **`buf lint` + `buf breaking` CI guard** (#44) — STANDARD ruleset
  minus two opinions (`RPC_RESPONSE_STANDARD_NAME`,
  `RPC_REQUEST_RESPONSE_UNIQUE`) that would force breaking renames.
  `buf breaking` runs WIRE_JSON against main so a JSON-over-HTTP
  rename matters as much as a tag-number change. Companion
  workflow ships in the chronos sister repo.
- **`confidence_components` JSONB on claims** (#39) — decomposes
  the scalar `Confidence` into named contributors (`data_quality`,
  `recency`, `corroboration`, `source_authority`, …). The scalar
  stays the canonical "overall" number for back-compat. Persisted
  as a new column (sqlite + postgres + mysql); schema-migrated
  idempotently via the existing column-add ladder.
- **`POST /v1/claims/{id}/feedback` reaction loop** (#40) — users
  push back ("not helpful") and the signal flows back into the
  claim: scalar `Confidence` decays by `MNEMOS_FEEDBACK_DECAY`
  (default 0.9), the `corroboration` component decays in lockstep,
  consecutive-negative streak is tracked in a new `claim_feedback`
  side table. After `MNEMOS_FEEDBACK_CONTEST_THRESHOLD` (default 3)
  consecutive negatives the status auto-transitions to `contested`.
  Positive feedback resets the streak and bumps `helpful_count`.
  Requires `claims:write` scope.
- **`claim_versions` audit trail + `GET /v1/claims/{id}/history`**
  (#38) — every `Upsert` path appends a snapshot to a new
  `claim_versions` side table (text, confidence, status,
  written_at, written_by). New port
  `ports.ClaimVersionRepository`; sqlite + libsql + memory back it.
  History returns newest-first so a consumer diffing reads
  `versions[0].text` vs `versions[1].text` without re-sorting.
  Required for GDPR Art.17 erasure proof and agent drift analysis.
- **`GET /v1/federation/export`** (#45) — opt-in
  (`MNEMOS_FEDERATION_ENABLED=true`) anonymized playbook export.
  Trigger / statement / steps / confidence / lesson_count
  preserved; playbook id, created_by, derived_from_lessons ids,
  scope tuple, source-tag stripped. Companion to chronos #30.
- **Joint chronos+mnemos integration harness** (#46) —
  `test/integration/docker-compose.yml` stands the stack up;
  `test/integration/smoke_test.go` (//go:build integration) pins
  the cross-talk contracts. Nightly +
  `repository_dispatch[chronos-main-advanced]` CI re-runs the
  smoke against the sister repo's advancing main so version-skew
  bugs surface within 24h.
- **Phase 1 — causal edges** (`feat(relate)`): `causes`, `caused_by`, `action_of`, `outcome_of`, `validates`, `refutes`, `derived_from` extend the relationship graph beyond logical agreement. `relate.DetectCausal` infers from event-time + shared-entity signals; optional `relate.DetectCausalLLM` augments borderline pairs.
- **Phase 2 — actions + outcomes**: `mnemos action record` / `mnemos outcome record`; Prometheus pull adapter (`internal/adapters/outcomes/prometheus.go`) emits Outcomes from PromQL instant queries.
- **Phase 3 — lessons synthesis**: `mnemos synthesize` clusters action→outcome chains into validated Lessons; `mnemos lessons` lists them.
- **Phase 4 — temporal hardening**: per-claim `last_verified`, `verify_count`, `half_life_days`; `mnemos verify` re-confirms; `Answer.StaleClaimIDs` surfaces decay below the trust floor.
- **Phase 5 — decisions**: `mnemos decision record/list/show/attach-outcome` audits agent reasoning with belief claims, alternatives, risk level, and observed outcomes.
- **Phase 6 — playbooks**: `mnemos playbook synthesize/list/show/<trigger>` derives Praxis-ready response steps from Lesson clusters.
- **Phase 7 — markdown round-trip + history**: `mnemos export/import` round-trips Lessons + Playbooks to YAML-frontmatter markdown; `mnemos history` lists snapshots from system-versioned `*_versions` tables.
- **Phase 8 — multi-tenant scope**: `Scope{Service, Env, Team}` on Claims, Lessons, Decisions, Playbooks; `mnemos query --service X --env prod --team Y` filters answers.
- **Polymorphic cross-entity edges**: `entity_relationships` table + `internal/autoedge` package auto-fires `action_of`/`outcome_of`/`validates`/`refutes`/`derived_from` edges. Documented in [ADR-0002](docs/adr/0002-cross-entity-edges-and-outcome-pull-adapters.md).
- **gRPC API expansion**: `feat(grpc): expand API to Phase 2-7 entities` — `List*/Append*` for Actions, Outcomes, Lessons, Decisions, Playbooks, EntityRelationships alongside the v0.12 core surface.
- **API documentation**: `api/openapi.yaml` covers the full HTTP registry surface; `proto/mnemos/v1/mnemos.proto` is the gRPC schema. OpenAPI `bearerAuth` scheme corrected to `JWT` (HS256) — same verifier as gRPC; `MNEMOS_REGISTRY_TOKEN` clarified as client-side only.
- **Glob-pattern run scopes** — `auth.Claims.AllowsRun` accepts `*` (wildcard), exact match, and shell-glob patterns (`prod-*`, `nightly-?-2026`, `release/[0-9]*`). Patterns that fail `path.Match` fall back to exact compare so a malformed glob can't grant unintended access.
- **Agent quotas** — `domain.AgentQuota` (rolling-window write count + token cap). `auth.QuotaTracker` enforces in-memory; `Charge` returns `ErrQuotaExceeded` on overflow. Counters reset on process restart (durable variant deferred).
- **Federated agent sync** — `AgentRepository.Upsert(batch)` shipped on every backend (sqlite + memory + postgres + mysql + libsql via sqlite). Registries can mirror peers' agents alongside events / claims / relationships / embeddings.
- **Bias detection** — `internal/bias` package ships four explainable indicators with operator-tunable thresholds: source concentration, polarity skew, temporal clustering, single-source-of-truth pathology. `Analyse(input, thresholds)` returns a `bias.Report` with per-finding explanation. No auto-action — operators decide what to do.
- **sqlc coverage parity** — moved `users`, `revoked_tokens`, `agents`, and `entity_relationships` from hand-written SQL to sqlc-generated queries (see `sql/sqlite/query/`). Every fixed-shape sqlite query now flows through `internal/store/sqlite/sqlcgen`. Dynamic-filter queries (claim/event search) keep raw SQL because sqlc doesn't model them well.
- **Server-side TLS + mTLS** — `MNEMOS_TLS_CERT_FILE` + `MNEMOS_TLS_KEY_FILE` enable TLS on the HTTP registry and gRPC server. `MNEMOS_MTLS_CLIENT_CA_FILE` upgrades to mutual TLS. Both transports share the same cert; helpers in `cmd/mnemos/serve_tls.go`.
- **Dual-key JWT verifier** — `auth.NewVerifierWithPrevious(active, previous, revoked)` accepts tokens signed under either active or previous secret, supporting zero-downtime key rotation. Resolution: `MNEMOS_JWT_PREV_SECRET` env, then `<auth-dir>/jwt-secret.previous` file. `serve` wires this automatically; rotation is a copy-and-restart procedure documented in `SECURITY.md`.
- **In-tree security baseline** — `make nox-scan` invokes [`nox`](https://github.com/felixgeelhaar/nox) v0.7.0; `findings.json` baseline committed in-tree (3 856 findings, all `Status: "baselined"`). New scans diff against the baseline; unbaselined findings fail CI. Categories tracked in `SECURITY.md`.
- `SECURITY.md` — auth surfaces, threat model, container hardening, secret management, known gaps.
- `CHANGELOG.md`.

### Fixed
- `fix(mysql)`: backtick-quote `trigger` reserved word in schema and DML.
- `fix(mysql)`: tolerate vanilla MySQL rejecting `ALTER ... IF NOT EXISTS`.
- `fix(sqlite)`: wire Phase 2-6 repositories into the SQLite Conn.

### Changed
- `README.md`, `CLAUDE.md`, `AGENTS.md`, `TDD.md`, `Product Brief.md` updated to cover Phase 1-8 surfaces, multi-backend storage, and gRPC.
- `docs/adr/005-scripted-llm-extractor.md` (Mnemos has no equivalent — this entry intentionally absent).

## [0.12.0] — 2026-04

- **gRPC API server** alongside HTTP REST. Schema in `proto/mnemos/v1/mnemos.proto`. Auth via the existing JWT verifier (`MNEMOS_JWT_SECRET` / `MNEMOS_AUTH_DIR`).

## [0.11.0] — 2026-04

- **Phase 7 legacy cleanup**: dropped `sqlite.Open`; ported all tests to `store.Open`.
- **Multi-backend storage** ([ADR 0001](docs/adr/0001-multi-backend-storage.md)) GA. Providers: `sqlite://` (default), `memory://`, `postgres://`/`postgresql://`, `mysql://`/`mariadb://`, `libsql://`. Postgres-wire-compatible engines (CockroachDB, YugabyteDB, Neon, Crunchy Bridge, TimescaleDB, AlloyDB Omni) and MySQL-wire-compatible engines (PlanetScale, TiDB, Vitess) work through native providers unchanged.
- Namespace isolation across all backends.

## [0.10.1] — 2026-03

- Retrieval-quality eval suite + v0.10 baseline.

## [0.10.0] — 2026-03

- **Hybrid retrieval** (Obvious Choice, part 2): BM25 over FTS5 keyword index + cosine over embeddings. Equal-weighted, max-normalised composite. Auto-creates / backfills `events_fts` and `claims_fts`.

## [0.9.0] — 2026-03

- **Entity layer** (Obvious Choice, part 1): canonicalised noun-phrases ("Felix Geelhaar", "Acme", "PostgreSQL", ...) as first-class entity nodes. New commands: `mnemos entities list/show/merge`, `mnemos extract-entities`, `mnemos query --entity`.

## [0.8.0] — 2026-03

- **Temporal validity** (Living Truth): `valid_from` / `valid_to` per claim; default queries hide superseded claims; `mnemos query --at YYYY-MM-DD` for point-in-time answers; `mnemos resolve <new> --supersedes <old>` to close one claim's interval when a new one takes its place.

## [0.7.0] — 2026-02

- **Trust scoring**: `trust = confidence × corroboration × freshness`. Auto-recomputed after every `process` run; `mnemos recompute-trust` for manual rebuild; `mnemos query --min-trust X` filters; `mnemos metrics` reports `avg_trust` and `low_trust_count`.
- Semantic dedupe via `mnemos dedup`.

## [0.6.1] — 2026-02

- v0.5 → v0.6 schema migration; junk/dedup filters; ops UX polish.

## [0.6.0] — 2026-02

- Local-LLM (Ollama) UX sweep: timeouts, reasoning-block tolerance, JSON-mode forgiveness, ops commands.

## [0.5.0 and earlier]

Tagged on GitHub. Notable themes: rule-based + LLM-powered extraction, contradiction detection, MCP server, CLI, embeddings, registry push/pull, JWT auth, claim lifecycle.
