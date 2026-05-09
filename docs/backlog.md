
## Multi-Format Input Service

Support ingestion of raw text and file-based inputs (TXT, MD, JSON, CSV), preserving source metadata and timestamps for downstream traceability.

---

## Parser and Event Normalization

Normalize heterogeneous inputs into immutable events with schema versioning and source linkage, forming the canonical Input -> Event transformation.

---

## Append-Only Event Store

Implement a local-first SQLite-backed append-only event store with indexes for timestamp and source input ID to support reliable replay and query foundations.

---

## Claim Extraction Engine

Derive structured claims (fact, hypothesis, decision) from events, enforce validation rules, and map each claim to at least one evidence event.

---

## Relationship Detection Engine

Detect support and contradiction relationships between claims within scoped comparisons and persist relationship edges for truth evolution analysis.

---

## CLI Query Interface

Provide a CLI query interface that returns structured answers with claims, surfaced contradictions, and an evidence-backed timeline.

---

## Workflow Orchestration and Observability

Track ingestion/extraction/relationship jobs through explicit workflow states with structured logging, retries, timeouts, and failure handling.

---

## Claim Extraction Heuristics v2

Improve claim extraction quality by splitting event text into sentence-level candidate claims, deduplicating near-identical claims, and applying stronger heuristic scoring for fact/decision/hypothesis classification while preserving evidence traceability.

---

## SQLC Typed Data Access

Adopt sqlc for SQLite data-access to replace handwritten SQL in repositories with generated, typed query methods; include schema/queries layout, generation command, and initial migration of core event and claim reads/writes.

---

## SQLC Migration Phase 2

Complete sqlc adoption by migrating relationship and compilation job repositories to generated typed queries, reducing handwritten SQL and unifying data access patterns.

---

## One-Step Process Command

Add a CLI `process` command that runs ingest, extract, and relate in one workflow for either file input or raw text, then prints IDs/counts so users can immediately run query without manual event ID lookups.

---

## Run-Scoped Querying

Add run-scoped processing and querying so each process/ingest flow emits a run_id and query can restrict retrieval to a specific run, preventing cross-run context contamination in answers.

---

## LLM-Powered Claim Extraction

Replace/augment rule-based extraction with LLM-powered claim extraction. Supports Anthropic, OpenAI, Google Gemini, Ollama, and any OpenAI-compatible provider. Activated via --llm flag with env var configuration. Falls back to rule-based extraction on LLM failure.

---

## Markdown Preprocessing and Structural Noise Filters

Strip markdown formatting (bold, italic, strikethrough, links, bullets, checkboxes, headers) before claim extraction, and reject structural noise like label-value metadata rows, pipe-separated table rows, JSON fragments, short title-case headers, and email salutations. Raises extraction F1 on real-world documents from 79.9% to 87.8%.

---

## Grounded LLM Answer Generation

When an LLM provider is configured, synthesize query answers from retrieved claims with inline citations instead of returning fixed templates. Falls back to template answers on LLM failure so offline/zero-config queries still work.

---

## Claim-Level Embeddings and Cosine Reranking

Generate vector embeddings for events and claims, store as little-endian float32 BLOBs in SQLite, and rerank query results by cosine similarity against the question embedding. Falls back to token-overlap ranking when no embedding provider is configured.

---

## Project-Scoped Database with Init Subcommand

Add `mnemos init` to create a `.mnemos/` directory in the working directory. Database resolution walks up from CWD looking for `.mnemos/mnemos.db`, falling back to the XDG global default. MNEMOS_DB_PATH still wins outright. Discovery stops at the user's HOME to avoid adopting an unrelated parent project's DB.

---

## MCP Project Document Auto-Ingest

When the MCP server starts inside a project (`.mnemos/` exists), bulk-ingest standard project documents — README, PRD, CHANGELOG, Roadmap, CLAUDE.md, ARCHITECTURE — plus the top level of `docs/` and recursive ADR conventions. Deduped via SQLite json_extract on event metadata source paths.

---

## File Watch MCP Tool

Expose a `watch_file` MCP tool that registers a path for re-ingestion when its content changes. Polling-based (5s, sha256 content comparison), in-memory state. Re-ingest reuses the same extract+relate pipeline as auto-ingest so agent edits flow into queryable claims with no manual step.

---

## Knowledge Browsing MCP Tools

Expose `list_claims`, `list_decisions`, and `list_contradictions` MCP tools for paginated read-only browsing of the knowledge base. Supports type/status filtering, pagination with sensible defaults, and claim-text hydration on the contradiction list via SQL JOIN.

---

## Git Commit Context Auto-Ingest

When the project root has a `.git` directory, read the most recent 50 commits via `git log` on MCP startup and persist each as an event with metadata (SHA, author, committed_at, subject). Commits flow through extract+relate so subjects and bodies become queryable claims. Deduped by SHA. Explicit `ingest_git_log` MCP tool exposes deeper history via limit/since.

---

## Merged PR Auto-Ingest via gh CLI

When the gh CLI is installed and authenticated for github.com, auto-ingest the 20 most recent merged pull requests on MCP startup. PR titles and bodies flow through extract+relate so they become queryable claims. Deduped by PR number. Explicit `ingest_git_prs` MCP tool exposes configurable limit for deeper history. Silent no-op when gh is missing, unauth, or the repo is not on GitHub.

---

## Knowledge Registry Server (mnemos serve)

Add a `mnemos serve` subcommand that starts an HTTP API registry server backed by the same SQLite schema as the local client. The registry is the "remote origin" to each local project's "local repo" — users can push knowledge to a shared registry and pull from it at query time. Core endpoints cover event append, claim lookup, and relationship reads. Runs standalone or in a container.

---

## Push/Pull Synchronization with Remote Registry

Add `mnemos registry connect <url>` to wire a local project to a remote registry, plus push and pull semantics that mirror git's model. Push sends local events/claims/relationships to the registry; pull fetches remote knowledge and merges by source hash + run_id. Automatic sync hooks into process/query when enabled. Depends on Knowledge Registry Server.

---

## Cross-Project Query Federation

Extend the query engine to federate across the local database and one or more connected registries. Results include source provenance (local vs registry-name) so claims can be weighed by trust. Ranks cross-project results using the same cosine + token-overlap logic as local queries. Depends on Push/Pull Synchronization.

---

## Registry Namespace and Scope Isolation

Add namespace/scope primitives to the registry (team, org, project) so multiple tenants share a single server without leaking claims across boundaries. Push and pull operations carry a namespace context. Query federation respects the caller's accessible namespaces. Depends on Knowledge Registry Server.

---

## GraphRAG Multi-Hop Query Support

Extend the query engine to traverse relationship edges at query time, enabling multi-hop questions like "what decisions contradict the current approach to auth?" that require walking from a seed claim through supports/contradicts chains. Reports the traversal path as part of the evidence trail.

---

## Governance and Compliance Layer

Add governance features to Mnemos as an enterprise-ready evidence layer: bias detection on claim sources, audit trails tracking who/when/why for every event and claim change, retention policies for aging out or archiving old events, and compliance export formats (CSV/JSON) for regulated environments.

---

## Enterprise Integration Adapters

Adapters that ingest content from common team knowledge surfaces: Slack threads, Microsoft Teams channels, Jira tickets, and Notion pages. Each adapter maps the source's identifiers into Mnemos event metadata so traceability survives the round trip. Adapters run as periodic jobs rather than real-time listeners to avoid webhook complexity.

---

## Web Interface on Registry API

Build a web UI that sits on top of the Phase 2B registry HTTP API — not a standalone app. Surfaces query, browsing, contradictions, and timeline views. Deferred until the registry API is proven with the CLI and MCP surfaces, per the "API before UI" principle established in the Phase 2A rewrite.

---

## Multi-Backend Storage Foundation

Replace the hard-wired SQLite construction with a pluggable storage layer per ADR 0001. Phase 1a: add `internal/store` driver registry with URL-scheme dispatch (`store.Open(ctx, dsn)`), repackage the existing SQLite implementation behind the registry as a `sqlite://` provider, ship a `memory://` in-process provider implementing the same `ports.*` interfaces (forces port purity, unblocks fast tests + Nous embedding), and widen `ports.EventRepository`/`ports.ClaimRepository` to the union of methods callers actually reach for. Subsequent phases (separate features): migrate cmd/mnemos and internal/pipeline call sites onto `store.Open`, add `MNEMOS_DB_URL`, add the Postgres provider with `pgvector`/`tsvector` and namespace isolation. Source: docs/adr/0001-multi-backend-storage.md.

---

## Multi-Backend Storage MNEMOS_DB_URL

Phase 1b of ADR 0001: introduce a `MNEMOS_DB_URL` environment variable that takes precedence over `MNEMOS_DB_PATH` and is the canonical way to point Mnemos at any registered storage backend. Add a `resolveDSN()` helper in cmd/mnemos that returns the URL when set, otherwise wraps the legacy resolved file path as `sqlite:///<path>`. Add an `openConn(ctx)` helper that calls `store.Open(ctx, resolveDSN())` so future call sites can switch by replacing two lines. Migrate the `mnemos doctor` deep-probe to use `openConn` as the proof-of-life consumer (it already exercises the deepest paths and is a natural smoke test for DSN resolution). Update help text + CLAUDE.md so MNEMOS_DB_URL appears alongside MNEMOS_DB_PATH. Mass call-site migration stays in a separate later phase.

---

## Multi-Backend Storage cmd/mnemos call site migration

Phase 1c of ADR 0001: migrate every production cmd/mnemos call site that does sqlite.Open(resolveDBPath()) onto a registry-mediated open. Add an openDB(ctx) helper that returns (*sql.DB, *store.Conn, error) — most cmd/mnemos surfaces still need the raw *sql.DB for entity/job repos and raw SQL paginations that aren't on the ports yet. Migration is mechanical: replace the open call, defer conn.Close instead of closeDB(db). Tests using fixed temp paths via sqlite.Open(filepath.Join(t.TempDir(), ...)) stay untouched — they're not on the resolveDBPath path. Result: every operator-facing CLI/MCP/server command honours MNEMOS_DB_URL (including future SQLite DSN options like ?busy_timeout=). Out of scope: lifting EntityRepository/CompilationJobRepository into ports (separate later phase), refactoring pipeline functions that take *sql.DB (separate later phase), Postgres provider (separate later phase).

---

## Multi-Backend Storage port lift Entity Job

Phase 2a of ADR 0001: lift EntityRepository and CompilationJobRepository from concrete SQLite types into ports interfaces, populate them on the Conn struct, and add memory provider implementations of both. After this phase, callers can use conn.Entities and conn.Jobs without reaching into the SQLite package, and memory:// can persist canonicalised entities and compilation_jobs the same way SQLite does. Pipeline refactor (PersistArtifacts / MaterializeEntities switching from *sql.DB to *store.Conn) and Postgres provider remain separate later phases.

---

## Multi-Backend Storage pipeline port refactor

Phase 2b of ADR 0001: refactor pipeline.PersistArtifacts, pipeline.MaterializeEntities, pipeline.GenerateEmbeddings, pipeline.GenerateClaimEmbeddings to take *store.Conn instead of *sql.DB. Replace the cross-table SQLite transaction inside PersistArtifacts with sequential port-typed repository calls; per-table writes are still atomic in each backend. Trust scoring becomes optional via ports.TrustScorer type assertion. Update every caller in cmd/mnemos. Status-history attribution per claim is preserved by grouping claims by CreatedBy and calling UpsertWithReasonAs once per group. After this phase memory:// can run end-to-end ingest/process/embed paths. semantic_dedupe.go remains SQLite-specific (raw SQL probes) and is out of scope.

---

## Multi-Backend Storage call site cleanup

Phase 2c of ADR 0001: drop the remaining sqlite.NewXxxRepository(db) constructions across cmd/mnemos in favor of conn.Xxx port-typed access. Trust scoring callers use ports.TrustScorer assertion. semantic_dedupe.go stays SQLite-specific (raw SQL probes) until a separate dedicated phase. Goal: cmd/mnemos imports internal/store/sqlite ONLY for blank-import provider registration after this phase.

---

## Multi-Backend Storage Postgres provider

Phase 3 of ADR 0001: add internal/store/postgres/ provider implementing every port interface. Uses pgx/v5 + database/sql. Translates ?namespace= into CREATE SCHEMA IF NOT EXISTS + SET search_path. pgvector for VectorSearcher capability, tsvector for TextSearcher. Migrations live alongside the provider and run on Open. CI gains a Postgres job (docker-compose). Mirrors the contract validated by SQLite + memory in Phases 1-2.

---

## Multi-Backend Storage backend-agnostic serve and dedupe

Phase 4a of ADR 0001: migrate the last two SQLite-bound surfaces in mnemos to ports — `cmd/mnemos/serve.go` HTTP handlers (events/claims/relationships/embeddings/metrics) and `internal/pipeline/semantic_dedupe.go` (PlanSemanticDedupe + ApplySemanticDedupe). Use port-typed repositories where they exist; keep raw SQL paths only when no port-level alternative is available, and clearly mark them as SQLite-specific. After this phase no production cmd/mnemos or pipeline code reaches for sqlite.NewXxxRepository directly outside its own package.

---

## Multi-Backend Storage MySQL MariaDB provider

Phase 4b of ADR 0001: add internal/store/mysql/ provider implementing every port interface, registered for `mysql://` and `mariadb://` schemes. Uses github.com/go-sql-driver/mysql + database/sql. Per ADR 0001 §3, MySQL has no per-tenant schemas — namespace translates to "use a separate database (CREATE DATABASE IF NOT EXISTS <ns>; USE <ns>)". Schema SQL adapted: jsonb → JSON, bytea → LONGBLOB, bigserial → BIGINT AUTO_INCREMENT, timestamptz → DATETIME(6), now() → CURRENT_TIMESTAMP. Integration tests gated on TEST_MYSQL_DSN. MariaDB shares the wire protocol so the same provider serves both.

---

## Multi-Backend Storage CI integration job

Phase 5 of ADR 0001: lock in the Postgres and MySQL integration tests with a CI job. Add a `database-providers` GitHub Actions job that runs alongside the existing `Build & Test` job, spinning up postgres:16 + mysql:8 services and running `go test -race -count=1 ./internal/store/postgres/ ./internal/store/mysql/` with TEST_POSTGRES_DSN + TEST_MYSQL_DSN populated. Add a `make test-integration` target so developers can run the same suite locally.

---

## Multi-Backend Storage libSQL provider

Phase 6 of ADR 0001: ship a libSQL/Turso provider for cloud-replicated and edge-deployable SQLite-compatible storage. Since libSQL is wire-compatible with SQLite, the provider reuses the existing sql/sqlite/schema.sql and the existing SQLite repository implementations — only the registration, DSN parsing, and sql.Open driver name change. Pure-Go driver (github.com/tursodatabase/libsql-client-go) keeps CGO_ENABLED=0. Supports both remote DSNs (libsql://my-db.turso.io?authToken=xyz) and local file DSNs (libsql:///tmp/test.db). Namespace param is ignored — each Turso database is its own tenant boundary, like plain SQLite. Plus a CLAUDE.md note that the existing postgres:// provider already serves any Postgres wire-protocol-compatible engine (CockroachDB, Yugabyte, Neon, Crunchy Bridge), no extra code needed.

---

## Multi-Backend Storage everything-on-ports legacy cleanup

Phase 7 of ADR 0001: every production surface is backend-agnostic; no legacy. Add the port methods needed to express semantic dedupe and the serve.go HTTP handlers without raw SQL — Claims.RepointEvidence, Claims.DeleteCascade, Relationships.RepointEndpoint, Relationships.DeleteByClaim, Embeddings.Delete, plus paginated list methods on Events/Claims/Relationships/Embeddings. Implement in every native provider (sqlite, memory, postgres, mysql; libsql inherits sqlite). Migrate pipeline.ApplySemanticDedupe and the serve.go handlers to ports. Then drop legacy: MNEMOS_DB_PATH env var, openDB helper (keep openConn only), sqlite.Open public function, any remaining sqlite-only call sites in cmd/mnemos. Pre-launch posture — no backwards-compat stubs.

---

## gRPC API Server

Add a gRPC API surface to Mnemos alongside the existing HTTP REST API. Define proto schemas for events, claims, relationships, and embeddings that mirror the HTTP API contract. Generate Go code with protoc-gen-go-grpc. Implement a gRPC server that reuses the existing port-typed repositories and bearer-token auth from serve.go. Wire into the CLI via `mnemos serve --grpc-port` or `mnemos grpc`. This enables high-performance service-to-service communication for the cognitive stack (Chronos, Praxis, Nous) and supports streaming for large dataset operations.

---

## Causal Relationship Edges

Phase 1 of evidence+causality+outcomes. Extend `relationships.kind` beyond supports/contradicts with: causes, caused_by, action_of, outcome_of, validates, refutes, derived_from. Domain: new RelKind* consts in internal/domain/types.go. internal/relate/engine.go: causal heuristic (action-verb claim t1 + state-change claim t2 with shared entities → causes) plus LLM extractor for ambiguous pairs. Query --hops gains --kind filter. Schema migration. Add evals/relate/causal/ with 20 cases. Foundation for outcomes, lessons, decisions, playbooks. No new entity types — pure edge-type expansion. Sequencing: ship before Action Outcomes since outcomes link via action_of/outcome_of edges.

---

## Action Outcome Recording

Phase 2 of evidence+causality+outcomes. New domain types: Action{ID, Kind, Subject, Actor, RunID, At} and Outcome{ID, ActionID, Result, Metrics map[string]float64, ObservedAt}. Schema: actions, outcomes tables with FK to events. Two ingestion paths: (1) push API — `mnemos ingest --kind=action` + MCP `record_action`/`record_outcome` tools for agent-reported events; (2) pull adapters — Prometheus/OTel/log scrape under internal/adapters/outcomes/ for autonomous capture. Link Action↔Outcome via Phase 1 action_of/outcome_of edges. Required for Lessons synthesis (clusters action→outcome chains). Pull adapters can be incremental — ship at least one (Prometheus) with the push API.

---

## Lessons Synthesis Engine

Phase 3 — sharpest differentiator. New domain: Lesson{ID, Statement, Scope{Service,Env,Team}, Evidence []ActionID, Confidence, DerivedAt, LastVerified}. Schema: lessons + lesson_evidence tables, system-versioned per Phase 7 versioning best-practice. internal/synthesize/ engine clusters action→outcome chains by similarity + scope; emits Lesson when N≥3 corroborating with low contradiction. Confidence formula: corroboration_count × outcome_consistency × recency_weight. Brain-best-practice synthesis trigger — hybrid: incremental on-write (cheap consolidation, every claim ingest) + periodic batch (full re-cluster, sleep-like, configurable cron, default 24h) + manual `mnemos synthesize`. Query: `mnemos lessons --service=X --kind=Y`. MCP tool: `query_lessons`. Positioning anchor: "evidence-based memory that learns from actions over time".

---

## Temporal Validity Hardening

Phase 4. Strengthen existing valid_from/valid_to with stale-detection signals. Add columns: claims.last_verified, claims.verify_count, claims.half_life_days (per-claim override of hardcoded 90d default in internal/trust/trust.go). Surface staleness: Answer.StaleClaims[] in query results when freshness < threshold. New CLI: `mnemos verify <claim>` re-confirms against new events, bumps last_verified + verify_count, recomputes trust. Trust formula stays confidence × corroboration × freshness but freshness now uses per-claim half-life. Prevents stale-memory poisoning of agent decisions. No new entity types — column additions + CLI.

---

## Decision Memory

Phase 5. New domain: Decision{ID, Belief []ClaimID, Plan, Alternatives []string, Reasoning, RiskLevel, ChosenAt, OutcomeID *string}. Closes the loop: decision → action → outcome → validates/refutes belief claims. Schema: decisions table, FK to outcomes. Decision.OutcomeID links to Phase 2 Outcome — when set, Phase 1 validates/refutes edges fire automatically against Belief claim list. Audit query: `mnemos decision <id>` replays decision with retrieved evidence as it existed at ChosenAt (point-in-time using Phase 4 temporal validity). MCP tool: `record_decision`. Enables agent self-audit and reasoning improvement.

---

## Playbook Synthesis

Phase 6. Direct compete with gbrain "skills" but derived not authored-from-scratch. Domain: Playbook{ID, Trigger, Steps []Step, Scope, DerivedFromLesson []LessonID, Confidence}. Steps as structured JSON contract — Mnemos returns steps only, Praxis executes (separation of concerns). Auto-synthesis in internal/synthesize/playbooks.go: cluster Phase 3 lessons by trigger pattern (e.g. "latency_spike_after_deploy" from N lessons matching that scope+kind) → emit playbook. Manual authoring also supported via markdown source-of-truth (overlaps with Phase 7). Query: `mnemos playbook <trigger>`. MCP tool: `query_playbook`. Distinct from skills: playbooks have provenance back to lessons back to actions back to events.

---

## Human-Editable Markdown Layer with System-Versioned History

Phase 7. Markdown export-back loop for engineers to "correct system memory". CLI: `mnemos export --kind=lesson|claim|playbook [--scope=...]` emits Git-friendly markdown with stable IDs in frontmatter. `mnemos import <file.md>` diffs against current state, creates new version row, fires extract+relate on changed text. Versioning best-practice: SQL:2011 system-versioned tables — append-only `*_versions` (claim_versions, lesson_versions, playbook_versions) with full row snapshot + valid_from/valid_to. Git-style deltas overcomplicate query. Audit-friendly, time-travel queries trivial: `WHERE @timestamp BETWEEN valid_from AND valid_to`. Schema migration adds versioning triggers in SQLite/Postgres/MySQL providers. Integrates with Phase 4 temporal validity.

---

## Scope-Aware Memory

Phase 8. Multi-tenant scope beyond run_id and Postgres-schema namespace. Add scope JSONB column to claims, lessons, playbooks, decisions: {service, env, team, custom_tags}. Scope filter on every query, list, and synthesis path. Synthesis (Phase 3, 6) clusters strictly within scope to prevent cross-context noise. CLI: `--service=`, `--env=`, `--team=` flags on query/process/lessons/playbook. MCP: scope param on every tool. Indexes on scope JSON paths in each backend (SQLite json_extract, Postgres JSONB GIN, MySQL JSON functional). Defaults: when unset, all-scopes. Integrates with existing Agent.AllowedRuns whitelist.

---

## Epistemic Provenance & Claim Trust Framework

Implement epistemic provenance tracking to explain *why* a claim or data source is trusted over others, addressing user feedback on broad knowledge bases (e.g. Obsidian/Karpathy-style setups) needing trust differentiation. Covers two core areas:
1. Source Credibility Signals: Score claim trustworthiness using link density (convergence across multiple resources), liveness (e.g. 12-year-old process doc still being actively executed), recency, authoritativeness, and citation graph analysis.
2. Test Conflict Resolution: Resolve conflicting test results (e.g. Test1 passes, Test2 fails for same functionality) via test provenance (recency, authority), confidence-weighted resolution, and first-class "which source/test to trust?" query support.

---

## Reliability-first recall hardening (never wrong on recall)

Prioritize product trust strategy: "never wrong on recall". Add a reliability-first initiative that hardens retrieval/answering so factual recall is lossless and deterministic before any advanced synthesis expansion.

Scope:
- Build a production eval harness using real anonymized conversation traces plus golden answers and required-substring assertions.
- Enforce deterministic answer fallback: when generated answer omits required facts, return evidence-grounded claim text block.
- Add confidence-calibrated answer policy with strict modes:
  - high confidence + no conflict: concise direct answer
  - low confidence or unresolved conflict: explicit uncertainty + evidence citations + escalation path
- Add CI regression gates for recall metrics (hard fail on any drop beyond tolerance).
- Add per-workspace memory quality telemetry: unresolved contradictions, stale claims, low-trust clusters, recall pass-rate trend.

Non-goals:
- New broad AI synthesis features.
- Fancy UI redesign.

Success metrics:
- Recall pass-rate (required-substring) >= 99% on held-out eval set.
- Zero false factual omissions on golden critical set.
- Regenerate rate and user correction rate decrease sprint-over-sprint.

---

## Recall hardening: real-trace eval harness

Create a reproducible eval harness with anonymized real conversation traces, golden answers, required-substring assertions, and critical recall sets. Include train/validation/holdout split and deterministic runner.

---

## Recall hardening: deterministic evidence fallback

Implement deterministic answer fallback in query path: when generated answer omits required evidence facts or falls below recall checks, return evidence-grounded claim text block with citations.

---

## Recall hardening: confidence-calibrated answer policy

Add confidence-calibrated answer policy modes: direct answer for high-confidence/no-conflict, uncertainty mode for low-confidence/unresolved conflicts with explicit citations and escalation options.

---

## Recall hardening: CI regression gates

Add CI benchmark gates for recall reliability with hard fail thresholds and baseline comparison. Enforce no-regression policy on required-substring recall and omission rate.

---

## Recall hardening: memory quality telemetry

Add per-workspace memory quality telemetry: unresolved contradictions, stale claim ratio, low-trust clusters, recall pass-rate trend, and correction/regenerate proxies.

---

## Recall hardening: rollout guardrails and SLOs

Ship trust-focused documentation and rollout controls: feature flags, runbooks, acceptance checklist, and SLO definitions for never-wrong recall operation.

---

## Public /v1/leads hardening: rate limit + email validation + structured logging

The public POST /v1/leads endpoint (cmd/mnemos/serve_auth.go:104-108 bypass + cmd/mnemos/serve.go:288-310 handler) has zero rate limiting, only a trivial `@` check, and writes the email verbatim to stderr via fmt.Fprintf — a DoS vector, log-injection vector, and GDPR-relevant PII exposure. Required: per-IP token-bucket limiter (golang.org/x/time/rate) with strict caps for /v1/leads; net/mail.ParseAddress + 254-char cap; reject CR/LF or non-printable bytes; hash-or-elide email in logs; route through bolt structured logger; explicit Origin/Referer allow-list for browser POSTs. Acceptance: chaos test sustaining 1k req/s for 60s does not exhaust process; emailed CRLF cannot forge log lines; logs contain no plaintext email.

---

## Security headers middleware (CSP, HSTS, X-Frame-Options, X-Content-Type-Options, Referrer-Policy)

No security headers on any HTTP response (cmd/mnemos/serve.go:267 middleware chain — only panicRecover, access log, JWT auth). Landing page (cmd/mnemos/web/landing.html) executes inline JS and posts to /v1/leads with no CSP, no clickjacking protection, no XSS containment. Add a securityHeaders middleware setting: Content-Security-Policy (script-src 'self', form-action 'self', frame-ancestors 'none'), X-Content-Type-Options: nosniff, X-Frame-Options: DENY, Referrer-Policy: no-referrer, Strict-Transport-Security when TLS is enabled. Wire ahead of the auth middleware so it covers landing, registry SPA, and API. Acceptance: securityheaders.com scan ≥ A; integration test asserts CSP header present on /, /app, /v1/health.

---

## Deduplicate trust signal calculation across trust + query packages

Weight constants (0.50/0.15/0.13/0.10/0.05/0.07) and per-signal calculations are duplicated across internal/trust/credibility.go:98-105 and internal/query/engine.go:1800-1807 with a `must stay in sync` comment as the only governance — a fitness-function smell. Refactor: `trust.ScoreCredibility` returns (score float64, signals []domain.ProvenanceSignal, rationale string) directly. Delete provenanceSignals in engine.go. WhyTrustClaim consumes the structured breakdown. Add one test asserting WhyTrustClaim signals == ScoreCredibility breakdown bit-for-bit so future drift fails CI. Effort: small (~2h).

---

## ClaimRepository.ListByTestRequirementRef port + index for trust queries

cmd/mnemos/trust.go:48-59 and cmd/mnemos/mcp.go:1118 (which_test_to_trust) call Claims.ListAll(ctx) and filter by TestRequirementRef in Go — O(n) scan over the full claim corpus per invocation, with all 30+ columns marshaled over the wire on Postgres/MySQL. Add `ListByTestRequirementRef(ctx, ref string) ([]Claim, error)` to ports.ClaimRepository. Implement across sqlite, postgres, mysql, memory, libsql. Add `CREATE INDEX idx_claims_test_requirement_ref ON claims(type, test_requirement_ref) WHERE test_requirement_ref != ''` (and dialect equivalents). Update both call sites. Acceptance: bench with 100k claims shows <50ms p95 vs current ListAll baseline.

---

## Test coverage for mnemos trust CLI and which_test_to_trust MCP tool

Newly-shipped surface in cmd/mnemos/trust.go:19-130 and cmd/mnemos/mcp.go:1108-1182 (mcpRunWhichTestToTrust) has zero tests — verdict thresholds (0.05 ambiguous), requirement_ref validation, empty-candidate path, sort stability, JSON shape all unverified. The resolver path (pickTestWinner) is covered, but the surface above it isn't. Add table-driven tests for mcpRunWhichTestToTrust covering: winner clear-margin / ambiguous (<0.05 gap) / no_candidates / missing requirement_ref. Use openConn against memory backend. Mirror as a cmd/mnemos handler test patterned on existing audit_test.go. Effort: small.

---

## Eval CI regression gate across extraction, causal, relationship, longmem, locomo suites

.github/workflows/benchmarks.yml:67-83 runs only --suite contradiction_detection. data/eval/*.yaml has 102 cases across extraction (90), causal_detection, relationship_detection running through go test with no F1/precision/recall floor — an LLM prompt change that drops extraction F1 from 0.92 to 0.55 still passes CI. Add `eval-gate` job that runs `go test ./data/eval/... -json` and a small Go check (or Python equivalent) comparing per-suite F1/precision/recall against data/eval/baseline.json floors, failing on >5% drop. Extend to LongMemEval/LoCoMo when adapters are wired. This is the single biggest oracle gap in the repo.

---

## Mutation testing on internal/trust, internal/relate, internal/query

Coverage is ~90%+ but mutation kill-rate is unknown — `>` flipped to `<` in trust scoring may pass current tests, giving coverage illusion. Add go-mutesting (or ooze) over internal/trust, internal/relate, internal/query. Wire as a separate non-blocking CI job that posts the kill-rate as a PR comment. Block merges when internal/trust/ kill-rate falls below 70%. Document the threshold + rationale in a docs/testing/mutation.md. Effort: medium.

---

## API surface parity fitness function (HTTP / gRPC / MCP)

cmd/mnemos/mcp.go:448 registers which_test_to_trust; no equivalent route in cmd/mnemos/serve.go:248-261 or method in internal/server/grpc/server.go. Conway's Law signal — three transports own three definitions, each hand-maintained against a different mental model. Likely repeats with other recently-added tools. Step 1 (small): add a CI parity test enumerating (mcp_tool_name | http_route | grpc_method) triples and asserting set equality with an explicit allowlist for intentional gaps. Step 2 (medium, separate task): introduce internal/app/ use-case interfaces so HTTP/gRPC/MCP become 20-line transcoders.

---

## North Star metric + opt-in activation telemetry

Recall pass-rate (internal/query/engine.go:2006 MinRecallPassRate=0.95) is a quality floor, not an adoption metric. The product cannot answer "is anyone getting value?" — no time-to-first-value, no week-2 query retention, no regenerate-rate aggregation. Pick North Star: "weekly active projects with ≥1 evidence-backed query." Instrument via existing MemoryQualityMetrics (engine.go:1921) plus daily rollup. Ship `mnemos metrics --workspace` view. Add opt-in anonymized telemetry POST to /v1/telemetry (random install_id, weekly_active_runs, queries, recall_pass_rate, regenerate_rate). Default off. Public dashboard at mnemos.dev/health once 10 workspaces opt in.

---

## Honest competitor benchmarks vs mem0 / zep / letta on LongMemEval + LoCoMo

benchmarks/suites/{longmemeval,locomo}.py scaffolds exist; benchmarks/RESULTS.md and baseline.json are present. Mem0 cites LongMemEval 93.4 / LoCoMo 91.6; Mnemos has no head-to-head numbers public. Wire mem0/zep adapters in benchmarks/, run all three providers on the published datasets, publish honest comparison in benchmarks/RESULTS.md. Link from README hero. If Mnemos loses on a suite, file the gap as a roadmap item — losing honestly beats not measuring. This is the strongest commercial wedge against leader-cited numbers.

---

## Landing positioning + demo-on-page + pricing tiers (GTM unification)

Landing (cmd/mnemos/web/landing.html:6) sells "Data Sovereignty & Compliance Platform"; README.md:3 sells "Self-hosted memory for AI apps." A GitHub visitor and a landing visitor get different products — category schizophrenia kills positioning. (1) Pick one lane: "evidence layer for AI agents you actually own" (where mem0/zep/letta compete); compliance becomes a wedge attribute, not the headline. Rewrite hero. (2) Add runnable code block / animated terminal showing `mnemos process --text "..."` → contradiction-detected JSON above the lead form. (3) Move README competitor comparison table onto landing as "vs hosted memory services" with one row per competitor. (4) Add 3-tier pricing strip: OSS (free, self-hosted) / Team (managed registry + SSO, waitlist) / Enterprise (audit, SLA, SOC2 — contact). (5) Replace adjective-soup social-proof section with real benchmark metrics linking to benchmarks/RESULTS.md. Effort: medium.

---

## Operator-readable trust rationale alongside compact metric string

internal/trust/credibility.go:116-133 emits engineer-shorthand: "base=0.62 authority=0.50 citations=3(0.42) recency=0.78 liveness=live agent_authority=1.00 test_decisiveness=8/10(0.60)". CLAUDE.md positions this as the surface for non-technical operators via which_test_to_trust, but the string assumes the reader knows what citations=3(0.42) means. Ship a second prose rationale alongside the metric string: "Last ran 12 days ago (fresh). Passed 8 of 10 runs (decisive). Live test. Authority not configured (assumed neutral)." Keep compact form for tooling under score_breakdown; surface prose form as `rationale` in cmd/mnemos/trust.go and the WhichTestToTrust MCP output. Also: bump CLAUDE.md prompt v1.2 → v1.4 (matches internal/extract/prompt.go), add hybrid BM25+cosine note to README.md:97.

---

## JWT kid header + rotation; scope/run filter on trust path

internal/auth/jwt.go:219-253 accepts only HS256 with no `kid` — dual-key rotation falls back to brute-trying the previous secret, masking real attacks in logs. internal/auth/secret.go:48-59 auto-generates a secret silently on first boot at a predictable path with no rotation prompt. Separately, cmd/mnemos/mcp.go:1118 (which_test_to_trust) and cmd/mnemos/trust.go:48 call Claims.ListAll without honoring the agent token's Scopes/Runs whitelist (jwt.go:51-97) — a low-privilege agent can enumerate test claims across runs/services it shouldn't see. Fix: include `kid` in JWT header, select verification key by kid, surface clearer rotation guidance when secret auto-generates. Thread actor scope/run whitelist into the trust path; filter candidates before scoring (or use the new ListByTestRequirementRef + scope filter).

---

## Postgres/MySQL connection pool tuning + LLM cache eviction

internal/store/postgres/postgres.go:116-121 and internal/store/mysql/mysql.go:156-163 call sql.Open + PingContext but never tune SetMaxOpenConns/SetMaxIdleConns/SetConnMaxLifetime. Go default is unlimited open conns — exhausts Postgres max_connections (100) under MCP burst load and silently leaks idle conns. Set MaxOpenConns=25, MaxIdleConns=5, ConnMaxLifetime=30m, configurable via MNEMOS_DB_MAX_CONNS. Separately, internal/extract/llm_engine.go:295-307 stores cache JSON files under data/cache/llm-extraction/ with no TTL, LRU, or size cap — long-running ingestion grows unbounded and fills disk silently. Add MNEMOS_LLM_CACHE_MAX_BYTES (default 1 GiB) and a sweep on cache miss that evicts oldest by mtime until under cap. Also delete duplicate `idx_events_run_id` declaration in sql/sqlite/schema.sql:15-16.

---

## Request ID + RED metrics + SLO doc + supply-chain attestation

No correlation/request IDs across HTTP/MCP/pipeline (cmd/mnemos/serve_auth.go:173-188 boltAccessLog has no request_id/trace_id), making distributed tracing impossible. No /metrics Prometheus endpoint, no histogram of http_request_duration_seconds — operators cannot answer "is mnemos healthy right now" beyond /health. No SLO/error budget defined. Supply chain weak: .goreleaser.yaml has no SBOMs, no cosign signing, no SLSA provenance, dockers: only linux/amd64. Step 1 (small): add requestIDMiddleware (UUIDv7) injecting X-Request-ID into context; propagate to bolt log lines. Step 2 (medium): add prometheus/client_golang RED metrics on /internal/metrics; document SLO.md (p99<250ms reads, 99.9% availability/30d). Step 3 (medium): add `sboms:`, `signs:` (cosign keyless), arm64+amd64 docker_manifests, slsa-framework/slsa-github-generator workflow.

---

## Supply-chain attestation: SBOM, cosign signing, SLSA provenance, arm64 docker

Step 3 of the original observability bundle, scoped to its own task because it's pure release pipeline work. .goreleaser.yaml has no `sboms:`, no `signs:` (cosign keyless), no SLSA provenance generator wiring, and `dockers:` only builds linux/amd64 — arm64 server users (Graviton, Apple-Silicon Linux) get no image. Add: `sboms: - artifacts: archive` for SPDX SBOM emission; `signs:` block with cosign keyless OIDC; switch `dockers:` to `docker_manifests:` covering amd64+arm64; add slsa-framework/slsa-github-generator workflow to produce SLSA L3 provenance for binaries and container images. Acceptance: `cosign verify-blob` passes against published archives; SBOM downloadable from GitHub release; arm64 image pulls from registry; SLSA provenance attached.

---

## JWT kid header + key-id-driven verification

Split from the original observability+rotation bundle. internal/auth/jwt.go currently signs HS256 without a `kid` (key id) header. The dual-key Verifier (NewVerifierWithPrevious) brute-tries the previous secret on every signature error — when an attacker forges a wrong-but-plausible token the verifier silently consults both secrets, masking real attacks in logs. Add: include `kid` (an opaque short id, e.g. first 8 hex chars of SHA-256(secret)) in the JWT header on issue; select verification key by `kid` in NewVerifierWithPrevious; record the chosen key in audit logs so anomalous combinations surface; rotate kid on each LoadOrCreateSecret cycle. Tests cover (a) issue→verify happy path with kid, (b) rotation across two kids, (c) fabricated unknown-kid is rejected. Out of scope here: full asymmetric signing — HS256 stays.

---
