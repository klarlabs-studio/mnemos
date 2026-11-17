# ADR 0035: A recorded-as-of read reconstructs what the brain believed

- **Status:** Accepted; step 1 implemented.
- **Date:** 2026-11-17
- **Deciders:** Felix Geelhaar
- **Builds on:** ADR 0034 (time axes; `claim_validity_history`)
- **Amends:** `query.AnswerOptions.RecordedAsOf`, `mnemos.Query.RecordedAsOf`,
  `query --recorded-at`, `/v1/search?recorded_as_of=`, gRPC `RecordedAsOf`;
  adds `recordedAsOf` to the `recall_at_time` MCP tool

## Context

`RecordedAsOf` is documented as the transaction-time axis: "returns the answer
as the store KNEW it at that instant … what did the agent believe at time T"
(`memory.go`). The implementation is one filter in admission
(`internal/query/admission.go`): drop claims whose `created_at` is after T.
Everything else in the answer is today's state:

- **Status is current.** A belief that was active on Tuesday and deprecated on
  Thursday is dropped from a Tuesday read by the deprecated filter, so the read
  answers "what we believed on Tuesday and still believe". A belief that became
  contested on Thursday reads as contested on Tuesday.
- **Valid time and the archive axis are current.** A belief superseded on
  Thursday carries Thursday's `valid_to`, so the default "valid now" check drops
  it from a Tuesday read. A belief archived by a later sweep likewise disappears.
- **Edges are current.** A contradiction detected on Thursday appears in the
  Tuesday answer, and hop expansion and priming walk edges that did not exist.
- **Events are current.** Events ingested after T compete for the top-ranked
  event slots and steer which claims are retrieved at all.
- **The read writes.** Retrieval write-backs (Hebbian strengthening,
  reconsolidation, inhibition) run on the historical answer, so asking what the
  brain believed last month reinforces those beliefs today.

The answer is wrong without an error, and it is the answer to the question
Mnemos is positioned on: what did the agent believe then, and what changed it.

The history needed to answer it is already recorded on every backend:
`claim_status_history` (every status transition, with time, actor, reason),
`claim_validity_history` (every change to `valid_to` and `archived_at`, ADR 0034
step 2), `relationships.created_at`, `events.ingested_at` and
`claims.created_at`. Restatements fold onto the existing claim
(`CollapseKnownClaims`) rather than rewriting it, so `created_at` is the time
the brain first recorded the belief.

## Decision

### 1. A recorded-as-of read sees the store as it was at T

When `RecordedAsOf` is set, the engine answers from a view of its repositories
bounded at T (`internal/query/recorded.go`), applied once per call in every
exported entry point:

| Read | Rule at T |
|---|---|
| events | only events with `ingested_at ≤ T` exist |
| claims | only claims with `created_at ≤ T` exist |
| claim status | the `to_status` of the last transition at or before T; if every transition is after T, the `from_status` of the first; no history, the stored value |
| `valid_to`, `archived_at` | the same rule over `claim_validity_history` |
| status history (narratives) | transitions after T are dropped |
| relationships | only edges with `created_at ≤ T` exist (contradictions, hops, priming) |

Admission then runs unchanged on the rewound claims, so the deprecated, validity
and archive filters ask their questions of the state the brain held at T.
The rewind rule is a pure function (`rewindClaim`), tested in isolation.

`AsOf` (valid time) keeps its meaning and composes: `RecordedAsOf=Tue, AsOf=Mon`
is "what did we believe on Tuesday about Monday".

### 2. A read of the past does not write the present

Retrieval write-backs are skipped when `AsOf` or `RecordedAsOf` is set. They
model the testing effect of using a memory now. An audit of what the brain
believed in the past is not that use, and must be repeatable: running it twice
returns the same answer.

### 3. What is not reconstructed, stated rather than approximated

- **Trust.** `trust_score` and recall credibility are current values. Trust at
  T needs the evidence count as of T (`claim_evidence` has no timestamp) and the
  credit as of T (not versioned). `MinTrust` and agent-mode contradiction
  resolution therefore use current trust on a recorded-as-of read. Step 3.
- **Merges.** Semantic dedupe repoints a loser's evidence and edges onto the
  winner and keeps the loser as a deprecated redirect (ADR 0033). The status of
  the loser rewinds correctly, but its evidence links now point at the winner,
  so a read before the merge can reach the winner (and drop it when it was
  recorded after T) instead of the loser. Repointed edges keep their original
  `created_at`. Step 2.
- **Text and confidence.** The pipeline never rewrites a stored claim's text;
  confidence changes are versioned only on SQLite and memory (`claim_versions`).
  Not rewound.
- **History older than its table.** Validity changes made before ADR 0034 step 2
  have no history row; the stored value is used.
- **Browse.** `GET /v1/beliefs?recorded_as_of=` (`page.ClaimFilter`) still
  filters on `created_at` only. It should use the same rewind. Step 2.

### 4. Cost

Two history reads per candidate claim, on the recorded-as-of path only. The
candidate set is bounded (top events, the widened claim candidates, hop
expansion), so this is tens to a few hundred indexed lookups. Every backend
already has `ListStatusHistoryByClaimID` and `ListValidityHistory`, so no port
changes. A batch method is warranted only if a measurement says so.

## Consequences

- `query --recorded-at`, `/v1/search?recorded_as_of=` and gRPC answer the
  question their documentation claims, on every backend. `recall_at_time` was
  valid-time only, so the agent surface could not ask what it believed at T at
  all; it gains an optional `recordedAsOf`.
- A historical read is repeatable and side-effect free.
- The incident timeline (deploy Monday, incident Tuesday, root cause Thursday)
  is an acceptance test against SQLite
  (`internal/query/recorded_as_of_test.go`): the
  Tuesday read returns the Tuesday diagnosis, not Thursday's, and does not show
  Thursday's contradiction, the retracted, archived and superseded beliefs
  read as they were on Tuesday, and repeating the read changes nothing.

## Rollout

1. Reconstruction of events, claims, status, validity, archive, edges and
   narratives; no write-backs on time-travel reads. **Implemented.**
2. Merge-aware rewind (follow `merged_into` redirects backward when a winner was
   recorded after T); browse uses the same rewind.
3. Trust at T: timestamp evidence links and version credit, then compute
   `trust.At` over the inputs as of T.
