# ADR 0038: Source dependence between episodes

- **Status:** Accepted; implemented.
- **Date:** 2026-11-18
- **Deciders:** Felix Geelhaar
- **Builds on:** ADR 0037 (evidence retraction), ADR 0036 (recorded-as-of
  reads), ADR 0026 (canonical trust)
- **Adds:** `domain.EventDependency`, the `event_dependencies` table on every
  backend (SQLite schema v36, Postgres RLS `scoped`), `ports.EventDependencyRepository`
  (`store.Conn.Dependencies`), `ports.EventSourceLister.ListBySourceInputIDs`,
  `internal/provenance`; amends `pipeline.PersistArtifacts`, the governed
  episode writes, `RetractEvent` / `UnretractEvent`, the retraction impact
  report on every transport, and every backend's trust inputs. Editing
  dependencies (2026-11-19) adds `removed_at` / `removed_by` (SQLite schema
  v38), `internal/dependency` and the list / declare / remove surfaces

## Context

ADR 0037 finds a retraction's dependents through evidence links: a belief
whose only live evidence is the retracted episode is withdrawn, one with other
live evidence is kept. The brainbench `evidence_retraction` scenario measured
what that misses. It tied raw chunk retrieval (3 of 5 unsupported beliefs
surviving on both):

1. A **verbatim copy** in another document is folded onto the original belief
   at ingest (exact-text dedupe). The copy's episode becomes a second evidence
   link, and the retraction keeps the belief because of it. The blog in the
   scenario says where it got the sentence ("Source: the libfoo vendor
   advisory."), and nothing read that.
2. A **paraphrase** in another document is a separate belief with its own
   evidence. The retraction never reaches it.
3. A **conclusion drawn in another document** is the same: separate, never
   reached.

The principle the brain is missing: *several reports copying one original are
not independent evidence*. Trust has the same blind spot. Corroboration is
graded by distinct evidence authors (`domain.EffectiveEvidenceCount`), and a
copy written by a second author counts as a second voice.

## What the practice and literature say

- **Provenance graphs record derivation between documents.** W3C PROV-O has
  `prov:wasDerivedFrom`, `prov:wasQuotedFrom` and `prov:hadPrimarySource`
  between entities. They are separate relations from attribution, so a
  derivation is a recorded edge and not something inferred from similarity.
- **Truth maintenance propagates through justifications, not resemblance.** In
  Doyle's JTMS a belief is OUT when every justification that supports it is
  OUT. A justification is an explicit link. Two beliefs that look alike do not
  justify each other (ADR 0037 already follows this for evidence links).
- **Citation analysis treats a citing work as dependent.** COPE's retraction
  guidance and retraction-propagation studies (for example, Bar-Ilan & Halevi
  2017 on citations of retracted articles) follow the citation graph to find
  affected work. They also show that a work citing a retracted paper is not
  thereby wrong, so affected work is flagged and reviewed, not deleted.
- **Source-independence in fact checking.** Work on corroboration and
  "copy detection" (Dong, Berti-Équille & Srivastava, *Integrating
  conflicting data: the role of source dependence*, VLDB 2009) shows that
  counting copiers as independent sources inflates confidence in whatever was
  copied. The same paper detects copying statistically, from shared *false*
  values across many objects. That needs many claims per source pair and
  knowledge of which values are false, which a single brain ingesting
  documents one at a time does not have.

## Decision

### 1. What counts as a dependency

An episode **depends on** another when it says so. Only explicit, deterministic
signals count:

| Signal | Where | Example | Kind |
|---|---|---|---|
| `metadata` | event metadata keys `cites`, `copied_from`, `derived_from` (and the `input_`-prefixed forms file and API ingestion produce), comma-separated | `cites: ev_123` | the key's kind |
| `link` | a URL anywhere in the text | `https://vendor.example/advisory/42` | `cites` |
| `named_source` | the phrase after a citation marker: a line starting `Source:`, `Sources:`, `Via:`, `Reference:`, `References:`, `Citation:`, `Credit:`, `Quoted from:`, `Copied from:`, `Reposted from:`, `Derived from:`, `Based on:`, or the inline `according to`, `as reported by`, `as reported in`, `quoting`, `citing` | `Source: the libfoo vendor advisory.` | `copies` for quoted/copied/reposted, `derived_from` for derived/based on, otherwise `cites` |

A reference **resolves** to stored episodes through the identifiers an episode
already has. No new identity is invented:

- metadata values and URLs must equal an event id, a `source_input_id` or an
  `input_source_path` exactly;
- a named source resolves when a run of words in the phrase, joined by `-`,
  `_` or a space, equals a `source_input_id` exactly (`the libfoo vendor
  advisory` contains `vendor advisory`, which matches `vendor-advisory`).
  Single stop words never match. The longest matching run wins. If the longest
  run matches more than one source, the reference is ambiguous and nothing is
  recorded.

A reference resolves to a **source**: every stored episode with that
`source_input_id` (a document's chunks share one). The citing document's own
source is never a target. A source with more than
`provenance.MaxEpisodesPerSource` (1000) episodes is not a specific citation,
and nothing is recorded for it. The rule is the same in reverse. A citation
anywhere in a document applies to every episode of that document in the same
write, because a footer `Source:` line speaks for the whole document, not for
the one paragraph it sits in.

### 2. How it is recorded

`domain.EventDependency` is one row: `episode_id` (the dependent), `depends_on`,
`kind` (`cites` | `copies` | `derived_from`), `signal` (`metadata` | `link` |
`named_source`), `reference` (the exact text that justified it, so a reader can
check it), `created_at`, `created_by`. The key is `(episode_id, depends_on)`. A
repeat write is ignored, so the first justification recorded stands. Rows are
never edited by detection; a person can remove or declare one (see "Editing
dependencies"), and a removal is a tombstone, not a delete. They are stored in `event_dependencies` on
SQLite (schema v36), libSQL (through SQLite), memory, Postgres (added to the
RLS `scoped` list) and MySQL. Destroying an event (`DeleteByID`, `DeleteAll`)
removes its rows in both directions. That is a deletion, not a retraction.

Detection runs on every write that stores episodes:
`pipeline.PersistArtifacts` (capture, `process`, `process_text`, `/v1/process`,
file ingestion, brainbench seeding), the governed episode write
(`govwrite.Writer.Events`), and the library's direct episode write
(`Memory.RememberEvent`).
All three call `pipeline.RecordEventDependencies`, so every ingest path detects
dependencies the same way. Text without a marker or URL costs one scan and no
reads.

Belief-level `derived_from` edges already exist in the relationship
vocabulary (`domain.RelationshipTypeDerivedFrom`). They link synthesised items
to their inputs and are written by no ingest path, so they cannot carry an
episode's citation. Dependency is a fact about *where a document came from*,
which is why it lives between episodes, not between beliefs.

### 3. Independence

An evidence episode `E` of a belief is **independent of** a source `S` unless
`E = S` or `E` depends on `S`, directly or through other episodes (the
**dependency closure** of `S`).

- **Retraction (ADR 0037 §2).** A belief's *live independent evidence* is its
  evidence from episodes that are not retracted, are not the retracted
  episode, and do not depend (transitively) on any retracted episode. A belief
  with none is retracted. The paths are followed in full.
- **Trust (ADR 0026).** A dependent evidence episode adds no distinct source.
  When an evidence episode depends directly on another evidence episode *of the
  same belief*, its author is not counted as a distinct source. The episode
  still counts toward the total, so it weighs as a repeat
  (`domain.EvidenceRepeatWeight`). The rule is one hop and inside the belief's
  own evidence on every backend (SQL `NOT EXISTS`, memory, and the
  recorded-as-of reconstruction, which counts only rows recorded at or before
  T). That keeps the four implementations identical and the aggregate a
  single query. A copy of a copy whose middle link is not evidence of the
  belief still counts as distinct. Retraction does not have this limit,
  because it walks the closure in Go.

### 4. How retraction propagates

`RetractEvent(S)`:

1. walks the closure of `S` over incoming dependencies (breadth-first,
   recording for each dependent the hop that reached it first);
2. takes every belief with evidence from `S` or from an episode in the closure;
3. for each, counts live independent evidence (§3);
4. withdraws a belief with none, with ADR 0037's reason
   (`retracted: evidence event S was retracted: …`), so un-retraction
   recognises its own transition unchanged;
5. closes the link to `S` and rescores a belief that has some and was linked
   to `S` (ADR 0037, unchanged);
6. leaves a belief that has some and was reached only through the closure
   untouched. Its evidence from a dependent was never counted as independent
   (§3), so retraction changes neither its links nor its trust. It is still
   listed.

The dependent episodes themselves are **not retracted**. A citing document is
not wrong because its source was, and it may say other things (COPE). Its
beliefs fall only when nothing independent supports them.

### 5. The impact report

`domain.RetractionImpact` gains two lists on every transport (CLI `--json`, MCP
`retract_episode`, REST, gRPC `RetractionImpact`, the Go library and client):

- `dependent_episodes`: every episode in the closure, with the hop that put it
  there (`episode_id`, `depends_on`, `kind`, `signal`, `reference`);
- `belief_paths`: for every withdrawn, rescored or dependent-kept belief, the
  `outcome` (`retracted` | `rescored` | `kept`) and `via`, the episode chain
  from `S` to the evidence that reached it. A directly linked belief has
  `via: [S]`. The blog's copy has `via: [advisory, blog]`.

An uncertainty note states what §6 says detection cannot see, whenever a
retraction runs.

Un-retraction (ADR 0037 §8) uses the same independence rule when it decides
whether a restored belief has live evidence. A belief whose only other
evidence depends on a *different*, still retracted episode stays withdrawn.

## What deterministic detection cannot see

- **An uncited paraphrase or an uncited conclusion.** The scenario's
  newsletter attributes its claim to "the vendor", which names no stored
  source. The checkout review cites nothing. Both stay beliefs with their own,
  apparently independent evidence. Matching them by similarity would also
  match the release notes, which state the release date in the advisory's
  exact words and are real corroboration. A similarity rule cannot tell the
  two apart, so this ADR does not use one.
- **Verbatim text without a citation.** Copying is not detected from text
  alone, for the same reason: identical sentences are also what independent
  corroboration looks like.
- ~~**Citations of sources the brain has not stored yet.**~~ Resolved since
  this ADR first shipped: a citation whose source is not stored yet is kept as
  a pending reference, and the dependency is recorded when the source is
  written. See [Late citations](#late-citations) for the mechanism, its bound
  and what remains out of scope.
- **A source named differently from its `source_input_id`.** Production
  ingestion assigns random input ids (`in_…`) and records a file's path, so a
  prose citation of a file by title resolves only if the path or id is in the
  text. Metadata (`cites:`) is the reliable channel for callers that know
  their provenance.
- **Episode granularity.** A citation makes every episode of the citing
  document dependent. A document that cites a source and also reports its own
  observation has both treated as dependent. Retraction withdraws them only
  when nothing independent supports them.

A model-assisted pass could propose dependencies for the uncited cases, as a
reviewed suggestion and never as an automatic edge. That is out of scope here,
per the strategy's "deterministic first, model-assisted only where needed".

## Late citations

A reference is resolved when its episode is written, and a document is often
ingested before the source it cites. Without more, that citation is forgotten,
and the document counts as independent evidence for as long as the brain
lives. Since retraction follows dependence and trust discounts it, the order
of ingestion would decide both. So a reference that resolves to nothing is
kept until its source arrives.

**Pending keys.** `provenance.DetectAll` returns, next to the dependencies,
the lookup keys of every reference that resolved to no stored episode,
stamped with the episode that made the reference. All keys use one canonical
form (`provenance.CanonicalKey`): lower-case, with `-`, `_`, `/`, `.` and
whitespace collapsed to single spaces.

- An exact reference (a metadata id or a URL) leaves the canonical form of
  each identifier that matched nothing. Each identifier resolves on its own,
  so one stored id in `cites: a, b` does not hide the other.
- A named reference leaves every contiguous word run of its phrase (at most
  twelve words, joined by single spaces, a lone stop word excluded), plus the
  canonical form of each whole token. The whole token differs from a run only
  when it holds another character, as in `inline:ev_1`.
- Not pending: a reference that resolved; an ambiguous one (its phrase spells
  two sources, so no later source can make it specific); an exact identifier
  that matched anything, the citing document included; and any key equal to
  one of the citing document's own keys.
- One episode records at most 256 keys, and a key longer than 512 characters
  is not recorded (MySQL's key-prefix limit sets that size, and every backend
  stores the same keys).

The rows live in `pending_references` (`event_id`, `lookup_key`,
`created_at`, key `(event_id, lookup_key)`, indexed on `lookup_key`). They
are append-only with insert-ignore, on SQLite (schema v37), libSQL (through
SQLite), memory, Postgres (in the RLS `scoped` list) and MySQL. Destroying an
event clears its rows, as it clears its dependencies.

**Resolution.** `pipeline.RecordEventDependencies` works in both directions on
every write. After recording the written episodes' own dependencies and
pending keys, it computes each written episode's keys (`provenance.EpisodeKeys`:
its id, its `source_input_id` and its `input_source_path`, which holds a file
path or URL) and lists the pending rows under them, skipping rows of the
episodes being written. Each citing document is detected again with the
normal resolver, which now finds the new source. A key is only a pre-filter:
two spellings that share a canonical form share a key, and the resolver
decides. The resulting dependencies are appended, and a matched row is deleted
unless its key is still pending after the re-detection.

The unit of re-detection is the citing document's write, as at write time: the
episodes of its `source_input_id` that share its ingestion time. A citation
applies to every episode of its document in the write that stored it, and to
no episode a different write stored under the same source id. Capture streams
share one (`agent_remember`, `float-back`), and loading the whole source would
spread one remembered episode's citation across the stream. A caller that
gives chunks of one document different ingestion times gets the dependency on
the citing chunk only. That under-records and never over-records.

**Trust.** A late dependency changes trust for beliefs evidenced by the citing
episode and by the source. Trust counts an evidence episode that depends on
another evidence episode of the same belief as a repeat (§3). The beliefs
evidenced by the citing episodes (`ClaimRepository.ListByEventIDs`) are
rescored with the scoped scorer, as `PersistArtifacts` rescores what it
touched. The source's own evidence, written after its episode by
`PersistArtifacts`, is rescored there as before.

**Bound.** One write re-detects at most `pipeline.MaxLateCitingDocuments` (32)
citing documents. Each costs a source lookup and one resolver pass over its
references, so the bound keeps a write's cost independent of how many
documents ever cited what it brings. The oldest pending citations go first.
The rest keep their rows and resolve when another episode carrying the key is
written. A source cited by more than 32 documents and never written again
leaves the excess pending.

**Out of scope.**

- *A source retracted before the late dependency was recorded.* Retraction
  (§4) follows the dependencies stored when it runs. A late dependency is
  normally recorded by the write that stores the source, before anything can
  retract it. When it is not (the citing document was past the bound on that
  write), a retraction of the source in between judges the citing document's
  beliefs without the dependency and does not withdraw them. Recording the
  dependency afterwards does not re-run the retraction. Un-retracting and
  retracting again applies the dependencies stored by then.
- *A source that grows after it resolved.* Once a citation resolves, its rows
  are gone. A later episode added to the same source in a separate write is
  not linked to documents that already resolved against its earlier episodes.
  This matches write time, where a citation resolves against the episodes
  stored at that moment.
- *Keys the canonical form cannot unify.* Resolution is case-sensitive and
  exact on identifiers, as at write time. A key match whose spelling the
  resolver does not accept keeps the row pending.

## Consequences

- Retracting the scenario's advisory withdraws the copied false claim, which
  now rests only on the blog, an episode citing the advisory. The release date
  survives on the release notes, which cite nothing. The paraphrase and the
  derived conclusion still survive (see above and the brainbench README).
- Copies no longer inflate trust when the original is evidence of the same
  belief.
- An episode write that finds a marker or URL pays indexed reads on
  `source_input_id` per reference: one per candidate run length for a named
  source (at most twelve), until one resolves. A path or URL also pays one
  `input_source_path` lookup, which is a JSON-metadata scan and not indexed.
  A write without a marker or URL pays nothing.
- A retraction pays one dependency read per closure level and one per
  evidence episode it judges. Trust aggregation gains a correlated
  `NOT EXISTS` per evidence link, served by the dependency primary key.
- A mistaken dependency is removed, and a missed one declared, from every
  transport (see "Editing dependencies" below). Removal keeps the row, so
  nothing detection does can undo it.

## Editing dependencies

*Added 2026-11-19.* Detection is deterministic and conservative: it misses a
copy nobody cited and can record a citation that was only a mention. Until
this step the only correction was destroying the episode. Every transport can
now list, declare and remove dependencies, with the same semantics everywhere.

**Surfaces.** `mnemos dependencies <id>` (`--include-removed`, `--declare <src>
--kind <k> [--note <text>]`, `--remove <src>`), the MCP tools
`episode_dependencies` (read), `declare_episode_dependency` and
`remove_episode_dependency` (`claims:write`, the scope of `retract_episode`),
REST `GET`/`POST /v1/episodes/{id}/dependencies` and `DELETE
/v1/episodes/{id}/dependencies/{depends_on}`, gRPC `ListEpisodeDependencies`,
`DeclareEpisodeDependency` and `RemoveEpisodeDependency`, and
`Memory.EventDependencies` / `DeclareEventDependency` /
`RemoveEventDependency` with the matching client methods. Every one calls
`internal/dependency` (`List`, `Declare`, `Remove`), which checks the ids, that
both episodes exist and are in the caller's runs (an episode outside them reads
as not found), the kind and the note, and renders one wire form. The writes are
governed (`govwrite.Writer.DeclareEventDependency` /
`RemoveEventDependency` on the daemon surfaces, `ApplyDependencyDeclaration` /
`ApplyDependencyRemoval` inside the library kernel) and audited like every
other governed write. A run-scoped listing leaves out rows whose other episode
is outside the caller's runs.

**Removal is a tombstone.** `event_dependencies` gains `removed_at` and
`removed_by` (SQLite schema v38, libSQL, memory, Postgres, MySQL). Removing
sets them; it never deletes the row. `Append` ignores a pair that is stored,
removed or not, so neither detection running again over the citing document
nor late-citation resolution can bring back a dependency a person removed.
Deleting the row instead would have let the next re-ingest of the same text
silently undo the correction. Every read that means "the live dependencies"
filters `removed_at IS NULL`: `ListByEventIDs`, `ListDependents`, the trust
aggregate's correlated `NOT EXISTS` on every backend and the memory trust
path, and through them the retraction closure and the independence check.
`ListByEventIDsWithRemoved` / `ListDependentsWithRemoved` keep the tombstones
for audit and for recorded-as-of trust, which counts a dependency at T only
while it was in force: `created_at <= T` and (`removed_at` unset or after T).
Destroying an episode still hard-deletes its rows, tombstones included.

**Declaring.** A declared row has signal `declared`, the caller's kind, the
caller's note as its reference (at most 512 characters, the pending-key bound)
and the caller as its author. Declaring a pair that has a live row is a
conflict (REST 409, gRPC `AlreadyExists`, `ErrDependencyExists`), because the
first justification stands. Declaring a removed pair revives it: the tombstone
is cleared and kind, signal, reference, `created_at` and `created_by` are
overwritten with the declaration. Removing a pair with no live row is not found
(REST 404, gRPC `NotFound`, `ErrDependencyNotFound`).

**What an edit changes.** Trust counts a dependent evidence episode as a
repeat, so only the beliefs the dependent episode evidences can change. Each
edit rescores exactly those with the scoped scorer
(`pipeline.RescoreBeliefsOfEpisodes`, the same rescore late citations use) and
returns them, with the stored row and `source_retracted`: whether the source
episode is retracted now.

**No automatic re-retraction.** An edit never runs or undoes a retraction. If
the source is retracted, removing a dependency does not restore the beliefs
the retraction withdrew through it, and declaring one does not withdraw the
dependent episode's beliefs. Doing either automatically would make a
bookkeeping correction a silent status change on beliefs, outside the
retraction record (ADR 0037) that says who withdrew what and why, and a later
un-retraction would then restore beliefs nobody recorded as withdrawn by it.
`source_retracted` tells the caller instead: un-retracting and retracting the
source again applies the dependencies stored by then, on the record.

**Limits.**

- *A revival rewrites history.* Reviving overwrites `created_at`, so a
  recorded-as-of read between the original recording and the removal no
  longer sees the dependency. Keeping every interval would need a history
  table; one row per pair was kept because the revival is rare and the
  current state is what retraction and trust read.
- *A tombstone is per pair.* When the citation also resolves to a different
  episode carrying the same key (a mirror of the source), that is a different
  pair and is recorded.
- *Conflicts are decided before the governed write.* The kernel carries an
  executor's error only as text, so `internal/dependency` reads the pair first
  to answer 409 / 404. Two concurrent edits of the same pair can both pass
  that check; the store still refuses the second (the upsert affects no row,
  or the update finds no live row), which then surfaces as an internal error
  rather than a conflict.
- *A failed rescore leaves the row written.* The rescore runs after the row
  is stored; if it fails, the edit reports an error and a retry of the same
  edit is a conflict. `mnemos recompute-trust` repairs the scores.
