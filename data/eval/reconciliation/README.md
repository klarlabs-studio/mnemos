# Reconciliation pairs

The labelled pair set behind ADR 0042 (deterministic vs LLM reconciliation).
Each pair is two beliefs and one label: does one contradict the other, or are
they compatible? `go run ./tools/brainbench -mode reconcile` scores the
deterministic relate classifier on it, and, with `-llm-model`, a local model
alone and a hybrid of the two.

## Labels

- **contradicts**: the two cannot both be true of the same subject, in the same
  scope, at the same time. This covers a denial ("succeeded" / "did not
  succeed"), a different value for one thing (a number, version, name, owner,
  tool, place, time, manner or policy), and supersession: one statement says the
  state the other describes was replaced or no longer holds ("on-call moved
  from Alice to Bob" against "the on-call is Alice"). Which side is newer is not
  scored.
- **compatible**: both can hold. They agree, restate each other, one adds
  detail, they concern different subjects, scopes, environments, branches,
  versions or periods, or they are unrelated. A text that asserts nothing (a
  header, a fragment, narration) is compatible with anything.

Rules applied when a pair was borderline:

1. Scope words decide. "in staging" against "in production", "the main branch"
   against "the release branch", "on Monday" against "on Tuesday" are different
   scopes, so differing values are compatible.
2. An unscoped attribute has one value. "The latency is 100ms" against "The
   latency is 500ms" contradicts, even though a real system could be read as
   having both at different times.
3. A role that has one holder (CEO, primary on-call, owner, default branch, the
   CI system, the processor for "payments") contradicts when two holders are
   named. A role that can have several (reviewer of a PR, tech lead of
   different teams) does not.
4. A negated antonym agrees ("not enabled" / "disabled").
5. Pairs whose meaning stayed ambiguous after these rules were left out rather
   than labelled by coin flip (for example "The project has 5 engineers" / "We
   hired 2 more", and code-line pairs from the relate tests).

Every pair carries a one-line `rationale`. No label was produced or checked by
asking a model.

## Provenance

| source | pairs | what was taken |
|---|---|---|
| `data/eval/relationship_detection.yaml` | 14 | every case; rel-011's three claims give three pairs |
| `data/eval/contested_detection.yaml` | 9 | every two-sentence case except two ambiguous or duplicate ones |
| `data/eval/brainbench/*.yaml` | 35 | supersession pairs (stale_and_superseded, ownership_timeline, incident_timeline) and their compatible neighbours (redundant_sessions, evidence_retraction, noisy_capture) |
| `internal/relate/*_test.go` | 46 | supersession_test.go, contradiction_test.go, false_positive_regression_test.go, numeric_test.go fixtures |
| hand-written (`source: hand`) | 97 | ADR 0040 blind spots and their compatible counterparts, below |

Where a source's expectation described a detector's behaviour rather than the
meaning, the label here follows the meaning and the rationale says so:
`cd-003` (latency 100ms / 500ms, which the source expects to stay `active`)
and `rt-merged-unmerged` ("merged cleanly" / "green and unmerged", which the
source only asserts the numeric path ignores) are labelled `contradicts`.

The hand-written pairs target the blind spots ADR 0040 §Limitations names, each
with compatible counterparts that share the same subject so precision is
measured too:

- **scope-blind** (15, compatible): the same counted noun about different
  branches, repositories, sessions, regions, plans, endpoints or machines, and
  five same-scope counterparts that do contradict.
- **supersession without antonym or number** (28, contradicts): a new owner
  (`supersession-owner`), a new tool (`supersession-tool`), a changed policy
  (`supersession-policy`), and ten compatible pairs that share their subject.
- **paraphrased negation** (13 contradicts, 6 agreeing): a conflict with no
  mirrored "not" ("idempotent" / "creates duplicate rows on re-run"), and
  negated paraphrases that agree.
- **false-positive bait** (20, compatible): pairs sharing most of their tokens
  that both hold ("runs unit tests before building the image" / "runs
  integration tests after building the image").

The hand-written share is deliberate and biases the set: it is built to find
what the rules miss. Read the corpus-derived and hand-written rows of the
per-category table separately.

## Split

The split is not stored. `ReconcileSplit` derives it from the pair id (FNV-1a
mod 10; 0-2 dev, 3-9 test), so a pair cannot be moved after a result is seen.
The committed set splits 50 dev / 151 test. The hybrid routing threshold was
fixed before any result (ADR 0042); the dev split is used only for a
descriptive threshold sweep that does not choose it. Headline numbers are the
test split.

## Running

```sh
go run ./tools/brainbench -mode reconcile                                    # deterministic arm only, no model needed
go run ./tools/brainbench -mode reconcile -llm-model gemma3:12b,qwen2.5:3b \
    -json reconcile.json                                                     # + llm and hybrid arms, local Ollama
```

The LLM arms reach only a local Ollama (`-llm-base-url`, default
`http://localhost:11434`) at temperature 0 with a fixed seed; there is no cloud
path. An unreachable model stops the run. `make test` never needs a model: the
unit tests use a fake judge.
