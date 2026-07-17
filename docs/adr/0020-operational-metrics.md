# ADR 0020: Operational metrics — product + cognitive signals for Prometheus/Grafana

- **Status:** Accepted; implemented (v0.98.0).
- **Date:** 2026-07-17
- **Deciders:** Felix Geelhaar
- **Scope:** Ops observability for the hosted brain (`mnemos serve`). Makes the brain's
  product and cognitive state **scrapeable as a Prometheus time series** so on-call can
  collect it and see it in Grafana.

## Context

The journal (ADR 0018) and brain-health (ADR 0019) surfaces are DB-queryable diagnostics
— great for research, useless to an on-call dashboard that pulls from Prometheus. The
only Prometheus surface today (`/internal/metrics`) exposes exactly **three HTTP RED
metrics** (`mnemos_http_requests_total`, `_request_duration_seconds`, `_in_flight_requests`)
and **nothing product or cognitive**. So an operator can see request rate/latency but not
how many beliefs the brain holds, whether trust is collapsing, whether free-energy is
climbing, or whether the brain is healthy — none of it reaches Grafana.

## Decision

Export **product + cognitive metrics as Prometheus gauges** on the existing
`/internal/metrics` surface of `mnemos serve`, populated by a **background sampler** so
the (potentially expensive) full-scan computations are decoupled from scrape frequency.

### The sampler

A goroutine started by `serve` (tied to the server lifecycle; stopped on shutdown) that
every `MNEMOS_METRICS_SAMPLE_INTERVAL` (Go duration, default `60s`; `0` disables) computes
the metrics from the base `store.Conn` + `Memory` and sets the gauges. Cheap counts and
the full-scan `BrainHealth` run on the same tick; raise the interval for a large brain.
Failures increment an error counter and are logged — a sampling error never crashes
`serve` and leaves the last-good gauge values in place. A scrape just reads the
last-sampled gauges (O(1)), so Prometheus can scrape at 15s without triggering a scan.

### Metrics (gauges, product vocabulary)

- **Volume:** `mnemos_beliefs_total`, `mnemos_episodes_total`, `mnemos_associations_total`,
  `mnemos_contradictions_total`, `mnemos_embeddings_total`.
- **Trust:** `mnemos_belief_trust_avg`, `mnemos_low_trust_beliefs`,
  `mnemos_contested_beliefs`.
- **Cognitive (from `BrainHealth`, ADR 0019):** `mnemos_free_energy`,
  `mnemos_calibration_ece`, `mnemos_dissonance_rate`, `mnemos_staleness_rate`,
  `mnemos_brain_health_status` (0 healthy / 1 degraded / 2 unhealthy — alertable).
- **Integrity (pathologies):** `mnemos_orphan_beliefs`, `mnemos_dangling_associations`,
  `mnemos_stale_expectations`.
- **Sampler self-health:** `mnemos_metrics_sample_timestamp_seconds` (last successful
  sample — alert on staleness), `mnemos_metrics_sample_duration_seconds`,
  `mnemos_metrics_sample_errors_total`.

All live on the private `mnemosRegistry`, exposed only at `/internal/metrics`.

### Scraping / auth

`/internal/metrics` stays **authenticated by default** (v0.85.1+). Prometheus scrapes it
with a bearer token (`authorization` in the scrape config), or `serve --metrics-public`
exposes it unauthenticated on a trusted internal network. No auth change here.

### Logs

Structured logs are already JSON on stderr (HTTP access, gRPC, kernel evidence) —
collectable by Promtail/Loki as-is; this ADR does not change logging. The gap was
*metrics*, which this fills.

## Consequences

- **Positive.** On-call gets a real Grafana dashboard: volume, trust, free-energy,
  calibration, dissonance, brain-health status, and integrity pathologies — plus
  `mnemos_brain_health_status` as a single alertable SLI. Reuses `BrainHealth`, so the
  dashboard and the CLI verdict agree.
- **Cost / guardrails.** The sampler runs one full-scan (`BrainHealth`) per interval;
  default 60s, tunable, `0` disables. Read-only. A sampling failure is isolated (error
  counter + log), never fatal. The scrape path stays O(1).
- **Negative / scope limits (v1).** Metrics are sampled over the **base connection**
  (the whole store when single-tenant; the default namespace under `--require-tenant`) —
  **per-tenant metric labels are deferred**. The vital set matches ADR 0019's; more
  series (per-run, per-kind) are future.

## Rollout

Shipped in v0.98.0: the product/cognitive gauges, the lifecycle-bound background sampler
(`MNEMOS_METRICS_SAMPLE_INTERVAL`), wired into `serve`, with tests that a sample sets the
gauges and that `/internal/metrics` exposes them. Per-tenant labeling and a starter
Grafana dashboard are tracked as follow-ups.
