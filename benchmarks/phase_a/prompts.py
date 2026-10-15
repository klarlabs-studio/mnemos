"""Answer and judge prompts (#441 Phase A).

One answer prompt serves every system, so only the context differs. The judge
criteria per question type follow the intent of LongMemEval's published grading
(Wu et al., ICLR 2025), in our own wording.
"""

from __future__ import annotations

from .datasets import Question

ANSWER_SYSTEM = (
    "You answer questions about a user using excerpts from their past conversations. "
    "Use only the excerpts. Dates in brackets say when each excerpt was recorded."
)


def answer_messages(context: str, q: Question) -> list[dict]:
    user = (
        f"Memory excerpts:\n{context or '(none)'}\n\n"
        f"Current date: {q.date or 'unknown'}\n"
        f"Question: {q.text}\n\n"
        "Answer concisely. If the excerpts do not contain the answer, reply exactly: I don't know."
    )
    return [{"role": "system", "content": ANSWER_SYSTEM}, {"role": "user", "content": user}]


_CRITERIA = {
    "temporal-reasoning": (
        "Accept an answer that is off by one in a count of days, weeks or months. "
        "Otherwise the answer must match the reference."
    ),
    "knowledge-update": (
        "The reference is the most recent value. Accept an answer that gives it, even if it also "
        "mentions an older value as superseded. An answer giving only an outdated value is wrong."
    ),
    "single-session-preference": (
        "The reference describes what a good personalised answer takes into account. Accept an answer "
        "that uses the user's relevant personal information as described. It need not match word for word."
    ),
    "temporal": "Dates may be in any format. Accept relative phrasing that resolves to the same date or period.",
}

_DEFAULT = (
    "Accept the answer if it contains the reference answer or something equivalent to it. "
    "If it gives only part of the required information, it is wrong."
)

_ABSTENTION = (
    "The question cannot be answered from the conversations. The answer is correct only if it says the "
    "information is not available or unknown. An answer that states a specific fact is wrong."
)


def judge_messages(q: Question, answer: str) -> list[dict]:
    criteria = _ABSTENTION if q.abstention else _CRITERIA.get(q.type, _DEFAULT)
    user = (
        f"Question: {q.text}\n"
        f"Reference answer: {q.answer}\n"
        f"Model answer: {answer}\n\n"
        f"Grading rule: {criteria}\n\n"
        "Reply with exactly one word: CORRECT or WRONG."
    )
    return [
        {"role": "system", "content": "You grade answers strictly against a reference answer and a grading rule."},
        {"role": "user", "content": user},
    ]


def parse_verdict(text: str) -> bool | None:
    t = (text or "").strip().upper()
    if t.startswith("CORRECT"):
        return True
    if t.startswith("WRONG"):
        return False
    if "CORRECT" in t and "WRONG" not in t:
        return True
    if "WRONG" in t and "CORRECT" not in t:
        return False
    return None
