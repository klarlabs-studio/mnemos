"""Build the Phase A report from answers.jsonl and verdicts.jsonl (#441).

Primary verdict: an answer is correct only if both judges say CORRECT.
Disagreement counts as wrong. Each judge's own accuracy and their agreement
(Cohen's kappa) are reported alongside, so the strict rule can be audited.
"""

from __future__ import annotations

import json
from collections import defaultdict
from pathlib import Path

from . import stats, systems

JUDGES = ("judge_mistral", "judge_qwen")

# Paired comparisons (a − b), each reported where both systems ran.
COMPARISONS = (
    ("mnemos", "bm25"),
    ("mnemos", "full-context"),
    ("mnemos-excerpts", "bm25"),
    ("mnemos-excerpts", "full-context"),
    ("mnemos-excerpts", "mnemos"),
    ("mnemos-excerpts-top10", "bm25"),
    ("mnemos-excerpts-top10", "mnemos-excerpts"),
    ("mnemos-excerpts-top20", "bm25"),
    ("mnemos-excerpts-top20", "mnemos-excerpts"),
    ("mnemos-excerpts-anchored", "bm25"),
    ("mnemos-excerpts-anchored", "mnemos-excerpts"),
    ("mnemos-excerpts-anchored", "mnemos-excerpts-reingest"),
    ("mnemos-excerpts-reingest", "mnemos-excerpts"),
    ("mnemos-excerpts-anchored-passages", "mnemos-excerpts-anchored"),
    ("mnemos-excerpts-anchored-passages", "bm25"),
    ("mnemos-excerpts-anchored-passages", "full-context"),
)


def _read(path: Path) -> list[dict]:
    return [json.loads(l) for l in path.read_text().splitlines() if l.strip()] if path.exists() else []


def build(d: Path, models: dict, extractor: str, spent: float) -> dict:
    answers = {(a["system"], a["question_id"]): a for a in _read(d / "answers.jsonl")}
    votes: dict = defaultdict(dict)
    unparsed = 0
    for v in _read(d / "verdicts.jsonl"):
        votes[(v["system"], v["question_id"])][v["judge"]] = v["correct"]
        unparsed += v["correct"] is None

    def verdict(key) -> bool | None:
        vs = votes.get(key, {})
        if any(vs.get(j) is None for j in JUDGES):
            return None
        return all(vs[j] for j in JUDGES)

    mn = systems.load_mnemos(d / "mnemos.jsonl") if (d / "mnemos.jsonl").exists() else {"answers": {}, "failed": []}
    out: dict = {
        "models": {k: {"id": m.id, "provider": m.provider} for k, m in models.items()} | {"extractor": {"id": extractor}},
        "context_budget_tokens": systems.CONTEXT_BUDGET_TOKENS,
        "spent_usd": round(spent, 4),
        "ingest": {
            "failed_haystacks": mn["failed"],
            "relate_skips": sum(a["relate_skips"] for a in mn["answers"].values()),
        },
        "unparsed_verdicts": unparsed,
        "benchmarks": {},
        "summary": {},
    }

    benches = sorted({a["benchmark"] for a in answers.values()})
    for b in benches:
        qids = sorted({qid for (s, qid), a in answers.items() if a["benchmark"] == b})
        # Compare systems only on questions every system answered and both judges graded.
        complete = [q for q in qids if all(verdict((s, q)) is not None for s in systems_in(answers, b))]
        bench: dict = {"questions": len(qids), "complete": len(complete), "systems": {}, "comparisons": {}}
        for s in systems_in(answers, b):
            rows = [answers[(s, q)] for q in complete]
            v = [verdict((s, q)) for q in complete]
            per_judge = {j: _mean([votes[(s, q)][j] for q in complete]) for j in JUDGES}
            by_type: dict = defaultdict(list)
            for r, ok in zip(rows, v):
                by_type["abstention" if r["abstention"] else r["type"]].append(ok)
            ev_rows = [r for r in rows if r["evidence_sessions"]]
            bench["systems"][s] = {
                "accuracy": _mean(v),
                "accuracy_by_judge": per_judge,
                "judge_kappa": stats.cohen_kappa([votes[(s, q)][JUDGES[0]] for q in complete],
                                                 [votes[(s, q)][JUDGES[1]] for q in complete]),
                "by_type": {t: {"n": len(x), "accuracy": _mean(x)} for t, x in sorted(by_type.items())},
                "context_tokens_mean": _mean([r["context_tokens"] for r in rows]),
                "evidence_recall": _mean([len(set(r["evidence_sessions"]) & set(r["context_sessions"])) / len(r["evidence_sessions"]) for r in ev_rows]),
                "evidence_any_hit": _mean([bool(set(r["evidence_sessions"]) & set(r["context_sessions"])) for r in ev_rows]),
            }
        for a, base in COMPARISONS:
            if a in bench["systems"] and base in bench["systems"]:
                bench["comparisons"][f"{a}_vs_{base}"] = stats.paired_bootstrap(
                    [verdict((a, q)) for q in complete], [verdict((base, q)) for q in complete])
        out["benchmarks"][b] = bench
        out["summary"][b] = {
            s: round(m["accuracy"], 3) for s, m in bench["systems"].items()
        } | {k: f"{c['diff']:+.3f} [{c['ci_low']:+.3f}, {c['ci_high']:+.3f}]" for k, c in bench["comparisons"].items()}
    out["summary"]["spent_usd"] = out["spent_usd"]
    return out


def systems_in(answers: dict, bench: str) -> list[str]:
    return sorted({s for (s, _), a in answers.items() if a["benchmark"] == bench})


def _mean(xs) -> float:
    xs = [x for x in xs if x is not None]
    return round(sum(xs) / len(xs), 4) if xs else float("nan")
