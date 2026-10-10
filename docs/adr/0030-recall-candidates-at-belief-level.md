# ADR 0030: Recall gathers candidates at the belief level; spreading activation is opt-in

- **Status:** Accepted (2026-10-10), Cognitive quality milestone. Fixes #456.
- **Date:** 2026-10-10
- **Deciders:** Felix Geelhaar
- **Amends:** ADR 0013 §2 (spreading activation was on by default).

## Context

The #441 Phase A evaluation put Mnemos at 27.9% answer accuracy on LoCoMo,
against 49.2% for BM25 over the raw conversation and 62.5% for full context.

For single-hop questions whose short answer appears verbatim in an evidence
session (n=376), the answer was in some belief in the brain 91.5% of the time.
It was in recall's top 100 only 58.2% of the time. **Extraction kept the fact;
recall did not surface it.**

Two mechanisms caused this. They were measured on the same 10 brains,
re-queried from identical snapshots. The figures are the share of answerable
single-hop questions (n=360) whose answer ranked in recall's top *k*:

| Recall configuration | R@5 | R@20 | R@100 |
|---|---|---|---|
| Default (all cognitive behaviours on) | 15.6% | 49.7% | 63.3% |
| Spreading activation off | 39.4% | 53.3% | 63.3% |
| Salience off | 15.6% | 49.7% | 63.3% |
| Hebbian + reconsolidation off | 15.8% | 50.0% | 63.3% |
| Inhibition off | 15.6% | 49.7% | 63.3% |
| Plain cosine over the brain's own vectors (reference) | 49.4% | 64.2% | 78.3% |

1. **Episode gate.** Recall ranked episodes and considered only the beliefs
   linked to the top `answerEventLimit` (5). A LoCoMo conversation has about 50
   episode chunks, so about 90% of a brain's beliefs were never candidates.
   This set the 63.3% ceiling at R@100, which no toggle moves.
2. **Spreading activation.** Priming re-sorts the retrieved beliefs by their
   supports edges to the top seeds. With up to 20 supports edges per belief
   (ADR 0027), it lifts whole clusters about the same subject above the belief
   that answers the question. Asked "What filling did Joanna use in the cake…",
   recall returned six beliefs *about Joanna* from other sessions. Spreading
   activation alone cut R@5 from 39.4% to 15.6%.

## Decision

1. **Candidates are gathered at the belief level.** Corpus-wide recall adds, to
   the beliefs linked to the top episodes:
   - the `claimCandidateK` (50) beliefs most similar to the question by vector;
   - the 50 best by belief BM25.

   The existing hybrid ranker (cosine + BM25, max-normalised) orders the pool.
   The episode-linked beliefs remain candidates, so nothing that was reachable
   becomes unreachable. Run-scoped recall (`AnswerForRun`) is not widened, so
   it never returns beliefs from another run.
2. **Spreading activation is opt-in.** It now requires `--prime` or
   `MNEMOS_SPREADING_ACTIVATION=true`. Salience, Hebbian, reconsolidation and
   inhibition stay on by default; none of them moved R@k. ADR 0013 holds that
   these behaviours *are* the memory model. That position stands for the four,
   but a default must not cost a measured 24 points of R@5. Priming becomes a
   default again only when a bounded form shows a measured gain on the #441
   harness.

## Consequences

- **Recall,** same brains, same questions:
  - R@5: 15.6% → **49.4%**;
  - R@20: 49.7% → **64.2%**;
  - R@100: 63.3% → **84.7%**.

  That matches plain cosine at R@5 and R@20, and beats it at R@100, where BM25
  contributes what vectors miss.
- **Answer accuracy** is re-measured by the #441 Phase A LoCoMo re-run (the
  before-number is 27.9%). It is recorded on #441, not asserted here.
- **brainbench:** no verdict changes across its 3 scenarios. `answer_mrr` moved
  within one item per scenario.
- **Cost:** each corpus-wide recall adds one claim-vector search over the
  claim embeddings (topK 50) and one claims FTS query (limit 50). The scale gate
  records the effect at 100k beliefs.
- **Behaviour change for callers relying on priming by default:** pass
  `--prime` or set `MNEMOS_SPREADING_ACTIVATION=true`.
