# ADR 0015: Learning dynamics — replay, association plasticity, and neuromodulation

- **Status:** Accepted; phased rollout.
  - **Batch 1 (v0.90.0, shipped) — learning-rate cluster, no schema change:**
    metaplasticity, neuromodulation/volatility, replay-with-surprise +
    forgetting-protection.
  - **Batch 2a (v0.91.0, shipped) — association-plasticity core:** the `strength`
    column on the association graph (all backends), Hebbian co-activation write-back
    (`query --hebbian`), and strength-weighted spreading activation.
  - **Batch 2b (shipped) — reconsolidation + Hebbian decay:** retrieval as a freshness
    touch (`query --reconsolidate`) and consolidation-time decay of over-base edge
    strength toward the base (`consolidate --decay-associations`). Edge *pruning* was
    dropped and *competitive inhibition* deferred — see the §6 note below.
- **Date:** 2026-07-12
- **Deciders:** Felix Geelhaar
- **Scope:** The cognitive core's *learning dynamics* — how memory **changes over
  time** and **protects itself**. Extends ADR 0013 (cognitive-completeness roadmap)
  and ADR 0014 (credit assignment).

## Context

ADR 0013 audited Mnemos against the brain/NN mechanism checklist and shipped five
mechanisms. That pass optimized for **inference-time** mechanisms (retrieval,
priming, salience) and the **learning signal** (credit assignment). What it
under-covered is the **learning dynamics**: how beliefs and their associations
change, strengthen, resist change, and are actively forgotten over repeated use —
the plasticity rules a brain runs *between* encoding and retrieval.

ADR 0013 does list "active forgetting" and "reconsolidation" as covered, but it
counts the **passive** forms: trust half-life decay (`--forget-below-trust`) and
explicit `verify`/supersession. This ADR specifies the **stronger, dynamic** forms
those names properly denote — *competitive* forgetting (recalling a winner
suppresses its rivals) and *retrieval-triggered* reconsolidation (every recall is
a write opportunity) — and is explicit about the overlap so nothing is double-counted.

### Why now

We adopted **Complementary Learning Systems** as the framing (ADR 0011). CLS in the
ML literature (McClelland et al., 1995) exists precisely to solve **catastrophic
forgetting** via **interleaved replay** — the neocortex learns slowly, replaying old
experience so new knowledge does not overwrite old. Mnemos consolidates and promotes
but does **not** replay with interference protection. That is the single largest gap
from the frame we chose, and — usefully — the priority signal it needs (salience +
surprise) already exists as of ADR 0013/0014. The rest of this ADR fills the
neighbouring gaps that make the learning loop biologically complete.

## Decision

Adopt five learning-dynamics mechanisms, plus predictive coding as a documented
**north star** (not built now). Each is bounded, additive, opt-in where it changes
behaviour, and — for the trust/association-touching ones — attributed, honouring the
ADR-0011 "no silent trust change" guardrail. Mechanisms split into two batches by
whether they require a store-schema change.

---

## Batch 1 — learning-rate cluster (no schema change)

These three converge on the consolidation path (`memory_impl.go`: `assignCredit`,
`replayTopK`) and reuse existing fields (`LastVerified`, `VerifyCount`, `CreatedAt`,
`confidence_components`, `Outcomes`/`Expectations`). No new columns.

### 1. Metaplasticity — crystallization (plasticity of plasticity)

**The gap.** Credit assignment (ADR 0014) applies the same-magnitude trust nudge to
a belief regardless of how long it has been stable. A brain does the opposite: a
memory that has been consistent for a long time becomes **harder to overturn**
(consolidated → crystallized). This is *metaplasticity* — the plasticity itself
decays with stability. Distinct from trust half-life, which governs *decay of
strength*, not *resistance to update*.

**Mechanism.** A per-belief **update-resistance factor** `r ∈ [r_min, 1]` derived
from stability (age since `CreatedAt`, `VerifyCount`, recency of `LastVerified`)
scales the credit delta *before* it is applied: `applied = raw_delta · r`. A young
or rarely-verified belief updates at full rate (`r → 1`); a long-stable,
often-verified one resists (`r → r_min`). **Blame (negative surprise) is scaled less
than credit (positive)** — a crystallized belief should still be *breakable* by
strong disconfirmation (avoids fossilization), matching the asymmetry that
disconfirmation is more informative than confirmation.

**Where.** In the I/O shell `assignCredit` (`memory_impl.go`, where the claim's
stability fields are already in hand), scaling `credit.SumFor(fresh)` — the pure
`internal/credit` engine stays untouched. Config-tunable floor (see §3 pattern).

### 2. Neuromodulation — global plasticity from volatility

**The gap.** Curiosity (ADR 0013 §3) is a *per-belief* uncertainty signal. Missing
is the **global** neuromodulatory mode switch (Yu & Dayan, 2005): distinguish
**expected uncertainty** (known noise — stay the course) from **unexpected
uncertainty** (the environment *changed* — raise the learning rate). Acetylcholine /
norepinephrine implement this gain control in the brain; a high-volatility regime
should make the whole system more plastic (encode mode), a stable one more
conservative (consolidate mode).

**Mechanism.** A **volatility detector** computes a scalar from the recent surprise
time-series (`Outcomes.ListAll` is time-ordered; `Expectation.Surprise()` /
`ReconcileExpectations` mint the per-event error). Rising surprise variance ⇒ high
volatility ⇒ a **global plasticity gain** `g ≥ 1` that multiplies credit's learning
rate (`credit.Step`); a stable series ⇒ `g → 1` (nominal) or below (conservative).
The gain is bounded and composes multiplicatively with §1's per-belief resistance:
`applied = raw_delta · r_belief · g_global`. Fully auditable — the gain and the
volatility it derived from are logged, and the applied factor is recorded in the
`credit:` provenance.

**Where.** New `internal/plasticity` package (pure volatility computation) consumed
in the consolidation credit stage. New `MNEMOS_PLASTICITY_*` config leaves following
the `MNEMOS_FEEDBACK_DECAY` end-to-end pattern (struct field → `EnvOverrides()` →
`mnemos.example.yaml` → `envFloat`). Off/nominal by default (`g = 1`).

### 3. Replay — prioritized experience replay + interference protection

**The gap.** `replayTopK` (`memory_impl.go`) is single-pass: sort by
`salience · recency`, take top-K, rehearse (freshness bump). It does **not** use
**surprise** in the priority, does **not interleave** (the CLS anti-catastrophic-
forgetting mechanism), and its rehearsal is **not coupled to forgetting-protection**.

**Mechanism.**
- **Prioritized replay:** priority becomes `salience ⊕ surprise` (the ADR-0015
  framing the recon confirmed is missing) — high-stakes *and* high-surprise beliefs
  replay first, mirroring prioritized experience replay (Schaul et al., 2016).
- **Interleaved consolidation:** replay is **interleaved** across schema/topic rather
  than a flat top-K sweep, so consolidating new knowledge re-presents old
  neighbouring knowledge in the same pass — the interference protection CLS is *for*.
- **Forgetting-protection coupling:** a belief that has been **replayed/consolidated**
  is protected from the same-pass forget stage (extending the existing
  `Lifecycle == promoted` skip and the `salienceProtectFloor` soft-keep in
  `forgetStaleClaims`). Replay and forgetting become one coherent rehearsal↔prune
  cycle, not two independent stages.

**Where.** `replayTopK` + the `Consolidate` stage ordering (`memory_impl.go`). New
`consolidate --replay` / interleaving option alongside `--replay-top-k`. No schema
change (protection uses existing lifecycle + a `confidence_components`-recorded
replay marker if a persisted signal is wanted, per the no-migration pattern).

---

## Batch 2 — association-plasticity cluster (adds `strength` to the graph)

The association graph (`domain.Association`/`Relationship`) is today a pure typed
adjacency record — **no strength/weight/count field on any backend**. These three
mechanisms make associations *plastic*. They share one schema change: a
`strength` (and/or `activation_count`) column on `relationships`, added across
**all** backends (sqlite + sqlc regen, postgres, mysql, memory) with the store-parity
discipline — every inlined `rows.Scan` updated in lockstep (there is no shared
scanner), verified against **live Postgres + MySQL** (the ADR-0007/#196 gate).

> **Parity prerequisite (must fix first).** The Postgres relationship upsert conflicts
> on `ON CONFLICT (id)`, not on the edge tuple `(type, from_claim_id, to_claim_id)`
> as sqlite/mysql do. Because detection mints a fresh random `id` per edge, a repeated
> edge on Postgres never hits the conflict target and would **error against the unique
> edge index** — so Hebbian strengthening would silently fail (or break inserts) on the
> hosted backend. Batch 2 **must** first re-point the Postgres upsert to the edge tuple
> to match sqlite/mysql, guarded by a live-Postgres test.

### 4. Hebbian co-activation — "fire together, wire together"

**The gap.** Spreading activation (ADR 0013 §2) *reads* the association graph by edge
**type** only; edges never strengthen with use. Co-retrieving two beliefs in the same
answer says something ("these belong together") that is currently discarded.

**Mechanism.** After an answer is finalized, the co-retrieved belief set strengthens
its pairwise association edges (`strength += increment`, bounded, decaying). Spreading
activation's per-edge multiplier then becomes `edgeActivationWeight(type) · norm(strength)`
so well-worn associations prime more strongly. Write-back is an **opt-in, type-asserted
optional capability** (the `BeliefCreditWriter` precedent), placed at the
`AnswerWithOptions` level — **not** inside `answerWithEvents`, which runs twice under
corrective retrieval — so it fires once, on the answer actually returned.

### 5. Reconsolidation — retrieval as a write opportunity

**The gap.** The query path is strictly read-only; recall never updates the recalled
belief. Biological memory reconsolidation says a retrieved memory goes **labile** and
is re-stored, possibly modified with current context.

**Mechanism (as shipped in 2b).** On recall, the top co-retrieved beliefs get a
`MarkVerified` recency touch — freshness/liveness only, **never a trust jump** — so
retrieval keeps a memory alive against decay (the testing effect). Opt-in
(`query --reconsolidate` / `MNEMOS_RECONSOLIDATE`), single-shot at the
`AnswerWithOptions` seam it shares with §4's write-back, bounded to the same top-N
focus, best-effort. Distinct from explicit `verify` (which ADR 0013 counted): this is
*automatic* on retrieval, not an operator action. (Refreshing `confidence_components`
from query context was considered and left out to keep the read path from ever moving
trust implicitly.)

### 6. Active forgetting — competitive inhibition + edge pruning

**The gap.** Forgetting today is *passive* (half-life) and *non-competitive*. Two
brain mechanisms are missing: **retrieval-induced forgetting** (recalling a winner
*suppresses* its rivals on the same topic) and **synaptic pruning** (weak, unused
edges are dropped, not merely decayed).

**Mechanism (as originally specified).**
- **Competitive inhibition:** the contradiction-winner selection already computed at
  retrieval (`resolveContradictionsForAgent` picks a winner and demotes losers in the
  in-memory slice) is *persisted* as a bounded suppression of the losing claims'
  liveness/salience — recalling the winner actively weakens the rival, attributed.
- **Edge pruning:** associations whose `strength` stays below a floor across
  consolidation passes are pruned, bounding graph growth.

**What actually shipped in 2b, and why it diverged.** Implementing 2a surfaced a design
fact: mnemos's association edges are **all meaningful, evidence-derived *typed*
relationships** (supports / contradicts / causes / …), and the 2a Hebbian design
deliberately **never creates co-occurrence edges** — it only *strengthens existing
typed ones*. So there are no "spurious co-occurrence edges" to prune; pruning by a
strength floor would only ever delete real, cited knowledge. Edge pruning was therefore
**dropped** as inappropriate for this graph. Its safe, principled substitute shipped
instead: **Hebbian decay** — `consolidate --decay-associations` pulls each over-base
edge's strength back *toward* the base 1.0 (keeping a retain fraction of the excess each
pass, asymptotic, never below base, never deleted), so strength tracks **recent** use
rather than a lifetime tally. Combined with the §4 write-back, associations now
strengthen on use and fade on disuse — the full Hebbian loop, without losing edges.

**Competitive inhibition is deferred.** Persisting a suppression of contradiction
losers on the read path is the most invasive remaining piece (it mutates trust/liveness
of claims the user did not act on, and threads through the agent contradiction-
resolution path). It is left for a later ADR rather than rushed onto the read seam.

---

## North star (documented, not built)

**Predictive coding — a hierarchical generative model.** The deepest reframe: each
level of the memory hierarchy *predicts* the level below, and only the **residual
prediction error** propagates upward (Rao & Ballard, 1999; Friston free-energy).
Mnemos has `Expectation → surprise` at the **decision** level only. Making prediction
error *hierarchical* — schemas predicting claims predicting events, consolidation
minimizing total surprise across levels — would unify credit assignment, salience,
curiosity, and consolidation under one objective. It is named here as direction; it
is a research-scale change, deliberately **out of scope** for this ADR's batches.

## Consequences

- **Positive.** Completes the CLS frame (interleaved replay + interference
  protection); makes associations plastic (Hebbian + pruning) so the graph reflects
  *use*, not just *detection*; adds the two neuromodulatory controls (per-belief
  metaplasticity, global volatility gain) that let learning rate adapt to stability
  and change; closes the retrieval→write loop (reconsolidation, co-activation,
  competitive inhibition) that a read-only recall path leaves open.
- **Guardrails preserved.** Every trust/association change is bounded, decayed, and
  attributed; write-back is opt-in and type-asserted so backends that cannot persist
  it skip rather than mutate silently; no mechanism bypasses the promotion no-leak or
  eligibility gates.
- **Risk — concentrated in Batch 2's schema change.** The `strength` column is the
  store-parity trap (9 column-synced sites, no shared scanner, sqlc regen, a latent
  Postgres conflict-target bug). Mitigated by shipping Batch 1 (no schema change)
  first and gating Batch 2 on live Postgres + MySQL round-trip tests, exactly as #196
  closed the `confidence_components` round-trip.
- **Negative.** More configuration surface (plasticity, replay, write-back toggles) —
  keep each small, typed, and defaulted to current behaviour. Query write-back adds
  a bounded write to a previously read-only path — kept single-shot and opt-in.

## Rollout

1. **Batch 1 — metaplasticity, neuromodulation, replay** (v0.90.0, shipped). No schema
   change; all in the consolidation path + a new `internal/plasticity` package + config.
2. **Batch 2a — the `strength` column + Hebbian + weighted spreading** (v0.91.0,
   shipped). Added `relationships.strength` (DEFAULT 1) across sqlite (versioned
   migration v21), postgres, mysql (inline in CREATE TABLE), and memory; the
   `ports.RelationshipStrengthener` capability (`StrengthenAssociations`); the
   `query --hebbian` / `MNEMOS_HEBBIAN` write-back at the `AnswerWithOptions` seam; and
   strength-weighted spreading (a base-1 edge stays neutral, so priming is unchanged
   until edges wire). No sqlc change — the Upsert relies on the column DEFAULT and
   there is no shared scanner to force column-parity beyond `ListByClaimIDs` (pg/mysql
   do share `collectRelationshipRows`, so all three of their SELECTs were updated in
   lockstep). Verified on live Postgres + MySQL. The pre-existing Postgres
   `ON CONFLICT (id)` edge-upsert quirk was left untouched: 2a strengthens via a
   dedicated UPDATE, not a re-Upsert, so it does not depend on the conflict target.
3. **Batch 2b — reconsolidation + Hebbian decay** (v0.92.0, shipped). `query
   --reconsolidate` / `MNEMOS_RECONSOLIDATE` re-marks the top recalled beliefs
   verified-now (liveness only, never trust) at the same `AnswerWithOptions` seam as
   §4; `consolidate --decay-associations` pulls over-base edge strength toward the base
   via the new `ports.RelationshipStrengthener.DecayAssociations` (all backends,
   verified on live pg + mysql). **Edge pruning was dropped** (mnemos has no spurious
   co-occurrence edges to prune — see §6) and **competitive inhibition deferred** to a
   later ADR.
4. **Predictive coding** — north star; revisit as its own ADR when the batches settle.
