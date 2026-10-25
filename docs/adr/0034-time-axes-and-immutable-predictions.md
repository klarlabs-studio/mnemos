# ADR 0034: Three ways a belief retires, and predictions that cannot be edited

- **Status:** Accepted; Rollout step 1 (archive axis) implemented, steps 2–3 pending.
- **Date:** 2026-10-25
- **Deciders:** Felix Geelhaar
- **Amends:** `ConsolidateOptions.ForgetBelowTrust` / `ForgetRefuted` (`memory.go`),
  `ClaimRepository.SetValidity`, the expectation loop (`expectations.go`,
  `claim_expectations`).
- **Issues:** #395, #446

## Context

Mnemos claims a bitemporal model: `valid_from`/`valid_to` is **valid time** (when
the belief held in the world) and `created_at` is **transaction time** (when the
brain learned it). `recall_at_time` and `query --at` filter on the first, and
`RecordedAsOf` on the second (`internal/query/admission.go`). Three things in the
code break that model.

**1. Forgetting writes valid time.** `ForgetBelowTrust` and `ForgetRefuted`
close `valid_to` (`memory.go`, the `ConsolidateOptions` doc). A belief forgotten
for low trust on 25 October therefore reads, under `--at 2026-10-26`, as a
statement that *the world changed* on 25 October. It did not; the brain stopped
surfacing it. `resolve --supersedes` and TTLs close `valid_to` for the right
reason, and those two cases cannot be told apart from forgetting afterwards.

**2. Valid time has no history.** `SetValidity` is a bare `UPDATE claims SET
valid_to = ?` on every backend. `claim_versions` snapshots text, confidence and
status but not validity, so a closed or reopened interval leaves no trace of what
it was, who changed it, or why. A bitemporal store that overwrites valid time
in place can no longer answer "what did we believe on date X about date Y", which
is the question the model exists for.

**3. Predictions are mutable.** `Expect` upserts `claim_expectations` keyed on
`claim_id`, replacing `predicted`, `tolerance`, `horizon` and `created_at`
(`internal/store/sqlite/expectation_repository.go`). `RecordObservation` overwrites
the previous observation, and the observation has no timestamp of its own. So:

- a prediction can be revised after the outcome is known, and the revision
  carries a fresh `created_at`, indistinguishable from a forecast made in
  advance;
- calling `Expect` again on a resolved claim resets `Resolved` and the
  observation, and the next reconcile credits the same beliefs twice;
- nothing records how confident the brain was, or what evidence it had, when it
  predicted, so calibration cannot be computed against the state that made the
  forecast;
- an expectation past its horizon with no observation stays open forever. It is
  never refuted, which is right, but it is also never counted, so a brain that
  only observes its successes looks perfectly calibrated.

## What the practice and literature say

- **Bitemporal databases keep both axes append-only.** Snodgrass (*Developing
  Time-Oriented Database Applications in SQL*, 1999) and SQL:2011 (ISO/IEC
  9075-2, `PERIOD FOR SYSTEM_TIME` and application-time periods) separate valid
  time, which a user may correct, from system time, which only moves forward.
  A correction to valid time is itself a new system-time version, never an
  overwrite.
- **Retraction is not expiry.** Datomic and XTDB distinguish a fact *retracted*
  (it was asserted in error) from a fact whose valid time *ended* (it was true
  and stopped being true). Both keep the history; they answer different
  questions under an as-of query.
- **Forecasts are frozen at the time they are made.** Forecasting tournaments
  (Tetlock & Gardner, *Superforecasting*, 2015; the Good Judgment Project's
  scoring) timestamp every forecast and score each one separately; an update is
  a new forecast, not an edit. Brier and log scores are only meaningful against
  what the forecaster knew at the time.
- **Unresolved outcomes are censored, not failures and not absent.** Survival
  analysis (Kaplan & Meier, *JASA* 1958) treats an observation that ended without
  the event as right-censored. Counting it as a failure biases toward pessimism;
  dropping it biases toward the cases that resolved, which is survivorship bias.

## Decision

### 1. Three ways a belief retires, each on its own axis

| Retirement | Meaning | Recorded as | Seen by `--at` before the date? | Seen by default recall? |
|---|---|---|---|---|
| **ended** | it was true and stopped being true | `valid_to` | yes | no |
| **retracted** | it was never true; we asserted it in error | `status = deprecated` with a reason | no (status has no bypass, as today) | no |
| **archived** | still believed true, no longer surfaced | new `archived_at` column | yes | no; `--include-history` shows it |

Only **ended** may close `valid_to`: `resolve --supersedes`, a TTL on
`remember`, and an explicit `valid_to` on `POST /v1/beliefs` / `WriteBeliefs`.
`ForgetBelowTrust` and `ForgetRefuted` set `archived_at` instead. `ForgetRefuted`
stays an archive rather than a retraction: the `refutes` edge already records the
verdict, and turning a belief into "never true" is a deliberate act (`forget`,
`memory_deprecate`), as the `ConsolidateOptions` doc already requires for status.

Archiving is undone by clearing `archived_at`. A belief that is recalled or
re-asserted while archived is un-archived by the same write that reinforces it,
in the same way `merged_into` redirects a restatement (ADR 0033).

### 2. Valid time is versioned

`SetValidity` appends a row to a new `claim_validity_changes` table (`claim_id`,
`old_valid_to`, `new_valid_to`, `changed_at`, `changed_by`, `reason`) in the same
governed write that updates the claim. `claims.valid_to` stays as the current
value for cheap filtering, and the table is the system-time history behind it.
The same applies to `archived_at`, with `reason` naming the sweep.

### 3. Predictions are append-only

`claim_expectations` is replaced by three append-only tables:

- **`predictions`**: `id`, `claim_id`, `predicted`, `tolerance`, `horizon`,
  `created_at`, `created_by`, plus two snapshots taken at write time:
  `confidence_at` (the claim's trust score) and `evidence_cutoff` (the newest
  evidence timestamp the claim had). A second `Expect` on the same claim inserts
  a new prediction and marks the open one `superseded`; it never edits it.
- **`prediction_observations`**: `prediction_id`, `observed`, `observed_at`,
  `observed_at_defaulted` (true when the caller gave no time and we used the
  write time), `created_at`, `source`. Several observations may arrive;
  reconciliation uses the first one at or before the horizon and records which.
- **`prediction_verdicts`**: `prediction_id` (unique), `verdict`
  (`confirmed`/`refuted`/`censored`), `surprise`, `observation_id`,
  `reconciled_at`. The unique key makes reconciliation idempotent, so a
  prediction is credited exactly once.

The verdict and surprise come from `Expectation.Confirmed` and
`Expectation.Surprise` (#395), so there is still exactly one definition of each.

### 4. Censoring

A prediction with no observation by `horizon + grace` (default 7 days) is
reconciled as `censored`. A censored prediction moves no trust and emits no
`validates`/`refutes` edge. `calibration` and `predictive_error` report the
censoring rate next to the score, and the score is computed over resolved
predictions only, with the censored count shown so a reader can see what the
score omits.

## Consequences

- `--at` answers the question it claims to: a belief forgotten for low trust is
  still valid at every date after it was archived, and only an ended belief
  drops out.
- Every change to valid time has a who, a when and a why.
- Calibration is computed against the confidence the brain had when it
  predicted, and a forecast can no longer be improved after the fact.
- Storage grows: one row per validity change and per prediction, observation and
  verdict. These are small, and the existing `claim_versions` growth is larger.
- Every backend needs the new column and tables, the projection-drift guard
  (`internal/store/schemaguard`) covers `archived_at`, and the Postgres RLS
  `scoped` list must include the three new tables.
- The public `Expect` / `RecordObservation` signatures stay; `RecordObservation`
  gains an optional observed-at variant. Code that read `claim_expectations`
  directly reads the new tables through the repository.

## Rollout

1. **Archive axis.** Add `archived_at`; move `ForgetBelowTrust`/`ForgetRefuted`
   onto it; admission excludes archived unless `IncludeHistory`. Backfill:
   claims whose `valid_to` was set by a sweep cannot be identified from the row
   alone, so the migration leaves existing `valid_to` values as they are and the
   change applies from the next consolidation.
2. **Validity history.** `claim_validity_changes`, written by `SetValidity` and
   the archive setter.
3. **Predictions.** The three tables, a one-time copy of `claim_expectations`
   (each row becomes a prediction, plus an observation if it had one, plus a
   verdict if resolved; `confidence_at` and `evidence_cutoff` are left empty
   and marked as unknown for migrated rows), then censoring.

Each step ships with a two-write test asserting what the store returns, per the
projection-drift guidance in `CLAUDE.md`.
