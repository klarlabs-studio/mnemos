---
name: mnemos-capture
description: Manually distill the current session into the Mnemos brain — decisions, facts, and corrections — without waiting for the SessionEnd capture hook. Use when the user says "/mnemos-capture", "capture this", "remember this", "save what we learned", "record this decision", or before a /clear, a context compaction, or ending a long session.
---

# Mnemos Capture

Manual counterpart to the `SessionEnd` capture hook. The hook reads the raw
transcript and runs it through extraction after the session is over. This skill
runs *now*, while you still hold the session in context — which means you can
distill what actually mattered instead of leaving a model to mine it out of a
20KiB transcript later.

Use it before `/clear`, before a compaction, or any time the user says a
conclusion is worth keeping.

## Choosing a path

Prefer the **MCP tools** (`mcp__mnemos__*`) — they work identically against a
local and a hosted brain. Fall back to the **CLI** only when no `mcp__mnemos__*`
tool is available or every call errors; the CLI resolves its own DSN and may
target a different brain than the MCP server is serving.

## Step 1 — Decide what is worth keeping

This is the whole job. The brain is only as useful as it is uncluttered, and
every junk claim dilutes recall for every future session.

**Capture:**

- Decisions, with the reasoning and the alternatives rejected.
- Facts discovered about the system that were not obvious from the code —
  behaviors, constraints, version quirks, why something is the way it is.
- Corrections: things believed at the start of the session that turned out to
  be wrong. These are the highest-value claims in the brain, because they
  overwrite something that is already in there misleading people.
- Preferences and constraints the user stated.

**Do not capture:**

- Your own narration ("I'll now read the file", "Let me check the tests").
  The capture hook filters assistant narration for a reason; do not
  reintroduce it by hand.
- Anything already recorded in the repo — code structure, git history,
  CLAUDE.md, ADRs. The brain is for what the repo does *not* say.
- Transient state: file paths you happened to open, test output, command
  invocations that worked.
- Secrets, tokens, credentials, or customer data of any kind. If a fact cannot
  be stated without a secret, omit the fact.

If nothing clears this bar, say so and write nothing. An honest "nothing worth
capturing this session" is a valid outcome.

## Step 2 — Write it

Route each item to the tool that fits it:

- **Decisions** → `record_decision` with the statement, the reasoning, the
  alternatives considered, and a risk level. Link the beliefs it rests on when
  you know their claim ids. A decision recorded here can have an outcome
  attached later, which is how the brain learns whether it was right.
- **Facts, corrections, narrative context** → `process_text` with a short
  prose summary. Write it as standalone assertions a future session can
  understand with zero context from this one — no "it", no "the above", no
  "as discussed". One claim per sentence.
- **A single durable fact the user explicitly asked to remember** →
  `remember`, which is the direct path and skips extraction.

Tag the work so it stays traceable: pass a run id derived from the session or
the branch when the tool accepts one.

### CLI fallback

```bash
mnemos process --text "<distilled summary>"          # ingest + extract + relate
mnemos process --llm --text "<distilled summary>"    # LLM extraction (better recall, slower)
mnemos decision record --statement "<statement>"     # record a decision
```

## Step 3 — Confirm

Report back exactly what was written: how many claims were extracted, the
decision ids, and the run id. Then state what you deliberately left out and
why, in one line — that is how the user catches a capture that dropped
something they cared about.

If extraction returned zero claims from non-empty text, that is a failure, not
a no-op: the LLM provider is likely misconfigured. Say so and point at
`mnemos doctor`.

## Failure modes

- **Brain unreachable** — report the error verbatim and suggest `mnemos
  doctor`. Do not silently drop the capture; the user believes it was saved.
- **Capture times out** — the default budget is 4m (`MNEMOS_CAPTURE_TIMEOUT`),
  sized for a slow local model. Capture a shorter summary rather than retrying
  the same payload.
- **Duplicate content** — re-running this skill in one session re-ingests what
  the last run already took. Capture only what happened *since* the previous
  run, and say which window you covered.
