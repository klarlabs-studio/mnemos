"""A synthetic brain for the Phase B tests, built from the real schema.

sql/sqlite/schema.sql is applied to a fresh file in a temporary directory and
filled with invented episodes, claims and edges. No test reads a real brain.
"""

from __future__ import annotations

import json
import sqlite3
from datetime import datetime, timedelta, timezone
from pathlib import Path

from benchmarks.phase_b import privacy

SCHEMA = privacy.REPO_ROOT / "sql" / "sqlite" / "schema.sql"
NEWEST = datetime(2026, 11, 1, 12, 0, tzinfo=timezone.utc)

# (workspace or "", origin, chunk_kind, age in days, how many)
PLAN = [
    ("alpha", "capture", "text", 1, 12),
    ("alpha", "capture", "text", 20, 8),
    ("alpha", "file", "text", 45, 6),
    ("beta", "capture", "text", 3, 5),
    ("beta", "file", "json_object", 200, 3),
    ("", "other", "text", 400, 4),
    ("", "capture", "text", 0, 2),
]


def build(path: Path, plan=PLAN) -> dict:
    """Write a brain to path. Returns ids useful to assertions."""
    conn = sqlite3.connect(path)
    conn.executescript(SCHEMA.read_text())
    n = 0
    claim_ids: list[str] = []
    events: list[str] = []
    for ws, origin, kind, age, count in plan:
        for _ in range(count):
            n += 1
            eid = f"ev_{n:03d}"
            ts = (NEWEST - timedelta(days=age, minutes=n)).isoformat().replace("+00:00", "Z")
            meta = {"chunk_kind": kind}
            if ws:
                meta["workspace"] = ws
            if origin == "capture":
                meta["session_id"] = f"sess-{n % 4}"
            elif origin == "file":
                meta["input_source_path"] = f"/work/{ws or 'misc'}/doc{n}.md"
            content = (f"Episode {n}. The build cache for service {ws or 'misc'} lives in directory cache{n}. "
                       f"We decided to pin tool version v1.{n}.0 for reproducibility.")
            conn.execute(
                "INSERT INTO events (id, run_id, schema_version, content, source_input_id, timestamp, metadata_json, "
                "ingested_at, created_by) VALUES (?,?,?,?,?,?,?,?,?)",
                (eid, "run1", "v1", content, f"in_{n}", ts, json.dumps(meta), ts, "tester"))
            events.append(eid)
            for k in range(n % 3):
                cid = f"cl_{n:03d}_{k}"
                text = (f"The build cache for service {ws or 'misc'} lives in directory cache{n}" if k == 0
                        else f"Tool version v1.{n}.0 is pinned for reproducibility")
                conn.execute(
                    "INSERT INTO claims (id, text, type, confidence, status, created_at, trust_score, valid_from) "
                    "VALUES (?,?,?,?,?,?,?,?)",
                    (cid, text, "fact", 0.8, "active", ts, round(0.3 + 0.05 * (n % 10), 2), ts))
                conn.execute("INSERT INTO claim_evidence (claim_id, event_id, created_at) VALUES (?,?,?)",
                             (cid, eid, ts))
                claim_ids.append(cid)
    # Edges: within-episode supports, cross-episode contradicts, and one supersession.
    rid = 0
    for cid in claim_ids:
        if cid.endswith("_1"):
            rid += 1
            conn.execute("INSERT INTO relationships (id, type, from_claim_id, to_claim_id, created_at) "
                         "VALUES (?,?,?,?,?)", (f"rel_{rid}", "supports", cid[:-1] + "0", cid, "2026-10-01T00:00:00Z"))
    rid += 1
    conn.execute("INSERT INTO relationships (id, type, from_claim_id, to_claim_id, created_at) VALUES (?,?,?,?,?)",
                 (f"rel_{rid}", "contradicts", claim_ids[0], claim_ids[-1], "2026-10-02T00:00:00Z"))
    conn.execute("UPDATE claims SET valid_to = ? WHERE id = ?", ("2026-10-02T00:00:00Z", claim_ids[-1]))
    conn.commit()
    conn.close()
    return {"events": events, "claims": claim_ids, "superseded": claim_ids[-1], "superseder": claim_ids[0]}
