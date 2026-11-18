# ADR 0039: Trust and freshness are recall ranking inputs

- **Status:** Accepted; implemented.
- **Date:** 2026-11-18
- **Deciders:** Felix Geelhaar
- **Builds on:** ADR 0026 (one canonical trust per belief, per instant),
  ADR 0013 §4 (the bounded salience term), ADR 0036 (recorded-as-of reads)
- **Adds:** the trust and freshness factors in `query.Engine.rankClaims`
  (`internal/query/currency.go`), `query.Ablation.Trust` and
  `query.Ablation.Freshness`, and their `minus_trust` / `minus_freshness`
  rows in the retrieval ablation

## Context

The retrieval ablation (#531, `make retrieval-ablation`) found that Mnemos
failed `stale_and_superseded/p-limit` and `p-deploy` under every policy. In
both, a statement 620 to 650 days old ("rate limit is one hundred requests",
"deployments are performed manually") scored slightly higher on similarity
than its current replacement and ranked first. The recency-weighted BM25
baseline ranked the current statement first. In `make brain-eval` the same
scenario's untreated Mnemos arm had precision@1 0.50 and MRR 0.75, below both
raw baselines (0.75/0.875 and 1.00/1.00).

Trust and freshness were computed, stored, reported and gated on, but neither
entered the claim order. Recall's order was hybrid similarity, plus the
salience and inhibition terms. Trust was read only by `MinTrust` and, as
credibility, by agent-mode contradiction resolution. Freshness only marked
`StaleClaimIDs`.

## Decision

### 1. A bounded multiplicative factor on relevance

For every belief with a relevance signal, the ranker multiplies its normalised
hybrid score by two factors before the salience and inhibition terms:

    score' = relevance × trustFactor × freshnessFactor
    trustFactor     = 1 − 0.2 × (1 − trust)
    freshnessFactor = 1 − 0.3 × staleness

Both factors lie in (0, 1], so the combined factor lies in
[`currencyMinFactor` = 0.8 × 0.7 = 0.56, 1].

The factor is multiplicative, not additive like salience, because that makes
both requirements structural:

- **Freshness alone cannot surface an irrelevant belief.** A belief with no
  relevance signal keeps the ranker's no-signal sentinel and is never
  re-weighted. A scored belief's factor only scales its own relevance down. A
  fresh, fully trusted belief can overtake a stale, untrusted one only when its
  relevance is at least 56% of the other's. An additive term would give a weak
  fresh match the same absolute lift as a strong one.
- **A stale high-similarity belief cannot drown a fresh relevant one.** The
  stale belief keeps at least 56% of its relevance and at most all of it.

For a negative (anti-correlated) cosine score the discount is applied to the
magnitude (`s − |s|·(1−f)`), so a discounted belief always moves down.

### 2. Freshness is canonical trust's decay, not a new one

`staleness` is `trust.Staleness`, rescaled from `[0, 1 − FreshnessFloor]` to
`[0, 1]`. That is the exponential decay, 0.3 floor and per-belief
`HalfLifeDays` that `trust.At` uses. The reference is the one `trust.At`
decays from: the belief's evidence date (`ValidFrom`, set from its source
episode) or its last explicit confirmation (`LastConfirmed`), whichever is
later.

`LastVerified` is not used. Recall reconsolidation and sleep replay bump it as
rehearsal (ADR 0026), so a ranking input read from it would let recall refresh
its own winners. A confirmation dated after the read's instant had not
happened at that instant and is ignored. An undated belief is not penalised,
as everywhere else in the trust package.

### 3. Measured at the read's instant

Freshness is measured at `readInstant`: `RecordedAsOf` when set (the brain at
T, whose now is T, as admission already uses for "valid now", ADR 0036 §1),
else `AsOf` (how recently a belief had been evidenced as of the asked-about
day), else the wall clock.

Trust is the belief's canonical `TrustScore`. On a recorded-as-of read the
engine reads through `internal/recorded`, which sets it to trust at T and
rewinds `HalfLifeDays` and `LastConfirmed` to T (ADR 0036 §7), so both factors
are at T without code of their own. On a current or valid-time read it is the
stored value, the cache of `trust.At` that Get and the API report.

### 4. The weights, and why these

- **`freshnessRankWeight = 0.3`.** A belief at the freshness floor keeps 70%
  of its relevance. Kept below 0.5 so freshness alone can never reorder two
  beliefs whose relevance differs by 2× or more.
- **`trustRankWeight = 0.2`.** Smaller than the freshness weight because
  canonical trust already contains a freshness factor. At full strength the
  two would count decay twice. Trust's own contribution here is confidence,
  corroboration and credit.

These are a priori bounds, not values fitted to the benchmark. The salience
term (0.2) sets the scale. Section 5 shows what the benchmark measured at
them.

### 5. Measured effect

Offline brainbench runs (rule-based extraction, stub embedder), before and
after this change:

| | before | after |
|---|---|---|
| `mnemos_default` successes, budgets 32 / 64..2048 | 39 / 41 of 44 | 40 / 41 of 44 |
| `p-deploy` gold rank (every budget) | 2 | 1 |
| `p-limit` gold rank (every budget) | 2 | 1 |
| `p-deploy` at 32 tokens | gold over budget | success |
| `p-limit` at 32 tokens | superseded in context | superseded in context |
| brain-eval `stale_and_superseded` control precision@1 / MRR | 0.50 / 0.75 | 1.00 / 1.00 |
| ... vs `raw_bm25_recency` (1.00 / 1.00) | baseline better | tie |

Across all 17 policies × 7 budgets × 44 questions, 175 cells changed. Each one
was a better gold rank or a failure turning into a success. No question that
passed before fails now, and no gold rank got worse. Every other brain-eval
metric, verdict and baseline comparison is unchanged. The `Recall Gate`
real-trace holdout is identical (pass rate, P, R and F1 all 1.0).
`TestRecallParity_EverySurfaceAgrees` and the `data/eval` suites pass.

Each factor alone is enough to put the current statement first in both probes:
`minus_trust` and `minus_freshness` each still pass `p-deploy` at 32 tokens.

`p-limit` at 32 tokens still fails. The cause is token packing, not ranking.
Mnemos's two rate-limit beliefs are 66 and 65 characters as context lines.
Together that is 131 characters, which the chars/4 estimate counts as 32
tokens, so the superseded line fits beside the current one. The raw
baselines' chunks keep the sentence's final period, so their two lines total
133 characters, or 33 tokens, and only the top line fits. Mnemos and the
recency baseline now produce the same order.

A first version took the freshness reference from `LastVerified`, the same
reference as `StaleClaimIDs`. Under it, `p-deploy` needed both factors and
`minus_trust` lost it. The scenario's warm-up recall had rehearsed the stale
statement and refreshed it. That is the loop §2 describes, and it showed up in
the benchmark.

### 6. Ablation

`query.Ablation` gains `Trust` and `Freshness`, one per factor, and the
retrieval ablation gains the leave-one-out rows `minus_trust` and
`minus_freshness`. The similarity-only policies switch both off. No surface
exposes them.

## Consequences

- Every recall surface ranks with the factors, because they live in the one
  ranker behind `query.Engine.Recall`. `TestRecallParity_EverySurfaceAgrees`
  still passes.
- Recall now does part of what forgetting was credited with on
  `stale_and_superseded`. In `make brain-eval` the untreated arm already ranks
  the current statement first, so the consolidation A/B no longer shows a
  precision@1/MRR gain there. That gain belonged to ranking, not to forgetting.
  Forgetting still removes the superseded statements from the context
  (`forbidden_hit_rate` 1.0 → 0.0).
- A durable fact with no recent evidence loses up to 30% of its relevance to a
  fresher belief. Per-belief `HalfLifeDays` sets how fast that happens, as it
  already does for trust, and the 0.56 bound limits how much it can cost.

## Limitations

- **Ranking cannot remove a statement from the context.** At every budget large
  enough to hold both statements, `p-limit` and `p-deploy` still fail under
  every policy, the raw baselines included, because the superseded statement
  is in the packed context. Only curation can fix that: detecting that the new
  statement supersedes the old one. `p-region` passes because a contradiction
  edge is detected there and agent-mode resolution drops the loser. No edge is
  detected between "one hundred" and "one thousand requests per minute", or
  between "manually by an engineer" and "automatically by the release
  pipeline". That is a relationship-detection gap and is out of scope here.
- **The evidence date is `ValidFrom`, not the latest corroborating episode.**
  The ranker has the claim row, not its evidence links. Canonical trust reads
  the latest episode, so later corroboration refreshes trust but not this
  factor. This is the same proxy `StaleClaimIDs` uses.
- **Trust on a valid-time read is current trust**, not trust as of `AsOf`.
  `AsOf` asks about the world on a day, using what the brain knows now, and
  current trust is that knowledge.
