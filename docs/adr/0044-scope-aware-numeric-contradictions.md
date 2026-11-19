# ADR 0044: Scope-aware numeric contradictions

- **Status:** Accepted; implemented.
- **Date:** 2026-11-19
- **Deciders:** Felix Geelhaar
- **Builds on:** ADR 0040 (supersession readings in relate), ADR 0042
  (deterministic vs LLM reconciliation)
- **Adds:** `relate/v6`: a scope check in the numeric contradiction detector
  (`internal/relate/numeric_scope.go`)

## Context

ADR 0040 §Limitations said the numeric detector could not see scope: two
counts of the same noun about different branches, sessions or repositories
were read as one quantity. ADR 0042 measured the cost. Eleven of the
deterministic reconciliation arm's 13 false positives on the test split were
this one shape, for example "the main branch has 12 failing tests" against "the
release branch has 3 failing tests". Because the rules were confident, the
hybrid arm kept every one of them. ADR 0042 recommended fixing this in the
rules, without a model.

The detector already requires the two claims to share most of their words.
What it lacked was a way to tell which thing each number was about. The frame
detector (ADR 0040) already rejected a tail that named a different environment
(`tailScopesDiffer`). Nothing similar guarded numbers.

## Decision

Before the numeric detector proposes a contradiction, `numericScopesDiffer`
checks whether the two claims are about different scopes. If they are, the
detector proposes nothing and the pair falls through to the other detectors.
The check never adds an edge. Two claims are about different scopes when any of
these holds.

### 1. A qualifier is swapped in place

Each claim is reduced to its qualifiers: its content words in sentence order,
normalised by `conceptOf`. Numbers, spelled numbers, known units, stop words and
restatement filler ("now", "currently", "raised", "back", "about") are dropped.
An all-capitals word is kept even if it is a stop word, so "US" is a region and
not "us". "it's" is read as "it". The two sequences are then aligned on their
longest common subsequence.

A **slot** is the stretch between two shared words, or before the first one, or
after the last. The claims differ in scope when some slot holds, on **both**
sides, a word the other claim does not contain at all:

- "**main** branch" / "**release** branch": the slot before "branch";
- "loads in 1.2 seconds on **desktop**" / "… on **mobile**": the slot after
  "loads";
- "**Go** service" / "**Python** service", "**free** plan" / "**pro** plan".

A supersession keeps its edge, because it changes the number and at most
inserts words: "the pool size is 50" / "the pool size is now 200", or "eleven
commits on the branch, tree clean" / "the audit is closed — 13 commits on the
branch". Inserted words in **different** slots are not a swap. Neither is a
shared word that moved ("74 GB of disk is free" / "92 GB free on the disk").

The alignment is walked in both argument orders. A tie can be walked two ways,
and the batch and incremental passes see a pair in opposite orders, so this
keeps the verdict symmetric.

### 2. An environment differs

One claim names an environment that the other does not: prod, staging, dev, qa,
local, ci and the rest of the frame detector's vocabulary. This is the frame
detector's rule, generalised into `scopesDiffer(a, b, vocab)`, which both
detectors now call. "test" and "testing" are left out for numbers, because
tests are the noun numeric claims count most often.

### 3. A label number differs

Some numbers name something rather than measure it: "Sprint 14", "Release 3",
"PR #12", "Wave 5", "Day 6", or a calendar year. Such a number follows a label
word from a fixed list. When both claims carry the same label word with
different values, they are about different things. A label counts only when its
claim asserts another number beside it. In "The deploy uses version 16" the
version is the asserted value, so it is not a label, and "Postgres 15" against
"Postgres 16" still contradicts.

`ModelVersion` is `relate/v6`. `relate --prune-stale` re-runs the current
detectors on every stored contradiction, whatever its stamp. It therefore drops
the v5 numeric edges this rule no longer produces and keeps the same-scope
ones. `TestPruneStale_DropsScopeBlindNumericEdges` covers that path.

## Measured effect

All four measurements were run on `main` (relate/v5) and on this change with
the same commands.

**Reconciliation, deterministic arm** (`go run ./tools/brainbench -mode
reconcile`, ADR 0042's set). This set's test split had already been seen when
the rule was written. The rule was designed from the scope-blind category and
from real-brain removals, and nothing was tuned against the split's score.

| split | before P / R / F1 | before TP/FP/FN/TN | after P / R / F1 | after TP/FP/FN/TN |
|---|---|---|---|---|
| test (151) | 0.667 / 0.366 / 0.473 | 26/13/45/67 | 0.929 / 0.366 / 0.525 | 26/2/45/78 |
| dev (50) | 0.917 / 0.647 / 0.759 | 11/1/6/32 | 1.000 / 0.647 / 0.786 | 11/0/6/33 |
| all (201) | 0.725 / 0.420 / 0.532 | 37/14/51/99 | 0.949 / 0.420 / 0.583 | 37/2/51/111 |

All 11 scope-blind numeric false positives on the test split are gone: ten
`hw-scope-*` pairs and `hw-bait-06` ("Version 2 … adds pagination" / "Version
3 … adds filtering"). So is the dev split's one (`hw-scope-13`). No
contradiction was lost, so recall is unchanged. The scope-blind category goes
from 9/20 correct to 20/20, and the hand-written compatible pairs given an edge
go from 12/51 to 0/51. The two test false positives left are not about scope:

- an entity pair ("frontend uses TypeScript" / "backend uses Go");
- an agreeing paraphrase whose versions a guessed unit misaligns
  (`bb-openssl`).

**Retrieval ablation** (`go run ./tools/brainbench -mode ablation`). No cell
changed. `mnemos_default` is still 41/44 at budget 32 and 43/44 at budgets
64–2048.

**`make brain-eval`.** Every verdict and every contradiction-edge count is
unchanged:

| scenario | contradiction edges |
|---|---|
| evidence_retraction | 1 |
| incident_timeline | 2 |
| noisy_capture | 0 |
| ownership_timeline | 5 |
| redundant_sessions | 0 |
| stale_and_superseded | 4 |

`stale_and_superseded` keeps the `p-limit` edge ("one hundred" / "one thousand"
requests per minute).

**Real brain.** I exported the 46,720 live claims of a production brain
read-only. I passed the 17,069 claims that contain a digit or a spelled number
through `DetectIncremental` against all of them, with the per-claim cap off, on
`main` and on this change. Edges are counted as distinct text pairs:

- Numeric edges fell from 345 to 244. 101 were removed and none was added.
  Every other detector's count is unchanged.
- By rule, 96 removals had a qualifier swap (some together with a label or an
  environment), 4 only a label, and 1 an environment and a label.

I judged each of the 101 removed edges by hand:

| judgement | edges | examples (paraphrased) |
|---|---|---|
| correct: different scopes | 79 | test-file counts of different packages in one monorepo (33 pairs); key counts of two projects; test counts of two suites; sizes of two different caches; "Wave 3" / "Wave 4" plans; two PRs' CI check counts; two experiments' seed ranges; runner lines from different sessions |
| correct: the claims agree | 11 | the same DNS commands restated; the same 48 GB cache "rebuilds itself" / "regenerates"; the same 111 ms first request |
| wrong: a supersession lost | 4 | free disk space on one disk at two times ("281 MB" / "fixed: 281 MB → 27 GB"; "114 GB" / "130 GB free"); one metric at two readings (0.055 / 0.097); one cache at two sizes |
| ambiguous | 7 | coverage figures that may be one project at two times; two cleanups on one day; two scans' finding counts |

On the removed edges, this is a precision of 90/101 (0.89) counting ambiguous
pairs as errors, or 90/94 (0.96) excluding them. The four lost supersessions
are snapshots of one machine's status whose wording varies between sessions:
the swap rule finds "hair" against "issue", or "critical" against "fixed", in
the same slot. ADR 0040's real-brain status changes ("all three" / "all five
lanes", "two" / "three slices still running", "ten" / "35 minutes") are
covered by `TestNumericScope_SameScopeStillContradicts`, paraphrased, and still
contradict.

## Consequences

- The numeric detector now fails toward silence on scope as well as on bounds,
  locators and identifiers. A pair the scope check rejects can still become a
  `supports` edge or reach the entity and temporal detectors.
- Session narration loses most of its numeric hubs. On the real brain, one
  package's test-file count had contradicted every other package's.
- When the wording differs in the same position, a few genuine changes of one
  quantity over time lose their edge (4 of 101 removals above). Those claims
  now coexist in recall, as all paraphrased supersessions already did.

## Limitations

- **Lexical, not syntactic.** A qualifier is any content word in the same
  aligned slot. The rule does not parse which word modifies the counted noun,
  so an unrelated word swapped in a trailing clause also reads as a different
  scope ("12 failing tests, see the logs" / "3 failing tests, see the
  dashboard").
- **Synonyms read as different scopes.** "user" / "customer" or "repo" /
  "repository" in the same slot is a swap. Inflections are normalised by
  `conceptOf`; synonyms are not.
- **One-sided qualifiers do not scope.** "The API runs 12 replicas" against
  "The API runs 3 replicas in eu-west" still contradicts, unless the extra
  word is a known environment. A bare restatement and a qualified one are
  treated as the same subject.
- **The label list is fixed.** An identifier after a word outside the list
  ("Experiment 061") is read as a measurement unless a swap catches it.
- **The test split has been seen.** The reconciliation numbers above are not a
  held-out estimate. ADR 0042 §Decision 5 still applies: a fresh labelled sample
  is needed before quoting a precision for the real distribution.
- **Real-brain judgements are one labeller's,** made on one brain dominated by
  development-session narration.
