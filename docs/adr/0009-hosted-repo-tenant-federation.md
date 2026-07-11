# ADR 0009: Repo-as-tenant in a hosted central brain, with federated reads

- **Status:** Accepted (design); Phases 1, 2 & 3 implemented
- **Date:** 2026-07-11
- **Deciders:** Felix Geelhaar
- **Scope:** Mnemos hosted mode (`mnemos serve` / `mcp --http --require-tenant`)
  + the Claude Code hooks' hosted path. Builds on ADR 0007 (per-tenant scoping)
  and the local two-tier brain (ADR-0008-era work).

## Context

The local two-tier brain federates a global brain with an opt-in per-repo brain
(`<repo>/.mnemos/mnemos.db`): recall returns `global ∪ repo`, capture routes
repo-first. That works because the two brains are two local SQLite files the
one-shot hooks can open in turn.

We also support a **hosted** central brain: `mnemos serve` (REST + gRPC) or
`mcp --http`, optionally `--require-tenant`, where every request scopes to its
JWT `tnt` claim via Postgres **row-level security** (or namespace-per-tenant for
sqlite/mysql/local libSQL) — ADR 0007. In this mode the store is one database
shared across tenants, and RLS **isolates each request to a single tenant**.

The open question (flagged when the local tier shipped): **how does the two-tier
model work against a hosted brain?** Two sub-questions:

1. What *is* a repo, physically, in a hosted store? (Not a local file.)
2. How does a client read `global ∪ repo` when RLS isolates a token to one
   tenant?

## Decision

**A repo is a tenant.** Its tenant id is *derived deterministically from the
repo's identity* — the git remote URL when present (portable across clones and
teammates), else the repo-root path — via `deriveHostedTenant` (a charset-safe
`repo_<slug>_<hash>` id, mirroring `store.TenantNamespace`). Physical placement
is then ADR 0007's job: Postgres RLS row, or a namespace-per-tenant
schema/db/file. The user's cross-cutting knowledge is their **default/personal
tenant**.

**Federation is client-side over two tenant-scoped requests**, mirroring the
local two-brain model: the client reads its personal tenant and the repo tenant
separately and merges (repo wins on conflict, claims tagged by tier). RLS stays
strictly single-tenant per request — we never weaken isolation. The client is
authorized for both tenants via a **tenant allowlist** on its token (a `tnts`
claim, exactly analogous to the existing per-run allowlist `Runs`): the auth
hook admits a request whose selected tenant is in the token's allowlist, and
denies anything else (fail-closed).

Writes route **repo-first**: a session inside a repo captures to the repo
tenant; a "remember globally" path targets the personal tenant.

## Detailed design

### Tenant identity (Phase 1 — implemented here)

- `repoTenantKey(dir)` → git remote (origin, else first remote), else `path:<root>`.
- `deriveHostedTenant(repoKey)` → `repo_<slug>_<sha6>`, a valid `tnt`
  (`^[A-Za-z0-9_.:-]{1,128}$`, never `__default__`), stable per repo key.
- `mnemos repo-tenant` prints the current repo's key + derived tenant, and the
  `token issue --tenant <id>` line to mint a token for it. This is the operator's
  entry point: derive the repo's tenant, mint tokens, point the repo at the
  hosted brain.

### Token: a tenant allowlist (Phase 2)

Add a `tnts []string` claim (mint with `token issue --tenant A --tenant B` or
`--tenants A,B`), mirroring `Runs`. In `--require-tenant` mode:

- The request selects a tenant (a header / MCP arg / gRPC metadata:
  `X-Mnemos-Tenant`).
- The auth hook admits the request iff the selected tenant ∈ `tnts` (or, for a
  single-tenant token, equals its `tnt`); else 403. RLS then isolates to the
  selected tenant as today. No change to the isolation guarantee — only *which*
  of the caller's allowed tenants this request uses.

A client federating `global ∪ repo` thus holds one token allowing
`{personal, repo_<...>}` and issues two requests.

### Client + hooks (Phase 3)

- `internal/client` gains a per-request tenant option (sets the tenant header).
- The hosted hook path (`recall`/`brief`/`capture` when `MNEMOS_URL` is set)
  federates: derive the repo tenant from `ev.Cwd`, issue the personal-tenant and
  repo-tenant reads, merge (reusing the same repo-wins/tier-tag logic as the
  local path and the MCP `scope` merge). Capture routes repo-first.
- `mnemos init --url … --project` scaffolds a repo-scoped hosted setup: it
  records the repo tenant so the hooks pass it.

### Team sharing

Because a repo tenant is keyed by the git remote, every teammate who clones the
repo derives the **same** tenant id. Granting a teammate a token whose `tnts`
includes `repo_<...>` gives them the shared repo brain — the hosted analogue of
committing the repo brain. The personal tenant stays private.

## Consequences

**Positive**
- One coherent model: local file overlay and hosted RLS/namespace are two
  placements of the same "repo = tenant" abstraction; the federation + merge
  logic is shared.
- RLS isolation is never weakened — federation is two isolated reads, merged by
  the client.
- Team-shared repo brains fall out of the tenant-allowlist token.

**Negative / risks**
- Two requests per federated read (latency); mitigated by the ADR-0007 per-tenant
  connection cache and small TopK.
- A tenant-allowlist token is a broader credential than a single-tenant one —
  scope it to `{personal, repo}` only, keep TTLs short.
- Cross-tenant contradiction detection isn't automatic (each tenant detects
  within itself); the client-side merge surfaces both tiers and can flag
  overlaps, but a cross-tier `consolidate` is out of scope.

## Alternatives considered

1. **Repo as a scope *within* the user's single tenant** (a `repo` dimension on
   `Scope`, filtered with a `WHERE`). Simplest — no cross-tenant reads — and fine
   for a purely personal hosted brain. Rejected as the primary model because it
   gives no per-repo physical isolation and **no team sharing** (a repo can't be
   granted to a teammate independently). Kept as a valid deployment option for
   single-user hosted brains.
2. **Server-side federated read** (one endpoint reads personal + repo and
   merges). Moves the merge server-side and needs the server to hold/relax RLS
   across two tenants in one request — more surface, more ways to leak. The
   client-side two-request approach keeps each query strictly single-tenant.
3. **A repo tenant per (user, repo)** rather than per repo. Rejected: defeats
   team sharing (each teammate would get a different tenant for the same repo).

## Phasing

- **Phase 1 (this ADR):** tenant derivation (`deriveHostedTenant`) + the
  `mnemos repo-tenant` command. No behavior change to existing hosted mode.
- **Phase 2 (implemented):** the `tnts` token allowlist (`Claims.Tenants` +
  `AllowsTenant`/`EffectiveTenant`, minted via `token issue --tenant A --tenant
  B` / `--tenants A,B`) and per-request tenant selection — an `X-Mnemos-Tenant`
  header (REST/MCP-HTTP) or `x-mnemos-tenant` gRPC metadata, validated against
  the grant and fail-closed in all three auth paths. RLS stays single-tenant per
  request. Guarded by `TestEffectiveTenant_FailClosed`,
  `TestGRPC_TenantAllowlist_SelectionAndDenial`, and the existing cross-tenant
  isolation suite.
- **Phase 3 (implemented):** the `client` package gained `WithTenant(ctx,
  tenant)`, which sets the `X-Mnemos-Tenant` header per request (blank = the
  token default); the server side already honored it (Phase 2). The hosted hook
  path now federates: `hostedWorkspaceTenant(cwd)` derives the repo/workspace
  tenant (named workspace → `deriveHostedTenant(name)`, else the nearest `.mnemos`
  repo's git-remote key), and recall/brief issue a personal-tenant read plus a
  workspace-tenant read, merged with the same repo-wins/tier-tag logic as the
  local path; capture routes to the workspace tenant (personal when outside one).
  `mnemos init --url --project` writes the `.mnemos` marker that opts a repo into
  federation and prints the derived tenant so the operator can grant it in the
  token's `tnts`. Guarded by `TestClient_TenantHeader` and
  `TestHostedWorkspaceTenant`.
