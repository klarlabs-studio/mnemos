# Consolidation baseline (Phase 0, #382)

Recorded 2026-10-04 against `main` @ `11d2778` (v0.127.1). The scale runs used
`tools/scalebench` @ `2d2e1dc`, which is `11d2778` plus the harness and no
product change. Every later phase compares against these numbers at the same
params.

**Hardware:** Apple M5, 10 cores, 24 GiB, darwin/arm64, Go 1.27.1 (the module
declares toolchain go1.26.7). Single-machine numbers. Compare ratios between
runs on the same host, not absolutes across hosts.

## Gates

| Gate | Result | Wall time |
|---|---|---|
| `make build` | ok, binary 45,229,506 bytes | 79 s |
| `go test -count=1 ./...` | pass | 59 s |
| `go test -race -count=1 ./...` | pass | 411 s |
| `golangci-lint run` | 0 issues | 33 s |
| `go vet ./...` | clean | 7 s |
| `govulncheck ./...` | no vulnerabilities | 5 s |
| `tools/mutate -pkg ./internal/trust -threshold 0.70` | pass; 1 survivor `internal/trust/trust.go:96:8` (leq→geq) | 28 s |
| `tools/brainbench` | ran; report not gated (see `make brain-eval`) | 1 s |

## Micro-benchmarks (`-count=3`, first sample shown)

| Benchmark | 1k | 10k | 50k |
|---|---|---|---|
| `DetectIncremental` | 10.6 ms | 251 ms | 1.06 s |
| `BuildCandidateIndex` | 4.3 ms | 39.9 ms | 106 ms |
| `Recall_SQLite/candidate` | 22.4 ms | 109 ms | 618 ms |
| `Recall_SQLite/corpus-scan` | 14.4 ms | 111 ms | 1.68 s |

## Scale (`scalebench`, shape `uniform`, seed 1)

Reproduce with `go run ./tools/scalebench -beliefs N -queries 50 -ingests 20 -timeout 30m -json out.json`.
The raw reports are next to this file.

| Operation (p50 unless noted) | 10k | 100k | 1M |
|---|---|---|---|
| Bulk load (beliefs/s) | 2,862 | 1,566 | 2,638 |
| `recompute-trust --all` | 252 ms | 1.62 s | 39.7 s |
| Open populated store | 3 ms | 2 ms | 6 ms |
| Recall, hops 0 (p50 / p95) | 8.0 / 11.5 ms | 53.8 / 60.2 ms | 985 / 2,702 ms |
| Recall, hops 1 (p50 / p95) | 8.7 / 15.5 ms | 52.9 / 59.7 ms | 601 / 882 ms |
| `BrainHealth` | 251 ms | 1.13 s | 12.8 s |
| `KnowledgeGaps(20)` | 124 ms | 628 ms | 6.16 s |
| `Remember` (p50 / p95) | 280 / 322 ms | 2.22 / 4.15 s | 18.8 / 23.4 s |
| Peak heap in use | 33 MiB | 295 MiB | 4.13 GiB |
| Max RSS | 77 MiB | 465 MiB | 4.45 GiB |
| DB size | 37 MiB | 340 MiB | 3.33 GiB |

### What the baseline says

- **The write path breaks first.** `Remember` grows roughly linearly with the
  corpus: 0.28 s at 10k, 18.8 s at 1M. Incremental relationship detection is
  the dominant term (`DetectIncremental` ≈ 1 s at only 50k). A 1M-belief brain
  cannot accept captures interactively.
- **Health and gaps are full scans.** Both grow ~linearly: `BrainHealth` 12.8 s
  and `KnowledgeGaps` 6.2 s at 1M. This is the Phase 5.2 target.
- **Recall is sub-linear at 100k but not at 1M.** p95 at hops 0 is 2.7 s. The
  hops-0 run is also the first recall after load, so part of its gap to hops 1
  is a cold page cache. Read the hops-1 row as the steadier number.
- **Memory grows ~linearly with corpus:** about 4.3 KiB of peak heap per
  belief at 1M.
- **The full trust recompute is a 40 s single pass at 1M.** That bounds a
  Phase 2 backfill done in one shot. A batched backfill must beat it per batch.

### Not yet baselined

- Provider matrix (postgres, mysql, libsql): since #418 the integration suite
  runs on every push through warden, against real Postgres and MySQL.
- Recall-quality gate (`benchmarks/`, Python): runs in CI on every PR. Its
  current status is the CI result on the merge commit.
- Pathological shapes: hub and contradiction are gated at 100k since Phase 5
  (below); common-token, skewed-evidence and stale were measured at 100k but
  are not gated.
- An actual production brain upgrade (Phase 7.15).

## After Phase 5 (2026-10-06)

Same harness, same host, `main` after #416–#429. 1M figures are p50 from
single warm runs. At that size a run swings 2–4× with the OS page cache
(health measured 57–221 s across runs of one commit before #421), so compare
ratios, not decimals.

| Operation, 1M uniform | Phase 0 | After Phase 5 | What changed |
|---|---|---|---|
| `Remember` | 18.8 s | **299 ms** (p95 514) | Supports budget (#417), candidate relate (#420, fixed in #427), candidate budget (#429) |
| `BrainHealth` | 12.8 s ¹ | **2.6 s** (sampled) | Single snapshot (#421), sampling + SQL counts (#422, #424) |
| `BrainHealthFull` | — | 10.8 s | The exact scan, now opt-in |
| `KnowledgeGaps` | 6.16 s | **2.6 s** | Gap candidates (#423) |
| Recall, hops 1 | 601 ms | 577–590 ms | Unchanged by design |
| Belief browse, first page | (full read, 7–11 s) | **0.7 s** | Keyset cursors (#425, #426) |

¹ Phase 2's canonical-trust inputs had raised it to 84 s on `main` before Phase 5.

On the contradiction-dense shape (2M contradictions over 1M beliefs), health
and gaps stay slower (about 9–21 s and 12 s): the exact counts the sampled
path keeps (dissonance, dangling edges) and a candidate set that is most of
the brain dominate there.

A correction. Between #420 and #427, `Remember` timings at 100k and 1M (73–97
ms and 1.2 s) measured writes that skipped relate entirely. The candidate read
exceeded SQLite's bound-variable limit and the write path swallowed the error
(#427, #428). The figures above were taken with the relate trace on, which
shows every measured write relating and keeping its 40 edges. With exact
candidates and the fix, `Remember` was 713 ms at 100k and 7.05 s at 1M. The
candidate budget (ADR 0028) brought those to 28.6 ms and 299 ms.

The relationship graph was the root cause: one capture wrote ~33,400
supports edges at 100k beliefs. A real 233k-claim brain held 32.4M of them.
On a copy of that brain, `relate --prune-supports` (#419) took it to 2.64M
edges and the file from 10.26 GB to 1.55 GB after `VACUUM`.

### Gate

`make scale-gate` runs 100k beliefs over the uniform, hub and contradiction
shapes, median of three warm rounds, against `bench/scale-gate.json`. Ceilings
are ≈3× the worst of two measurement rounds. The second round's medians, with
the relate trace confirming every write related:

| p50 ms | uniform | hub | contradiction |
|---|---|---|---|
| `Remember` | 55 | 60 | 41 |
| `BrainHealth` | 1,485 | 1,341 | 1,802 |
| `BrainHealthFull` | 2,406 | 2,429 | 2,540 |
| `KnowledgeGaps` | 599 | 529 | 1,663 |
| Recall hops 1 (p95) | 442 | 2,467 | 111 |

Rounds vary: the first round measured uniform health at 985 ms and hub recall
p95 at 449 ms. That spread is why the ceilings sit well above either.

## Storage growth and maintenance cost (2026-11-18)

Reproduce with `go run ./tools/scalebench -beliefs N -churn -json out.json`.
The raw reports are `scale-churn-uniform-10000.json` (commit `25db8e3`) and
`scale-churn-uniform-100000.json` (commit `8d729a9`: same measurement, before
the restatement guard existed, so its `findings` is `null`). Both ran on
`main` @ `72ffa6e` plus the harness only. Hardware: Apple M5, 10 cores,
24 GiB, darwin/arm64, Go 1.27.4. The 100k run shared the machine with other
builds (bulk load 540 beliefs/s against 2,683/s for the quiet 10k run), so
treat its latencies as an upper bound and compare ratios.

Every operation now reports p99 too. `-churn` adds a `storage` section and
the maintenance operations below. Every churn phase performs the same number
of operations at every size, so per-operation figures compare across sizes.

### Footprint after seeding

| | 10k | 100k |
|---|---|---|
| On disk | 21.0 MiB | 205.4 MiB |
| Bytes per belief | 2,200 | 2,153 |
| Bytes per episode | 10,998 | 10,767 |

Footprint is linear in beliefs. At 100k the biggest tables are `claims` (26%,
563 B/belief), the `claim_tokens` candidate index (23%, 7.8 rows and 493 B per
belief), `claim_versions` (10%), `relationships` (10%), `claim_evidence` (9%)
and `claim_status_history` (6%, 139 B/belief). Seeding writes one version row
and one status-history row per belief. Every other history table starts empty.

### History growth per operation

Rows added per operation, identical at 10k and 100k unless both are shown:

| Phase (ops) | Kind | History rows per op |
|---|---|---|
| live recall, hops 1 (200) | recall | none (it updates rows in place: Hebbian, reconsolidation, inhibition) |
| recorded-as-of recall (200 + 200) | read | none, and nothing committed (ADR 0036 §2 holds) |
| `Get` (200), `BrainHealth` (1) | read | none, and nothing committed |
| re-ingest the same document (20) | write | none. Adds 1 episode and its FTS rows per call, no evidence (#443) |
| feedback (50) | write | 0.5 `trust_input_history`, 1 `claim_versions` |
| decisions with observed expectations (20) | write | none |
| consolidation pass (1, 100 seeded merge pairs) | maintenance | 10k: 54 merged, 54 status, 73 closed evidence, 40 credit, 41 journal. 100k: 46, 46, 59, 40, 41 |
| retract event (5) | write | 3–4 status, 3–5 closed evidence, 1 journal |
| un-retract event (5) | write | the same status rows again, 1 journal, closed evidence kept |
| deprecate (50) | write | 1 status, 1 version |
| `recompute-trust --all` | maintenance | none |

No read grows a history table, and history growth per write does not change
with brain size. Every history row is one change to one belief. Over the
whole churn the brain grew from 2,153 to 2,236 B/belief at 100k. At 10k it
grew from 2,200 to 2,847 B/belief, mostly the WAL (page bytes went from
21.0 to 21.8 MiB).

### Maintenance cost

| Operation (p50) | 10k | 100k |
|---|---|---|
| Consolidation, the full sleep pass (`sleepArgs` minus the LLM step) | 858 ms | 13.6 s |
| Retract event and build the impact report (p50 / p95) | 3.5 / 6.1 ms | 4.7 / 10.6 ms |
| Un-retract event (p50 / p95) | 2.9 / 9.9 ms | 4.2 / 17.8 ms |
| `recompute-trust --all` after churn | 414 ms | 6.0 s |
| Live recall after churn, hops 1 (p50 / p95 / p99) | 11.9 / 15.2 / 17.7 ms | 90.8 / 113 / 127 ms |
| Recorded-as-of recall (p50 / p95 / p99) | 31.5 / 44.0 / 54.8 ms | 90.7 / 122 / 134 ms |

- **Time travel is not O(history).** It adds about 20 ms at 10k (2.6× live)
  and nothing measurable at 100k, where scoring the candidates dominates.
  `recorded.Claims.ListByIDs` and `ListByEventIDs` read the status and
  validity history one claim at a time (two queries per candidate). That is
  the fixed overhead. A bulk by-IDs history read would remove it, but no
  port for that exists yet.
- **Retraction and un-retraction cost stays flat with brain size.** Each
  touches only the event's claims.
- **Consolidation grows roughly linearly with the brain.** About 6 s of the
  13.6 s at 100k is its full trust recompute. The rest is the dedupe scan,
  credit, salience and the journal.

### Defect found: `RememberClaim` never folds a restatement

A held belief restated through `Memory.RememberClaim` (a new episode in a new
session, then the claim citing it) adds a **second belief row** instead of
an evidence link to the held one. The run found 20 of 20 restatements doing
this, at both sizes. Each duplicate also costs about 8 `claim_tokens` rows,
its FTS rows and about 22 relationship edges: 445–482 edges for 20
restatements. The cause is that `rememberClaimExecutor`
(`governed_executors.go`) calls `Claims.Upsert` directly, and so skips
`pipeline.CollapseKnownClaims`. Every other write path applies it
(`PersistArtifacts`, MCP `process_text`, git/PR context, auto-ingest).

The same path serves `RememberClaimWithEvidence`, and through it REST
`POST /v1/facts` and gRPC `RememberFact`. Left unfixed here: the fix changes
the id those calls return for a restatement, and that is a contract decision
across transports. The `restate_new_session` phase reports it as a `DEFECT`
finding until it is fixed.

A related gap: `Memory.Remember` cannot express a session. `Item.Metadata`
lands on the episode as `input_session_id`, but the restatement fold reads
`session_id`. So through the library, every restatement counts as
sessionless, and only MCP `process_text` can corroborate across sessions.

### Not measured

- memory:// was not run. Each open of `memory://` gets a fresh, isolated state,
  so a corpus loaded on one connection is invisible to the library's
  connection, and the census reads the SQLite file directly.
- Postgres was not run. The census is SQLite-specific (dbstat,
  `PRAGMA data_version`).
