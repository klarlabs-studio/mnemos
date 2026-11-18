# ADR 0036: A recorded-as-of read reconstructs what the brain believed

- **Status:** Accepted; steps 1, 2 and 3 and closing instead of deleting (§8) implemented.
- **Date:** 2026-11-17
- **Deciders:** Felix Geelhaar
- **Builds on:** ADR 0034 (time axes; `claim_validity_history`)
- **Amends:** `query.AnswerOptions.RecordedAsOf`, `mnemos.Query.RecordedAsOf`,
  `query --recorded-at`, `/v1/search?recorded_as_of=`, gRPC `RecordedAsOf`;
  adds `recordedAsOf` to the `recall_at_time` MCP tool. Step 2 amends
  `GET /v1/beliefs?recorded_as_of=`, gRPC `ListBeliefs.recorded_as_of` and
  `ports.ClaimRepository` (`ListMergedInto`). Step 3 amends
  `domain.ClaimEvidence` (`CreatedAt`), the `claim_evidence` table on every
  backend, and adds `claim_credit_history` and `ports.CreditHistoryLister`.
  §7 adds `claim_trust_input_history` and `ports.TrustInputHistoryLister` on
  every backend. §8 adds `closed_claim_evidence` and `closed_relationships` on
  every backend, `ClaimRepository.CloseEvidence`,
  `ListClosedEvidenceByClaimIDs`, `ListClosedEvidenceByEventIDs`,
  `RelationshipRepository.ListClosedByClaimIDs`, `ListAllClosed` and
  `ports.RelationshipCloser` (replacing `RelationshipDeleter`); amends
  `RepointEvidence`, `RepointEndpoint` and `Writer.DropRelationships`

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
| relationships | only edges with `created_at ≤ T` exist (contradictions, hops, priming); endpoints a later merge moved are moved back (§5); edges a later merge dropped are back (§8) |
| evidence links | live links, plus closed links with `created_at ≤ T < closed_at` (§8) |

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

- **Trust.** Reconstructed by step 3 (§7), every input: evidence, credit,
  confidence, half-life and the last explicit confirmation.
- **Merges.** Reconstructed from the redirect, with the approximations stated
  in §5. Step 2.
- **Text.** The pipeline never rewrites a stored claim's text. Not rewound.
- **`last_verified`, `verify_count`.** Rehearsal bookkeeping (recall and replay
  bump them), not trust inputs (ADR 0026), and not versioned: versioning them
  would add a history write to every recall. A recorded-as-of read returns their
  stored values.
- **History older than its table.** Validity changes made before ADR 0034 step 2,
  credit applied before step 3 and trust-input changes made before §7's history
  have no row; the stored value is used (§7 states the rule).
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
  before it shipped. Edges a merge *dropped* (a loser–winner self-loop, a
  duplicate of an edge the winner already had) need their full row recorded,
  which §8 does.
- **Walk the redirect backwards (chosen).** Everything needed is already
  stored: `merged_into` says who absorbed whom, and the redirect's status
  transition dates the merge. One read port method is added,
  `ClaimRepository.ListMergedInto(targetIDs)` (claims whose `merged_into` is in
  the set; indexed `idx_claims_merged_into` on every backend; memory scans). It
  works for every merge ever recorded, adds no write-path cost and no table.

The rules, in `internal/recorded`:

- **Claims reached through events.** `ListByEventIDs` returns the claims whose
  link to one of the events was recorded by T and was in force at T, live or
  closed (§8): a merged loser is reached through its own closed links, and a
  winner is not reached through an episode it gained only in a merge after T.
  For a merge recorded before §8 it also returns every claim merged into the
  holders (transitively) that was still held separately at T: recorded by T,
  merge transition after T. A claim already a redirect at T is not added; its
  evidence was on its target then too.
- **A rewound claim** has its `merged_into` cleared when the merge is after T,
  and its status rewinds from the same history, so the loser reads as the
  active belief it was.
- **Evidence.** A claim merged after T reads its own evidence links, which the
  merge closed (§8). Only for a merge recorded before §8, which left no closed
  links, is it attributed the evidence of the claim at the end of its redirect
  chain. Links to events ingested after T, and links recorded after T, are
  dropped.
- **Edges.** An endpoint recorded *after* the edge was created cannot be the
  edge's original endpoint, so the edge arrived by a merge. The view moves it to
  the oldest claim merged into that endpoint that existed when the edge was
  created, followed forward through any merge that had already happened by T.

Stated approximations, all confined to claims a merge judged near-duplicates
(cosine above threshold, same type, scope and polarity, no recorded
contradiction):

- For a merge recorded before §8 only: its evidence links were moved, not
  closed, so a claim it absorbed after T is reached through all of its
  absorber's evidence, not only its own, and is attributed all of it. A merge
  recorded since closes the loser's links, and the view reads them exactly.
- An edge whose current endpoint already existed when the edge was created
  stays on that endpoint: it may have been the loser's, and nothing recorded
  says so. Both claims are in a read at T that reaches either, so the edge still
  surfaces, attached to the wrong one of two near-duplicates. When several
  losers could have held a moved edge, the oldest is chosen.
- Edges a merge dropped are closed, not deleted (§8), and are reconstructed.
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
| confidence, half-life, last confirmed | from `claim_trust_input_history`: the prior values of the first change recorded after T; nothing changed after T, the stored values. A last confirmation still after T reads as none |

What made it possible:

- **Evidence links carry when they were recorded.** `claim_evidence.created_at`
  on every backend (SQLite/libSQL `TEXT NOT NULL DEFAULT ''`, Postgres
  `timestamptz` NULL, MySQL `DATETIME(6)` NULL, memory per link). Writes stamp
  the caller's `ClaimEvidence.CreatedAt` or the store's clock
  (`domain.EvidenceRecordedAt`); a re-upsert keeps the original; a merge
  (`RepointEvidence`) closes the loser's link with its time and gives the
  winner the episode as a link recorded at the merge (§8). The episode's `ingested_at`
  is not enough: extraction can run after ingestion, and a restatement attaches
  an already-ingested episode to an older belief.
- **Backfill rule for links, explicit and in one place.** Existing rows are not
  backfilled. A link with no recorded time reads as recorded at the later of
  its claim's `created_at` and its episode's `ingested_at`
  (`recorded.LinkRecordedAt`): it cannot predate either, and it is exact for a
  claim extracted from an episode as it was ingested and for a restatement. It
  is early only when extraction ran after both, which nothing recorded dates.
  The rule lives in code rather than a one-shot `UPDATE` so every backend
  applies the same one, and a mixed-precision text comparison in SQL cannot
  get it wrong. No recorded source holds a better time to backfill from:
  `compilation_jobs` names the events of a CLI `extract` run but not the links
  it wrote, the hook, REST, gRPC and MCP write paths record no job naming events
  at all, `cognitive_journal` has no link kind, `claim_versions` dates claim
  writes, not links (a restatement links without rewriting the claim), and the
  kernel audit log is a process-local file outside the store.
- **Credit is versioned, and only an explicit write changes it.** Two writers
  set `confidence_components`. `ApplyBeliefCredit` owns the column and writes
  the map exactly as given; it is the only way to clear credit (credit
  assignment, salience tagging and inhibition decay use it). The upsert
  applies `domain.UpsertComponents` against the row it read in its
  transaction, identically on every backend: an incoming empty map keeps the
  stored one, incoming credit (any `credit:*` key) wins as given, and any
  other incoming map keeps the stored `credit:*` keys. A re-ingest therefore
  no longer erases applied credit, salience or inhibition, the hazard #334
  fixed for `half_life_days`; `TestUpsert_ACreditlessRewriteKeepsAppliedCredit`
  pins it per backend. Both writers record a row in `claim_credit_history`
  (claim, from, to, changed_at) in the same transaction whenever the stored
  applied net credit changed, through the same before/after comparison as the
  trust inputs below; a write that leaves it unchanged (inhibition decay, an
  idempotent re-run, a credit-less restatement) records nothing. Read through the
  optional `ports.CreditHistoryLister`. Every backend that implements
  `ports.BeliefCreditWriter` carries the table: SQLite (and libSQL), memory,
  Postgres (registered in the ADR-0007 RLS `scoped` array, so it gets a tenant
  column and policy) and MySQL (isolated by namespace database). On Postgres
  and MySQL the current value is read `FOR UPDATE`, so concurrent credit
  writes each record the value they replaced. Delete and cascade paths clear
  it. `TestCreditHistory_RecordsEveryAppliedCreditChange` fails if a backend
  implements the writer without the lister, or if either writer changes the
  credit without a row; the AST guard below covers `confidence_components`
  too. Credit applied before the table
  existed has no history, and reads as the stored value.
- **The row's own trust inputs are versioned.** Confidence, `half_life_days`
  and `last_confirmed` are overwritten in place by three writers on every
  backend: the upsert (confidence; a non-zero half-life, per its conflict
  rule), `MarkVerified` (a non-zero half-life) and `MarkConfirmed`.
  `claim_versions` could not serve: it exists only on SQLite and memory,
  snapshots text, confidence and status on every upsert whether or not they
  changed, and misses the other two writers and columns. One table,
  `claim_trust_input_history` (claim, prior confidence, prior half-life, prior
  last confirmation, changed_at), is written on SQLite (and libSQL), memory,
  Postgres (in the RLS `scoped` array) and MySQL. Each writer reads the three
  values before its statement and after it, in the same transaction (`FOR
  UPDATE` on Postgres and MySQL), and records the prior values only when the
  row changed, so whatever conflict rule the statement applied, the history
  matches what was stored. Memory writes every claim row through one function,
  `putClaimLocked`, which records. Delete and cascade paths clear the table.
  Read through `ports.TrustInputHistoryLister`; the view applies the same
  values to the claim it returns, so a read at T also shows the confidence,
  half-life and confirmation the brain held at T.
- **Every writer is covered, and a new one cannot slip in.**
  `TestTrustInputHistory_RecordsEveryWritePath` drives each writer on every
  backend and asserts the rows the lister returns.
  `TestTrustInputWriters_RecordHistory` reads the SQL backends' Go AST
  (literals and string constants, sqlc's generated queries included) and fails
  if any function holding SQL that assigns one of the three columns, or
  `confidence_components`, on `claims` reaches an entry point without passing through
  `recordTrustInputChange`; `TestMemoryClaimWritesGoThroughPutClaimLocked`
  fails on any other write into memory's claims map.
- **Rows written before the history: one rule.** A claim with no recorded
  change reads its stored values, held since creation as far as anything
  recorded says (`recorded.StoredInputsAt`). It is exact for every claim not
  changed since the history shipped; a claim changed since gets its earlier
  values from its first recorded change, which holds what it replaced. Only a
  change made before the history existed is lost, and nothing recorded it. A
  last confirmation that the rule would place after T reads as none, so it
  understates freshness rather than overstating it.

Today's reads are unchanged: only the recorded views rescore. The cost is one
evidence read, one event read, one credit-history read and one trust-input
history read per view call, on the recorded-as-of path only. On the write path,
each upsert, verification and confirmation reads the three columns once more
inside its transaction, and writes a history row only when one changed.

### 8. Ended links and edges are closed, not deleted

Four writes used to delete rows a recorded-as-of read needs:

- retraction (ADR 0037) deleted the evidence link of every belief it rescored,
  so a read from before the retraction missed the link, the trust it supported
  and, when the belief's other evidence came later, the belief itself;
- a merge deleted the loser's evidence links once it had copied them to the
  winner (and the copy carried the loser's time, so the winner counted them
  from before it held them), which is why §5 had to attribute a loser all of
  its winner's evidence;
- a merge deleted the edges it dropped (a loser–winner self-loop, a duplicate
  of an edge the winner already had);
- pruning (`relate --prune-supports`, `relate --prune-stale`,
  `prune --session-noise`) deleted edges it judged spurious, though the brain
  held them until then.

All now **close** the row instead:

- `closed_claim_evidence (claim_id, event_id, created_at, closed_at,
  closed_by, reason)` and `closed_relationships` (every `relationships`
  column, plus `closed_at`, `closed_by`, `reason`), on SQLite/libSQL, memory,
  Postgres (both in the ADR-0007 RLS `scoped` array) and MySQL (namespace
  database). A closed row was in force over `[created_at, closed_at)`.
- `ClaimRepository.CloseEvidence(claim, event, at, note)` moves the link out
  of `claim_evidence` into `closed_claim_evidence` in one transaction, keeping
  its `created_at`; closing a link that is not live records nothing.
  Retraction calls it with the retraction's time, actor and reason.
  `UnlinkEvidence` stays for `delete-event`, which destroys by design.
- `ClaimRepository.RepointEvidence` closes every link of the loser, with its
  time, reason `merged <loser> into <winner>` (`domain.MergeClosedReason`), and
  gives the winner each episode as a link recorded at the merge (one the
  winner already held keeps its own time).
- `RelationshipRepository.RepointEndpoint` stages every edge it may drop, as
  it is, and keeps the staged copy only of those it actually dropped, closed
  with the same merge reason. Each backend keeps its own collision rule; the
  closure records whichever edge that rule dropped, with the endpoints it had.
- `ports.RelationshipCloser.CloseByIDs(ids, at, note)` (formerly
  `RelationshipDeleter.DeleteByIDs`, on every backend) closes edges by id with
  a `pruned: <why>` reason. `relate --prune-supports` calls it directly;
  `Writer.DropRelationships(drop, note)`, used by `relate --prune-stale` and
  now also by `prune --session-noise`, matches the enumerated edges on content
  per claim and closes them by id. That also retires its old per-claim
  delete-and-restore, and with it the window in which a concurrent write to the
  same claim could be lost.

**Why separate tables, not a `removed_at` column.** The live tables are read
by a few dozen hand-written queries across four backends: trust inputs,
evidence counts, pagers, gap and health samplers, autoedge, dedupe, brainbench.
A column would have needed a `removed_at IS NULL` in every one of them, and the
first forgotten one would leak a retracted link into live trust. Moving the row
out leaves every live reader correct by construction, and a reopened link
(below) needs no change to `claim_evidence`'s `(claim_id, event_id)` key.
`TestClosedRows_OnlyRecordedReadsSeeThem` reads the Go AST of every
production file and fails if a closed table is named outside the stores, or a
closed lister is called outside `internal/recorded`.

**Reads at T.** `recorded.Claims.ListEvidenceByClaimIDs` adds the closed links
in force at T (`ClosedEvidence.InForceAt`), so trust at T (§7) counts them;
`ListByEventIDs` also reaches a claim through a closed link in force at T;
`recorded.Relationships` adds the closed edges in force at T, which then go
through the merge rewind of §5. When the same (claim, event) arrives both
closed and live (a link closed by a retraction and reopened by its
un-retraction), the one recorded by T is the one returned; a live link
recorded after T is not evidence at T. A loser whose merge closed its links
(`domain.IsMergeClosureOf`) reads exactly those, so the §5 attribution of its
winner's evidence applies only to merges recorded before this section.

**Reopening appends.** Un-retraction (ADR 0037 §8) writes a new live link
recorded at the un-retraction; the closed interval stays. History is
append-only, so a read inside the retracted window sees neither, and a read
before the retraction sees the original.

**Destruction still destroys.** Deleting a claim (`DeleteCascade`, `DeleteAll`)
clears its closed links; deleting its edges (`DeleteByClaim`, `DeleteAll`)
clears its closed edges; deleting an event (`DeleteByID`, `DeleteAll`) clears
the closed links that name it; `reset` clears everything. The closed tables
carry no foreign keys for that reason. `Writer.PruneRelationships` (replace
the whole edge set) is deprecated, has no production caller, and still
destroys, closed edges included.

**What stays unrecoverable.** Links and edges deleted before this change are
gone; a read of that period has only what was left, as before, and a merge
recorded before it keeps the §5 attribution.

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
3. Trust at T: timestamp evidence links and version credit, confidence,
   half-life and confirmation, then compute `trust.At` over the inputs as of T.
   **Implemented** (§7). Acceptance: `TestRecordedAsOf_TrustIsReconstructedAtT`
   (SQLite, through recall): a Monday episode linked on Thursday, a Thursday
   credit, a Thursday restatement with more confidence, a Thursday half-life
   and a Thursday confirmation change today's trust and not Tuesday's;
   `TestRecordedAsOf_StoredTrustInputsAreReconstructedAtT` shows the same for
   confidence, half-life and a belief confirmed before and after T on every
   backend; the link time round-trips on every backend
   (`TestClaimEvidence_RecordedAtRoundTripsOnEveryBackend`).
4. Close instead of delete (§8). **Implemented.** Acceptance:
   `TestClosedLinksAndEdges_AgreeAcrossBackends` (every backend: live reads
   exclude closed rows, the closed listers return them with their times,
   actor and reason, a merge closes exactly the edges it drops, destruction
   clears them); `TestRetractEvent_AgreesAcrossBackends` (a read before the
   retraction keeps the closed link and its trust, before and after
   un-retraction); `TestMerge_ReadBeforeTheMergeSeesEachClaimsOwnEvidence`
   (every backend: before a merge each claim reads exactly its own evidence
   and is reached only through its own episodes);
   `TestRelationships_CloseByIDsAcrossBackends` (pruned edges are closed and
   visible before the prune); `TestRecall_ReadBeforeRetractionKeepsTheClosedLink`
   and `TestRecordedAsOf_MergeKeepsTheEdgesItDropped` (SQLite, through recall).
