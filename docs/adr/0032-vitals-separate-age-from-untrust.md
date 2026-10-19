# ADR 0032: Trust vitals separate age from untrust

- **Status:** Accepted; implemented.
- **Date:** 2026-10-19
- **Deciders:** Felix Geelhaar
- **Amends:** ADR 0019 (brain health: `low_trust`, `staleness`, `trust_decay`),
  ADR 0026 (canonical trust, as read by those vitals).
- **Issue:** #481

## Context

Three vitals read belief trust, and two of them measured the same thing in a way
that graded a healthy brain unhealthy.

**`low_trust` counted age as untrust.** It compared `trust.At(now)` with a 0.30
floor. Trust is `confidence × corroboration × freshness + credit`, and freshness
bottoms out at `FreshnessFloor = 0.30`. So a single-evidence belief ends at
`confidence × 0.30`, below 0.30 for every confidence under 1.0. Every such belief
joined `low_trust` permanently once it aged (about 99 days for confidence 0.9 on the
default τ = 90), and never left. The floor meant to keep "Paris is the capital of
France" usable at any age instead marked it untrusted.

**`trust_decay` graded the steady state as critical.** It reported the share of
trusted beliefs that cross that floor within 30 days, against a fixed 0.20 critical
line. A brain fed at a steady rate always has the beliefs of one month in every
~108 days (τ·ln(1/0.30)) reaching the floor: about 28%. Any fixed share has this
problem, because the normal crossing rate depends on each belief's τ and on the
brain's intake, not on whether anything is wrong.

**`staleness` measured from the wrong instant.** It counted beliefs not created or
verified in 90 days. A record backfilled today with evidence from 2022 was "fresh",
and a record with a ten-year half-life was "stale" after 90 days.

pet-medical's prod brain read `trust_decay 0.44` (unhealthy) and `low_trust 0.55`
while doing exactly what it should.

## Decision

Each vital answers one question, and age is answered once.

### `low_trust`: untrusted on its merits

A belief is low-trust when its **age-neutral trust** is below 0.30: `trust.At`
evaluated at the belief's own reference instant (latest evidence or last
confirmation), where freshness is 1. Only confidence, corroboration and credit
remain. A belief with confidence 0.2, or one pushed down by contradiction credit,
counts. A confidence-0.9 belief that is merely old does not.

The vital still reads the canonical function (ADR 0026), not the stored cache. The
cache bakes in freshness as of `trust_computed_at`.

### `staleness`: aged onto the floor without re-verification

A dated belief is stale once its freshness reaches the floor, measured from its own
reference (latest evidence, confirmation or verification) against its own τ:
`age ≥ τ·ln(1/FreshnessFloor)`. That is about 108 days on the default τ (close to
the old 90-day horizon), 17 days for a volatile τ = 14 belief, and 12 years for a
dated record with τ = 3650. A belief with no dated evidence keeps the old rule:
not created or verified within 90 days.

### `trust_decay`: observed against steady-state crossings

For each belief that is not low-trust and has not yet reached the floor:

- it **crosses** if it reaches the floor within the 30-day horizon;
- its **expected** crossing is `min(1, 30 / (τ·ln(1/FreshnessFloor)))`, the chance a
  belief of its τ crosses within the horizon when beliefs arrive at a steady rate.

`trust_decay = crossings / Σ expected`. A steadily fed brain reads ~1.0 whatever its
mix of half-lives. It reads higher when a backlog ages out together, or when intake
stopped and every remaining belief is drifting toward the floor. It is graded warn
at 1.5 and critical at 3.0.

It is **unknown** when fewer than 1.0 crossings are expected. One crossing against
0.2 expected reads 5×, and a handful of beliefs is not a rate. It is also unknown
when nothing is on its way to the floor (an empty brain, or one entirely aged out,
which `staleness` reports).

The sampled health path reports the ratio's margin as the crossing share's margin
scaled by the same divisor.

## Consequences

- A steadily fed brain of single-evidence beliefs no longer reads unhealthy by
  construction. Its `trust_decay` sits near 1.0 and its `low_trust` reflects
  confidence and credit only.
- `trust_decay`'s value is no longer in [0, 1]. Consumers that plotted it as a share
  now see a ratio, and the help text and health detail say so.
- `staleness` moves for brains whose beliefs carry non-default half-lives or
  backfilled evidence dates. Volatile beliefs go stale sooner, long-lived records
  later, and a backfill no longer looks fresh.
- An aged-out brain is reported by `staleness`, not `low_trust`.
- Unchanged: the trust model itself (`trust/v2`), the stored score, and every
  threshold not named here.

## Alternatives considered

- **Lower the low-trust floor below `min confidence × FreshnessFloor`.** This
  weakens the check for every brain, and still conflates age with untrust for
  low-confidence beliefs.
- **Make the freshness floor scale with confidence.** This changes `trust.At` and
  every stored score to fix a reading of it.
- **Keep a fixed `trust_decay` share and raise its threshold.** The normal rate
  depends on τ and intake. No single number is right for both a volatile brain and
  a records brain.
