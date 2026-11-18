# ADR 0037: Evidence retraction and dependency propagation

- **Status:** Accepted; step 1 implemented; un-retraction (§8) implemented.
- **Date:** 2026-11-17
- **Deciders:** Felix Geelhaar
- **Builds on:** ADR 0034 (three ways a belief retires), ADR 0036 (recorded-as-of
  reads), ADR 0033 (merged beliefs become redirects), ADR 0018 (cognitive journal)
- **Adds:** `govwrite.Writer.RetractEvent`, `mnemos retract-event`, the
  `retract_episode` MCP tool, `ports.EventSourceLister`; amends `watch_file`
  (`cmd/mnemos/watch.go`). §8 adds `govwrite.Writer.UnretractEvent`,
  `mnemos unretract-event`, the `unretract_episode` MCP tool,
  `POST /v1/episodes/{id}/unretract`, the `UnretractEpisode` RPC,
  `Memory.UnretractEvent` and `Client.UnretractEvent`

## Context

Mnemos has no notion of a source being withdrawn. Before this ADR the word
"retract" appeared nowhere in the Go code, and three paths showed the gap:

- **`watch_file` only adds.** When a watched file changes, the tick loop
  (`Watcher.tick`, `cmd/mnemos/watch.go`) re-ingests the whole file through
  `ingestSingleDoc` (`cmd/mnemos/autoingest.go`). Every paragraph becomes a new
  event with a new random id (`parser.Normalizer.newEvent`). Nothing happens to
  the events of the previous version, so a belief extracted from a sentence the
  author deleted stays active, with the same evidence and trust as before. A
  deleted file is dropped from the watch set and its beliefs live on.
- **Nothing propagates from an event to what depends on it.** A belief's
  evidence is the `claim_evidence` (claim, event) link. Beliefs feed
  associations (`relationships`), decisions (`decision_beliefs`), outcome
  verdict edges (`entity_relationships`, `validates`/`refutes` onto a claim,
  `internal/autoedge`, `expectations.go`) and, through semantic dedupe, merge
  redirects (`internal/pipeline/semantic_dedupe.go`). None of them hears that a
  source stopped counting.
- **The only removal is destruction.** `delete-event` (`DeleteEventCascade`,
  `internal/govwrite/lifecycle.go`) deletes the event row and every claim left
  without evidence, with its version chain and status history. That answers
  "remove this data", not "this source was wrong": afterwards nobody can see
  what the brain believed, why, or that the belief was withdrawn, and a
  recorded-as-of read (ADR 0036) cannot reconstruct it.

ADR 0034 already names the retirement this needs: **retracted** means "it was
never true; we asserted it in error", recorded as `status = deprecated` with a
reason, distinct from **ended** (`valid_to`) and **archived** (`archived_at`).
ADR 0036 rewinds status from `claim_status_history`, so a retraction recorded as
a status transition is invisible to a read of the time before it, which is the
behaviour an audit needs.

## What the practice and literature say

- **Retraction is an assertion, not a deletion.** Datomic and XTDB record a
  retraction as a new fact about an old one; the history stays queryable as of
  any earlier time (ADR 0034 cites both).
- **Truth maintenance systems propagate support.** Doyle's JTMS (*A Truth
  Maintenance System*, AI Journal 1979) and de Kleer's ATMS (1986) keep, for each
  derived belief, the justifications that support it. When a premise is
  withdrawn, a belief stays IN only while some justification still holds. That
  is the rule below: a belief survives while it has live evidence elsewhere.
- **Scholarly retraction keeps the record.** COPE's retraction guidelines
  (2019) require a retracted article to remain available, clearly marked, with
  who retracted it and why; citing works are not silently edited, and whether a
  citing work is affected is a separate judgement.
- **Provenance distinguishes derivation from invalidation.** W3C PROV-O models
  `prov:wasInvalidatedBy` as an activity in its own right, with a time and an
  agent, rather than as removal of the entity.

## Decision

### 1. Retracting an event records a fact; the event stays

`govwrite.Writer.RetractEvent(ctx, eventID, domain.ChangeNote)` is the one
implementation (`internal/govwrite/retract.go`). It runs as a single governed
action (`retract_event`, write-local, idempotent), so the whole propagation
leaves one evidence record on the kernel session, like `delete_event_cascade`.

- The event row is immutable and is not touched.
- The retraction is appended to the cognitive journal (ADR 0018) as kind
  `event_retraction`, subject = the event id, row id
  `domain.RetractionJournalID(eventID)`, with `retracted_at`, `retracted_by`,
  `reason` and the impact report as the payload (`domain.EventRetraction`).
  The journal is append-only, exists on every backend (`store.Conn.Journal` is
  wired by sqlite/libSQL, memory, Postgres and MySQL), is in the Postgres RLS
  `scoped` list, and is idempotent on id, so a retraction is recorded at most
  once. No schema change was needed: the journal's payload is opaque and "new
  journal kinds are a code-only change" (`internal/domain/journal.go`).
- `govwrite.LookupRetraction(ctx, conn, eventID)` reads it back.
- Retracting an already retracted event writes nothing and returns the stored
  record with `already_retracted: true`. The journal row is written **last**, so
  a failure part-way leaves no record and a re-run converges: deprecating a
  deprecated belief and unlinking an absent link are both no-ops.

### 2. What happens to each dependent

A belief's **live evidence** is its evidence links to events that are not
themselves retracted. For every belief linked to the retracted event
(`ClaimRepository.ListByEventIDs`):

| Dependent | Rule | Recorded as |
|---|---|---|
| belief with no other live evidence | **retracted** | status → `deprecated` via `UpsertWithReasonAs`, which appends a `claim_status_history` row; reason `retracted: evidence event <id> was retracted: <why>`, changed_by the caller. The evidence link is **kept** (see §5). |
| belief already deprecated | left alone | listed as `already_deprecated` |
| belief with other live evidence | **rescored** | the link to the retracted event is removed (`UnlinkEvidence`), then `RecomputeTrustForClaims` with canonical trust (`rescoreKept`, shared with `DeleteEventCascade`); before and after trust are reported |
| association touching a retracted belief | kept, listed | recall's admission already excludes deprecated beliefs |
| decision citing a retracted belief | kept, listed for review | never edited, never marked refuted |
| outcome verdict edge onto a retracted belief | kept, listed | |
| schema (lesson) / reflex (playbook) | not reachable, stated | see below |

Derived items are **flagged for refresh, never silently rewritten**. In step 1
the flag is the impact report itself, persisted in the journal row, so it can
be read back after the fact.

Schemas and reflexes deserve a precise statement: they are synthesised from
action→outcome chains (`internal/synthesize`), and their evidence is
`lesson_evidence.action_id` and `playbook_lessons.lesson_id`
(`sql/sqlite/schema.sql`). No stored link runs from an event or a belief to a
lesson, so a retraction cannot find one. The report says so as an uncertainty
rather than reporting "none affected".

Counting only *live* evidence matters for a belief that was retracted, later
re-linked to a new event, and then loses that new event: its link to the first,
retracted event is still there (§5), and counting it would keep the belief alive
on evidence already withdrawn (`TestRetractEvent_EvidenceFromRetractedEventsDoesNotCount`).

### 3. An inspectable impact report that does not overclaim

`domain.RetractionImpact` lists `retracted_beliefs`, `already_deprecated`,
`rescored_beliefs` (id, trust before/after, remaining evidence), `associations`,
`decisions` (id and the retracted beliefs each cited), `outcome_edges`, and
`uncertainty`. The uncertainty notes are part of the contract:

- a listed decision cited a retracted belief; that does not make the decision
  wrong, because it may rest on other beliefs and its recorded outcome is what
  judges it;
- beliefs related to a retracted belief by an association are not re-evaluated;
- schemas and reflexes are not reachable by a stored link (§2);
- rescored trust reflects only the evidence that remains.

### 4. One operation, every surface

The CLI (`mnemos retract-event <id> [--reason <text>] [--json]`) and the MCP
tool (`retract_episode`, scope `claims:write`) both go through `runRetraction`
(`cmd/mnemos/retract.go`), which calls `RetractEvent` and renders the report
through one view type, so the two surfaces return the same document for the same
store (`TestRetract_MCPAndCLIShareOneOperation`). The MCP handler additionally
refuses an episode outside a run-scoped token's allowlist, reporting it as not
found. The capability is `episode.retract` in `internal/capability`.

Every other transport binds the same operation: the Go library
(`Memory.RetractEvent`), REST (`POST /v1/episodes/{id}/retract`), gRPC
(`RetractEpisode`) and the Go client (`Client.RetractEvent`). The request checks
(required id, the event exists, run scope reported as not found) live once in
`internal/retraction.Run`, which every surface calls, with a sentinel
`ErrEventNotFound` so REST answers 404 and gRPC `NotFound` rather than 500. The
daemon surfaces retract through `govwrite.Writer.RetractEvent`; the library
dispatches through its own governed kernel to `govwrite.ApplyRetraction`, the
same implementation. REST, MCP and the CLI return the same `retraction.Report`
document. Every write binding requires `claims:write`.

### 5. Interaction with ADR 0036 (recorded-as-of reads)

A recorded-as-of read at T reaches beliefs through events ingested at or before
T (`recordedEvents`, `recordedClaims.ListByEventIDs` in
`internal/query/recorded.go`) and rewinds each belief's status from
`claim_status_history`. Two choices follow:

- A **retracted** belief keeps its evidence link to the retracted event, so a
  read before the retraction still finds it, and the retraction transition,
  being after T, rewinds it to its earlier status. Today's read drops it
  because it is deprecated. This is asserted on SQLite and memory, and on
  Postgres and MySQL when their DSNs are set
  (`TestRetractEvent_AgreesAcrossBackends`).
- A **rescored** belief loses its link to the retracted event. A read before
  the retraction then reaches it only through its other evidence. When that
  other evidence was ingested after T, the read misses a belief the brain held
  at T. `claim_evidence` has no timestamps (ADR 0036 §3), so this is the same
  limitation ADR 0036 already lists for trust; the retraction record keeps the
  event id, which is enough to restore the link when evidence becomes
  versioned (ADR 0036 step 3).

Trust is not rewound (ADR 0036 §3): a read before the retraction shows a
rescored belief's current, lower trust.

### 6. Interaction with merges (ADR 0033)

Semantic dedupe repoints a loser's evidence onto the winner and keeps the loser
as a deprecated redirect. A retraction follows the links as they are now, so it
reaches the **winner**:

- if the winner has other live evidence (often the loser's, now repointed), it
  is rescored and survives;
- if the retracted event was its only live evidence, the winner is retracted,
  and its redirects keep pointing at it. A restatement of the loser's text then
  follows the redirect onto a deprecated belief (`ListMergedByTextKeys`).

A restatement of a retracted belief's own text is not folded back onto it:
the write path's duplicate check considers only live beliefs
(`ListLiveByTextKeys`), and claim ids are random (`extract.newClaimID`), so a
new source creates a new belief. Retraction never prevents the brain from
learning the fact again from a sound source.

### 7. Source revision: `watch_file`

File ingestion stamps `input_source_path` on every event (`internal/ingest`,
`parser.Normalizer`). A new optional port, `ports.EventSourceLister`
(`ListBySourcePath`), lists a file's events on every backend (SQLite
`json_extract`, Postgres `->>`, MySQL `JSON_EXTRACT`, memory scan;
`TestListBySourcePath_AgreesAcrossBackends`). On each tick:

- **Changed file.** After the new version is ingested, every stored event of the
  file whose text is not a chunk of the new version, and which is not one of the
  events just written, is retracted with reason
  `source revised: text no longer in <path>`. Unchanged paragraphs keep their
  older events, which still say what the file says.
- **Deleted file.** Every event of the file is retracted with reason
  `source deleted: <path>`.

Already retracted events are skipped before dispatch, so an unchanged history
costs a journal read per old event, not a governed write. A failed retraction is
logged and retried on the file's next change; the new version is already stored.

### 8. Un-retraction

A retraction can be wrong too: the source was right after all, or a revised
file was reverted. Un-retraction clears it, and like the retraction it is an
assertion about the past, never a deletion of it.

**Representation.** `govwrite.Writer.UnretractEvent(ctx, eventID, note)`
appends a journal row of kind `event_unretraction`
(`domain.EventUnretraction`: who, when, why, which retraction it clears and
what it changed). The retraction row stays. Each retraction carries a
`generation` (1 for the first, which is also what a row written before this
section decodes to); un-retraction *n* clears retraction *n*, and its row id is
`domain.UnretractionJournalID(event, n)`. A later re-retraction is generation
*n+1* with row id `domain.RetractionJournalIDGen(event, n+1)` (generation 1
keeps the original `retraction_<event>` id, so existing rows need no
migration). The journal's idempotent-on-id append still makes a duplicate of
any one transition impossible.

**Retracted at T.** `domain.RetractionHistory` folds an event's retraction and
un-retraction rows. At an instant T the event is retracted iff the highest
retraction generation recorded at or before T exceeds the highest
un-retraction generation recorded at or before T. Generations strictly
alternate in time (retraction *n* < un-retraction *n* < retraction *n+1*),
so this is the state of the last transition at or before T. Recall
(`internal/query/retracted.go`) reads both kinds once per recall and applies it
at the read's instant: a recorded-as-of read inside the retracted window hides
the episode, a read after the restoration shows it, and a read before the
retraction shows it. Beliefs need nothing new: a restoration is a status
transition in `claim_status_history`, which ADR 0036's rewind already reads.

**Which beliefs come back.** Only beliefs the cleared retraction itself
deprecated, i.e. its recorded `impact.retracted_beliefs`, and only while
nothing else has changed them since. A belief is restored when all hold:

- it still exists and has no `merged_into` (a merge decided where its belief
  lives now);
- it is deprecated, and its latest status transition is the retraction's own:
  `to_status = deprecated`, `changed_by` = the retraction's `retracted_by`, and
  the exact reason the retraction wrote. Any later transition (a human
  re-deprecating it with a reason of their own, a reactivation, another
  retraction) means someone else owns its status now. A write that leaves a
  deprecated belief deprecated records no transition (`claim_status_history`
  logs changes only), so it is not seen; a person who wants the belief to stay
  retired after an un-retraction deprecates it again afterwards;
- it has live evidence once the event is restored (normally the kept link to
  the event itself, §5).

A restored belief returns to the transition's `from_status` (its status before
the retraction, `active` if none was recorded), through `UpsertWithReasonAs`
with reason `unretracted: evidence event <id> was restored[: <why>]` and the
caller as `changed_by`. Every other candidate is listed in `skipped_beliefs`
with the reason it was skipped. Beliefs the retraction **rescored** get back
the evidence link it removed (`UpsertEvidence`) and are rescored with the other
restored beliefs (`rescoreKept`); their status is not touched, because the
retraction never changed it. A relinked belief that is deprecated (by a later
retraction of another source, say) stays deprecated: un-retracting this event
does not undo another retraction, and the report says so. Associations,
decisions and outcome edges were never rewritten, so nothing is restored for
them. Trust is recomputed, never restored from the report: it reflects the
evidence the belief has now.

**Idempotency and errors.**

- Un-retracting an event that was never retracted is a user error
  (`retraction.ErrNotRetracted`: CLI exit with a user error, REST `409`, gRPC
  `FailedPrecondition`), checked in `retraction.Unretract` before the governed
  dispatch, like the not-found check.
- Un-retracting an event whose latest transition is an un-retraction writes
  nothing and returns that stored record with `already_unretracted: true`.
- An unknown event, or one outside a run-scoped token's runs, is
  `ErrEventNotFound`, exactly as for retraction.
- As with retraction, the journal row is written last, so a failure part-way
  leaves the event retracted and a re-run converges: a restored belief's latest
  transition is now the restoration, so a re-run lists it as skipped rather
  than restoring it twice, and relinking an existing link is a no-op.
- `watch_file` skips an event only while it is retracted, so a file that is
  revised again after an un-retraction retracts afresh (generation *n+1*).

**Surfaces.** Every transport that binds retraction binds un-retraction, through
`internal/retraction.Unretract`: `mnemos unretract-event <id> [--reason]
[--json]`, the `unretract_episode` MCP tool (`claims:write`), REST
`POST /v1/episodes/{id}/unretract`, gRPC `UnretractEpisode`, `Memory.UnretractEvent`
and `Client.UnretractEvent`. The capability is `episode.unretract`. The CLI,
MCP and REST return the same `retraction.UnretractionReport`.

## Consequences

- A wrong or revised source can be withdrawn without destroying the record:
  the belief, its history and the event remain, and the retraction says who,
  when and why.
- A belief survives the loss of one source exactly when another live source
  supports it, and its trust drops to what the rest supports.
- `watch_file` now keeps a file's beliefs in step with the file in both
  directions.
- Every retraction costs one governed dispatch and, per dependent belief, an
  evidence read; listing decisions is a full `Decisions.ListAll`, acceptable
  while decisions number in the hundreds.
- Fixed on the way: SQLite's `ListStatusHistoryByClaimID` did not select
  `changed_by`, so the actor of a transition read back empty on SQLite while
  Postgres, MySQL and memory returned it. The retraction test asserts it on
  every backend.

## Not done in step 1

- ~~**Recall still ranks a retracted event.**~~ Done in step 1b: the query
  engine reads the retraction journal once per recall (`WithRetractions`,
  `internal/query/retracted.go`) and hides every episode retracted at or before
  the read's instant (now, or the recorded-as-of instant, so a read of the past
  still sees it). Every surface gets it through `newQueryEngine` /
  `query.NewEngineWith`, the library included (`TestRecall_SkipsRetractedEpisodes`).
- **A persistent refresh flag** on lessons, playbooks and decisions that the
  synthesiser and `decision show` read. Today the flag lives only in the
  retraction record.
- ~~**Un-retraction.**~~ Done: §8.
- **Evidence-link timestamps**, which would let a recorded-as-of read restore a
  rescored belief's link to the retracted event (ADR 0036 step 3).
- **Watch-file path aliasing.** Matching is on the resolved absolute path the
  watcher stores. A file first ingested under a different spelling of its path
  (a symlink, a relative path resolved elsewhere) is not found.
- **Transient deletes.** A tool that deletes a file and recreates it between two
  ticks retracts its events; the recreated file is no longer watched until it is
  registered again, and its re-ingest creates new beliefs.

## Rollout

1. `RetractEvent` with the impact report, on every backend; CLI and MCP through
   the same call; `watch_file` retracts revised and deleted text.
   **Implemented.**
2. Recall excludes retracted events; persistent refresh flags read by
   synthesis and decision review; REST, gRPC, Go library and client bindings
   (**bindings implemented**).
3. Un-retraction as a recorded transition (**implemented**, §8);
   evidence-link timestamps so as-of reads restore rescored links.
