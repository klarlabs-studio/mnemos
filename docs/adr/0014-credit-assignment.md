# ADR 0014: Credit assignment — outcomes update belief trust

- **Status:** Accepted
- **Date:** 2026-07-12
- **Deciders:** Felix Geelhaar
- **Scope:** The cognitive core. Implements the capstone (§1) of ADR 0013's
  cognitive-completeness roadmap. Extends ADR 0011 (Complementary Learning
  Systems) — specifically its "borrow the brain's dynamics, keep the citation
  trail" guardrail — and consumes the `domain.Expectation` prediction-error
  signal and the `Decision → Beliefs[] → Outcome` chain that already exist.

## Context

`RecomputeTrust` is **evidence-only**: `trust_score = confidence × corroboration ×
freshness` (`ListClaimTrustInputs`). It answers "is this claim corroborated, and
recently?" but never "did acting on it work out?". Meanwhile `domain.Expectation`
already mints a first-class **prediction-error** scalar — a decision's forward
prediction (`Predicted ± Tolerance`) reconciled against an `Observed` value,
yielding a `validates`/`refutes` verdict and a tolerance-normalized `Surprise()`.
But that surprise fed only consolidation ranking and `GlobalSchema.Surprise`; it
**never updated a belief's trust**. The learning loop was therefore open: a belief
whose decisions repeatedly led to bad outcomes did not lose trust, and one that led
to good outcomes did not gain it.

Mnemos is uniquely able to close this loop because it stores the whole chain —
`Decision.Beliefs []ClaimID` and `Decision.OutcomeID`, plus the `Expectation`
attached to the belief that carried the prediction. This is the symbolic analogue
of a neural network's **gradient** / RL **temporal credit assignment**: update the
weights (trust) of the units (beliefs) in proportion to their contribution to the
error (outcome vs expectation). Dopaminergic prediction error is the same signal in
the brain.

## Decision

Add a **credit-assignment pass** that reconciles resolved expectations into signed
errors and propagates a **bounded, decayed, attributed** trust delta to the beliefs
that informed each decision. It runs as a consolidate stage: `mnemos consolidate
--credit` (library: `ConsolidateOptions.AssignCredit`).

### 1. The error signal

For each belief that carries an **observed** expectation, `Expectation.Surprise()`
gives the tolerance-normalized magnitude `s` (`|predicted − observed| / tolerance`).
The pure `internal/credit.decisionDelta` maps it to a signed, bounded credit:

- **Validated** (`s ≤ 1`, within tolerance): positive credit `+Step · (1 − s)` —
  largest for a tight hit, fading to zero at the tolerance edge.
- **Refuted** (`s > 1`): negative credit `−Step · min(s − 1, R)/R`, saturating over
  `R = 3` tolerance-units past the band so an extreme outlier can't dominate.

The result is always within `[−Step, +Step]` with `Step = 0.10`. An unobserved (or
absent) expectation contributes nothing — no error has been measured, so its beliefs
are left unchanged.

### 2. Belief linkage

The expectation attaches to a belief claim; a `Decision` lists that claim among its
`Beliefs`. So for each decision we look up the expectation on each of its beliefs;
when one is observed, its decision-level credit is **split equally across all of the
decision's beliefs** (equal split for v1 — every belief was a load-bearing input;
weighting by how load-bearing each was is left as future work). Contributions are
keyed by `(decision, prediction)`, so distinct decisions and distinct predictions
attribute independently. `internal/credit.Assign` computes this as a pure,
deterministic function (no I/O, no wall clock).

### 3. The update — bounded, decayed, attributed

Each contribution is stored in the belief's existing **`confidence_components`**
map under a key `credit:<decisionID>:<predictionClaimID>` holding the signed
per-belief delta. This **reuses an existing column** — no new table, no cross-backend
schema change — and the key **doubles as the attribution audit trail** the ADR-0011
guardrail requires: every trust change records which decision and prediction drove
it. Trust is then set to

```
trust_score = clamp01( base_trust + clamp(Σ credit contributions, −CreditCap, +CreditCap) )
```

on top of the evidence-based `base_trust` that `RecomputeTrust` just produced.

- **Bounded:** each contribution ≤ `Step`; the accumulated credit is clamped to
  `±CreditCap = 0.30`.
- **Decayed:** the `CreditCap` clamp is a saturating eligibility — repeated
  same-sign outcomes have diminishing marginal effect once the cap is reached,
  rather than an unbounded gradient. (The refute magnitude also saturates.)
- **Attributed:** the driving decision/prediction is preserved in
  `confidence_components`; trust is never silently mutated.

### 4. Idempotency

The pass is deterministic and self-healing. `Assign` has no wall-clock term, so the
same decisions + expectations always yield the same contributions; those are written
by **assignment** (credit keys replaced, other components preserved), never
accumulation. Trust is recomputed each pass as `base + creditSum`, where `base` is
the freshly recomputed evidence trust — so a second pass produces byte-identical
credit components and the same trust. Re-running never double-credits. (Credit runs
immediately after `RecomputeTrust` in the consolidate pass, before forgetting, so a
belief reality refuted can be swept up by `--forget-below-trust` in the same pass.)

### 5. Storage seam and backend scope

Persistence is an **optional capability**, `ports.BeliefCreditWriter`, implemented by
the backends that fully round-trip `confidence_components`: **SQLite** (and therefore
local **libSQL**) and the in-memory store. The pass type-asserts for it exactly like
`ports.TrustScorer`: a backend that cannot persist the attribution map **skips**
credit assignment rather than mutating trust it could not attribute — the ADR-0011
"no silent trust change" guardrail applied at the storage seam. Postgres and MySQL
carry the `confidence_components` column in their schema but their Go repositories do
not yet round-trip it, so credit assignment is a no-op there; wiring that column into
those repositories is a mechanical follow-up that needs no new migration. This keeps
the change free of the cross-backend column-parity risk called out in ADR 0007's
"new table" gotcha.

## Consequences

**Positive**

- Mnemos learns from *being wrong*, not only from *being corroborated*: a belief's
  trust now reflects the realized consequences of the decisions it informed. This is
  the missing half of learning and the highest-leverage roadmap item (ADR 0013 §1).
- Fully auditable: every trust change carries provenance in `confidence_components`.
- No schema change; no new cross-backend surface. Additive optional capability keeps
  DB-provider CI green.

**Negative / open**

- v1 uses **equal split** across a decision's beliefs; load-bearing weighting is
  future work.
- Credit is materialized at consolidation time (`base + creditSum`) rather than baked
  into `RecomputeTrust`; a plain `RecomputeTrust` outside a credit pass shows the
  evidence base, and the next `--credit` pass re-materializes credit from the durable
  `confidence_components` audit trail. This is intentional (credit is a
  consolidation-time effect) but means the two must be run together to see the
  combined score.
- Postgres/MySQL credit is a no-op until their repositories round-trip
  `confidence_components`.

## Rollout

1. **This ADR** + the pass: `internal/credit` (pure), `ports.BeliefCreditWriter`
   (SQLite + memory), `memory.assignCredit`, and the `consolidate --credit` /
   `ConsolidateOptions.AssignCredit` wiring. Done.
2. Follow-up: round-trip `confidence_components` in the Postgres/MySQL repositories to
   extend credit assignment to those backends.
3. Follow-up: load-bearing weighting of credit across a decision's beliefs.
