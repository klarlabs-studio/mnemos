# Cognitive layer

Most memory systems are a *store*: they write and they read. Mnemos also runs the
background processes a brain runs — it **consolidates**, **forgets**, **weighs
importance**, **self-corrects retrieval**, and **flags when new evidence
challenges an established belief**. None of it needs an LLM; all of it is
deterministic and self-hostable.

This is the layer that turns "a bigger table" into "a memory that learns."

## The sleep pass — consolidation + forgetting

Call `Consolidate` off the hot path (a nightly cron is typical). In one pass it:

- **Gist** — collapses near-duplicate claims into one canonical claim, repointing
  the duplicates' evidence onto the survivor. Nothing is lost; the store stops
  bloating with restatements of the same fact.
- **Renormalise** — recomputes trust so freshly-merged evidence counts are
  reflected.
- **Forget** — invalidates claims whose trust has decayed below a floor and that
  aren't reinforced. Forgetting is **reduced retrievability, not erasure**: the
  claim and its history are kept (a point-in-time query still sees what was once
  believed), it just stops surfacing in recall. Human-**promoted** claims are
  never forgotten.

```go
res, _ := mem.Consolidate(ctx, mnemos.ConsolidateOptions{
    ForgetBelowTrust: 0.05, // 0 disables forgetting (dedupe-only)
})
// res.Merged, res.TrustRefreshed, res.Forgotten
```

This mirrors what the brain does during slow-wave sleep — hippocampus→neocortex
gist transfer plus synaptic renormalisation that prunes weak traces.

## Salience — importance that resists forgetting

Trust answers *"is this still true?"* and decays. **Salience** answers *"does this
matter enough to keep, even if rarely recalled?"* and does **not** decay. It is
scored from signals already on a claim — confidence, corroboration, claim type (a
decision or verified test result outweighs a bare fact), source authority,
verification — the rule-based analog of an LLM "importance" rating, with no LLM.

The sleep pass consults it: a consequential-but-quiet claim (a Sev-1 post-mortem,
a high-authority decision) **survives** forgetting even as its trust fades, while
the mundane low-salience tail is pruned. Importance and freshness are separate
axes, exactly as they are in human memory.

## Hybrid retrieval — dense + sparse, fused by RRF

Cosine similarity blurs exact tokens — SHAs, service names, error codes, flag
names. Mnemos fuses the dense embedding leg (pgvector `<=>`) with a sparse
full-text leg (Postgres `tsvector` / SQLite FTS5) by **Reciprocal Rank Fusion**.
RRF consumes only ranks, so the two incomparable score scales combine without
tuning, and a result strong in *both* legs outranks one that tops a single leg.
An identifier is now recallable even when the query is semantically distant from
it. Auto-wired by capability; no configuration.

## Corrective retrieval — recall that grades itself

When the first recall pass comes back weak — no claims, or aggregate confidence
below a floor — Mnemos makes **one** bounded corrective pass: it widens the narrow
top-K to the whole corpus and relaxes the soft trust filter, then keeps whichever
answer is stronger. It's a CRAG-style gate: bounded to a single retry (so it
respects a tool-call budget), only fires on a weak first pass, and is strictly
**non-regressive** — it can only improve the answer or be discarded. Semantic
filters (scope, lifecycle, visibility, as-of) are never relaxed.

## Hypercorrection — when new evidence challenges a settled belief

When a new claim contradicts an **established** one (human-promoted, or high
trust), that's the epistemic event most worth a human's attention — either the
old belief is now wrong, or the new claim is suspect. `Hypercorrections` surfaces
exactly those conflicts, most-established-first — the front of the review queue.
Ordinary churn between two unvetted claims is *not* an alert.

```go
alerts, _ := mem.Hypercorrections(ctx)
// each: the contradicted (established) claim + the challenging claim
```

Resolve one by retiring a side — `SetClaimLifecycle(id, superseded)` on the stale
belief (accept the new evidence) or on the challenger (dismiss it). History is
preserved; the alert clears.

Named for the *hypercorrection effect*: high-confidence errors, once caught, are
corrected most strongly.

---

## Toward a brain — the prediction, learning, executive, and connected loops (v0.42–v0.60)

The capabilities above are the *organs*. A second arc added the *nervous system*: one
signal — **prediction error** — tying them into loops so the store gets *better with
use*. All deterministic, no LLM. Full detail in
[`RESEARCH-perfect-agent-brain.md`](https://github.com/klarlabs-studio/mnemos/blob/main/docs/RESEARCH-perfect-agent-brain.md).

**Epistemic honesty.** Corroboration is graded by source **independence**
(`EffectiveEvidenceCount`) so a single voice can't manufacture consensus, and
calibration is **per-source** (`Calibration.Sources`) so an over-confident author is
visible.

**The prediction loop.** A claim can carry a numeric **expectation** —
`mem.Expect(claimID, predicted, tolerance, horizon)`. When the value arrives,
`RecordObservation` + `ReconcileExpectations` close it into a validates/refutes verdict
and a **surprise** scalar, which the sleep pass routes both ways: confirmed beliefs kept
fresh (`ReinforceValidated`), refuted ones forgotten (`ForgetRefuted`). `KnowledgeGaps`
ranks the store's weak spots — unresolved hypotheses, contested claims — worth chasing next.

**The learning loop.** The sleep pass rehearses the most salient memories (`ReplayTopK`),
auto-derives skills from experience (`Synthesize`: actions-with-outcomes → lessons →
playbooks), reinforces each playbook by its real outcome success rate
(`ReinforcePlaybooks`), and surfaces novel recombinations and schema fits
(`Recombinations`, `ClassifyClaim`).

**The executive.** Bounded, always-loaded **working-memory blocks** (`mem.Blocks` /
`SetBlock` / `AppendBlock`) — the agent's core memory. A **feeling-of-knowing** gate
(`RecallWithSufficiency`) lets a caller abstain instead of confabulating when memory is
thin. **Effort budgeting** (`RecallWithEffort`) spends retrieval in proportion to stakes ×
uncertainty. **Spreading activation** (`RecallWithContext`) biases recall toward the
current train of thought.

**The connected brain.** A transactive **who-knows-what** directory (`WhoKnows`) routes a
gap to the right expert-memory. **Analogical retrieval** (`AnalogousClaims`) finds past
situations with the same *causal shape* via Weisfeiler-Lehman fingerprinting over the
typed graph — something a pure-vector store cannot do. The **contested frontier**
(`RecallWithConflicts`) makes a recall carry its own live counter-evidence, and
`RecallIterative` runs recall as a bounded retrieve↔reason fixpoint.

---

The through-line: perception (temporal signals), encoding (salience), retrieval
(hybrid + corrective + contextual), consolidation (sleep), prediction (expectations →
surprise), learning (skills), the executive (working memory + abstain + effort), and the
connected brain (routing + analogy) are the organs *and nervous system* of a brain — and
in Mnemos they run on your infrastructure, over your data, with no model calls required.
