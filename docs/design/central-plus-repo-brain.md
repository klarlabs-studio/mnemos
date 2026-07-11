# Brainstorm: a central brain that also holds repo-isolated knowledge

> **Status: v1 implemented** (Option A, repo-first routing). The recall/brief/
> capture hooks are now repo-aware via `ev.Cwd`: recall federates global ∪ repo
> (repo wins, tagged `{repo}`/`{global}`), capture routes to the repo brain when
> inside an opted-in repo (`mnemos init --project`), and brief reports both
> counts. Follow-ups still open: the MCP `scope` param, the "remember globally"
> escape hatch, and the committed-repo-brain share format (markdown vs binary
> `.db`) — see Open questions.

**Goal.** Keep the single central/global brain that follows you across every
Claude Code session, but let knowledge that *belongs to a repo* stay with that
repo — surfaced when you work there, invisible everywhere else, and (optionally)
committable so it travels with the code and the team.

---

## v2 direction: repo-as-tenant + an Agent-OS markdown source-of-truth

Two refinements sharpen v1 and resolve its biggest open question.

### 1. The repo tier IS a tenant

mnemos already has the isolation machinery (ADR 0007): a tenant key, `Tenancy
ModeForDSN`, `TenantNamespace(tenant)`, Postgres RLS + namespace-per-tenant for
sqlite/mysql/libsql, the `tnt` JWT claim, `--require-tenant`. So model the
**repo as a tenant** and separate two concerns that v1 conflated:

- **Tenant key** (identity): what names "this repo" — a git remote URL
  (stable across clones, shareable) or the repo-root path (works offline). This
  is the federation key: recall = `tenant:default (global) ∪ tenant:<repo>`.
- **Physical placement** (storage): where that tenant's rows live —
  - *local file* `<repo>/.mnemos/mnemos.db` (v1; personal, per-repo), or
  - *namespace/RLS inside a central store* (`mnemos serve` on Postgres; shared
    across your machines and teammates, one DB, isolated per repo-tenant).

Same federation logic, two backends. v1 is the local-file case of this more
general model; a hosted central brain becomes "one store, one tenant per repo,
plus the default (personal) tenant." A team/org is just another tenant level.

### 2. Markdown is the source of truth; the `.db` is a derived index

This is the Agent-OS technique, and it dissolves the "SQLite conflicts badly in
git" problem. Agent OS keeps durable knowledge as **human-editable, append-only
markdown** — `AGENTS.md` (identity), `decisions.md` (append-only; superseded
entries get `→ superseded [date]`, never deleted), `open-threads.md`,
`status.md` — with a brief/capture/compact session lifecycle. mnemos already has
a markdown round-trip layer (lessons/playbooks ↔ YAML-frontmatter).

Marry them for the repo tier:

- **Commit the markdown, gitignore the `.db`.** `<repo>/.mnemos/*.md` is the
  reviewable, diff-able, PR-able source of truth; `<repo>/.mnemos/mnemos.db` is
  a local, rebuildable index (`mnemos import` / a `rebuild` step). No binary
  merge conflicts; the repo brain travels with the code as text.
- **Map the Agent-OS files onto mnemos concepts:**
  - `AGENTS.md` → repo identity/context, injected at **brief**.
  - `decisions.md` (append-only + supersession) → mnemos **decisions/claims**
    with `resolve --supersedes` / temporal `valid_to` (the model already exists).
  - `open-threads.md` / `status.md` → a repo-scoped **working-memory** surface
    that brief reads ("3 open threads in this repo").
  - session **capture** writes both the claim (into the index) and, optionally,
    a human session log — same as Agent OS `/capture`.
  - **compaction** (`/mem-compact`) → mnemos `consolidate` already digests +
    dedupes; point it at the repo tier on a schedule.
- **Staleness:** adopt the Agent-OS `updated:` frontmatter + "verify if
  untouched N days" so stale repo knowledge is flagged, not trusted blindly.

### What this changes about v1

v1 (local binary `.db` overlay, repo-first routing) stays the fast path and the
default. v2 adds: (a) a tenant abstraction so the same federation works against
a hosted central store, and (b) a committed-markdown source-of-truth so a repo
brain is shareable as text. They compose — the `.db` is always derivable from
the markdown + captured sessions.

### Decisions this raises

1. **Tenant key:** git remote URL vs repo-root path (or: remote when present,
   path hash offline). Determines shareability and clone-portability.
2. **Markdown schema:** adopt the Agent-OS file set verbatim
   (`AGENTS.md`/`decisions.md`/`open-threads.md`/`status.md`) vs a mnemos-native
   export shape vs a bridge that reads both.
3. **Rebuild ergonomics:** `mnemos import` on demand, a hook that rebuilds the
   index when the markdown is newer, or a `mnemos rebuild` command.
4. **Central-store placement:** for the hosted case, repo-tenant via Postgres
   RLS (one DB) vs namespace-per-tenant — mnemos already supports both.

Two kinds of memory, one workflow:

- **Central (personal):** cross-cutting preferences, general facts, decisions
  that aren't tied to one codebase. Lives in `~/.local/share/mnemos/mnemos.db`.
- **Repo-scoped:** "this service uses Kafka for the event backbone", "we chose
  gRPC over REST here", incident post-mortems, project conventions. Should
  surface in *this* repo and never leak into an unrelated one.

## What already exists (so the lift is small)

- **Project brains + walk-up.** `mnemos init --project` writes a repo brain at
  `<repo>/.mnemos/mnemos.db`, and `findProjectDB` resolves it by walking up from
  the CWD (now correct after ADR 0008 — `$HOME` is a hard stop, so repo brains
  are strictly below home and never collide with the global one).
- **Hooks already know the CWD.** The Claude Code hook payload includes `cwd`
  (`hookEvent.Cwd`) — recall/capture just don't use it yet.
- **A `--db` global flag** (new) lets any manual command target either brain.
- **`Scope{Service, Env, Team}`** on claims/lessons + a query scope filter — a
  logical-partition primitive already wired through the engine.
- **Namespaces (ADR 0007)** — physical isolation within one backend.

The missing pieces are a **routing policy** (which brain a write goes to) and a
**federated read** (recall from global + current repo together).

## The design options

### Option A — Federated two-tier brains (RECOMMENDED)

Global brain stays the base layer. Each repo optionally has its own
`<repo>/.mnemos/mnemos.db` overlay. The hooks become repo-aware using `ev.Cwd`:

- **Recall** (`UserPromptSubmit`): resolve the repo brain from `ev.Cwd`; query
  **both** the global brain and the repo brain; merge, de-dup, re-rank; tag each
  injected claim with its source (`[global]` / `[repo:<name>]`). On
  contradiction, the repo claim wins (local context overrides the general).
- **Capture** (`SessionEnd`): route to the repo brain when the CWD is inside a
  repo that has opted in (a `.mnemos/` exists); otherwise the global brain.
- **Brief** (`SessionStart`): "Mnemos: 412 global claims + 37 for this repo."

The global hook registration stays pinned to the global DSN (unchanged); the
repo DSN is discovered per-invocation from `ev.Cwd`. No re-registration.

**Why this one:**
- **Physical isolation** — a repo's knowledge is a file in the repo; a client
  project's memory never touches your global store.
- **Brain-as-code** — `.mnemos/mnemos.db` can be *committed*. Your repo's
  accumulated decisions, lessons, and playbooks become versioned, PR-reviewable,
  and shared with teammates who clone it. (Or `.gitignore` it to keep it
  private.) This is a genuinely new capability and fits mnemos's "evidence layer
  as code" identity.
- Builds directly on primitives that already exist and are now correct.

**Costs:** recall does two queries + a merge (fine — both are local SQLite);
cross-store contradiction detection only runs *within* each store at write time
(the read-time merge still surfaces both and flags the conflict); a write-
routing policy has to be defined (below).

### Option B — One brain, a `repo` scope dimension (lighter)

Add `Repo` to `Scope` (or a dedicated tag). Everything captured in a repo is
tagged with a stable repo identity (git remote URL, or a hash of the repo root).
Recall filters to `repo IS NULL (global) OR repo = <current>`, hiding other
repos' claims.

- **Pros:** one file, one backup; cross-repo queries are possible when you *want*
  them; contradiction detection spans everything; no merge logic.
- **Cons:** isolation is only logical — every repo's knowledge sits in the global
  file (no per-repo privacy, no committable repo brain); needs disciplined
  tagging on every write and a filter on every read.

### Option C — Namespaces per repo

Use the ADR-0007 namespace primitive: global = namespace `mnemos`, each repo = a
derived namespace, federated read across the two. For SQLite,
namespace-per-tenant already means separate files — so this collapses into
Option A with more machinery. Most useful if the central brain is a shared
Postgres/hosted server rather than local SQLite.

## Recommendation

**Option A (federated two-tier), with an explicit, simple write-routing policy.**
It delivers physical isolation *and* the committable-repo-brain feature, and it's
mostly wiring on top of what's already there. Option B is the fallback if you'd
rather keep a single file and treat isolation as a view.

## The one real decision: where do writes go?

This is the crux and worth your call. Candidates:

1. **Repo-first (recommended default):** if the CWD is in a repo *with* a
   `.mnemos/` (opted in via `mnemos init --project`), session capture goes to the
   repo brain; otherwise global. Explicit, predictable, no surprise files.
2. **Auto-repo:** auto-create `<repo>/.mnemos/` on first capture inside any git
   repo. Zero-config, but litters repos with brain files and needs a
   `.gitignore` nudge.
3. **Classified split:** an extractor tags each claim "repo-specific" vs
   "general" and routes per-claim (general → global, specific → repo). Most
   magical, heaviest, and fuzziest — probably a later refinement, not v1.

Plus an **escape hatch** regardless of default: a phrase like *"remember
globally: …"* (or an MCP `process_text(scope: "global")` param) always promotes
to the central brain, and `query`/recall take an optional `scope: global | repo
| both` (default `both`).

## Phased plan (if we go with A + repo-first)

1. **Repo resolution helper** shared by the hooks: `ev.Cwd` → repo brain DSN (or
   none). Reuse `findProjectDB` semantics rooted at `cwd`, not the process CWD.
2. **Federated recall:** query global + repo, merge/re-rank, source-tag the
   injection. Add `scope` to the recall path.
3. **Routing on capture:** repo-first policy + the "remember globally" escape
   hatch.
4. **Brief:** two-line summary (global + repo counts).
5. **MCP parity:** `scope` param on `query_knowledge`/`process_text`; a
   `configure_environment` option to set the default routing.
6. **Docs:** "commit your repo brain" workflow + `.gitignore` guidance.

## Open questions

- **Repo identity for Option B / tagging:** git remote URL (stable across
  clones, but absent on local-only repos) vs repo-root path hash (works
  offline, breaks if the repo moves). Federated A sidesteps this — the file's
  location *is* the identity.
- **Committed repo brains & merge conflicts:** a binary SQLite file conflicts
  badly in git. If we want truly shareable repo brains, the markdown
  export/import layer (lessons/playbooks already round-trip to YAML-frontmatter
  markdown) may be the better share format — commit the *markdown*, rebuild the
  `.db` locally. Worth deciding early.
- **Contradiction across tiers:** surface-and-flag at read time (cheap) vs a
  periodic cross-tier `consolidate` (thorough). Start with read-time flagging.
- **Precedence:** repo-wins-on-conflict is the proposed default; confirm that
  matches intuition (local context overrides general knowledge).
