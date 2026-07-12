# ADR 0012: Subject-classified promotion — individual stays private, class can go global

- **Status:** Accepted; extends ADR 0011 (brain consolidation / CLS)
- **Date:** 2026-07-12
- **Deciders:** Felix Geelhaar
- **Scope:** The hosted-brain promotion gate — the hippocampus→neocortex
  consolidation pass (`internal/consolidate`). Builds directly on ADR 0011
  (consolidation, de-identification, contradiction, prediction-error ranking,
  operator/auto gate) and ADR 0007 (per-tenant isolation). See also
  `docs/deployment-modes.md` — "One topology at two scales" and "Hosted
  knowledge kinds".

## Context

ADR 0011 made the promotion gate **pure cross-tenant corroboration**: a schema
promotes to the shared global brain (neocortex) only when ≥ N distinct tenants
independently produced an equivalent lesson. Corroboration was simultaneously
the quality signal *and* the privacy gate — "seen in N tenants ⇒ high-confidence
*and* provably not any one tenant's secret."

That gate is correct for **emergent statistical patterns** ("Golden Retrievers
are predisposed to diabetes"), but it is too narrow for the hosted product
(pet-medical): it **blocks novel, non-private class knowledge that is valuable
from a single source**. A vet who encounters a newly-described spider and records
its envenomation profile and treatment produces knowledge that is:

- **not private** — it is about a *species*, not about any patient; and
- **valuable to every tenant** — but it exists in exactly one tenant, so the
  ≥ N corroboration gate silently drops it forever.

The mistake was conflating two different questions:

1. **Is this safe to share?** (a privacy question about the *subject*), and
2. **Is this good enough to share?** (a quality question).

ADR 0011 answered both with one mechanism (corroboration count). This ADR
separates them: **classify the subject first** (privacy), then apply a
**quality** gate that has two paths.

## Decision

Introduce a **subject-class eligibility gate** applied *before* corroboration,
and two promotion paths for eligible knowledge.

### 1. Subject classification (the new eligibility gate, applied FIRST)

Add a claim-level classification `domain.SubjectClass`:

| Value | Meaning | Promotable? |
| --- | --- | --- |
| `individual` | about a specific entity (this pet, this owner) | **never** — private |
| `class` | about a category (breed, species, disease, a spider) | eligible |
| `unknown` (empty) | unclassified | **never** — fail closed |

- **Fail closed.** `unknown` and `individual` are **not** eligible. For a medical
  product the safe default is that anything not *positively* identified as
  class-level stays inside its tenant. Empty (the zero value) is `unknown`, so a
  belief/schema that nobody classified is private by default.
- **Inference (entity-based heuristic).** A belief's class is inferred from the
  types of its **subject entities** (`domain.SubjectClassFromEntityTypes`): a
  `concept` entity is class-level; `person`/`org`/`project`/`product`/`place`
  each name a specific instance and are individual-level. A belief whose subjects
  are *all* class-level is `class`; **any** individual subject taints it to
  `individual`; **no** subject entities ⇒ `unknown`. An **extraction-time hint**
  or an **explicit operator override** can set it directly (explicit wins).
- **Flow up to the schema.** `domain.AggregateSubjectClass` combines the classes
  of the beliefs backing a synthesized schema: a schema is `class` only when
  **every** backing belief is class-level; any individual belief ⇒ `individual`;
  any unknown (and no individual) ⇒ `unknown`. "A lesson over class-level claims
  is class-level."
- **Persistence.** The class is a column on the `lessons` (schema) table so it
  survives synthesis → the offline promotion pass on the default (sqlite/libsql)
  backend. Postgres/MySQL persistence and richer inference are follow-ups
  (see below).

The eligibility gate runs **per lesson, before clustering/corroboration**, in the
promotion engine (`consolidate.Promote`, gate 0). An ineligible lesson is
recorded in `Skipped` with reason `ineligible subject class (not class-level)`
and can never appear in `Promoted`/`Pending`/`Dissonant`, regardless of how many
tenants corroborate it. This is the medical-product privacy invariant, and it is
strictly stronger than ADR 0011's corroboration floor (which an individual fact
seen in N tenants would otherwise clear).

### 2. Two promotion paths for eligible (class) knowledge

Both paths run **only** on lessons that already cleared the eligibility gate, and
both still run every downstream ADR-0011 gate (de-identification, contradiction,
ranking, operator/auto).

- **Emergent** *(default; unchanged from ADR 0011)* — a class-level pattern
  corroborated across ≥ `MinTenants` distinct tenants (e.g. "Golden Retrievers
  are predisposed to diabetes"). Here cross-tenant corroboration is a **quality**
  signal: many tenants independently learned the same thing. Token-level
  cross-tenant corroboration (every promoted word seen in ≥ 2 tenants) remains
  the structural de-identification guarantee on this path.

- **Curated** *(new)* — a class-level fact contributed from a **single source**,
  **bypassing** the ≥ N corroboration gate (and the token-level cross-tenant
  corroboration that single-source data cannot satisfy). It is gated instead by a
  **curator authorization scope** (§3). The privacy floor on this path is
  eligibility (the subject is provably class-level) plus the denylist
  de-identification and the contradiction check — not corroboration. This is how
  the new spider's envenomation profile reaches the global brain from one vet.

The engine exposes this as `Options.Curated`. The pure engine enforces the
class-level + de-identification + contradiction floors; it **trusts the caller**
to have verified the curator authorization before setting the flag.

### 3. Curator scope

Add an auth scope `promote:global` (`domain.ScopePromoteGlobal`) and
`auth.Claims.CanCurate()`. Only a token bearing it (or the wildcard `*`
admin/operator token) may take the **curated** single-source path. Without it,
only the **emergent** (corroborated) path is available — which needs no special
scope because corroboration is its own privacy proof.

Modelling "contribute to global" as a granted capability means it is a **vet /
curator role**, not something every tenant user holds. The CLI wires it as
`consolidate --promote --curate` (alias `--contribute`), which requires a curator
token via `--token <jwt>` or `MNEMOS_TOKEN`; the command validates the token's
signature, expiry, and revocation, and requires `CanCurate()` **before reading
any tenant data** (fail closed). An unauthorized `--curate` run does nothing.

Consistent with `docs/deployment-modes.md`, the promotion pass is **Mode F**
(offline consolidation, gated by DB credentials, never a network surface). The
curator scope is a **second** authorization *inside* that offline job: holding
the DSN lets you run promotion; holding a `promote:global` token additionally
lets you take the single-source curated path.

### 4. The existing gates are unchanged — they run AFTER eligibility

Eligibility is a new **gate 0**. Everything ADR 0011 defined remains, in order,
as the stages after it:

0. **Subject-class eligibility** *(new)* — class-level only.
1. **Corroboration** — emergent path only (curated bypasses it).
2. **De-identification** — token-level cross-tenant corroboration (emergent) +
   denylist scrub (both paths), fail closed.
3. **Contradiction** — check against vetted global claims, fail closed → route to
   `Dissonant`.
4. **Prediction-error ranking** — order survivors by peak surprise.
5. **Gate policy** — `operator` (Pending, default) vs `auto` (Active).

### 5. Born-global (documented; top-down feed)

Alongside bottom-up float-back (emergent + curated), the neocortex also supports
**born-global** knowledge: an operator authors **reference taxonomy** straight
into the global tier, never passing through a tenant — e.g. a seed of species /
breed / disease reference data. Born-global data is class-level by definition and
does not go through the promotion gate at all; it is the top-down complement to
the bottom-up paths. This ADR reserves the concept; the concrete authoring
surface (a `mnemos neocortex seed` verb or an operator import) is a follow-up.
Its safety story is simpler than promotion's: it never contains tenant data, so
there is nothing to de-identify — only an operator-authored write into the shared
tier, gated by the same `promote:global` curator capability.

## Consequences

**Positive**

- Novel, non-private class knowledge from a single authoritative source can now
  reach the global brain — the pet-medical use case ADR 0011 could not serve.
- Privacy is **stronger and clearer**: an explicit subject-class gate, fail-closed
  on `unknown`/`individual`, applied before any counting. An individual fact seen
  in N tenants — which ADR 0011's corroboration gate would have promoted — is now
  excluded up front.
- Privacy and quality are **separated**: corroboration is now (correctly) a
  quality signal on the emergent path, not overloaded as the sole privacy gate.
- The curator role makes "who may contribute to the shared brain" an explicit,
  auditable capability rather than an implicit property of any write.

**Negative / open**

- Curated promotion trades the structural cross-tenant de-identification guarantee
  for **eligibility + denylist + curator sign-off**. That is a deliberate,
  human-in-the-loop trust decision appropriate to a class-level fact; it must not
  be enabled for individual data (the eligibility gate enforces this) and the
  curator scope must be issued sparingly.
- Classification quality bounds the gate. The initial inference is a simple
  entity-based heuristic; misclassifying an individual subject as `class` would be
  a leak. Mitigations: fail-closed defaults, and the denylist/contradiction gates
  still run. **LLM-assisted classification** (higher recall on class-vs-individual)
  is a noted follow-up.
- Persistence is currently wired for sqlite/libsql (the default backend and the
  offline promotion path); **Postgres/MySQL `subject_class` columns and the
  synthesis-time inference wiring are follow-ups**, as is the born-global
  authoring surface.

## Rollout

- **Domain** — `SubjectClass` type + `SubjectClassFromEntityTypes` /
  `AggregateSubjectClass` / `EligibleForPromotion` helpers; `SubjectClass` field
  on `Belief` and `Schema`; `subject_class` persisted on the sqlite/libsql
  `lessons` table (schema + migration + sqlc). **DONE.**
- **Engine** — `consolidate.Promote` gate 0 (eligibility, fail closed) +
  `Options.Curated` single-source path; `PromotedLesson.Curated` for audit.
  **DONE.**
- **Auth** — `domain.ScopePromoteGlobal` (`promote:global`) +
  `auth.Claims.CanCurate()`. **DONE.**
- **CLI** — `consolidate --promote --curate|--contribute --token <jwt>` verifies
  the curator scope (signature + expiry + revocation + `CanCurate`) before
  reading any tenant data. **DONE.**
- **Follow-ups** — synthesis-time classification wiring; Postgres/MySQL
  `subject_class`; LLM-assisted subject classification; the born-global operator
  authoring surface.
