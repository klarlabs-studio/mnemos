# Mnemos Roadmap

*Core Principle: Validate each phase before expanding.*

---

## Phase 1: Developer Primitive (COMPLETE)

**Status:** Complete (v0.2)
**Goal:** Establish Mnemos as a local-first, open-source knowledge engine for AI agents and developer tooling.

### Milestones

- [x] Core domain model (Event, Claim, Relationship)
- [x] Multi-format input ingestion (TXT, MD, JSON, CSV, raw text)
- [x] Append-only SQLite event store
- [x] Claim extraction engine with evidence mapping
- [x] Relationship detection (supports/contradicts)
- [x] CLI query interface with BM25 ranking
- [x] Workflow orchestration with structured logs
- [x] SQLC typed data access
- [x] 102 eval test cases with precision/recall metrics
- [x] LLM-powered extraction with few-shot prompt
- [x] Embeddings for semantic search (event + claim level)
- [x] Query-time grounded generation (--llm)
- [x] Incremental relationship detection
- [x] Ollama auto-detect for zero-config LLM/embeddings
- [x] MCP server (`mnemos mcp`) with 3 tools
- [x] Distribution: Homebrew, Docker, go install

---

## Phase 2A: MCP Project Memory (SHIPPED)

**Status:** Shipped on `main`, awaiting v0.4 tag
**Goal:** Make Mnemos the default persistent knowledge layer for AI coding agents.

### Milestones

- [x] Project-scoped DB (`.mnemos/mnemos.db` in working directory) — `mnemos init` + git-style discovery walking up from CWD
- [x] Auto-ingest project docs on MCP startup — README, PRD, CHANGELOG, Roadmap, CLAUDE.md, ARCHITECTURE, top-level `docs/`, recursive ADR conventions
- [x] File watch MCP tool (`watch_file`) — polling-based, sha256 content comparison, in-memory state
- [x] Browsing MCP tools (`list_claims`, `list_decisions`, `list_contradictions`) — paginated, filtered, hydrated
- [x] Git-aware context — commit auto-ingest at MCP startup + `ingest_git_log` tool. Merged PR auto-ingest via `gh` CLI + `ingest_git_prs` tool

### Success Metrics

- AI agent correctly answers project decision questions
- Zero manual `mnemos process` commands after MCP setup
- Knowledge persists across agent sessions

---

## Phase 2B: Knowledge Registry (SHIPPED)

**Status:** Shipped on `main`
**Goal:** Knowledge flows across projects and teams through a shared registry.

### Concept

```
Local DB    = local repo     (per-project, local-first)
Registry    = remote origin  (shared team knowledge)
mnemos push = share knowledge to registry
mnemos pull = query team knowledge alongside local
```

### Milestones

- [x] `mnemos serve` — HTTP API registry server with embedded web UI
- [x] `mnemos registry connect <url>` — wire local to registry
- [x] `mnemos push` / `mnemos pull` — git-style sync with batched paginated transfer
- [x] Cross-project queries with claim provenance (`pulled_from_registry` metadata)
- [x] REST API (`/v1/events`, `/v1/claims`, `/v1/relationships`, `/v1/embeddings`, `/v1/metrics`)
- [x] Embeddings round-trip bit-exact through push/pull
- [x] Typed Go client SDK (`client/`) with fluent builder, bolt logging, fortify retry

---

## Phase A: Identity (SHIPPED)

**Status:** Shipped on `main`
**Goal:** Authenticated, attributable writes — every change traces to a real principal.

### Milestones

- [x] **A.1** — JWT primitives (Issuer/Verifier, HS256 with alg-confusion lock, revocation denylist) + `mnemos user create|list|revoke`, `mnemos token issue|revoke`
- [x] **A.2 + A.3** — JWT middleware on `mnemos serve` (replaces shared bearer token); every audit-bearing table gains `created_by` / `changed_by` with `<system>` sentinel; POST handlers stamp the JWT subject
- [x] **A.2.b** — CLI/MCP actor resolution: `--as <user-id>` flag and `MNEMOS_USER_ID` env so non-server writes also carry attribution
- [ ] **A.4** — OIDC integration (deferred until first real OIDC need surfaces)

---

## Phase F: Agent Governance (SHIPPED)

**Status:** Shipped on `main`
**Goal:** Non-human principals are first-class with explicit, narrowable authority.

### Milestones

- [x] **F.1 + F.2** — Agent identities + scope enforcement: `domain.Agent` with owning user, `agents` table FK'd to users, scoped JWTs (`events:write`, `claims:write`, `relationships:write`, `embeddings:write`, `*`), POST handlers gated with `requireScope(...)`. CLI: `mnemos agent create|list|revoke`, `mnemos agent token issue`
- [x] **F.3** — Per-user scope policy: `users.scopes_json`, `mnemos user create --scope <s>`, user JWTs honour the recorded list (legacy users keep `*`)
- [x] **F.4 + F.4.b** — Agent → run_id whitelist: `agents.allowed_runs_json`, JWT `Runs` claim, batch pre-checks on every write endpoint (events directly; claims/relationships/embeddings via evidence join). CLI: `mnemos agent create --run <id>`
- [x] **F.5** — Audit by principal: `mnemos audit who <id> [--since <duration>] [--human]` returns every write attributed to a user/agent/system across events, claims, relationships, embeddings, and `claim_status_history`
- [x] Glob-pattern run scopes — `Claims.AllowsRun` + `domain.Agent.AllowedRuns` accept `*`, exact, and shell-glob patterns (`prod-*`, `nightly-?-2026`, `release/[0-9]*`).
- [x] Agent quota policies — `domain.AgentQuota` (window seconds, max writes, max tokens) + `auth.QuotaTracker` enforces rolling windows in memory; `ErrQuotaExceeded` on overflow.
- [x] Federated agent sync — `AgentRepository.Upsert(batch)` on every backend (sqlite/memory/postgres/mysql); registries push/pull agents like other resources.

---

## axi-go Execution Kernel (SHIPPED)

**Status:** Shipped on `main`
**Goal:** Wrap the MCP tool surface with a uniform DDD execution kernel for governance, audit, and budgets.

### Milestones

- [x] Each MCP tool registered as an axi-go action with effect profile (`read-local`, `write-local`) and idempotency profile
- [x] Tamper-evident SHA-256 evidence chain per session
- [x] Duration / capability-invocation budgets (`MNEMOS_AXI_MAX_DURATION`, `MNEMOS_AXI_MAX_INVOCATIONS`)
- [x] Domain events fanned into bolt as structured `axi_event` log lines
- [ ] Future: LLM token reporting through capability evidence (gates `MaxTokens` budget); persist evidence chain to SQLite for cross-session audit; approval flow for any future write-external tool

---

## Phase 3: Cognitive Infrastructure (FUTURE)

**Status:** Future (v1.0)
**Goal:** Backend standard for enterprise AI and decision systems.

### Milestones

- [x] GraphRAG-style multi-hop queries (supports/contradicts edge expansion)
- [x] Compliance and audit trails (Phase A + F deliver the substrate)
- [x] Bias detection — `internal/bias` ships four indicators (source concentration, polarity skew, temporal cluster, single-source-of-truth) with operator-tunable thresholds.
- [x] **Epistemic Provenance & Claim Trust Framework** (shipped)
  - [x] Claim provenance data model (source doc, authority, liveness)
  - [x] Citation graph & link density tracking (know what converges)
  - [x] Liveness detection (e.g. 12-year-old process doc still being executed = live/zombie)
  - [x] Source credibility scoring engine (link density + liveness + recency + authority)
  - [x] Test provenance model (first-class "test result" as claim with metadata)
  - [x] Test conflict detection (Test1 passes, Test2 fails for same thing)
  - [x] Confidence-weighted conflict resolution (which test/source to trust?)
  - [x] Provenance Query API: "Why trust this claim?" with rationale (`mnemos trust --test <ref>` + `which_test_to_trust` MCP tool)
  - [x] Human-readable provenance markdown export
- [x] Web interface — embedded SPA shipped with `mnemos serve`
- ~~Enterprise integrations (Slack, Teams, Jira)~~ — **out of scope.** A memory layer doesn't host vendor chat integrations; agent runtimes connect to Slack / Teams / Jira via their own MCP servers and tools. Mnemos stays focused on memory + evidence.

---

## Cognitive Stack Simplification (SHIPPED, v0.17.x)

**Status:** Shipped on `main` (v0.17.0 + v0.17.1)
**Goal:** Collapse the five-primitive cognitive stack (mnemos / chronos / nous / praxis / olymp) to three (mnemos / chronos / agent runtimes), make Mnemos embeddable as a Go library, bundle Chronos by default, expose three input modes.

### Milestones

- [x] **Embeddable Go library** — root `mnemos` package with `Memory` interface (`Remember`, `RememberClaim`, `Recall`, `RememberEvent`, `Timeline`, `Close`)
- [x] **Three input modes** — passive (no LLM), shared-provider (agent runtime supplies the model), enhanced (own provider). All three exposed via library, CLI (`mnemos claim record`), and MCP (`remember`)
- [x] **Framework-neutral providers** — `mnemos/providers/` exposes `TextGenerator` + `Embedder` interfaces; agent runtimes implement as thin adapters
- [x] **Chronos bundled by default** — `mnemos.New()` boots an in-process `chronos/embed.Engine`; `RememberEvent` forwards as a presence signal; `WithChronos(c)` overrides
- [x] **Temporal MCP tools** — `remember_event`, `timeline_query`, `recall_at_time` join the existing 25-tool surface
- [x] **decisionkit extracted** — risk + intervention engines lifted from archived nous into [`github.com/felixgeelhaar/decisionkit`](https://github.com/felixgeelhaar/decisionkit) v0.1.0
- [x] **Three sibling repos archived** — [olymp](https://github.com/felixgeelhaar/olymp), [nous](https://github.com/felixgeelhaar/nous), [praxis](https://github.com/felixgeelhaar/praxis) at `*-final` tags
- [x] **ADRs 0003-0006** — full decision record under [`docs/adr/`](./docs/adr/)

---

## Pending Roadmap Items

Captured here for visibility; not in active development.

### Security hardening

- [ ] **A.4 OIDC integration** — deferred until first real OIDC need surfaces
- [x] **JWT `kid` header + key-id-driven verification** — N-key `Keyring` selects the verification secret by header `kid`; `Issuer.IssueAgentTokenWithScopes` stamps the primary kid into every fresh token. Multiple active keys can coexist during rotation; legacy single-secret callers still work via `NewIssuer`/`NewVerifier`. (PR #66)

### Release hardening

- [x] **Supply-chain attestation** — CycloneDX + SPDX SBOMs (generated by `nox scan -format all`), cosign keyless signing for archives + Docker manifests, SLSA Level 3 provenance via `slsa-github-generator`, GitHub artifact attestations, multi-arch docker (linux/amd64 + linux/arm64). (PR #65)
- [ ] **axi-go evidence** — JSONL evidence sink shipped (PR #67) — every kernel event lands in `MNEMOS_AXI_EVIDENCE_LOG` for cross-session audit, fan-out via `multiAxiPublisher` alongside the existing bolt log. Two smaller follow-ups remain: (a) LLM token reporting through capability evidence so `MaxTokens` budgets actually gate, (b) end-to-end approval-flow exercise once a tool with `EffectWriteExternal` exists. SQLite-backed persistence skipped in favor of JSONL+`jq`; revisit only if a query surface beyond `tail -f` becomes worthwhile.

## Out of scope

- **Enterprise chat integrations (Slack, Teams, Jira)** — these are agent-runtime concerns, not memory-layer concerns. Agent runtimes (Claude Code, Codex, Hermes, Nomi, OpenClaw, NanoClaw, ...) already connect to Slack / Teams / Jira via their own MCP servers and tools. Mnemos stays focused on memory + evidence.

---

## Development Principles

1. **Validate before scaling** — Each phase must prove value before expanding
2. **Local-first** — Data stays on user's machine until explicitly shared
3. **Evidence-backed** — Every claim traces to source material
4. **No magic** — Explicit over implicit; simple over complex

## Contributing

See [CONTRIBUTING.md](./CONTRIBUTING.md) for development guidelines.
