# ADR 0017: Predictive coding — the hierarchical prediction-error surface

- **Status:** Accepted; first slice implemented (v0.94.0). The full generative model
  remains future work (see "Not in scope").
- **Date:** 2026-07-12
- **Deciders:** Felix Geelhaar
- **Scope:** The one deferred north star of ADR 0015. Takes the first concrete,
  *safe* step toward predictive coding without pretending to build the whole reframe.

## Context

ADR 0015 named **predictive coding** the north star: a brain (Rao & Ballard 1999;
Friston free-energy) is a hierarchical generative model where each level *predicts* the
level below and only the **residual prediction error** propagates upward; learning is
the minimization of total prediction error (free energy) across all levels. Mnemos has
prediction error at the **decision→outcome** level only (`domain.Expectation.Surprise`)
and, separately, scattered error-like signals — contradictions, schema surprise,
calibration error — that no one view unifies.

The full reframe (a generative model that top-down predicts and actively minimizes free
energy) is research-scale and was deliberately left out of ADR 0015's batches. But there
is a legitimate, bounded **first step**, and it is the one predictive coding logically
requires first: **make hierarchical prediction error observable and aggregated.** You
cannot minimize a free energy you cannot measure; today mnemos cannot even *report* its
total prediction error or say which level of its hierarchy is most wrong.

## Decision

Ship the **prediction-error surface**: a strictly **read-only** metacognition view
(`mnemos predictive-error`, and a `Memory.PredictiveError` library method) that computes
prediction error at each level of the memory hierarchy from **existing** signals,
aggregates them into a single total (the free-energy proxy), and names the **hotspot** —
the level where the model is most wrong. It writes nothing; it is the measurement layer.

### Levels (each reuses a signal that already exists)

| Level | Meaning | Source (read-only) |
|-------|---------|--------------------|
| **outcome** | did acting on beliefs match reality? | `observedSurprises` — the decisions→beliefs→`Expectation.Surprise` walk (also feeds credit + neuromodulation) |
| **schema** | do generalizations still fit? | `GlobalSchema.Surprise` (peak PE backing each promoted generalization) |
| **dissonance** | is the belief set internally consistent? | `Hypercorrections` (validity-filtered active contradictions among entrenched beliefs) |
| **calibration** | is stated confidence honest? | `Calibration().ECE` (expected calibration error) |

Each level yields an error in `[0,1]` (surprises are saturated and normalized;
dissonance is contradiction-rate over the belief count; calibration uses ECE directly)
and a sample count. The **total** is the mean over the levels that actually have data
(a level with zero samples is omitted, not counted as zero — absence of evidence is not
low error). The **hotspot** is the highest-error level with data. Output is human or
JSON, mirroring `mnemos curiosity`.

### Why this is the right first slice

- **It is the objective made visible.** Predictive coding's whole content is "minimize
  the sum of prediction errors across the hierarchy." This surface *is* that sum,
  decomposed. Every later step (top-down prediction, active minimization) needs it to
  exist first — you optimize against a meter.
- **It is safe.** Strictly read-only — no new store surface, no write on any path, no
  schema change. It only reads signals other mechanisms already produce.
- **It is honest.** It does not claim to be the generative model. It unifies four
  error signals that were already there but siloed, into the one cross-level view the
  brain's free-energy framing calls for.

## Not in scope (the remaining north-star work)

- **Top-down generation.** A schema *actively predicting* what its member claims should
  assert (rather than us reading a stored peak surprise), and events being predicted
  from claims. Requires a generative model per level.
- **Active free-energy minimization.** Consolidation choosing what to revise so as to
  *reduce* total prediction error — turning the meter into a controller. This is the
  deep reframe; it composes with credit assignment (ADR 0014) and schema assimilation
  (ADR 0013 §5) but is a research effort of its own.
- **Extraction-fidelity level (claim vs source event).** No signal exists today for how
  faithfully a claim reflects its source event, so that hierarchy level is omitted here
  and noted as future.

## Consequences

- **Positive.** Mnemos can now report its total prediction error and where it is
  concentrated — the free-energy meter. Turns four siloed error signals into one
  actionable "where is my model most wrong?" view, and gives every future
  predictive-coding step something to optimize against.
- **Negative / honest limits.** It measures, it does not minimize; and it can only
  aggregate the levels that have data (a store with no resolved expectations reports no
  outcome-level error, correctly, rather than guessing). The generative-model reframe
  is still ahead.
- **Guardrails.** Read-only; no write path touched; a level with no samples is omitted
  from the total so sparse data never masquerades as a well-predicted model.

## Rollout

Shipped in v0.94.0: `Memory.PredictiveError`, `mnemos predictive-error` (human + JSON),
the four levels, the sample-gated total, and the hotspot — with tests over each level
and the aggregation. The generative model and active minimization remain the documented
north star; this ADR converts it from "someday" into "measured, and here is the meter."

**Follow-up (v0.95.0): exposed for agent active inference.** The meter is now a
`predictive_error` MCP tool, so an agent using mnemos as its brain can read "where is my
model most wrong?" and direct its own learning/verification there. That is *active
inference* — the consumer minimizing its own prediction error — the first place the
measure→act loop closes, ahead of baking active minimization into consolidation itself.
Exposed MCP-only (the agent-facing surface); the API-parity matrix records the deliberate
REST/gRPC asymmetry.
