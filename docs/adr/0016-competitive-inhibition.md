# ADR 0016: Competitive inhibition — retrieval-induced forgetting

- **Status:** Accepted; implemented (v0.93.0).
- **Date:** 2026-07-12
- **Deciders:** Felix Geelhaar
- **Scope:** The last deferred mechanism of ADR 0015 §6. Completes the active-forgetting
  story: passive half-life decay (already shipped) + Hebbian edge decay (2b) + this,
  the *competitive* form.

## Context

ADR 0015 §6 specified two active-forgetting mechanisms: **edge pruning** (dropped in
2b — mnemos has no spurious co-occurrence edges to prune) and **competitive inhibition**
(deferred as "the most invasive remaining piece"). This ADR builds inhibition, the
brain's *retrieval-induced forgetting*: recalling a winning belief actively suppresses
the retrievability of the rival it beat.

The reason it was deferred is exactly the reason it must be built carefully. Mnemos is
an **evidence** system: a contradiction loser is *contested*, not *disproven* — it may
later gain evidence and be right. So inhibition must never do what forgetting-by-trust
does. It gets four hard constraints:

1. **Retrievability only, never trust or status.** Inhibition lowers a loser's ranking,
   nothing else. Trust, `status`, `valid_to`, and lifecycle are untouched.
2. **Bounded and small.** The ranking penalty tips ties; it can never sink a genuinely
   better match. Same guarantee the salience term carries.
3. **Reversible / temporary.** Inhibition **decays toward zero** during consolidation,
   so a suppression fades unless the winner keeps re-establishing it — retrieval-induced
   forgetting is transient in the brain, and a loser that later earns evidence recovers.
4. **Guardrailed.** A **promoted** (human-endorsed) or **high-salience** loser is never
   inhibited.

## Decision

### Signal — a reserved `confidence_components` key

Store a suppression **magnitude** in `[0,1]` under the reserved key `inhibition`
(`domain.InhibitionComponentKey`), the same no-schema-change pattern salience (ADR 0013
§4) and credit (ADR 0014) use. `Claim.Validate()` already constrains non-`credit:` keys
to `[0,1]`, so no validator change. `Belief.EffectiveInhibition()` reads it, returning
**0 when absent** — inert, so a graph that has never inhibited anything ranks exactly as
before. (Neutral is 0 here, unlike salience's 0.5, because inhibition is a one-sided
penalty, not a two-sided bias.)

### Write — at the retrieval write-back seam, opt-in

`query --inhibit` / `MNEMOS_INHIBIT`. After an answer resolves, `inhibitLosers` walks
`Answer.Verdicts` (the agent-mode contradiction resolutions, each a
winner/loser pair already selected by `resolveContradictionsForAgent` under its strict
confidence-floor + margin rule) and, for each clear loser, accumulates a bounded
inhibition increment (capped). It **reuses `ports.BeliefCreditWriter`** — reads the
loser, merges the `inhibition` key into its existing components, and writes back with
**trust passed through unchanged** — so no new store capability is added and constraint
(1) holds structurally. Promoted / high-salience losers are skipped (constraint 4).
Single-shot at the `AnswerWithOptions` seam (not inside `answerWithEvents`, which runs
twice under corrective retrieval), best-effort — a suppression-write failure never fails
the read. Requires `ConsumerAgent` (only that path resolves winners/losers).

### Read — a negative ranking term, always applied (inert when absent)

`rankClaimsByHybrid` subtracts a bounded `inhibitionScoreDelta(claim)` from each
signal-bearing claim's score, mirroring `salienceScoreDelta` but negative. **Unlike the
salience term, it is not gated by a query flag** — the whole point of *persisting*
inhibition is that a suppression established once keeps lowering the loser's rank on
later recalls, whether or not those recalls pass `--inhibit`. It is still inert when the
component is absent (magnitude 0), so nothing changes until inhibition is actually used.
`--inhibit` gates **writing** (does this query suppress its losers), not reading.

### Decay — reversibility

`consolidate --decay-inhibition` pulls every claim's `inhibition` component toward 0 by a
retain factor each pass (same asymptotic shape as Hebbian edge decay), via the reused
credit writer with trust unchanged. So inhibition is transient: it must be renewed by
repeated winning to persist, and a formerly-suppressed loser recovers its retrievability
over a few sleeps. `ConsolidateResult.InhibitionDecayed` reports the count.

## Consequences

- **Positive.** Completes ADR 0015 §6 and the brain/NN active-forgetting set: recalling a
  winner now actively (and reversibly) suppresses the rival it beat, so a decisively
  settled contradiction stops resurfacing the loser — without ever destroying the loser's
  evidence, trust, or status.
- **Guardrails.** Retrievability-only, bounded (tips ties), reversible (decays),
  promoted/high-salience-protected, opt-in to write, and — reusing the credit writer with
  trust passed through — structurally incapable of a silent trust change (ADR 0011).
- **Composes cleanly.** The `inhibition` component is a non-`credit:` key, so the credit
  pass preserves it and `RecomputeTrust` ignores it; salience and inhibition ranking
  terms add independently.
- **Negative.** One more retrieval write-back flag and one consolidation flag; both
  default off and inert. A persisted retrieval penalty is a genuine epistemic lever, so
  it is deliberately small, decaying, and guarded rather than a hard filter.

## Rollout

Shipped in v0.93.0: the `inhibition` component + `EffectiveInhibition`, the
`query --inhibit` write-back, the negative ranking term, `consolidate --decay-inhibition`,
guardrails, and tests (the write-back seam, the ranking penalty, the promoted/salience
skips, and the decay). This closes ADR 0015's deferred concrete work; **predictive
coding** remains the sole documented north star.
