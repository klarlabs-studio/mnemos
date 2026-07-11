# ADR 0010: User-selected workspaces (the Cowork model)

- **Status:** Accepted; implemented
- **Date:** 2026-07-11
- **Deciders:** Felix Geelhaar
- **Scope:** mnemos CLI + the Claude Code hooks / MCP repo-federation path. Builds
  on the two-tier brain (ADR-0008-era) and repo-as-tenant (ADR 0009).

## Context

The two-tier brain scoped its second tier to a **git repo / single folder**,
resolved implicitly by walking up from the session cwd to the nearest `.mnemos`
directory. Two limits surfaced:

1. **The unit is really a folder, not a repo.** `mnemos init --project` already
   creates `<folder>/.mnemos/mnemos.db` with no git requirement — "repo" was only
   a label. But the boundary should be able to span **several folders** and carry
   a **portable, explicit identity** (a path isn't shareable; a folder may not be
   a git repo).
2. **Selection was implicit.** Claude Cowork — the reference model — lets the
   **user select** the working location ("Work in a folder") and organizes work
   into **Projects**: a named unit bundling *one or more folders*, standing
   *instructions*, and a *project-scoped memory store* that persists across
   sessions. mnemos already has the memory + evolving-instructions half (the repo
   brain + the AGENTS.md managed block with two-way sync); it lacked the
   user-selected, multi-folder, named unit.

## Decision

Introduce a **workspace**: a user-created, **named** unit mapping **one or more
folders** to **one brain**, federated with the global brain — mnemos's analogue
of a Cowork Project. A git repo is just one kind of workspace.

**Selection is registry + folder-resolved.** `mnemos workspace create <name>
--folder A --folder B` records the mapping in `~/.config/mnemos/workspaces.yaml`.
A session's cwd **activates whichever workspace owns it** — the registered
workspace whose folder is cwd or its nearest ancestor (most-specific path wins).
So the user defines the workspace once (like picking a Cowork Project) but never
has to re-select it per session.

- **Brain:** a workspace spans folders, so its brain lives centrally
  (`~/.local/share/mnemos/workspaces/<name>.db`) rather than in one folder; the
  global `--db` overrides it. `.mnemos`-per-folder brains still work — the hooks
  consult the registry first, then fall back to the walk-up (backward compatible).
- **Identity:** the explicit name derives a portable hosted tenant
  (`deriveHostedTenant(name)`, ADR 0009) — the same across machines, so a
  workspace can be shared with teammates without git.
- **Federation:** unchanged. `repoBrain(cwd)` now returns the workspace brain (+
  its matched folder as the AGENTS.md root) when a workspace owns cwd; recall
  federates `global ∪ workspace`, capture routes to the workspace, and the MCP
  `scope` param and sync-docs/rebuild all ride the same resolver. User-facing
  wording is "workspace" (a repo is one kind).

## Mapping to Claude Cowork

| Cowork | mnemos |
| --- | --- |
| Project (user-selected) | workspace (`workspace create/use-by-folder`) |
| Project's folders (1+) | workspace folders (1+) |
| Project memory store | workspace brain |
| Folder instructions (Claude edits) | AGENTS.md managed block + sync-back |
| Global instructions | global brain |

## Consequences

**Positive**
- The scope unit generalizes from "git repo" to "any folder(s) the user groups",
  with an explicit shareable identity — matching how people actually organize work.
- Zero migration: existing `.mnemos` repo brains keep working (walk-up fallback);
  a workspace is opt-in and takes precedence only where its folders match.
- One resolver (`repoBrain`) feeds hooks + MCP + sync-docs, so the whole
  federation stack gains workspaces for free.

**Negative / open**
- The registry is a per-machine file; sharing a workspace *definition* across
  machines (not just its hosted tenant) is manual for now.
- A `workspace use <name>` explicit override (pin regardless of cwd) is not built
  — folder-membership is the only activation. Add later if needed.
- Internal identifiers (the MCP `scope=repo` enum, `claim_provenance=repo`) still
  say "repo"; only user-facing recall/brief wording was renamed to "workspace".
