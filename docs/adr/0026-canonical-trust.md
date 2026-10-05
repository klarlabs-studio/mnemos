# ADR 0026: One canonical trust value per belief, per instant

- **Status:** Accepted (2026-10-04). Implementation is tracked in #386 under the
  consolidation program (#382, Phase 2).
- **Date:** 2026-10-04
- **Deciders:** Felix Geelhaar
- **Supersedes:** the "Known limit" in ADR 0019 (stored trust uses a global
  half-life) and the "run them together" caveat in ADR 0014 (credit is erased by
  any recompute).

## Context

Different subsystems compute different trust values for the same belief at the
same instant (#386 maps every site). For a volatile belief (`HalfLifeDays = 14`,
confidence 0.9, one episode 30 days old):

| Path | Trust | Formula |
|---|---|---|
| Stored `trust_score` (Get/list, `low_trust`, forgetting, curiosity) | 0.645 | `trust.Score`, global 90-day constant |
| Recall, `MinTrust`, `Result.TrustScore` | ≈0.559 | `ScoreCredibility` over the stored value, recency τ = 180 |
| Health `trust_decay`, float-back gate | 0.27 | `ScoreWithHalfLife`, the belief's own 14 days |

The root cause is a port. `TrustScorer.RecomputeTrust` takes
`score func(confidence, evidenceCount, latestEvidence)`. That signature has no
room for the belief's half-life, its last verification or its credit, so the
stored value cannot honour any of them. Every consumer that needed them computed
its own variant instead.

Two further defects follow from the same gap:

- **Credit is erased.** `assignCredit` writes stored trust as base + credit, and
  the next scoped recompute triggered by any ingest touching the belief rewrites
  it to the base. The `credit:*` audit keys are left behind.
- **Confirmation is ignored.** `verify` and `--reinforce-validated` record that
  a belief is still true, but stored trust never reads it.

## Decision

### 1. One function: `trust.At`

```text
TrustAt(belief, evidence, at) = clamp01( base + credit )

base      = clamp01( confidence × (1 + 0.2·ln(max(1, n))) × freshness )
n         = EffectiveEvidenceCount(distinct sources, total links)   (graded, unchanged)
freshness = max(0.3, exp(−d / τ))
τ         = HalfLifeDays if > 0, else 90                            (per belief)
d         = days from ref to at;  ref = max(latest evidence, LastConfirmed)
            ref zero or in the future → freshness 1                 (unchanged)
credit    = clamp(stored applied credit, −0.30, +0.30)              (ADR 0014 cap)
```

`trust.At` is pure: no I/O and no wall clock. The instant is a parameter. Every
subsystem that needs a belief's trust reads the stored value or calls `trust.At`.
There is no third formula.

### 1a. Confirmation, not rehearsal

`LastVerified` cannot be the confirmation input. It is written by four things:
explicit `verify`, a validated outcome, sleep replay rehearsal (in the default
sleep), and recall when reconsolidation is enabled (`--reconsolidate`, off by
default). If it fed trust, recalling or rehearsing a belief
would refresh its own trust. That is a feedback loop, and brainbench measured
its effect: in `stale_and_superseded`, consolidation retired 0 of 4 stale
beliefs instead of 4.

`claims.last_confirmed` is the confirmation input. It has one writer,
`MarkConfirmed`, called by the governed `verify` (CLI, MCP `memory_promote`)
and by validated-outcome reinforcement. Recall and replay keep bumping
`last_verified` only, which still drives liveness and replay recency.

### 2. Recall credibility is a ranking signal, not trust

`ScoreCredibility` (authority, citations, execution liveness, test results) stays
as a recall-time *ranking* signal derived from `trust.At`. It is renamed in code
and on the wire, and it is never reported under the name "trust":

- `Result.TrustScore` and the `trust_score` field of recall/search responses
  carry `trust.At`, the same number `Get` returns. Credibility gets its own field.
- `MinTrust` filters on `trust.At`. A caller asking for "trust ≥ 0.7" gets the
  value that `Get` reports, not a different number on the same scale.

This is a visible change in recall responses. It is the point of the decision:
one number per belief per instant, whoever asks.

### 3. Credit is a stored component, re-applied on every recompute

`assignCredit` additionally stores the **applied** net credit (after the ADR-0015
resistance and gain modulation, before the cap) in `confidence_components` under
`domain.CreditAppliedComponentKey`. `trust.At` adds it on every recompute, so an
ingest, `recompute-trust` or post-dedupe rescore no longer erases it. The
per-decision `credit:*` keys remain the audit trail. The applied key is what
scoring reads.

### 4. `HalfLifeDays` keeps its arithmetic and is documented as a time constant

`exp(−d/τ)` leaves 37% of freshness at `d = τ`, not 50%. The arithmetic is kept,
so ranking is not perturbed by a units change. Code comments and docs call it the
**freshness time constant** (e-folding time) instead of a half-life. The column
and field names stay as they are (`half_life_days`, `HalfLifeDays`), because
renaming them breaks the storage schema and the vocabulary freeze in
`docs/vocabulary.md`. `internal/synthesize` keeps its true half-life, which is
documented as a different quantity.

### 5. Stored trust is a versioned cache

`claims` gains `trust_computed_at` and `trust_model_version`. The value computed
by §1 is `trust/v2`. Everything written before this change is implicitly
`trust/v1`, the global-constant formula. A stored value is authoritative only as
"`trust.At` at `trust_computed_at` under `trust_model_version`".
`mnemos recompute-trust` backfills rows whose version is not current. It runs in
bounded batches through the governed writer and reads each batch back, in the
same way as `recompute-half-life`. It never rewrites the whole store as a side
effect of an ordinary write.

## Consequences

- **Stored values move.** Volatile beliefs drop, recently verified beliefs rise,
  and credited beliefs keep their credit. A brain's `low_trust` rate,
  forgetting candidates and float-back eligibility change on the first
  backfill. The release notes for the consolidation release must say so, and
  Phase 7 compares the before/after distributions on a real brain.
- **Recall responses change meaning.** `trust_score` in recall/search becomes
  the canonical value. Consumers that ranked by it see slightly different numbers
  but the same ordering signal they were promised.
- **Health becomes internally consistent.** `low_trust` and `trust_decay` measure
  the same quantity, one now and one projected forward.
- **One test guards the invariant:** for a fixed belief, evidence set, model
  version and instant, every subsystem derives the same trust.

## Rollout

Each step is its own PR, in order:

0. `claims.last_confirmed` with `MarkConfirmed` as its only writer (#403).
1. `trust.At` and a `TrustInput` port signature (#404). Backends select `last_confirmed`,
   `half_life_days` and the applied-credit component. All four writers
   (pipeline, `Consolidate`, `recompute-trust`, post-dedupe) switch to it.
   `assignCredit` stores the applied credit.
2. `trust_computed_at` and `trust_model_version` columns on every backend, plus
   the bounded, verified backfill.
3. Consumers: health, float-back and curiosity read `trust.At`. Recall
   credibility is renamed and reported separately, and `MinTrust` moves to
   `trust.At`.
4. A cross-subsystem invariance test.
