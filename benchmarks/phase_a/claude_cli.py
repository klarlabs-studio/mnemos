"""Headless Claude Code as a model backend for the Phase A evaluation (#441).

`claude -p` runs one completion on the operator's Claude subscription. Every
call is isolated from the operator's environment, because a plain `claude -p`
loads their settings, CLAUDE.md files, MCP servers and hooks, and the Mnemos
SessionEnd capture hook would write evaluation text into their real brain:

- `--setting-sources ""` and `--settings {"disableAllHooks": true}`: no user,
  project or local settings, and no hooks at all;
- `--strict-mcp-config` with an empty MCP config: no MCP servers;
- `--tools ""`: the model cannot call tools, so a completion is one turn;
- `--no-session-persistence`: nothing is written to the session history;
- the subprocess runs in an empty working directory, so no project
  instructions are discovered, with MNEMOS_DB_URL pinned to memory:// as a
  second guard should anything ever open a store.

The CLI has no temperature, seed or max-tokens control; that difference from
the OpenRouter backend is recorded as a pre-registered deviation on #441.
Responses are cached by the caller (llm.Client), so re-runs are reproducible.

Standard library only.
"""

from __future__ import annotations

import json
import os
import subprocess
import tempfile
import time
from pathlib import Path

_WORKDIR = Path(tempfile.gettempdir()) / "mnemos-eval-claude-cli"
_MCP_CONFIG = _WORKDIR / "mcp.json"


def _prepare() -> None:
    _WORKDIR.mkdir(parents=True, exist_ok=True)
    if not _MCP_CONFIG.exists():
        _MCP_CONFIG.write_text('{"mcpServers": {}}')


def render(messages: list[dict]) -> tuple[str, str]:
    """Split chat messages into a system prompt and one user prompt.

    System messages are joined into the system prompt. A single user message is
    passed as is; a longer exchange is rendered as a labelled transcript, since
    `claude -p` takes one prompt.
    """
    system = "\n\n".join(m["content"] for m in messages if m["role"] == "system")
    rest = [m for m in messages if m["role"] != "system"]
    if len(rest) == 1 and rest[0]["role"] == "user":
        return system, rest[0]["content"]
    turns = [f"{'User' if m['role'] == 'user' else 'Assistant'}: {m['content']}" for m in rest]
    return system, "\n\n".join(turns)


def command(model: str, system: str) -> list[str]:
    """The exact argv of one isolated headless completion."""
    return [
        "claude", "-p",
        "--model", model,
        "--setting-sources", "",
        "--settings", json.dumps({"disableAllHooks": True}),
        "--strict-mcp-config", "--mcp-config", str(_MCP_CONFIG),
        "--tools", "",
        "--no-session-persistence",
        "--system-prompt", system or "You are a helpful assistant.",
        "--output-format", "json",
    ]


def complete(model: str, messages: list[dict], timeout: int = 300) -> tuple[str, float]:
    """Run one completion; return its text and the CLI's reported notional cost.

    Retries transient failures (rate limits, timeouts) with backoff. A response
    the CLI marks as an error is raised, never returned as an answer.
    """
    _prepare()
    system, prompt = render(messages)
    env = {**os.environ, "MNEMOS_DB_URL": "memory://"}
    delay, last = 5.0, None
    for _ in range(6):
        try:
            out = subprocess.run(
                command(model, system), input=prompt, capture_output=True, text=True,
                cwd=_WORKDIR, env=env, timeout=timeout,
            )
            resp = json.loads(out.stdout) if out.stdout.strip() else {}
            if resp and not resp.get("is_error"):
                return resp.get("result") or "", float(resp.get("total_cost_usd") or 0.0)
            last = RuntimeError(f"claude -p failed (exit {out.returncode}): "
                                f"{(resp.get('result') or out.stderr or out.stdout)[:300]}")
        except (subprocess.TimeoutExpired, json.JSONDecodeError) as e:
            last = e
        time.sleep(delay)
        delay = min(delay * 2, 120)
    raise RuntimeError(f"giving up after retries: {last}")
