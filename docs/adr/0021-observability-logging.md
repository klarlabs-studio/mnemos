# ADR 0021: Observability logging — structured logs for the important brain operations

- **Status:** Accepted; implemented (v0.99.0).
- **Date:** 2026-07-17
- **Deciders:** Felix Geelhaar
- **Scope:** Ops observability. Makes the brain's important operations emit structured
  logs so on-call can monitor them (Loki/Grafana) and alert on anomalies — the log
  complement to the Prometheus metrics (ADR 0020).

## Context

Metrics (ADR 0020) give scrapeable gauges; logs give discrete, queryable *events* with
detail — "a consolidation pass forgot 400 beliefs at 03:12", "brain health went
unhealthy: dangling edges = 3". Today the cognitive mechanisms are **silent**: the bolt
logger exists on the `memory` struct but is used only for embed warnings, and — the key
finding — **`serve` builds its `Memory` with a `nil` logger**, so nothing cognitive logs,
and a naive `m.logger.Info(...)` would *panic*. Only infra (HTTP access, gRPC) logs to
stderr. So there is no way to watch what the brain is doing, or to alert when it degrades.

## Decision

Wire one stderr JSON logger into the brain and instrument the **important, low-frequency
aggregate operations** — not per-item spam.

### Wiring

`newLibraryMemoryForDSN` (the CLI + `serve` construction path) now passes
`mnemos.WithLogger(bolt.New(bolt.NewJSONHandler(os.Stderr)).SetLevel(level))`. A new
**`MNEMOS_LOG_LEVEL`** (`trace|debug|info|warn|error`, default `info`) sets the minimum
level. A nil-safe `m.log()` accessor returns a package discard logger when unset, so
library consumers that never call `WithLogger` neither panic nor spam.

Wiring `WithLogger` also enables the axi-kernel's per-write `axi_event` audit line (Info)
— the write trail, previously discarded. That is legitimately important for a hosted
brain; operators who find it too chatty set `MNEMOS_LOG_LEVEL=warn` and still get the
degradation/anomaly logs below.

### What logs

- **Consolidation pass** (`info`, `mnemos: consolidation pass`): one line per pass with
  every `ConsolidateResult` count (credited/forgotten/refuted/validated/replayed/merged/
  associations-decayed/inhibition-decayed/synthesized) + plasticity gain. The "what did
  the sleep pass do" record.
- **Mass forgetting** (`warn`): when a pass prunes/refutes an unusually large number of
  beliefs — an anomaly worth a page.
- **Brain-health degradation** (`warn` degraded / `error` unhealthy): emitted by the
  metrics sampler each cycle the verdict is not healthy, with the failing signals — the
  primary alerting log, on the same cadence as the gauges.
- **Metrics-sample error** (`warn`): the sampler previously dropped its error into a
  counter; it now also logs it, so a broken monitoring pipeline is visible.
- **Promotion** (`info`): each tenant→global promotion run (how many lessons promoted /
  pending / rejected) — an important epistemic event.

Every line is structured JSON (bolt), so Loki parses the fields and alerts key off
`level` + `msg` (e.g. `msg="mnemos: brain health degraded"`).

## Consequences

- **Positive.** On-call can watch consolidation activity, get paged on health
  degradation / dangling-edge corruption / mass-forgetting, see a broken metrics pipeline,
  and audit promotions and writes — all from structured logs, filterable in Grafana/Loki.
  Complements the gauges: metrics for trends/alerts, logs for the event and its detail.
- **Cost / guardrails.** Enabling `WithLogger` turns on the per-write `axi_event` audit
  at info by default — more log volume for a busy brain; `MNEMOS_LOG_LEVEL=warn` trims it
  while keeping alerts. All new logs are aggregate/low-frequency, not per-belief. The
  logger is nil-safe (no panic, no spam) for library consumers.
- **Negative / scope limits (v1).** Instrumentation covers the memory-facade aggregate
  operations + the sampler + promotion; the ingest/query pipeline free-functions (which
  hold no logger) are not yet instrumented — volume is covered by the ADR-0020 gauges,
  and threading loggers there is a follow-up. Level is global, not per-subsystem.

## Rollout

Shipped in v0.99.0: the stderr logger wired via `WithLogger` with `MNEMOS_LOG_LEVEL`, the
`m.log()` nil-safe accessor, the consolidation + mass-forget + promotion logs, and the
brain-health-degradation + sample-error logs in the metrics sampler — with tests for the
level parsing and that a consolidation pass and a degraded health sample emit the
expected structured lines.
