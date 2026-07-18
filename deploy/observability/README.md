# mnemos observability bundle

Ready-to-import Prometheus + Grafana assets for a `mnemos serve` deployment
(ADR 0022). `mnemos serve` already **emits** everything (metrics via ADR 0020,
structured logs via ADR 0021); this bundle is the **consumer side** so on-call
can collect and see it without building dashboards from scratch.

| File | What it is | Where it goes |
|------|------------|---------------|
| `prometheus.yml` | Scrape config for the `mnemos` job + `rule_files` | Your Prometheus (or merge the `mnemos` job into an existing config) |
| `alerts.yml` | Alerting rules, thresholds copied from `BrainHealth` (ADR 0019) | Alongside `prometheus.yml`, referenced by `rule_files` |
| `grafana-dashboard.json` | The "mnemos — brain" dashboard | Grafana → Dashboards → Import |

## 1. Scrape the metrics

`mnemos serve` exposes Prometheus metrics at **`/internal/metrics`**. That
endpoint is **authenticated by default** (v0.85.1+) and is deliberately **not**
covered by `--public-reads`. Two supported postures (both documented inline in
`prometheus.yml`):

- **Authenticated (default).** Prometheus presents a bearer token. Mint one and
  save it to a `0600` file Prometheus can read:

  ```sh
  # single-tenant (Mode C)
  mnemos token issue --user prometheus > /etc/prometheus/mnemos-token
  # under --require-tenant (Mode D) — metrics aren't tenant-partitioned, but the
  # bearer check still needs a tenant-scoped token:
  mnemos token issue --user prometheus --tenant <tenant-id> > /etc/prometheus/mnemos-token
  ```

  The scrape config points at that file via `authorization: credentials_file:`.

- **Trusted internal network.** Run `mnemos serve --metrics-public` where
  `/internal/metrics` is unreachable from outside the perimeter, and delete the
  `authorization:` block from the scrape config. No token needed.

Point the `mnemos` job's `static_configs.targets` at your instance
(`host:8080`), then reload Prometheus.

## 2. Load the alert rules

`alerts.yml` is referenced by `rule_files:` in `prometheus.yml`. It ships four
groups:

- **`mnemos-availability`** — `MnemosDown`, `MnemosMetricsSampleStale`,
  `MnemosMetricsSampleErrors` (is the brain up and is the sampler feeding fresh
  gauges?).
- **`mnemos-brain-health`** — `MnemosBrainHealthUnhealthy` /
  `…Degraded` (the primary SLI, `mnemos_brain_health_status`), plus
  `MnemosDanglingAssociations` (referential corruption → critical),
  `MnemosOrphanBeliefs`, `MnemosStaleExpectationsHigh`.
- **`mnemos-cognitive-vitals`** — free-energy / calibration / dissonance /
  staleness crossing their ADR-0019 critical thresholds; these annotate *why*
  the brain-health status alert fired.
- **`mnemos-api-slo`** — HTTP 5xx rate and p99 latency (RED).

Thresholds are copied from the `BrainHealth` grader, so a firing alert means the
same thing as the `mnemos health` CLI verdict. Wire your Alertmanager receivers
separately — the bundle ships the rules, not the pager.

## 3. Import the dashboard

Grafana → **Dashboards → Import → Upload JSON** → `grafana-dashboard.json`, then
pick your Prometheus datasource when prompted. Rows: Overview (health status +
volume), Trust, Cognitive vitals (with the ADR-0019 threshold lines), Integrity
pathologies, HTTP RED, and sampler self-health.

## 4. Ship the logs to Loki (optional)

The ADR-0021 structured logs are JSON on **stderr** — no config needed to
collect them. Point Promtail / Grafana Alloy / the Loki Docker driver at the
container's stdout/stderr and they land in Loki as-is. Useful queries once
they're flowing (the log messages are stable strings):

```logql
{service="mnemos"} | json | msg="mnemos: brain health degraded"
{service="mnemos"} | json | msg="mnemos: consolidation pass"
{service="mnemos"} | json | level="error"
```

`mnemos: brain health degraded` is the log complement to the
`mnemos_brain_health_status` metric — the metric pages you, the log names the
failing vital. This bundle does not ship a Loki instance; add one to your stack
if you want the logs in Grafana next to these panels.
