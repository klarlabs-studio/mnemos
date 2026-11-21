"""Draw the Phase B golden-set sample from one or more brains (#441).

    python3 -m benchmarks.phase_b.sample BRAIN [BRAIN ...] [--out DIR] [--seed 441] [--n 200]

BRAIN is a path to a SQLite brain file, optionally prefixed with a label
(`global=/path/to/mnemos.db`). There is deliberately no default brain: the
caller names it. Each brain is opened strictly read-only
(`file:...?mode=ro&immutable=1`) and is never written.

Strata. Every eligible episode (an `events` row) falls in exactly one stratum,
the triple
- project: the episode's `workspace` metadata (--project-key), or the brain's
  label when the episode has none;
- session type: `<origin>/<chunk_kind>`, where origin is `capture` when the
  episode has a session_id, otherwise `file` when it has an input_source_path,
  otherwise `other`;
- age: the episode timestamp's distance from the brain's newest episode, in the
  buckets of AGE_BUCKETS. Measured from the newest episode, not the wall clock,
  so the same brain always gives the same buckets.

Allocation. --n episodes are allocated across strata by --allocation
(`sqrt`, the default, is proportional to the square root of the stratum's
size: small projects and rare session types still appear, and large ones still
get more), by largest remainder, capped at each stratum's size. Within a
stratum the episodes are ordered by sha256(seed, brain, episode id) and the
first k are taken, so the draw depends only on the seed and the brain's
contents, never on row order.

Splits. A third of the sample (--heldout-fraction) is marked held out, and
--kappa-n items are marked for the second labelling pass. Both are systematic
draws over the sample ordered by (stratum, seeded hash), so each mirrors the
strata of the whole.

Output (--out, default ~/.local/share/mnemos-eval/phase-b/<run>):
    sample.json    the items: episode text and metadata, the claims extracted
                   from it (claim_evidence), and relationships touching them
    manifest.json  parameters, brain fingerprints, the strata table
    labels/        where label.py writes pass-1 and pass-2 label files
    .gitignore     ignores everything in the directory

The directory may not be inside a git worktree or the brain's data directory
(privacy.py). An existing sample is never overwritten without --force, and
labels/ is never touched.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import math
import re
import sqlite3
import sys
import urllib.parse
from datetime import datetime, timezone
from fractions import Fraction
from pathlib import Path

from benchmarks.phase_b import privacy

FORMAT = "mnemos-phase-b-sample/1"

# (label, upper bound in days, inclusive). The last bucket is open-ended.
AGE_BUCKETS = [("0-7d", 7), ("8-30d", 30), ("31-90d", 90), ("91-365d", 365), (">365d", None)]

# Claim columns exported when the brain has them. Missing columns (an older
# brain) are skipped rather than failing the run.
CLAIM_COLUMNS = [
    "id", "text", "type", "confidence", "status", "created_at", "created_by",
    "trust_score", "trust_model_version", "trust_computed_at", "valid_from", "valid_to",
    "last_verified", "verify_count", "half_life_days", "lifecycle", "durability",
    "subject_class", "merged_into", "archived_at", "scope_service", "scope_env", "scope_team",
]
NEIGHBOUR_COLUMNS = ["id", "text", "type", "status", "created_at", "trust_score",
                     "valid_from", "valid_to", "lifecycle", "merged_into"]
EDGE_PRIORITY = {"contradicts": 0, "supports": 1}


# ---- opening the brain -----------------------------------------------------

def open_readonly(path: Path) -> sqlite3.Connection:
    """Open a SQLite brain so that nothing can write to it."""
    if not path.is_file():
        raise FileNotFoundError(f"brain not found: {path}")
    uri = "file:" + urllib.parse.quote(str(path.resolve())) + "?mode=ro&immutable=1"
    conn = sqlite3.connect(uri, uri=True)
    conn.execute("PRAGMA query_only = ON")
    conn.row_factory = sqlite3.Row
    return conn


def wal_pending(path: Path) -> bool:
    """immutable=1 reads the main file only, so uncheckpointed WAL frames are
    invisible, and a writer checkpointing mid-read can make reads inconsistent."""
    wal = Path(str(path) + "-wal")
    return wal.exists() and wal.stat().st_size > 0


def columns(conn: sqlite3.Connection, table: str) -> set[str]:
    return {r["name"] for r in conn.execute(f"PRAGMA table_info({table})")}


def file_sha256(path: Path) -> str:
    h = hashlib.sha256()
    with path.open("rb") as f:
        for block in iter(lambda: f.read(1 << 20), b""):
            h.update(block)
    return h.hexdigest()


def brain_label(spec: str) -> tuple[str, Path]:
    """`label=path` or a bare path, whose label is derived from where it lives."""
    if "=" in spec and not Path(spec).exists():
        label, _, p = spec.partition("=")
        return label, Path(p).expanduser()
    p = Path(spec).expanduser()
    parent = p.resolve().parent
    if parent.name == ".mnemos":  # a repo brain: <repo>/.mnemos/mnemos.db
        return parent.parent.name, p
    return (parent.name if p.stem == "mnemos" else p.stem), p


# ---- strata ----------------------------------------------------------------

_FRACTION = re.compile(r"(\.\d{6})\d+")


def parse_time(s: str) -> datetime | None:
    if not s:
        return None
    s = _FRACTION.sub(r"\1", s.strip().replace("Z", "+00:00"))
    try:
        t = datetime.fromisoformat(s)
    except ValueError:
        return None
    return t if t.tzinfo else t.replace(tzinfo=timezone.utc)


def age_bucket(t: datetime | None, newest: datetime | None) -> str:
    if t is None or newest is None:
        return "unknown"
    days = (newest - t).total_seconds() / 86400
    for label, upper in AGE_BUCKETS:
        if upper is None or days <= upper:
            return label
    return AGE_BUCKETS[-1][0]


def session_type(meta: dict) -> str:
    if meta.get("session_id"):
        origin = "capture"
    elif meta.get("input_source_path"):
        origin = "file"
    else:
        origin = "other"
    return f"{origin}/{meta.get('chunk_kind') or '-'}"


def parse_metadata(raw: str) -> dict:
    try:
        meta = json.loads(raw or "{}")
    except json.JSONDecodeError:
        return {}
    return {str(k): ("" if v is None else str(v)) for k, v in meta.items()} if isinstance(meta, dict) else {}


def seeded_key(seed: int, *parts: str) -> str:
    return hashlib.sha256("\0".join([str(seed), *parts]).encode()).hexdigest()


def allocate(sizes: dict, n: int, mode: str = "sqrt") -> dict:
    """Split n across strata by weight (largest remainder), capped by size."""
    weight = {"proportional": lambda s: s, "sqrt": math.sqrt, "equal": lambda s: 1}[mode]
    alloc = {k: 0 for k in sizes}
    remaining = min(n, sum(sizes.values()))
    while remaining > 0:  # a stratum that hits its cap hands its excess to the rest
        active = sorted(k for k in sizes if alloc[k] < sizes[k])
        total_w = sum(weight(sizes[k]) for k in active)
        quotas = {k: remaining * weight(sizes[k]) / total_w for k in active}
        add = {k: min(int(quotas[k]), sizes[k] - alloc[k]) for k in active}
        left = remaining - sum(add.values())
        order = sorted((k for k in active if alloc[k] + add[k] < sizes[k]),
                       key=lambda k: (-(quotas[k] - int(quotas[k])), k))
        for k in order[:left]:
            add[k] += 1
        for k, a in add.items():
            alloc[k] += a
        remaining -= sum(add.values())
    return alloc


def systematic(n: int, fraction: Fraction) -> set[int]:
    """Positions i in range(n) for which floor((i+1)f) > floor(i f): exactly
    floor(n f) of them, spread evenly over the order."""
    return {i for i in range(n) if math.floor((i + 1) * fraction) > math.floor(i * fraction)}


# ---- reading episodes ------------------------------------------------------

def population(conn: sqlite3.Connection, label: str, args) -> tuple[list[dict], dict]:
    rows = conn.execute("SELECT id, timestamp, metadata_json, length(content) AS chars FROM events").fetchall()
    times = [parse_time(r["timestamp"]) for r in rows]
    newest = max((t for t in times if t), default=None)
    eligible, excluded = [], {"too_short": 0, "too_long": 0}
    for r, t in zip(rows, times):
        chars = r["chars"] or 0
        if chars < args.min_chars:
            excluded["too_short"] += 1
            continue
        if args.max_chars and chars > args.max_chars:
            excluded["too_long"] += 1
            continue
        meta = parse_metadata(r["metadata_json"])
        project = meta.get(args.project_key) or label
        eligible.append({
            "brain": label, "event_id": r["id"],
            "stratum": (project, session_type(meta), age_bucket(t, newest)),
        })
    info = {"events_total": len(rows), "eligible": len(eligible), "excluded": excluded,
            "newest_episode": newest.isoformat() if newest else None}
    return eligible, info


def _select(conn, table: str, wanted: list[str]) -> str:
    have = columns(conn, table)
    return ", ".join(c for c in wanted if c in have)


def export_item(conn: sqlite3.Connection, label: str, event_id: str, max_cross_edges: int) -> dict:
    ev = conn.execute("SELECT * FROM events WHERE id = ?", (event_id,)).fetchone()
    episode = {k: ev[k] for k in ev.keys() if k != "metadata_json"}
    episode["metadata"] = parse_metadata(ev["metadata_json"])
    episode["brain"] = label

    cols = _select(conn, "claims", CLAIM_COLUMNS)
    claims = [dict(r) for r in conn.execute(
        f"SELECT {', '.join('c.' + c for c in cols.split(', '))} FROM claim_evidence ce "
        "JOIN claims c ON c.id = ce.claim_id WHERE ce.event_id = ? ORDER BY c.created_at, c.id",
        (event_id,))]
    ids = [c["id"] for c in claims]
    for c in claims:
        others = [r[0] for r in conn.execute(
            "SELECT event_id FROM claim_evidence WHERE claim_id = ? AND event_id != ? ORDER BY event_id",
            (c["id"], event_id))]
        c["evidence_count"] = len(others) + 1
        c["other_episode_ids"] = others[:5]

    edges, neighbours, truncated = [], {}, 0
    if ids:
        marks = ",".join("?" * len(ids))
        rows = [dict(r) for r in conn.execute(
            "SELECT id, type, from_claim_id, to_claim_id, strength, derived_by, created_at FROM relationships "
            f"WHERE from_claim_id IN ({marks}) OR to_claim_id IN ({marks})", ids + ids)]
        mine = set(ids)
        for r in rows:
            r["within_episode"] = r["from_claim_id"] in mine and r["to_claim_id"] in mine
        rows.sort(key=lambda r: (not r["within_episode"], EDGE_PRIORITY.get(r["type"], 9),
                                 r["created_at"] or "", r["id"]))
        cross = 0
        for r in rows:
            if not r["within_episode"]:
                cross += 1
                if cross > max_cross_edges:
                    truncated += 1
                    continue
            edges.append(r)
        outside = sorted({e[k] for e in edges for k in ("from_claim_id", "to_claim_id")} - mine)
        if outside:
            ncols = _select(conn, "claims", NEIGHBOUR_COLUMNS)
            for r in conn.execute(f"SELECT {ncols} FROM claims WHERE id IN ({','.join('?' * len(outside))})",
                                  outside):
                neighbours[r["id"]] = dict(r)
    return {
        "item_id": f"{label}/{event_id}",
        "episode": episode,
        "claims": claims,
        "relationships": edges,
        "neighbour_claims": [neighbours[k] for k in sorted(neighbours)],
        "relationships_truncated": truncated,
    }


# ---- the run ---------------------------------------------------------------

def draw(brains: list[tuple[str, Path]], args) -> tuple[dict, dict]:
    conns, pop, brain_info = {}, [], []
    for label, path in brains:
        if label in conns:
            raise ValueError(f"two brains share the label {label!r}; name them with label=path")
        if wal_pending(path) and not args.ignore_wal:
            raise RuntimeError(
                f"{path}-wal is not empty: a read-only immutable open cannot see those writes, and a live "
                "writer can make the read inconsistent. Sample a quiescent copy of the brain (copy the .db, "
                "-wal and -shm files while nothing writes), or pass --ignore-wal to sample the main file as is.")
        conn = open_readonly(path)
        conns[label] = conn
        eligible, info = population(conn, label, args)
        pop += eligible
        brain_info.append({"label": label, "path": str(path.resolve()), "bytes": path.stat().st_size,
                           "sha256": file_sha256(path), **info})

    by_stratum: dict[tuple, list[dict]] = {}
    for e in pop:
        by_stratum.setdefault(e["stratum"], []).append(e)
    alloc = allocate({k: len(v) for k, v in by_stratum.items()}, args.n, args.allocation)

    picked = []
    for k in sorted(by_stratum):
        ordered = sorted(by_stratum[k], key=lambda e: seeded_key(args.seed, e["brain"], e["event_id"]))
        picked += ordered[: alloc[k]]

    def draw_subset(salt: str, fraction: Fraction) -> set[int]:
        ordered = sorted(picked, key=lambda e: (e["stratum"], seeded_key(args.seed, salt, e["brain"], e["event_id"])))
        return {id(ordered[i]) for i in systematic(len(ordered), fraction)}

    heldout = draw_subset("heldout", Fraction(args.heldout_fraction).limit_denominator(1000))
    kappa_n = min(args.kappa_n, len(picked))
    kappa = draw_subset("kappa", Fraction(kappa_n, len(picked)) if picked else Fraction(0))

    items = []
    for e in sorted(picked, key=lambda e: seeded_key(args.seed, "order", e["brain"], e["event_id"])):
        item = export_item(conns[e["brain"]], e["brain"], e["event_id"], args.max_cross_edges)
        project, stype, age = e["stratum"]
        item["stratum"] = {"project": project, "session_type": stype, "age": age}
        item["split"] = "heldout" if id(e) in heldout else "dev"
        item["kappa"] = id(e) in kappa
        items.append(item)
    for c in conns.values():
        c.close()

    sample = {"format": FORMAT, "seed": args.seed, "items": items}
    strata = []
    for k in sorted(by_stratum):
        chosen = [e for e in picked if e["stratum"] == k]
        strata.append({"project": k[0], "session_type": k[1], "age": k[2], "population": len(by_stratum[k]),
                       "sampled": len(chosen), "heldout": sum(id(e) in heldout for e in chosen),
                       "kappa": sum(id(e) in kappa for e in chosen)})
    manifest = {
        "format": FORMAT,
        "created_at": datetime.now(timezone.utc).isoformat(timespec="seconds"),
        "parameters": {"n": args.n, "seed": args.seed, "allocation": args.allocation,
                       "heldout_fraction": args.heldout_fraction, "kappa_n": args.kappa_n,
                       "min_chars": args.min_chars, "max_chars": args.max_chars,
                       "project_key": args.project_key, "max_cross_edges": args.max_cross_edges,
                       "age_buckets": [b[0] for b in AGE_BUCKETS]},
        "brains": brain_info,
        "counts": {"items": len(items), "heldout": sum(i["split"] == "heldout" for i in items),
                   "kappa": sum(i["kappa"] for i in items), "claims": sum(len(i["claims"]) for i in items),
                   "relationships": sum(len(i["relationships"]) for i in items)},
        "strata": strata,
    }
    return sample, manifest


def sample_text(sample: dict) -> str:
    return json.dumps(sample, indent=1, ensure_ascii=False) + "\n"


def write(out: Path, sample: dict, manifest: dict, force: bool) -> None:
    out = privacy.check_output_dir(out)
    if (out / "sample.json").exists() and not force:
        raise FileExistsError(f"{out}/sample.json exists; labels may refer to it. Pass --force to replace it "
                              "(labels/ is left untouched), or choose another --out")
    privacy.make_private_dir(out)
    privacy.make_private_dir(out / "labels")
    text = sample_text(sample)
    manifest["sample_sha256"] = hashlib.sha256(text.encode()).hexdigest()
    privacy.write_private(out / "sample.json", text)
    privacy.write_json(out / "manifest.json", manifest)


def parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    p.add_argument("brains", nargs="+", metavar="BRAIN", help="[label=]path to a SQLite brain (no default)")
    p.add_argument("--out", help=f"output directory (default {privacy.DEFAULT_OUT}/<run>)")
    p.add_argument("--run", default="golden", help="run name under the default output root")
    p.add_argument("--n", type=int, default=200)
    p.add_argument("--seed", type=int, default=441)
    p.add_argument("--allocation", choices=["sqrt", "proportional", "equal"], default="sqrt")
    p.add_argument("--heldout-fraction", default="1/3", help="fraction held out, e.g. 1/3")
    p.add_argument("--kappa-n", type=int, default=50, help="items for the second labelling pass")
    p.add_argument("--min-chars", type=int, default=20, help="skip episodes shorter than this")
    p.add_argument("--max-chars", type=int, default=8000, help="skip longer episodes (0: no limit)")
    p.add_argument("--project-key", default="workspace", help="metadata key naming the project")
    p.add_argument("--max-cross-edges", type=int, default=40, help="cap on edges leaving the episode, per item")
    p.add_argument("--ignore-wal", action="store_true", help="sample even when the brain has a non-empty -wal")
    p.add_argument("--force", action="store_true", help="replace an existing sample.json")
    return p


def main(argv: list[str] | None = None) -> int:
    args = parser().parse_args(argv)
    out = Path(args.out).expanduser() if args.out else privacy.DEFAULT_OUT / args.run
    try:
        out = privacy.check_output_dir(out)  # refuse before reading anything
        sample, manifest = draw([brain_label(s) for s in args.brains], args)
        write(out, sample, manifest, args.force)
    except (privacy.UnsafeOutputDir, FileExistsError, FileNotFoundError, RuntimeError, ValueError) as e:
        print(f"sample: {e}", file=sys.stderr)
        return 2
    c = manifest["counts"]
    print(f"{c['items']} items ({c['heldout']} held out, {c['kappa']} for pass 2), "
          f"{c['claims']} claims, {c['relationships']} relationships, {len(manifest['strata'])} strata -> {out}")
    print(f"label: python3 -m benchmarks.phase_b.label --dir {out} --labeller <you> --pass 1")
    return 0


if __name__ == "__main__":
    sys.exit(main())
