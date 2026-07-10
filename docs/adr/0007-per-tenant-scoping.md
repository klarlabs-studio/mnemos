# ADR 0007: Per-Tenant Scoping Within a Namespace

- **Status:** Accepted
- **Implemented:** 2026-07-02 — Postgres provider (RLS). Other backends fail closed.
- **Date:** 2026-07-02
- **Deciders:** Felix Geelhaar
- **Scope:** Mnemos storage + public API. Driven by Senat-OS dogfooding.

## Implementation (as shipped)

The shipped design is stronger + lower-risk than the per-repo `WHERE tenant`
predicate first sketched below: **Postgres row-level security**, so isolation is
enforced by the database and no repository SQL was touched (a forgotten predicate
is impossible).

- **`Memory.Tenant(id string) (Memory, error)`** returns a view over the same DSN
  with a `tenant=` parameter. It returns an **error** on any non-Postgres backend
  (memory/sqlite/…) rather than silently sharing data — fail-closed. (The signature
  gained an `error` vs the original sketch for exactly this.)
- The Postgres provider pins the tenant as a per-connection GUC (`SET
  mnemos.tenant = '<id>'`, alongside `search_path`, in the pgx `AfterConnect` hook).
- Every tenant-scoped table (21 of them; `users`/`revoked_tokens` excluded as auth
  infra) gets a `tenant text NOT NULL DEFAULT current_setting('mnemos.tenant', …)`
  column, **`ENABLE` + `FORCE ROW LEVEL SECURITY`**, and a policy
  `USING/WITH CHECK (tenant = current_setting('mnemos.tenant', true))`. The column
  DEFAULT stamps writes; RLS filters reads/writes. Applied idempotently in
  `schema.sql`.
- **Fail-closed everywhere:** a connection without the GUC gets `NULL` → RLS denies
  every row and the `NOT NULL` default rejects every insert. An unscoped `Memory`
  uses the reserved `__default__` tenant, so pre-existing single-tenant data (which
  backfills to `__default__`) stays reachable.
- **`FORCE` is required** so RLS applies to the table-owning role too. **Caveat:**
  superusers and `BYPASSRLS` roles bypass RLS unconditionally — a deployment MUST
  connect as a non-superuser role. The isolation test skips (loudly) if the role
  bypasses RLS.

Follow-up (shipped, opt-in): the shared-pool mode is now implemented behind
`MNEMOS_PG_SHARED_POOL`. Instead of a pool per tenant, one shared pool is opened
per (DSN, namespace); a tenant request checks out a `*sql.Conn`, pins
`SET mnemos.tenant` on it, and the Conn's Closer runs `RESET mnemos.tenant`
before releasing it — fail-closed if ever reused unset. Repositories accept a
`pgQuerier` interface so the same code serves either a pooled `*sql.DB` (default
per-tenant-pool path) or a checked-out `*sql.Conn` (shared path). The default is
unchanged. Guarded by `TestPostgres_CrossTenantIsolation` (both modes; skips on
an RLS-bypassing role). Other backends (sqlite/libsql/mysql) can adopt tenant
scoping later behind the same `Tenant()` API + isolation test; until then they
fail closed.

---

*Original proposal (kept for context):*

## Context

ADR 0001 gave every store a `?namespace=` that maps to a **coarse** isolation
unit: a Postgres *schema* or a SQLite *file*. That is the right boundary for
"four cognitive tools sharing one database server."

It is the *wrong* boundary for a single multi-tenant **application process** that
must isolate many end-customers (orgs). Senat-OS is exactly this: one runtime
serves every org, and each org's worker memory must be invisible to the others.
mnemos's public API (`Remember` / `Recall` / `RememberEvent`) carries **no
tenant**, so the only lever is namespace = store instance. Senat therefore keeps a
**store-per-org cache** (`memory.StoreProvider.For(orgID)` → one namespaced
mnemos per org).

That works and is shipping, but it has costs the namespace model forces:

- **Postgres:** one `*sql.DB` **pool per org** (each `Open` builds its own pool).
  A few orgs is fine; hundreds means hundreds of pools and connection pressure.
- **SQLite:** one **file per org** (namespace folds into the filename), so no
  single-store multi-tenant option at all.
- No way to run a query *across* a tenant set for platform-level analytics
  without opening every store.

We want a **fine-grained**, in-namespace tenant boundary so one store (one pool,
one schema) can safely serve many tenants.

## Decision drivers

1. **Default-deny isolation.** A read with tenant T must never return another
   tenant's rows. A *missing* tenant must not act as a wildcard.
2. **Ergonomics.** Avoid churning every method signature; keep the `Memory`
   surface familiar.
3. **Compose with, not replace, namespaces.** Namespace stays the coarse
   DB/schema boundary; tenant is the fine row boundary *within* a namespace.
4. **Cross-provider.** The contract must hold for postgres, sqlite, libsql, mysql,
   memory.

## Options

**A. Status quo — namespace per store (what Senat does today).**
Simple, already works. But N pools / N files; no single-store multi-tenant.

**B. Per-call tenant parameter** on `Remember`/`Recall`/`RememberEvent` and a
`ClaimItem.Tenant` / `Query.Tenant` / `Event.Tenant` field.
Explicit, but churns every write/read call site in every consumer and is easy to
forget (forgetting = a leak). Fails driver 2.

**C. Context-scoped tenant** (`mnemos.WithTenant(ctx, id)`).
No signature churn, but implicit — a caller that forgets to scope the context
reads globally. Fails driver 1 (not default-deny).

**D. Tenant-scoped view (RECOMMENDED).**
`Memory.Tenant(id string) Memory` returns a lightweight view over the **same**
connection/pool that (a) stamps `tenant = id` on every write and (b) filters
`tenant = id` on every read. The base `Memory` (no tenant) is treated as its own
reserved tenant (e.g. `""` → `__default__`), never a wildcard. One pool, one
schema, many tenants; the whole existing `Memory` surface works unchanged on the
view.

## Decision (proposed)

Adopt **D**, backed by a persisted `tenant` column:

- **API:** add `Tenant(id string) Memory` to the `Memory` interface. It returns a
  view sharing the underlying `store.Conn`; `id` is validated (same charset as
  namespace) and non-empty. The unscoped `Memory` maps to a reserved default
  tenant.
- **Storage:** add a `tenant TEXT NOT NULL DEFAULT '__default__'` column to every
  tenant-scoped table (claims, events, embeddings, decisions, lessons, playbooks,
  entities, relationships, …), plus a composite index `(tenant, …)` on the hot
  query paths. Every repository query gains an unconditional `WHERE tenant = $tenant`
  (writes set it). **Default-deny:** the tenant predicate is never optional.
- **Governance:** the axi write session stamps the tenant, so the evidence chain
  records which tenant a write belonged to.
- **Migration:** existing rows backfill to `__default__` (they were single-tenant).
  A new store schema version + `IF NOT EXISTS` column adds keep re-opens no-ops
  (consistent with ADR 0001's idempotent bootstrap).

Namespace (schema/file) remains the coarse boundary; tenant is the fine boundary
within it. A deployment can use either or both (Senat would collapse its
store-per-org cache into one Postgres store with `mem.Tenant(orgID)`).

## Consequences

- **Senat** drops `StoreProvider`'s per-org pool cache → one shared pool,
  `mem.Tenant(orgID)` per run. Removes the N-pools cost.
- Every repository query must include the tenant predicate — enforced by review +
  a test that asserts cross-tenant reads return nothing (the same guarantee Senat
  already tests at the store-instance level).
- Slightly larger rows + indexes. Acceptable.
- Backwards compatible: unscoped `Memory` keeps working (maps to the default
  tenant); `Tenant` is additive.

## Risks

- **A missing `WHERE tenant` is a silent leak.** Mitigation: centralise the
  predicate (a query helper every repo funnels through), and a mandatory
  cross-tenant-isolation test per provider before this ships. Isolation is the
  one thing we do not rush — hence this ADR rather than a same-day patch.

## Status / next step

Proposed. Implementation is a provider-wide change (schema + every repo + the
public view) and should land provider-by-provider behind the isolation test,
starting with Postgres (the backend that motivates it). Until then, consumers use
namespace-per-store (Option A), which is correct — just heavier.
