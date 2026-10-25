# ADR 0033: Merged beliefs become redirects

- **Status:** Accepted; implemented.
- **Date:** 2026-10-25
- **Deciders:** Felix Geelhaar
- **Amends:** the semantic-dedupe merge (`internal/pipeline/semantic_dedupe.go`),
  and the write path's duplicate check (`internal/pipeline/known_claims.go`).
- **Issue:** #387

## Context

Semantic dedupe runs on every consolidation, including the default sleep. It
clusters beliefs whose embeddings have a cosine of 0.92 or more and folds each
cluster into one winner. #397 fixed the planner: it no longer merges across
`contradicts`/`refutes` edges, negation, scope or type, and it uses complete
linkage. The apply step still **hard-deleted every loser** after moving its
evidence, edges, entity links, expectation and feedback to the winner. Deletion
took the loser's text, its `claim_versions` and its `claim_status_history` with
it.

That loses two things:

- **Auditability.** A merge is a judgement that two texts state the same belief.
  Once the loser is gone, nobody can see what was merged, why, or what the loser
  said, and a wrong merge cannot be undone.
- **Identity.** Anything that recorded the loser's id (a decision's beliefs, an
  exported link, a log line) now points at nothing.

Keeping the loser naively is not enough either. The write path folds a
restatement only onto a *live* claim. A deprecated loser would be skipped, the
restatement would come back as a new claim, and the next sleep would merge that
copy again. Evidence would eventually reach the winner, but only after another
consolidation, and only through another similarity judgement.

## What the practice and literature say

- **Wikidata merges into redirects, never deletions** ([Help:Merge],
  [Help:Redirects], [RfC: redirect vs. deletion]). Item ids are persistent
  identifiers, so a merged item becomes a redirect to the survivor. Redirects are
  never deleted or reused. References are rewritten lazily, which keeps a merge
  easy to revert, and a bot collapses double redirects.
- **Master data management keeps source records and supports unmerge**
  (e.g. [Salesforce match/merge], [Reltio merge workflow]). Source records survive
  under the merged master. Unmerging is a supported operation that restores the
  original cross-references.
- **Similarity is not identity.** Halpin et al. ([When owl:sameAs isn't the
  Same], ISWC 2010) show that identity links are often asserted more strongly
  than the evidence supports. A cosine of 0.92 is a similarity judgement, so a
  merge built on it has to stay auditable and reversible.
- **W3C PROV** models this as lineage: the winner `wasDerivedFrom` the loser.

## Decision

A merge keeps the loser as a **redirect**.

1. **`claims.merged_into`** holds the winner's id; empty means not merged. The
   column exists on every backend (SQLite, libSQL, Postgres, MySQL, memory). The
   upsert keeps a stored value when a write carries none (`CASE WHEN <incoming>
   <> '' THEN <incoming> ELSE <stored> END`), because re-extraction rebuilds a
   claim from its text and never sets it.
2. **The loser is deprecated and pointed at the winner before anything moves**,
   so an interrupted merge leaves a retired row, never an active claim without
   evidence. Evidence, edges, entity links, expectation and feedback move to the
   winner as before. The loser's embedding is deleted. Its `claim_versions` and
   `claim_status_history` stay.
3. **Restatements follow the redirect.** When no live claim holds a belief, the
   write path looks up redirects with the same text key, scope and audience
   (`ClaimRepository.ListMergedByTextKeys`) and folds the restatement onto the
   live claim at the end of the `merged_into` chain. A chain is followed for at
   most 8 hops. A chain that ends at a claim that is gone or no longer live
   resolves to nothing, and the belief comes back as a new claim, as before.
4. **Ids are never reused.** A redirect is an ordinary deprecated row, so its id
   stays taken.

`merged_into` is a column, not a relationship edge. A merge repoints and prunes
the loser's edges, so an edge would not survive it.

## Consequences

- Merged losers now accumulate as deprecated rows. They are excluded from recall,
  relate and brain-health live counts like every deprecated claim, and the
  orphan vital counts live beliefs only.
- The merge no longer reports "could not be removed" tombstones. Every loser is
  a redirect by design.
- A merge stops paying the FTS delete and `DeleteCascade` for the loser, which
  also helps the write-lock problem in #466.

## Not done here

- **Unmerge.** The merge does not yet record which evidence links moved, so it
  cannot be reversed mechanically. A follow-up adds that record and an `unmerge`
  operation.
- **Chain compression.** Chains are resolved on read. Rewriting old redirects to
  point straight at the final winner (Wikidata's double-redirect bot) is left
  until chains are observed to grow.
- **The planner's cost.** Planning compares every pair of embedded beliefs with
  no budget. On a real 66k-belief brain a plain `consolidate` spent over an hour
  planning and merged nothing. That is a separate change.
- Other retirements (`forget`, `prune --narration`) are unchanged. How
  retraction, ending and archiving differ is the subject of the next ADR.

[Help:Merge]: https://www.wikidata.org/wiki/Help:Merge
[Help:Redirects]: https://www.wikidata.org/wiki/Help:Redirects
[RfC: redirect vs. deletion]: https://www.wikidata.org/wiki/Wikidata:Requests_for_comment/Redirect_vs._deletion
[Salesforce match/merge]: https://trailhead.salesforce.com/content/learn/modules/match-merge-and-survivorship/resolve-data-conflicts-with-merge-and-unmerge
[Reltio merge workflow]: https://digihub.mastechdigital.com/assets/Reltio/Reltio%20Asset%20Library/04_Match_Merge/RELTIO_Merge_Workflow_Automation.docx
[When owl:sameAs isn't the Same]: https://dspace.rpi.edu/items/9b1c4b6d-dacc-4c25-a040-da68c3d9b1c2
