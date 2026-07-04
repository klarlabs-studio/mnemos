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

The through-line: perception (temporal signals), encoding (salience), retrieval
(hybrid + corrective), consolidation (sleep), and metacognition (hypercorrection)
are the organs of a brain — and in Mnemos they run on your infrastructure, over
your data, with no model calls required.
