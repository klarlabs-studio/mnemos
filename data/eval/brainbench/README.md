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

**The write clock is simulated, not back-dated.** Transaction time is stamped
by the repositories themselves (status history, validity history, relationship
and claim creation). Back-dating rows would measure a reimplementation of the
history tables instead of the history the product records, so the seeder lets
the shipped write path stamp and reads each phase boundary from the same clock,
strictly between the last write of one phase and the first of the next — the
method of `internal/query/recorded_as_of_test.go`.

That clock is `internal/clock`, the one seam through which the library reads
the wall clock as data. Production gets `time.Now`; a brainbench scenario runs
on a simulated clock that starts at a fixed instant (2026-01-01T00:00:00Z) and
advances one microsecond per read, and a phase gap is a clock advance rather
than a sleep. Before it existed (issue #396), the same scenario gave different
numbers on every run: claims that should score identically were rescored a few
microseconds apart, the freshness decay between those instants decided their
ranking order, and noisy_capture's `answer_mrr` moved between 0.55 and 0.383.
Durations — `duration_ms` and the cost report's latencies — still use the real
clock, since they measure the machine. `TestRun_IsReproducibleAcrossRuns` and
`TestRunAblation_IsReproducibleAcrossRuns` require every other field of the
report to be identical across fresh runs, and
`TestRun_TimelinePhasesAreOrderedOnTheWriteClock` guards the phase ordering.

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

### Evidence retraction (`retraction:` scenarios)

Hypothesis (ADR 0037): dependency maintenance (withdrawing a source and
propagating that to what rests on it) reduces the unsupported beliefs a brain
keeps holding. Pass condition: **fewer unsupported beliefs survive than on raw
retrieval whose retraction deletes the document's chunks, and no fewer
supported beliefs do.** That baseline is the honest strong-simple one. It is
what a document store with a search box does, and it keeps every copy,
restatement and conclusion in other documents.

The track forks its own arms off the warmed seed and leaves the A/B arms alone:

- **reference**: not retracted;
- **retracted**: the episode is retracted through `Memory.RetractEvent`, the
  path every surface shares;
- **noise**: a second retracted copy that must reproduce the retracted arm,
  or the hypothesis is `unknown`;
- `raw_bm25` and `raw_bm25_recency` over the event texts **minus the retracted
  document**.

Gold beliefs carry a role. `original`, `copy` and `derived` are dependents and
should stop being believed. `corroborated` (stated by the retracted document
and independently elsewhere) and `independent` must survive. Load-time
validation checks each role against the documents, so an `original` has to
appear in the retracted document and a `copy` must not. Before anything is
retracted, every gold text must match a live claim. A gold text that
extraction never produced would otherwise score as withdrawn without any
work.

| Metric | Direction | What it tells you |
|---|---|---|
| `unsupported_surviving` | lower better | Share of dependents still held: a live claim, or a visible chunk for the baseline. The hypothesis' metric. |
| `unsupported_surfaced` | lower better | Share of dependents returned by at least one retraction query. |
| `supported_surviving` | higher better | **Anti-gaming.** Withdrawing everything near the source wins the row above and loses here. |
| `supported_surfaced` | higher better | Share of must-survive beliefs still returned. |
| `impact_precision`, `impact_recall` | higher better | What the retraction withdrew, against the dependent gold. Unknown on the reference arm, which has no report. Precision is unknown when nothing was withdrawn. |
| `impact_withdrawn`, `impact_rescored` | descriptive | Beliefs (or chunks) withdrawn, and beliefs kept and rescored. |

The effect rows (retracted vs reference) count toward `-fail-on-regression`.
A retraction that loses a supported belief is a regression caused by the
operation under test
(`TestRun_RetractionReportsRegressionWhenCorroboratedBeliefsAreDestroyed`).
The baseline rows and the hypothesis are findings and do not gate anything.

**How dependents are recognised, which is the finding.** Mnemos follows
evidence links and declared source dependence (ADR 0038). Ingest records that
an episode depends on another when it says so: a citation marker whose phrase
names a stored source ("Source: the libfoo vendor advisory." names
`vendor-advisory`), a link, or provenance metadata. A belief is withdrawn when
it has no live evidence independent of the retracted episode, and evidence
from an episode that depends on it is not independent. A belief with
independent evidence left is kept and rescored. Three consequences follow, and
the track measures them as they are instead of special-casing the scenario:

- A **cited verbatim copy** is folded onto the original belief at ingest, but
  its episode depends on the retracted one, so the belief falls with the
  source. In `evidence_retraction.yaml` the blog's copy of the false claim is
  `--W-`. The report's `belief_paths` shows it withdrawn with `via:
  [advisory]`, and `dependent_episodes` lists the blog with the citation line
  that justified it.
- An **uncited verbatim restatement** is corroboration. Nothing tells it from
  a copy except a citation, and similarity is deliberately not used. The
  release notes repeat the release date in the advisory's words and cite
  nothing, so the date is kept and rescored (`SQ-R`).
- An **uncited paraphrase** or an **uncited conclusion** in another document
  is a separate belief with apparently independent evidence, and the
  retraction does not reach it. The newsletter says "according to the vendor",
  which names no stored source. The checkout review cites nothing. Both stay
  `SQ--`.

On that scenario Mnemos now holds **fewer** unsupported beliefs than the
baseline: `unsupported_surviving` 0.4 vs 0.6 (2 of 5 dependents survive, against
3), with `supported_surviving` tied at 1.0. The hypothesis is **supported**. The
whole difference is the one cited copy. Detection catches nothing else, and
the two surviving dependents are exactly the uncited ones above. Mnemos's
report stays more precise (1.0 vs 0.75), because the baseline also deletes the
corroborated sentence's chunk, and recall now ties (0.6). Before ADR 0038 the
two tied at 0.6, and the blog's copy kept the false claim alive (`SQ-R`).

### Shifting world (`shifting_world:` scenarios)

Hypothesis (ADR 0049): after the world changes, beliefs that
surprise-triggered revision acts on are corrected **faster** than with the
prediction loop that existed before it, with no stable belief lost. Pass
condition, fixed before the first run: a strictly lower
`changed_correction_lag` than the current loop, and `changed_accuracy`,
`stable_accuracy` and `stable_disturbed` no worse.

The track replays one scripted agent episode on its own copies of the warmed
seed, each on a simulated clock restarted at the same instant. A round is a
day: documents arrive, the agent recalls and takes each scheduled decision
only if **every belief it rests on came back from that recall**, the outcome
is observed and reconciled, the arm sleeps (the same consolidation options on
every arm), and every probe is asked. Because a decision needs its premises
recalled, a corrected arm stops acting on the stale picture by itself.

| Arm | What the agent can record |
|---|---|
| `evidence_only` | The decision and its beliefs. No prediction. |
| `current_loop` | The decision, and one expectation on **each** rested-on belief, observed and reconciled. The sleep's ForgetRefuted then archives each refuted belief. Attaching a failed outcome to the decision does the same. It is the fastest use of the old API, which makes it the honest control for a speed claim. |
| `revision` | The decision with its expected outcome (`Decision.Expectation`), observed with `RecordDecisionObservation`; reconciliation attributes the surprise and revises. |

Two noise arms replay `current_loop` and `revision`; each must reproduce its
twin or the hypothesis is `unknown`.

| Metric | Direction | What it tells you |
|---|---|---|
| `changed_accuracy` | higher better | Strict accuracy of the changed probes over all rounds (expect returned, stale state not). |
| `changed_corrected` | higher better | Share of changed probes correct from some round to the end. |
| `changed_correction_lag` | lower better | Rounds from the one whose documents carry the new state to the first round a changed probe stays correct. Unknown when a probe never settles. |
| `stable_accuracy` | higher better | **Anti-gaming.** Accuracy of the beliefs that stayed true. Archiving everything a surprise touched wins the rows above and loses here. |
| `stable_disturbed` | lower better | Share of stable beliefs that end retired or not active (a flag counts). |
| `decisions_taken`, `surprises`, `revisions`, `archived_by_sleep` | descriptive | What each loop did. |

`shifting_world.yaml` changes two facts (the payments timeout, evidence in
r2; the search refresh interval, evidence in r3), keeps two co-rested facts
true, and gives the ledger one outlier outcome in r2 that later outcomes
confirm. Each stale fact is stated twice, so it outranks the single document
that reports the new state. The result, deterministic across runs:

| Metric | evidence_only | current_loop | revision |
|---|---|---|---|
| `changed_accuracy` | 0.00 | 0.70 | 0.70 |
| `changed_correction_lag` | unknown (never) | 0.00 | 0.00 |
| `stable_accuracy` | 1.00 | 0.07 | 1.00 |
| `stable_disturbed` | 0.00 | 1.00 | 0.33 |
| `archived_by_sleep` | 0 | 5 | 0 |

The hypothesis is **not supported**: revision corrects the changed beliefs
in the same round as the current loop, not sooner. Read the reason before the
verdict. The current loop archives every belief a failed decision rested on,
at the first sleep after the surprise, so it cannot be beaten on speed. It
pays for it: by round 2 it has archived both co-rested facts and the ledger
belief that one outlier refuted, and answers 1 of 15 stable questions.
Revision keeps all three answerable. It flags them contested on the first
surprise, supersedes the stale fact when the newer contradicting document
arrives, and clears the co-rested fact's flag once the stale fact explains
the surprise. The ledger belief stays flagged (`stable_disturbed` 0.33),
because nothing explained its outlier, and later confirmations leave it alone
by design. Against `evidence_only`, both loops are faster: evidence alone
never retires the better-corroborated stale fact, and the user-mode probe keeps
returning both values.


## Retrieval ablation (`make retrieval-ablation`)

A third mode, separate from the A/B verdict, asks whether Mnemos's ranking puts
more *useful* beliefs into a fixed token budget than similarity alone
(`go run ./tools/brainbench -mode ablation [-budgets 64,256]`). Every scenario
question is answered under each policy on its own copy of the warmed seed, then
the ranked list is packed into each budget (32 to 2048 tokens) with the
Context Block's chars/4 estimate and stop rule. A question succeeds when the
packed context holds its `expect` text and none of its `must_not` texts.

The policies are the shipped agent recall (`mnemos_default`), similarity only
(dense+BM25 with every other component off, plus dense-only and lexical-only
variants), one leave-one-out row per ranking component, the opt-in priming and
hop expansion, and the two raw BM25 baselines. Every Mnemos policy is a
`query.AnswerOptions` passed to `query.Engine.Recall`; components are switched
off through `query.Ablation`, which no surface exposes. A test fails if an
`Ablation` field gains no leave-one-out row.

Read the per-question grid, not only the totals: the suite has 23 questions,
and in the current run the whole gap between the default and similarity only is
four of them, in two scenarios.

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

A scenario adds the retraction track with a `retraction:` block (see
`evidence_retraction.yaml`):

```yaml
retraction:
  episode: advisory               # a document id of this scenario
  reason: vendor withdrew the advisory
  limit: 3                        # top-k of the queries (0 = 10)
  queries: [is CVE-2026-1234 fixed in libfoo 2.3]
  gold:                           # roles: original | copy | derived (dependents),
    - text: release fixed the heap overflow   #        corroborated | independent (must survive)
      role: original
    - text: released on 3 March 2026
      role: corroborated
```

A scenario adds the shifting-world track with a `shifting_world:` block (see
`shifting_world.yaml`):

```yaml
shifting_world:
  sleep: {forget_refuted: true, credit: true}   # every arm, every round
  rounds:
    - id: r1
      docs: []                       # evidence arriving this round
      decisions:
        - id: retry-budget
          recall: payments API timeout and payment retries   # asked first
          rests_on: [timeout is 30 seconds, retries are idempotent]
          predicted: 30
          tolerance: 5
          observed: 5                # the world's outcome
  probes:
    - id: timeout-now
      role: changed                  # or stable
      evidence_in: r2                # round whose docs carry the new state
      query: what is the payments API timeout
      expect: timeout is 5 seconds
      must_not: [timeout is 30 seconds]
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
7. **The retraction track**, for a scenario that has one. Read its per-belief
   table before the hypothesis line: it shows which dependent survived, and
   whether it was rescored (kept on evidence Mnemos took as independent) or
   never reached.
