"""Phase A of #441: Mnemos against full-context and BM25 on public benchmarks.

Stages, each resumable from its output file:

    prepare  select questions and write the haystacks memeval ingests
    ingest   run tools/memeval (Mnemos) over the haystacks
    answer   build each system's context and answer every question
    judge    grade every answer with both judges
    report   accuracy, judge agreement, retrieval recall, paired bootstrap, cost

    python -m benchmarks.phase_a.run prepare --run smoke --benchmark locomo --limit 20
    python -m benchmarks.phase_a.run ingest  --run smoke
    python -m benchmarks.phase_a.run answer  --run smoke
    python -m benchmarks.phase_a.run judge   --run smoke
    python -m benchmarks.phase_a.run report  --run smoke

The mnemos-excerpts systems (ADR 0031) read mnemos-excerpts.jsonl, which
ingest does not write. Produce it with tools/memeval -recall-only -excerpts
against brains kept by a memeval -keep run. Recall writes back to a brain
(reinforcement, reconsolidation), so recall against a copy of the brains, and
recall a control (-recall-only without -excerpts) from an identical copy to
confirm both arms recalled the same claim IDs. The -topK variants render that
same file and need no extra recall.

OPENROUTER_API_KEY must be set. Raw outputs go to --out (default: a directory
outside the repository), never to the repository.
"""

from __future__ import annotations

import argparse
import json
import os
import random
import subprocess
import sys
import threading
from concurrent.futures import ThreadPoolExecutor
from dataclasses import asdict
from pathlib import Path

from . import datasets, prompts, stats, systems
from .llm import BudgetExceeded, Client, Model

ROOT = Path(__file__).resolve().parents[2]
DATA = ROOT / "benchmarks" / "data" / "phase_a"

# Pinned per role. Recorded in every report.
MODELS = {
    "answerer": Model("google/gemini-2.5-flash-lite", "google-ai-studio", max_tokens=200),
    "judge_mistral": Model("mistralai/mistral-small-3.2-24b-instruct", "mistral/eu", max_tokens=5),
    "judge_qwen": Model("qwen/qwen3-30b-a3b-instruct-2507", "nebius/fp8", max_tokens=5),
}
EXTRACTOR = "google/gemini-2.5-flash-lite"  # Mnemos's own client; OpenRouter routes within Google.
SYSTEMS = ["mnemos", "mnemos-excerpts", "mnemos-excerpts-top10", "mnemos-excerpts-top20", "bm25", "full-context"]

# Systems built from a tools/memeval output: the file in the run directory and
# how its recall becomes the answerer's context. A system whose file is absent
# is skipped. mnemos-excerpts is memeval -recall-only -excerpts (ADR 0031);
# the -topK variants render that same recall with only the first K excerpted.
MNEMOS_SYSTEMS = {
    "mnemos": ("mnemos.jsonl", systems.mnemos),
    "mnemos-excerpts": ("mnemos-excerpts.jsonl", systems.mnemos_excerpts),
    "mnemos-excerpts-top10": ("mnemos-excerpts.jsonl", systems.mnemos_excerpts_top(10)),
    "mnemos-excerpts-top20": ("mnemos-excerpts.jsonl", systems.mnemos_excerpts_top(20)),
}


def run_dir(args) -> Path:
    d = Path(args.out) / args.run
    d.mkdir(parents=True, exist_ok=True)
    return d


def load_selected(d: Path) -> list[datasets.Haystack]:
    sel = json.loads((d / "selection.json").read_text())
    by_bench = datasets.load(DATA)
    out = []
    for bench, picks in sel["questions"].items():
        for h in by_bench[bench]:
            qs = [q for q in h.questions if q.id in set(picks)]
            if qs:
                out.append(datasets.Haystack(id=h.id, benchmark=bench, sessions=h.sessions, questions=qs))
    return out


def cmd_prepare(args) -> None:
    d = run_dir(args)
    by_bench = datasets.load(DATA)
    benches = ["longmemeval", "locomo"] if args.benchmark == "all" else [args.benchmark]
    sel: dict[str, list[str]] = {}
    rng = random.Random(args.seed)
    for b in benches:
        all_q = [q for h in by_bench[b] for q in h.questions]
        if args.stratify and b in args.stratify:
            sel[b] = stratified_sample(all_q, args.stratify[b], args.seed)
            continue
        qs = [q.id for q in all_q]
        if args.limit:
            qs = sorted(rng.sample(qs, min(args.limit, len(qs))))
        sel[b] = qs
    (d / "selection.json").write_text(json.dumps({"seed": args.seed, "limit": args.limit, "questions": sel}, indent=1))
    with (d / "haystacks.jsonl").open("w") as f:
        for h in load_selected(d):
            f.write(json.dumps({
                "id": h.id,
                "sessions": [asdict(s) for s in h.sessions],
                "questions": [{"id": q.id, "text": q.text} for q in h.questions],
            }) + "\n")
    print({b: len(v) for b, v in sel.items()}, "->", d)


def stratified_sample(questions: list, total: int, seed: int) -> list[str]:
    """Sample `total` questions in proportion to question_type (largest
    remainder), drawing independently within each type from one seeded RNG.
    The rule recorded on #441 before the run."""
    by_type: dict[str, list[str]] = {}
    for q in questions:
        by_type.setdefault(q.type, []).append(q.id)
    n = len(questions)
    quotas = {t: total * len(ids) / n for t, ids in by_type.items()}
    alloc = {t: int(x) for t, x in quotas.items()}
    for t in sorted(quotas, key=lambda t: (quotas[t] - alloc[t], t), reverse=True)[: total - sum(alloc.values())]:
        alloc[t] += 1
    rng = random.Random(seed)
    picked = []
    for t in sorted(by_type):
        picked += rng.sample(sorted(by_type[t]), alloc[t])
    return sorted(picked)


def cmd_ingest(args) -> None:
    d = run_dir(args)
    binary = Path(args.out) / "memeval"
    subprocess.run(["go", "build", "-o", str(binary), "./tools/memeval"], cwd=ROOT, check=True)
    env = {
        **os.environ,
        "MEMEVAL_LLM_PROVIDER": "openai-compat",
        "MEMEVAL_LLM_BASE_URL": "https://openrouter.ai/api",
        "MEMEVAL_LLM_API_KEY": os.environ["OPENROUTER_API_KEY"],
        "MEMEVAL_LLM_MODEL": EXTRACTOR,
        "MEMEVAL_EMBED_PROVIDER": "ollama",
        "MEMEVAL_EMBED_MODEL": "nomic-embed-text",
        "MEMEVAL_EMBED_BASE_URL": "http://localhost:11434",
        # Keep the extraction cache out of the user's real data directory.
        "XDG_DATA_HOME": str(Path(args.out) / "xdg"),
    }
    with (d / "ingest.log").open("a") as log:
        subprocess.run(
            [str(binary), "-in", str(d / "haystacks.jsonl"), "-out", str(d / "mnemos.jsonl"),
             "-workdir", str(Path(args.out) / "brains"), "-workers", str(args.workers), "-limit", "100"],
            env=env, stderr=log, check=True,
        )
    failed = sum(1 for l in (d / "mnemos.jsonl").read_text().splitlines() if '"error"' in l)
    exhausted = (d / "ingest.log").read_text().count("retry exhausted")
    print(f"ingest done; failed haystacks: {failed}; LLM calls that exhausted retries (rule-based fallback): {exhausted}")


def _jsonl_done(path: Path, key: str) -> set:
    if not path.exists():
        return set()
    return {json.loads(l)[key] for l in path.read_text().splitlines() if l.strip()}


def cmd_answer(args) -> None:
    d = run_dir(args)
    client = Client(str(Path(args.out) / "llm-cache.sqlite"), args.budget)
    haystacks = load_selected(d)
    recalls = {
        s: systems.load_mnemos(d / f)["answers"] if (d / f).exists() else {}
        for s, (f, _) in MNEMOS_SYSTEMS.items()
    }
    out = d / "answers.jsonl"
    done = {(r["system"], r["question_id"]) for r in map(json.loads, out.read_text().splitlines())} if out.exists() else set()
    lock = threading.Lock()
    jobs = []
    for h in haystacks:
        index = systems.BM25Index(h)
        for q in h.questions:
            for s in args.systems:
                if (s, q.id) in done:
                    continue
                if s in MNEMOS_SYSTEMS:
                    if q.id not in recalls[s]:
                        continue
                    ctx = MNEMOS_SYSTEMS[s][1](recalls[s][q.id])
                elif s == "bm25":
                    ctx = systems.bm25(index, q)
                else:
                    ctx = systems.full_context(h, q)
                jobs.append((s, h, q, ctx))
    print(f"{len(jobs)} answers to produce; spent so far ${client.spent:.3f}")

    def work(job):
        s, h, q, ctx = job
        text = client.complete(MODELS["answerer"], prompts.answer_messages(ctx.text, q))
        rec = {"system": s, "question_id": q.id, "benchmark": h.benchmark, "type": q.type,
               "abstention": q.abstention, "answer": text, "context_tokens": ctx.tokens,
               "context_sessions": ctx.sessions, "evidence_sessions": q.evidence_sessions}
        with lock, out.open("a") as f:
            f.write(json.dumps(rec) + "\n")

    _pool(work, jobs, args.workers)
    print(f"spent ${client.spent:.3f}")


def cmd_judge(args) -> None:
    d = run_dir(args)
    client = Client(str(Path(args.out) / "llm-cache.sqlite"), args.budget)
    questions = {q.id: q for h in load_selected(d) for q in h.questions}
    out = d / "verdicts.jsonl"
    done = {(r["system"], r["question_id"], r["judge"]) for r in map(json.loads, out.read_text().splitlines())} if out.exists() else set()
    answers = [json.loads(l) for l in (d / "answers.jsonl").read_text().splitlines() if l.strip()]
    jobs = [(a, j) for a in answers for j in ("judge_mistral", "judge_qwen") if (a["system"], a["question_id"], j) not in done]
    print(f"{len(jobs)} verdicts to produce; spent so far ${client.spent:.3f}")
    lock = threading.Lock()

    def work(job):
        a, j = job
        q = questions[a["question_id"]]
        raw = client.complete(MODELS[j], prompts.judge_messages(q, a["answer"]))
        rec = {"system": a["system"], "question_id": a["question_id"], "judge": j,
               "raw": raw, "correct": prompts.parse_verdict(raw)}
        with lock, out.open("a") as f:
            f.write(json.dumps(rec) + "\n")

    _pool(work, jobs, args.workers)
    print(f"spent ${client.spent:.3f}")


def _pool(work, jobs, workers) -> None:
    errors = 0
    with ThreadPoolExecutor(max_workers=workers) as ex:
        for fut in [ex.submit(work, j) for j in jobs]:
            try:
                fut.result()
            except BudgetExceeded as e:
                print("STOPPED:", e, file=sys.stderr)
                ex.shutdown(cancel_futures=True)
                raise
            except Exception as e:  # recorded, retried on the next run
                errors += 1
                if errors <= 5:
                    print("error:", e, file=sys.stderr)
    if errors:
        print(f"{errors} jobs failed; re-run the stage to retry them", file=sys.stderr)


def cmd_report(args) -> None:
    from .report import build

    d = run_dir(args)
    report = build(d, MODELS, EXTRACTOR, Client(str(Path(args.out) / "llm-cache.sqlite"), args.budget).spent)
    (d / "report.json").write_text(json.dumps(report, indent=1))
    print(json.dumps(report["summary"], indent=1))


def main() -> None:
    p = argparse.ArgumentParser()
    p.add_argument("stage", choices=["prepare", "ingest", "answer", "judge", "report"])
    p.add_argument("--run", required=True)
    p.add_argument("--out", default=os.environ.get("PHASE_A_OUT", str(Path.home() / ".cache" / "mnemos-phase-a")))
    p.add_argument("--benchmark", default="all", choices=["all", "longmemeval", "locomo"])
    p.add_argument("--limit", type=int, default=0, help="sample this many questions per benchmark (0 = all)")
    p.add_argument("--seed", type=int, default=441)
    p.add_argument("--sample-longmemeval", type=int, default=0,
                   help="stratified sample of this many LongMemEval questions (0 = all)")
    p.add_argument("--systems", nargs="+", default=SYSTEMS)
    p.add_argument("--workers", type=int, default=8)
    p.add_argument("--budget", type=float, default=30.0, help="hard stop in USD for this cache's total spend")
    args = p.parse_args()
    args.stratify = {"longmemeval": args.sample_longmemeval} if args.sample_longmemeval else {}
    {"prepare": cmd_prepare, "ingest": cmd_ingest, "answer": cmd_answer, "judge": cmd_judge, "report": cmd_report}[args.stage](args)


if __name__ == "__main__":
    main()
