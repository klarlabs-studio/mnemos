"""A local OpenAI-compatible endpoint backed by headless Claude (#441).

tools/memeval extracts beliefs through Mnemos's own `openai-compat` LLM
client, which needs an HTTP endpoint. This serves POST /v1/chat/completions on
127.0.0.1 and answers each request with claude_cli.complete, so extraction
runs on the same isolated headless Claude as the answerer. It binds to the
loopback interface only and is started and stopped by `run.py ingest`.

Standard library only.
"""

from __future__ import annotations

import json
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

from benchmarks.phase_a import claude_cli


class _Handler(BaseHTTPRequestHandler):
    model = ""

    def do_POST(self):  # noqa: N802 (http.server naming)
        if not self.path.rstrip("/").endswith("/chat/completions"):
            self.send_error(404)
            return
        body = json.loads(self.rfile.read(int(self.headers.get("Content-Length") or 0)) or b"{}")
        try:
            text, _ = claude_cli.complete(self.model or body.get("model", ""), body.get("messages", []))
        except Exception as e:  # surfaced to the client as a server error, which it retries
            self.send_error(502, str(e)[:200])
            return
        out = json.dumps({
            "id": "claude-cli", "object": "chat.completion", "created": int(time.time()),
            "model": self.model,
            "choices": [{"index": 0, "finish_reason": "stop", "message": {"role": "assistant", "content": text}}],
        }).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(out)))
        self.end_headers()
        self.wfile.write(out)

    def log_message(self, *args):  # keep the ingest log free of per-request noise
        pass


def serve(model: str) -> tuple[ThreadingHTTPServer, str]:
    """Start the shim on a free loopback port; return the server and its base URL."""
    handler = type("Handler", (_Handler,), {"model": model})
    srv = ThreadingHTTPServer(("127.0.0.1", 0), handler)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    return srv, f"http://127.0.0.1:{srv.server_address[1]}"
