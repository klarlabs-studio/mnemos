"""Zep (open-source server) provider adapter.

Talks to a self-hosted Zep server's HTTP API:
- POST /api/v2/users/{user_id}/sessions/{session_id}/memory — store
- GET  /api/v2/users/{user_id}/sessions/{session_id}/memory — retrieve
- POST /api/v2/users/{user_id}/sessions/{session_id}/memory/search — semantic search
- DELETE /api/v2/users/{user_id} — wipe (used for reset)

Auth: Zep cloud uses API key in Authorization header. The OSS server
defaults to no auth in local benchmark mode; ZEP_API_KEY is honoured
when set so the same adapter can run against either deployment.

Zep's contradiction-detection story: Zep extracts "facts" from the
session memory and presents them as a knowledge graph, but it doesn't
emit contradiction edges. Two contradicting facts will both surface
on retrieval; the suite scores that as zero contradictions detected.

That's the wedge: Mnemos's relationships table makes contradictions
queryable via supports/contradicts/causes/etc.; Zep's fact graph stops
at extraction.

Connection: set ZEP_URL (default http://localhost:8000) and optionally
ZEP_API_KEY. The adapter raises a clear error if the server is not
reachable rather than silently passing.
"""

from __future__ import annotations

import os
import uuid
from typing import Any

import httpx

from . import QueryResult


class ZepProvider:
    name = "zep"

    def __init__(self, base_url: str | None = None, api_key: str | None = None):
        self.base_url = (
            base_url or os.environ.get("ZEP_URL", "http://localhost:8000")
        ).rstrip("/")
        self.api_key = api_key or os.environ.get("ZEP_API_KEY", "")
        self.user_id = f"bench-{uuid.uuid4().hex[:8]}"
        self.session_id = f"sess-{uuid.uuid4().hex[:8]}"

    def _headers(self) -> dict[str, str]:
        h = {"Content-Type": "application/json"}
        if self.api_key:
            h["Authorization"] = f"Api-Key {self.api_key}"
        return h

    def _check_alive(self) -> None:
        try:
            r = httpx.get(f"{self.base_url}/healthz", timeout=5)
            if r.status_code >= 500:
                raise RuntimeError(f"Zep healthz returned {r.status_code}")
        except httpx.HTTPError as exc:
            raise RuntimeError(
                f"Zep not reachable at {self.base_url}: {exc}. "
                "Start the OSS server (docker-compose up zep) or set ZEP_URL."
            ) from exc

    def reset(self) -> None:
        try:
            httpx.delete(
                f"{self.base_url}/api/v2/users/{self.user_id}",
                headers=self._headers(),
                timeout=30,
            )
        except httpx.HTTPError:
            pass
        self.user_id = f"bench-{uuid.uuid4().hex[:8]}"
        self.session_id = f"sess-{uuid.uuid4().hex[:8]}"

    def add(self, content: str, metadata: dict[str, Any] | None = None) -> str:
        self._check_alive()
        body = {
            "messages": [
                {
                    "role": "user",
                    "role_type": "user",
                    "content": content,
                    "metadata": metadata or {},
                }
            ],
        }
        r = httpx.post(
            f"{self.base_url}/api/v2/users/{self.user_id}/sessions/{self.session_id}/memory",
            json=body,
            headers=self._headers(),
            timeout=60,
        )
        r.raise_for_status()
        data = r.json() if r.text else {}
        if isinstance(data, dict):
            messages = data.get("messages") or []
            if messages:
                return messages[0].get("uuid", "")
        return ""

    def query(self, question: str) -> QueryResult:
        body = {"text": question, "search_scope": "messages", "limit": 50}
        r = httpx.post(
            f"{self.base_url}/api/v2/users/{self.user_id}/sessions/{self.session_id}/memory/search",
            json=body,
            headers=self._headers(),
            timeout=60,
        )
        r.raise_for_status()
        results = r.json() or []
        if isinstance(results, dict):
            results = results.get("results", [])
        memories = []
        for hit in results:
            msg = hit.get("message") or {}
            memories.append(
                {
                    "id": msg.get("uuid", ""),
                    "content": msg.get("content", ""),
                    "metadata": msg.get("metadata", {}),
                    "score": hit.get("score", 0.0),
                }
            )
        # Zep does not surface contradiction edges; the fact graph
        # stops at extraction. The suite scores this as zero
        # contradictions detected — architectural limitation, not bug.
        return QueryResult(
            answer="",
            memories=memories,
            contradictions=[],
            evidence_ids=[m["id"] for m in memories if m.get("id")],
        )
