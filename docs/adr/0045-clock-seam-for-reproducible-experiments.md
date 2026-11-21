# ADR 0045: A clock seam for reproducible experiments

- **Status:** Accepted; implemented.
- **Date:** 2026-11-21
- **Deciders:** Felix Geelhaar
- **Fixes:** issue #396 (brainbench is not deterministic)
- **Adds:** `internal/clock`; brainbench runs every scenario on a simulated clock

## Context

`make brain-eval` is documented as offline and deterministic, so that a delta
between two commits is exact. It was not. Three runs at one commit gave
noisy_capture `answer_mrr` of 0.55, 0.383 and 0.383, and `mean_trust` differed
in the 8th decimal. The paired design hid this inside one run, because every
arm is a copy of one seed, but any before/after comparison was confounded.

The cause was the wall clock. Trust is `f(confidence, evidence, freshness)`,
and freshness is evaluated at `time.Now()`. Two claims extracted from one
document have identical inputs, so identical trust in principle, but they were
rescored microseconds apart. The decay over those microseconds made one score
larger in the 12th decimal, ranking ordered them by it, and which one came
first changed from run to run. Rounding would hide that without removing it,
and a tie-break key cannot fix it, because the scores were not equal.

The library read the wall clock in about 150 places: row stamps in every store
backend, trust and staleness evaluation, the as-of "now" of recall.

## Decision

One package, `internal/clock`, is the source of every instant mnemos reads as
data. `clock.Now()` returns `time.Now()` unless an experiment installs a clock
with `clock.Install`, which holds a process-wide lock until its restore runs,
so two experiments can never share one clock. `clock.Simulated` starts at a
fixed instant and advances a fixed tick per read. Reads are strictly
increasing, as wall-clock writes are, and the sequence of instants depends only
on the sequence of reads.

The library packages and every store backend call `clock.Now()` where they
stamp or evaluate time. Durations and deadlines keep the real clock
(`time.Since`, `time.Until`, and the semantic-dedupe scan budget): they measure
the machine, and a simulated clock must not starve a real timeout.

brainbench installs a fresh simulated clock (2026-01-01T00:00:00Z, 1µs per
read) for each scenario, in both the paired A/B mode and the ablation mode.
Timeline phases are clock advances instead of sleeps. The seeder also derives
claim and relationship ids from the scenario, document and position, as it
already did for event ids, because equally scored candidates keep an order that
follows claim ids.

## Alternatives rejected

- **A clock parameter on every constructor (dependency injection).** This is
  the cleaner shape, but it touches every repository constructor, the library
  options and every call site, for a value production never changes. The seam
  can become an injected port later without changing what reads it.
- **`testing/synctest`.** It fakes time only inside a test bubble, so it cannot
  serve `go run ./tools/brainbench`, the entry point people actually use.
- **Back-dating rows in the harness.** This would measure a reimplementation of
  the history tables rather than the history the product records. That was the
  reason the timeline track gave for running on real time, and the seam meets
  it: the shipped write path does the stamping, only its clock source changes.
- **Rounding metrics.** This hides the drift and does not remove it.

## Consequences

- Two brainbench runs at one commit produce the same report apart from
  `generated_at`, `duration_ms` and the cost report's latencies. The
  ownership_timeline scenario no longer sleeps through its 2s forward
  reservations. `TestRun_IsReproducibleAcrossRuns` and
  `TestRunAblation_IsReproducibleAcrossRuns` pin both modes.
- `clock.Install` is process-global. Anything that installs a clock must not
  run concurrently with code that expects the wall clock in the same process.
  Production never installs one.
- New code that stamps or scores time must call `clock.Now()`. A stray
  `time.Now()` on the brainbench path fails the cross-run tests. Elsewhere,
  nothing enforces this yet.
- `cmd/` and the server layers still call `time.Now()` directly. They are not
  on the experiment path.
