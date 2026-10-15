# ADR 0031: Recall can return each belief's source excerpt

- **Status:** Accepted (2026-10-15), Cognitive quality milestone. Proposed
  2026-10-12; accepted on the #441 LoCoMo result (see Validation).
- **Date:** 2026-10-12
- **Deciders:** Felix Geelhaar

## Context

After ADR 0030, the #441 Phase A run on LoCoMo (1,540 questions, equal
4,000-token budget, two judges) scored:

| System | Correct |
|---|---|
| Full context | 62.5% |
| BM25 over raw conversation turns | 49.2% |
| Mnemos recall | 39.2% |

Mnemos − BM25 is −10.0 pp, with a 95% CI of [−12.8, −7.2].

Recall now usually reaches the right belief. What it hands the reader loses
the answer:

1. **A belief drops the detail the question asks for.** "Caroline attends an
   LGBTQ support group" loses "on 7 May 2023". This is why temporal questions
   score 19.9% and multi-hop 22.7%, against single-hop at 54.8%.
2. **Beliefs underuse the budget.** Mnemos's context averaged about 2,000
   tokens. BM25 filled its 4,000 with raw turns, wording included.

Every belief already links to the episode (event) it was extracted from
(`BeliefEvidence`). The original wording is in the store; recall just doesn't
return it.

## Decision

1. **`Query.Excerpts` asks recall to attach `Result.Excerpt`** to each
   returned belief. The excerpt is the part of the belief's evidence that best
   matches it.
2. **Selection.** The evidence text is split into lines; a single long line is
   split into sentences instead. The segment sharing the most words with the
   belief is taken (stopwords and words under three letters ignored), plus at
   most one neighbouring segment on each side. The whole excerpt is capped at
   400 characters. The excerpt is empty when no segment shares a word.
3. **Cost.** Excerpts are attached after `Limit`, so only returned beliefs are
   read: one evidence lookup and one event lookup per recall. The ranker is
   unchanged.
4. **Off by default.** Callers that don't ask get the same results as before,
   at no cost. A failure to read evidence fails the recall that asked for
   excerpts rather than returning silently without them (compare #451).

## Alternatives considered

- **Have extraction return a verbatim quote per belief and store it.** That
  gives exact provenance with no re-matching. It needs a prompt and schema
  change, and existing brains have no quotes. It remains the follow-up if
  re-matching proves too imprecise. Re-matching has a known weakness: the
  extractor links a belief to the best-overlapping event in its batch, which
  can be the wrong one.
- **Return whole episodes.** A LoCoMo session is one event of several
  thousand characters, so a few would fill the budget. That would turn Mnemos
  into a worse BM25.
- **Compute excerpts inside the query engine, before `Limit`.** That would
  reuse evidence the engine already loads for provenance, but it would run
  over 100+ candidates instead of the returned few, and change the HTTP and
  MCP answer shapes.

## Consequences

- **Previous:** recall returned belief text only.
- **New:** with `Excerpts: true`, each belief comes with up to 400 characters
  of the source wording it was extracted from.
- **Rationale:** keep what Mnemos adds (which beliefs, how trusted, what
  contradicts them) and give the reader the wording that the belief
  compressed away.
- **Impact:** no change for existing callers. A caller that asks gets larger
  results (up to about 100 tokens per belief) and two extra lookups per recall.

## Validation

Re-run LoCoMo on the #441 harness, with memeval asking for excerpts and the
context showing each belief with its excerpt under the same 4,000-token budget.
The comparison is against the ADR 0030 run: same brains' code, same models and
judges.

- **Accept** when Mnemos improves on 39.2% with a paired 95% CI excluding
  zero. The hypothesis to test is that it closes most of the gap to BM25.
- **Otherwise,** record the negative result here and keep the field
  experimental.

### Result (2026-10-15): accepted

LoCoMo Phase A on the #441 harness: 1,540 questions, the pre-registered #441
configuration, both judges, 4,000-token budget.

| System | Correct |
|---|---|
| Full context | 62.5% |
| Mnemos recall with excerpts | 50.1% |
| BM25 over raw conversation turns | 49.5% |
| Mnemos recall | 38.3% |

- **Against Mnemos without excerpts:** +11.8 pp, paired bootstrap 95% CI
  [+9.4, +14.0]. The CI excludes zero, so the acceptance criterion is met.
- **Against BM25:** +0.5 pp, CI [−1.9, +3.0]. This is a tie: excerpts close
  the gap to BM25 but do not pass it.

By question type (excerpts / plain / BM25):

| Type | Excerpts | Plain | BM25 |
|---|---|---|---|
| Single-hop | 66.8% | 51.0% | 68.1% |
| Multi-hop | 39.0% | 31.6% | 33.0% |
| Temporal | 24.6% | 16.5% | 24.3% |
| Open-domain | 20.8% | 19.8% | 19.8% |

Both Mnemos arms recalled identical claim-ID lists for every question; only
the rendering differs. The gain comes from the excerpt, not from selection.

The trade-off is budget. An excerpt averages about 318 characters per belief,
so about 38 beliefs fit the 4,000 tokens instead of about 99, and evidence
recall drops from 97.2% to 92.9%. Excerpts therefore stay opt-in
(`Query.Excerpts`). A top-k rendering experiment is running and will be
reported on #441.
