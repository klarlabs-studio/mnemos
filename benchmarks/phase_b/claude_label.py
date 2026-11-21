"""A blind model as the second (pass-2) labeller of the Phase B golden set (#441).

    python3 -m benchmarks.phase_b.claude_label --dir DIR [--model claude-haiku-5-5] [--as-of YYYY-MM-DD]

For every item sample.py marked for the kappa subset, this produces pass-2
labels in exactly the format the labelling page writes
(labels/pass2-<model>.json, via label.Session), so score.py computes Cohen's
kappa against pass 1 unchanged.

Blindness. The model sees what a human pass-2 labeller sees, in the same
order, and nothing else:
1. extraction: the episode text and the header the page shows (date, project,
   session type, age). It writes the gold atomic claims.
2. after "reveal": its own gold claims, the automatic matches, the extracted
   and neighbour claims and the stored edges, from label.Session.view(), which
   hides stored trust, status and validity exactly as the page does. It
   labels the links, relations and truth.
Pass-1 labels are never opened: the only files read are sample.json, the
labelling guide (the system prompt), this labeller's own pass-2 file and its
response cache. Questions are not labelled; they play no part in kappa.

Requests go through benchmarks.phase_a.claude_cli.complete (headless Claude,
isolated from the operator's settings, hooks and MCP servers). Responses must
be strict JSON and are validated; an invalid one is sent back with the error,
at most --retries times. An item whose response never validates is recorded
as failed in its notes with no stage marked done, never filled in. Every
response is cached under labels/cache-<labeller>/ (0600), keyed on the exact
request, so a rerun with the same --as-of is free and reproducible.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import sys
from datetime import date
from pathlib import Path

from benchmarks.phase_a import claude_cli
from benchmarks.phase_b import label, privacy, score

DEFAULT_MODEL = "claude-haiku-5-5"
GUIDE = privacy.REPO_ROOT / "docs" / "eval" / "phase-b-labelling-guide.md"
RELATION_CHOICES = ("supports", "contradicts", "supersedes>", "supersedes<", "none")
MAX_GOLD = 100
MAX_TEXT = 1000

ROLE = """
---

You are the independent second labeller for this golden set. Apply the guide
above exactly. You label one episode at a time, in the same two steps a human
labeller follows. Reply with a single JSON object and nothing else: no prose,
no code fences.
"""


# ---- prompts ---------------------------------------------------------------

def _header(it: dict, as_of: str) -> str:
    ep, st = it["episode"], it["stratum"]
    return (f"Episode recorded {ep.get('timestamp')} · project {st['project']} · "
            f"session type {st['session_type']} · age {st['age']}. Today is {as_of}.")


def step1_messages(system: str, it: dict, as_of: str) -> list[dict]:
    user = (
        "STEP 1 of 2: extraction (guide section 1). You have not seen what Mnemos extracted.\n"
        f"{_header(it, as_of)}\n"
        'Reply with JSON only: {"gold": ["<atomic claim>", ...]}. Use an empty list if the episode '
        "asserts nothing a memory should keep.\n\n"
        f"<<<EPISODE\n{it['episode'].get('content') or ''}\nEPISODE>>>"
    )
    return [{"role": "system", "content": system}, {"role": "user", "content": user}]


def step2_messages(system: str, it: dict, as_of: str, gold: list[str]) -> list[dict]:
    auto = score.match_claims(gold, it["claims"])["pairs"]
    data = {
        "your_gold_claims": [{"index": i, "text": g} for i, g in enumerate(gold)],
        "extracted_claims": [{"id": c["id"], "text": c.get("text", ""), "recorded": (c.get("created_at") or "")[:10],
                              "automatic_match": auto.get(c["id"])} for c in it["claims"]],
        "neighbour_claims": [{"id": c["id"], "text": c.get("text", ""), "recorded": (c.get("created_at") or "")[:10]}
                             for c in it["neighbour_claims"]],
        "stored_edges": [{"id": r["id"], "stored_type": r["type"], "first": r["from_claim_id"],
                          "second": r["to_claim_id"], "within_episode": r["within_episode"]}
                         for r in it["relationships"]],
    }
    user = (
        "STEP 2 of 2: you have written your gold claims; the extracted claims are now revealed.\n"
        f"{_header(it, as_of)}\n"
        "Do three things, following the guide:\n"
        "1. links (section 1, 'When to link by hand'): override an automatic match only where it is wrong. "
        "Map an extracted claim id to the 0-based index of the gold claim it expresses, or to null for "
        "'matches no gold claim'. Omit claims whose automatic match is right.\n"
        "2. relations (section 2): judge EVERY stored edge as one of supports, contradicts, "
        "'supersedes>' (first supersedes second), 'supersedes<' (second supersedes first) or none; then "
        "add edges Mnemos missed among the extracted and neighbour claims (for supersedes, a = the newer, "
        "superseding claim).\n"
        "3. truth (section 3): label EVERY extracted claim true, false or unknown as of today.\n"
        'Reply with JSON only: {"links": {"<claim id>": <index or null>}, "relations": {"judged": '
        '{"<edge id>": "<choice>"}, "added": [{"a": "<claim id>", "b": "<claim id>", "type": '
        '"supports|contradicts|supersedes"}]}, "truth": {"<claim id>": "true|false|unknown"}}\n\n'
        f"<<<EPISODE\n{it['episode'].get('content') or ''}\nEPISODE>>>\n\n"
        f"<<<DATA\n{json.dumps(data, indent=1, ensure_ascii=False)}\nDATA>>>"
    )
    return [{"role": "system", "content": system}, {"role": "user", "content": user}]


# ---- validation ------------------------------------------------------------

def parse_json(text: str):
    """The single JSON object in a response, tolerating surrounding fences."""
    t = text.strip()
    t = re.sub(r"^```(?:json)?\s*|\s*```$", "", t)
    start, end = t.find("{"), t.rfind("}")
    if start < 0 or end < start:
        raise ValueError("no JSON object in the response")
    return json.loads(t[start:end + 1])


def validate_step1(obj) -> list[str]:
    if not isinstance(obj, dict) or set(obj) != {"gold"} or not isinstance(obj["gold"], list):
        raise ValueError('expected exactly {"gold": [...]}')
    gold = obj["gold"]
    if len(gold) > MAX_GOLD:
        raise ValueError(f"at most {MAX_GOLD} gold claims")
    if not all(isinstance(g, str) and g.strip() and len(g) <= MAX_TEXT and "\n" not in g for g in gold):
        raise ValueError(f"every gold claim must be a non-empty single-line string of at most {MAX_TEXT} characters")
    return [g.strip() for g in gold]


def validate_step2(obj, it: dict, n_gold: int) -> dict:
    if not isinstance(obj, dict) or set(obj) - {"links", "relations", "truth"} or "relations" not in obj \
            or "truth" not in obj:
        raise ValueError('expected {"links": {...}, "relations": {...}, "truth": {...}}')
    claims = {c["id"] for c in it["claims"]}
    shown = claims | {c["id"] for c in it["neighbour_claims"]}
    links = obj.get("links") or {}
    if not isinstance(links, dict):
        raise ValueError("links must be an object")
    for cid, g in links.items():
        if cid not in claims:
            raise ValueError(f"links: {cid!r} is not an extracted claim id")
        if g is not None and not (isinstance(g, int) and not isinstance(g, bool) and 0 <= g < n_gold):
            raise ValueError(f"links: {cid!r} must map to a gold index 0..{n_gold - 1} or null")
    rel = obj["relations"]
    if not isinstance(rel, dict) or set(rel) - {"judged", "added"}:
        raise ValueError('relations must be {"judged": {...}, "added": [...]}')
    judged, added = rel.get("judged") or {}, rel.get("added") or []
    edges = {r["id"] for r in it["relationships"]}
    if not isinstance(judged, dict) or set(judged) != edges:
        missing, extra = sorted(edges - set(judged)), sorted(set(judged) - edges)
        raise ValueError(f"relations.judged must judge every stored edge exactly (missing {missing}, unknown {extra})")
    bad = {k: v for k, v in judged.items() if v not in RELATION_CHOICES}
    if bad:
        raise ValueError(f"relations.judged values must be one of {RELATION_CHOICES}: {bad}")
    if not isinstance(added, list):
        raise ValueError("relations.added must be a list")
    for e in added:
        if not isinstance(e, dict) or set(e) != {"a", "b", "type"} or e["type"] not in score.EDGE_TYPES \
                or e["a"] not in shown or e["b"] not in shown or e["a"] == e["b"]:
            raise ValueError(f"relations.added: {e!r} must be {{a, b, type}} over two different displayed claims")
    truth = obj["truth"]
    if not isinstance(truth, dict) or set(truth) != claims:
        raise ValueError("truth must label every extracted claim exactly once")
    if any(v not in score.TRUTH_VALUES for v in truth.values()):
        raise ValueError(f"truth values must be one of {score.TRUTH_VALUES}")
    return {"links": links, "judged": judged, "added": added, "truth": truth}


def derive_edges(it: dict, judged: dict, added: list[dict]) -> list[dict]:
    """The gold edge list, exactly as the page's deriveEdges builds it."""
    out = []
    for r in it["relationships"]:
        v = judged.get(r["id"])
        if not v or v == "none":
            continue
        a, b = r["from_claim_id"], r["to_claim_id"]
        if v == "supersedes>":
            out.append({"a": a, "b": b, "type": "supersedes", "source": r["id"]})
        elif v == "supersedes<":
            out.append({"a": b, "b": a, "type": "supersedes", "source": r["id"]})
        else:
            out.append({"a": a, "b": b, "type": v, "source": r["id"]})
    out += [{"a": e["a"], "b": e["b"], "type": e["type"], "source": "added"} for e in added]
    return out


# ---- calling the model -----------------------------------------------------

class Labeller:
    def __init__(self, session: label.Session, model: str, system: str, retries: int, as_of: str):
        self.session, self.model, self.system, self.retries, self.as_of = session, model, system, retries, as_of
        self.cache = session.dir / "labels" / f"cache-{session.labeller}"
        privacy.make_private_dir(self.cache)
        self.calls = self.cache_hits = 0
        self.cost = 0.0

    def _complete(self, messages: list[dict]) -> str:
        key = hashlib.sha256(json.dumps({"model": self.model, "messages": messages},
                                        sort_keys=True, ensure_ascii=False).encode()).hexdigest()
        path = self.cache / f"{key}.json"
        if path.exists():
            self.cache_hits += 1
            return json.loads(path.read_text())["response"]
        text, cost = claude_cli.complete(self.model, messages)
        self.calls += 1
        self.cost += cost
        privacy.write_json(path, {"model": self.model, "response": text, "cost_usd": cost})
        return text

    def ask(self, messages: list[dict], check):
        """A validated answer, sending each invalid response back with its error."""
        last = None
        for _ in range(1 + self.retries):
            raw = self._complete(messages)
            try:
                return check(parse_json(raw))
            except (ValueError, json.JSONDecodeError) as e:
                last = str(e)
                messages = messages + [
                    {"role": "assistant", "content": raw},
                    {"role": "user", "content": f"That response was invalid: {last}. "
                                                "Reply again with the corrected JSON object only."}]
        raise ValueError(f"no valid response after {1 + self.retries} attempts: {last}")

    def label_item(self, it: dict) -> dict:
        note = f"claude_label {self.model}, as of {self.as_of}"
        try:
            gold = self.ask(step1_messages(self.system, it, self.as_of), validate_step1)
        except (ValueError, RuntimeError) as e:
            return {"done": {}, "notes": f"{note}: extraction FAILED, not labelled: {e}"}
        lab = {"done": {"extraction": True},
               "extraction": {"gold": gold, "links": {}, "revealed": True}, "notes": note}
        try:
            out = self.ask(step2_messages(self.system, it, self.as_of, gold),
                           lambda o: validate_step2(o, it, len(gold)))
        except (ValueError, RuntimeError) as e:
            lab["notes"] = f"{note}: relations and truth FAILED, not labelled: {e}"
            return lab
        lab["extraction"]["links"] = out["links"]
        lab["relations"] = {"judged": out["judged"], "added": out["added"],
                            "edges": derive_edges(it, out["judged"], out["added"])}
        lab["truth"] = out["truth"]
        lab["done"].update({"relations": True, "truth": True})
        return lab


def complete_label(lab: dict | None) -> bool:
    done = (lab or {}).get("done") or {}
    return all(done.get(s) for s in ("extraction", "relations", "truth"))


def run(d: Path, model: str = DEFAULT_MODEL, guide: Path = GUIDE, retries: int = 2,
        as_of: str | None = None, labeller: str | None = None, limit: int | None = None) -> dict:
    labeller = labeller or re.sub(r"[^A-Za-z0-9_-]", "-", model)[:32]
    session = label.Session(privacy.check_output_dir(d), labeller, 2)
    system = guide.read_text() + ROLE
    as_of = as_of or date.today().isoformat()
    lab = Labeller(session, model, system, retries, as_of)
    view = session.view()
    done = failed = skipped = 0
    for it in view["items"][:limit]:
        if complete_label(view["labels"].get(it["item_id"])):
            skipped += 1
            continue
        result = lab.label_item(it)
        session.save(it["item_id"], result)
        if complete_label(result):
            done += 1
        else:
            failed += 1
    return {"labels": str(session.path), "labelled": done, "failed": failed, "already_done": skipped,
            "calls": lab.calls, "cache_hits": lab.cache_hits, "cost_usd": round(lab.cost, 4), "as_of": as_of}


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    p.add_argument("--dir", required=True, help="sample directory written by sample.py")
    p.add_argument("--model", default=DEFAULT_MODEL)
    p.add_argument("--labeller", help="labeller id (default: the model name)")
    p.add_argument("--guide", default=str(GUIDE), help="labelling guide used as the system prompt")
    p.add_argument("--retries", type=int, default=2, help="re-asks after an invalid response, per step")
    p.add_argument("--as-of", help="the 'today' of the truth labels (default: today); fix it to rerun from cache")
    p.add_argument("--limit", type=int, help="label at most this many kappa items (for a smoke run)")
    args = p.parse_args(argv)
    try:
        out = run(Path(args.dir).expanduser(), args.model, Path(args.guide), args.retries, args.as_of,
                  args.labeller, args.limit)
    except (ValueError, FileNotFoundError, privacy.UnsafeOutputDir) as e:
        print(f"claude_label: {e}", file=sys.stderr)
        return 2
    print(json.dumps(out, indent=1))
    return 1 if out["failed"] else 0


if __name__ == "__main__":
    sys.exit(main())
