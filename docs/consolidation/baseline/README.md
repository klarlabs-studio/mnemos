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
