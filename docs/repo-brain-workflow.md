# Central + repo-isolated brain workflow

How to run mnemos as a **two-tier memory**: one global brain that follows you
across every session, plus an optional **repo brain** that keeps a project's
learnings with the project — committed, human-editable, and re-buildable.

## 1. Overview — two tiers

| Tier | Location | Scope | Purpose |
|------|----------|-------|---------|
| **Global** | `~/.local/share/mnemos/mnemos.db` | You, everywhere | Cross-project facts, decisions, and habits. Wired into Claude Code via the `recall` / `brief` / `capture` hooks and the `mnemos` MCP server. |
| **Repo** | `<repo>/.mnemos/mnemos.db` | One repository | Project-specific truth that should travel with the code and be shared with teammates and agents. |

The hooks are **repo-aware** — they read the session's working directory to
decide which brain applies:

- **recall** federates **global ∪ repo**. Repo claims are tagged `{repo}`,
  global claims `{global}`, and on conflict **repo wins**.
- **capture** routes **repo-first**: a session inside an opted-in repo captures
  to that repo's brain; otherwise it captures to the global brain.
- **brief** reports how many repo-scoped claims apply, e.g.
  `+3 claim(s) scoped to this repo`.

You always have the global brain. A repo brain is opt-in, per project.

## 2. Opt a repo in

Run once inside the repository:

```bash
mnemos init --project
```

This creates `<repo>/.mnemos/mnemos.db` — the repo's local brain. From then on,
sessions started inside this repo federate global ∪ repo on recall and capture
repo-first.

## 3. What gets committed vs ignored

- **Commit** `AGENTS.md` (or `CLAUDE.md`) — the human-editable projection of the
  repo brain. This is the shareable, always-current source that teammates and
  agents read.
- **Do not commit** `.mnemos/mnemos.db` or the sync-back hash files
  `.mnemos/.*.sha`. The `.db` is a **derived, rebuildable index** — a binary
  file that produces merge conflicts and carries no information that isn't
  reconstructable from the committed `AGENTS.md`.

Add to `.gitignore`:

```gitignore
# mnemos repo brain — local, rebuildable index (never commit)
.mnemos/mnemos.db
.mnemos/.*.sha
```

Rule of thumb: **the brain is the source of truth, `AGENTS.md` is its committed
projection, and the `.db` is a local cache** you can throw away and rebuild.

## 4. The managed block

`mnemos sync-docs` writes the repo's high-signal learnings — decisions,
top-trust facts, and open questions — into an agent-facing markdown file inside
a **delimited managed block**. Everything between the markers is generated;
everything outside is yours.

```markdown
# AGENTS.md

Hand-written project notes live up here and are never touched by mnemos.

<!-- mnemos:begin (generated — edits inside are re-synced into the brain) -->
<!-- tenant: git@github.com:acme/widgets.git -->

## Decisions
- Use Postgres row-level security for multi-tenant isolation (ADR 0007).
- Builds are pure Go (CGO_ENABLED=0) via modernc.org/sqlite.

## Facts
- The query engine falls back from cosine similarity to token overlap when no
  embeddings are present.

## Open questions
- Do we need per-tenant connection pooling under the shared-pool mode?
<!-- mnemos:end -->

More durable prose down here is also preserved across syncs.
```

- The block **header stamps the repo tenant identity** — the git remote URL if
  there is one, otherwise the repo path.
- **Edit inside the markers** → your changes are synced back into the brain on
  the next `brief` (see below).
- **Put durable prose outside the markers** → it is preserved verbatim and never
  overwritten.

`sync-docs` targets `AGENTS.md` by default; use `--claude` to target
`CLAUDE.md`, or `--file <name>` for a custom filename. It also runs
**automatically after a repo-scoped session capture**, so the committed file
stays current without a manual step. Because Claude Code and other agents
auto-load `AGENTS.md` / `CLAUDE.md`, these learnings are followed natively.

## 5. The two-way loop

```
   ┌─────────────────────────────────────────────────────────┐
   │                                                         │
   │   repo brain (.mnemos/mnemos.db — the repo tenant)      │
   │                                                         │
   └───────┬──────────────────────────────────▲─────────────┘
           │                                    │
   sync-docs / capture regen          brief sync-back
   (brain → markdown)                 (markdown → brain)
           │                                    │
           ▼                                    │
   ┌─────────────────────────────────────────────────────────┐
   │   AGENTS.md managed block  ──►  human edits inside it     │
   └─────────────────────────────────────────────────────────┘
```

1. **Brain → AGENTS.md.** `mnemos sync-docs` (or the automatic regen after a
   repo-scoped capture) writes decisions, top-trust facts, and open questions
   into the managed block.
2. **Human edits.** You refine wording or add a line inside the markers and
   commit the file.
3. **AGENTS.md → brain.** At session start, `brief` detects edits **inside** the
   managed block by comparing against a content hash stored in
   `.mnemos/.<file>.sha`, and ingests the changed lines into the repo brain.
   The pipeline's dedup collapses lines the brain already knows.

**Safety:** sync-back never overwrites your edits. A note that didn't extract
into a claim is not wiped — the managed block is only regenerated by an explicit
`mnemos sync-docs` or by the capture regen, never silently during sync-back.

## 6. Cloning a repo

After `git clone`, `.mnemos/mnemos.db` is absent (it's gitignored). The
committed `AGENTS.md` still carries the full projection of the repo brain, so
rebuild the local index once:

```bash
git clone <repo> && cd <repo>
mnemos rebuild
```

`mnemos rebuild` reconstructs `.mnemos/mnemos.db` from the committed `AGENTS.md`
mnemos block. Run it **once** after cloning a repo that has a committed
`AGENTS.md` block; afterward the two-way loop resumes normally.

## 7. Manual commands cheat-sheet

```bash
# Opt a repo in — create <repo>/.mnemos/mnemos.db
mnemos init --project

# Regenerate the managed block in AGENTS.md from the repo brain
mnemos sync-docs

# ...target CLAUDE.md instead, or a custom filename
mnemos sync-docs --claude
mnemos sync-docs --file NOTES.md

# Rebuild the local .db index from a committed AGENTS.md (run once after clone)
mnemos rebuild

# Inspect a specific brain directly
mnemos query "how do we isolate tenants?" --db .mnemos/mnemos.db
mnemos metrics --db .mnemos/mnemos.db
```
