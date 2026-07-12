# ADR 0013: Cognitive completeness — the brain/NN mechanisms Mnemos still lacks

- **Status:** Accepted; all five mechanisms **implemented** — credit assignment
  (ADR 0014, PR #194), spreading activation (#192), curiosity (#193), salience
  (#195), schema-consistency assimilation (#191). Each landed bounded, additive,
  and (for the trust-touching ones) attributed. Credit + salience reuse the
  `confidence_components` map (no store-schema change).
- **Date:** 2026-07-12
- **Deciders:** Felix Geelhaar
- **Scope:** The cognitive core. Extends ADR 0011 (Complementary Learning Systems)
  and ADR 0012 (knowledge topology). This ADR audits Mnemos against the mechanisms
  of a biological brain and a neural network, records what is already covered, and
  specifies the genuine gaps as a prioritized roadmap.

## Context

Mnemos is deliberately built as **Complementary Learning Systems for agents** — a
symbolic, cited counterpart to a neural network's distributed memory (ADR 0011).
Auditing it against the brain/NN mechanism checklist shows unusually broad
coverage; the value of this ADR is naming the **specific mechanisms still missing**
so they can be built deliberately rather than rediscovered.

### Already covered (for the record)

Episodic + semantic memory (events/claims vs schemas); **consolidation** (the
offline "sleep" pass); **CLS** (hippocampus/neocortex at two scales, local and
hosted); generalization/promotion (cross-tenant corroboration); **active
forgetting** (trust half-life, `--forget-below-trust`); **reconsolidation**
(`verify`, supersession); **dissonance** (contradictions first-class);
**metacognition** (knowledge-gaps, calibration); **analogical recombination**
(recombinations, analogous-claims); **working memory** (blocks); **prospective
memory** (trigger-keyed reflexes); **prediction error** (`domain.Expectation`
mints a surprise scalar); **source memory** (who-knows); an **echo-chamber /
habituation guard** (independence-graded corroboration — `COUNT(DISTINCT author)`
not `COUNT(events)`); and **attention/retrieval** (embedding rerank, `--hops`
graph expansion). The differentiator throughout is **provenance-to-evidence** —
Mnemos borrows the brain's dynamics but never confabulates.

## Decision

Adopt the following five mechanisms as the **cognitive-completeness roadmap**,
ranked. **Credit assignment (1) is the capstone and should be built first** — it
is the missing half of learning.

---

### 1. Credit assignment — learning from consequences (CAPSTONE)

**The gap.** `RecomputeTrust` feeds on `confidence + distinct-sources + recency`
(`ListClaimTrustInputs`) — trust is purely **evidence-based**. `domain.Expectation`
surprise feeds *consolidation ranking* and `GlobalSchema.Surprise`, but it **never
updates a belief's trust**. So the learning loop is open: a belief whose decisions
repeatedly led to bad outcomes does not lose trust; one that led to good outcomes
does not gain it.

**The brain/NN basis.** This is the neural-network **gradient** and RL **temporal
credit assignment**: update the weights (trust) of the units (beliefs) proportional
to their contribution to the error (outcome vs expectation). Dopaminergic
prediction error is exactly this signal in the brain.

**Why Mnemos is uniquely able.** It already stores the full chain —
`Decision → Beliefs[] (ClaimIDs) → Outcome`, with `Expectation` surprise. No vector
store can do this, because none store the prediction *or* the decision that relied
on it.

**Proposed mechanism.** On expectation/outcome reconciliation, compute the error
(surprise, signed by validates/refutes) and **propagate it to the backing beliefs'
trust**: beliefs that informed a decision whose outcome confirmed their prediction
gain trust; those whose predictions were refuted lose it. Bounded, decayed, and
attributed (a `belief_credit` audit trail preserving provenance — never a silent,
unexplained trust change). Distribute credit across a decision's beliefs (equal, or
weighted by how load-bearing each was). Result: Mnemos learns from *being wrong*,
not only from *being corroborated* — it gets **better at being right** over time.

**Priority: HIGHEST.** Build first (own ADR + implementation).

---

### 2. Spreading activation — associative priming at retrieval

**The gap.** Retrieval is embedding-similarity + explicit `--hops` graph traversal.
There is no *priming*: recalling one belief does not raise the accessibility of its
associated beliefs.

**The brain basis.** Spreading activation (Collins & Loftus): activation flows
through the association graph; recently/frequently co-activated memories become more
retrievable. It is why one thought cues the next.

**Proposed mechanism.** At query time, seed activation on the top retrieved beliefs
and spread it over `Association` edges (decaying per hop, weighted by edge type and
co-activation history), then blend the activation score into ranking. Mnemos already
has the association edges — this is a retrieval re-weighting, not new storage.

**Priority: MEDIUM.**

---

### 3. Curiosity — gap-driven active acquisition

**The gap.** Mnemos *knows what it does not know* (knowledge-gaps, calibration) but
does not *act* on it. Metacognition is passive.

**The brain/NN basis.** Curiosity as intrinsic motivation / active learning: seek
information that most reduces uncertainty; prioritize learning where the model is
least confident.

**Proposed mechanism.** Turn knowledge-gaps into an **acquisition signal**: surface
"what to learn / verify next" (agent-facing questions, or a prioritized re-verify
queue targeting high-uncertainty, high-salience beliefs). Converts passive
metacognition into active learning and closes the loop with reconsolidation.

**Priority: MEDIUM.**

---

### 4. Unified salience / stakes

**The gap.** Prediction-error and trust exist, but there is no consequence-*severity*
dimension. All beliefs of equal trust are treated equally regardless of stakes.

**The brain basis.** Amygdala salience tagging: high-stakes memories consolidate
more strongly and are recalled preferentially.

**Proposed mechanism.** A `salience` weight derived from outcome severity, decision
`risk_level`, or explicit marking, biasing **both** retrieval and consolidation. In a
domain like pet-medical, a life-threatening fact should outrank a trivial one
independent of corroboration.

**Priority: MEDIUM-LOW.**

---

### 5. Schema-consistency fast-assimilation

**The gap.** Dissonance (the *negative* side of schema fit) is first-class, but the
*positive* effect is missing: info consistent with an existing schema is not
consolidated faster.

**The brain basis.** Schema assimilation vs accommodation (Piaget; Tse et al.
schema-consistency effect): schema-consistent information integrates rapidly;
inconsistent information either updates the schema or is rejected.

**Proposed mechanism.** When a new claim matches an existing schema, **accelerate**
its consolidation / raise its promotion priority (assimilation); when it conflicts,
route to the existing dissonance-resolution path (accommodation). The positive
complement to the contradiction engine.

**Priority: LOW.**

---

## Consequences

**Positive**
- A concrete, ranked plan for the remaining "learn like a brain" mechanisms, each
  grounded in existing Mnemos primitives (Expectation, Decision.Beliefs,
  Associations, knowledge-gaps, RecomputeTrust) — actionable, not aspirational.
- Credit assignment (1) makes the system *improve* with use, the missing half of
  learning; it is the highest-leverage next build.

**Negative / open**
- Credit assignment must stay **auditable** — trust changes carry provenance
  (which outcome, which decision drove them), never silent gradient noise; this is
  the ADR-0011 "borrow dynamics, keep the citation trail" guardrail applied to
  learning.
- Salience and curiosity add configuration surface; keep them small and typed.

## Rollout

**All five shipped (2026-07-12).** One follow-up remains: credit + salience persist
into `confidence_components`, which sqlite/libsql/memory round-trip but the
Postgres/MySQL claim repos do not yet — so those two trust-updating mechanisms are
currently a no-op on the hosted backend (they work fully on the local/default
sqlite path). The fix is a repo-mapping change only (the column already exists) plus
a `BeliefCreditWriter` implementation for pg/mysql — tracked as the next store task.

1. **Credit assignment** — its own ADR + the outcome→belief-trust loop (reconcile
   expectations/outcomes → propagate bounded, decayed, attributed trust deltas to
   backing beliefs). First.
2. **Spreading activation** at retrieval.
3. **Curiosity** (gap-driven acquisition/re-verify queue).
4. **Salience** dimension.
5. **Schema-consistency assimilation.**
