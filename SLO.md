# Mnemos service-level objectives

This file is the canonical declaration of what "healthy" means for a
running Mnemos server (`mnemos serve`). Each SLO below is enforceable
from the metrics exposed at `/internal/metrics` (Prometheus text
format) and observable via the `mnemos_http_*` series.

These SLOs apply to the HTTP API. The MCP stdio surface is a per-agent
session and not subject to the same availability targets — its
correctness is asserted through the eval suite, not service metrics.

## Read endpoints

Routes that don't mutate state. Includes `/health`, `/v1/events`
(GET), `/v1/claims` (GET), `/v1/relationships` (GET),
`/v1/embeddings` (GET), `/v1/metrics`, `/v1/context`, `/v1/search`,
`/v1/incidents` (GET), `/v1/claims/<id>/...`, `/`, `/app`.

| Objective                          | Target                          | Rationale                                                |
| ---------------------------------- | ------------------------------- | -------------------------------------------------------- |
| Availability (30-day rolling)      | **99.9%** (≤ 43m downtime)      | Single-tenant local-first; cluster pairing is operator-led. |
| p99 latency                        | **≤ 250 ms**                    | Cosine reranking dominates; LLM grounding is a separate path. |
| p50 latency                        | **≤ 50 ms**                     | Hot-path queries should feel instant inside an IDE.      |
| Error rate (5xx / total)           | **≤ 0.1%**                      | A single failing read is recoverable; sustained failure is not. |

PromQL examples (replace `$range` with `30d` or `1h`):

```promql
# 30-day availability
1 - (
  sum(rate(mnemos_http_requests_total{status=~"5.."}[$range]))
  / sum(rate(mnemos_http_requests_total[$range]))
)

# p99 read latency
histogram_quantile(0.99,
  sum by (le) (rate(mnemos_http_request_duration_seconds_bucket{method="GET"}[5m]))
)
```

## Write endpoints

Routes that mutate state. Includes `/v1/events` (POST), `/v1/claims`
(POST), `/v1/relationships` (POST), `/v1/embeddings` (POST),
`/v1/incidents` (POST). Excludes `/v1/leads` (public, rate-limited).

| Objective                | Target                  | Rationale                                                              |
| ------------------------ | ----------------------- | ---------------------------------------------------------------------- |
| Availability             | **99.9%**               | Same target as reads; writes are the load-bearing surface for agents.  |
| p99 latency              | **≤ 500 ms**            | Trust recompute + status-history transition adds overhead to upserts.  |
| Error rate (5xx / total) | **≤ 0.5%**              | Higher tolerance than reads — write-side validation legitimately fails. |

## Public lead form

`POST /v1/leads`. Public, rate-limited, no auth.

| Objective                | Target                  | Rationale                                                              |
| ------------------------ | ----------------------- | ---------------------------------------------------------------------- |
| Sustained throughput     | **5 req/min per IP**    | Token-bucket cap (cmd/mnemos/serve_ratelimit.go).                      |
| Burst                    | **10 req per IP**       | Same source.                                                           |

Throttled responses (HTTP 429) are not counted as errors against the
read/write SLOs — they are the system working as designed.

## Error budget

The 99.9% availability target leaves a 30-day budget of **~43 minutes**
of downtime or 4-nines of failed requests. When the budget burns:

  - **Below 25% remaining**: pause non-critical feature work; focus
    the next deploy cycle on root-cause fixes and resilience.
  - **Below 5% remaining**: freeze deploys to the affected surface;
    only roll forward security or correctness patches.
  - **Exhausted**: run a postmortem; document the incident under
    `docs/incidents/`; revisit the SLO target if the workload changed
    materially.

## Updating this file

This document is policy, not just observation. Updates require a
human reviewer who can vouch for the change against the
incident-response budget — bumping a target up is cheap, bumping it
down silently is not.
