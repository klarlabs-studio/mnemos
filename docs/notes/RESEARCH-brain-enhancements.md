# Making the brain better — research synthesis

Grounded in three parallel research sweeps (state-of-the-art agent-memory systems,
neuroscience of memory, retrieval/reasoning techniques) cross-checked against what
mnemos actually ships today. Sources are inline.

---

## The one-line thesis

**mnemos is a well-stocked memory *library* that mostly operates as a passive
*store*. The single biggest lever is turning it into an active *system* — because
the machinery for the higher functions already exists but is dormant or manual.**

All three lenses land on the same gap from different directions: the brain — and
every leading agent-memory system — does not just store and retrieve. It runs
continuous background processes (consolidation, salience-tagging, forgetting,
calibration, reflection) and does smarter retrieval. mnemos has most of the parts;
they aren't wired to run.

---

## What mnemos already has (so we build, not reinvent)

The research agents under-credited mnemos on several axes — the real baseline:

| Capability | Status in mnemos |
|---|---|
| Episodic (events) + semantic (claims) + decisions | ✅ shipped |
| Trust = confidence × corroboration × freshness, **half-life decay** | ✅ (`internal/trust`) — an ACT-R base-level-activation analog |
| **Bi-temporal** claims (`ValidFrom`/`ValidUntil` + `AsOf`/`RecordedAsOf`) | ✅ already point-in-time queryable |
| Epistemic graph (supports/contradicts/causes) + **auto edge generation** | ✅ (`relate`, `autoedge`) |
| **Consolidation machinery** — claims→lessons→playbooks | ✅ EXISTS (`synthesize`) but **manual / CLI-only** |
| **Procedural memory** — playbooks | ✅ (`synthesize.Playbooks`) |
| **Metacognition** — evidence-base bias detection | ✅ EXISTS (`bias`) but **on-demand** |
| Temporal perception (trend/spike/anomaly) | ✅ (chronos) — now surfaced into runs |
| Human curation lifecycle (candidate→promoted→superseded) | ✅ — a governance advantage over last-write-wins systems |
| Per-tenant isolation, federation, governed writes | ✅ |

**The dormant-machinery pattern is the story of this whole consolidation** — chronos
was computed-but-unread until we woke it; `synthesize` and `bias` are the same shape.

---

## Where the three lenses converge (the priorities that showed up 2–3×)

1. **Write-time importance / salience weighting** — top-2 in BOTH the SOTA and
   neuroscience sweeps. Generative Agents' "poignancy", EM-LLM's Bayesian-surprise
   segmentation, and the brain's amygdala/dopamine salience-gating + synaptic
   tag-and-capture all say the same thing: score how *important* each memory is at
   encoding, and use it everywhere downstream (ranking, consolidation, forgetting).
   mnemos treats all events uniformly today.
2. **A background "sleep" pass that consolidates + reflects + forgets** — top-2 in
   BOTH. Letta sleep-time compute, Cognee `memify`, Stanford reflection; CLS
   (hippocampus→neocortex) + SWS replay + synaptic renormalisation. This is the
   *container* for consolidation, gist-extraction, and forgetting — and mnemos
   already has the consolidation engine (`synthesize`), just not scheduled.
3. **Smarter retrieval, mostly on infra we already have** — hybrid dense+sparse
   fusion (+26–31% NDCG), a corrective-retrieval gate (CRAG: +7–37%), reranking
   (+5–15 NDCG@10). Postgres FTS + the epistemic edge table + bi-temporal columns
   are already present; these are the highest ROI-per-effort items.
4. **Metacognitive calibration** — flag when a high-confidence belief gets
   contradicted (ACC "hypercorrection"), track whether confidence is *itself*
   trustworthy, and make curation targeted instead of manual triage.

---

## Prioritized roadmap

### Tier 0 — Retrieval sharpening (highest ROI/effort; reuses existing infra, no LLM)

- **R1. Hybrid retrieval (dense + Postgres FTS, RRF fusion).** Add a `content_tsv`
  GIN column, run `websearch_to_tsquery` alongside the pgvector `<=>` leg, fuse by
  Reciprocal Rank Fusion (k≈60) in Go. Fixes mnemos's real weakness: exact-token
  recall of identifiers/SHAs/service names that cosine underweights. **+26–31% NDCG**
  on mixed queries, zero new infra. *(HyDE §7, Zilliz, DigitalApplied)*
- **R2. Recall-confidence + as-of.** Return a recall-episode confidence (top-sim,
  rank-1↔K margin, aggregate trust) so a caller can abstain below τ; expose
  `Recall(query, asOf)` over the **already-present** bi-temporal columns. Near-free.
- **R3. Corrective-retrieval gate (CRAG-style).** Grade the candidate set
  sufficient/ambiguous/insufficient; on insufficient, one bounded corrective
  re-query (widen K / drop filters / rewrite). **Largest measured deltas in the
  literature (+7–37%)**, bounded to respect the 30s tool-call cap. *(arXiv 2401.15884)*
- **R4. Optional reranker + bounded multi-hop.** A rerank stage over top-K behind
  the existing "supply a Voyage/Cohere key to upgrade" ergonomics (+5–15 NDCG@10);
  a depth-2–3 recursive-CTE expansion over the existing edge table for causal-chain
  reasoning + conflict-aware recall (never silently return a disproven fact).

### Tier 1 — Wake the dormant cognition (biggest structural win)

- **C1. Write-time salience score.** A `Salience float64` on events/claims from
  signals the org already emits: **novelty** (embedding distance from existing
  memory), **surprise** (reuse chronos's anomaly/spike output — already computed!),
  **outcome magnitude** (incident severity, reverted deploy). Fold it into recall
  ranking as a 4th factor beside confidence/corroboration/freshness, and use it to
  gate the sleep pass. *This is the cheapest high-impact item and the prerequisite
  for consolidation + forgetting.* Add **tag-and-capture**: a high-salience event
  retroactively boosts retention of temporally-nearby ordinary events (auto-preserve
  the mundane config change that preceded a Sev-1).
- **C2. Scheduled consolidation/"sleep" worker.** A cron/off-hot-path job that:
  (a) runs the **existing** `synthesize` (episodes→lessons→playbooks) automatically
  instead of by CLI; (b) **reflects** — pulls recent high-salience clusters, asks
  "what pattern follows?", writes derived claims with `causes`/`supports` edges to
  their evidence, landing as *candidates* (preserving the human-curation invariant);
  (c) **forgets** — gist-collapses near-duplicate episodes into one claim + a count,
  and predictively prunes episode fields that never fed a consolidated claim
  (bounds unbounded event growth). Interleaved/incremental, per CLS, to avoid
  catastrophic overwrite. This is the keystone — it makes mnemos *learn on its own*.
- **C3. Metacognitive calibration + hypercorrection alerts.** Track per-source
  calibration (is 0.9-confidence actually right 90%?); emit a conflict alert when a
  high-trust claim is contradicted by strong new evidence and route it to the *front*
  of the curation queue; expose feeling-of-knowing at query time. Builds on the
  existing `bias` package + confidence. Makes the brain *know what it doesn't know*.

### Tier 2 — New capabilities (differentiating, bigger builds)

- **P1. Prediction/anticipation layer.** Let a claim carry a forward prediction
  ("Friday deploys → incident"); compute prediction-error on each new event; route
  high-error events to consolidation + boost their salience, explain-away low-error
  ones, and surface anticipations proactively ("matches the Friday-deploy pattern;
  expect an incident within 2h"). Prediction-error becomes the unified currency for
  salience (C1), forgetting, and calibration (C3). Upgrades mnemos from *descriptive*
  to *anticipatory* — what an ops org actually wants. *(predictive-coding review
  arXiv 2107.12979)*
- **P2. Working-memory blocks + governed self-editing (MemGPT/Letta).** A small set
  of bounded, always-loaded, labeled per-worker blocks (`persona`, `working_context`,
  `open_threads`); self-edit tools that route through the candidate lifecycle — so
  edits land as candidates, *safer* than MemGPT's last-write-wins. Closes the
  working-memory + self-editing gaps together.
- **P3. Auto contradiction-invalidation + spreading activation.** On writing a claim
  that contradicts an existing one with overlapping valid-time, auto-set the older
  claim's `valid_to` (Graphiti's rule) as a supersede *candidate*. At recall, boost
  claims that are graph-neighbours of already-active context (ACT-R spreading
  activation) — reuses the edge table as a ranking signal.

---

## Recommended sequence

1. **R1 (hybrid retrieval)** — biggest baseline lift, no LLM, no new infra. Ship first.
2. **C1 (salience score)** — cheap, unlocks C2/forgetting, reuses chronos surprise.
3. **C2 (sleep/consolidation worker)** — the keystone; mostly *scheduling machinery
   that already exists*. This is where mnemos starts learning autonomously.
4. **R3 + C3** — corrective retrieval + calibration: correctness + trust.
5. Then the Tier-2 differentiators (prediction, working memory) once 1–3 compound.

**The through-line:** we spent this session making mnemos the *store* of the org
brain and waking its *perception* (chronos). The next arc is waking its *cognition*
— consolidation, salience, forgetting, calibration — most of which mnemos already
has the organs for. That is what turns "a bigger table" into "a brain that learns."
