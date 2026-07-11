# ADR 0008: `$HOME/.mnemos` is the global fallback, not a project root

- **Status:** Accepted
- **Implemented:** 2026-07-11 (v0.81.x)
- **Date:** 2026-07-11
- **Deciders:** Felix Geelhaar
- **Scope:** Mnemos CLI DB/config/secret resolution (`cmd/mnemos`).

## Context

Mnemos resolves the SQLite brain for a bare CLI invocation (no `MNEMOS_DB_URL`,
no config `db.url`) by walking up from the working directory looking for a
`.mnemos/` directory (`findProjectDB` → `resolveDBPath`). The first `.mnemos/`
found wins; if none is found before the home directory, it falls back to the
XDG global brain at `~/.local/share/mnemos/mnemos.db`.

Separately, `$HOME/.mnemos/` is the **global fallback location** for other
per-user state: `internal/auth.DefaultSecretPath` writes `jwt-secret` there
when no project root is found, and it is the natural home for a user-global
`mnemos.yaml`.

These two facts collide. `findProjectDB` treats `$HOME/.mnemos` as a *project
root* — so if that directory exists, every bare `mnemos` command run from
anywhere under `$HOME` (i.e. almost everywhere) resolves its brain to
`$HOME/.mnemos/mnemos.db`, **shadowing the XDG global brain** that `mnemos init`
configures and pins into the Claude Code hooks/MCP server.

Worse, the collision is self-inflicting and self-healing in the wrong
direction:

1. `mnemos init` (user scope) configures the brain at
   `~/.local/share/mnemos/mnemos.db` and pins it into the hooks + MCP server.
2. A user's bare `mnemos query`/`metrics` from `$HOME` (or any subdir) instead
   resolves `$HOME/.mnemos/mnemos.db` via the walk-up — a **different brain**
   than the one the hooks write to. Manual inspection and the automated
   integration silently diverge.
3. Even after removing `$HOME/.mnemos/mnemos.db`, any auth-touching command
   (`doctor`, `serve`, `token issue`, `users`) calls `loadJWTSecret` →
   `LoadOrCreateSecret`, which **recreates** `$HOME/.mnemos/` to hold the
   secret — resurrecting the directory and, with it, the shadow.

This was hit in practice during global-brain setup: a pre-existing
`$HOME/.mnemos/mnemos.db` (529 claims) shadowed the freshly-configured XDG
brain, and `mnemos doctor`'s `project_root` check reported the shadow while
`store_open` reported the real brain. The immediate workaround was to pin
`db.url` in the global config, but that only masks the resolver bug.

## Decision

**`findProjectDB` stops at `$HOME` without treating `$HOME/.mnemos` as a project
root.** A `.mnemos/` directory located exactly at the home directory is no
longer adopted as a project brain; discovery falls through to the XDG global
default.

Concretely, the home-boundary check moves to the *top* of the walk-up loop, so
the home directory is a hard stop that is reached **before** its own `.mnemos/`
is considered:

```go
for {
    // $HOME is the global fallback dir (jwt-secret, user-global config), not a
    // project. Adopting $HOME/.mnemos would shadow the XDG global brain for
    // essentially every directory under $HOME, so stop here and use the XDG
    // default. Project brains live strictly BELOW $HOME.
    if home != "" && dir == home {
        return "", "", false
    }
    candidate := filepath.Join(dir, ".mnemos")
    if info, err := os.Stat(candidate); err == nil && info.IsDir() {
        return filepath.Join(candidate, "mnemos.db"), dir, true
    }
    parent := filepath.Dir(dir)
    if parent == dir {
        return "", "", false
    }
    dir = parent
}
```

After this change the two roles of `$HOME/.mnemos` no longer conflict:

- **`$HOME/.mnemos/`** — global fallback only: `jwt-secret`, user-global
  `mnemos.yaml`. Never a brain.
- **`~/.local/share/mnemos/mnemos.db`** — the single global brain (XDG),
  shared by the CLI, the hooks, and the MCP server.
- **`<project>/.mnemos/mnemos.db`** — project brains, discovered by walk-up,
  but only when the project root is strictly **below** `$HOME`.

`loadJWTSecret` is unaffected: when `findProjectDB` returns no root,
`DefaultSecretPath("")` already resolves to `$HOME/.mnemos/jwt-secret` — the
same path it used when `$HOME` was (incorrectly) reported as the project root.

## Consequences

**Positive:**

- The XDG global brain the CLI, hooks, and MCP server all agree by default —
  no more manual-vs-automated divergence, and the `db.url` config pin becomes
  unnecessary (though still honored as an explicit override).
- The jwt-secret ↔ resolver feedback loop is broken: recreating
  `$HOME/.mnemos/` for the secret can no longer resurrect a brain shadow.
- `doctor`'s `project_root` and `store_open` agree on a stock install.

**Negative / breaking:**

- **A brain deliberately placed at `$HOME/.mnemos/mnemos.db` is no longer
  discovered by bare CLI.** Anyone relying on "home as a project" must either
  move it under a real project directory, or set `MNEMOS_DB_URL` / config
  `db.url` explicitly (or run `mnemos init --db sqlite://~/.mnemos/mnemos.db`
  to pin it). We are pre-1.0 with a single known user, so we take the break
  now rather than carry the footgun. There is **no automatic migration**; the
  file is left in place and simply ignored by the walk-up.

- `mnemos init --project` run from `$HOME` itself would scaffold
  `$HOME/.mnemos` that the walk-up won't later adopt. This is a degenerate
  invocation (the home directory is not a project); `--project` is meant for
  actual project directories below `$HOME`. `init` still writes the DSN
  explicitly into the hooks/MCP, so a `--project` setup does not depend on the
  walk-up regardless.

## Alternatives Considered

**1. Keep the workaround only (pin `db.url` in the global config).**
Rejected: it masks the resolver bug for one machine but every fresh
`mnemos init` on a host that happens to have `$HOME/.mnemos` (e.g. from a prior
auth command) reintroduces the divergence. Fix the resolver, not the symptom.

**2. Make `init` always write `db.url` to the global config for user-scope
sqlite brains.** Useful and complementary (it makes the CLI agree with the
hooks explicitly), but it does not stop `$HOME/.mnemos` from shadowing other
tools/config, and it still relies on config discovery not itself being
shadowed. Worth doing as a follow-up, but not a substitute for fixing the
walk-up. Not in this ADR.

**3. Move the jwt-secret / global config out of `$HOME/.mnemos` (e.g. to
`~/.config/mnemos` and `~/.local/share/mnemos`).** A larger churn touching
`internal/auth` and every path that reads the secret, with its own migration
cost, and it does not by itself stop a genuine `$HOME/.mnemos/mnemos.db` from
being adopted. The minimal, targeted fix is to make the walk-up not treat home
as a project.

## Related Work

- `cmd/mnemos/main.go` — `findProjectDB` / `resolveDBPath` (the walk-up).
- `internal/auth/secret.go` — `DefaultSecretPath` (`$HOME/.mnemos/jwt-secret`
  fallback; unchanged by this ADR).
- `cmd/mnemos/doctor.go` — `probeProjectRoot`, updated in the same series to
  reflect the `MNEMOS_DB_URL` override so it no longer contradicts `store_open`.
- `docs/global-brain-setup-ux.md` — the dogfooding walkthrough that surfaced
  this footgun.
