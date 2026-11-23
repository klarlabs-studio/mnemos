# ADR 0049: Expectations tied to decisions, and surprise-triggered revision

- **Status:** Accepted; implemented.
- **Date:** 2026-11-23
- **Deciders:** Felix Geelhaar
- **Builds on:** ADR 0014 (credit assignment), ADR 0017 (hierarchical
  prediction error), ADR 0034 (archive, end, deprecate), ADR 0039 (trust and
  freshness rank recall)
- **Adds:** `Decision.Expectation` / `ExpectedOutcome`,
  `Memory.RecordDecisionObservation`, `Reconciliation.{DecisionID,
  AttributedTo, Revisions, Rechecked}`, `PredictiveError.Attributed`,
  `internal/revision`, the brainbench shifting-world track. **No schema
  change.**
- **Closes:** #446 (calibration on the #441 harness is measured separately)

## Context

Mnemos had a prediction-error loop: `Expect` attaches a numeric prediction to
one claim, `RecordObservation` records what arrived, and
`ReconcileExpectations` turns the pair into a precision-weighted surprise and
a `validates`/`refutes` edge on that claim. Three things were missing.

1. **It was not tied to decisions.** An agent acts on several beliefs at once
   and expects an outcome of the act, not of any one belief. The only home
   for a prediction was a claim, one per claim, so an agent wanting the loop
   had to copy the decision's prediction onto each belief it rested on.
   Re-recording replaced the previous prediction on that belief.
2. **Surprise had one consumer that changed beliefs, and it was blunt.** The
   sleep pass's `ForgetRefuted` archives every claim whose latest verdict is
   a refutation. Attaching a failed outcome to a decision refutes every belief
   it rested on (`autoedge.OnDecisionOutcomeAttached`). Either way, one
   failure archives every premise, including the ones that were true.
3. **Recall and surprise were not distinguished.** Nothing said that a belief
   only recalled, or recalled and borne out, must not be revised.

The evidence the issue cites points one way: prediction error is what opens
a memory to revision (Sevenster et al. 2013). It does not by itself say how to
revise. And world models proposed by an LLM are unreliable (Wang et al. 2024;
Vafa et al. 2024), so revision must rest on recorded evidence, not on a
model's story about cause.

## Decision

### 1. A decision may carry its expected outcome

`Decision.Expectation` (`Predicted`, `Tolerance`, `Horizon`) records the
outcome the agent expects. It **rests on the decision's `Beliefs`**: the
decision record is already the belief→plan link, so a second list of claim
ids would be a second source of truth. An expectation that rests on nothing is
refused (`ErrExpectationRestsOnNothing`), and so is one naming an unknown
belief, because a surprise attributed to a missing id silently absorbs blame
that belongs elsewhere. The check runs before the decision is written, so a
decision with an unusable expectation is refused whole.
`RecordDecisionObservation` records the observed outcome.

### 2. Storage: the existing expectation table, under a reserved key

No schema change. A decision's expectation is a `claim_expectations` row keyed
`decision:<decision id>` (`domain.DecisionExpectationKey`). The row already has
every column it needs (predicted, tolerance, horizon, observed, resolved), the
beliefs are the decision's `decision_beliefs` rows, and Postgres already
scopes the table under RLS. The cost is that the column named `claim_id` now
holds a subject key. That is contained by:

- `domain.Expectation.DecisionID()`, which every consumer uses to tell the two
  apart;
- `CheckExpectation` and `CheckObservation` refusing a belief id that starts
  with the prefix (`ErrReservedExpectationKey`), so the key spaces cannot
  collide;
- every reader of the table now handling both: reconciliation, credit,
  the surprise series, `PredictiveError`, and the stale-expectation pathology
  (which correctly counts both).

A dedicated `decision_expectations` table was the alternative. It would have
meant a schema migration on four backends and a new RLS entry, for rows with
the same shape. We would revisit it if decision expectations grow columns a
claim expectation does not have (a metric name, for example).

Actions are not given an expectation. An `Action` has no belief linkage, so an
expectation on it would have nowhere to name what it rests on without either a
schema change or an overloaded `Metadata` map. An agent that acts on beliefs
records the decision (with its expectation) and the action.

### 3. A decision's verdict lands on the decision, not on its beliefs

Reconciling a decision expectation emits the `validates`/`refutes` edge **to
the decision** (`ToType: decision`). It does not emit one to each belief. A
failed outcome is evidence against the **conjunction** of the beliefs, not
against each conjunct: a deploy that failed while resting on "the timeout is
30 s" and "retries are idempotent" says at least one is wrong, not which.
The per-belief verdict consumers would each misread a per-belief edge:
calibration would score every premise as confidently wrong, and ForgetRefuted
would archive every premise. They filter on `ToType == claim`, so the
decision-level edge reaches none of them. This also means calibration on
beliefs is unchanged by this ADR: no new per-belief verdict is created.

### 4. Surprise is attributed to the beliefs it rested on

- **Credit (ADR 0014).** `credit.Assign` treats the decision's expectation as
  one prediction and splits its signed, bounded delta equally over the
  decision's beliefs under the key `credit:<decision>:outcome`. Per-belief
  expectations keep their own entries.
- **PredictiveError (ADR 0017).** The outcome level counts each observed
  prediction once (a decision expectation is one sample, not one per belief).
  The new `PredictiveError.Attributed` lists, per belief, the largest surprise
  attributed to it, how many observed predictions rest on it and how many of
  them were refuted, largest surprise first: where the outcome error points.
- **Replay and neuromodulation (ADR 0015)** read the same attributed series.

### 5. Surprise-triggered revision

Revision runs inside the governed `reconcile_expectations` write, after the
verdicts. The rules live in `internal/revision` as a pure function and are
applied through the store's audited paths (status history, validity history),
each with a reason naming the decisions behind it. For every **refuted**
decision expectation, including ones reconciled in earlier passes:

1. **Re-check against newer evidence.** A rested-on belief that a *newer*,
   current belief contradicts (a live `contradicts` relationship) is
   superseded by it: its valid time ends at the successor's valid-from, or
   now. The surprise is the tie-breaker the contradiction lacked; the newer
   belief is the evidence. Two beliefs the same decision rested on cannot
   decide against each other, since the surprise says nothing about which of
   them was wrong. A promoted (human-endorsed) belief is never superseded, for
   the same reason ForgetRefuted exempts it.
2. **Otherwise flag.** If no rested-on belief was superseded and none is
   already retired, the surprise is unexplained and each active rested-on
   belief moves **active → contested**.
3. **Explaining away.** Once a surprise is explained, the flags it set on the
   other beliefs it rested on are cleared (contested → active), unless an
   unexplained surprise still rests on them.

Never revised: a belief rested on by a **confirmed** decision; a belief that
was **only recalled** (recall writes association strength and inhibition,
never status or validity); a deprecated belief; and a contested belief this
mechanism did not flag. Re-planning is idempotent: applying a plan and
planning again yields nothing, which `TestReconcile_LaterEvidenceSupersedesAndExplainsAway`
pins across three passes.

#### Why contested, and not archive or a re-check queue

The issue left the choice open: "mark it contested, or queue it for
re-check". We chose contested for the unexplained case and supersession for
the explained one.

- **Contested is the lifecycle's existing state for a live disagreement.**
  Recall keeps contested beliefs and shows them as such
  (`internal/query/admission.go`: "hiding it would silently pick a side"). It
  is visible to the agent, reversible, audited in `claim_status_history`, and
  costs nothing in recall quality: a flagged belief is still answerable. That
  is the right reading of a surprise without evidence: *this may be wrong, and
  nobody knows which premise*.
- **Archive (ForgetRefuted's move) is revision by surprise alone.** It is
  exactly what the evidence argues against, and the shifting-world result
  below shows its cost.
- **A re-check queue would be a new structure** (a table or a status) that
  some later pass drains. The re-check already happens: every reconcile pass
  re-plans every refuted decision against the current evidence, so a belief
  flagged on Monday is superseded on Thursday when the contradicting document
  arrives, with no queue. A `Rechecked` reconciliation reports it.
- **Ending valid time is the right axis for the explained case** (ADR 0034):
  the world changed, and the newer belief says so. It is what `mnemos resolve
  --supersedes` does, with the same cutoff.

`recompute-contested` clears contested statuses its extraction heuristic would
no longer assign. It now leaves a surprise flag alone
(`govwrite.IsSurpriseFlag`): the heuristic never produces it, so it would
otherwise clear every one as stale.

### 6. Causal edges still need evidence

Revision and reconciliation write verdict edges, status and validity. They
never write `causes`/`caused_by` (`assertNoCausalEdges` in
`internal/govwrite/revise_test.go`). The LLM and rule-based causal detectors
stay unwired (`TestDetect_NeverEmitsCausalEdges`). A decision with an observed
outcome is itself the recorded intervention with temporal precedence, but
this ADR records it as a verdict on the decision, not as a causal claim between
beliefs, because the outcome does not say which premise caused it.

## Consequences

### Measured: the shifting-world track

`data/eval/brainbench/shifting_world.yaml` (README, "Shifting world") replays
a scripted agent over five rounds after two facts change, on three arms: no
prediction recorded, the old loop used at its fastest (one expectation on
every rested-on belief, then the production sleep's ForgetRefuted), and this
ADR. Deterministic across runs; both noise arms reproduce their twins.

| Metric | evidence_only | current_loop | revision |
|---|---|---|---|
| `changed_accuracy` | 0.00 | 0.70 | 0.70 |
| `changed_correction_lag` (rounds) | unknown (never) | 0.00 | 0.00 |
| `stable_accuracy` | 1.00 | 0.07 | 1.00 |
| `stable_disturbed` | 0.00 | 1.00 | 0.33 |

**The speed hypothesis is not supported.** Revision corrects the changed
beliefs in the round the contradicting evidence arrives, the same round as the
old loop. That is structural, not a tuning gap: the old loop archives every
premise at the first sleep after a surprise, so nothing that waits for evidence
can be faster. What changes is the cost. The old loop answers 1 of 15
stable questions after it archives two true co-rested facts and the ledger
belief that a single outlier refuted. Revision keeps all of them answerable.
The ledger belief stays flagged contested (the 0.33), since nothing explained
its outlier. Against no prediction at all, both loops are faster. Evidence
alone never retires a stale fact that is better corroborated than the
document reporting the change.

So the defensible claim is: **as fast as the old loop, without destroying the
beliefs that were true.** A slower variant of the old loop would lose on
speed: an agent attaching the prediction to only one belief, or a sleep
without ForgetRefuted. We did not measure those, because claiming a speed win
over a weakened control is the result the harness exists to refuse.

### Other consequences

- **Reconcile is the trigger.** Revision runs when expectations are
  reconciled, on every transport, not in the sleep pass. A brain whose agent
  never reconciles is never revised.
- **Re-planning cost grows with refuted decisions.** Each pass reads every
  decision's expectation. That is bounded by the decision count. If it grows
  large, the fix is to skip refuted decisions whose rested-on beliefs are all
  retired.
- **A flag is not cleared by a later confirmation.** "Recalled without
  surprise is left alone" is taken literally. A belief that one outlier
  flagged stays contested until a surprise it rests on is explained, or a
  person resolves it.
- **Transport parity.** REST, gRPC, MCP and the Go client carry the
  expectation on decision writes and the decision observation
  (`internal/capability.Registry`).
- **Calibration** on the #441 harness is measured separately. By
  construction no per-belief verdict is added, so belief calibration cannot
  move through this ADR's edges. It can move through credit, which shifts
  trust.
