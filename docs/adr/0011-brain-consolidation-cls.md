# ADR 0011: Mnemos as a brain — Complementary Learning Systems, consolidation, and the ubiquitous language

- **Status:** Accepted; implemented (Phases A–D)
- **Date:** 2026-07-11
- **Deciders:** Felix Geelhaar
- **Scope:** The whole system. This is the organizing principle the rest of the
  roadmap hangs off. Builds on ADR 0007 (per-tenant scoping), ADR 0009 (hosted
  repo-as-tenant federation), and the existing cognitive surface (synthesize,
  consolidate, expectation, corroboration, calibration, knowledge-gaps).

## Context

Mnemos is used in two shapes: a **local brain** (a developer's Claude Code
sessions) and a **hosted agentic brain** powering products (pet-medical,
kraftsprot). The local shape is a side benefit — dogfooding and distribution.
The business is the hosted shape: **a fleet of tenant-scoped agents that each
operate in their own context but draw on, and feed, a shared global brain** — an
agent that gets better for everyone as it is used by anyone, with no tenant's
data leaking.

Two observations make this precise:

1. **Mnemos is already unusually brain-like.** It has an episodic/semantic split
   (events/claims vs synthesized lessons), an offline "sleep" pass
   (`consolidate`), active forgetting (trust half-life, `--forget-below-trust`),
   dissonance (contradictions as first-class), metacognition (knowledge-gaps,
   calibration), analogical recombination, working memory (blocks), prospective
   memory (trigger-keyed playbooks), and — critically — a **prediction-error**
   primitive (`domain.Expectation` mints a *surprise* scalar from expected-vs-
   observed). The vocabulary was reaching for a model it never named.

2. **The differentiator is truth-maintenance, not memory.** A vector store
   remembers; Mnemos adjudicates — provenance to evidence, contradiction
   detection, trust decay, supersession, temporal validity. That is what makes
   the shared-learning story safe and defensible.

## Decision

Adopt **Complementary Learning Systems (CLS)** — neuroscience's two-store model
of learning — as the organizing principle for Mnemos, and align the ubiquitous
language with it.

### 1. The two-store model

- **Hippocampus** = a tenant's fast, specific, episodic memory (that tenant's
  events + claims). One per tenant (ADR 0007 isolation).
- **Neocortex** = the slow, generalized, semantic global brain (promoted,
  abstracted knowledge every tenant's agent inherits).
- **Consolidation** = the offline pass that bridges them, replaying recurring
  episodes and integrating the generalizable ones into neocortex.

Reads federate **neocortex ∪ hippocampus** (already built: ADR 0009 federation +
the tenant-allowlist token). Writes are tenant-scoped by default (built). The
missing, and defining, flow is **consolidation: hippocampus → neocortex**.

### 2. Consolidation / promotion (the product)

An offline pass (an extension of `consolidate`, never the hot path) that promotes
tenant-learned knowledge to global. Neuroscience supplies both the mechanism and
the safety constraints:

- **Runs offline, per cycle** — a scheduled "sleep", not request-time.
- **Replay + recurrence** — only knowledge that recurs is a candidate. The
  cross-tenant analogue of replay across nights is **cross-tenant
  corroboration**: a schema independently produced in ≥N tenants.
- **Cross-tenant corroboration is simultaneously the quality signal and the
  privacy gate.** If a lesson arose independently in N tenants, it is (a) high
  confidence and clearly generalizable and (b) provably not any single tenant's
  secret. This is the keystone idea.
- **Gist, not episode** — neocortex stores abstractions. Promotion
  **de-identifies**: strips tenant specifics / PII / identifiers, keeping the
  generalized statement. A promoted schema carries corroboration counts and
  abstracted evidence, never raw tenant events.
- **Schema-consistent integrates; contradictory triggers conflict** — a
  promotion candidate is checked against existing neocortex via the contradiction
  engine. Consistent → integrate/reinforce; contradictory → do not silently
  overwrite; record the dissonance for resolution (operator or a later cycle).
- **Prediction-error weighting (the prioritizer).** Consolidate hardest where the
  agent was *surprised*: `domain.Expectation`'s surprise scalar (predicted vs
  observed on a decision/hypothesis) ranks what promotes first. A decision whose
  predicted outcome missed is exactly where learning lives — and no vector store
  can do this, because none store the prediction. This is a moat inside the moat.
- **Deliberate override (prefrontal gate).** Promotion trust policy is per
  product: **operator-gated** for regulated domains (a vet signs off before a
  schema goes global in pet-medical) or **auto** above a corroboration/surprise
  threshold (kraftsprot). Same mechanism, different gate.

### 3. Read-time precedence (differs from the local default)

In the local brain the tenant/repo tier wins on conflict (more specific). In a
**product** brain, vetted neocortex truth should not automatically lose to a
tenant's idiosyncratic claim. Precedence is a **policy**, per deployment:

- `tenant-wins` (local/dev default, unchanged),
- `global-wins` (vetted-domain default, e.g. pet-medical), or
- `surface-dissonance` — neither silently wins; the conflict is returned to the
  agent to reconcile before acting.

### 4. Isolation & auditability guarantees

- **No-leak, provably.** The read direction is already guarded (cross-tenant
  RLS/namespace tests). Promotion needs the same rigor in the *write-up*
  direction: a promoted item must be traceable to ≥N distinct tenants and carry
  no tenant-specific payload. A guardrail test (a fact present in exactly one
  tenant must never appear in neocortex) is a merge gate, like the ADR-0007
  isolation suite. For regulated domains this may be a compliance requirement.
- **A brain that cites its sources.** We borrow the brain's *dynamics*
  (consolidation, forgetting, reconsolidation, dissonance, prediction-error
  salience) but keep the machine's *citation trail*. Mnemos must never confabulate
  — provenance-to-evidence is the advantage. Where the brain is simply worse than
  a database (false memories, hindsight bias, unreliable recall), we do not
  imitate it. The metaphor is a generative source of mechanisms and a coherence
  check, not a spec.

### 5. Ubiquitous language (the rename)

Align the vocabulary with the domain (DDD ubiquitous language). Two layers with
very different blast radius, sequenced accordingly:

- **Narrative / internal types** — docs, ADRs, Go identifiers. Adopt now.
  Non-breaking: introduce the brain name as the canonical Go type and keep a
  back-compat alias (`type Event = Episode`) during migration.
- **Public API / wire** — MCP tool names, REST paths, JSON field tags, DB
  columns, CLI verbs, config keys. This is a *released contract* that
  pet-medical/kraftsprot call. It changes only via a **planned API v2** with v1
  aliases kept alive, so product clients migrate on their own schedule. JSON
  tags / DB columns stay put behind renamed Go identifiers until v2.

**Mapping — renames that add meaning:**

| Now | Brain term | Rationale |
| --- | --- | --- |
| Event | **Episode** | episodic memory — a specific "this happened" |
| Claim | **Belief** | a held assertion; pairs with Evidence to keep auditability (`Decision.beliefs` already uses the word) |
| Relationship (edge) | **Association** (a `contradicts` edge = dissonance) | associative memory |
| Contradiction | **Dissonance** | cognitive dissonance |
| Playbook | **Reflex** | procedural memory: cue → automatic response |
| Lesson | **Schema** | schema theory — a consolidated generalization |
| Scope | **Context** | contextual memory |
| `verify` | **reconsolidate** | recall makes a memory editable, then re-stabilizes |
| global vs tenant brain | **neocortex** vs **hippocampus** | the CLS split, as product vocabulary |

**Kept deliberately — the machine term beats the brain:**

- **Evidence** — the brain has no word for "citation", and citation is the moat.
- **Embedding** — a mechanism, not a concept.
- **Action / Outcome / Decision** — already brain-adjacent (motor / feedback /
  prefrontal deliberation); optionally `trust_score → confidence` later.
- **`consolidate`** — already named correctly.

## Consequences

**Positive**
- One coherent identity: *Complementary Learning Systems for agents* — fast
  tenant-local episodic memory, slow global semantic memory, bridged by a
  prediction-error-weighted consolidation pass that promotes only recurring,
  corroborated, de-identified, non-contradictory knowledge — and cites every
  step. That is the homepage sentence and the roadmap.
- The flywheel is ~90% present in primitives (events→claims, actions→outcomes→
  lessons→playbooks, decisions with beliefs, `Expectation` surprise,
  corroboration, `consolidate`). The defining 10% is the consolidation/promotion
  gate.
- The tenant-allowlist federation (ADR 0009 Phase 2/3) is exactly the substrate
  the shared brain needs — nothing there is wasted.

**Negative / open**
- Promotion is the highest-leverage *and* highest-risk piece; the no-leak
  guarantee must be provable, not asserted.
- The rename is broad; done wrong it costs the epistemic precision that is a
  feature. Mitigated by keeping Evidence, aliasing during migration, and gating
  the public wire behind v2.
- Per-product policy (promotion gate, read precedence) adds configuration
  surface; kept to a small enum, not free-form.

## Rollout

- **Phase A — vocabulary (non-breaking): DONE.** Brain terms are the canonical Go
  types (`Episode`/`Belief`/`Association`/`Reflex`/`Schema`/`Context`), back-compat
  aliases retained, `reconsolidate` added; public wire unchanged.
- **Phase B — consolidation/promotion: DONE.** `consolidate --promote` runs the
  hippocampus→neocortex pass (cross-tenant corroboration → token-level
  de-identification → fail-closed contradiction → prediction-error ranking →
  operator/auto gate). `--apply` persists a first-class `domain.GlobalSchema`
  (counts-only provenance) to the shared neocortex tier (deliberately un-scoped by
  RLS); the operator gate stages `pending` and `--promote approve <id>` activates.
  Prediction error is the direct Lesson→Expectation edge. Guarded by the
  cross-tenant no-leak tests on both the compute and the write path.
- **Phase C — read-time precedence policy: DONE.** `MNEMOS_PRECEDENCE` selects
  `tenant-wins` (default) / `global-wins` / `surface-dissonance`, applied at the
  hook and MCP federation merges.
- **Phase D — API v2: DONE (REST).** A brain-native `/v2` REST surface
  (`beliefs`/`episodes`/`associations`/`schemas`/…) is served alongside the
  byte-identical v1 via a capture-and-remap delegate that reuses the exact v1
  handlers. MCP and gRPC brain-aliasing are deferred follow-ups (renaming their
  live I/O risks existing consumers); DB columns and config keys are unchanged.
