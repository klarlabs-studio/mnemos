"""Statistics for #441: paired bootstrap and judge agreement.

Pre-registered (comment on #441, 2026-10-10): each comparison is a paired
bootstrap with 10,000 resamples over questions, reported as a difference with
a 95% confidence interval.
"""

from __future__ import annotations

import random


def paired_bootstrap(a: list[bool], b: list[bool], resamples: int = 10_000, seed: int = 441) -> dict:
    """Difference in accuracy (a − b) over the same questions, with a 95% CI."""
    if len(a) != len(b) or not a:
        raise ValueError("paired samples must be non-empty and equal length")
    n = len(a)
    diffs = [int(x) - int(y) for x, y in zip(a, b)]
    rng = random.Random(seed)
    means = sorted(sum(diffs[rng.randrange(n)] for _ in range(n)) / n for _ in range(resamples))
    return {
        "n": n,
        "diff": sum(diffs) / n,
        "ci_low": means[int(0.025 * resamples)],
        "ci_high": means[int(0.975 * resamples) - 1],
    }


def cohen_kappa(x: list[bool], y: list[bool]) -> float:
    """Agreement between two binary raters, corrected for chance."""
    n = len(x)
    if n == 0:
        return float("nan")
    po = sum(a == b for a, b in zip(x, y)) / n
    px, py = sum(x) / n, sum(y) / n
    pe = px * py + (1 - px) * (1 - py)
    return 1.0 if pe == 1 else (po - pe) / (1 - pe)
