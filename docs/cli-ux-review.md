# Mnemos CLI — UX review of commands & flags

A systematic audit of the `mnemos` command surface (43 top-level commands) for
consistency, discoverability, and correctness. Findings are prioritized; two P0
bugs are already fixed (marked ✅). Evidence is cited as `file:line`.

## How the CLI is wired (root cause of most findings)

`ParseFlags` (`cmd/mnemos/flags.go`) runs **once** over the entire arg vector in
`main()` *before* command dispatch. It strips every recognized **global** flag
(`--help`, `--version`, `--verbose`, `--human/-o`, `--json`, `--llm`, `--embed`,
`--no-relate`, `--force`, `--dry-run`, `--yes/-y`, `--as`, `--config`, `--db`)
from anywhere in the line and passes only the remainder to the handler. Each
handler then parses its **local** flags in a bespoke `for` loop (only `claim
record` uses `flag.FlagSet`).

Two structural consequences drive most of this review:

1. A handler with a `_ Flags` signature **cannot see** the global output/
   confirmation flags — so `--human`/`--json`/`--dry-run` silently do nothing
   there.
2. A **local** flag whose spelling collides with a global one is consumed
   upstream and its local `case` becomes **dead code**.

---

## P0 — Correctness bugs

### ✅ 1. `consolidate --dry-run` mutated the store (FIXED)
`handleConsolidate` took `_ Flags` and read `--dry-run` from a local `case` that
could never fire (it's stripped globally). `ConsolidateOptions.DryRun` stayed
false, so the pass ran **real** forget/reinforce/forget-below-trust mutations
when the user asked for a preview. Now sourced from `f.DryRun`; guarded by
`TestParseConsolidateOpts_*`. (`consolidate.go`)

### ✅ 2. `doctor --json` rendered human output (FIXED)
Same dead-`case` pattern with `--json`, breaking any script/CI that parsed
`doctor` output. Now sourced from `f.JSON`. (`doctor.go`)

### 3. `agent heal --json` uses a *separate* local `--json` (open)
It works, but via a local flag (`agents.go:340`) distinct from the global
`Flags.JSON` — so the two `--json` code paths behave differently across the
tool. Should converge on the global flag.

### 4. Panic on a trailing value-flag (open, robustness)
Several reject-style loops read `args[i+1]` with no bounds check, so a trailing
value-flag crashes with an index-out-of-range panic instead of a clean error:
`export`/`history` (`markdown.go:26,29,32,180,183`), `decision record/list`
(`decisions.go`), `incident open/list` (`incidents.go`), `playbook
list/synthesize` (`playbooks.go`). Bounds-checked siblings already exist
(`consolidate`, `entities list`, `audit who`, `resolve`, `registry connect`),
so this is an inconsistently-applied guard. Fix: add the `if i+1 >= len(args)`
check everywhere (or adopt a shared value-flag helper).

---

## P1 — Consistency quick-wins (non-breaking or low-risk)

### 5. `--help` hides ~⅓ of the product
`printUsage` documents 27 of 43 commands; **15 user-facing commands are
undiscoverable** from `--help` (`main.go` switch vs `printUsage`): `agent`,
`doctor`, `claim`, `action`, `outcome`, `synthesize`, `lessons`, `verify`,
`decision`, `incident`, `playbook`, `export`, `import`, `history`, `quality`.
That's essentially every Phase 2–7 feature. A new user reading `--help` cannot
learn that lessons, playbooks, decisions, incidents, or export/import exist.
**Fix: list every command; group by lifecycle.**

### 6. No per-command help
`--help`/`-h` is consumed globally and handled before dispatch, so `mnemos query
--help`, `mnemos decision --help`, etc. all print the *global* usage. There is
no `<command> --help`. **Fix: intercept `--help` after dispatch (or per
handler) and print command-specific usage.** Handlers already hold the usage
strings.

### 7. Typo suggestions cover only 7 of 43 commands
`suggestCommand` ranks against a hardcoded 7-command list (`ux.go:117`), so a
typo of `synthesize`/`playbook`/`decision`/… gets no "Did you mean?". **Fix:
build the candidate list from the actual dispatch table.**

### 8. Error-message shapes are inconsistent
Three different shapes for "you forgot the subcommand": `error: usage: …`
(`decision`/`incident` double-prefix a `usage:` string that then gets `error:`
prepended — `decisions.go:14`, `incidents.go:14`), plain `usage: …`
(`action`/`outcome`), and `error: <noun> requires a subcommand` + hint lines
(`user`/`agent`). Unknown-subcommand errors sometimes list valid verbs
(`entities.go:37`) and sometimes don't (`decision`, `incident`, `user`, `agent`,
`registry`). Two emission styles coexist: structured `exitWithMnemosError`
(prints a `See 'mnemos --help'` hint) vs raw `fmt.Fprintln(os.Stderr,…)` +
`os.Exit`. **Fix: route all user errors through `NewUserError`/
`exitWithMnemosError`; standardize "missing/unknown subcommand" copy.**

### 9. Exit codes are unsystematic
`ExitNotFound` (3) exists but "not found" maps to exit 1, 2, or 3 depending on
code path — e.g. in `export`, a missing lesson wraps `GetByID` in
`NewSystemError` → exit 1 (`markdown.go:55`), while a missing claim returns
`NewUserError` → exit 2 (`markdown.go:105`). **Fix: use `NewNotFoundError`
consistently for lookups.**

### 10. Help lists some commands twice
`token`, `mcp`, and `serve` each appear under two group headings (Serving +
Identity) in `printUsage`. Minor, but reads as disorganized.

---

## P2 — Larger consistency (some breaking; we're pre-1.0)

### 11. Output modes are all over the map
The `--human`/`--json` contract is honored by only **5** commands (`query`,
`metrics`, `entities list/show`, `audit who`). The rest split into: **JSON-only**
(ignores `--human`) — every Phase 2/3/5/6 command via `emitJSON` plus `quality`;
**human-only** (ignores `--json`) — `resolve`, `registry`, `push`/`pull`, all
`user`/`token`/`agent`, `serve`, `init`, `setup`, `doctor`; **fixed `key=value`
lines** (ignores both) — `ingest`, `extract`, `relate`, `process`, and the six
`admin.go` commands; and one **plaintext** outlier (`claim record`,
`claims.go:101`). Net: the TTY-detection default in `main.go:151` is meaningful
for only ~5 of 43 commands. **Fix: make `emitJSON`/`emitHuman` honor the global
flags uniformly; every read command should support both.**

### 12. Two grammars for the same entity
Entity ops split between noun-verb subcommands (`action record`, `decision
record`, `entities list/merge`) and verb-noun hyphenated top-level
(`delete-claim`, `delete-event`, `extract-entities`, `recompute-trust`). The
same noun collides: create via `claim record` but delete via `delete-claim`;
`entities merge` but `extract-entities`. **Recommendation: standardize on
`<noun> <verb>` (`claim delete`, `entity extract`, `trust recompute`), with
hidden aliases for the old spellings during a deprecation window.**

### 13. Flag vocabulary is fragmented
- **Run linkage:** `--run` (action/agent/query) vs `--run-id` (`claim record`).
- **Actor/attribution:** global `--as` vs local `--actor` (action) vs
  `--created-by` (incident) vs `--owner`/`--user`/`--agent` vs env-only
  `MNEMOS_USER_ID` (claim) vs none (decision). Five conventions.
- **Confirmation/dry-run:** `reset` uses `--yes`+prompt; `dedup`/`reembed` use
  `--force`/`--dry-run`; `delete-claim`/`delete-event` have **no gate at all**
  (destructive!); `setup` accepts both `--dry-run` and a local synonym
  `--print`.
- **`--type` vs `--kind`** for the same "filter by category" idea.
**Recommendation: pick one spelling per concept (`--run`, `--as`, `--kind`,
`--yes`) and alias the rest; add a confirmation gate to `delete-claim`/
`delete-event`.**

### 14. `--flag=value` works almost nowhere
Only the global `--config`/`--db` and `claim record` accept `--flag=value`;
every other local value-flag is space-only. So `mnemos --db=x` works but
`mnemos serve --port=8080`, `token issue --user=usr_1`, `trust --test=foo` (the
last is even advertised with `=` in its own error text, `trust.go`) are rejected.
**Recommendation: a shared value-flag parser that accepts both forms** (this
also fixes the P0-#4 panics in one move).

### 15. Silent bare-arg fallthrough on two commands
`lessons <x>` treats an unknown first arg as flags for `list`; `playbook <x>`
treats it as a *trigger lookup* — so `playbook lst` silently searches for a
trigger named "lst" instead of erroring (`playbooks.go:25`). Every other
sub-verb command hard-errors. **Recommendation: reserve the bare-arg form
explicitly (e.g. `playbook for <trigger>`) or validate against known verbs
first.**

### 16. Near-synonym commands blur the mental model
`process` = `ingest`+`extract`+`relate`; `setup` vs `init`; `metrics` vs
`quality`; `synthesize` vs `consolidate` vs `dedup`; `verify` vs `trust` vs
`recompute-trust`; `export`/`import` vs `audit` vs `push`/`pull`. Several pairs
have overlapping intent but unrelated names. **Recommendation: document the
relationships in `--help` (or fold overlaps, e.g. `quality` → `metrics
--quality`).**

### 17. `push`/`pull` silently ignore unknown flags
`parseRegistryFlags` has no `default` case (`registry.go:318`), so unknown flags
and stray positionals are dropped silently — the opposite of every sibling that
rejects them. **Fix: reject unknowns.**

### 18. CLI ↔ MCP grammar inversion
CLI is noun-verb (`action record`); the MCP tools are verb_noun
(`record_action`). An agent author moving between the two surfaces meets
inverted word order. Cosmetic, but worth a note in docs.

---

## Suggested sequencing

1. **Land the P0s** (done: #1, #2; next: #3 `agent heal --json`, #4 panic
   guards — a shared bounds-checked value-flag helper knocks out #4 and #14
   together).
2. **P1 discoverability** (#5–#7): highest user-visible value, non-breaking —
   complete `--help`, add per-command help, fix typo suggestions.
3. **P1 error hygiene** (#8–#10): route everything through `NewUserError`,
   standardize copy and exit codes.
4. **P2 output-mode unification** (#11): make every read command honor
   `--human`/`--json`. Mechanical but broad.
5. **P2 naming/flag vocabulary** (#12–#16): the only breaking changes. Do them
   as one deliberate pass with hidden aliases for old spellings, pre-1.0.

Nothing here blocks the current global-brain work; #1/#2 were genuine defects
and are fixed. The rest is a roadmap toward a CLI that reads as one coherent
tool.
