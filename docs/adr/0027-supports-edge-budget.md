# ADR 0027: A per-claim budget on inferred supports edges

- **Status:** Accepted (2026-10-06), consolidation program #382, Phase 5.
- **Date:** 2026-10-06
- **Deciders:** Felix Geelhaar

## Context

`relate` infers a `supports` edge between two claims that share at least two
stemmed content tokens covering 30% of the shorter claim. Nothing bounded how
many such edges one claim could have. Domain vocabulary ("service", "team",
"pipeline") passes that rule against a large share of any brain, so the graph
grew with the corpus rather than with what claims actually corroborate:

- A real, 233,631-claim brain held **32.8M relationships, 32.4M of them
  `supports`**. The median claim had 111 outgoing supports edges; 5,307 claims
  had more than 1,000 (8.6M edges between them). The file was 10 GB.
- On the 100k-belief scale corpus, one `Remember` of two claims wrote ~33,400
  edges, linking each new claim to a third of the brain. A CPU profile put 60%
  of the write in `RelationshipRepository.Upsert`.
- Every consumer pays for the fan-out: recall's hop expansion and spreading
  activation traverse supports edges, and `BrainHealth` loads every edge.

An edge shared with a third of the corpus says the claims use common words, not
that one corroborates the other.

## Decision

Each detection pass keeps, per source claim, at most `DefaultSupportsBudget`
(20) supports edges: those with the highest **Jaccard overlap of content
tokens**, ties to the earlier target. It applies to both `DetectIncremental`
(new claims against the corpus) and `Detect` (pairs within one batch).

- **Only supports edges are budgeted.** Contradictions, citations, test
  conflicts and every other type are kept in full. They are rare, and each one
  is signal.
- **Jaccard, not the overlap rule's own ratio.** The acceptance rule measures
  overlap against the shorter claim, so a three-token claim scores 1.0 against
  anything containing its words. Jaccard is measured against the union, which
  ranks genuinely close claims first.
- **Deterministic.** The scan and indexed paths report identical strengths
  (`TestScanAndIndexPathsAgree`), so the same edges are kept whichever path
  runs. IDs are minted only for kept edges.
- `Engine.WithSupportsBudget(n)` overrides it; `n < 0` restores the unbounded
  behaviour (used by the equivalence tests, which check the optimisation
  separately from this semantic change).

## Consequences

| | Before | After |
|---|---|---|
| Supports edges per new claim | unbounded (~16,700 at 100k) | ≤ 20 |
| `Remember` p50, 100k uniform / hub | 1,593 / 1,502 ms | 799 / 772 ms |
| Edges written per two-claim `Remember`, 100k | ~33,400 | 40 |
| DB after 20 captures on a 100k brain | 342 MiB | 135 MiB |
| Recall hops 1 p50, 100k | 47 ms | 48 ms |

Impact on behaviour:

- **Hop expansion and spreading activation reach a claim's strongest
  neighbours,** not every claim that shares two words with it. That is the
  intent. A weakly related claim that used to be one hop away may now be two
  hops away, or unreachable through supports edges.
- **Existing edges are not touched.** A brain created before this change
  keeps its edges. Thinning them is a separate, explicit operation; it is
  never a side effect of upgrading.
- **Not measured: recall quality on a real brain.** brainbench cannot see this
  change, because its seed brains hold 0–8 relationships and the budget never
  binds. A/B runs on main and on this change differ only within the known
  run-to-run noise (#396). The scale corpus has no ground truth. Judge the
  quality effect on real brains, and revisit the default of 20 if hop recall
  degrades.

## Alternatives considered

- **Ignore tokens most of the corpus shares, as IDF weighting does.** More
  principled, but it needs corpus-wide token statistics (a persisted token
  index). It may follow; it composes with this budget.
- **Keep the semantics and only make writes faster.** Leaves the edge count
  growing with the corpus, and with it every traversal and health scan.
