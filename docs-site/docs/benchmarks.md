# Benchmarks

Honest, reproducible cross-product comparisons. Source: [`benchmarks/`](https://github.com/klarlabs-studio/mnemos/tree/main/benchmarks).

## Suites

| Suite | What it tests |
|---|---|
| `contradiction_detection` | Feed deliberately-contradicting facts. Score precision/recall/F1 on detected `contradicts` edges. |
| `longmemeval` | Chat-history → question recall. Scores recall@1 against expected substrings. |
| `locomo` | Multi-session conversation cross-recall. Same scoring shape as longmemeval. |
| `replay_completeness` (planned) | Multi-step agent run. Ask each provider for the full chain. Measure ordered-recall completeness. |

## Latest result: contradiction_detection (rule-based mode)

```
Mnemos: precision 1.00, recall 1.00, F1 1.00 across 5 cases
```

| Case | F1 |
|---|---|
| direct polarity conflict | 1.00 |
| three-way partial conflict | 1.00 |
| no contradictions clean facts | 1.00 |
| numeric disagreement | 1.00 |
| implicit temporal conflict | 1.00 |

Reproduce locally:

```bash
docker compose -f benchmarks/docker-compose.yml up -d mnemos
python -m benchmarks.run --provider mnemos --suite contradiction_detection
python -m benchmarks.summarize
```

## CI gate

Every PR touching `internal/relate/`, `internal/extract/`, `internal/pipeline/`, `cmd/mnemos/`, or `benchmarks/` reruns the suite. The workflow fails when any baselined metric drops more than 0.05 below the recorded floor.

Update `benchmarks/baseline.json` in the same commit when you intentionally raise (or lower) a number.

## What we won't do

- Cherry-pick suites where Mnemos wins.
- Bury suites where Mnemos loses.
- Compare against products on different deployment models without flagging the asymmetry (managed vs OSS, online vs offline).
- Pretend latency or cost are the same across providers when they aren't.

The point is to know the truth — including when the truth is "we're not yet best at X."
