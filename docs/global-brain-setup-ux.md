# Setting up mnemos as a global brain for Claude Code — UX walkthrough

A dogfooding pass at wiring mnemos into every Claude Code session as a persistent
"global brain," documenting the real user experience and the defects found and
fixed along the way.

**Date:** 2026-07-11 · **Install:** Homebrew (`klarlabs-studio/tap`) · **Brain:**
local SQLite at `~/.local/share/mnemos/mnemos.db` · **LLM:** Ollama `qwen2.5:14b`
(extraction) + `nomic-embed-text` (embeddings)

---

## What "global brain" means here

Three Claude Code hooks plus one MCP server, all pinned to a single SQLite brain
so memory persists across every session regardless of which project directory
you launch from:

| Surface | Event | What it does |
|---------|-------|--------------|
| `recall` hook | `UserPromptSubmit` | Injects relevant claims into context before Claude answers |
| `brief` hook | `SessionStart` | Injects a one-line brain summary (claim/run counts) |
| `capture` hook | `SessionEnd` | Distills the session transcript into the brain (ingest→extract→relate) |
| `mnemos` MCP | — | 44 tools: `query_knowledge`, `process_text`, `record_decision`, … |

---

## The happy path

```bash
brew install klarlabs-studio/tap/mnemos
mnemos init            # detects OS + Claude Code + Ollama, previews, applies
```

`mnemos init` is genuinely good UX: it detects the host, previews the exact
changes (brain path, config, MCP registration, hooks), asks before writing,
backs up `settings.json`, and runs `mnemos doctor` afterward to verify. Output:

```
Will apply:
  • brain:  sqlite://~/.local/share/mnemos/mnemos.db  (sqlite, scope: user)
  • config: ~/.config/mnemos/config.yaml
  • mcp:    register `mnemos mcp` (scope: user)
  • hooks:  recall, brief, capture → ~/.claude/settings.json
```

End-to-end verification (all passed):

- **capture** → transcript in, 8 claims + 4 relationships + 9 embeddings out, exit 0
- **brief** → `Mnemos brain connected: N claims across M runs`
- **recall** → surfaced the right claims for a "payments database" query
- **MCP** → clean stdio handshake, 44 tools listed

---

## Issues found & fixed

### 1. 🔴 Homebrew upgrades silently break the whole integration (fixed)

**Symptom.** After `mnemos init`, the MCP server and all three hooks were
registered with a *version-pinned* binary path:

```
/opt/homebrew/Cellar/mnemos/0.81.0/bin/mnemos hook recall --db …
```

**Root cause.** `selfPath()` (`cmd/mnemos/setup.go`) ran the invocation path
through `filepath.EvalSymlinks`, collapsing the stable
`/opt/homebrew/bin/mnemos` symlink down to the Cellar realpath.

**Impact.** `brew upgrade mnemos` deletes `Cellar/mnemos/0.80.0/…`, so every
registered path 404s. The MCP server and hooks fail — and because hooks are
*fail-open by design*, they fail **silently**: no memory, no error, no clue.

**Fix.** `os.Executable()` already returns an absolute path (the stable
symlink). Keep it as-is; don't resolve symlinks. Registration now uses
`/opt/homebrew/bin/mnemos`, which survives upgrades.
→ commit `fix(setup): register the stable binary path so brew upgrades don't break hooks`

**Live setup also repaired.** The already-written `~/.claude.json` and
`~/.claude/settings.json` were patched from the Cellar path to the stable
symlink so this machine is upgrade-safe today.

### 2. 🟠 The walk-up resolver diverges from the configured brain (worked around + partial fix)

**Symptom.** A pre-existing `~/.mnemos/mnemos.db` (529 claims from prior use)
**shadowed** the brain that `init` configured. `mnemos init` pins the XDG path
into the hooks/MCP, but a *bare* `mnemos query`/`metrics` resolves its DB by
walking up from CWD for a `.mnemos/` directory — and `~/.mnemos` at the home
root catches essentially every directory. So manual CLI and the hooks used
**different brains**.

**Circular footgun.** `$HOME/.mnemos/` is *both* the JWT-secret fallback dir
*and* is treated as a project root by `findProjectDB`. `loadJWTSecret` →
`LoadOrCreateSecret` *creates* `~/.mnemos/`, which then hijacks DB resolution
away from the XDG global brain. Merely running `mnemos doctor` or `serve` can
resurrect the shadow.

**Robust fix for the user.** Pin `db.url` in the global config so *every*
`mnemos` invocation resolves to the same brain the hooks use:

```yaml
# ~/.config/mnemos/config.yaml
db:
  url: sqlite:///Users/<you>/.local/share/mnemos/mnemos.db
```

`resolveDSN()` reads `MNEMOS_DB_URL` first, and the config loader hydrates that
from `db.url` — so the walk-up (and any stray `.mnemos`) is bypassed. A
project with its own `.mnemos/mnemos.yaml` still overrides, as intended.

**Root-cause fix (ADR 0008).** `findProjectDB` now stops at `$HOME` *without*
treating `$HOME/.mnemos` as a project root — it's the global fallback dir
(jwt-secret, user-global config), not a brain. Bare CLI from anywhere under
`$HOME` (with no closer project `.mnemos`) resolves the XDG global brain, so
the CLI, hooks, and MCP agree by default and the jwt-secret ↔ resolver
resurrection loop is broken. Project brains still work — they just have to live
strictly **below** `$HOME`. See `docs/adr/0008-home-mnemos-not-a-project-root.md`.
The `db.url` config pin above is now redundant on a fixed binary but is kept as
an explicit override (and is still needed on the currently-installed 0.81.0
until `brew upgrade`).

### 3. 🟡 `doctor` reported a brain it wasn't using (fixed)

**Symptom.** With the brain pinned via config, `doctor` still contradicted
itself:

```
✓ project_root  ok  root=~ db=~/.mnemos/mnemos.db        ← walk-up (not used)
✓ store_open    ok  sqlite://~/.local/share/mnemos/…     ← actually opened
```

**Fix.** `probeProjectRoot` now reports the `MNEMOS_DB_URL` override so the two
lines agree:

```
✓ project_root  ok  root=~ — MNEMOS_DB_URL overrides project db ~/.mnemos/mnemos.db (using sqlite://~/.local/share/mnemos/…)
```

→ commit `fix(doctor): reflect MNEMOS_DB_URL override in the project_root check`

### 4. 🟡 Extraction quality tracks the model — pick a good default

`init` auto-selected `llama3.2` (the smallest/oldest pulled Ollama model). Its
extraction was noisy — it restated the user's *question* as a claim:

```
[fact] What should we use (trust 0.66)
[decision] We need to decide on the database for the payments service
```

Switching the config to `qwen2.5:14b` produced **clean** extraction with no
junk (2 crisp facts, ~20s for a short input, with embeddings). For a knowledge
brain, signal-to-noise matters more than capture latency (capture is
background and fail-open). Worth considering: `init` could prefer a stronger
locally-available model over the smallest one.

### 5. 🟡 CLI `--db` inconsistency (fixed)

**Symptom.** `mnemos hook … --db <dsn>` worked, but `mnemos metrics --db <dsn>`
failed with `unknown argument "--db"`. `--db` was parsed ad hoc by `hook`,
`init`, and `setup` (three copies), while every brain-resolving command
(`metrics`, `query`, `process`, `ingest`, `extract`, `relate`, …) had none —
they only read `MNEMOS_DB_URL`.

**Fix.** Promoted `--db` to a **global flag** parsed once in `ParseFlags`
(`--db <dsn>` and `--db=<dsn>`, mirroring `--config`). `main()` exports it as
`MNEMOS_DB_URL` before dispatch as the most specific DSN source — overriding
the config file and any inherited `MNEMOS_DB_URL`. Every command now resolves
the brain identically via `resolveDSN()`; `hook`/`init`/`setup` route through
the same flag (the existing `mnemos hook recall --db …` registration string
still works verbatim). Documented in `--help`.
→ commit `fix(cli): make --db a global flag so every command resolves the brain uniformly`

---

## Final state

- ✅ Homebrew install, PATH-resolved, **upgrade-safe** registration
- ✅ Single global brain at `~/.local/share/mnemos/mnemos.db` for hooks, MCP, **and** manual CLI
- ✅ `qwen2.5:14b` extraction + `nomic-embed-text` embeddings, verified healthy
- ✅ recall / brief / capture / MCP all verified end-to-end
- ✅ Old 529-claim brain archived to `~/.mnemos/mnemos.db.archived-2026-07-11`
- ✅ `--db` now works on every command (global flag)
- ✅ Three code fixes committed on `fix/homebrew-selfpath-and-doctor-override`

**Restart Claude Code** so it reconnects the MCP server and picks up the hooks.
Confirm with `/mcp` (mnemos → Connected) and `/hooks`.
