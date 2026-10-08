# ADR 0028: A computation budget on relate's candidates

- **Status:** Accepted (2026-10-07), consolidation program #382, Phase 5
  ("explicit computation budgets, not result budgets").
- **Date:** 2026-10-07
- **Deciders:** Felix Geelhaar
- **Builds on:** ADR 0027 (supports budget). That bounds what a write
  *keeps*. This bounds what it *examines*.

## Context

Incremental relate compares each new claim with every stored claim that
shares a content token with it (#420). A pair sharing no token cannot produce
an edge, so that candidate set is exact. It is not small: domain words such as
"service", "team" and "pipeline" are carried by a large share of any brain.

On the 1M-belief scale corpus each write's candidate set was ~502k claims,
half the brain. Remember took 7.05 s, 4.2 s of it fetching those candidates.
Every exact token-overlap scheme grows linearly with the corpus once common
words are involved. (The earlier "1.2 s at 1M" measured writes that skipped
relate entirely because of a bug, #427; it was never real.)

## Decision

Each new claim gathers at most `DefaultCandidateBudget` (5000) token-matched
candidates, chosen as follows (`relate.CandidateQuery.PlanTokens`):

1. Rank the claim's content tokens by document frequency, the number of stored
   claims carrying them, rarest first. Ties are broken by the token itself.
2. Take each token with its **whole** posting list while the running total of
   posting sizes stays within the budget.
3. A token that would overflow the budget is **skipped whole**. It is never
   cut part-way, which would make the choice arbitrary within it.

Claims a new claim cites, and test results, are candidates regardless of the
budget. The budget is per new claim, so a batch where one claim has only
common words does not starve another of its rare ones. Each answer reports how
many tokens were skipped. With `MNEMOS_RELATE_TRACE` on, a skipping write logs
`relate.candidates … skipped_tokens=N`. A negative budget restores the exact
candidate set; the equivalence tests use it.

Every store computes candidates through the same plan. SQLite reads document
frequencies from `claim_tokens`, then the postings of the planned tokens only,
so a skipped common token's posting list (half the brain, on a large one) is
never read. The memory store applies `SelectCandidates`, the reference the SQL
implementation is tested against.

## Consequences

- **What is no longer evaluated:** pairs whose only shared tokens are ones the
  budget skipped. Those share only common words, the pairs least able to reach
  the overlap a relationship needs: two shared tokens covering 30% of the
  shorter claim for supports, more for contradictions. The cost is not zero.
  A real relationship whose evidence is only common words can now be missed,
  on brains large enough for the budget to bind.
- **When it binds:** whenever one of a new claim's tokens is carried by more
  stored claims than the budget has left. Common domain words reach that on
  mid-sized brains: in the 100k benchmark, 7 tokens were skipped per write. On
  small brains no token is that common and the candidate set stays exact.
- **Cost**, measured with scalebench on the uniform corpus with the relate trace
  on (each write still keeps its 40 edges):

  | `Remember` | exact candidates | budget |
  |---|---|---|
  | 100k, p50 | 713 ms (~50k candidates) | **28.6 ms** (511) |
  | 1M, p50 / p95 | 7.05 s (~502k candidates) | **299 / 514 ms** (~3,950) |

  Document frequencies are counted only up to budget+1. A token above the
  budget is skipped whatever its exact count, so the plan is unchanged and a
  common token's count no longer walks its whole posting list. That took 1M
  from 968 ms to 299 ms.
- **Not measured:** relationship recall on a real brain under the budget.
  brainbench's seed brains are far below the budget. Revisit the default if
  missed relationships appear in practice; the trace line shows how often it
  binds.

## Alternatives considered

- **Exact candidates (status quo):** 7 s per write at 1M, linear in the corpus.
- **Ignore tokens above a corpus-share threshold (IDF):** more principled about
  which words are noise, but mid-frequency words still fan out at 1M. It
  composes with this budget and may follow.
- **Cap the candidates by an arbitrary subset** (first N by id): bounded but
  arbitrary. Rarest-first keeps the candidates most likely to relate.
