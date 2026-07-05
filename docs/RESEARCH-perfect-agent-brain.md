# Toward a perfect agent brain — research synthesis, part 2

The first synthesis (`RESEARCH-brain-enhancements.md`) had one thesis: mnemos was a
store whose higher functions were dormant, and the lever was to *run them*. That arc
shipped — consolidation + forgetting, salience, hybrid + corrective retrieval,
hypercorrection, and confidence calibration are all live (mnemos v0.35–v0.42). mnemos
now maintains itself and knows something about how well it knows.

This part asks the next question: what separates "a memory that maintains itself"
from **a brain**? Four parallel research sweeps (predictive processing / world models;
cognitive control & working memory; learning, skills & sleep-stages; social memory &
retrieval-as-reasoning), each cross-checked against what mnemos already ships. They
converged more than they diverged — the convergence *is* the finding.

---

## The one-line thesis

**The first arc gave mnemos organs. The next gives it a nervous system: one signal —
prediction error — that ties the organs into a loop, so the store gets *better with
use* instead of merely *bigger*.** And it can be built, mostly, with no model in the
loop, on primitives already in production.

---

## The through-line (what all four lenses independently landed on)

1. **Prediction-error / surprise is a currency mnemos can already mint — and it unifies
   subsystems that are currently independent heuristics.** Calibration *is*
   prediction-error on confidence. `validates`/`refutes` edges are completed
   predict→observe cycles. Hypercorrection is a high-magnitude surprise event, unlabeled.
   chronos anomalies are deviations-from-expected — surprise in the perception channel.
   One number — the gap between an expectation mnemos held and the evidence that arrived —
   can drive salience (surprising memories should resist forgetting), forgetting (prune the
   *predictable*, keep the surprising), attention (retrieve what most reduces uncertainty),
   replay (reprocess high-error episodes first), and decay (a *refuted* belief should decay
   faster than the fixed 90-day half-life). Today these are five separate knobs; surprise
   is the one dial behind them.

2. **The machinery is dormant, not missing — so the work is closing loops, not building
   subsystems.** Every lens found the same shape: mnemos already computes the signal
   (calibration, validates/refutes, salience, chronos) but doesn't *feed it back*. The
   pattern that defined arc 1 (wake the dormant capability) is still the highest-ROI move.

3. **Monitoring must become control.** Arc 1 made mnemos *measure* its epistemic state.
   A brain *acts* on it — allocating effort, deciding when to retrieve vs. answer, when to
   think harder, when to abstain. The signals exist; they aren't yet wired to decisions.

4. **Epistemic honesty at the primitive level is the foundation everything else reads.**
   In a multi-worker org, trust and corroboration are consumed by *every* downstream layer
   (retrieval, reasoning, prediction, learning). If corroboration can be inflated by
   self-citation, or confidence is calibrated only globally, then every layer amplifies a
   false belief with a confident number attached. Fix trust at the root first.

5. **Determinism is the safety rail, not a constraint.** The loops that need no LLM are
   exactly the ones safe to close automatically; the ones that need an LLM (creative
   recombination, schema induction, open-ended sub-question generation) are exactly the
   ones to leave as human-curated *proposals*. This maps cleanly onto mnemos's existing
   propose→promote discipline.

---

## Already shipped (so we build, not reinvent)

Episodic events + semantic claims + decisions, evidence-backed, bi-temporal; an epistemic
graph (supports / contradicts / causes / validates / refutes); trust = confidence ×
corroboration × freshness (90-day half-life). **Cognitive layer (v0.35–v0.42), all
deterministic:** consolidation "sleep" pass (dedupe→gist + trust refresh + active
forgetting, salience- and promotion-protected); write-time salience; hybrid dense+sparse
retrieval fused by RRF; CRAG-style corrective retrieval; hypercorrection alerts;
confidence calibration (ECE/Brier). Perception via chronos; a manual `synthesize`
(claims→lessons→playbooks); `bias` detection; per-tenant isolation.

---

## The roadmap

Ordered by dependency, not excitement. The foundation makes the rest trustworthy; the
prediction loop mints the currency the rest spends.

### Tier 0 — Epistemic honesty (foundation; cheap; fully deterministic)

Every other tier reads trust and corroboration. Harden them first.

- **T0.1 Independence-aware corroboration (echo-chamber guard).** Count corroboration by
  *distinct* `(CreatedBy, SourceDocument)` origins, not raw edge count. If a claim's
  support closure traces back to a single root source (a worker re-citing its own earlier
  belief; two workers reading the same dashboard), collapse it toward weight 1 via a
  provenance-DAG check over the existing recursive-CTE traversal. **The single most
  important primitive fix** — without it a multi-agent store silently manufactures
  consensus. *Deterministic. Low–medium effort.* Risk: "independent" is hard to define;
  make it a graded shared-root *discount*, not a hard collapse, and require
  `SourceDocument` granularity at write time.
- **T0.2 Per-source calibration.** Shard the global calibrator by `(CreatedBy, SourceType)`.
  Trust becomes `confidence × corroboration × freshness × source_reliability`, where each
  source earns its own Brier/hit-rate from the validates/refutes it accrues. A chronically
  over-confident source auto-discounts, no config. *Deterministic (a `GROUP BY` over the
  existing calibrator). Low–medium effort.* Risk: a miscalibrated per-source score fails
  silently per-worker — validate the curve empirically before trusting the multiplier.

### Tier 1 — The prediction loop (mints the currency)

- **T1.1 Structured forward *expectations* on claims.** Let a `decision`/`hypothesis`
  carry an optional structured prediction with a valid-time horizon ("error rate < 1%
  within 24h"). A scheduled reconciliation job (same grain as the sleep pass) closes open
  expectations against arrived evidence, auto-emits `validates`/`refutes`, and writes a
  **surprise scalar** = precision-weighted |predicted − observed|. *Deterministic for
  structured predictions (numeric/boolean/chronos-signal) — bless these as the only
  auto-scored path; unmet-at-horizon is `unconfirmed`, never `refuted`. Medium effort.*
- **T1.2 Route surprise into the existing knobs.** The surprise scalar bumps **salience**
  (surprising memories resist forgetting — the flashbulb / hypercorrection effect), tags
  episodes as priority for the next consolidation pass, and shortens the **trust half-life**
  of a violated belief. This is what turns prediction-error from a metaphor into the
  *unified currency* — and it's the "dormant machinery" pattern: no new subsystem, a new
  input to knobs that already exist. *Deterministic. Low–medium effort once T1.1 lands.*
- **T1.3 Curiosity / knowledge-gap detector.** A scheduled scan ranks the store's weak
  spots by expected information gain (crudely `salience × uncertainty × staleness`):
  dangling hypotheses with no validates/refutes, high-`contradicts`-density clusters,
  volatile-but-sparse topics, regions where calibration is *poorly* calibrated. It writes
  `open_question` proposals — *what to seek next* — surfaced at planning time. Memory turns
  from reactive to agenda-setting; mnemos proposes, the agent disposes. *Detection/ranking
  fully deterministic. Medium effort.*

### Tier 2 — Gets better with use (the learning loop)

- **T2.1 Prioritized-replay scheduler + staged sleep.** Before consolidation, rank episodes
  by `salience × prediction-error × recency` and reprocess the top-K first and more often
  (textbook prioritized experience replay). Today's pass becomes the SWS stage, now fed by
  the scheduler instead of a uniform scan; interleaving old+new episodes is also the
  standard defense against catastrophic interference. *Deterministic. Low effort — a
  rewire of the pass you already run.*
- **T2.2 Close the skill loop (deterministic RL-lite) — highest ROI-per-effort in this
  tier.** Two arrows, no LLM: (a) auto-trigger `synthesize` on evidence pressure (N new
  corroborating claims, or a calibration miss), not just a cron; (b) reinforce a playbook's
  confidence by its running *outcome* success rate (validates/refutes already fire) — a
  bandit mirroring the claim calibrator. Playbooks that keep working rise; ones that fail
  decay (reuse trust half-life). Turns the skill store from write-only into self-tuning —
  the difference between "stores facts" and "gets better." *Deterministic. Low–medium.*
  **SHIPPED (arrow b) — v0.46.0 (`ConsolidateOptions.ReinforcePlaybooks`).** The sleep pass
  bends each playbook's confidence toward the observed success rate of the outcomes on the
  actions its lessons were derived from (`0.7·prior + 0.3·success_rate`, success=1 /
  partial=0.5 / failure=0, "unknown" ignored, skipped below 2 scored outcomes). Signal is
  corroboration-by-underlying-evidence (reuses the links synthesize already writes — no new
  schema primitive), not decision-level attribution. Senat runs it nightly
  (`REINFORCE_PLAYBOOKS=1`, v0.11.58). *Arrow (a) auto-trigger-synthesize is the open
  remainder — synthesize is still CLI-only, so the loop is dormant in Senat until it runs.*
- **T2.3 REM-like recombination (LLM-optional, lands as candidates).** A second offline
  stage: deterministic clustering picks high-salience pairs the waking graph never
  juxtaposed (distant in the graph, close in embedding); an LLM only *names* the candidate
  schema/hypothesis. Everything lands in the candidate table for human promotion — never
  auto-promoted. No key → skip REM, SWS still runs. *LLM-optional. Medium.*
- **T2.4 Schema-guided assimilation vs. accommodation.** Route a new claim by fit to
  existing gists: fits a schema → learn it cheaper (lower corroboration bar, fast-path);
  *violates* a high-trust schema → high prediction-error → straight into hypercorrection.
  *Routing deterministic; schema induction leans on T2.1/T2.3. Medium.*

### Tier 3 — The executive (control & working memory)

- **T3.1 Working-memory blocks (the executive substrate).** A small set of bounded,
  always-loaded, labeled blocks per (agent, org) — `persona`, `open-threads`,
  `working-context` — injected verbatim ahead of retrieval. The size cap *is* the attention
  budget. Self-edits route through the **existing candidate lifecycle** (governed,
  evidence-backed, reversible); eviction to the archive is the consolidation pass. Converts
  mnemos from a queried archive into an agent with a persistent, governed *train of thought*.
  *Deterministic writes. Medium.* Risk: an always-loaded self-editing block is a standing
  prompt-injection / error-amplification surface — the candidate lifecycle + evidence-backing
  + trust decay are the native mitigations; it must never become an ungoverned scratchpad.
  **SHIPPED — v0.49.0 (`Memory.Blocks`/`SetBlock`/`AppendBlock` + `ports.BlockRepository`).**
  Bounded (`WorkingMemoryBlockLimit`: `SetBlock` rejects over-limit, `AppendBlock` evicts
  oldest lines), labeled, per-owner blocks on in-memory + SQLite + Postgres (with the ADR 0007
  tenant column + RLS, so per-tenant isolated); nil-able → `ErrBlocksUnsupported` elsewhere.
  Senat wires it (v0.11.61): `engine.workingMemoryHint` injects a worker's blocks verbatim
  AHEAD of the retrieval hints, and a governed, worker-scoped `update_working_memory` tool
  (write-local, auto-approved, bounded to the worker's OWN blocks) is auto-granted to every
  worker — the self-edit governance the risk note wants (deliberate tool call + bounded block
  + axi audit, never an ungoverned always-on write). *Blocks are set/append with UpdatedAt;
  full version-chain reversibility + consolidation-pass eviction to the archive are the open
  refinements.*
- **T3.2 Retrieve-vs-answer / feeling-of-knowing gate — highest ROI per line of code.**
  Before retrieving (or answering), consult calibration-adjusted recall confidence:
  high + well-calibrated → answer from working memory, skip retrieval; mid → retrieve;
  low, *or low in a domain where calibration is known-poor* → **abstain / escalate to a
  human**. Abstention is the most under-served agent behavior, and mnemos is uniquely able
  because it *knows its own calibration* (now per-source, via T0.2). *Deterministic. Low.*
  **SHIPPED (feeling-of-knowing) — v0.47.0 (`Memory.RecallWithSufficiency` → `Sufficiency`).**
  Surfaces the aggregate answer confidence (the trust/density/contradiction/recency blend the
  query already computes but plain Recall discarded) + a `Sufficient` flag against the exact
  `RecallSufficiencyFloor` (0.35) mnemos uses to gate its own corrective pass — so the "feeling
  of knowing" a caller reads is the signal mnemos acts on. Senat wires it as an **abstain gate**
  (v0.11.59): `engine.abstainHint` injects an epistemic-humility directive into a run's objective
  when the org's VETTED memory is thin, so the worker investigates first-hand and flags
  uncertainty instead of asserting weakly-supported conclusions; silent when memory is sufficient.
  *Calibration-adjusted thresholding + explicit human-escalation are the open remainder — today
  the gate is a fixed floor over promoted-knowledge confidence.*
- **T3.3 Effort/attention budgeting.** Generalize the corrective pass into an
  Expected-Value-of-Control budget: retrieval depth, fusion breadth, and number of
  corrective passes scale with `stakes × (1 − calibratedConfidence)`. Stakes can be read
  from the axi effect profile of the pending action (read vs. write-external) — zero new
  inputs. *Deterministic. Low–medium.*
  **SHIPPED — v0.48.0 (`Memory.RecallWithEffort` + pure `EffortBudget`).** A first pass,
  then ONE bounded wider+deeper pass invested only when `budget = stakes × (1 − confidence)`
  clears `EffortEscalationThreshold` and the first pass wasn't already sufficient — low-stakes
  stays cheap, high-stakes+uncertain escalates (up to `EffortMaxExtraHops`/`EffortMaxExtraBreadth`),
  non-regressive, `EffortReport` returned. Senat wires it (v0.11.60): `engine.runStakes` reads
  stakes from the worker's grants (any non-read capability → acting → high stakes) and threads it
  into `knowledgeHint` via `Store.RecallPromotedGraphEffort`, so workers that act on the world
  ground their knowledge harder when vetted memory is thin. *Confidence not yet
  calibration-adjusted (T0.2 feeds it later); passes capped at one.*
- **T3.4 Spreading activation.** Whatever is in working-context pre-activates its graph
  neighbours: at retrieval, expand 1–2 hops over the epistemic graph and add a decaying
  `+activation` term before RRF/CRAG. Makes retrieval sensitive to the current train of
  thought — coherence for nearly free. *Deterministic. Low.*
  **SHIPPED — v0.50.0 (`Memory.RecallWithContext`).** Expands the context up to
  `ActivationHops` (2) over the epistemic graph (reusing hop expansion as the spread) and
  promotes each connected query result by a decaying term (`ActivationDecay`/hop, up to
  `ActivationBoost` positions) — an order-preserving re-rank, empty context = plain Recall.
  Senat wires it (v0.11.62): a worker's `knowledgeHint` recall is biased by its working
  memory when present (`Store.RecallPromotedContext`), else falls back to effort-budgeting —
  so working memory (T3.1) now shapes both what the worker reads AND what knowledge surfaces.
  **Tier 3 (the executive) complete: working-memory substrate + abstain/effort/activation control.**

### Tier 4 — The connected brain (social routing & retrieval-as-reasoning)

- **T4.1 Transactive "who-knows-what" index.** Per-tenant expertise directory: aggregate
  each worker's high-trust claims into topic centroids (reuse stored embeddings; cluster
  with the consolidation clusters). `WhoKnows(query) → ranked [(worker, affinity,
  reliability)]` routes a knowledge gap to the right expert-memory instead of a flat search.
  The deterministic backbone of theory-of-mind. *Deterministic. Medium.*
- **T4.2 Analogical / structural retrieval — differentiated; only possible here.** Retrieve
  by *relational* similarity, not content: fingerprint each episode's typed subgraph
  (Weisfeiler-Lehman label propagation, 1–2 iterations over the edge types), index it, and
  rank by signature overlap, then re-rank by role semantics. Finds past incidents with the
  *same causal skeleton* even when services/metrics differ — "we've seen this *shape*
  before." A pure-vector store cannot do this; mnemos can *because* it has a typed epistemic
  graph. *Deterministic. Medium–high.*
- **T4.3 Iterative retrieve↔reason loop + conflict-aware multi-hop.** Replace single-shot
  retrieve→answer with a bounded fixpoint: retrieve → assess coverage → on an unresolved
  entity/claim, spawn a follow-up query → stop on saturation or budget. The controller is
  deterministic (reuse the CRAG signal + entity-gap detection); an LLM helps only for
  open-ended sub-question generation, kept optional. Traverse `supports`/`contradicts`/
  `refutes` as *signed* edges and surface the **contested frontier** — high-trust claims
  connected by an unresolved contradiction — so the answer carries its own live
  counter-evidence. *Deterministic controller viable. Medium.*

---

## Recommended sequence

1. **Tier 0** first, always. It's cheap, fully deterministic, and everything downstream
   reads trust/corroboration — honest primitives make every later layer trustworthy instead
   of amplifying error.
2. **Tier 1 (the prediction loop).** T1.1 + T1.2 together close predict→observe→error and
   route the surprise. This is the flagship of arc 2 — it converts three shipping-but-passive
   facts (calibration, validates/refutes, hypercorrection) into an active feedback loop and
   makes salience/forgetting/decay *earned by surprise* rather than asserted. Produces a
   compounding asset mnemos uniquely can: a *track record* of its own past predictions.
3. **Tier 2 (learning).** T2.1 + T2.2 with zero new LLM dependency — the system starts
   spending its offline budget on what it got wrong, and its skills earn their confidence
   from reality. T2.3/T2.4 follow, LLM-optional, as candidates.
4. **Tier 3 (executive)** and **Tier 4 (connected/reasoning)** in parallel — both consume the
   foundation + currency the first tiers lay down. Within them, T3.2 (the abstain gate) and
   T4.2 (analogical retrieval) are the standout, low-regret wins.

**If you build one thing:** Tier 0 + T1.1/T1.2. Honest trust plus a closed prediction-error
loop is the smallest change that turns "maintains itself" into "learns."

---

## The risks that recur (and their shared rail)

Three failure modes showed up across lenses, and they share one mitigation:

- **Runaway self-reinforcement / model collapse** (Tier 2) — a loop that promotes its own
  gists and up-weights its own playbooks amplifies early bias and over-forgets minority
  cases.
- **Context poisoning** (T3.1) — a wrong claim in an always-loaded block is injected into
  every subsequent run and compounds.
- **Manufactured consensus** (Tier 0) and **noisy/gameable surprise** (Tier 1) — a bad
  signal that now *drives* forgetting and decay can prune the right memories.

The shared rail is mnemos's own discipline: **the loops that need no LLM are safe to close
automatically; the ones that need an LLM stay human-curated candidates.** Reinforcement is
*outcome-gated* (confidence rises only on externally-attached validates, never on reuse
alone). Interleaved replay + active forgetting + trust decay are the homeostatic
counter-pressure that stops any single schema, block, or belief from dominating. Determinism
isn't a limitation here — it's the boundary between what's safe to automate and what must
stay a proposal.

---

*The first arc woke mnemos's cognition. This arc closes the loop: a store that predicts,
measures its own error, and spends that error to get better — honestly, and without a model
in the loop for the parts that matter. That is the difference between a memory and a mind.*
