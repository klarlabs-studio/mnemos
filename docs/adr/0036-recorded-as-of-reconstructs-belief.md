# ADR 0036: A recorded-as-of read reconstructs what the brain believed

- **Status:** Accepted; steps 1, 2 and 3 implemented.
- **Date:** 2026-11-17
- **Deciders:** Felix Geelhaar
- **Builds on:** ADR 0034 (time axes; `claim_validity_history`)
- **Amends:** `query.AnswerOptions.RecordedAsOf`, `mnemos.Query.RecordedAsOf`,
  `query --recorded-at`, `/v1/search?recorded_as_of=`, gRPC `RecordedAsOf`;
  adds `recordedAsOf` to the `recall_at_time` MCP tool. Step 2 amends
  `GET /v1/beliefs?recorded_as_of=`, gRPC `ListBeliefs.recorded_as_of` and
  `ports.ClaimRepository` (`ListMergedInto`). Step 3 amends
  `domain.ClaimEvidence` (`CreatedAt`), the `claim_evidence` table on every
  backend, and adds `claim_credit_history` and `ports.CreditHistoryLister`

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
bounded at T (`internal/recorded`, wired in `internal/query/recorded.go`),
applied once per call in every exported entry point:

| Read | Rule at T |
|---|---|
| events | only events with `ingested_at ≤ T` exist |
| claims | only claims with `created_at ≤ T` exist |
| claim status | the `to_status` of the last transition at or before T; if every transition is after T, the `from_status` of the first; no history, the stored value |
| `valid_to`, `archived_at` | the same rule over `claim_validity_history` |
| status history (narratives) | transitions after T are dropped |
| `merged_into` | cleared when the merge transition (`domain.MergeRedirectReason`) is after T |
| relationships | only edges with `created_at ≤ T` exist (contradictions, hops, priming); endpoints a later merge moved are moved back (§5) |

Admission then runs unchanged on the rewound claims, so the deprecated, validity
and archive filters ask their questions of the state the brain held at T.
The rewind rule is a pure function (`recorded.Rewind`), tested in isolation.

`AsOf` (valid time) keeps its meaning and composes: `RecordedAsOf=Tue, AsOf=Mon`
is "what did we believe on Tuesday about Monday".

### 2. A read of the past does not write the present

Retrieval write-backs are skipped when `AsOf` or `RecordedAsOf` is set. They
model the testing effect of using a memory now. An audit of what the brain
believed in the past is not that use, and must be repeatable: running it twice
returns the same answer.

### 3. What is not reconstructed, stated rather than approximated

- **Trust.** Reconstructed by step 3 (§7), with two inputs held at their
  stored value: confidence and the per-claim half-life. A belief confirmed both
  before and after T reads as unconfirmed at T, since `last_confirmed` keeps
  only the latest confirmation.
- **Merges.** Reconstructed from the redirect, with the approximations stated
  in §5. Step 2.
- **Text and confidence.** The pipeline never rewrites a stored claim's text;
  confidence changes are versioned only on SQLite and memory (`claim_versions`).
  Not rewound.
- **History older than its table.** Validity changes made before ADR 0034 step 2
  have no history row; the stored value is used.
- **Browse.** Uses the same views (§6). Step 2.

### 4. Cost

Two history reads per candidate claim, on the recorded-as-of path only. The
candidate set is bounded (top events, the widened claim candidates, hop
expansion), so this is tens to a few hundred indexed lookups. Every backend
already has `ListStatusHistoryByClaimID` and `ListValidityHistory`. A batch
method is warranted only if a measurement says so. The browse lists the whole
store, so it reads histories in bulk instead (`ListAllStatusHistory`,
`ListValidityHistory("")`): three reads per page request, not two per claim.
The merge walk (§5) adds one indexed `ListMergedInto` per redirect level, which
on a store with no merges is a single lookup that finds nothing.

### 5. Merges are walked backwards from the redirect (step 2)

Semantic dedupe (ADR 0033) folds a loser into a winner in place:
`RepointEvidence` moves the loser's evidence links onto the winner,
`RepointEndpoint` moves its edges (same id, same `created_at`), and the loser
stays as a deprecated redirect with `merged_into` set and a status transition
whose reason is `domain.MergeRedirectReason`. A read at T before the merge
reaches the winner through the moved evidence; a winner recorded after T is
dropped, and the loser the brain held at T is unreachable.

Two designs were considered:

- **Record repoint history.** Version every moved evidence link and edge (a
  new history table on four backends, with RLS registration, migrations and
  cascade handling) so the view can put each one back exactly. Precise, but a
  large schema change, and it reconstructs nothing for merges that happened
  before it shipped. Edges a merge *deleted* (a loser–winner self-loop, a
  duplicate of an edge the winner already had) would also need their full row
  recorded.
- **Walk the redirect backwards (chosen).** Everything needed is already
  stored: `merged_into` says who absorbed whom, and the redirect's status
  transition dates the merge. One read port method is added,
  `ClaimRepository.ListMergedInto(targetIDs)` (claims whose `merged_into` is in
  the set; indexed `idx_claims_merged_into` on every backend; memory scans). It
  works for every merge ever recorded, adds no write-path cost and no table.

The rules, in `internal/recorded`:

- **Claims reached through events.** `ListByEventIDs` returns, besides the
  claims holding the events' evidence now, every claim merged into them
  (transitively) that was still held separately at T: recorded by T, merge
  transition after T. A claim already a redirect at T is not added; its
  evidence was on its target then too.
- **A rewound claim** has its `merged_into` cleared when the merge is after T,
  and its status rewinds from the same history, so the loser reads as the
  active belief it was.
- **Evidence.** A claim merged after T is attributed the evidence of the claim
  at the end of its redirect chain, and links to events ingested after T are
  dropped.
- **Edges.** An endpoint recorded *after* the edge was created cannot be the
  edge's original endpoint, so the edge arrived by a merge. The view moves it to
  the oldest claim merged into that endpoint that existed when the edge was
  created, followed forward through any merge that had already happened by T.

Stated approximations, all confined to claims a merge judged near-duplicates
(cosine above threshold, same type, scope and polarity, no recorded
contradiction):

- Evidence links were moved, not versioned, so a claim held separately at T is
  reached through all of its absorber's evidence, not only its own, and is
  attributed all of it.
- An edge whose current endpoint already existed when the edge was created
  stays on that endpoint: it may have been the loser's, and nothing recorded
  says so. Both claims are in a read at T that reaches either, so the edge still
  surfaces, attached to the wrong one of two near-duplicates. When several
  losers could have held a moved edge, the oldest is chosen.
- Edges a merge deleted are not reconstructed.
- A redirect with no merge transition in its history (none is known to exist)
  keeps its stored pointer, like any field without history.

### 6. Every surface reads the past through the same views (step 2)

`internal/recorded` holds the rewind (`Rewind`) and the bounded views
(`Events`, `Claims`, `Relationships`). It imports only `domain` and `ports`, so
every caller can use it without a cycle, and none re-implements it:

- recall: `query.Engine.recordedAt` swaps its repositories for the views;
- the belief browse: `browse.Recorded(conn, t)` returns the event and claim
  views, `browse.BeliefsInGo` lists, filters and run-scopes through them, and
  `browse.Beliefs` always pages a recorded-as-of browse in Go (the SQLite pager
  already refused time bounds; the Postgres pager now refuses
  `recorded_as_of`, since SQL over current rows cannot rewind);
- `GET /v1/beliefs` reads the page *and* its evidence through
  `browse.Recorded`;
- gRPC `ListBeliefs` reads beliefs, run scope and evidence through
  `browse.Recorded` and filters with `page.ClaimFilter.Matches`, the same
  filter as REST.

A recorded-as-of browse of status `active` therefore lists exactly the beliefs
a recorded-as-of recall can admit, on every backend.

### 7. Trust at T (step 3)

A stored `trust_score` is `trust.At` (ADR 0026) over today's inputs. A
recorded-as-of read that returned it reported Thursday's corroboration and
credit as Tuesday's belief, and `MinTrust`, ranking and agent-mode
contradiction resolution acted on it. The views now replace it with `trust.At`
over the inputs as they stood at T, scored at T (`recorded.TrustInputAt`, pure;
`Claims.scoreAt` applies it to every claim the view returns, and stamps
`TrustComputedAt = T`):

| Input | At T |
|---|---|
| evidence count | links recorded by T to episodes ingested by T, graded by independence (`domain.EffectiveEvidenceCount` over distinct episode authors), as the stores grade them |
| latest evidence | the freshest such episode's timestamp |
| credit | the applied net credit from `claim_credit_history`, by the same rule as status (§1); no history, the stored value |
| last confirmed | the stored value when at or before T, else none |
| confidence, half-life | the stored values (§3) |

What made it possible:

- **Evidence links carry when they were recorded.** `claim_evidence.created_at`
  on every backend (SQLite/libSQL `TEXT NOT NULL DEFAULT ''`, Postgres
  `timestamptz` NULL, MySQL `DATETIME(6)` NULL, memory per link). Writes stamp
  the caller's `ClaimEvidence.CreatedAt` or the store's clock
  (`domain.EvidenceRecordedAt`); a re-upsert keeps the original; a merge
  (`RepointEvidence`) moves a link with its time. The episode's `ingested_at`
  is not enough: extraction can run after ingestion, and a restatement attaches
  an already-ingested episode to an older belief.
- **Backfill rule, explicit and in one place.** Existing rows are not
  backfilled. A link with no recorded time reads as recorded at the later of
  its claim's `created_at` and its episode's `ingested_at`
  (`recorded.LinkRecordedAt`): it cannot predate either, and it is exact for a
  claim extracted from an episode as it was ingested and for a restatement. It
  is early only when extraction ran after both, which nothing recorded dates.
  The rule lives in code rather than a one-shot `UPDATE` so every backend
  applies the same one, and a mixed-precision text comparison in SQL cannot
  get it wrong.
- **Credit is versioned.** `ApplyBeliefCredit` records a row in
  `claim_credit_history` (claim, from, to, changed_at) in the same transaction
  whenever it changes the applied net credit; a write that leaves it unchanged
  (inhibition decay, an idempotent re-run) records nothing. Read through the
  optional `ports.CreditHistoryLister`. Every backend that implements
  `ports.BeliefCreditWriter` carries the table: SQLite (and libSQL), memory,
  Postgres (registered in the ADR-0007 RLS `scoped` array, so it gets a tenant
  column and policy) and MySQL (isolated by namespace database). On Postgres
  and MySQL the current value is read `FOR UPDATE`, so concurrent credit
  writes each record the value they replaced. Delete and cascade paths clear
  it. `TestCreditHistory_RecordsEveryAppliedCreditChange` fails if a backend
  implements the writer without the lister. Credit applied before the table
  existed has no history, and reads as the stored value.

Today's reads are unchanged: only the recorded views rescore. The cost is one
evidence read, one event read and one credit-history read per view call, on the
recorded-as-of path only.

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
- Step 2 acceptance tests: a merge after T keeps the belief and its
  contradiction as held at T (`TestRecordedAsOf_MergeAfterTKeepsTheBeliefHeldAtT`,
  SQLite, through the real `pipeline.ApplySemanticDedupe`); the browse rewinds
  status and merges on memory and SQLite
  (`TestBeliefs_RecordedAsOfRewindsStatusAndMerges`), over REST
  (`TestServe_ListBeliefsRecordedAsOfRewinds`) and gRPC
  (`TestListBeliefs_RecordedAsOfRewindsStatusAndMerges`); `ListMergedInto` is
  pinned on every backend (`TestClaims_ListMergedIntoOnEveryBackend`).

## Rollout

1. Reconstruction of events, claims, status, validity, archive, edges and
   narratives; no write-backs on time-travel reads. **Implemented.**
2. Merge-aware rewind (follow `merged_into` redirects backward when a winner was
   recorded after T); browse uses the same rewind. **Implemented** (§5, §6).
3. Trust at T: timestamp evidence links and version credit, then compute
   `trust.At` over the inputs as of T. **Implemented** (§7). Confidence and
   half-life stay at their stored values, and `last_confirmed` is unversioned
   (§3). Acceptance: `TestRecordedAsOf_TrustIsReconstructedAtT` (SQLite): a
   Monday episode linked on Thursday and a Thursday credit change today's trust
   and not Tuesday's; the link time round-trips on every backend
   (`TestClaimEvidence_RecordedAtRoundTripsOnEveryBackend`).
