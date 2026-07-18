# ADR 0022: Observability bundle — shipped Grafana dashboard, alert rules, and scrape config

- **Status:** Accepted; implemented (v0.100.0).
- **Date:** 2026-07-18
- **Deciders:** Felix Geelhaar
- **Scope:** The "see it in Grafana" half of ops observability for the hosted brain
  (`mnemos serve`). ADR 0020 made the brain **emit** product/cognitive metrics; ADR 0021
  made it **emit** structured logs. This ADR ships the **consumer side**: a ready-to-import
  Grafana dashboard, Prometheus alert rules keyed to the real thresholds, and a scrape
  config — so on-call can actually collect and see them without reverse-engineering.

## Context

ADR 0020 ends with an explicit follow-up: *"Per-tenant labeling and a starter Grafana
dashboard are tracked as follow-ups."* Today an operator who runs `mnemos serve` gets a
Prometheus endpoint (`/internal/metrics`) with 19 `mnemos_*` product/cognitive gauges plus
3 HTTP RED metrics, and structured JSON logs on stderr — but **nothing to point them at**.
To get a dashboard they must:

- discover which `mnemos_*` series matter and what each one means,
- re-derive the alerting thresholds that already live in `BrainHealth` (ADR 0019) — e.g.
  that `free_energy >= 0.70` is "unhealthy", that any `dangling_associations > 0` is
  referential corruption, that `>= 25` stale expectations is critical,
- build every panel by hand.

That is exactly the work the project already did once (in the code) and should not ask
every operator to redo. Emitting a metric nobody can see is not observability.

## Decision

Ship a **canonical, importable observability bundle** in the repo at
`deploy/observability/`, usable by any existing Prometheus + Grafana:

- **`grafana-dashboard.json`** — a Grafana dashboard covering volume (beliefs / episodes /
  associations / contradictions / embeddings), trust (avg / low-trust / contested), the
  cognitive vitals (free-energy / calibration / dissonance / staleness), the single
  `mnemos_brain_health_status` SLI, the integrity pathologies, the sampler self-health,
  and the HTTP RED metrics. Product vocabulary throughout, matching the emitted names.
- **`alerts.yml`** — Prometheus alerting rules whose thresholds are **copied from the
  `BrainHealth` grader** (ADR 0019) so an alert firing means the same thing as the CLI
  `mnemos health` verdict and the `mnemos_brain_health_status` gauge. Grouped into
  availability, brain-health, cognitive-vitals, and API-SLO.
- **`prometheus.yml`** — a minimal scrape config for the `mnemos` job pointing at
  `/internal/metrics`, wiring in `alerts.yml`, and documenting the two auth postures.
- **`README.md`** — how to import each piece into an existing stack, the scrape **auth
  caveat**, and how to ship the ADR-0021 logs to Loki.

### Auth caveat (baked into the scrape config + README)

`/internal/metrics` is **authenticated by default** (v0.85.1+) and is deliberately **not**
covered by the `--public-reads` anonymous-GET bypass. So the shipped scrape config
documents the two supported postures:

- **Trusted internal network:** run `serve --metrics-public` and scrape anonymously —
  only safe when `/internal/metrics` is not reachable from outside the perimeter.
- **Authenticated (default):** Prometheus presents a bearer token via `authorization:
  credentials_file:`. Any validly-signed token passes (the metrics are a single
  process-wide registry, not tenant-partitioned); under `--require-tenant` mint it with a
  `--tenant` so it satisfies the bearer check. The token file is the operator's to create
  and keep at `0600` — the bundle ships a placeholder path, never a secret.

### Drift guard (a test, not a doc promise)

A test (`cmd/mnemos/observability_bundle_test.go`) parses the shipped `alerts.yml` and
`grafana-dashboard.json`, extracts every `mnemos_*` metric they reference, and asserts each
one is actually emitted by `mnemosRegistry`. A renamed or dropped metric breaks the build
instead of silently rotting the dashboard. It also validates that both files parse.

### Not the scaffold's copy

The `mnemos init --service` bundle (ADR: service scaffold) does **not** embed a second copy
of the dashboard JSON — that would be copy-paste that drifts. Its README instead gains an
**Observability** section that points at `deploy/observability/` and shows the scrape
snippet with the auth caveat inline. Single source of truth.

## Consequences

- **Positive.** On-call imports one JSON and one rules file and immediately sees the brain
  and gets paged on `mnemos_brain_health_status`, referential corruption, API error-rate,
  and a stalled sampler — with thresholds that match the code. Closes the ADR 0020
  follow-up.
- **Cost / guardrails.** The bundle is static config, no new runtime surface, no new env
  vars. The drift test keeps it honest against the emitted metric set.
- **Negative / scope limits.** The dashboard is single-instance / single-tenant aggregate
  (per-tenant labels remain deferred, per ADR 0020). It assumes a Prometheus datasource; a
  Loki logs panel is documented but not provisioned (no Loki is shipped). Alert routing
  (Alertmanager receivers) is the operator's — the bundle ships the rules, not the pager.

## Rollout

Shipped in v0.100.0: `deploy/observability/` (dashboard + `alerts.yml` + `prometheus.yml` +
`README.md`), the drift-guard test, and the scaffold README Observability pointer. No code
paths in `serve` change — this is the consumer side of ADR 0020 / ADR 0021.
