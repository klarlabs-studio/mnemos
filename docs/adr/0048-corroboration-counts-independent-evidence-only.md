# ADR 0048: Corroboration counts independent evidence only

- **Status:** Accepted; implemented (repeat weight). The recall-seeded half is
  deferred.
- **Date:** 2026-11-26
- **Deciders:** Felix Geelhaar
- **Builds on:** ADR 0014 (outcome credit), ADR 0026 (canonical trust),
  ADR 0038 (source dependence), ADR 0046 (evidence origin)
- **Amends:** ADR 0046 (`domain.EvidenceRepeatWeight` 0.5 → 0; `trust/v5`)
- **Refs:** #443, measured on #441

## Context

Trust's corroboration factor is `1 + 0.2·ln(n)`. Through trust/v3, n was the
number of independent evidence origins plus half of the remaining episodes
(`distinct + ⌊0.5 · (total − distinct)⌋`). A repeat from an origin already
counted therefore still raised trust.

Mnemos's main writers are capture hooks. They re-capture the same narration and
the same assumptions across sessions, which is the setting in which repetition
and truth come apart. Repeated statements are judged truer regardless of their
truth (Hasher, Goldstein & Toppino 1977), even by people who know better
(Fazio et al. 2015).

The human labels on #441 showed the effect in a real brain. On 141 claims the
operator labelled true or false, v3 trust ranks truth backwards (AUC 0.33):
false claims averaged 42 evidence episodes and true ones 33.

## Decision

`domain.EvidenceRepeatWeight` is 0. The graded evidence count is the number of
independent origins (ADR 0046), excluding episodes that depend on another of
the belief's episodes (ADR 0038). A repeat is still stored, listed and counted
in the total, so it is provenance. It is no longer credibility. Outcome credit
(ADR 0014) remains how consequences raise credibility.

The trust model version becomes `trust/v5`. `trust/v4` is skipped because the
#441 records use it for the refuted #442 model. `mnemos recompute-trust --stale`
rescores stored rows.

**Deferred: episodes seeded by recall.** A session that recall showed a belief
to, and that then says the belief back, is repeating the brain rather than
reporting independently. Excluding such episodes needs a record of what recall
showed each session. No existing brain has that record, so the rule could not
be measured, and it does not ship on this measurement.

## Measurement (pre-registered on #441)

The test used 141 human-labelled claims (golden and heldout2), with v3 `At` at
one instant, comparing repeat weight 0.5 against repeat weight 0.

| | ECE | Brier | AUC |
|---|---|---|---|
| repeat weight 0.5 (v3) | 0.276 | 0.321 | 0.327 |
| repeat weight 0 (v5) | 0.245 | 0.275 | 0.339 |

- Δ ECE is −0.031, 95% CI [−0.093, +0.020]. The upper bound clears the +0.02
  margin, so the change is **non-inferior**. It is **not shown better**.
- The golden subset alone got worse: ECE 0.180 → 0.232, AUC 0.399 → 0.313.
- True and false claims average about the same number of independent origins
  (1.7 vs 2.0). No corroboration signal tracks truth on this brain; removing
  repetition only stops it pulling the wrong way.

The decision rests on the mechanism and on non-inferiority, not on a measured
gain. A larger labelled set should re-test it.

## Consequences

- The brainbench `captured_stream` scenario pins the behaviour. A falsehood is
  repeated in four chunks of one file and restated by a second session; a
  truth is stated once and borne out by one outcome. Trust was 0.923 for the
  falsehood against 0.874 for the truth, and is now 0.862 against 0.874
  (`TestRun_CapturedStreamTruthOutTrustsRepetition`).
- Beliefs whose trust came mostly from repetition lose up to about 0.2 of
  corroboration factor after `recompute-trust --stale`, which can move them
  below `--min-trust` or forgetting thresholds.
- Every backend reads the weight through `domain.EffectiveEvidenceCount`, so
  there is one place to change it and the cross-backend parity tests follow
  it.
