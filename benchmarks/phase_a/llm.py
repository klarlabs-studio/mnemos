"""Chat client for the Phase A evaluation (#441): OpenRouter, Ollama or
headless Claude, chosen per model by its provider.

Everything a result depends on is pinned and recorded:
- the exact model id;
- one upstream provider, with fallbacks off, so a re-run cannot be served by
  a differently quantised deployment;
- temperature 0;
- a fixed seed where the provider honours one.

Responses are cached on disk, keyed on the full request, so a re-run is
reproducible and free. Spend is read from OpenRouter's own usage accounting
and checked against a hard budget before every uncached call.

Standard library only.
"""

from __future__ import annotations

import hashlib
import json
import os
import sqlite3
import threading
import time
import urllib.error
import urllib.request
from dataclasses import dataclass

from benchmarks.phase_a import claude_cli

ENDPOINT = "https://openrouter.ai/api/v1/chat/completions"
OLLAMA_ENDPOINT = os.environ.get("OLLAMA_OPENAI_URL", "http://localhost:11434/v1/chat/completions")

# Providers that are not OpenRouter upstreams.
CLAUDE_CLI = "claude-cli"  # headless Claude Code on the operator's subscription
OLLAMA = "ollama"          # a local model through Ollama's OpenAI-compatible API


@dataclass(frozen=True)
class Model:
    """One model in one role, pinned to one upstream provider.

    provider is an OpenRouter upstream (pinned, fallbacks off), or CLAUDE_CLI,
    or OLLAMA.
    """

    id: str
    provider: str
    max_tokens: int = 256


class BudgetExceeded(RuntimeError):
    pass


class Client:
    def __init__(self, cache_path: str, budget_usd: float, api_key: str | None = None):
        # Checked on first OpenRouter call: a run on headless Claude and Ollama
        # needs no OpenRouter key.
        self.api_key = api_key or os.environ.get("OPENROUTER_API_KEY", "")
        self.budget = budget_usd
        self._lock = threading.Lock()
        self._db = sqlite3.connect(cache_path, check_same_thread=False)
        self._db.execute(
            "CREATE TABLE IF NOT EXISTS responses (key TEXT PRIMARY KEY, body TEXT NOT NULL, cost REAL NOT NULL)"
        )
        self._db.execute("CREATE TABLE IF NOT EXISTS spend (id INTEGER PRIMARY KEY CHECK (id = 1), usd REAL NOT NULL)")
        self._db.execute("INSERT OR IGNORE INTO spend (id, usd) VALUES (1, 0)")
        self._db.commit()

    @property
    def spent(self) -> float:
        with self._lock:
            return self._db.execute("SELECT usd FROM spend WHERE id = 1").fetchone()[0]

    def complete(self, model: Model, messages: list[dict], max_tokens: int | None = None) -> str:
        if model.provider == CLAUDE_CLI:
            # The CLI takes no temperature, seed or token cap; the key covers
            # everything the response can depend on.
            body = {"backend": CLAUDE_CLI, "model": model.id, "messages": messages}
            return self._cached(body, lambda: claude_cli.complete(model.id, messages))
        if model.provider == OLLAMA:
            body = {
                "backend": OLLAMA, "model": model.id, "messages": messages, "temperature": 0,
                "seed": 441, "max_tokens": max_tokens or model.max_tokens,
            }
            def call():
                resp = self._post({k: v for k, v in body.items() if k != "backend"}, OLLAMA_ENDPOINT, auth=False)
                return resp["choices"][0]["message"]["content"] or "", 0.0
            return self._cached(body, call)
        body = {
            "model": model.id,
            "messages": messages,
            "temperature": 0,
            "seed": 441,
            "max_tokens": max_tokens or model.max_tokens,
            "provider": {"order": [model.provider], "allow_fallbacks": False},
            "usage": {"include": True},
        }
        if not self.api_key:
            raise RuntimeError("OPENROUTER_API_KEY is not set")

        def call():
            resp = self._post(body, ENDPOINT, auth=True)
            return resp["choices"][0]["message"]["content"] or "", float((resp.get("usage") or {}).get("cost") or 0.0)

        return self._cached(body, call)

    def _cached(self, body: dict, call) -> str:
        """Return the cached text for body, or run call() and cache its text and cost.

        Entries are stored as {"text": ...}. Entries written before the
        backends were added hold the raw OpenRouter response and still read.
        """
        key = hashlib.sha256(json.dumps(body, sort_keys=True).encode()).hexdigest()
        with self._lock:
            row = self._db.execute("SELECT body FROM responses WHERE key = ?", (key,)).fetchone()
        if row:
            stored = json.loads(row[0])
            if "text" in stored:
                return stored["text"]
            return stored["choices"][0]["message"]["content"] or ""
        if self.spent >= self.budget:
            raise BudgetExceeded(f"spent ${self.spent:.2f} of the ${self.budget:.2f} budget")

        text, cost = call()
        with self._lock:
            self._db.execute(
                "INSERT OR REPLACE INTO responses (key, body, cost) VALUES (?, ?, ?)",
                (key, json.dumps({"text": text}), cost),
            )
            self._db.execute("UPDATE spend SET usd = usd + ? WHERE id = 1", (cost,))
            self._db.commit()
        return text

    def _post(self, body: dict, endpoint: str, auth: bool) -> dict:
        data = json.dumps(body).encode()
        delay = 2.0
        last: Exception | None = None
        for _ in range(6):
            headers = {"Content-Type": "application/json"}
            if auth:
                headers["Authorization"] = f"Bearer {self.api_key}"
            req = urllib.request.Request(endpoint, data=data, headers=headers)
            try:
                with urllib.request.urlopen(req, timeout=300) as r:
                    resp = json.loads(r.read())
                if "choices" not in resp:
                    raise RuntimeError(f"no choices in response: {str(resp)[:300]}")
                return resp
            except urllib.error.HTTPError as e:
                detail = e.read().decode(errors="replace")[:300]
                # 4xx other than rate limiting is a request error: retrying cannot help.
                if e.code < 500 and e.code != 429:
                    raise RuntimeError(f"HTTP {e.code}: {detail}") from e
                last = RuntimeError(f"HTTP {e.code}: {detail}")
            except (urllib.error.URLError, TimeoutError, RuntimeError) as e:
                last = e
            time.sleep(delay)
            delay = min(delay * 2, 60)
        raise RuntimeError(f"giving up after retries: {last}")
