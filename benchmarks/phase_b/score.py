"""Per-stage scores from Phase B labels (#441).

    python3 -m benchmarks.phase_b.score --dir DIR [--split dev|heldout|all] [--json report.json]
    python3 -m benchmarks.phase_b.score --dir DIR --export-questions questions.jsonl
    python3 -m benchmarks.phase_b.score --dir DIR --verdicts verdicts.jsonl --split heldout

Stages are scored separately, as HaluMem does, so a failure traces to its stage:

- extraction: precision and recall of the stored claims against the gold
  atomic claims, by the deterministic matching rule in match_claims (defined
  in docs/eval/phase-b-labelling-guide.md, "Matching rule"). The primary row,
  H3's measure, counts live claims only (not deprecated, not merged); the
  row over every claim linked to the episode is secondary (all_linked);
- relations: precision and recall by edge type (supports, contradicts,
  supersedes) over the claims each item displays;
- trust: Brier score, Brier skill score, expected calibration error and the
  reliability curve of the stored trust_score against the truth labels;
- agreement: Cohen's kappa between the pass-1 and pass-2 labellers on the
  items both labelled.

Answering is not scored here: no LLM runs in this module. --export-questions
writes the labelled questions in the Phase A question shape for the Phase A
answer and judge machinery, and --verdicts scores the per-question verdicts it
produces (score_answers), with an answer that relied on invalidated memory
counted wrong.

The default split is dev. Held-out items decide #442-#447 and are scored only
when --split heldout is asked for.
"""

from __future__ import annotations

import argparse
import json
import random
import re
import sys
import unicodedata
from pathlib import Path

from benchmarks.phase_a import stats
from benchmarks.phase_b import privacy

MATCH_THRESHOLD = 0.5
SENSITIVITY_THRESHOLDS = (0.4, 0.5, 0.6)
EDGE_TYPES = ("supports", "contradicts", "supersedes")
QUESTION_TYPES = ("single-episode", "multi-episode", "knowledge-update", "temporal", "abstention")
TRUTH_VALUES = ("true", "false", "unknown")
RESAMPLES = 10_000
SEED = 441

STOPWORDS = frozenset("""
a an the and or but if of to in on at by for with from as into onto over under about
is are was were be been being am do does did has have had it its this that these those
there here which who whom whose what when where why how than then so such very can
will would should could may might must shall also just only not no nor yet
""".split())

_TOKEN = re.compile(r"[a-z0-9]+(?:[._/\-][a-z0-9]+)*")


# ---- extraction ------------------------------------------------------------

def normalise(text: str) -> list[str]:
    """Content tokens of a claim: NFKC, lowercased, identifiers kept whole
    (v0.85.0, go1.22, foo_bar), stopwords dropped, a plural -s stripped."""
    text = unicodedata.normalize("NFKC", text).lower()
    out = []
    for tok in _TOKEN.findall(text):
        if tok in STOPWORDS:
            continue
        if len(tok) > 3 and tok.endswith("s") and not tok.endswith("ss") and tok.isalpha():
            tok = tok[:-1]
        out.append(tok)
    return out


def similarity(a: str, b: str) -> float:
    """Dice coefficient over the two token sets: 2|A∩B| / (|A| + |B|)."""
    x, y = set(normalise(a)), set(normalise(b))
    if not x or not y:
        return 0.0
    return 2 * len(x & y) / (len(x) + len(y))


def match_claims(gold: list[str], predicted: list[dict], links: dict | None = None,
                 threshold: float = MATCH_THRESHOLD) -> dict:
    """One-to-one matching of predicted claims to gold claims.

    1. Manual links first, in predicted order: links[claim_id] = gold index
       pairs them; links[claim_id] = None marks the claim as matching nothing.
       A gold claim already taken leaves a later link to it unmatched.
    2. The rest greedily, highest similarity first, ties broken by gold index
       then predicted order, among pairs with similarity >= threshold.
    """
    links = links or {}
    pairs: dict[str, int] = {}
    taken: set[int] = set()
    decided: set[str] = set()
    for c in predicted:
        if c["id"] not in links:
            continue
        decided.add(c["id"])
        g = links[c["id"]]
        if g is not None and 0 <= g < len(gold) and g not in taken:
            pairs[c["id"]] = g
            taken.add(g)
    candidates = []
    for pi, c in enumerate(predicted):
        if c["id"] in decided:
            continue
        for gi, g in enumerate(gold):
            if gi in taken:
                continue
            s = similarity(g, c.get("text", ""))
            if s >= threshold:
                candidates.append((-s, gi, pi, c["id"]))
    for _, gi, _, cid in sorted(candidates):
        if gi in taken or cid in pairs:
            continue
        pairs[cid] = gi
        taken.add(gi)
    return {"pairs": pairs, "matched": len(pairs), "gold": len(gold), "predicted": len(predicted),
            "manual": len(decided)}


def is_live(c: dict) -> bool:
    return c.get("status") != "deprecated" and not c.get("merged_into")


# ---- relations -------------------------------------------------------------

def _invalidated(c: dict | None) -> bool:
    return bool(c and (c.get("valid_to") or c.get("lifecycle") == "superseded"))


def edge_key(kind: str, a: str, b: str):
    """supports/contradicts are undirected; supersedes is (newer, older)."""
    return (kind, a, b) if kind == "supersedes" else (kind, *sorted((a, b)))


def predicted_edges(item: dict) -> set:
    """The brain's edges in the gold vocabulary. A contradicts edge whose one
    endpoint has been invalidated (valid_to set, or lifecycle superseded) is
    read as the other endpoint superseding it."""
    claims = {c["id"]: c for c in item["claims"] + item.get("neighbour_claims", [])}
    out = set()
    for r in item["relationships"]:
        a, b, kind = r["from_claim_id"], r["to_claim_id"], r["type"]
        if kind not in ("supports", "contradicts"):
            continue
        if kind == "contradicts":
            ia, ib = _invalidated(claims.get(a)), _invalidated(claims.get(b))
            if ia != ib:
                out.add(edge_key("supersedes", b, a) if ia else edge_key("supersedes", a, b))
                continue
        out.add(edge_key(kind, a, b))
    return out


def gold_edges(item: dict, label: dict) -> tuple[set, int]:
    shown = {c["id"] for c in item["claims"] + item.get("neighbour_claims", [])}
    out, invalid = set(), 0
    for e in (label.get("relations") or {}).get("edges", []):
        if e.get("type") not in EDGE_TYPES or e.get("a") not in shown or e.get("b") not in shown or e["a"] == e["b"]:
            invalid += 1
            continue
        out.add(edge_key(e["type"], e["a"], e["b"]))
    return out, invalid


def within(item: dict, edges: set) -> set:
    mine = {c["id"] for c in item["claims"]}
    return {e for e in edges if e[1] in mine and e[2] in mine}


def _pr(tp: int, fp: int, fn: int) -> dict:
    return {"tp": tp, "fp": fp, "fn": fn,
            "precision": tp / (tp + fp) if tp + fp else None,
            "recall": tp / (tp + fn) if tp + fn else None}


def relation_scores(pairs: list[tuple[set, set]]) -> dict:
    out = {}
    for kind in EDGE_TYPES:
        tp = fp = fn = 0
        for pred, gold in pairs:
            p = {e for e in pred if e[0] == kind}
            g = {e for e in gold if e[0] == kind}
            tp, fp, fn = tp + len(p & g), fp + len(p - g), fn + len(g - p)
        out[kind] = _pr(tp, fp, fn)
    tp = fp = fn = 0
    for pred, gold in pairs:  # any relation, type and direction ignored
        p = {frozenset(e[1:]) for e in pred}
        g = {frozenset(e[1:]) for e in gold}
        tp, fp, fn = tp + len(p & g), fp + len(p - g), fn + len(g - p)
    out["any"] = _pr(tp, fp, fn)
    return out


# ---- trust -----------------------------------------------------------------

def calibration(points: list[tuple[float, bool]], bins: int = 10) -> dict:
    """Brier score, Brier skill score against the base rate, ECE and the
    reliability curve, for (stored trust, labelled true) points."""
    n = len(points)
    if n == 0:
        return {"n": 0}
    out_of_range = sum(not 0 <= p <= 1 for p, _ in points)
    pts = [(min(max(p, 0.0), 1.0), bool(y)) for p, y in points]
    brier = sum((p - y) ** 2 for p, y in pts) / n
    base = sum(y for _, y in pts) / n
    ref = base * (1 - base)
    curve, ece = [], 0.0
    for b in range(bins):
        lo, hi = b / bins, (b + 1) / bins
        inb = [(p, y) for p, y in pts if (lo <= p < hi) or (b == bins - 1 and p == 1.0)]
        if not inb:
            curve.append({"bin": [lo, hi], "n": 0, "mean_trust": None, "fraction_true": None})
            continue
        mp = sum(p for p, _ in inb) / len(inb)
        fy = sum(y for _, y in inb) / len(inb)
        ece += len(inb) / n * abs(mp - fy)
        curve.append({"bin": [lo, hi], "n": len(inb), "mean_trust": mp, "fraction_true": fy})
    return {"n": n, "brier": brier, "base_rate": base, "brier_reference": ref,
            "brier_skill": (1 - brier / ref) if ref else None, "ece": ece,
            "trust_out_of_range": out_of_range, "reliability": curve}


# ---- agreement -------------------------------------------------------------

def categorical_kappa(x: list, y: list) -> float | None:
    """Cohen's kappa for any number of categories."""
    n = len(x)
    if n == 0 or n != len(y):
        return None
    cats = set(x) | set(y)
    po = sum(a == b for a, b in zip(x, y)) / n
    pe = sum((x.count(c) / n) * (y.count(c) / n) for c in cats)
    return 1.0 if pe == 1 else (po - pe) / (1 - pe)


def _pair_category(edges: set, pair: frozenset) -> str:
    for e in edges:
        if frozenset(e[1:]) == pair:
            return e[0] if e[0] != "supersedes" else f"supersedes:{e[1]}"
    return "none"


def agreement(items: dict, first: dict, second: dict, threshold: float) -> dict:
    ext_x, ext_y, tru_x, tru_y, bin_x, bin_y, rel_x, rel_y = [], [], [], [], [], [], [], []
    gold_diff, n_items = [], 0
    for iid, la in first.items():
        lb = second.get(iid)
        if lb is None or iid not in items:
            continue
        item = items[iid]
        n_items += 1
        if _done(la, "extraction") and _done(lb, "extraction"):
            ma = match_claims(_gold(la), item["claims"], _links(la), threshold)["pairs"]
            mb = match_claims(_gold(lb), item["claims"], _links(lb), threshold)["pairs"]
            for c in item["claims"]:
                ext_x.append(c["id"] in ma)
                ext_y.append(c["id"] in mb)
            gold_diff.append(abs(len(_gold(la)) - len(_gold(lb))))
        if _done(la, "truth") and _done(lb, "truth"):
            ta, tb = la.get("truth") or {}, lb.get("truth") or {}
            for c in item["claims"]:
                if c["id"] in ta and c["id"] in tb:
                    tru_x.append(ta[c["id"]])
                    tru_y.append(tb[c["id"]])
                    if "unknown" not in (ta[c["id"]], tb[c["id"]]):
                        bin_x.append(ta[c["id"]] == "true")
                        bin_y.append(tb[c["id"]] == "true")
        if _done(la, "relations") and _done(lb, "relations"):
            ga, gb, pred = gold_edges(item, la)[0], gold_edges(item, lb)[0], predicted_edges(item)
            for pair in sorted({frozenset(e[1:]) for e in ga | gb | pred}, key=sorted):
                rel_x.append(_pair_category(ga, pair))
                rel_y.append(_pair_category(gb, pair))

    def k(fn, x, y):
        return {"n": len(x), "kappa": fn(x, y) if x else None,
                "observed_agreement": (sum(a == b for a, b in zip(x, y)) / len(x)) if x else None}

    return {
        "items": n_items,
        "extraction_matched": k(stats.cohen_kappa, ext_x, ext_y),
        "extraction_gold_count_mean_abs_diff": (sum(gold_diff) / len(gold_diff)) if gold_diff else None,
        "truth": k(categorical_kappa, tru_x, tru_y),
        "truth_binary": k(stats.cohen_kappa, bin_x, bin_y),
        "relations": k(categorical_kappa, rel_x, rel_y),
    }


# ---- answering hook ----------------------------------------------------------

def export_questions(items: list[dict], labels: dict) -> list[dict]:
    """Labelled questions in the Phase A Question shape (datasets.Question),
    plus the invalidated answers that count an answer wrong."""
    out = []
    for item in items:
        lab = labels.get(item["item_id"])
        if not lab or not _done(lab, "questions"):
            continue
        for i, q in enumerate(lab.get("questions") or []):
            if not q.get("question"):
                continue
            out.append({
                "id": f"{item['item_id']}#q{i}",
                "text": q["question"],
                "answer": q.get("answer", ""),
                "type": q.get("type", "single-episode"),
                "date": q.get("as_of") or (lab.get("updated_at") or "")[:10],
                "evidence_sessions": q.get("evidence") or [item["episode"]["id"]],
                "abstention": q.get("type") == "abstention",
                "invalidated_answers": q.get("invalidated_answers") or [],
                "split": item["split"],
            })
    return out


def score_answers(questions: list[dict], verdicts: list[dict]) -> dict:
    """Accuracy per system from per-question verdicts produced elsewhere (the
    Phase A answer and judge stages): {question_id, system, correct,
    used_invalidated}. An answer that relied on invalidated memory is wrong
    however plausible it reads (#441, item 6). Systems are compared paired, on
    the questions every system has a verdict for."""
    qtype = {q["id"]: q["type"] for q in questions}
    by_sys: dict[str, dict[str, dict]] = {}
    for v in verdicts:
        if v["question_id"] in qtype:
            by_sys.setdefault(v["system"], {})[v["question_id"]] = v
    common = sorted(set.intersection(*(set(v) for v in by_sys.values()))) if by_sys else []
    out = {"paired_questions": len(common), "systems": {}, "comparisons": {}}
    ok = {s: [bool(vs[q].get("correct")) and not vs[q].get("used_invalidated") for q in common]
          for s, vs in by_sys.items()}
    for s, vs in by_sys.items():
        per_type = {}
        for t in sorted({qtype[q] for q in common}):
            idx = [i for i, q in enumerate(common) if qtype[q] == t]
            per_type[t] = {"n": len(idx), "accuracy": sum(ok[s][i] for i in idx) / len(idx)}
        out["systems"][s] = {
            "accuracy": (sum(ok[s]) / len(common)) if common else None,
            "used_invalidated_rate": (sum(bool(vs[q].get("used_invalidated")) for q in common) / len(common))
            if common else None,
            "by_type": per_type,
        }
    names = sorted(by_sys)
    for a in names:
        for b in names:
            if a < b and common:
                out["comparisons"][f"{a} - {b}"] = stats.paired_bootstrap(ok[a], ok[b])
    return out


# ---- assembling the report -------------------------------------------------

def _done(label: dict, stage: str) -> bool:
    return bool((label.get("done") or {}).get(stage))


def _gold(label: dict) -> list[str]:
    return [g.strip() for g in (label.get("extraction") or {}).get("gold", []) if g.strip()]


def _links(label: dict) -> dict:
    return (label.get("extraction") or {}).get("links") or {}


def bootstrap_ratio(rows: list[tuple[int, int]], resamples: int = RESAMPLES, seed: int = SEED) -> dict:
    """A micro-averaged ratio sum(num)/sum(den) with a 95% CI from resampling items."""
    den = sum(d for _, d in rows)
    if not rows or den == 0:
        return {"value": None, "ci_low": None, "ci_high": None, "items": len(rows)}
    if resamples <= 0:
        return {"value": sum(x for x, _ in rows) / den, "ci_low": None, "ci_high": None, "items": len(rows)}
    rng = random.Random(seed)
    n = len(rows)
    vals = []
    for _ in range(resamples):
        pick = [rows[rng.randrange(n)] for _ in range(n)]
        d = sum(x[1] for x in pick)
        if d:
            vals.append(sum(x[0] for x in pick) / d)
    vals.sort()
    return {"value": sum(x for x, _ in rows) / den, "items": n,
            "ci_low": vals[int(0.025 * len(vals))], "ci_high": vals[int(0.975 * len(vals)) - 1]}


def extraction_scores(items: list[dict], labels: dict, threshold: float, live_only: bool = False,
                      resamples: int = RESAMPLES) -> dict:
    prec, rec, manual = [], [], 0
    for item in items:
        lab = labels.get(item["item_id"])
        if not lab or not _done(lab, "extraction"):
            continue
        claims = [c for c in item["claims"] if is_live(c)] if live_only else item["claims"]
        m = match_claims(_gold(lab), claims, _links(lab), threshold)
        prec.append((m["matched"], m["predicted"]))
        rec.append((m["matched"], m["gold"]))
        manual += m["manual"]
    return {"items": len(prec), "threshold": threshold, "predicted": sum(d for _, d in prec),
            "gold": sum(d for _, d in rec), "matched": sum(x for x, _ in prec), "manual_links": manual,
            "precision": bootstrap_ratio(prec, resamples), "recall": bootstrap_ratio(rec, resamples)}


def report(sample: dict, labels: dict, split: str, threshold: float = MATCH_THRESHOLD,
           second: dict | None = None) -> dict:
    items = [i for i in sample["items"] if split == "all" or i["split"] == split]
    # H3's primary measure (decided before the first run): precision over LIVE
    # claims, those neither deprecated nor merged into another. The figure over
    # every claim linked to the episode is kept as a secondary row.
    ext = extraction_scores(items, labels, threshold, live_only=True)
    ext["measure"] = "live claims (H3 primary)"
    ext["all_linked"] = extraction_scores(items, labels, threshold)
    ext["all_linked"]["measure"] = "all claims linked to the episode (secondary)"
    ext["sensitivity"] = {str(t): {k: extraction_scores(items, labels, t, live_only=True, resamples=0)[k]["value"]
                                   for k in ("precision", "recall")} for t in SENSITIVITY_THRESHOLDS}

    rel_all, rel_in, invalid, rel_items = [], [], 0, 0
    for item in items:
        lab = labels.get(item["item_id"])
        if not lab or not _done(lab, "relations"):
            continue
        rel_items += 1
        gold, bad = gold_edges(item, lab)
        invalid += bad
        pred = predicted_edges(item)
        rel_all.append((pred, gold))
        rel_in.append((within(item, pred), within(item, gold)))

    points, seen, conflicts = [], {}, 0
    for item in items:
        lab = labels.get(item["item_id"])
        if not lab or not _done(lab, "truth"):
            continue
        for c in item["claims"]:
            v = (lab.get("truth") or {}).get(c["id"])
            if v not in ("true", "false"):
                continue
            if c["id"] in seen:
                conflicts += seen[c["id"]] != v
                continue
            seen[c["id"]] = v
            points.append((float(c.get("trust_score") or 0.0), v == "true"))
    trust = calibration(points)
    trust["conflicting_duplicate_labels"] = conflicts

    qs = export_questions(items, labels)
    out = {
        "split": split,
        "items": len(items),
        "labelled_items": sum(1 for i in items if i["item_id"] in labels),
        "extraction": ext,
        "relations": {"items": rel_items, "invalid_gold_edges": invalid,
                      "displayed": relation_scores(rel_all), "within_episode": relation_scores(rel_in)},
        "trust": trust,
        "questions": {"n": len(qs), "by_type": {t: sum(q["type"] == t for q in qs) for t in QUESTION_TYPES}},
        "limitations": LIMITATIONS,
    }
    if second is not None:
        kappa_items = {i["item_id"]: i for i in sample["items"] if i.get("kappa")}
        out["agreement"] = agreement(kappa_items, labels, second, threshold)
    return out


LIMITATIONS = [
    "Extraction matching is lexical (Dice over content tokens, threshold 0.5) plus the labeller's manual links; "
    "a paraphrase the labeller did not link counts as a miss on both sides. See the sensitivity rows.",
    "Relation recall is measured only over the claims an item displays. Neighbour claims are displayed because "
    "the brain linked them, so cross-episode recall is optimistic; within_episode is a closed universe.",
    "Trust calibration uses the stored trust_score, a cache (ADR 0026); its trust_model_version is in sample.json.",
    "A brain sampled with --max-chars excludes its longest episodes; manifest.json records how many.",
]


# ---- files -----------------------------------------------------------------

def load_labels(path: Path) -> dict:
    data = json.loads(path.read_text())
    return data.get("items", {})


def find_labels(d: Path, pass_no: int, labeller: str | None) -> Path | None:
    files = sorted((d / "labels").glob(f"pass{pass_no}-*.json"))
    if labeller:
        files = [f for f in files if f.stem == f"pass{pass_no}-{labeller}"]
    if len(files) > 1:
        raise ValueError(f"several pass-{pass_no} label files; choose one with --labeller: "
                         + ", ".join(f.name for f in files))
    return files[0] if files else None


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    p.add_argument("--dir", required=True, help="sample directory written by sample.py")
    p.add_argument("--split", choices=["dev", "heldout", "all"], default="dev")
    p.add_argument("--labeller", help="pass-1 labeller id, when several labelled pass 1")
    p.add_argument("--second", help="pass-2 labeller id for kappa (default: the only pass-2 file)")
    p.add_argument("--threshold", type=float, default=MATCH_THRESHOLD)
    p.add_argument("--json", help="also write the report here")
    p.add_argument("--export-questions", help="write the labelled questions as JSONL for the answer stage")
    p.add_argument("--verdicts", help="JSONL of {question_id, system, correct, used_invalidated} to score")
    args = p.parse_args(argv)

    d = Path(args.dir).expanduser()
    sample = json.loads((d / "sample.json").read_text())
    first = find_labels(d, 1, args.labeller)
    if first is None:
        print("score: no pass-1 labels in labels/", file=sys.stderr)
        return 2
    labels = load_labels(first)
    sec_path = find_labels(d, 2, args.second)
    second = load_labels(sec_path) if sec_path else None
    if args.split == "heldout":
        print("score: scoring the HELD-OUT split; use it only for final verdicts (#441).", file=sys.stderr)
    out = report(sample, labels, args.split, args.threshold, second)
    out["labels"] = {"pass1": first.name, "pass2": sec_path.name if sec_path else None}
    items = [i for i in sample["items"] if args.split == "all" or i["split"] == args.split]
    if args.export_questions:  # question text is golden-set content: same rules as the sample
        dest = Path(args.export_questions).expanduser()
        try:
            privacy.check_output_dir(dest.parent)
        except privacy.UnsafeOutputDir as e:
            print(f"score: {e}", file=sys.stderr)
            return 2
        qs = export_questions(items, labels)
        privacy.write_private(dest, "".join(json.dumps(q, ensure_ascii=False) + "\n" for q in qs))
    if args.verdicts:
        rows = [json.loads(line) for line in Path(args.verdicts).expanduser().read_text().splitlines() if line.strip()]
        out["answering"] = score_answers(export_questions(items, labels), rows)
    text = json.dumps(out, indent=1)
    if args.json:
        Path(args.json).expanduser().write_text(text + "\n")
    print(text)
    return 0


if __name__ == "__main__":
    sys.exit(main())
