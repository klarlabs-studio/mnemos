"""Load LongMemEval_S and LoCoMo into one schema (#441 Phase A).

A Haystack is one conversation history, a list of dated sessions, plus the
questions asked of it:
- LongMemEval gives every question its own haystack;
- LoCoMo asks about 150 questions of each of its 10 conversations.

Sources, verified by sha256 in load():
- LongMemEval_S (cleaned), Wu et al., ICLR 2025:
  huggingface.co/datasets/xiaowu0162/longmemeval-cleaned
- LoCoMo-10, Maharana et al., ACL 2024: github.com/snap-research/locomo
"""

from __future__ import annotations

import hashlib
import json
from dataclasses import dataclass, field
from pathlib import Path

SHA256 = {
    "longmemeval_s_cleaned.json": "d6f21ea9d60a0d56f34a05b609c79c88a451d2ae03597821ea3d5a9678c3a442",
    "locomo10.json": "79fa87e90f04081343b8c8debecb80a9a6842b76a7aa537dc9fdf651ea698ff4",
}

# LoCoMo categories, as numbered in the dataset. Category 5 (adversarial: the
# question has no answer in the conversation) is excluded, as in Mem0 and Zep's
# published LoCoMo runs; abstention is measured on LongMemEval's _abs questions.
LOCOMO_CATEGORIES = {1: "multi-hop", 2: "temporal", 3: "open-domain", 4: "single-hop"}


@dataclass
class Session:
    id: str
    date: str
    text: str


@dataclass
class Question:
    id: str
    text: str
    answer: str
    type: str
    date: str
    evidence_sessions: list[str] = field(default_factory=list)
    abstention: bool = False


@dataclass
class Haystack:
    id: str
    benchmark: str
    sessions: list[Session]
    questions: list[Question]


def _verify(path: Path) -> None:
    want = SHA256[path.name]
    got = hashlib.sha256(path.read_bytes()).hexdigest()
    if got != want:
        raise RuntimeError(f"{path.name}: sha256 {got} != pinned {want}")


def load_longmemeval(path: Path) -> list[Haystack]:
    _verify(path)
    out = []
    for q in json.loads(path.read_text()):
        sessions = []
        for sid, date, turns in zip(q["haystack_session_ids"], q["haystack_dates"], q["haystack_sessions"]):
            text = "\n".join(f"{t['role']}: {t['content']}" for t in turns)
            sessions.append(Session(id=sid, date=date, text=text))
        out.append(
            Haystack(
                id=q["question_id"],
                benchmark="longmemeval",
                sessions=sessions,
                questions=[
                    Question(
                        id=q["question_id"],
                        text=q["question"],
                        answer=str(q["answer"]),
                        type=q["question_type"],
                        date=q["question_date"],
                        evidence_sessions=list(q.get("answer_session_ids") or []),
                        abstention=q["question_id"].endswith("_abs"),
                    )
                ],
            )
        )
    return out


def load_locomo(path: Path) -> list[Haystack]:
    _verify(path)
    out = []
    for conv in json.loads(path.read_text()):
        c = conv["conversation"]
        n = 1
        sessions = []
        last_date = ""
        while f"session_{n}" in c:
            turns = []
            for t in c[f"session_{n}"]:
                line = f"{t['speaker']}: {t['text']}"
                if t.get("blip_caption"):
                    line += f" [shares a photo: {t['blip_caption']}]"
                turns.append(line)
            last_date = c.get(f"session_{n}_date_time", "")
            sessions.append(Session(id=f"session_{n}", date=last_date, text="\n".join(turns)))
            n += 1
        questions = []
        for i, qa in enumerate(conv["qa"]):
            cat = qa.get("category")
            if cat not in LOCOMO_CATEGORIES:
                continue
            # Evidence ids look like "D3:12": dialogue in session 3.
            ev = sorted({f"session_{e.split(':')[0][1:]}" for e in qa.get("evidence", []) if e.startswith("D")})
            questions.append(
                Question(
                    id=f"{conv['sample_id']}-q{i}",
                    text=qa["question"],
                    answer=str(qa.get("answer", "")),
                    type=LOCOMO_CATEGORIES[cat],
                    # LoCoMo gives no question date; questions are asked after the last session.
                    date=last_date,
                    evidence_sessions=ev,
                )
            )
        out.append(Haystack(id=conv["sample_id"], benchmark="locomo", sessions=sessions, questions=questions))
    return out


def load(data_dir: Path) -> dict[str, list[Haystack]]:
    return {
        "longmemeval": load_longmemeval(data_dir / "longmemeval_s_cleaned.json"),
        "locomo": load_locomo(data_dir / "locomo10.json"),
    }
