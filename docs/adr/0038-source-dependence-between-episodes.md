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
  report on every transport, and every backend's trust inputs

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
repeat write is ignored, so the first justification recorded stands, and like
events the rows are append-only. They are stored in `event_dependencies` on
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
- **Citations of sources the brain has not stored yet.** A reference is
  resolved when its episode is written. A document that cites a source ingested
  later records nothing.
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
- The rows are append-only. A mistaken dependency is corrected by deleting
  the episode (destruction) or, for retraction purposes, by un-retracting. No
  surface edits dependencies in this step.
