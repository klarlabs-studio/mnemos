"""Local labelling server for the Phase B golden set (#441).

    python3 -m benchmarks.phase_b.label --dir DIR --labeller felix --pass 1
    python3 -m benchmarks.phase_b.label --dir DIR --labeller anna  --pass 2

Serves label.html on 127.0.0.1 only and writes every change straight to
DIR/labels/pass<N>-<labeller>.json (atomic, owner-only). Pass 1 labels every
item; pass 2 shows only the items sample.py marked for the kappa subset, and
never shows or reads pass-1 labels, so the second labelling is independent.
A pass-2 labeller id equal to a pass-1 id is refused (--allow-same-labeller
for a deliberate test-retest).

Standard library only, no network access beyond the loopback listener. The
page loads nothing from outside. Requests whose Host or Origin is not the
loopback address, or POSTs that are not application/json, are refused, so
another web page open in the browser cannot read or write the labels.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import sys
from datetime import date, datetime, timezone
from http import HTTPStatus
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path

from benchmarks.phase_b import privacy, score

FORMAT = "mnemos-phase-b-labels/1"
PAGE = Path(__file__).with_name("label.html")
LABELLER = re.compile(r"^[A-Za-z0-9_-]{1,32}$")
MAX_BODY = 2 << 20


class Session:
    """One labeller, one pass, one sample directory."""

    def __init__(self, d: Path, labeller: str, pass_no: int, allow_same: bool = False):
        if not LABELLER.match(labeller):
            raise ValueError("labeller id: 1-32 of letters, digits, _ and -")
        if pass_no not in (1, 2):
            raise ValueError("pass must be 1 or 2")
        self.dir = privacy.check_output_dir(d)
        text = (self.dir / "sample.json").read_text()
        self.sample_sha256 = hashlib.sha256(text.encode()).hexdigest()
        sample = json.loads(text)
        self.labeller, self.pass_no = labeller, pass_no
        self.items = [i for i in sample["items"] if pass_no == 1 or i.get("kappa")]
        self.by_id = {i["item_id"]: i for i in self.items}
        privacy.make_private_dir(self.dir / "labels")
        if pass_no == 2 and not allow_same and (self.dir / "labels" / f"pass1-{labeller}.json").exists():
            raise ValueError(f"{labeller} labelled pass 1; pass 2 must be a different person "
                             "(--allow-same-labeller for a deliberate test-retest)")
        self.path = self.dir / "labels" / f"pass{pass_no}-{labeller}.json"
        if self.path.exists():
            self.data = json.loads(self.path.read_text())
            if self.data.get("sample_sha256") != self.sample_sha256:
                raise ValueError(f"{self.path.name} was made against a different sample.json; "
                                 "keep it and label a fresh copy of the sample instead")
        else:
            self.data = {"format": FORMAT, "labeller": labeller, "pass": pass_no,
                         "sample_sha256": self.sample_sha256, "items": {}}

    def view(self) -> dict:
        """What the page needs. Stored trust, status and validity are left
        out: they would anchor the truth labels."""
        hidden = {"trust_score", "trust_model_version", "trust_computed_at", "status", "valid_to",
                  "confidence", "lifecycle", "merged_into", "archived_at", "verify_count", "last_verified"}
        items = []
        for i in self.items:
            items.append({
                "item_id": i["item_id"], "stratum": i["stratum"],
                "episode": {k: i["episode"].get(k) for k in ("id", "content", "timestamp", "metadata", "brain")},
                "claims": [{k: v for k, v in c.items() if k not in hidden} for c in i["claims"]],
                "neighbour_claims": [{k: v for k, v in c.items() if k not in hidden}
                                     for c in i.get("neighbour_claims", [])],
                "relationships": [{k: r[k] for k in ("id", "type", "from_claim_id", "to_claim_id", "within_episode")}
                                  for r in i["relationships"]],
            })
        return {"labeller": self.labeller, "pass": self.pass_no, "today": date.today().isoformat(),
                "items": items, "labels": self.data["items"], "file": self.path.name}

    def save(self, item_id: str, label: dict) -> str:
        if item_id not in self.by_id:
            raise KeyError(item_id)
        if not isinstance(label, dict):
            raise ValueError("label must be an object")
        label = {k: label[k] for k in ("done", "extraction", "relations", "truth", "questions", "notes") if k in label}
        label["updated_at"] = datetime.now(timezone.utc).isoformat(timespec="seconds")
        self.data["items"][item_id] = label
        privacy.write_json(self.path, self.data)
        return label["updated_at"]

    def match(self, item_id: str, gold: list, links: dict) -> dict:
        item = self.by_id[item_id]
        gold = [g.strip() for g in gold if isinstance(g, str) and g.strip()]
        m = score.match_claims(gold, item["claims"], links)
        sims = {cid: round(score.similarity(gold[g], next(c["text"] for c in item["claims"] if c["id"] == cid)), 2)
                for cid, g in m["pairs"].items()}
        return {"pairs": m["pairs"], "similarity": sims, "threshold": score.MATCH_THRESHOLD}


def handler(session: Session, port: int):
    hosts = {f"127.0.0.1:{port}", f"localhost:{port}"}
    origins = {f"http://{h}" for h in hosts}

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *args):  # keep labels and ids out of the terminal scrollback
            pass

        def _send(self, status: int, body: bytes, ctype: str = "application/json") -> None:
            self.send_response(status)
            self.send_header("Content-Type", ctype)
            self.send_header("Content-Length", str(len(body)))
            self.send_header("Cache-Control", "no-store")
            self.send_header("X-Content-Type-Options", "nosniff")
            self.send_header("Content-Security-Policy",
                             "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; "
                             "connect-src 'self'; frame-ancestors 'none'")
            self.end_headers()
            self.wfile.write(body)

        def _json(self, status: int, obj) -> None:
            self._send(status, json.dumps(obj, ensure_ascii=False).encode())

        def _trusted(self) -> bool:
            if self.headers.get("Host") not in hosts:
                return False
            origin = self.headers.get("Origin")
            return origin is None or origin in origins

        def do_GET(self):
            if not self._trusted():
                return self._json(HTTPStatus.FORBIDDEN, {"error": "loopback only"})
            if self.path == "/":
                return self._send(HTTPStatus.OK, PAGE.read_bytes(), "text/html; charset=utf-8")
            if self.path == "/api/session":
                return self._json(HTTPStatus.OK, session.view())
            return self._json(HTTPStatus.NOT_FOUND, {"error": "not found"})

        def do_POST(self):
            if not self._trusted():
                return self._json(HTTPStatus.FORBIDDEN, {"error": "loopback only"})
            if (self.headers.get("Content-Type") or "").split(";")[0].strip() != "application/json":
                return self._json(HTTPStatus.UNSUPPORTED_MEDIA_TYPE, {"error": "application/json only"})
            length = int(self.headers.get("Content-Length") or 0)
            if length > MAX_BODY:
                return self._json(HTTPStatus.REQUEST_ENTITY_TOO_LARGE, {"error": "too large"})
            try:
                body = json.loads(self.rfile.read(length) or b"{}")
                if self.path == "/api/label":
                    return self._json(HTTPStatus.OK, {"updated_at": session.save(body["item_id"], body["label"])})
                if self.path == "/api/match":
                    return self._json(HTTPStatus.OK,
                                      session.match(body["item_id"], body.get("gold") or [], body.get("links") or {}))
            except (KeyError, ValueError, TypeError) as e:
                return self._json(HTTPStatus.BAD_REQUEST, {"error": str(e)})
            return self._json(HTTPStatus.NOT_FOUND, {"error": "not found"})

    return Handler


def main(argv: list[str] | None = None) -> int:
    p = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    p.add_argument("--dir", required=True, help="sample directory written by sample.py")
    p.add_argument("--labeller", required=True, help="your labeller id, e.g. felix")
    p.add_argument("--pass", dest="pass_no", type=int, choices=[1, 2], required=True)
    p.add_argument("--port", type=int, default=8441)
    p.add_argument("--allow-same-labeller", action="store_true")
    args = p.parse_args(argv)
    try:
        session = Session(Path(args.dir).expanduser(), args.labeller, args.pass_no, args.allow_same_labeller)
    except (ValueError, FileNotFoundError, privacy.UnsafeOutputDir) as e:
        print(f"label: {e}", file=sys.stderr)
        return 2
    server = HTTPServer(("127.0.0.1", args.port), handler(session, args.port))
    print(f"pass {args.pass_no}, {len(session.items)} items, labels -> {session.path}")
    print(f"open http://127.0.0.1:{args.port}/   (Ctrl-C to stop; every change is saved as you go)")
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()
    return 0


if __name__ == "__main__":
    sys.exit(main())
