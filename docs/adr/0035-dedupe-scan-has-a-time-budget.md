# ADR 0035: The dedupe scan has a time budget

- **Status:** Accepted; implemented.
- **Date:** 2026-10-27
- **Deciders:** Felix Geelhaar
- **Amends:** the semantic-dedupe planner (`internal/pipeline/semantic_dedupe.go`,
  `semantic_dedupe_scan.go`) and `Consolidate`.
- **Issues:** #387, #466

## Context

A plain `consolidate` runs semantic dedupe first. Its similarity scan compares
claims pairwise, so its cost grows with the square of the brain. #491 removed
the write-lock holders and #498 made each comparison about six times cheaper,
but on a real 66k-claim brain a plain pass still spent over an hour planning and
merged nothing. The scan held no write lock, so other writers were no longer
starved, but the pass was effectively unbounded and a cancelled or timed-out run
threw away every pair it had found.

## Decision

The scan takes a wall-clock budget. When it runs out, the scan stops, keeps the
pairs found so far and reports that it was truncated.

- `PlanSemanticDedupeBudget(ctx, conn, threshold, budget)`; `PlanSemanticDedupe`
  is the same call with no budget, so `mnemos dedup` is unchanged.
- `SemanticDedupePlan` carries `Truncated`, `RowsScanned` and `RowsTotal`.
- `ConsolidateOptions.DedupeScanBudget` bounds the scan. Unset means 5 minutes;
  a negative value opts out. `ConsolidateResult.DedupeTruncated` and the
  `dedupe_truncated` field in the pass log and the CLI JSON report a partial scan.
- With a budget, rows are scanned in shuffled order so repeated truncated passes
  cover different claims instead of the same long early rows.

## Why a partial scan is safe

Clustering is complete-linkage: two clusters join only if every cross pair was
found above the threshold. A pair the scan never reached is therefore treated as
not similar and keeps its claims apart. A truncated plan can merge less than a
full one; it cannot merge anything a full scan would refuse. Cancellation of the
caller's context is still an error: stopping because asked to is not the scan
choosing to stop.

## Consequences

- A consolidate pass is bounded by default. On a large brain it makes progress
  every pass, because each applied merge retires a claim and shrinks the pool.
- It does not make the scan cheaper. Subquadratic candidate generation
  (an ANN index or blocking) is still the way to cover a very large brain in one
  pass, and stays open under #387.
- Coverage per pass is random rather than resumable. A cursor that remembers
  which rows were scanned would converge faster; deferred until measured.
