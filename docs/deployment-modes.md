# Mnemos deployment & access modes

> Status: draft (2026-07-12) — a working map of how Mnemos is run, who the trust
> boundary is in each case, and what "secure by default" means per mode. Drives
> the `init` scaffolds and the auth defaults. Refine freely.

Mnemos runs in shapes that differ along **two axes**. Everything else (transport,
tokens, TLS) is a cross-cutting toggle.

## Axis 1 — Locality: who is the trust boundary

- **Local.** CLI, MCP over stdio, and a local store (`sqlite://`, local
  `libsql://`, `memory://`). There is no network and no JWT — **the filesystem /
  OS is the boundary.** Whoever can read the DB file has the data. This is the
  personal brain wired into Claude Code.
- **Networked.** `mnemos serve` (REST + gRPC) or `mnemos mcp --http`, usually over
  a networked store (`postgres://`, `mysql://`, remote `libsql://`). **The
  boundary is authentication.** As of v0.85.1 a valid JWT is required by default
  for *every* request, reads included; only infra endpoints (`/health`, `/`,
  `/app`, `/internal/metrics`) are anonymous.

## Axis 2 — Tenancy: isolation within a networked deployment

- **Single-tenant** (default `serve` / `mcp --http`). One shared dataset behind
  auth. Correct for a single app or team backend.
- **Multi-tenant** (`--require-tenant`, ADR 0007). Every request must present a
  **tenant-scoped** token (`tnt`, or a `tnts` allowlist — ADR 0009). Isolation is
  physical: **Postgres row-level security** (`?tenant=`) or
  **namespace-per-tenant** (sqlite / mysql / local libSQL). Backends that cannot
  isolate (`memory://`, remote libSQL) are **refused** in this mode (fail closed).
  The shared, **de-identified neocortex** (promoted `GlobalSchema`s, ADR 0011)
  sits above all tenants and is readable by any authenticated tenant by design.
  This is the mode for a product with many customers (e.g. pet-medical, one
  tenant per clinic).

## The modes

| Mode | Surface | Storage | Auth | Isolation | For |
| --- | --- | --- | --- | --- | --- |
| **A. Personal brain** | CLI + recall/brief/capture hooks + MCP stdio | local sqlite | none (filesystem) | n/a (one user) | a developer's Claude Code |
| **B. Workspace-federated** | A + global∪workspace overlay (ADR 0009/0010) | local sqlite ×N | none (filesystem) | by folder/workspace | multi-repo / Cowork-style work |
| **C. Hosted single-tenant** | `serve` / `mcp --http` | Postgres/MySQL | **JWT required** (reads + writes) | one shared tenant | one app/team's backend |
| **D. Hosted multi-tenant** | `serve --require-tenant` / `mcp --http --require-tenant` | Postgres (RLS) or ns-per-tenant | **tenant-scoped JWT** | per-tenant + shared neocortex | products (pet-medical, kraftsprot) |
| **E. Hybrid (local → hosted)** | local hooks/MCP routed to a remote brain (`init --url --token`) | remote (C or D) | JWT bearer from 0600 config | inherits the remote | your CLI/Claude Code on a hosted brain |
| **F. Consolidation (offline)** | CLI `consolidate --promote [--all-tenants]` | direct store DSN (DB creds) | **DB-credential level, not network** | reads all tenants → writes neocortex | the operator "sleep" pass |

Notes:
- **F is not a network surface.** Cross-tenant promotion runs as an offline
  operator job with direct DB credentials (like `pg_dump`); it is never reachable
  over REST/MCP/gRPC. Its cross-tenant read is inherently privileged and gated by
  who holds the DSN.

## Cross-cutting toggles

- **Auth escapes (explicit, warned — never defaults):**
  - `serve --public-reads` / `MNEMOS_PUBLIC_READS=1` — anonymous **reads** on the
    single-tenant REST/gRPC surface (prints a warning; ignored under
    `--require-tenant`).
  - `mcp --http --no-auth` — all requests anonymous ("trusted networks only");
    **cannot** be combined with `--require-tenant`.
- **Transport security:** TLS via `MNEMOS_TLS_CERT_FILE` / `MNEMOS_TLS_KEY_FILE`;
  mTLS via `MNEMOS_MTLS_CLIENT_CA_FILE`.
- **Identity & tokens:** user tokens (`token issue --user`) and agent tokens
  (`agent token issue`), optionally tenant-scoped (`--tenant` → `tnt`/`tnts`),
  with a TTL and `jti` revocation (denylist). Signed with `MNEMOS_JWT_SECRET`
  (auto-created per-install file when unset — but a hosted deployment should set a
  **persisted** secret so tokens survive restarts).

## One topology at two scales (the brain model)

The local and hosted shapes are the *same* structure — a **sub-region** feeding a
**central brain**, with important knowledge **floating up** (consolidation) and
central knowledge **federating down** at read time. Only the gate on what floats
up changes with scale.

| | Sub-region (hippocampus) | Central brain (neocortex) | Float-back gate |
| --- | --- | --- | --- |
| **Local** (single owner) | a repo / defined folder | the user's personal central brain | **importance / generality** (reusable cross-project learning vs repo-specific detail) + explicit "remember globally" |
| **Hosted** (many owners) | a pet-owner tenant | the product's global brain | **privacy** (individual-vs-class subject) → then **quality** (cross-tenant corroboration for emergent patterns; curator sign-off for novel facts) |

**Local topologies:**
- **Single repo brain** (Mode A) — one isolated brain per repo. Done.
- **Central brain + sub-regions with float-back** — each repo/folder is a small
  area of one central brain; reads federate `central ∪ area` (built), and
  important learnings **float back up** into the central brain (the local twin of
  hosted promotion — **not yet built**; capture currently only writes *into* the
  area).

**Hosted knowledge kinds** (what is worth floating to global, ADR 0011 + the
pet-medical model):
- **Individual-subject** (this pet/owner) → private, never promotes.
- **Class-subject** (a breed, species, disease, a newly-encountered spider) →
  eligible for global. Two paths: *emergent* (a pattern corroborated across many
  private cases, e.g. "Golden Retrievers predisposed to diabetes") and *curated*
  (a novel/authoritative class fact contributed from a single source with a
  curator's sign-off, e.g. the new spider's envenomation profile).
- **Born-global** (optional): reference taxonomy authored straight into the
  neocortex, never passing through a tenant (top-down seeding alongside bottom-up
  float-back).

The consolidation engine is one mechanism; the **gate policy plugged into it is
the per-deployment setting**.

## Secure-default principle

1. **Local = filesystem boundary.** No network auth; correct as-is.
2. **Hosted = a JWT on EVERY exposed endpoint (gRPC *and* HTTP) — an invariant,
   not a default.** No data endpoint is ever anonymous. `/internal/metrics` is
   authenticated or moved to an internal-only port; `/health` is a bare liveness
   `200` with no data; the `--public-reads` and `mcp --http --no-auth` escapes are
   **not available** for a hosted/production deployment (an insecure-local flag,
   if kept at all, must be clearly non-production). (v0.85.1 made reads
   auth-required; the remaining infra-endpoint hardening is pending.)
3. **Multi-consumer ⇒ isolate.** A deployment serving more than one party runs
   `--require-tenant` so data is physically partitioned and every token is
   tenant-scoped.
4. **Encrypt in transit.** TLS for any real deployment.
5. **Persist the signing secret.** So issued tokens survive restarts.

**Consequence for `init --service`:** a hosted *product* deployment is **Mode D**,
so the scaffold should default to `serve --require-tenant` + a required, persisted
`MNEMOS_JWT_SECRET` (+ TLS guidance), rather than a single-tenant `serve`.

## Decided
- Hosted requires auth on every exposed endpoint (gRPC + HTTP) — see principle 2.
- The `init --service` scaffold defaults to **Mode D**: `serve --require-tenant`
  with a required, persisted `MNEMOS_JWT_SECRET`.

## Open questions / to refine
- **Local float-back gate:** what makes a repo/folder learning "important" enough
  to float up to the central brain — generality (cross-project applicability),
  accumulated trust, recurrence across areas, or only an explicit
  "remember globally"? (Local has no privacy gate; it's purely relevance.)
- **Hosted classification:** is individual-vs-class subject classification inferred
  (entities/LLM), asserted by a curator, or both (explicit-wins)? For a *medical*
  product, should nothing promote without a human confirming it's class-level?
- **Curator role:** is "contribute to global" a token scope (a vet capability), so
  not every tenant user can push to the shared brain?
- **Born-global feed:** do we support operator-authored reference taxonomy written
  straight into the neocortex, alongside bottom-up float-back?
- **Defense in depth:** should `serve` bind `127.0.0.1` by default and require an
  explicit `--host 0.0.0.0` to expose externally?
