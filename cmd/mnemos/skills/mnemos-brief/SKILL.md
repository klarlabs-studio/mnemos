---
name: mnemos-brief
description: On-demand orientation from the Mnemos brain — totals, what this workspace knows, open contradictions, recent decisions, and knowledge gaps. Use when the user says "/mnemos-brief", "brief me", "brief me from mnemos", "what does the brain know", "where did we leave off", or wants to re-orient mid-session after the SessionStart brief has scrolled out of context.
---

# Mnemos Brief

Manual counterpart to the `SessionStart` brief hook. The hook fires once, at
session start, and emits two lines. This skill is the deep version: run it any
time the user wants to know what the brain actually holds — mid-session, after
a `/clear`, or when the automatic brief never fired.

## Choosing a path

Prefer the **MCP tools** (`mcp__mnemos__*`). They work identically against a
local brain and a hosted one, and they return structured results.

Fall back to the **CLI** only when the MCP server is not connected — i.e. no
`mcp__mnemos__*` tool is available, or every call errors. Do not shell out to
`mnemos` when the MCP tools are working; the CLI opens its own connection to
the brain and may resolve a different DSN than the MCP server is serving.

## Procedure — MCP path

Run these concurrently where possible; each is independent.

1. `knowledge_metrics` — total claims, runs, open contradictions. This is the
   headline. If it fails, the brain is unreachable: say so and stop.
2. `query_knowledge` with a question drawn from the user's current work (the
   repo name, the branch, the feature under discussion). If the user gave no
   topic, use the repository name from the working directory.
3. `list_dissonances` — open contradictions the agent is expected to
   reconcile. Cap what you report at the 5 most relevant; say how many were
   omitted rather than silently truncating.
4. `list_decisions` — the most recent recorded decisions, for continuity with
   the last session.
5. `knowledge_gaps` — only when the user asks what the brain is missing, or
   when steps 1–4 came back thin. It is the slowest call; skip it otherwise.

## Procedure — CLI fallback

```bash
mnemos metrics --human            # totals: claims, runs, contradictions
mnemos metrics --workspace        # this workspace's scoped overlay
mnemos query --human "<topic>"    # what the brain knows about the current work
mnemos quality                    # trust, staleness, contested, contradictions
mnemos health --human             # vitals + integrity, one verdict
```

`mnemos health` is the right escalation when the numbers look wrong (zero
claims on a brain that should be populated, contradiction count exploding).

## Output

Write a compact brief, not a data dump. Target 10–20 lines:

- **Brain** — claims / runs / open contradictions, plus the workspace overlay
  count if there is one.
- **What it knows about this work** — 3–6 claims that bear on what the user is
  doing right now, each with its trust score. Quote the claim, don't paraphrase
  it into something the brain never said.
- **Contradictions** — the open ones that touch this work, and which side is
  more trusted. This is the part the user cannot get anywhere else; lead with
  it when the count is non-trivial.
- **Recent decisions** — what was decided last session and whether an outcome
  was ever attached.
- **Suggested next step** — one, concrete, drawn from the above.

State trust scores as the brain reports them. If a claim is stale or contested,
say so inline rather than presenting it as settled.

## Failure modes

- **Brain unreachable** — report the error and suggest `mnemos doctor`. Do not
  invent a brief from conversation memory; a fabricated brief is worse than
  none, because the user will act on it.
- **Empty brain (0 claims)** — say it plainly and suggest `/mnemos-capture` at
  the end of this session, or the `ingest_git_log` MCP tool to seed the brain
  from commit history.
- **Numbers disagree between MCP and CLI** — the two resolved different DSNs.
  Report both and point at `mnemos doctor`, which prints the resolved brain.
