# Vocabulary: where each term is used

Mnemos uses two names for each core concept. One is a **machine term** (Claim,
Event, Relationship) and the other is a **brain term** (Belief, Episode,
Association). [ADR 0011](adr/0011-brain-consolidation-cls.md) introduced the
brain terms. This page says which surface uses which term, and freezes both
for the duration of the consolidation program (#382).

## The mapping

| Machine term | Brain term | What it is |
|---|---|---|
| Claim | **Belief** | An assertion derived from evidence, with confidence, status and trust |
| Event | **Episode** | An immutable, append-only record of something that happened |
| Relationship | **Association** | A belief-to-belief edge (`supports`, `contradicts`, causal family) |
| Contradiction | **Dissonance** | A `contradicts` edge, or a belief left `contested` |
| Lesson | **Schema** | A generalisation synthesised from action→outcome chains |
| Playbook | **Reflex** | A trigger → steps response derived from schemas |
| Scope | **Context** | The `{Service, Env, Team}` filter |
| `verify` | `reconsolidate` | Re-stabilise a recalled belief |

Some terms have no second name: **Evidence**, **Embedding**, **Action**,
**Outcome**, **Decision**, and `consolidate`. ADR 0011 explains why.

## Which surface uses which term

| Surface | Vocabulary | Examples |
|---|---|---|
| REST paths and JSON bodies | Brain | `/v1/beliefs`, `/v1/episodes`, `/v1/associations`, `/v1/schemas`, `from_belief_id` |
| gRPC (`proto/mnemos/v1`) | Brain | `Belief`, `Episode`, `AppendBeliefs` |
| MCP tool names and I/O | Brain | `list_beliefs`, `list_dissonances`, `synthesize_schemas`, `query_reflex` |
| CLI JSON output | Brain | `audit.v2`, `query --json` |
| Metrics fields | Brain | `dissonances`, `contested_beliefs` |
| Root Go library (`go.klarlabs.de/mnemos`) | Machine | `RememberClaim`, `ClaimItem`, `SetClaimLifecycle`, `AnalogousClaims` |
| Go HTTP client (`go.klarlabs.de/mnemos/client`) | Machine types, brain wire | `client.Claim`, `c.Claims()`, sent to `/v1/beliefs` |
| `internal/domain` | Both (see below) | `type Claim = Belief`, `type Lesson = Schema` |
| Storage schema (tables, columns) | Machine | `claims`, `claim_evidence`, `relationships`, `trust_score` |
| Config keys and env vars | Machine | `MNEMOS_DB_URL`, `db.shared_pool` |
| CLI verbs | Mostly machine | `extract`, `relate`, `lessons`, `playbook`, `verify` (plus `reconsolidate`) |

### `internal/domain` declares brain names; call sites use machine names

ADR 0011 Phase A made the brain terms the declared Go types and kept the
machine terms as aliases:

```text
type Claim = Belief             type Relationship = Association
type Event = Episode            type Lesson = Schema
type ClaimEvidence = BeliefEvidence   type Playbook = Reflex
type Scope = Context
```

The call sites were never migrated. In October 2026 there were 863
references to `domain.Claim` and 3 to `domain.Belief`. The aliases are
load-bearing: they are the names the code actually uses.

## Rules during consolidation

1. **No broad rename in either direction.** Do not migrate call sites from
   `domain.Claim` to `domain.Belief`, or the reverse. Do not remove an alias.
   A rename touches every package at once, which makes the trust, capability
   and scale changes in this program impossible to review and attribute.
2. **Each surface keeps its current vocabulary.** New REST/gRPC/MCP fields use
   the brain term. New storage columns, config keys and root-library
   identifiers use the machine term, so they match their neighbours.
3. **The public Go API keeps machine terms until the next major version.**
   Renaming `RememberClaim` or `ClaimItem` breaks every consumer. That kind of
   break belongs at a major-version boundary with deprecated aliases, not in a
   consolidation release.
4. **Documentation uses the term of the surface it describes.** A REST guide
   says "belief". A Go library guide says "claim". When a page spans both, it
   introduces the pair once, e.g. "a belief (`Claim` in the Go API)".
5. **A correctness fix may break these rules.** If a name actively misleads,
   for example by naming the wrong concept, fix it in its own PR and record
   the reason there.
