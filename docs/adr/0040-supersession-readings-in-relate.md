# ADR 0040: Relationship detection reads spelled-out counts and manner swaps

- **Status:** Accepted; implemented.
- **Date:** 2026-11-18
- **Deciders:** Felix Geelhaar
- **Builds on:** the relate/v4 contradiction precision rules
  (`internal/relate/contradiction.go`), ADR 0039 (trust and freshness rank
  recall)
- **Adds:** `relate/v5`: spelled cardinals in the numeric detector
  (`internal/relate/numberwords.go`) and the `frame` contradiction detector

## Context

ADR 0039 put the current statement first in `stale_and_superseded/p-limit`
and `p-deploy`. Both still failed at every token budget large enough to hold
two statements, under every policy, because ranking orders a context and
cannot remove anything from it. Only curation removes a superseded statement,
and agent-mode contradiction resolution drops the loser only when a
contradiction edge links the two. `p-region` passed for exactly that reason.

No edge was detected in the other two pairs:

- "The payments API rate limit is **one hundred** requests per minute" and
  "… **one thousand** requests per minute". The numeric detector read digits
  only, so the two claims differed in one word that was not a value token.
- "Deployments are performed **manually** by an engineer running the release
  script" and "Deployments are performed **automatically** by the release
  pipeline on merge to main". Manual and automatic were not an antonym pair.
  Even as one, the value detector accepts an antonym swap only when at most
  two concept tokens differ on each side. The manner word brings its own agent
  and mechanism, so these differ in four tokens each and align at 0.27.

## Decision

### 1. Spelled cardinals are numbers

`spelledNumerics` reads "one hundred", "twenty-five", "twenty five" and "two
hundred and fifty", and `extractNumerics` returns them beside the digit
literals.

A spelled number is almost always a count of the noun that follows it, so that
noun is its unit (`raw:<noun>`, or a known unit's family: "ten minutes" is
time). The value is marked `counted`. When two single-number claims meet, a
counted noun and any other unit are different quantities, not a disagreement:
"five commits" says nothing about "693 tests", and "nine mutants" nothing
about "100% coverage". The guessed-unit rule for digits is unchanged.

Not read as counts:

- a lone "one", which is usually a determiner or pronoun ("one of the
  reviewers");
- ordinals ("tenth");
- a number with no noun directly after it ("all three are", "five of the
  six");
- a partitive's denominator ("of the six fail");
- a run of number words ("three four five"), which is a list.

### 2. The frame detector

Two claims of the same polarity contradict when:

- their concept tokens, in sentence order, are identical for at least two
  tokens (a subject and a predicate);
- the next token is an exclusive **manner** pair: manual/automatic,
  manually/automatically, synchronous/asynchronous (and the -ly forms),
  sync/async;
- neither tail names an environment (prod, staging, dev, test, qa, local, ci,
  …) that the other does not.

The strength is `0.7 × (0.5 + 0.5 × (frame + 1) / shorter claim)`. The edge
records `relate/v5:frame`.

Only manner pairs qualify:

- Ordering and comparison antonyms coexist about one subject: "tests run
  before the merge" and "tests run after the deploy" are both true.
- State pairs ("enabled"/"disabled") usually carry a scope in the tail that a
  frame cannot compare.

manual/automatic and manually/automatically also join the general antonym
list, so the existing value detector catches the tight form ("Deploys are
manual" vs "Deploys are automatic").

## Measured effect

**Retrieval ablation** (`make retrieval-ablation`, 17 policies × 7 budgets ×
44 questions). 142 cells changed, every one a failure turning into a success:

| policy | budget 32 | budgets 64–2048 |
|---|---|---|
| `mnemos_default`, before | 40/44 | 41/44 |
| `mnemos_default`, after | 41/44 | 43/44 |

`p-limit` and `p-deploy` now pass at every budget under every policy that
keeps contradiction resolution. `minus_contradiction_resolution` and the raw
baselines still fail them, which places the gain in the new edges. No cell got
worse.

**`make brain-eval`.** Every verdict is unchanged. `stale_and_superseded`
seeds 4 contradiction edges instead of 2.

**Real brain.** I exported the 46,575 live claims of a production brain and
passed every claim containing a number word or a manner word (4,659) through
`DetectIncremental` against all of them, on main and on this change:

- The frame detector added no edges.
- The numeric detector added edges on 8 distinct text pairs (14 edge rows; the
  brain holds some texts twice). Six are genuine disagreements, mostly status
  that moved on:
  - "all three lanes are back" vs "all five lanes are back";
  - "two slices are still running" vs "three slices are still running";
  - eleven vs 13 commits on the same branch;
  - "takes about ten minutes" vs "will take about 35 minutes";
  - two vs all four templates building;
  - four vs three images being signed.
- Two are wrong: commit counts on what are probably different branches, and
  eight vs fifteen PRs merged at different scopes.
- Two numeric edges went away: "two migrations claim 0027" vs "two migrations
  numbered 0029", which are both true.

That is a precision of about 0.75 on the added edges, at a volume of roughly 1
edge per 3,000 claims.

## Consequences

- `ModelVersion` is `relate/v5`. The change only adds readings, so
  `relate --prune-stale` keeps every v4 edge it would have kept.
- A superseded count or manner now reaches contradiction resolution, which
  drops the loser on agent-mode recall. That is the curation step ADR 0039
  said ranking could not supply.
- Spelled counts in session narration ("three slices are still running")
  produce edges between status reports. Most are true disagreements in time,
  where the newer statement should win.

## Limitations

- **Scope is still invisible.** Two counts of the same noun about different
  scopes (branches, sessions, repositories) are read as one quantity. The
  numeric detector's alignment rules limit this, but they do not remove it.
  ADR 0044 (`relate/v6`) adds a scope check to the numeric detector: a
  qualifier swapped in place, a differing environment, or a differing label
  number ("Sprint 14") now marks two numbers as being about different scopes.
- **Manner only.** Other ways a statement is replaced, such as a new owner, a
  new tool, or a changed policy with no antonym or number, still need an
  explicit edge or the semantic path (#444).
- **Partitives and lists are dropped, not read.** "Five of the six" yields no
  count at all, so a change to it is not detected.
