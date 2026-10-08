# ADR 0029: Derived state records what produced it

- **Status:** Accepted (2026-10-07), consolidation program #382, Phase 6
  ("persisted derived-state model versions where re-interpretation is needed").
- **Date:** 2026-10-07
- **Deciders:** Felix Geelhaar

## Context

Much of a brain is derived: computed from claims and evidence by rules that
change. Trust scores, half-lives, relate tokens, embeddings, relationships and
health snapshots are all derived. When a rule changes, the state an older rule
wrote must be found and re-derived, or it silently keeps the old meaning. That
is only possible if each piece records what produced it.

Inventory at the start of Phase 6:

| Derived state | Records its producer |
|---|---|
| Trust scores | `trust_model_version`, `trust_computed_at` (ADR 0026) |
| Half-lives | `half_life_classifier` |
| Relate tokens (`claim_tokens`) | tokenizer version, rebuilt on change (#420) |
| Embeddings | `model` |
| Health snapshots | `mode`: exact or sampled (#422) |
| **Relationships** | **nothing** |

Relationships are where re-interpretation is needed most. Their rules changed
twice in this program (ADR 0027, ADR 0028), and the contradiction detectors
have changed before. Nothing distinguished an edge written under the unbounded
rules from a current one, so applying a rule change meant re-deriving the whole
graph. `relate --prune-supports` re-ranks every claim; `relate --prune-stale`
re-derives every contradiction.

## Decision

1. Relationships gain `derived_by` on every backend. The rule-based detectors
   stamp `relate.ModelVersion` (now `relate/v3`) on every edge they infer:
   pairwise, incremental, citation and test-conflict edges. The LLM causal
   detector stamps `relate.CausalLLMVersion`. Edges a caller supplies keep
   whatever it set, usually empty. Re-deriving an edge overwrites the stamp.
2. The version history lives beside the constant: v1 unbounded (rows from that
   era predate the column and read empty), v2 supports budget, v3 candidate
   budget. **Bump it whenever what the rules produce changes.**
3. Going forward, any new derived state records its producer when it is
   introduced. Adding a version later leaves every earlier row unidentifiable,
   which is the position relationships were in until now.

## Consequences

- A maintenance pass can now target exactly the edges an older rule set wrote,
  instead of re-deriving the whole graph. Existing commands do not use it yet;
  rows written before this change read empty and still need the full pass once.
- Storage grows by one short string per edge.
- Not covered: claims themselves. Extraction is versioned only indirectly,
  through the LLM response cache key. Lessons and playbooks carry no producer
  version either. Neither has needed re-interpretation so far; the third point
  of the decision applies when one does.
