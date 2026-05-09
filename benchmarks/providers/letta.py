"""Letta (formerly MemGPT) provider adapter.

Talks to a self-hosted Letta server's REST API:
- POST /v1/agents — create an agent (the Letta unit of memory)
- POST /v1/agents/{id}/messages — send a message that goes through
  the agent's working + archival memory
- GET  /v1/agents/{id}/archival-memory — retrieve archival entries
- POST /v1/agents/{id}/archival-memory — write an archival entry
- DELETE /v1/agents/{id} — wipe (used for reset)

Auth: LETTA_TOKEN env header when set; the local OSS server runs
unauthenticated by default.

Letta's contradiction story: archival memory stores text passages with
embeddings. There is no relationship layer; contradicting passages
co-exist in the archive and both surface on retrieval. The suite
scores zero contradictions detected — architectural, not adapter
limitation.

Mnemos's wedge against Letta is the same as against mem0/Zep:
typed claims, contradiction edges, and per-claim evidence as
first-class outputs.

Connection: set LETTA_URL (default http://localhost:8283) and
optionally LETTA_TOKEN. The adapter raises a clear error if the
server is not reachable rather than silently passing.
"""

from __future__ import annotations

import os
import uuid
from typing import Any

import httpx

from . import QueryResult


class LettaProvider:
    name = "letta"

    def __init__(self, base_url: str | None = None, token: str | None = None):
        self.base_url = (
            base_url or os.environ.get("LETTA_URL", "http://localhost:8283")
        ).rstrip("/")
        self.token = token or os.environ.get("LETTA_TOKEN", "")
        self.agent_id: str | None = None
        self.agent_name = f"bench-{uuid.uuid4().hex[:8]}"

    def _headers(self) -> dict[str, str]:
        h = {"Content-Type": "application/json"}
        if self.token:
            h["Authorization"] = f"Bearer {self.token}"
        return h

    def _check_alive(self) -> None:
        try:
            r = httpx.get(f"{self.base_url}/v1/health", timeout=5)
            if r.status_code >= 500:
                raise RuntimeError(f"Letta health returned {r.status_code}")
        except httpx.HTTPError as exc:
            raise RuntimeError(
                f"Letta not reachable at {self.base_url}: {exc}. "
                "Start the OSS server (docker-compose up letta) or set LETTA_URL."
            ) from exc

    def _ensure_agent(self) -> str:
        if self.agent_id:
            return self.agent_id
        self._check_alive()
        body = {
            "name": self.agent_name,
            # Memory-only mode: no LLM tools, no chat completion. The
            # benchmark exercises retrieval, not generation.
            "memory_blocks": [
                {"label": "human", "value": "benchmark user"},
                {"label": "persona", "value": "memory-only retrieval agent"},
            ],
        }
        r = httpx.post(
            f"{self.base_url}/v1/agents",
            json=body,
            headers=self._headers(),
            timeout=60,
        )
        r.raise_for_status()
        data = r.json()
        self.agent_id = data.get("id", "")
        if not self.agent_id:
            raise RuntimeError(f"Letta create-agent returned no id: {data}")
        return self.agent_id

    def reset(self) -> None:
        if self.agent_id:
            try:
                httpx.delete(
                    f"{self.base_url}/v1/agents/{self.agent_id}",
                    headers=self._headers(),
                    timeout=30,
                )
            except httpx.HTTPError:
                pass
        self.agent_id = None
        self.agent_name = f"bench-{uuid.uuid4().hex[:8]}"

    def add(self, content: str, metadata: dict[str, Any] | None = None) -> str:
        agent = self._ensure_agent()
        body = {"text": content, "metadata": metadata or {}}
        r = httpx.post(
            f"{self.base_url}/v1/agents/{agent}/archival-memory",
            json=body,
            headers=self._headers(),
            timeout=60,
        )
        r.raise_for_status()
        data = r.json()
        # Letta's archival-memory write returns the created passage(s).
        if isinstance(data, list) and data:
            return data[0].get("id", "")
        if isinstance(data, dict):
            return data.get("id", "")
        return ""

    def query(self, question: str) -> QueryResult:
        agent = self._ensure_agent()
        # Letta supports archival-memory retrieval via a search query
        # parameter on the GET endpoint; result count caps at top_k.
        r = httpx.get(
            f"{self.base_url}/v1/agents/{agent}/archival-memory",
            params={"search": question, "limit": 50},
            headers=self._headers(),
            timeout=60,
        )
        r.raise_for_status()
        results = r.json() or []
        memories = [
            {
                "id": p.get("id", ""),
                "content": p.get("text") or p.get("content", ""),
                "metadata": p.get("metadata", {}),
                "score": p.get("score", 0.0),
            }
            for p in results
        ]
        return QueryResult(
            answer="",
            memories=memories,
            contradictions=[],
            evidence_ids=[m["id"] for m in memories if m.get("id")],
        )
