# ADR 0043: Relational vs graph representation

- **Status:** Accepted (experiment)
- **Date:** 2026-11-19
- **Deciders:** Felix Geelhaar
- **Evidence:** `tools/graphbench`, `scripts/graphbench.sh`, results and method in
  [docs/eval/graphbench](../eval/graphbench/README.md)

## Context

Mnemos stores a graph in relational tables:

- beliefs, joined by associations (`relationships`);
- beliefs to episodes, through evidence (`claim_evidence`);
- episodes to episodes (`event_dependencies`, ADR 0038);
- merge redirects (`merged_into`, ADR 0033).

Every backend is relational: SQLite (the local default), Postgres, MySQL and
libSQL. The strategy agenda carried the question whether a native graph store
would serve the graph-shaped operations materially better. The rule was to
answer it only once benchmarks existed, so the answer would be a measurement
and not an opinion. `tools/scalebench` (with p99 and growth) and
`tools/brainbench` now exist, so this ADR records the experiment and its
decision.

The operations that walk the graph are:

1. multi-hop recall expansion (`query.expandClaimsByHops`, up to
   `query.MaxHops` = 5);
2. retraction closure (`govwrite.retractEvent`: dependents over
   `event_dependencies` plus evidence, then each belief's live independent
   evidence, ADR 0037/0038);
3. the dissonance listing (`list_dissonances`) and the contradiction
   neighbourhood of a belief set (`collectContradictions`);
4. merge-redirect chains (`pipeline.StoredRedirects`, at most 8 hops,
   ADR 0033/0041);
5. provenance paths (belief → evidence episodes → the episodes they depend
   on);
6. the write path that creates all of the above (`PersistArtifacts`).

## Method

The full method is in the [eval README](../eval/graphbench/README.md). The
harness:

- generates one deterministic corpus at 10k and 100k beliefs, using scalebench
  plus an overlay with dependencies, redirect chains, and a `dense` profile
  calibrated to a real brain's aggregate shape;
- loads identical data into SQLite, Postgres 16 and Neo4j 5.26.16 Community
  (with APOC core);
- times every workload on the current production path, on a single-query
  recursive CTE, on idiomatic Cypher, and on an in-process adjacency map (the
  upper bound);
- checks every answer, per input, against an oracle;
- waits for a quiet machine before each cell and records the load average.

## Results

All four reports (dense and uniform, at 10k and 100k) pass parity: 7,749
per-input checks, 0 mismatches. The cells started at a 1-minute load average
of 1.9 to 7.7. The table shows the dense profile at 100k beliefs (~446k
associations, ~350k evidence links), p50 / p95 in ms. Full tables are in the
eval README.

| Workload | Best relational path | Neo4j (best Cypher) | Relational production path |
|---|---|---|---|
| Expand 2 hops | 4.0 / 10.6 (SQLite prod) | 6.9 / 15.9 | SQLite 4.0 / 10.6 · Postgres 4.2 / 15.5 |
| Expand 5 hops | 270 / 549 (Postgres CTE) | 809 / 1,226 (APOC BFS) | SQLite 1,120 / 2,033 · Postgres 591 / 966 |
| Retraction closure | 0.67 / 4.8 (Postgres CTE) | 11.1 / 34.1 | SQLite 3.9 / 110 · Postgres 469 / 1,263 |
| Dissonance page | 1.3 / 1.8 (SQLite + index) | 6.3 / 8.7 | SQLite 663 / 806 · Postgres 453 / 522 |
| Contradiction neighbourhood | 0.80 / 1.5 (Postgres) | 1.4 / 2.3 | (already one query) |
| Merge redirects | 0.30 / 0.33 (Postgres CTE) | 1.5 / 3.2 | SQLite 0.80 / 1.2 · Postgres 3.9 / 5.8 |
| Provenance path | 0.20 / 0.23 (SQLite CTE) | 2.5 / 4.4 | SQLite 0.32 / 0.45 · Postgres 1.5 / 1.8 |
| Write batch (50 beliefs) | 35.6 / 40.3 (SQLite ports) | 16.0 / 25.1 | SQLite 69 / 81 · Postgres 140 / 150 |

The in-process adjacency map, the bound no engine can beat, answers 5-hop
expansion in 33 ms and every other read in under 0.3 ms.

Other measured costs at dense 100k:

| | Load time | Disk | Memory |
|---|---|---|---|
| SQLite | 52 s | 334 MB | 0.8 GiB, the whole process including the corpus and the map |
| Postgres | 269 s | 338 MB | 0.18 GiB |
| Neo4j | 12.8 s | 116 MB store + 514 MiB preallocated transaction log | 1.7 GiB, set by its 1 GiB heap and 1 GiB page cache |

Neo4j's HTTP round-trip floor was 1.2–2.1 ms, so transport does not explain
any gap larger than ~2 ms.

## Decision

**A graph backend is not justified.** We do not add Neo4j, or any other graph
store, as a Mnemos backend. The relational backends stay the only
representation.

The numbers decide it as follows:

- **No read workload is materially better on the graph store.** For every
  graph-shaped read, the best relational path (the production code, or one
  recursive CTE or index replacing it) matches Neo4j or beats it, by 1.7–3× on
  expansion and up to 17× on the lookups. That holds at both sizes and on both
  corpus profiles.
- **Where production is slow, the fix is a query, not an engine.** Two
  production paths have large gaps:
  - `list_dissonances` scans every association to page 50 contradictions.
    Neo4j is 70–105× faster than it, but an index on
    `relationships(type, created_at)` is 200–500× faster and beats Neo4j.
  - Retraction's read side on Postgres makes a round trip per episode. Neo4j is
    42× faster than it, but one recursive CTE is 700× faster.
- **Deep expansion is slow on every engine.** Five hops reach ~70% of a
  real-density brain. If that ever has to be interactive, an in-process
  adjacency index (8× faster than the best engine) or a bounded frontier comes
  before any new server.
- **Writes are the one material graph win.** Neo4j is 2.2–3.7× faster than
  SQLite's port writes and ~7.5× faster than Postgres's. That comes from
  batching and from doing less work: no status history, no FTS, no text keys,
  no trust rescore. Persistence is ~2–6% of a `Remember` at this size, so the
  win is not worth a second store. Batching the relational writes is the cheap
  route to it.
- **The qualitative costs are disqualifying on their own.** There is no tenant
  isolation that satisfies ADR 0007 in Community. Writes would split across
  two stores with no atomic commit. It adds a JVM server that cannot be
  embedded in the local-first binary.

**When to revisit.** Run `scripts/graphbench.sh` again, ideally at a larger
size or on a real brain's export. Reopen the question only if both of these
hold:

1. On the dense profile at ≥100k, some interactive graph read has a Neo4j p95
   at least **3× lower** than the best relational path (production or a single
   SQL statement or index), and an in-process index cannot close the gap.
2. The graph store's tenant isolation and transactional story can meet ADR 0007
   and the governed writer. In practice that means Enterprise or a different
   engine, which would need its own ADR.

New requirements could also reopen it: whole-graph algorithms on the hot path,
such as PageRank, community detection or weighted shortest paths over
millions of edges, which neither a CTE nor the current code provides.

**Follow-ups found by the experiment.** Each is tracked separately, and none
is changed here:

- `list_dissonances` should page contradictions with an indexed query instead
  of `Relationships.ListAll`.
- The retraction read side should batch its journal and dependency reads, or
  use a recursive CTE where the backend supports it.
- Correctness: `govwrite`'s `dependsOnRetracted` memoises a result that a
  cycle cut computed. With mutually dependent episodes, a belief whose only
  evidence depends on the retracted episode through the cycle can be
  **kept** instead of retracted, depending on evaluation order. This was
  reproduced with A→{B, R}, B→A and R retracted: a belief resting only on B
  was kept. The benchmark corpus is acyclic, so it did not show up in parity.

## Consequences

- `tools/graphbench` stays as the instrument for this question. It needs no
  Docker in `make test`: the parity check runs on SQLite only, and Neo4j and
  Postgres are behind `-neo4j` / `-pg` and `scripts/graphbench.sh`.
- go.mod gains no dependency. Neo4j is reached over HTTP.
- The production code does not change in this decision.

## Costs a graph backend would add

These costs apply whatever the latencies are.

- **Multi-tenant isolation (ADR 0007).** Mnemos isolates tenants with Postgres
  row-level security, or with a namespace (schema, database or file) per tenant
  on SQLite, MySQL and local libSQL. `--require-tenant` refuses to start on a
  backend that cannot isolate. Neo4j Community has neither mechanism:
  - It serves one user database. Multiple databases are an Enterprise feature.
  - It has no role-based or property-level access control.
  So tenant isolation would have to be a `tenant` property that every Cypher
  statement filters on. That fails open: one missed predicate leaks across
  tenants, which is exactly what ADR 0007's RLS and namespaces were adopted to
  prevent. The alternative is one Neo4j server per tenant.
- **Transactional consistency.** Mnemos writes beliefs, evidence,
  associations, trust, status history and the journal in one store, and the
  governed writer (`internal/govwrite`) relies on that. Keeping the graph in
  Neo4j and the rest in SQL would split every write across two stores, with no
  atomic commit. Retraction, merge redirects (ADR 0041) and recorded-as-of reads
  (ADR 0036) would all need an outbox or reconciliation, and a crash between the
  two commits leaves an edge without its belief or a belief without its edges.
  Moving everything into Neo4j instead means a new implementation of the ~20
  repository ports (`store.Conn`) and their guard tests, for one backend.
- **Operations.** It would be another stateful server: a JVM using about
  1.5 GiB resident in these runs, against about 0.1 GiB for Postgres and 0.13
  GiB for the whole SQLite process. Community has no online backup:
  `neo4j-admin database dump` needs the database stopped. It has no clustering.
  It cannot be embedded in a Go binary, so the local-first default (one
  pure-Go, `CGO_ENABLED=0` binary and one SQLite file) could not use it.
- **License.** Neo4j Community is GPLv3. Using it as a separate server reached
  over the network does not affect Mnemos's MIT license. Bundling or
  redistributing it with Mnemos would need review. The features ADR 0007 would
  need (multiple databases, fine-grained access control) are commercial
  Enterprise features. APOC core is Apache 2.0.
