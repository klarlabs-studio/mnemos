# ADR 0041: Id-addressed writes follow redirects

- **Status:** Accepted; implemented.
- **Date:** 2026-11-19
- **Deciders:** Felix Geelhaar
- **Builds on:** ADR 0033 (merged beliefs become redirects), the write path's
  duplicate check (`internal/pipeline/known_claims.go`, #533)
- **Adds:** the governed `write_beliefs` action (`govwrite.Writer.Beliefs`),
  `internal/pipeline/addressed_claims.go`

## Context

ADR 0033 keeps a merged belief as a redirect: a deprecated row whose
`claims.merged_into` names the winner. The claim upsert keeps a stored
`merged_into` when a write carries none, but it sets `status` from the write.

Some writes address claims by the sender's ids rather than by what they say:

- REST `POST /v1/beliefs`, which registry push uses;
- gRPC `AppendBeliefs`;
- registry pull (`persistPulledClaims`).

They send a belief with its original id and status `active`. Associations
(`/v1/associations`, `AppendAssociations`) and embeddings (`/v1/embeddings`,
`AppendEmbeddings`) follow in later requests, still carrying the sender's ids.

That breaks in two ways.

1. **A redirect comes back to life.** Suppose a registry consolidation merges
   belief A into B. The next push from the brain that minted A sends A again,
   active. The upsert stores A as **active with `merged_into` still set**, which
   is an incoherent state. The evidence, associations and embedding pushed for A
   attach to A instead of B. So every push after a consolidation resurrects the
   duplicates it merged. `TestBeliefs_RedirectIsNotRevivedByAnIDAddressedWrite`
   reproduces this: before the fix, A read back as `status=active
   merged_into="c-w"`.
2. **The same belief from two brains is two beliefs.** Brains mint random claim
   ids (`newClaimID` in `internal/extract`). Two brains that learned the same
   fact push it under two ids, and the registry stores two live beliefs. Every
   single-fact write path already folds a restatement onto the belief it
   restates (`pipeline.CollapseKnownClaims`). The bulk path cannot reuse it,
   because the associations, embeddings and evidence that arrive with or after
   the beliefs still name the sender's ids.

## Decision

Every id-addressed belief write goes through one governed action,
`write_beliefs` (`govwrite.Writer.Beliefs`). It resolves the batch against the
store first (`pipeline.ResolveAddressedBeliefs`) and then writes. REST, gRPC
and registry pull all call it, so they behave the same.

### A. A write to a redirect follows the redirect

- **Claims.** A claim whose id is stored as a redirect (`merged_into` is set) is
  **not upserted**. The redirect row is never rewritten by an id-addressed
  write, so it cannot come back active.
- **Evidence.** A link addressed to a redirect moves to the live claim at the
  end of its `merged_into` chain. The chain is followed for at most 8 hops by
  the same resolver ADR 0033 uses (`resolveRedirect`). A moved link is kept only
  when it adds a new origin to its new claim (`dropRepeatedOrigins`, #443), so a
  re-push does not raise trust.
- **Associations.** The `write_relationships` executor re-points an end that is
  a redirect to the live claim. An edge whose two ends then name the same claim
  is dropped, as are exact repeats (`pipeline.FollowRedirectsInRelationships`).
- **Embeddings.** The `write_embedding` executor drops a claim embedding
  addressed to a redirect. The live claim has its own embedding, and the merge
  deleted the redirect's.

These two executors resolve redirects for every caller, not only the bulk
surfaces. A redirect has no use for an edge or a vector, so no caller should
want either one written to it.

**A chain that ends at nothing live.** The winner may have been forgotten,
deprecated or deleted after the merge. In that case the redirect is still not
rewritten, and links addressed to it stay on it. ADR 0033 lets a *restatement*
come back as a new claim in this case. An id-addressed write is not a
restatement: it names a specific retired row. Reviving that row would undo the
retirement of its winner, and it would leave a live claim that still points at
a retired one. The evidence is kept on the redirect, where an audit or a later
unmerge can still see it.

### B. An unknown id that restates a held belief becomes a redirect

An incoming claim whose id is not stored is folded when a live claim already
holds its belief. "Holds" means the same `domain.ClaimTextKey`, scope and
audience, found with the same lookup as `CollapseKnownClaims`, including its
redirect-following. The incoming claim is then **stored as a redirect**:
deprecated, with `merged_into` naming the held claim. Its status history reads
`<request reason>: restatement folded onto <id>`, and its evidence moves to the
held claim under the same origin rule as in A.

Storing the id, rather than dropping it, is what lets the associations and
embeddings that arrive later resolve through rule A. It also means the id is
never reused (ADR 0033 §4).

Within one batch, the first claim with a given identity holds it, and later
claims with the same identity redirect to it. Folding never happens when:

- **The id is already stored and is not a redirect.** An explicit write to a
  known id is an update of that claim, even if its new text restates another
  belief.
- **The scope or audience differs.** Identity includes both.
- **The incoming claim is retired** (deprecated, or with `valid_to` set). It is
  not a restatement of a live belief, and folding it would hand its evidence to
  a belief its sender had retired.
- **The held claim is retired by the same batch.**

### C. Responses say where each id went

- **REST.** `POST /v1/beliefs` returns `folded` (the new ids stored as
  redirects) and `remapped` (incoming id → held id, for folded ids and for ids
  that followed a stored redirect). Both are additive and omitted when empty.
- **gRPC.** `AppendResponse` gains fields 3 (`folded`) and 4 (`remapped`).
- **Go client.** `client.AppendResponse` gains both fields.

Registry push and pull print how many ids were folded. Pull now writes in
batches of 100 claims, so each resolution reads a bounded set of ids.

A `valid_to` sent for an id that resolved elsewhere is not applied: nothing was
written under that id, so there is nothing to close.

No transport binding changed, so the capability registry is unchanged.

## What this does not do

- **No similarity folding.** Only exact identity folds: normalised text, scope
  and audience. Paraphrases from two brains stay two beliefs until a
  consolidation's semantic dedupe merges them. Once merged, the redirect keeps
  them merged across every later push, which is the point of rule A.
- **Registry consolidation is unchanged.** It still makes the semantic merges.
- **Unmerge, and chain compression,** remain as ADR 0033 left them.
- **The sender is not rewritten.** A pushing brain keeps its own ids. The
  `remapped` map tells it where they went, but nothing in the CLI acts on it
  yet.
- **Trust is not rescored here.** As before, an id-addressed write upserts
  evidence links without the incremental rescore that `PersistArtifacts` does.
  Folded evidence therefore counts the next time trust is recomputed.
- **The internal claim upsert (`Writer.Claims`) is unchanged.** Forget,
  deprecate, merge and the lifecycle writes still address a row deliberately,
  redirects included.

## Consequences

- A push after a registry consolidation no longer resurrects what it merged.
  Repeated pushes and pulls between brains converge on one live belief per
  identity.
- A registry accumulates one deprecated redirect row per id it folded. Like
  merge redirects, these rows are excluded from recall, relate and the live
  counts.
- An id-addressed belief write costs one id lookup and one text-key lookup more
  per batch. Each association batch and each claim embedding costs one id
  lookup more.
