# Phase B labelling guide (#441)

This guide is written before any labelling starts, as #441 requires. It defines
the four label kinds, the rules for each, and the matching rule the scorer
applies. Every example below is invented; none comes from a real brain.

The golden set itself is private. It never enters this repository: the tools
refuse to write inside a git worktree, and the default location is
`~/.local/share/mnemos-eval/phase-b/`.

## The workflow

```bash
# 1. Draw the sample (read-only on the brain; there is no default brain path).
python3 -m benchmarks.phase_b.sample /path/to/mnemos.db --out ~/.local/share/mnemos-eval/phase-b/golden

# 2. Pass 1: one labeller labels every item.
python3 -m benchmarks.phase_b.label --dir ~/.local/share/mnemos-eval/phase-b/golden --labeller felix --pass 1

# 3. Pass 2: a second person labels the ~50 kappa items, independently.
python3 -m benchmarks.phase_b.label --dir ~/.local/share/mnemos-eval/phase-b/golden --labeller anna --pass 2

# 4. Score (dev split by default; held-out only for final verdicts).
python3 -m benchmarks.phase_b.score --dir ~/.local/share/mnemos-eval/phase-b/golden
```

The labelling page opens at `http://127.0.0.1:8441/`. Press `?` for the keys.
Every change is saved as you go to `labels/pass<N>-<labeller>.json`.

**Independence.** The pass-2 labeller never sees pass-1 labels, and does not
discuss items with the pass-1 labeller until both passes are complete. Use this
guide, not each other, to settle a doubt. Record a doubt in the item's notes.

**Split blindness.** The page does not show whether an item is held out. Label
every item the same way.

**Order within an item.** Extraction comes first. Write your gold claims
before you reveal what Mnemos extracted; relations and truth stay locked until
you do. Revealing cannot be undone for that item, so do not reveal early.

**Marking done.** Each stage has its own done flag (`d`). The scorer counts a
stage for an item only when it is marked done. Mark a stage done even when the
right answer is "nothing": an episode with no claims, no edges or no question.

## 1. Extraction: gold atomic claims

Write every claim the episode asserts that a memory should keep, one per line.

### What is one atomic claim

- **One fact per line.** If a sentence joins two facts with "and", "but" or a
  comma, split it. "The API uses Postgres and is deployed on Fridays" is two
  claims.
- **Self-contained.** Resolve pronouns and implicit subjects from the episode,
  so the line can be read alone. Write "The billing service retries failed
  webhooks three times", not "It retries them three times".
- **The episode's wording where it is clear.** Keep names, identifiers,
  numbers, versions and dates exactly as written (`v2.4.1`, `PG_POOL_MAX`,
  `eu-west-1`). Do not normalise units or round numbers.
- **Resolve relative time when the episode lets you.** If the episode is dated
  2026-03-12 and says "we switched yesterday", write "The team switched the
  queue to SQS on 2026-03-11". If it gives no anchor, keep the phrase.
- **Attribute opinions and plans.** A decision, a preference or an intention
  is a claim, but say what kind it is: "The team decided to drop Python 3.9
  support", "Dana prefers squash merges".

### What is not a claim

- Greetings, acknowledgements, agent narration ("Let me check the logs"),
  questions, and instructions to the assistant.
- Statements about the conversation itself ("As discussed above").
- Tool output or code pasted without an assertion about it. A sentence that
  says what the code does is a claim; the code is not.
- Hypotheses the episode rejects in the same breath. "I thought it was DNS,
  but it was the certificate" gives one claim: the cause was the certificate.

### Worked example

Episode (dated 2026-04-02):

> ok so the flaky test in the payments repo was a timezone bug, it assumed
> UTC. I fixed it by pinning TZ in the CI config. Also we're moving the nightly
> job to 02:00 starting next week. Thanks!

Gold claims:

```
The flaky test in the payments repository was caused by a timezone bug
The flaky test in the payments repository assumed UTC
The flaky test was fixed by pinning TZ in the CI configuration
The nightly job moves to 02:00 starting the week after 2026-04-02
```

"Thanks!" is not a claim. "It assumed UTC" names its subject. "Next week" is
anchored to the episode date.

### Matching rule (deterministic)

After you reveal the extracted claims, each is matched to at most one gold
line, and each gold line to at most one extracted claim:

1. **Your manual links first.** For any extracted claim you may pick the gold
   line it expresses, or "matches no gold claim". A link wins over everything
   automatic. If two extracted claims are linked to the same gold line, the
   first one (in the order shown) gets it and the second counts as unmatched:
   a duplicate is not a second correct extraction.
2. **Then automatic.** Both texts are reduced to content tokens: Unicode NFKC,
   lowercase, identifiers kept whole (`v0.85.0`, `go1.22`, `foo_bar`),
   stopwords removed, a plural `-s` stripped from words longer than three
   letters. The similarity is the Dice coefficient of the two token sets,
   2|A∩B| / (|A|+|B|). Pairs at or above **0.5** are matched greedily, highest
   similarity first, ties broken by gold line order and then extracted order.

Precision is matched extracted claims over all extracted claims; recall is
matched gold lines over all gold lines, both summed over items.

**H3's primary measure is precision over live claims only**: claims that are
neither deprecated (for example pruned as narration after ingest) nor merged
into another claim. That is the report's primary extraction row. The same
scores over every claim linked to the episode are kept as a secondary row
(`all_linked`). The report also gives the live-claim scores at thresholds 0.4
and 0.6. Label every extracted claim the page shows, live or not: you cannot
see which are live, and the scorer applies the filter.

**When to link by hand.** The page shows each automatic match and its score.
Override it when:

- an extracted claim says the same thing in other words and scored below 0.5
  (link it);
- an automatic match pairs two claims that share words but assert different
  things, such as a different number, a negation or another subject (choose
  "matches no gold claim", or the right line);
- an extracted claim distorts the episode (a wrong number, a dropped "not"):
  "matches no gold claim", even if it is close.

Links are stored by gold line position. If you edit your gold lines after
linking, check the links again.

## 2. Relations

The page lists the edges Mnemos stored between this episode's claims, and
from them to claims of other episodes (the "neighbour" claims). Judge each,
then add any edge Mnemos missed among the claims shown.

| Label | Key | Use when |
|---|---|---|
| supports | `s` | One claim makes the other more likely to be true, and they are not the same statement. |
| contradicts | `c` | Both cannot be true at the same time, and neither has replaced the other. |
| supersedes | `>` / `<` | They conflict because one replaced the other over time. `>` means the first claim (upper) supersedes the second; `<` the reverse. The newer claim supersedes the older. |
| none | `x` | Unrelated, merely on the same topic, or near-duplicates. |

Rules:

- **Exactly one label per pair.** A supersession is not also a contradiction.
- **Supersedes needs a change over time**, stated or evident from the dates:
  "The rate limit is 100/min" (January) and "We raised the rate limit to
  1000/min" (March): the March claim supersedes the January one.
- **Contradicts is a live conflict**: two sources disagree about the same thing
  at the same time. "Staging runs in eu-west-1" and "Staging runs in
  us-east-1", both from the same week, contradict.
- **Different scope is not a conflict.** "Production uses 8 replicas" and
  "Staging uses 2 replicas" are unrelated (`x`).
- **Supports is not topic overlap.** "The cache is Redis" and "Redis is
  deployed on Kubernetes" share a word and are unrelated. "Cache misses spiked
  after the deploy" supports "The deploy broke cache key hashing".
- **Duplicates are `none`.** Two claims saying the same thing are a dedupe
  issue, not a relation.

To add an edge Mnemos missed, choose the two claims and the type. For
supersedes, put the newer claim first.

Scoring: precision and recall per type, over the claims the item shows, and
separately for pairs within the episode only. Cross-episode recall is
optimistic, since a neighbour claim is shown only because Mnemos linked it;
the within-episode figures are the unbiased ones. Mnemos stores no
supersedes edge: a stored contradiction with exactly one end invalidated
(`valid_to` set, or lifecycle `superseded`) is read as the other end
superseding it.

## 3. Truth: is the claim still true?

For each claim extracted from the episode, label whether it is **true today**
(the labelling date, shown on the page): `t` true, `f` false, `u` cannot tell.

This is not "was it extracted correctly". A faithfully extracted claim can
be false now (the team has since moved off Jenkins), and a garbled extraction
can still state something true.

Rules:

- **True** if, to your knowledge today, the claim holds. A claim about the
  past is true if it correctly describes the past: "The outage on 2026-02-03
  was caused by an expired certificate" stays true.
- **False** if it never held, or held once and no longer does: "CI runs on
  Jenkins" after the move to GitHub Actions.
- **Cannot tell** when you do not know and could not find out in about a
  minute, or the claim is too vague to be true or false ("Things are slow").
  Use it honestly; it is excluded from calibration, not counted against you.
- Judge the claim as written, not what the speaker probably meant.
- Stored trust and status are hidden on purpose. Do not look them up.

Scoring: Brier score, Brier skill score against always predicting the base
rate, expected calibration error and the reliability curve of the stored
trust score against your true/false labels (H2).

## 4. Questions (LongMemEval style)

Optionally, write questions an agent might ask later that this episode
answers, or shows the brain cannot answer. Aim for one or two on items where a
good question exists; many items will have none. Each question has a gold
answer, a type and an "as of" date (the moment the question is asked).

| Type | Example (invented) |
|---|---|
| single-episode | "Which environment variable caps the payments DB pool?" Answer: `PG_POOL_MAX`. |
| multi-episode | "Which two services share the audit queue?" Needs this episode and another; list the other's episode id in the notes. |
| knowledge-update | "What is the payments API rate limit?" Answer: "1000 requests per minute". Stale answers: "100 requests per minute". |
| temporal | "When was the nightly job moved to 02:00?" Answer: "the week of 2026-04-06". |
| abstention | "Which on-call tool does the payments team use?" when no episode says. Answer: "Not known / not recorded". |

Rules:

- The answer must follow from the episodes, not from your general knowledge.
- **Knowledge-update questions list their stale answers.** An answer that
  relies on an invalidated belief counts as wrong even if it reads plausibly
  (#441, item 6), so name every superseded value under "stale answers".
- **Temporal questions** need an answer with a date or a span, resolved
  against the episode dates.
- **Abstention questions** must be plausible: something the brain could have
  known but does not. Do not write trick questions.
- Keep the question free of the answer's wording, so retrieval has to work.

Answering is not scored by these tools: `score --export-questions` writes the
questions in the Phase A shape for the Phase A answer and judge stages, and
`score --verdicts` scores what comes back.

## Agreement

About 50 items are labelled by both passes. The report gives Cohen's kappa
for extraction (whether each extracted claim matched a gold line under each
labeller's gold), truth (three categories, and true/false where both knew),
and relations (the label of every pair either labeller or Mnemos linked). A
kappa below 0.6 on a stage means the guide is ambiguous for that stage: fix
the guide and relabel that stage, rather than report its scores as settled.

The second labeller may be a model run blind by
`benchmarks/phase_b/claude_label.py`. It is given this guide as its
instructions and sees exactly what a human pass-2 labeller sees, in the same
order: it writes gold claims from the episode alone, and only then sees the
extracted claims, with stored trust, status and validity hidden. It never
reads pass-1 labels. Its labels go to `labels/pass2-<model>.json`, in the
same format as the labelling page's.

When no human pass 1 is available, `claude_label.py --pass 1 --model <model>`
makes a model the first labeller: it labels every item into
`labels/pass1-<model>.json`. Use a different model from the pass-2 labeller,
so kappa still compares two independent labellers. A model's truth labels are
its best guess about the operator's projects, not knowledge of them, so a
report scored against a model pass 1 must say so, and its calibration verdict
is weaker than one scored against a human pass 1.
