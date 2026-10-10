"""The three systems under test (#441 Phase A).

Each system turns (haystack, question) into the context the answerer sees,
and reports which sessions that context drew on, for retrieval recall:

- full-context: every session, in order. Run only where the history fits a
  context window (the benchmarks here).
- bm25: raw conversation chunks ranked by BM25 against the question. This is
  what an agent with files and search does, and H1's baseline.
- mnemos: the beliefs Mnemos recalls (tools/memeval), each with the dates of
  the sessions its evidence came from.

The two retrieval systems get the same context budget, so neither wins by
handing the answerer more text.
"""

from __future__ import annotations

import json
import math
import re
from collections import Counter
from dataclasses import dataclass
from pathlib import Path

from .datasets import Haystack, Question

CONTEXT_BUDGET_TOKENS = 4000


def tokens(text: str) -> int:
    """Rough token count (4 characters per token), used only for budgeting."""
    return max(1, len(text) // 4)


@dataclass
class Context:
    text: str
    sessions: list[str]
    tokens: int


def full_context(h: Haystack, q: Question) -> Context:
    parts = [f"### Session {s.id} ({s.date})\n{s.text}" for s in h.sessions]
    text = "\n\n".join(parts)
    return Context(text=text, sessions=[s.id for s in h.sessions], tokens=tokens(text))


# --- BM25 -----------------------------------------------------------------

_WORD = re.compile(r"[a-z0-9]+")


def _terms(text: str) -> list[str]:
    return _WORD.findall(text.lower())


@dataclass
class _Chunk:
    session: str
    date: str
    text: str
    terms: Counter
    length: int


def _chunks(h: Haystack, turns_per_chunk: int = 2) -> list[_Chunk]:
    """Split each session into consecutive pairs of turns (one exchange)."""
    out = []
    for s in h.sessions:
        lines = [l for l in s.text.split("\n") if l.strip()]
        for i in range(0, len(lines), turns_per_chunk):
            text = "\n".join(lines[i : i + turns_per_chunk])
            t = _terms(text)
            out.append(_Chunk(session=s.id, date=s.date, text=text, terms=Counter(t), length=len(t)))
    return out


class BM25Index:
    """Okapi BM25 (k1=1.2, b=0.75) over one haystack's chunks."""

    def __init__(self, h: Haystack, k1: float = 1.2, b: float = 0.75):
        self.chunks = _chunks(h)
        self.k1, self.b = k1, b
        n = len(self.chunks)
        self.avgdl = sum(c.length for c in self.chunks) / max(1, n)
        df: Counter = Counter()
        for c in self.chunks:
            df.update(c.terms.keys())
        self.idf = {t: math.log(1 + (n - d + 0.5) / (d + 0.5)) for t, d in df.items()}

    def rank(self, query: str) -> list[_Chunk]:
        q = set(_terms(query))
        scored = []
        for i, c in enumerate(self.chunks):
            s = 0.0
            for t in q:
                f = c.terms.get(t, 0)
                if f:
                    s += self.idf[t] * f * (self.k1 + 1) / (f + self.k1 * (1 - self.b + self.b * c.length / self.avgdl))
            if s > 0:
                scored.append((s, -i, c))
        scored.sort(reverse=True)
        return [c for _, _, c in scored]


def bm25(index: BM25Index, q: Question, budget: int = CONTEXT_BUDGET_TOKENS) -> Context:
    picked, used = [], 0
    for c in index.rank(q.text):
        t = tokens(c.text)
        if used + t > budget:
            continue
        picked.append(c)
        used += t
    parts = [f"[{c.date}] (session {c.session})\n{c.text}" for c in picked]
    return Context(text="\n\n".join(parts), sessions=sorted({c.session for c in picked}), tokens=used)


# --- Mnemos ---------------------------------------------------------------


def load_mnemos(path: Path) -> dict[str, dict]:
    """Read tools/memeval output into {question_id: answer} plus per-haystack
    ingest stats. Haystacks that failed are reported, never silently dropped."""
    answers, failed = {}, []
    for line in path.read_text().splitlines():
        if not line.strip():
            continue
        o = json.loads(line)
        if o.get("error"):
            failed.append((o["haystack_id"], o["error"]))
            continue
        for a in o.get("answers") or []:
            answers[a["question_id"]] = {**a, "haystack": o["haystack_id"], "claims": o["claims"], "relate_skips": o["relate_skips"]}
    return {"answers": answers, "failed": failed}


def mnemos(recall: dict, budget: int = CONTEXT_BUDGET_TOKENS) -> Context:
    picked, sessions, used = [], set(), 0
    for r in recall.get("recalled") or []:
        dates = ", ".join(d for d in r.get("dates") or [] if d) or "date unknown"
        line = f"- [{dates}] {r['text']}"
        t = tokens(line)
        if used + t > budget:
            continue
        picked.append(line)
        sessions.update(r.get("session_ids") or [])
        used += t
    return Context(text="\n".join(picked), sessions=sorted(sessions), tokens=used)
