# Relational vs graph representation (ADR 0043)

This experiment asks whether a native graph store serves Mnemos's graph-shaped
workloads much better than the relational backends Mnemos uses today. If it
does, the gain might justify a graph backend. The decision is in
[ADR 0043](../../adr/0043-relational-vs-graph-representation.md). This page
covers the method, the numbers and their limits. The raw reports
(`<profile>-<beliefs>.json`) are next to this file.

## Reproduce

```sh
scripts/graphbench.sh docs/eval/graphbench          # needs Docker; ~1-2 h on a quiet machine
go run ./tools/graphbench -beliefs 10000           # SQLite + in-process map only, no Docker
MNEMOS_DB_URL=memory:// go test -race -count=1 ./tools/graphbench/...
```

The script runs `postgres:16-alpine`, the image `scripts/test-integration.sh`
uses. For each corpus it also starts a fresh `neo4j:5.26.16-community` with APOC
core from the image's `labs/` directory, a 1 GiB heap and a 1 GiB page cache.
It labels every container with its owner PID, reaps containers left by dead
runs, and removes everything with `docker rm -f -v` on exit. Before it loads a
store and before each timed cell, the harness waits until the 1-minute load
average is at most 8. It records the load before and after each cell.

## Method

**Data.** `internal/scalebench` (uniform shape, seed 1) generates the beliefs,
episodes, evidence and associations. `tools/graphbench/corpus.go` adds an
overlay from its own random stream, so the scalebench part stays byte-identical:

- Episode dependencies (ADR 0038). About 8% of episodes depend on 1–2 earlier
  ones. 30% of those dependencies point at one of 20 widely copied sources and
  the rest at a recent episode, so chains form. The dependency graph is a DAG.
- Merge redirects (ADR 0033). There is one chain per 100 beliefs, of length 1–7,
  inside the resolver's 8-hop bound.
- In the `dense` profile only, extra `supports` edges and evidence links. These
  bring the means up to the shape of a real brain (below).

The same in-memory corpus is loaded into every store. SQLite and Postgres load
through the storage ports, in scalebench's order. Neo4j loads with batched
`UNWIND … CREATE`.

**Real-brain calibration.** The developer's own brain was read with
`sqlite3 -readonly` for aggregate counts only. No content was read. It holds
69,342 beliefs, 86,172 episodes, 247,498 evidence links, 331,473 `supports` and
751 `contradicts` associations. That is a mean of 9.6 association endpoints and
3.57 evidence links per belief, with a maximum degree of 993 and 21.6% of
beliefs unconnected. Its schema predates `event_dependencies` and
`merged_into`, so it has neither, and the overlay's dependency and redirect
rates are assumptions. The scalebench generator alone produces a mean degree of
~1.6 and ~1.45 evidence links per belief, with 15% of associations
`contradicts`. The `dense` profile reaches ~8.9 and ~3.5. Its degree tail is
lighter than the real brain's (max 90–110 vs 993), and it has far fewer
unconnected beliefs.

**Workloads**, each on 50 deterministic inputs (10 dissonance pages):

| # | Workload | Production path replicated | Input |
|---|---|---|---|
| 1 | `recall_expand_hops2/5` | `query.Engine.expandClaimsByHops`: per hop, `Relationships.ListByClaimIDs` + `Claims.ListByIDs` | 10 seed beliefs from one topic; 2 and 5 (`query.MaxHops`) hops |
| 2 | `retraction_closure` | read side of `govwrite.retractEvent`: direct beliefs, BFS dependency closure (`ListDependents`), reached beliefs, per-episode `LookupRetraction` + memoised dependency walk; stops before writing | half a widely-copied source, half a random episode |
| 3a | `dissonance_page` | `list_dissonances` (`listContradictionPairs`): `Relationships.ListAll`, filter, newest first, page, resolve texts | limit 50, offsets 0–450 |
| 3b | `contradiction_neighbourhood` | `query.collectContradictions`: one `ListByClaimIDs` | 50 beliefs |
| 4 | `merge_redirects` | `pipeline.StoredRedirects` (called directly) | 10 redirects + 10 random ids |
| 5 | `provenance_path` | belief → evidence episodes → transitive dependencies, one level per round trip with `ListEvidenceByClaimIDs` / `Dependencies.ListByEventIDs` (no single production function returns this path) | 20 beliefs |
| 6 | `write_batch` | `pipeline.PersistArtifacts` (+ dependencies), and the bare port writes | 50 beliefs, 10 episodes, their evidence and associations |

Each workload runs several **paths**:

- `production` is the code path above.
- `single-query` is one recursive-CTE SQL statement per workload, written for
  this experiment. Only the dissonance page is a plain query.
- `indexed` (dissonance only) adds the index `relationships(type, created_at)`,
  which no production schema has, on the benchmark's own store. It then runs an
  index-only count and a page read in index order. This is the relational
  counterpart of the relationship-property index the Cypher path uses.
- `ports-only` (writes only) is the bare storage-port write of a batch, without
  `PersistArtifacts`' collapse and trust rescore.
- `cypher` and its variants run on Neo4j:
  - `cypher-apoc-bfs` uses APOC `spanningTree` for one multi-source BFS.
  - `cypher-per-hop` mirrors the production loop.
  - `cypher-shortest` uses GQL `ANY SHORTEST` per (seed, belief) pair.
- `adjacency-map` uses Go maps in-process. It is an upper bound on any engine,
  not a candidate backend.

**Timing.** Each cell runs 5 untimed warm-up calls, then up to 100 timed calls,
cycling over the inputs. It stops once it has 10 samples and has used its 30 s
budget. A variant slower than 6 s per call gets 3 samples. Percentiles are
nearest-rank, so with fewer than 100 samples p99 is the max. `round_trip_floor`
measures each store's empty round trip (`SELECT 1` / `RETURN 1`).

**Parity.** For each input, the in-process oracle's answer is compared with
every path's answer as sorted lines: hop distances, closure levels and
per-belief retraction outcomes, page ids and totals, redirect targets, and
provenance depths. SQLite's production path is one of the checked paths, so
when every path matches the oracle, every store agrees with production.
`TestSQLitePathsMatchOracle` runs the same check without Docker, and
`TestMemGraph_HandBuilt` checks the oracle against hand-worked answers.

## Results

Run on 2026-11-19, Apple M5 (10 cores, 24 GiB), darwin/arm64, Go 1.27.4, at
`ebe618e` plus this harness. Docker Desktop had 4 CPUs and 8 GiB. The four
reports were all recorded in one clean pass of `scripts/graphbench.sh`. Each
cell started with a 1-minute load average between 1.9 and 7.7, and the
per-cell values are in the table. Three Neo4j hop-5 cells at 10k ended at a
load of 8.2 to 14.3, probably from Neo4j's own parallel traversal in the Docker
VM. An earlier pass on a busier machine (load up to ~10) put every workload's
stores in the same order. Its numbers were noisier and are not reported.

**Parity: 7,749 per-input checks across the four reports, 0 mismatches.**
Every path on every store returned the oracle's answer for every input it ran.
This includes SQLite's and Postgres's production paths, the recursive CTEs,
and all three Cypher formulations of expansion.

### Profile `dense` (calibrated to a real brain)

- 10000: beliefs 10000, episodes 2000, evidence 35127, rels {'contradicts': 1191, 'supports': 43328}, deps 174, redirects 169, mean degree 8.904, max degree 90, ev/belief 3.513, parity 1938/1938
  - store sqlite: load 3.9s (loadavg 1.92), disk 33.7 MB , mem 145 MB
  - store postgres: load 23.8s (loadavg 2.24), disk 37.7 MB , mem 98 MB
  - store neo4j: load 2.3s (loadavg 2.45), disk 281.1 MB /data/databases/neo4j=11292KiB /data/transactions/neo4j=263176KiB, mem 1597 MB
- 100000: beliefs 100000, episodes 20000, evidence 350358, rels {'contradicts': 11891, 'supports': 433529}, deps 1901, redirects 1674, mean degree 8.908, max degree 110, ev/belief 3.504, parity 1851/1851
  - store sqlite: load 52.4s (loadavg 5.89), disk 333.8 MB , mem 807 MB
  - store postgres: load 268.8s (loadavg 4.98), disk 338.2 MB , mem 176 MB
  - store neo4j: load 12.8s (loadavg 2.81), disk 657.4 MB /data/databases/neo4j=115712KiB /data/transactions/neo4j=526328KiB, mem 1746 MB

| workload | store / path | 10k p50 / p95 / p99 ms | 100k p50 / p95 / p99 ms | parity | load avg |
|---|---|---|---|---|---|
| round_trip_floor | sqlite / SELECT 1 | 0.01 / 0.03 / 0.03 (n=100) | 0.01 / 0.03 / 0.04 (n=100) | — | 2.5→2.5, 3.5→3.5 |
| round_trip_floor | postgres / SELECT 1 | 0.19 / 0.25 / 0.26 (n=100) | 0.18 / 0.27 / 0.28 (n=100) | — | 2.5→2.5, 3.5→3.5 |
| round_trip_floor | neo4j / RETURN 1 (HTTP) | 1.62 / 2.72 / 3.99 (n=100) | 1.16 / 1.70 / 3.16 (n=100) | — | 2.5→2.5, 3.5→3.5 |
| recall_expand_hops2 | sqlite / production | 3.34 / 6.61 / 9.31 (n=100) | 4.00 / 10.58 / 19.39 (n=100) | 50/50, 50/50 | 2.5→2.5, 3.5→3.5 |
| recall_expand_hops2 | sqlite / single-query | 4.64 / 6.88 / 8.72 (n=100) | 25.88 / 32.57 / 40.54 (n=100) | 50/50, 50/50 | 2.5→2.5, 3.5→3.5 |
| recall_expand_hops2 | postgres / production | 2.72 / 5.32 / 6.16 (n=100) | 4.18 / 15.54 / 23.26 (n=100) | 50/50, 50/50 | 2.5→2.5, 3.5→3.9 |
| recall_expand_hops2 | postgres / single-query | 2.02 / 2.90 / 3.13 (n=100) | 12.83 / 17.16 / 18.04 (n=100) | 50/50, 50/50 | 2.5→2.5, 3.9→3.9 |
| recall_expand_hops2 | neo4j / cypher-apoc-bfs | 6.32 / 14.84 / 26.79 (n=100) | 7.48 / 16.66 / 43.23 (n=100) | 50/50, 50/50 | 2.5→2.5, 3.9→3.9 |
| recall_expand_hops2 | neo4j / cypher-per-hop | 6.15 / 12.66 / 20.55 (n=100) | 8.89 / 19.45 / 48.81 (n=100) | 50/50, 50/50 | 2.5→2.5, 3.9→3.9 |
| recall_expand_hops2 | neo4j / cypher-shortest | 5.45 / 11.56 / 16.32 (n=100) | 6.91 / 15.94 / 29.40 (n=100) | 50/50, 50/50 | 2.5→2.5, 3.9→3.8 |
| recall_expand_hops2 | in-process / adjacency-map | 0.19 / 0.39 / 0.67 (n=100) | 0.26 / 0.69 / 1.06 (n=100) | 50/50, 50/50 | 2.5→2.5, 3.8→3.8 |
| recall_expand_hops5 | sqlite / production | 181.30 / 204.03 / 227.98 (n=100) | 1119.90 / 2033.23 / 2457.86 (n=25) | 50/50, 25/25 | 2.5→2.6, 3.8→2.9 |
| recall_expand_hops5 | sqlite / single-query | 171.93 / 214.46 / 247.84 (n=100) | 804.44 / 1758.50 / 2345.40 (n=33) | 50/50, 33/33 | 2.6→2.4, 2.9→2.5 |
| recall_expand_hops5 | postgres / production | 86.83 / 99.04 / 100.41 (n=100) | 590.58 / 966.35 / 1247.51 (n=48) | 50/50, 48/48 | 2.4→3.9, 2.5→6.2 |
| recall_expand_hops5 | postgres / single-query | 55.31 / 80.64 / 125.18 (n=100) | 269.54 / 549.00 / 693.18 (n=100) | 50/50, 50/50 | 3.9→3.6, 6.2→5.3 |
| recall_expand_hops5 | neo4j / cypher-apoc-bfs | 201.29 / 296.51 / 338.37 (n=100) | 809.06 / 1226.00 / 1564.96 (n=36) | 50/50, 36/36 | 3.6→8.8, 5.3→6.8 |
| recall_expand_hops5 | neo4j / cypher-per-hop | 276.44 / 392.07 / 450.72 (n=100) | 1188.21 / 2700.74 / 2719.87 (n=22) | 50/50, 22/22 | 7.2→8.2, 6.8→7.7 |
| recall_expand_hops5 | neo4j / cypher-shortest | 3257.21 / 5573.38 / 5573.38 (n=8) | 3872.03 / 6610.35 / 6610.35 (n=7) | 8/8, 7/7 | 6.5→14.3, 7.7→5.0 |
| recall_expand_hops5 | in-process / adjacency-map | 3.60 / 4.94 / 5.27 (n=100) | 33.12 / 55.03 / 61.62 (n=100) | 50/50, 50/50 | 7.5→7.5, 5.0→5.2 |
| retraction_closure | sqlite / production | 3.10 / 11.49 / 12.44 (n=100) | 3.92 / 110.34 / 117.25 (n=100) | 50/50, 50/50 | 7.5→7.1, 5.2→5.2 |
| retraction_closure | sqlite / single-query | 7.26 / 8.99 / 9.20 (n=100) | 55.90 / 91.67 / 92.71 (n=100) | 50/50, 50/50 | 7.1→7.1, 5.2→4.7 |
| retraction_closure | postgres / production | 37.29 / 136.30 / 156.59 (n=100) | 469.29 / 1262.71 / 1316.31 (n=81) | 50/50, 50/50 | 7.1→6.8, 4.7→4.0 |
| retraction_closure | postgres / single-query | 0.92 / 1.23 / 1.26 (n=100) | 0.67 / 4.80 / 5.27 (n=100) | 50/50, 50/50 | 6.8→6.8, 4.0→4.0 |
| retraction_closure | neo4j / cypher | 4.48 / 8.66 / 11.74 (n=100) | 11.07 / 34.14 / 59.29 (n=100) | 50/50, 50/50 | 6.8→6.8, 4.0→4.3 |
| retraction_closure | in-process / adjacency-map | 0.02 / 0.06 / 0.07 (n=100) | 0.03 / 0.48 / 0.52 (n=100) | 50/50, 50/50 | 6.8→6.8, 4.3→4.3 |
| dissonance_page | sqlite / production | 73.88 / 80.34 / 86.28 (n=100) | 662.63 / 805.60 / 1006.61 (n=44) | 10/10, 10/10 | 6.8→6.4, 4.3→3.7 |
| dissonance_page | sqlite / single-query | 4.16 / 4.50 / 4.67 (n=100) | 45.64 / 48.92 / 51.51 (n=100) | 10/10, 10/10 | 6.4→6.4, 3.7→3.6 |
| dissonance_page | sqlite / indexed | 0.47 / 0.72 / 0.76 (n=100) | 1.34 / 1.80 / 1.87 (n=100) | 10/10, 10/10 | 6.4→6.4, 3.6→3.4 |
| dissonance_page | postgres / production | 56.57 / 65.79 / 78.55 (n=100) | 453.06 / 522.33 / 601.85 (n=66) | 10/10, 10/10 | 6.4→6.7, 3.4→3.4 |
| dissonance_page | postgres / single-query | 3.99 / 4.98 / 5.33 (n=100) | 34.64 / 49.79 / 52.32 (n=100) | 10/10, 10/10 | 6.7→6.7, 3.4→3.2 |
| dissonance_page | postgres / indexed | 1.34 / 1.91 / 2.77 (n=100) | 2.08 / 2.96 / 3.40 (n=100) | 10/10, 10/10 | 6.7→6.7, 3.2→3.2 |
| dissonance_page | neo4j / cypher | 3.01 / 5.98 / 9.25 (n=100) | 6.33 / 8.67 / 9.49 (n=100) | 10/10, 10/10 | 6.7→6.7, 3.2→3.2 |
| dissonance_page | in-process / adjacency-map | 0.00 / 0.01 / 0.01 (n=100) | 0.01 / 0.03 / 0.05 (n=100) | 10/10, 10/10 | 6.7→6.7, 3.2→3.2 |
| contradiction_neighbourhood | sqlite / production | 0.96 / 1.23 / 1.70 (n=100) | 1.05 / 1.54 / 1.67 (n=100) | 50/50, 50/50 | 6.7→6.7, 3.2→3.2 |
| contradiction_neighbourhood | postgres / production | 0.79 / 1.37 / 1.77 (n=100) | 0.80 / 1.49 / 1.81 (n=100) | 50/50, 50/50 | 6.7→6.7, 3.2→3.2 |
| contradiction_neighbourhood | neo4j / cypher | 1.35 / 2.20 / 6.25 (n=100) | 1.38 / 2.33 / 4.20 (n=100) | 50/50, 50/50 | 6.7→6.7, 3.2→3.2 |
| contradiction_neighbourhood | in-process / adjacency-map | 0.00 / 0.01 / 0.01 (n=100) | 0.01 / 0.01 / 0.02 (n=100) | 50/50, 50/50 | 6.7→6.7, 3.2→3.2 |
| merge_redirects | sqlite / production | 0.84 / 1.26 / 1.78 (n=100) | 0.80 / 1.15 / 1.50 (n=100) | 50/50, 50/50 | 6.7→6.7, 3.2→3.2 |
| merge_redirects | sqlite / single-query | 4.24 / 4.80 / 4.85 (n=100) | 33.41 / 36.18 / 40.55 (n=100) | 50/50, 50/50 | 6.7→6.7, 3.2→3.0 |
| merge_redirects | postgres / production | 4.17 / 6.47 / 7.18 (n=100) | 3.89 / 5.82 / 6.60 (n=100) | 50/50, 50/50 | 6.7→6.6, 3.0→3.0 |
| merge_redirects | postgres / single-query | 0.34 / 0.40 / 0.46 (n=100) | 0.30 / 0.33 / 0.37 (n=100) | 50/50, 50/50 | 6.6→6.6, 3.0→3.0 |
| merge_redirects | neo4j / cypher | 1.41 / 2.86 / 3.90 (n=100) | 1.46 / 3.23 / 4.55 (n=100) | 50/50, 50/50 | 6.6→6.6, 3.0→3.0 |
| merge_redirects | in-process / adjacency-map | 0.00 / 0.01 / 0.01 (n=100) | 0.01 / 0.02 / 0.03 (n=100) | 50/50, 50/50 | 6.6→6.6, 3.0→3.0 |
| provenance_path | sqlite / production | 0.25 / 0.31 / 0.65 (n=100) | 0.32 / 0.45 / 0.53 (n=100) | 50/50, 50/50 | 6.6→6.6, 3.0→3.0 |
| provenance_path | sqlite / single-query | 0.20 / 0.22 / 0.24 (n=100) | 0.20 / 0.23 / 0.25 (n=100) | 50/50, 50/50 | 6.6→6.6, 3.0→3.0 |
| provenance_path | postgres / production | 1.05 / 1.49 / 2.09 (n=100) | 1.45 / 1.84 / 2.34 (n=100) | 50/50, 50/50 | 6.6→6.6, 3.0→3.0 |
| provenance_path | postgres / single-query | 0.38 / 0.43 / 0.48 (n=100) | 0.54 / 0.62 / 0.70 (n=100) | 50/50, 50/50 | 6.6→6.6, 3.0→3.0 |
| provenance_path | neo4j / cypher | 3.01 / 4.67 / 7.93 (n=100) | 2.51 / 4.39 / 6.49 (n=100) | 50/50, 50/50 | 6.6→6.6, 3.0→3.0 |
| provenance_path | in-process / adjacency-map | 0.02 / 0.03 / 0.03 (n=100) | 0.05 / 0.07 / 0.08 (n=100) | 50/50, 50/50 | 6.6→6.6, 3.0→3.0 |
| write_batch | sqlite / production | 37.11 / 40.97 / 43.32 (n=30) | 69.14 / 81.45 / 81.77 (n=30) | — | 6.6→6.6, 3.0→3.0 |
| write_batch | sqlite / ports-only | 33.27 / 36.94 / 39.57 (n=30) | 35.62 / 40.29 / 94.83 (n=30) | — | 6.6→6.6, 3.0→3.0 |
| write_batch | postgres / production | 157.73 / 178.12 / 201.20 (n=30) | 140.28 / 149.54 / 155.54 (n=30) | — | 6.6→6.3, 3.0→3.2 |
| write_batch | postgres / ports-only | 145.12 / 161.44 / 162.31 (n=30) | 120.96 / 133.91 / 136.85 (n=30) | — | 6.3→6.2, 3.2→3.3 |
| write_batch | neo4j / cypher | 19.37 / 29.20 / 65.74 (n=30) | 16.04 / 25.12 / 50.48 (n=30) | — | 6.2→6.1, 3.3→3.3 |

### Profile `uniform` (scalebench as generated)

- 10000: beliefs 10000, episodes 2000, evidence 14536, rels {'contradicts': 1191, 'supports': 6756}, deps 176, redirects 169, mean degree 1.589, max degree 11, ev/belief 1.454, parity 1980/1980
  - store sqlite: load 3.1s (loadavg 3.41), disk 21.4 MB , mem 88 MB
  - store postgres: load 13.7s (loadavg 3.29), disk 19.5 MB , mem 180 MB
  - store neo4j: load 1.9s (loadavg 3.35), disk 276.1 MB /data/databases/neo4j=6408KiB /data/transactions/neo4j=263176KiB, mem 1537 MB
- 100000: beliefs 100000, episodes 20000, evidence 144863, rels {'contradicts': 11891, 'supports': 67144}, deps 1920, redirects 1674, mean degree 1.581, max degree 12, ev/belief 1.449, parity 1980/1980
  - store sqlite: load 40.0s (loadavg 3.11), disk 208.6 MB , mem 435 MB
  - store postgres: load 143.1s (loadavg 2.56), disk 156.4 MB , mem 195 MB
  - store neo4j: load 6.5s (loadavg 3.14), disk 325.3 MB /data/databases/neo4j=54512KiB /data/transactions/neo4j=263176KiB, mem 1660 MB

| workload | store / path | 10k p50 / p95 / p99 ms | 100k p50 / p95 / p99 ms | parity | load avg |
|---|---|---|---|---|---|
| round_trip_floor | sqlite / SELECT 1 | 0.01 / 0.02 / 0.04 (n=100) | 0.01 / 0.02 / 0.02 (n=100) | — | 3.4→3.4, 3.5→3.5 |
| round_trip_floor | postgres / SELECT 1 | 0.21 / 0.29 / 0.31 (n=100) | 0.22 / 0.26 / 0.30 (n=100) | — | 3.4→3.4, 3.5→3.5 |
| round_trip_floor | neo4j / RETURN 1 (HTTP) | 2.09 / 4.23 / 4.98 (n=100) | 1.47 / 2.57 / 3.49 (n=100) | — | 3.4→3.4, 3.5→3.5 |
| recall_expand_hops2 | sqlite / production | 0.57 / 1.21 / 1.26 (n=100) | 0.67 / 1.29 / 1.47 (n=100) | 50/50, 50/50 | 3.4→3.4, 3.5→3.5 |
| recall_expand_hops2 | sqlite / single-query | 2.29 / 2.60 / 2.70 (n=100) | 20.09 / 25.13 / 31.01 (n=100) | 50/50, 50/50 | 3.4→3.4, 3.5→3.5 |
| recall_expand_hops2 | postgres / production | 1.16 / 1.42 / 2.00 (n=100) | 1.26 / 1.84 / 4.22 (n=100) | 50/50, 50/50 | 3.4→3.4, 3.5→3.4 |
| recall_expand_hops2 | postgres / single-query | 1.63 / 2.13 / 2.27 (n=100) | 0.49 / 0.61 / 0.69 (n=100) | 50/50, 50/50 | 3.4→3.4, 3.4→3.4 |
| recall_expand_hops2 | neo4j / cypher-apoc-bfs | 2.61 / 6.13 / 10.25 (n=100) | 2.69 / 4.59 / 7.93 (n=100) | 50/50, 50/50 | 3.4→3.8, 3.4→3.4 |
| recall_expand_hops2 | neo4j / cypher-per-hop | 4.14 / 7.82 / 9.82 (n=100) | 4.91 / 10.27 / 12.30 (n=100) | 50/50, 50/50 | 3.8→3.8, 3.4→3.4 |
| recall_expand_hops2 | neo4j / cypher-shortest | 388.81 / 447.86 / 476.50 (n=77) | 2.41 / 4.29 / 6.16 (n=100) | 50/50, 50/50 | 3.8→3.3, 3.4→3.4 |
| recall_expand_hops2 | in-process / adjacency-map | 0.03 / 0.07 / 0.08 (n=100) | 0.04 / 0.05 / 0.06 (n=100) | 50/50, 50/50 | 3.3→3.3, 3.4→3.4 |
| recall_expand_hops5 | sqlite / production | 1.17 / 1.85 / 2.28 (n=100) | 2.02 / 2.47 / 4.33 (n=100) | 50/50, 50/50 | 3.3→3.3, 3.4→3.4 |
| recall_expand_hops5 | sqlite / single-query | 3.22 / 3.64 / 3.71 (n=100) | 23.28 / 27.08 / 27.72 (n=100) | 50/50, 50/50 | 3.3→3.3, 3.4→3.4 |
| recall_expand_hops5 | postgres / production | 3.03 / 3.50 / 4.61 (n=100) | 3.04 / 3.36 / 5.30 (n=100) | 50/50, 50/50 | 3.3→3.3, 3.4→3.9 |
| recall_expand_hops5 | postgres / single-query | 1.95 / 2.12 / 2.16 (n=100) | 1.13 / 1.33 / 1.74 (n=100) | 50/50, 50/50 | 3.3→3.2, 3.9→3.9 |
| recall_expand_hops5 | neo4j / cypher-apoc-bfs | 2.64 / 3.82 / 5.53 (n=100) | 2.67 / 4.91 / 6.14 (n=100) | 50/50, 50/50 | 3.2→3.2, 3.9→3.9 |
| recall_expand_hops5 | neo4j / cypher-per-hop | 6.60 / 9.41 / 11.96 (n=100) | 6.96 / 9.45 / 10.83 (n=100) | 50/50, 50/50 | 3.2→3.2, 3.9→3.9 |
| recall_expand_hops5 | neo4j / cypher-shortest | 3.99 / 6.77 / 8.67 (n=100) | 3.52 / 7.06 / 12.94 (n=100) | 50/50, 50/50 | 3.2→3.2, 3.9→3.9 |
| recall_expand_hops5 | in-process / adjacency-map | 0.05 / 0.13 / 0.14 (n=100) | 0.08 / 0.11 / 0.13 (n=100) | 50/50, 50/50 | 3.2→3.2, 3.9→3.9 |
| retraction_closure | sqlite / production | 1.06 / 3.91 / 4.03 (n=100) | 0.77 / 19.37 / 20.80 (n=100) | 50/50, 50/50 | 3.2→3.2, 3.9→3.9 |
| retraction_closure | sqlite / single-query | 4.31 / 6.17 / 8.63 (n=100) | 33.99 / 52.68 / 54.62 (n=100) | 50/50, 50/50 | 3.2→3.2, 3.9→3.7 |
| retraction_closure | postgres / production | 8.84 / 33.80 / 35.36 (n=100) | 7.87 / 172.31 / 186.16 (n=100) | 50/50, 50/50 | 3.2→3.2, 3.7→3.8 |
| retraction_closure | postgres / single-query | 1.42 / 1.71 / 1.95 (n=100) | 0.44 / 1.76 / 1.83 (n=100) | 50/50, 50/50 | 3.2→3.2, 3.8→3.8 |
| retraction_closure | neo4j / cypher | 1.75 / 5.40 / 9.97 (n=100) | 4.52 / 13.41 / 15.29 (n=100) | 50/50, 50/50 | 3.2→3.2, 3.8→3.8 |
| retraction_closure | in-process / adjacency-map | 0.01 / 0.05 / 0.07 (n=100) | 0.02 / 0.30 / 0.35 (n=100) | 50/50, 50/50 | 3.2→3.2, 3.8→3.8 |
| dissonance_page | sqlite / production | 8.95 / 9.91 / 10.55 (n=100) | 119.77 / 135.31 / 142.35 (n=100) | 10/10, 10/10 | 3.2→3.4, 3.8→3.3 |
| dissonance_page | sqlite / single-query | 3.66 / 4.01 / 7.43 (n=100) | 45.16 / 48.20 / 50.13 (n=100) | 10/10, 10/10 | 3.4→3.4, 3.3→3.1 |
| dissonance_page | sqlite / indexed | 0.37 / 0.60 / 0.62 (n=100) | 1.27 / 1.79 / 1.83 (n=100) | 10/10, 10/10 | 3.4→3.4, 3.1→3.1 |
| dissonance_page | postgres / production | 7.79 / 9.81 / 10.86 (n=100) | 83.82 / 102.18 / 109.05 (n=100) | 10/10, 10/10 | 3.4→3.4, 3.1→3.8 |
| dissonance_page | postgres / single-query | 3.09 / 3.85 / 4.01 (n=100) | 32.69 / 37.66 / 42.86 (n=100) | 10/10, 10/10 | 3.4→3.4, 3.8→3.8 |
| dissonance_page | postgres / indexed | 1.24 / 3.01 / 3.28 (n=100) | 1.93 / 2.56 / 2.70 (n=100) | 10/10, 10/10 | 3.4→3.4, 3.8→3.6 |
| dissonance_page | neo4j / cypher | 2.24 / 3.59 / 5.97 (n=100) | 5.07 / 6.81 / 12.30 (n=100) | 10/10, 10/10 | 3.4→3.4, 3.6→3.6 |
| dissonance_page | in-process / adjacency-map | 0.00 / 0.02 / 0.02 (n=100) | 0.00 / 0.02 / 0.02 (n=100) | 10/10, 10/10 | 3.4→3.4, 3.6→3.6 |
| contradiction_neighbourhood | sqlite / production | 0.26 / 0.49 / 0.52 (n=100) | 0.44 / 0.65 / 0.73 (n=100) | 50/50, 50/50 | 3.4→3.4, 3.6→3.6 |
| contradiction_neighbourhood | postgres / production | 0.46 / 0.72 / 0.90 (n=100) | 0.45 / 0.57 / 0.92 (n=100) | 50/50, 50/50 | 3.4→3.4, 3.6→3.6 |
| contradiction_neighbourhood | neo4j / cypher | 1.00 / 1.34 / 2.41 (n=100) | 0.97 / 1.36 / 2.93 (n=100) | 50/50, 50/50 | 3.4→3.4, 3.6→3.6 |
| contradiction_neighbourhood | in-process / adjacency-map | 0.01 / 0.02 / 0.07 (n=100) | 0.00 / 0.01 / 0.02 (n=100) | 50/50, 50/50 | 3.4→3.4, 3.6→3.6 |
| merge_redirects | sqlite / production | 0.77 / 1.14 / 1.27 (n=100) | 0.84 / 1.19 / 1.34 (n=100) | 50/50, 50/50 | 3.4→3.4, 3.6→3.6 |
| merge_redirects | sqlite / single-query | 3.58 / 4.07 / 4.21 (n=100) | 33.59 / 35.77 / 36.38 (n=100) | 50/50, 50/50 | 3.4→3.4, 3.6→3.6 |
| merge_redirects | postgres / production | 3.29 / 5.35 / 5.67 (n=100) | 3.70 / 5.34 / 5.95 (n=100) | 50/50, 50/50 | 3.4→3.4, 3.6→3.5 |
| merge_redirects | postgres / single-query | 0.28 / 0.30 / 0.37 (n=100) | 0.30 / 0.36 / 0.39 (n=100) | 50/50, 50/50 | 3.4→3.4, 3.5→3.5 |
| merge_redirects | neo4j / cypher | 1.05 / 2.48 / 4.62 (n=100) | 2.73 / 5.30 / 6.92 (n=100) | 50/50, 50/50 | 3.4→3.4, 3.5→3.5 |
| merge_redirects | in-process / adjacency-map | 0.01 / 0.01 / 0.02 (n=100) | 0.01 / 0.01 / 0.02 (n=100) | 50/50, 50/50 | 3.4→3.4, 3.5→3.5 |
| provenance_path | sqlite / production | 0.13 / 0.19 / 0.73 (n=100) | 0.27 / 0.61 / 0.74 (n=100) | 50/50, 50/50 | 3.4→3.4, 3.5→3.5 |
| provenance_path | sqlite / single-query | 0.10 / 0.12 / 0.13 (n=100) | 0.19 / 0.30 / 0.45 (n=100) | 50/50, 50/50 | 3.4→3.4, 3.5→3.5 |
| provenance_path | postgres / production | 0.85 / 1.02 / 1.11 (n=100) | 1.36 / 2.24 / 2.81 (n=100) | 50/50, 50/50 | 3.4→3.4, 3.5→3.5 |
| provenance_path | postgres / single-query | 0.35 / 0.41 / 0.46 (n=100) | 0.64 / 0.79 / 0.94 (n=100) | 50/50, 50/50 | 3.4→3.4, 3.5→3.5 |
| provenance_path | neo4j / cypher | 1.19 / 1.96 / 4.45 (n=100) | 1.35 / 3.00 / 4.53 (n=100) | 50/50, 50/50 | 3.4→3.4, 3.5→3.5 |
| provenance_path | in-process / adjacency-map | 0.02 / 0.03 / 0.04 (n=100) | 0.02 / 0.03 / 0.04 (n=100) | 50/50, 50/50 | 3.4→3.4, 3.5→3.5 |
| write_batch | sqlite / production | 22.33 / 28.94 / 29.80 (n=30) | 62.82 / 79.74 / 137.36 (n=30) | — | 3.4→3.2, 3.5→3.5 |
| write_batch | sqlite / ports-only | 25.08 / 46.10 / 58.50 (n=30) | 20.92 / 31.04 / 40.03 (n=30) | — | 3.2→3.2, 3.5→3.5 |
| write_batch | postgres / production | 81.65 / 89.96 / 98.58 (n=30) | 82.53 / 90.85 / 104.11 (n=30) | — | 3.2→3.2, 3.5→3.6 |
| write_batch | postgres / ports-only | 66.26 / 72.06 / 78.05 (n=30) | 68.46 / 74.27 / 74.74 (n=30) | — | 3.2→3.1, 3.6→3.6 |
| write_batch | neo4j / cypher | 6.28 / 14.89 / 21.87 (n=30) | 5.67 / 13.18 / 75.79 (n=30) | — | 3.1→3.1, 3.6→3.6 |

### What the numbers say (dense, 100k; best path per store)

| Workload | best relational (p50 / p95 ms) | Neo4j best (p50 / p95 ms) | relational production (p50 / p95 ms) | in-process map |
|---|---|---|---|---|
| expand, 2 hops | SQLite production 4.0 / 10.6 | `ANY SHORTEST` 6.9 / 15.9 | SQLite 4.0 / 10.6, Postgres 4.2 / 15.5 | 0.26 |
| expand, 5 hops (~70k beliefs reached) | Postgres CTE 270 / 549 | APOC BFS 809 / 1,226 | SQLite 1,120 / 2,033, Postgres 591 / 966 | 33 |
| retraction closure | Postgres CTE 0.67 / 4.8 | 11.1 / 34.1 | SQLite 3.9 / 110, **Postgres 469 / 1,263** | 0.03 |
| dissonance page | SQLite + index 1.3 / 1.8 | 6.3 / 8.7 | **SQLite 663 / 806, Postgres 453 / 522** | 0.01 |
| contradiction neighbourhood | Postgres 0.80 / 1.5 | 1.4 / 2.3 | (same) | 0.01 |
| merge redirects | Postgres CTE 0.30 / 0.33 | 1.5 / 3.2 | SQLite 0.80 / 1.2, Postgres 3.9 / 5.8 | 0.01 |
| provenance path | SQLite CTE 0.20 / 0.23 | 2.5 / 4.4 | SQLite 0.32 / 0.45, Postgres 1.5 / 1.8 | 0.05 |
| write batch (50 beliefs) | SQLite ports 35.6 / 40.3 | MERGE 16.0 / 25.1 | SQLite 69 / 81, Postgres 140 / 150 | — |

- **On reads, Neo4j is never the fastest store.** On every read workload the
  best relational path, production or a single statement, is as fast as Neo4j
  or faster. Neo4j is 1.7–3× slower on expansion and 1.7–17× slower on the
  lookups (neighbourhood, redirects, provenance, retraction).
- **The large gaps are in two production paths, not in the model.**
  - `list_dissonances` reads every association (`ListAll`) to page 50
    contradictions: 453–663 ms at 100k. An index on
    `relationships(type, created_at)` brings the same page to 1.3–2.1 ms. That
    is 200–500×, and faster than Neo4j's 6.3 ms with its relationship index.
  - Retraction on Postgres makes one round trip per episode (a journal lookup
    and a dependency read for each): 469 ms p50 and 1.26 s p95. One recursive
    CTE answers the same question in 0.67 ms.
- **Hop-5 expansion is slow everywhere**, because on a real-density graph five
  hops reach about 70% of the brain. The best engine path is 270 ms (Postgres
  CTE) and the in-process adjacency map takes 33 ms. If deep expansion ever
  becomes interactive, the cheapest fix is an in-process index or a bound on
  the frontier, not a graph engine.
- **Batch writes are the one place Neo4j wins:** 2.2× over SQLite's port writes
  and 7.5× over Postgres's at dense 100k, and 3.7× over SQLite on the uniform
  corpus. The relational path issues one statement per episode and per belief.
  It also maintains status history, text keys and FTS, which the Cypher `MERGE`
  does not. In Mnemos, persistence is a small share of a write: `Remember`
  costs 2.2 s p50 at 100k (consolidation baseline), against 35–140 ms here.
  Batching the relational statements would be the cheap fix. That is not
  measured here.
- **Loading:** Neo4j bulk-loads fastest (12.8 s at dense 100k, against 52 s for
  SQLite and 269 s for Postgres through the ports). This is the same batching
  effect.
- **Footprint (dense 100k):**
  - Disk: SQLite 334 MB and Postgres 338 MB, against Neo4j's 116 MB of store
    files plus 514 MiB of preallocated transaction log. Neo4j stores fewer
    properties: no FTS, no history tables, about 8 belief properties against
    ~35 columns.
  - Memory: Neo4j's JVM used 1.6–1.7 GiB resident, set by the 1 GiB heap and
    1 GiB page cache the script configures. Postgres used 0.18 GiB, and the
    whole in-process SQLite + harness process 0.8 GiB.
- **Transport is not the explanation.** Neo4j's empty HTTP round trip was 1.2–2.1
  ms. Its fastest real calls (neighbourhood, redirects) took 1.0–1.5 ms. So
  HTTP+JSON adds at most ~1–2 ms per call against Bolt. That narrows the short
  lookups. It does not change any ordering above 3 ms.
- **The uniform corpus** (scalebench's own density, mean degree 1.6) puts
  every store in the same order. Everything is faster because expansion
  reaches only ~35 beliefs at 2 hops and ~100 at 5.

## Limitations

- **Synthetic data.** The dense profile matches a real brain's means, but its
  degree tail is lighter: max ~110 against 993, and ~2% of beliefs
  unconnected against 21%. The dependency and redirect rates are assumptions,
  because the sampled brain predates both features. Topic locality is
  synthetic.
- **One machine, warm caches.** Each cell is warm, after warm-up calls, with
  the database in the OS page cache. Postgres and Neo4j run in Docker Desktop's
  VM (4 CPUs), and SQLite runs on the host. Concurrent work on the machine is
  bounded by the load-average gate, not eliminated. Compare ratios within a
  run, not absolutes across hosts.
- **Configuration.** Postgres runs `postgres:16-alpine` with default settings
  (128 MB shared buffers). Neo4j runs with a 1 GiB heap, a 1 GiB page cache and
  a 10 s checkpoint interval, and is otherwise at defaults. Neither was tuned.
- **Client and transport.** Neo4j is reached over HTTP/JSON, not Bolt, to keep
  go.mod free of a driver. The floor is measured and reported above.
- **What Cypher does.** The Cypher statements are idiomatic but not exhaustive.
  APOC is the only plugin, and GDS was not tried. The Cypher write does less
  work than the production write.
- **Retraction.** The workload replicates the read side and stops before the
  writes. With only the root retracted, the single-query path treats "tainted"
  as "in the closure", which is exact for this corpus. A general version seeds
  the walk with every retracted episode.
- **Sizes.** Only 10k and 100k. A 1M run was out of scope: Postgres loading
  through the ports alone would take over an hour.
