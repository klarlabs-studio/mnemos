# brainbench — do the cognitive processes improve what the brain knows?

Mnemos runs continuous cognitive processes: consolidate, forget, reinforce,
credit, salience, synthesize, decay. Before this harness, nothing measured
whether any of them **improves** anything. `mnemos consolidate` prints counters
— `forgotten: 412`, `associations_decayed: 900` — which establish that the pass
**ran**, not that the brain got better. That gap made every claim about the
brain improving unfalsifiable.

This harness closes it, and is built so that it can return bad news.

```bash
make brain-eval          # human table + brainbench.json
make brain-eval-strict   # same, exit 1 if any scored metric degraded
go run ./tools/brainbench -human=false -json -   # JSON to stdout
```

## Method

A paired A/B experiment on byte-identical brains.

1. **Seed** one pristine brain from the scenario corpus, document by document,
   reproducing the production ingest path (`mnemos process --embed`) offline:
   rule-based extraction, ingest-time exact-text dedupe, within-batch and
   incremental relationship detection, then event and claim embeddings.
2. **Warm up** (optional per scenario) by running recall against the seed, so
   retrieval-created state — Hebbian association strength, competitive
   inhibition — exists for the decay stages to act on.
3. **Copy** the seed file three times. The arms are byte-identical, so they
   cannot differ for any reason except the treatment.
4. **Treatment** — the process set runs. The merge redirects it leaves
   (`merged_into`) are read as the run's belief *identity*, before any arm is
   measured, and every arm is scored by that same identity.
5. **Control** — measured, untreated. The treatment arm is then measured.
6. **Noise arm** — a second untreated copy, measured independently. It must
   reproduce the control's numbers exactly. When it does not, the run is
   reported `invalid-nondeterministic-measurement` and no delta is attributed.

Measurement is itself a mutation — recall applies the Hebbian, reconsolidation
and inhibition write-backs — which is why every arm is a throwaway copy measured
exactly once, and why the census is read before the probes fire.

## What it measures

| Metric | Direction | What it tells you |
|---|---|---|
| `answer_hit_rate`, `answer_precision_at_1`, `answer_mrr` | higher better | Does the right claim come back, and how high? |
| `forbidden_hit_rate` | lower better | Does a superseded statement still surface? Unknown when no probe declares `must_not`. |
| `gold_survival` | higher better | **Is the knowledge still there at all?** |
| `as_of_recorded_accuracy`, `as_of_valid_accuracy` | higher better | Timeline scenarios only; see below. Unknown elsewhere. |
| `accuracy_<class>` | higher better | Strict accuracy per probe class, for scenarios that declare `class:`; see below. |
| `contested_claims`, `contradiction_edges` | lower better | Unresolved dissonance. |
| `noise_fraction` | lower better | Share of valid claims that `extract.IsJunk` — mnemos's own shipped narration filter — rejects. |
| `trust_decay_by_identity` | lower better | The ADR 0032 trust-decay ratio counted per belief identity, not per claim row. Unknown when too few beliefs approach the floor. |
| `vital_*` | lower better (`skill_coverage` higher; `trust_decay` descriptive) | The ADR 0019 vitals, mirrored in. `HealthUnknown` maps to unknown, never to 0. |
| `pathology_*` | lower better | The ADR 0019 integrity checks. |
| `valid_claims`, `invalidated_claims`, `status_*`, `events`, `relationships`, `evidence_links`, `mean_trust`, `db_bytes` | **descriptive** | Reported, never scored. |

`gold_survival` is the metric that makes the rest trustworthy. A process can
raise precision, cut dissonance and shrink the brain simply by deleting claims.
Only this metric notices, and `TestRun_ReportsRegressionWhenKnowledgeIsDestroyed`
asserts that a brain-destroying configuration is reported as a regression.

**Why `vital_trust_decay` is not scored.** The vital counts claim rows. The
untreated arms hold a duplicate and its original as two beliefs, and the
treatment holds one after a merge. Folding a not-yet-crossing duplicate away
shrinks the expected crossings, so the ratio rises with no belief any staler.
If the folded claim was the one crossing, the ratio falls with no belief any
fresher. `trust_decay_by_identity` follows each `merged_into` chain to its
survivor and measures every identity once, on its freshest live member: in the
treatment that is the survivor, which carries the union of the group's
evidence. A correct merge is therefore neutral, and a merge that keeps the stale
claim and loses the fresh evidence still reads as decay
(`TestIdentityTrustDecay_RealDecayStillRegisters`).

**Why the descriptive category exists.** Forgetting mechanically shrinks the
claim count and mechanically raises mean trust, because it deletes the low-trust
tail. Scoring "fewer claims" or "higher mean trust" as improvements would make
the harness report success for a process that did nothing but destroy
knowledge — for *every* forgetting configuration, including one that deleted the
whole brain. Those numbers are still shown, because they are how a reader
notices a brain was gutted.

## Two more tracks: temporal reconstruction and a raw-retrieval baseline

The A/B design answers one question: did the process improve the brain? Two
other questions need the same corpus and the same gold but are not process
questions, so they are added as tracks of a run rather than as new processes.
Neither changes how the A/B arms are built, measured or judged; the noise arm,
the `unknown` rules and the verdict are untouched.

### Temporal reconstruction (`timeline:` scenarios)

An incident review asks two different things about Tuesday:

- **What did the brain believe on Tuesday?** Transaction time,
  `mnemos.Query.RecordedAsOf` (ADR 0036 reconstructs status, validity, archive
  state, edges and events as they were recorded).
- **What is known now about Tuesday?** Valid time, `mnemos.Query.AsOf`, read
  with today's knowledge.

A timeline scenario replaces `corpus:` with phases. Each phase ingests its
documents through the same path as a corpus scenario, then applies curation
changes through the store's own write methods (`retract` → a status
transition, `archive` → ADR 0034 archive axis, `supersede` → ends valid time,
`contradict` → a contradicts edge). `as_of_probes` name a phase; the question is
asked about that phase's end.

**Why real time, not an injected clock.** Transaction time is stamped by the
repositories themselves (`time.Now()` in status history, validity history,
relationship and claim creation, on every backend). Back-dating rows or adding
a clock the shipped write path does not have would measure a reimplementation
of the history tables instead of the history the product records. So phases run
on the real wall clock a few milliseconds apart, and each boundary is read
strictly between the last write of one phase and the first of the next — the
method of `internal/query/recorded_as_of_test.go`. The instants differ between
runs; within a run every arm is a byte-copy of one seed measured against the
same instants, so the noise arm still has to reproduce the control exactly.
`TestRun_TimelinePhasesAreOrderedOnTheWriteClock` guards the ordering.

A document can set `valid_from: <phase>` in either direction, which is what
separates the two axes. An earlier phase is a fact learned late: the root cause
written down on Thursday was true since Monday's deploy. A later phase is a
change announced ahead: written in February, in force from March. That later
phase's start is not known when the document is ingested, so the seeder reserves
an instant on the write clock 2 s ahead, and when the phase begins it waits for
that instant and starts there. If the phases in between outrun the reservation,
seeding fails instead of placing March before February
(`TestPhaseStart_ReservationOverrunIsAnError`).

`supersede_by: {old, new}` ends the old claim's valid time where the successor's
begins, the cutoff `mnemos resolve --supersedes` uses. `supersede: <target>`
ends it at the moment the change is applied. Only `supersede_by` can express a
change recorded after it took effect, or one announced before.

| Metric | Direction | What it tells you |
|---|---|---|
| `as_of_recorded_accuracy` | higher better | Share of `recorded_as_of` probes answered correctly. |
| `as_of_valid_accuracy` | higher better | Share of `valid_as_of` probes answered correctly. |

A probe is correct only when `expect` is in the top results **and** no
`must_not` string is: returning Tuesday's diagnosis beside Thursday's root cause
is not a reconstruction of Tuesday. Both metrics are `unknown`, never 0, on a
scenario that asks nothing on that axis. They are measured on every arm, so a
process that corrupts history regresses them;
`TestRun_ReportsRegressionWhenAsOfAnswersBreak` asserts that a merge-everything
dedupe is reported as a regression.

### Probe classes

Any probe, ordinary or as-of, may set `class: <identifier>`. Each class present
gets an `accuracy_<class>` metric (higher better), scored by the strict rule
above over that class's probes, on every arm and every baseline. A scenario with
no classes emits none. Every report also carries the per-question `outcomes`
(rank of the expected text, whether anything forbidden came back, correct or
not) for the untreated Mnemos arm and for each baseline, so a class accuracy
traces back to the probes behind it.

`ownership_timeline.yaml` uses four classes: `current` (who owns X now),
`valid_history` (who owned X then, with today's knowledge), `known_history`
(what the brain believed then about who owned X then) and `change_why` (what
changed and why). Its world has a change announced ahead, one recorded late,
one corrected afterwards (retract), and two uncurated stale restatements: one
verbatim, one reworded.

### Raw-retrieval baseline

Until this track the harness only compared Mnemos with itself. The baseline
answers "do Mnemos's semantics beat what you would get without them?" on the
same corpus:

- **Corpus:** the seed's raw event texts, read from the store before warmup —
  exactly the documents every arm was built from, nothing derived from them.
- **Chunks:** sentences (lines, then `.`/`!`/`?` followed by whitespace), the
  granularity rule-based extraction produces claims at. Whole-document chunks
  would carry superseded and current statements together.
- **`raw_bm25`:** BM25, k1=1.2, b=0.75, lowercase alphanumeric tokens, no
  stemming, no stop words, no tuning. Chunks sharing no term are not returned.
- **`raw_bm25_recency`:** the same score times `1 + r`, where `r` is the
  document's chronological rank in [0, 1] (newest ×2). Rank, not age, so it
  behaves the same on a two-year corpus and a millisecond timeline.
- **Time travel:** the filter a timestamped log can apply. A `recorded_as_of`
  question sees documents ingested by T; a `valid_as_of` question sees
  documents whose facts were observed by T.

Both baselines are put through the identical battery as Mnemos recall
(`runBattery`): the same question list in the same order, the same top-k, the
same instants, the same gold and the same arithmetic.
`TestRun_BaselineAnswersTheIdenticalQuestionSet` asserts the question lists and
sample sizes match.

They are compared with the **untreated** arm only: the baseline has no process,
so comparing it with the consolidated arm would mix the process effect into the
baseline effect. Only metrics a baseline can have are compared (answer quality,
forbidden hits, as-of accuracy); census, vitals and `gold_survival` describe a
store the baseline does not have. Verdicts are `mnemos-better`,
`baseline-better`, `tie` or `unknown`, summed per baseline in
`baseline_summary`. They never feed the A/B `summary`, the scenario verdict or
`-fail-on-regression`: a baseline win is a finding about retrieval semantics,
not a regression caused by a process, and the two must not hide each other.

**Cost.** Every scenario also reports what each system spent (`cost` in the
JSON, the COST block in the table): build time (the Mnemos ingest path summed
over documents, against chunking and tokenising for a baseline), curation time,
stored bytes (the untreated arm's SQLite file, schema and history tables
included, against the baseline's in-memory chunk and token bytes), row counts,
and per-question latency (mean, p50, max). These are wall-clock numbers that
vary run to run, so they are not metrics, feed no verdict and are not checked by
the noise arm. They are there so "an accuracy gain at acceptable cost" can be
read off one table.

What the comparison cannot say is in the report's limitations. In short: the
baseline sees documents only, so a timeline's structured curation has no
textual trace for it (a real log would carry a retraction note); both sides are
lexical here (stub embedder vs BM25), so this says nothing about dense-retrieval
RAG; and timeline phases are milliseconds apart, so freshness and decay see no
age difference between them.

## What it does **not** measure

The full list ships inside every report, because a JSON file gets pasted into an
issue or a prompt without its documentation. In short:

- **The LLM path.** Extraction is rule-based, no LLM is configured. Says nothing
  about LLM extraction, LLM causal detection, grounded generation, or
  `consolidate --clear-session-noise`.
- **Real embeddings.** The embedder is a deterministic hashed bag-of-words stub.
  It sees exact and near-lexical restatements and misses true paraphrases, so
  reported dedupe behaviour is a **lower bound** on production.
- **Scale.** Scenarios hold tens of claims, not thousands.
- **Repetition.** One consolidation pass, not a series of nightly ones.
- **Cross-tenant promotion.** `consolidate --promote` (ADR 0011) is out of scope.
- **Downstream utility.** These are properties of the store and of retrieval.
  Whether an agent using this brain made a better decision needs a task
  benchmark, not a store diff.
- **Statistical significance**, which would be meaningless: the run is
  deterministic, so a delta is exact for its scenario and carries no confidence
  interval across scenarios. The noise arm is the empirical substitute.

## Writing a scenario

Scenarios live here as `*.yaml`. The format deliberately echoes
`../retrieval.yaml`'s seeds/query/gold idiom, but the unit of work differs:
`retrieval.yaml` compares **rankers** over one store, this compares **stores**
that a process has or has not been applied to.

```yaml
id: my_scenario
description: One sentence on the question this scenario settles.

corpus:
  - id: s1
    source: session-label        # optional; defaults to the doc id
    age_days: 400                # back-dates the event; see below
    text: |
      The service runs on PostgreSQL 16.

warmup:                          # optional; creates state for the decay stages
  - which database does the service use

probes:
  - id: p1
    query: which database does the service use
    expect: PostgreSQL 16        # case-insensitive substring
    must_not:                    # optional
      - MySQL

process:                         # the ConsolidateOptions under test
  dedupe_threshold: 0.92
  forget_below_trust: 0.45
  decay_associations: true
```

A timeline scenario (see `incident_timeline.yaml`) sets `timeline:` instead of
`corpus:` and may add `as_of_probes:`:

```yaml
timeline:
  - phase: monday
    docs:
      - id: pool-50
        text: The payments connection pool size is 50.
  - phase: thursday
    docs:
      - id: root-cause
        valid_from: monday          # facts true since Monday, written Thursday
        text: ...
    changes:                        # exactly one op per entry; each target must
      - supersede: pool size is 50  # match exactly one current claim
      - retract: disk saturation
      - archive: restart the payments database first
      - contradict: {from: pool change starving, to: disk saturation}
      - supersede_by: {old: pool size is 50, new: pool size is 200}  # ends at new's valid_from

as_of_probes:
  - id: believed-tue
    class: known_history            # optional; scores accuracy_known_history
    recorded_as_of: tuesday         # or valid_as_of — exactly one
    query: what caused the payments outage
    expect: disk saturation
    must_not: [starving]
```

Three things to get right:

- **`age_days` is not cosmetic.** Trust freshness decays on a 90-day half-life.
  With everything stamped "now", the forgetting stage can never fire and the
  scenario measures nothing while still printing a table.
- **`warmup` is what makes the decay stages measurable.** On a freshly-ingested
  brain no association edge is above base and nothing is inhibited, so
  `decay_associations` and `decay_inhibition` report zero — "untested", which
  reads identically to "inert" and is a much weaker claim.
- **Expectations match text, not claim ids.** Claim ids are minted per ingest
  and are not stable across runs.

Unknown YAML keys are rejected. A misspelled process key would otherwise be
silently ignored and the scenario would test a different process set than its
author wrote, while still producing a credible-looking report.

## Reading a result

Read in this order, which is how the human report is laid out:

1. **Validity.** If measurement was not deterministic, stop; nothing below is
   attributable.
2. **Activity.** Did the process mutate anything? Zero means `inert` — the stage
   was not exercised, which is not the same as "the stage is neutral".
3. **The rows.** Deltas at or below the `1/n` resolution floor are annotated;
   on a six-probe suite that is one probe moving, not a trend.
4. **`gold_survival`.** Any improvement bought alongside a survival regression
   was bought by deleting knowledge.
5. **The verdict**. `mixed` means read the rows.
6. **The baseline table**, separately. It compares the untreated arm with raw
   retrieval and says nothing about the process; read `baseline-better` rows as
   loudly as `mnemos-better` ones.
