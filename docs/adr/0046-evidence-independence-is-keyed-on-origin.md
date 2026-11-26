# ADR 0046: Evidence independence is keyed on origin, not principal

- **Status:** Accepted; implemented.
- **Date:** 2026-11-21
- **Deciders:** Felix Geelhaar
- **Builds on:** ADR 0026 (canonical trust), ADR 0036 (recorded-as-of reads),
  ADR 0038 (source dependence between episodes)
- **Adds:** `domain.EvidenceOrigin`; `trust/v3`. Amends every backend's trust
  inputs (SQLite and libSQL through `ListClaimTrustInputs*`, Postgres, MySQL,
  memory) and `recorded.TrustInputAt`.
- **Closes:** #390
- **Amended by:** ADR 0048 (the repeat weight is now 0; `trust/v5`)

## Context

Trust grades corroboration by independence (ADR 0026 §1):

```text
n = EffectiveEvidenceCount(distinct, total) = distinct + ⌊0.5 · (total − distinct)⌋
```

`distinct` was `COUNT(DISTINCT events.created_by)`: the principal that
ingested each evidence episode. A principal is who pressed the button, not
where the text came from, so the count was wrong in both directions:

- **A single-user brain has one principal.** Every document, every session,
  every independent report shares one `created_by`, so no belief of a local
  brain could ever have more than one distinct source. Real corroboration was
  discounted as a repeat.
- **One document ingested by two users counted as two sources.** A shared
  runbook read into a hosted brain by two teammates looked like two
  independent witnesses.

ADR 0038 asked the right question, *are these reports independent?*, and
answered it for declared dependence. It left the unit of a "source" as the
principal, which this ADR replaces.

### The measurement that motivated it

Read-only, on the developer's real brain (46,641 live beliefs):

| Key | Beliefs with ≥ 2 distinct sources | Max sources | Mean sources |
|---|---|---|---|
| principal (`created_by`) | 14 | — | — |
| origin (this ADR) | 1,217 (2.6%) | 35 | 1.052 |

Under the principal key, corroboration was effectively switched off: 14
beliefs out of 46,641. Under the origin key, 2.6% of beliefs have more than
one independent origin, which is the order of magnitude one would expect of a
brain built mostly from conversation.

The same measurement ruled out `source_input_id` as the key. Capture
re-ingests the same transcript text under fresh input ids, about 4.7 times per
belief on that brain, so keying on the input id would have turned each
restatement into a new "independent" source and inflated corroboration across
the board (the issue's "one document ingested 500 times counts as 250
corroborations", relocated from the principal to the input id).

## Decision

### 1. The independence key is the episode's origin

`domain.EvidenceOrigin(ev)` is the definition. Two evidence episodes of a
belief are one distinct source exactly when their origins are equal. In order
of precedence:

1. **The file.** `metadata.input_source_path`, when non-empty. One file is one
   source: all of its chunks, whoever ingested it, however often.
2. **The session.** `metadata.session_id` (else the legacy
   `metadata.input_session_id`, which `pipeline.EventSession` also reads),
   when non-empty. One conversation is one source, however many messages
   restate the belief.
3. **The text.** The episode's content with leading and trailing U+0020 spaces
   removed and ASCII letters lower-cased (`lower(trim(content))`). The same
   text re-ingested from many inputs is one source.

Each kind is prefixed (`path:`, `session:`, `content:`), so a text that
happens to spell a path or a session id never collides with one.

The repeat weight (`domain.EvidenceRepeatWeight`, 0.5) and the trust formula
(`trust.At`) are unchanged. Only the `distinct` argument changes.

### 2. ADR 0038 dependence is unchanged

An evidence episode that live-depends on another evidence episode of the same
belief adds no distinct source; it still counts toward the total, so it weighs
as a repeat. The rule is the same one hop, inside the belief's own evidence,
on every backend. Two different files where the second cites the first are one
source, as before.

### 3. One definition, restated in SQL

The Go function is the definition and carries the rule's documentation. The
in-process paths call it: the memory store's `trustInputLocked` and the
recorded-as-of reconstruction `recorded.TrustInputAt`. The SQL backends
cannot call Go inside an aggregate, so each restates it as a `COALESCE` over
the same keys and points at the function:

| Backend | Metadata read | Text normalisation |
|---|---|---|
| SQLite, libSQL | `json_extract(metadata_json, '$.key')` | `lower(trim(content))` (ASCII-only `lower`) |
| Postgres | `metadata_json->>'key'` (jsonb) | `translate(trim(content), 'A…Z', 'a…z')` |
| MySQL | `JSON_UNQUOTE(JSON_EXTRACT(metadata_json, '$.key'))` | `LOWER(TRIM(content))`, compared `AS BINARY` |

The normalisation is SQLite's, because SQLite is the default backend and its
`lower()` and `trim()` are the narrowest: `trim` strips U+0020 only and
`lower` folds ASCII only. Postgres uses `translate` rather than `lower` to
fold exactly the same letters. MySQL compares the key as `BINARY`, since under
the tables' `utf8mb4_unicode_ci` collation `DISTINCT` would otherwise also
merge accents and ignore trailing spaces. Its `LOWER()` folds non-ASCII
letters as well, so MySQL can count *fewer* sources than the definition for
texts that differ only in the case of a non-ASCII letter, and never more.
That divergence is documented on the function and accepted, because the
exact alternative (26 nested `REPLACE` calls over every evidence episode's
text) costs more than the case it fixes.

`internal/store/trust_origin_parity_test.go` drives the same scenarios
through every backend (Postgres and MySQL when `TEST_POSTGRES_DSN` /
`TEST_MYSQL_DSN` are set) and checks the evidence count, that stored trust is
`trust.At` of it, and that the recorded-as-of reconstruction agrees with live
trust: one file ingested by two principals is one source; two files by one
principal are two; two sessions are two; one session restating a belief in
four messages is one; a path outranks the session it was read in; identical
text across two inputs with no path or session is one; distinct texts are
two; and a dependent episode still adds no source.

### 4. What was rejected

- **The principal (`created_by`).** The defect this ADR fixes. A principal
  says who is accountable for a write, which is still recorded and audited; it
  says nothing about whether two reports are independent.
- **`source_input_id`.** Random per ingestion call, so a re-capture of the same
  text is a new input. Measured at ~4.7 inputs per belief on a real brain
  (above): keying on it would have inflated corroboration far more than the
  principal ever deflated it.
- **Similarity of the text.** ADR 0038 forbids similarity matching for
  dependence, and the reason holds here: near-identical sentences are also
  what genuine independent corroboration looks like, and no threshold tells
  the two apart. Only exact equality after a fixed, documented normalisation
  is used.
- **A content hash column.** It would make the text key cheaper to aggregate
  and indexable, but needs a schema change and backfill on four backends for
  a value that is derivable at read time. Not needed at today's scale; it is
  the obvious next step if the aggregate's cost shows up.

## Consequences

- **Trust moves for a minority of beliefs.** On the measured brain, the
  1,217 beliefs (2.6%) with more than one origin gain corroboration and their
  trust rises. A belief whose evidence came from one file ingested by several
  principals loses the extra sources it never had, and its trust falls. On a
  single-user brain nothing can fall: every belief had exactly one distinct
  source before.
- **The trust model version is bumped to `trust/v3`.** ADR 0026 §5 makes a
  stored score authoritative only as "`trust.At` at `trust_computed_at` under
  `trust_model_version`". The formula is unchanged, but its evidence-count
  input is not, so a v2 score and a v3 score for the same belief at the same
  instant can differ, and they must not share a version. Scores written before
  this change stay stamped `trust/v2`; `mnemos recompute-trust --stale`
  rescores them in bounded, verified batches. An ordinary write rescores only
  the beliefs it touched, as before.
- **Recorded-as-of trust before this change differs from what was recorded.**
  `recorded.TrustInputAt` reconstructs the inputs at T and scores them with
  today's model (ADR 0036 §7 already stamps the reconstruction with the
  current `ModelVersion`). A recorded-as-of read at an instant before this
  change therefore reports the v3 trust of the inputs as they stood then, not
  the v2 score the brain stored at the time. That is deliberate: the
  reconstruction answers "how trustworthy was this belief, on what was known
  at T", under the model the brain now holds to be right. The v2 scores
  themselves are not kept; nothing reads them.
- **Cost.** The aggregate reads two or three JSON keys and the episode's text
  per evidence link and `DISTINCT`s a string the length of the text. The trust
  aggregate is already bounded to the claims a write touched
  (`RecomputeTrustForClaims`), so the per-write cost does not grow with the
  brain. A full `recompute-trust --all` pays it over every evidence link.
- **brainbench.** `make brain-eval` before and after: every verdict is
  unchanged (improved 1, inert 2, ran-no-measurable-effect 3). The only metric
  that moved is the descriptive `mean_trust`, up on three scenarios
  (`evidence_retraction` 0.850 → 0.863, `noisy_capture` 0.652 → 0.661,
  `redundant_sessions` control 0.487 → 0.502), because the scenario corpora
  are seeded by one principal and their distinct documents now count as
  distinct sources.
- **Capture streams.** A session's episodes share one origin, so a belief a
  user restated across one long conversation is one source with many repeats,
  exactly the echo-chamber case `EvidenceRepeatWeight` exists for.
- **Not addressed.** Two different files that are copies of each other without
  a citation remain two sources (ADR 0038 "What deterministic detection cannot
  see"). Text ingested through API paths that carry neither a path nor a
  session is keyed on its text, so two different paraphrases of one report
  posted by API count as two sources.
