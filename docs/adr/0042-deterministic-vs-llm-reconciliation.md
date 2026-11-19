# ADR 0042: Deterministic vs LLM reconciliation

- **Status:** Accepted (experiment). Nothing is wired into production.
- **Date:** 2026-11-19
- **Deciders:** Felix Geelhaar
- **Builds on:** ADR 0039 (trust and freshness rank recall), ADR 0040
  (supersession readings in relate), the handling of `DetectCausalLLM`
  (measured, deliberately not wired)
- **Adds:** `go run ./tools/brainbench -mode reconcile`
  (`internal/brainbench/reconcile*.go`), `data/eval/reconciliation/`,
  `relate.SharesTopic` (read-only), opt-in `WithTemperature` / `WithSeed` on the
  OpenAI-compatible LLM client

## Context

Reconciliation is deciding how two beliefs that may conflict relate:
`contradicts` (one denies or supersedes the other) or compatible. Mnemos decides
it deterministically. The detectors in `internal/relate` (polarity, value,
numeric, entity, temporal, frame) propose contradiction edges, adjudication
accepts or rejects each one, and agent-mode recall drops the loser.

ADR 0040 §Limitations names what the rules cannot read. They do not see scope,
so they count the same noun on different branches or sessions as one quantity.
They miss a new owner, a new tool, or a changed policy when no antonym or number
marks the change. The strategy agenda asked whether hybrid maintenance does
better: deterministic where it is confident, an LLM where it is not. Success
meant lower error at acceptable cost, without an unacceptable loss of recall.

Mnemos has rejected this kind of LLM path before. `DetectCausalLLM` labelled
co-occurrence as cause in nearly every pair on a real brain, so it was never
wired. This ADR records the same kind of measurement for contradictions.

## Method

**Data.** `data/eval/reconciliation/pairs.yaml` holds 201 hand-labelled pairs:
88 contradicts and 113 compatible. 104 pairs come from existing corpora and
relate fixtures (`relationship_detection.yaml`, `contested_detection.yaml`, the
brainbench scenarios, `supersession_test.go`, `contradiction_test.go`,
`false_positive_regression_test.go`). The other 97 were written for the ADR 0040
blind spots, each with compatible counterparts:

- scope-blind counts;
- supersession with no antonym or number;
- paraphrased negation;
- compatible pairs that share most of their tokens.

The labelling rules and provenance are in the set's README, and every pair has a
one-line rationale. No label came from a model. The split is derived from the
id: FNV-1a mod 10, so 50 dev pairs and 151 test pairs, 71 of them contradicts.
Headline numbers are on the test split.

**Arms.**

1. **deterministic**: `relate.Engine.Detect` on the pair, as production runs
   it. A `contradicts` edge counts as a contradicts prediction.
2. **llm**: a local model labels every pair. It gets a fixed prompt
   (`reconcile-prompt/v1`, `ReconcileSystemPrompt`) that states the labelling
   rules and asks for `{"verdict": …}`. It sees no deterministic output. An
   unreadable or failed reply counts as compatible and is reported separately.
   No reply was unreadable and no call failed.
3. **hybrid**: the routing rule below.

**Routing rule.** It was fixed in code and committed before any LLM result
existed, and nothing was tuned on the test split:

- A deterministic `contradicts` edge with strength ≥ 0.65 is kept as is.
- An edge weaker than 0.65 is re-decided by the LLM. Strength is
  prior × (0.5 + 0.5 × alignment), so this sends every temporal edge, plus frame
  and named-value edges that align poorly.
- A pair with no edge that passes `relate.SharesTopic` is decided by the LLM.
  `SharesTopic` is the supports path's gate: at least 2 shared content tokens,
  covering at least 30% of the shorter claim.
- Every other pair stays compatible, with no LLM call.

The hybrid arm reuses the llm arm's judgement for each pair it routes, with the
same prompt and sampling. That makes the two arms a paired comparison, and the
hybrid's cost counts only the calls it routes.

**Models.** qwen2.5:3b (the configured default) and gemma3:12b ran on a local
Ollama 0.40.16 at temperature 0 with seed 42, one call at a time. Each pass was
preceded by one untimed warm-up call. Spend was zero.

**Statistics.** The report gives precision, recall and F1 for `contradicts`,
each with a 2000-resample percentile bootstrap 95% interval. The difference in
F1 from the deterministic arm uses a paired bootstrap, scoring both arms on the
same resamples.

## Results (test split, 151 pairs)

| arm | precision | recall | F1 [95% CI] | TP/FP/FN/TN | ΔF1 vs deterministic [95% CI] | LLM calls | p50 per call |
|---|---|---|---|---|---|---|---|
| deterministic | 0.667 | 0.366 | 0.473 [0.352, 0.584] | 26/13/45/67 | — | 0 | — |
| llm, qwen2.5:3b | 0.778 | 0.789 | 0.783 [0.703, 0.854] | 56/16/15/64 | +0.310 [+0.182, +0.434] | 151 | 483 ms |
| hybrid, qwen2.5:3b | 0.687 | 0.648 | 0.667 [0.571, 0.752] | 46/21/25/59 | +0.194 [+0.088, +0.301] | 88 | 468 ms |
| llm, gemma3:12b | 0.945 | 0.972 | 0.958 [0.919, 0.986] | 69/4/2/76 | +0.486 [+0.368, +0.604] | 151 | 3,185 ms |
| hybrid, gemma3:12b | 0.784 | 0.817 | 0.800 [0.719, 0.868] | 58/16/13/64 | +0.327 [+0.221, +0.432] | 88 | 3,201 ms |

The deterministic arm decides all 151 pairs in 10 ms. With gemma3:12b, the
hybrid spends 282 s of model time on the test split and the llm arm 568 s. With
qwen2.5:3b they spend 41 s and 71 s, not counting five calls slowed by other
clients on the shared server. Token counts, the per-category table and every raw
reply are in the report:
`data/eval/reconciliation/results/2026-11-19-qwen2.5-3b-gemma3-12b.json`.

Downstream, the question is how many of the 46 hand-written contradictions each
arm turns into an edge, and how many of the 51 hand-written compatible pairs it
links by mistake:

| arm | contradictions edged (of 46) | compatible pairs edged (of 51) |
|---|---|---|
| deterministic | 7 | 12 |
| hybrid, qwen2.5:3b | 24 | 14 |
| hybrid, gemma3:12b | 35 | 13 |
| llm, gemma3:12b | 46 | 1 |

The routing rule causes most of the hybrid's remaining errors. On the test
split, the gemma3:12b hybrid gets 29 pairs wrong:

- 13 are false edges it kept because the deterministic verdict was confident.
  Eleven of these are scope-blind numeric edges, such as "the main branch has 12
  failing tests" against "the release branch has 3".
- 11 are contradictions the topical gate never sent to the model. The model
  alone got all 11 right.
- 5 are model errors on routed pairs.

A descriptive sweep of the threshold on the dev split moves F1 by at most 0.03,
so the threshold is not what limits the hybrid.

## Decision

**The hypothesis holds against the deterministic arm.** The hybrid beats it on
F1 with both models, and the paired interval excludes zero (+0.33 with
gemma3:12b, +0.19 with qwen2.5:3b). Recall rises without any loss of precision.
**The routing rule tested here is still not the one to ship.** The model alone
beats the hybrid by a wide margin, and the hybrid's remaining errors come from
trusting the rules. It keeps their confident scope-blind edges, and it never
asks about pairs whose shared vocabulary is too small for the topical gate.

Nothing is wired. When this is built, it should be an **opt-in adjudication
pass**, with these guardrails:

1. **Off by default, and never on the write path.** It runs as an explicit
   offline pass, such as a consolidation stage or a `relate` subcommand, with a
   per-pass call budget. A write would route every topical candidate to the
   model. That was 108 of 201 pairs here, and a real brain has hundreds of
   candidates per new claim at roughly 3 s per call with gemma3:12b, which is
   not acceptable inside `Remember`.
2. **Local model by default, with a model floor.** qwen2.5:3b was too weak: it
   had 21 false edges as a hybrid and 16 as the only judge. A pass should refuse
   to run with a model that has not been measured on this set.
3. **Every model-made or model-removed edge is attributable and reversible.** It
   is stamped `DerivedBy = "reconcile-llm/v1:<model>@<prompt version>"`, goes
   through the governed writer, and can be pruned by that stamp, as relate edges
   are by `relate/vN`. A failed or unreadable reply leaves the deterministic
   verdict in place.
4. **The model also verifies deterministic edges, not only weak ones.** The
   largest error class here was confident deterministic edges that were wrong.
5. **Measure again before wiring.** This test split has now been seen. A
   different routing rule needs fresh held-out pairs and a labelled sample from
   a real brain's candidate pairs, because the precision that matters is
   precision on the real distribution, not on a set built around the rules'
   blind spots.

This follows the `DetectCausalLLM` precedent: measured first, then deliberately
not wired. The difference is that this measurement found a real gain, so the
gain is recorded here with the conditions it would need, not discarded.

The numeric detector's scope blindness also stands out as a deterministic defect
on its own. Eleven of the deterministic arm's 13 test false positives are
counts of the same noun in different named scopes. A scope check in the numeric
detector, like the frame detector's environment check (`tailScopesDiffer`), is a
separate change that needs no model.

## Limitations

- **The set is small.** It has 151 test pairs, so the intervals are wide.
  Differences of a few pairs are noise, although every arm's difference from the
  deterministic arm is well outside it.
- **The labeller wrote the detectors.** About half the pairs were written to
  find the rules' documented blind spots, which biases recall against the
  deterministic arm by design. The corpus-derived pairs were mostly written to
  pass the rules, which biases the other way. The per-category table separates
  the two.
- **The prompt states the labelling rules.** The LLM therefore got the
  labeller's definition of scope and supersession in words, while the rules
  encode their own reading of them.
- **Pairs are judged in isolation.** Candidate budgets, the per-claim
  contradiction cap and session-noise suppression do not act here. The
  direction of a supersession, meaning which side wins, is not scored.
- **The models are local and quantised, on one machine.** qwen2.5:3b had to be
  pulled for this run. Temperature 0 and a fixed seed make a run repeatable on
  this machine, not across Ollama versions or hardware. The qwen pass shared the
  server with other clients for its first seven calls. Verdicts are unaffected,
  and wall times are reported with and without those calls.
- **There is one prompt version, and it was not tuned.**
- **Cloud cost is not quoted.** The repository has no price table.
