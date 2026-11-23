# ADR 0050: Gated task-state working memory (experiment)

- **Status:** Experimental; shipped opt-in (`MNEMOS_TASK_STATE`), not on by default.
- **Date:** 2026-11-23
- **Deciders:** Felix Geelhaar
- **Builds on:** ADR 0011 (complementary learning systems), the working-memory
  blocks (`set_block` / `get_blocks`, `working_memory_blocks`)
- **Adds:** `internal/taskstate`; MCP `get_task_state` / `update_task_state`;
  config `working_memory.task_state`; `brainbench -mode taskstate`
  (`make brain-eval-taskstate`). No schema change.
- **Refs:** #447; integration point with #446 (expectation ledger)

## Context

Working-memory blocks are labelled text capped at 2,000 characters. Appending
past the cap evicts the oldest lines. Nothing decides what enters a block, and
nothing carries its contents into long-term memory: a block lives until someone
overwrites it.

#447 collects the evidence for doing better:

- **Gating is the core of working memory.** In models such as PBWM, gating
  matters more than capacity.
- **Position matters in a context window.** Models attend best to the start
  and end of a long context.
- **A curated scratchpad helps where a raw recent-turns buffer does not.**
- **Constraints stated early and needed late get lost.** The question that
  needs them shares no words with them, so similarity retrieval cannot find
  them, and a FIFO buffer has evicted them by the time they are needed.

## Decision

### Contents

The task-state block is a per-session block, labelled
`task_state:<session>`, that holds typed items. Each item is one line:

- **goal**
- **constraint**
- **commitment**: pending
- **hypothesis**: carries `rests_on=<claim ids>`, and those ids must exist
- **question**: open

The block keeps the 2,000-character budget of ordinary blocks, and each item is
capped at 300 characters.

The block is stored as an ordinary row in `working_memory_blocks`. A rendered
header (`# task state`) and line grammar make it parseable, so neither the schema
nor the block port changes.

### Gating in (`taskstate.Gate`)

The gate is a pure function of the held state and a `Probe` onto long-term
memory, applied in order:

1. **Refuse invalid items.** An item with an unknown kind, empty text, text
   over the item cap, or a `rests_on` id that does not exist returns an error.
   This is input validation, not a rejection.
2. **Reject duplicates.** An item that duplicates one already held, compared
   on normalized text, is rejected (`already_held`).
3. **Always admit goals.** Goals are what relevance is measured against.
4. **Admit anything else that passes at least one test:**
   - **new to long-term memory**: no live claim restates it. A restatement is
     an exact match on the indexed text key, or content-token Jaccard of 0.8
     or more against relate candidates.
   - **relevant to a goal**: `relate.SharesTopic` with a held goal.
   - **surprising against a stored claim**: the incremental contradiction
     detector finds a `contradicts` edge. The ids it contradicts are recorded
     on the item as `against=`.
5. **Reject the rest.** Anything that passes none of the tests is rejected
   (`known_irrelevant_unsurprising`).

`StoreProbe` is read-only and reuses the write path's own machinery, the
text-key lookup, `pipeline.ExistingForRelate` and
`relate.Engine.DetectIncremental`. "Known" and "contradicts" therefore mean what
they mean at ingest. A contradiction is never also counted as "known": content
tokens drop negation, so without this exclusion a negated restatement would look
like a perfect duplicate.

**#446 integration point.** The expectation ledger on `feat/expectation-ledger`
adds attributed prediction errors (`PredictiveError.Attributed`). They are the
intended second source of surprise: an item that contradicts an attributed
prediction error is surprising even when no stored claim contradicts it. Wire it
in `taskstate.Gate`, next to the `Contradicts` check, once #446 merges. This
change deliberately does not depend on that branch.

### Placement

`Render` writes the items in a fixed kind order: goals, then constraints,
commitments, hypotheses and questions. Within a kind, items keep their admission
order. Goals and constraints therefore open the block. While the experiment is
enabled, `get_blocks` also lists task-state blocks before an owner's other
blocks, so the task state starts the context block.

### Eviction

When the block overflows, `EvictByPriority` drops the oldest item of the
lowest-priority kind present: open questions first, then hypotheses,
commitments, constraints and finally goals. `EvictOldest`, the FIFO policy, is
kept only so the experiment can ablate eviction separately from the gate.

### Handoff out

At SessionEnd, the capture hook hands every task-state block of the default
owner (`claude-code`) to long-term memory. The `update_task_state` action
`handoff` does the same on demand. The handoff writes through the governed
writer (`govwrite.Writer.Artifacts`):

- **One episode.** The rendered block is stored verbatim, with
  `run_id=task-state`, as provenance for everything below.
- **One candidate claim per assertion.** Each has `Lifecycle=candidate` and
  confidence 0.5. Goals and commitments become `decision` claims, constraints
  become `fact` claims, and hypotheses become `hypothesis` claims.
- **No claim for open questions.** A question is not an assertion, so it
  survives only in the episode.
- **Edges.** A hypothesis gets a `derived_from` edge to each claim it rests
  on, and a surprising item gets a `contradicts` edge to the claims it
  contradicts. Edges to claims that have since been deleted are dropped.
- **Deterministic ids.** Ids derive from owner, session and content, so a
  retried handoff rewrites the same rows.
- **Clearing.** The block is cleared only after the write succeeds.

Handoff items never enter long-term memory directly as beliefs. Consolidation and
review decide whether a candidate is promoted, which follows CLS.

Hosted brains are skipped. The REST surface has no task-state endpoint while
this remains an experiment, and the capability registry records the Go, REST,
gRPC and client omissions as deliberate (`taskStateExperiment`). The task-state
tools are scoped like `get_blocks` and `set_block`: reads need no scope, and
`update_task_state` needs `claims:write`.

## Evidence: `make brain-eval-taskstate`

`brainbench -mode taskstate` replays each scenario in
`data/eval/brainbench/taskstate/` into five arms. Each arm runs on its own copy of
one seeded SQLite brain, and every arm receives exactly the same items:

| Arm | What it runs |
|-----|--------------|
| control | Today's `AppendBlock`: ungated, oldest lines evicted |
| control_replica | The same as control, on another copy; it sets the noise floor |
| gated | The treatment: gate plus priority eviction |
| gate_only | Ablation: gate with FIFO eviction |
| priority_only | Ablation: priority eviction with no gate |

The scored metrics are:

- **needed_in_block**: whether each (question, need) pair is still in the block
  at question time, checked as a substring.
- **implicit_in_block**: the same check, restricted to implicit questions.
- **needed_available**: the need is in the block or already in long-term
  memory.

Mean offset (placement) and the handoff counts are descriptive only. The control
has no handoff, so it would score zero on them by construction.

Results, 2026-11-23:

| Scenario | Arm | needed_in_block | implicit_in_block | needed_available |
|----------|-----|-----------------|-------------------|------------------|
| implicit_constraints | control | 0.400 | 0.000 | 0.400 |
| | gated | **1.000** | **1.000** | **1.000** |
| | gate_only | 0.400 | 0.000 | 0.400 |
| | priority_only | 1.000 | 1.000 | 1.000 |
| question_burst | control | 1.000 | unknown | 1.000 |
| | gated | **0.750** | unknown | **0.750** |
| | gate_only | 1.000 | unknown | 1.000 |
| | priority_only | 0.750 | unknown | 0.750 |
| within_budget | control | 1.000 | 1.000 | 1.000 |
| | gated | 1.000 | 1.000 | 1.000 |

The noise floor was 0.000 in every scenario: control and control_replica agree,
because the run is deterministic. The verdicts for gated against control are:

- **implicit_constraints: improved.**
- **question_burst: regressed.**
- **within_budget: no effect.**

### What the numbers say

1. **The win comes from eviction, not the gate.** On implicit_constraints,
   priority_only matches gated exactly and gate_only matches the control.
   Over the three scenarios, the gate rejected one item in implicit_constraints
   (an off-goal restatement of a stored claim) and one in within_budget, and
   neither changed retention. "New to long-term memory" admits almost every
   item a session produces, so the gate, as specified in #447, is close to
   inert on these scenarios.
2. **Priority eviction is a trade, not a free win.** It keeps early
   constraints that FIFO loses (0.4 to 1.0). It loses a late burst of open
   questions that FIFO keeps (1.0 to 0.75), because questions are evicted
   first. The question_burst scenario exists to show this. Its verdict is
   pinned as a regression in `TestRunTaskState_CommittedScenarios`, so the
   harness is shown to report a loss.
3. **Placement improves as designed.** On implicit_constraints, needed items
   sit near the start of the block in the gated arm (mean offset 0.32 against
   0.76). Whether position helps a model was not measured.
4. **The handoff works.** Every gated arm left its assertions in the store as
   candidate claims, read back from the store and not from the write call.

### Decision on shipping

The #447 acceptance bar is to ship only if the block beats today's blocks. It does
not clear that bar. It wins one scenario, loses one, and ties one, and the win
is attributable to an eviction policy that a FIFO block could adopt without the
gate. The block therefore ships **opt-in only**, as an instrument for further
measurement, and stays off by default. The control was not weakened to get a
win: it receives the same items, uses the real `AppendBlock` path, and has the
same budget.

## Consequences

- No schema change: no v42, no new table, no claim column. The RLS `scoped`
  array and the projection guards are therefore untouched.
- Two MCP tools, 72 to 74 in total. Both refuse to run unless the experiment
  is enabled, so they are inert by default.
- When the experiment is off, the SessionEnd hook and `get_blocks` behave
  exactly as before.

## Open questions

- **Gate strength.** Should "new to long-term memory" require relevance as
  well? As specified, the gate almost never rejects. A stricter
  `novel ∧ relevant` gate would drop goal-unrelated constraints, which are
  exactly the implicit ones the experiment protects. It needs its own
  scenario before it is tried.
- **Questions under eviction.** Could answered questions be evicted first,
  and open ones kept, using the `remove` action as the signal? That might
  recover question_burst without giving up implicit_constraints.
- **Sleep and hosted brains.** The handoff runs at SessionEnd only. A hosted
  brain has no session hook, so its handoff would belong in the hosted sleep
  cycle and needs a REST surface first.
- **Evaluation coverage.** The #441 harness (LoCoMo and LoCoMo-Plus) should run
  the gated block against FIFO on real conversations before any default
  change.
