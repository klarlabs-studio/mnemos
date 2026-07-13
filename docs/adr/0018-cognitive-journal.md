# ADR 0018: The cognitive journal — instrumentation for studying learning

- **Status:** Accepted; implemented (v0.96.0).
- **Date:** 2026-07-13
- **Deciders:** Felix Geelhaar
- **Scope:** Research/observability infrastructure. Makes the brain's *learning*
  observable over time, which nothing before this did.

## Context

Mnemos persists a strong **raw substrate** — immutable timestamped `events`,
`claim_status_history`, `claim_versions`, and the decision→outcome→verdict-edge chain —
so the *inputs* to learning are durable and the pure `credit`/`consolidate` math can be
re-run offline. But the **derived cognitive state** the mechanisms produce is not
recorded: trust scores are overwritten in place (the credit path deliberately bypasses
versioning), salience / inhibition / edge-strength live in overwritten columns,
per-consolidation-pass effects (`PlasticityGain`, credit volume, replay/forget/decay
counts) are computed and then discarded, and free-energy / prediction-error / ECE are
on-demand only, never snapshotted. The mechanisms are also silent — no structured log
records what they decided.

The consequence: you cannot answer the questions research needs — *did metaplasticity
dampen churn? is free-energy trending down? which beliefs did inhibition suppress, and
did they recover? what should these hardcoded constants actually be?* Everything needed
to answer them exists in memory at mutation time; it is simply never written down.

This is also the honest gate on ADR 0017's north star: **you cannot validate active
free-energy minimization without first being able to plot free-energy.** So the journal
is not a detour from the generative-model research — it is step zero of it.

## Decision

Add an append-only **cognitive journal**: a durable, timestamped record of what the
brain's learning mechanisms did, so their trajectories can be studied and their
constants tuned against real data.

### Storage — one generic append-only table

`cognitive_journal(id, at, run_id, kind, subject_id, data)` — a single table across
every backend, with `data` a JSON payload (the same JSON-column pattern
`compilation_jobs.scope_json` / `outcomes.metrics_json` already use). One table, not a
wide schema per journal kind, keeps the cross-backend parity surface minimal and makes
new journal kinds a code-only change. `kind` discriminates; `subject_id` is the claim id
for per-belief entries (empty for pass-level). Append-only — never updated, never
deleted on the write path — so it is a true event log. On Postgres it joins the ADR-0007
tenant-scoped set (per-tenant isolation), since each tenant's brain journals separately.

### Two kinds in v1

- **`consolidation`** (one per journaled pass): the full `ConsolidateResult` — every
  counter, including the ones the CLI never printed (`PlasticityGain`,
  `AssociationsDecayed`, `InhibitionDecayed`, `ReplayProtected`, `Merged`) — plus a
  `PredictiveError` snapshot (total, hotspot, and each level). This yields the
  **free-energy-over-time curve** and per-pass effect sizes.
- **`belief_trust`** (one per belief credit assignment moved this pass): `before`,
  `after`, `delta`. This yields **per-belief trust trajectories** — the signal that lets
  you check, e.g., whether a crystallized belief churned less than a fresh one.

### Write path — opt-in, off the default hot path

`consolidate --journal`. When set, `Consolidate` records one `consolidation` entry (with
a fresh `PredictiveError` snapshot) and, if credit ran, the `belief_trust` deltas credit
applied. Journaling is a **side record** — it never changes what consolidation computes,
and with the flag off the write path is byte-for-byte unchanged. Journaling *does* make a
journaled pass non-idempotent (each pass appends), which is the entire point of a log.
Best-effort at the seam: a journal-write failure is surfaced (it is the operator's
research data), but journaling is only reached under the explicit flag.

### Read / export surface

`mnemos journal` — recent `consolidation` entries (the free-energy curve + effect sizes),
`mnemos journal --belief <id>` for a belief's trust trajectory, JSON by default (for
research tooling) with `--human` for a readable table. A `ports.JournalRepository`
(`Append` / `List` / `ListBySubject`) attached to `store.Conn`, nil on backends without
it — the same optional-capability pattern the rest of the store uses.

## Consequences

- **Positive.** Converts mnemos from "a brain that learns" into "a brain whose learning
  you can *study*": trust / free-energy / effect-size trajectories become queryable and
  exportable. Directly unblocks tuning the hardcoded constants (metaplasticity floor,
  neuromodulation sensitivity, decay retains, inhibition bounds) against real data, and
  is the prerequisite for empirically validating any active-minimization work.
- **Cost / guardrails.** One new table across all backends (the store-parity discipline
  applies: all backends, Postgres RLS registration, live pg+mysql tests). Append-only and
  read-nothing on the hot path, so low-risk; opt-in so the default is untouched. A
  journaled pass is intentionally non-idempotent.
- **Negative / scope limits (v1).** It records the credit-driven trust deltas and the
  per-pass aggregate + free-energy snapshot — not *every* trust change (evidence-based
  `RecomputeTrust` touches many claims each pass; journaling all of them is deferred),
  and not a per-mutation structured log stream (the journal is the queryable substitute).
  Retention/rotation is left to the operator (append-only; prune manually) — a bounded
  auto-rotation is future work.

## Rollout

Shipped in v0.96.0: the `cognitive_journal` table (sqlite migration, Postgres +
tenant-RLS, mysql inline, memory, libSQL-via-sqlite), `ports.JournalRepository`,
`consolidate --journal` writing per-pass + belief-trust entries with a `PredictiveError`
snapshot, and `mnemos journal` (+`--belief`, `--human`) — with store round-trip tests on
live Postgres + MySQL and a consolidation-journaling integration test.
