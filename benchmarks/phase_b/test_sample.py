"""Tests for the Phase B sampler, against a synthetic brain only."""

from __future__ import annotations

import contextlib
import hashlib
import io
import json
import os
import sqlite3
import stat
import subprocess
import sys
import tempfile
import unittest
from collections import Counter
from fractions import Fraction
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))

from benchmarks.phase_b import _brain_fixture, privacy, sample  # noqa: E402


def run(brain: Path, out: Path, *extra: str) -> int:
    with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
        return sample.main([str(brain), "--out", str(out), *extra])


class SamplerTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.brain = self.root / "mnemos.db"
        self.ids = _brain_fixture.build(self.brain)

    def tearDown(self):
        self.tmp.cleanup()

    def load(self, out: Path) -> tuple[dict, dict]:
        return json.loads((out / "sample.json").read_text()), json.loads((out / "manifest.json").read_text())

    def test_deterministic_given_seed(self):
        a, b, c = self.root / "a", self.root / "b", self.root / "c"
        self.assertEqual(run(self.brain, a, "--n", "20", "--kappa-n", "5"), 0)
        self.assertEqual(run(self.brain, b, "--n", "20", "--kappa-n", "5"), 0)
        self.assertEqual(run(self.brain, c, "--n", "20", "--kappa-n", "5", "--seed", "7"), 0)
        self.assertEqual((a / "sample.json").read_bytes(), (b / "sample.json").read_bytes())
        ids = lambda d: {i["item_id"] for i in self.load(d)[0]["items"]}  # noqa: E731
        self.assertNotEqual(ids(a), ids(c))

    def test_brain_is_opened_read_only_and_left_unchanged(self):
        before = hashlib.sha256(self.brain.read_bytes()).hexdigest()
        self.assertEqual(run(self.brain, self.root / "out", "--n", "10"), 0)
        self.assertEqual(hashlib.sha256(self.brain.read_bytes()).hexdigest(), before)
        conn = sample.open_readonly(self.brain)
        with self.assertRaises(sqlite3.OperationalError):
            conn.execute("INSERT INTO events (id, run_id, schema_version, content, source_input_id, timestamp, "
                         "metadata_json, ingested_at) VALUES ('x','r','v','c','i','t','{}','t')")
        conn.close()
        self.assertFalse(Path(str(self.brain) + "-wal").exists())

    def test_strata_cover_projects_session_types_and_age(self):
        out = self.root / "out"
        self.assertEqual(run(self.brain, out, "--n", "20", "--kappa-n", "6"), 0)
        s, m = self.load(out)
        self.assertEqual(len(s["items"]), 20)
        strata = {(x["project"], x["session_type"], x["age"]) for x in m["strata"]}
        self.assertIn(("alpha", "capture/text", "0-7d"), strata)
        self.assertIn(("beta", "file/json_object", "91-365d"), strata)
        label = sample.brain_label(str(self.brain))[0]
        self.assertIn((label, "other/text", ">365d"), strata)  # no workspace: the brain's label
        self.assertTrue(all(x["sampled"] >= 1 for x in m["strata"]), "sqrt allocation reaches every stratum")
        self.assertEqual(sum(x["sampled"] for x in m["strata"]), 20)
        for x in m["strata"]:
            self.assertLessEqual(x["sampled"], x["population"])

    def test_heldout_third_and_kappa_subset(self):
        out = self.root / "out"
        self.assertEqual(run(self.brain, out, "--n", "30", "--kappa-n", "9"), 0)
        s, m = self.load(out)
        splits = Counter(i["split"] for i in s["items"])
        self.assertEqual(splits["heldout"], 10)
        self.assertEqual(sum(i["kappa"] for i in s["items"]), 9)
        self.assertEqual(m["counts"]["heldout"], 10)
        # The held-out third is drawn across strata, not from one corner of the sample.
        self.assertGreater(len({i["stratum"]["project"] for i in s["items"] if i["split"] == "heldout"}), 1)

    def test_item_exports_episode_claims_and_relationships(self):
        out = self.root / "out"
        self.assertEqual(run(self.brain, out, "--n", "40"), 0)
        s, _ = self.load(out)
        by_ep = {i["episode"]["id"]: i for i in s["items"]}
        self.assertEqual(len(by_ep), 40, "the fixture has 40 episodes, so all are sampled")
        ep = by_ep["ev_002"]
        self.assertIn("content", ep["episode"])
        self.assertEqual(ep["episode"]["metadata"]["workspace"], "alpha")
        self.assertEqual([c["id"] for c in ep["claims"]], ["cl_002_0", "cl_002_1"])
        self.assertIn("trust_score", ep["claims"][0])
        rel = ep["relationships"]
        self.assertTrue(any(r["type"] == "supports" and r["within_episode"] for r in rel))
        # The cross-episode contradiction brings its other endpoint as a neighbour claim.
        holder = next(i for i in s["items"] if any(c["id"] == self.ids["superseder"] for c in i["claims"]))
        self.assertIn(self.ids["superseded"], [c["id"] for c in holder["neighbour_claims"]])

    def test_cross_edge_cap(self):
        out = self.root / "out"
        self.assertEqual(run(self.brain, out, "--n", "40", "--max-cross-edges", "0"), 0)
        s, _ = self.load(out)
        self.assertTrue(all(r["within_episode"] for i in s["items"] for r in i["relationships"]))
        self.assertGreaterEqual(sum(i["relationships_truncated"] for i in s["items"]), 1)

    def test_refuses_output_inside_the_repository(self):
        inside = privacy.REPO_ROOT / "benchmarks" / "data" / "phase_b" / "x"
        self.assertEqual(run(self.brain, inside), 2)
        self.assertFalse(inside.exists())

    def test_refuses_output_inside_any_git_worktree(self):
        repo = self.root / "otherrepo"
        repo.mkdir()
        subprocess.run(["git", "init", "-q", str(repo)], check=True)
        self.assertEqual(run(self.brain, repo / "golden"), 2)
        self.assertFalse((repo / "golden").exists())

    def test_refuses_output_in_the_brain_data_directory(self):
        with self.assertRaises(privacy.UnsafeOutputDir):
            privacy.check_output_dir(privacy.BRAIN_DATA_DIR / "phase-b")
        privacy.check_output_dir(privacy.DEFAULT_OUT / "probe")  # the default root is allowed (not created)

    def test_never_overwrites_a_sample_without_force(self):
        out = self.root / "out"
        self.assertEqual(run(self.brain, out, "--n", "10"), 0)
        (out / "labels" / "pass1-felix.json").write_text("{}")
        self.assertEqual(run(self.brain, out, "--n", "12"), 2)
        self.assertEqual(len(self.load(out)[0]["items"]), 10)
        self.assertEqual(run(self.brain, out, "--n", "12", "--force"), 0)
        self.assertEqual(len(self.load(out)[0]["items"]), 12)
        self.assertEqual((out / "labels" / "pass1-felix.json").read_text(), "{}")

    def test_output_is_private_and_self_ignoring(self):
        out = self.root / "out"
        self.assertEqual(run(self.brain, out, "--n", "10"), 0)
        self.assertEqual(stat.S_IMODE(os.stat(out).st_mode), 0o700)
        self.assertEqual(stat.S_IMODE(os.stat(out / "sample.json").st_mode), 0o600)
        self.assertIn("*", (out / ".gitignore").read_text().splitlines())
        _, m = self.load(out)
        self.assertEqual(m["sample_sha256"], hashlib.sha256((out / "sample.json").read_bytes()).hexdigest())

    def test_refuses_a_brain_with_pending_wal(self):
        Path(str(self.brain) + "-wal").write_bytes(b"x" * 32)
        self.assertEqual(run(self.brain, self.root / "out"), 2)
        self.assertEqual(run(self.brain, self.root / "out2", "--n", "5", "--ignore-wal"), 0)

    def test_reads_a_brain_that_predates_newer_edge_columns(self):
        # Real workspace brains written by older versions lack
        # relationships.derived_by; the sampler must still export their edges.
        old = self.root / "old.db"
        _brain_fixture.build(old)
        with sqlite3.connect(old) as c:
            c.execute("ALTER TABLE relationships DROP COLUMN derived_by")
        out = self.root / "out-old"
        self.assertEqual(run(old, out, "--n", "10"), 0)
        sample_json, _ = self.load(out)
        edges = [e for it in sample_json["items"] for e in it["relationships"]]
        self.assertTrue(edges, "fixture should yield edges")
        self.assertTrue(all("derived_by" in e and e["derived_by"] is None for e in edges))

    def test_no_default_brain(self):
        with self.assertRaises(SystemExit), contextlib.redirect_stderr(io.StringIO()):
            sample.parser().parse_args(["--out", str(self.root / "o")])

    def test_length_filters_are_recorded(self):
        out = self.root / "out"
        self.assertEqual(run(self.brain, out, "--n", "10", "--max-chars", "120"), 0)
        _, m = self.load(out)
        b = m["brains"][0]
        self.assertGreater(b["excluded"]["too_long"], 0)
        self.assertEqual(b["eligible"] + b["excluded"]["too_long"] + b["excluded"]["too_short"], b["events_total"])

    def test_trust_bands_stratify_by_live_claim_trust(self):
        # Every third fixture episode has no claims, and claim trust spans 0.30-0.75.
        out = self.root / "bands"
        self.assertEqual(run(self.brain, out, "--n", "12", "--trust-bands", "10", "--allocation", "equal"), 0)
        s, m = self.load(out)
        self.assertEqual(len(s["items"]), 12)
        for item in s["items"]:
            live = [c for c in item["claims"] if c["status"] != "deprecated" and not c.get("merged_into")]
            self.assertTrue(live, f"{item['item_id']} has no live claim to label")
            mean = sum(c["trust_score"] for c in live) / len(live)
            band = item["stratum"]["trust_band"]
            lo, hi = (float(x) for x in band.split("-"))
            self.assertTrue(lo <= mean < hi or (hi == 1.0 and mean == 1.0), f"{mean} outside {band}")
        bands = {st["trust_band"]: st["sampled"] for st in m["strata"]}
        self.assertEqual(set(bands), {"0.3-0.4", "0.4-0.5", "0.5-0.6", "0.6-0.7", "0.7-0.8"})
        self.assertLessEqual(max(bands.values()) - min(bands.values()), 1)  # equal allocation
        self.assertGreater(m["brains"][0]["excluded"]["no_live_claims"], 0)
        self.assertEqual(m["parameters"]["trust_bands"], 10)

    def test_trust_bands_ignore_deprecated_claims(self):
        conn = sqlite3.connect(self.brain)
        eid = conn.execute("SELECT event_id FROM claim_evidence ORDER BY event_id LIMIT 1").fetchone()[0]
        conn.execute("UPDATE claims SET status = 'deprecated' WHERE id IN "
                     "(SELECT claim_id FROM claim_evidence WHERE event_id = ?)", (eid,))
        conn.commit()
        conn.close()
        out = self.root / "dep"
        self.assertEqual(run(self.brain, out, "--n", "500", "--trust-bands", "10"), 0)
        s, _ = self.load(out)
        picked = {i["episode"]["id"] for i in s["items"]}
        self.assertGreater(len(picked), 5)
        self.assertNotIn(eid, picked)

    def test_exclude_drops_episodes_of_an_earlier_sample(self):
        first, second = self.root / "first", self.root / "second"
        self.assertEqual(run(self.brain, first, "--n", "15"), 0)
        self.assertEqual(run(self.brain, second, "--n", "500", "--exclude", str(first / "sample.json")), 0)
        a = {i["item_id"] for i in self.load(first)[0]["items"]}
        s, m = self.load(second)
        b = {i["item_id"] for i in s["items"]}
        self.assertEqual(a & b, set())
        self.assertEqual(m["brains"][0]["excluded"]["earlier_sample"], 15)
        self.assertEqual(len(a) + len(b), m["brains"][0]["events_total"] - sum(
            v for k, v in m["brains"][0]["excluded"].items() if k != "earlier_sample"))


class UnitTest(unittest.TestCase):
    def test_allocate(self):
        sizes = {"a": 100, "b": 4, "c": 1}
        for mode in ("sqrt", "proportional", "equal"):
            alloc = sample.allocate(sizes, 20, mode)
            self.assertEqual(sum(alloc.values()), 20)
            self.assertTrue(all(alloc[k] <= sizes[k] for k in sizes))
        self.assertEqual(sample.allocate(sizes, 20, "proportional")["a"], 19)
        self.assertEqual(sample.allocate(sizes, 500, "sqrt"), sizes)  # capped by the population

    def test_systematic_draws_exactly(self):
        self.assertEqual(len(sample.systematic(200, Fraction(1, 3))), 66)
        self.assertEqual(len(sample.systematic(200, Fraction(50, 200))), 50)
        self.assertEqual(len(sample.systematic(0, Fraction(1, 3))), 0)

    def test_age_buckets_and_session_type(self):
        newest = sample.parse_time("2026-11-01T00:00:00.123456789Z")
        self.assertEqual(sample.age_bucket(sample.parse_time("2026-10-30T00:00:00Z"), newest), "0-7d")
        self.assertEqual(sample.age_bucket(sample.parse_time("2025-01-01T00:00:00+00:00"), newest), ">365d")
        self.assertEqual(sample.age_bucket(None, newest), "unknown")
        self.assertEqual(sample.session_type({"session_id": "s", "input_source_path": "/a"}), "capture/-")
        self.assertEqual(sample.session_type({"input_source_path": "/a", "chunk_kind": "text"}), "file/text")
        self.assertEqual(sample.session_type({}), "other/-")

    def test_brain_label(self):
        self.assertEqual(sample.brain_label("global=/nowhere/x.db")[0], "global")
        self.assertEqual(sample.brain_label("/r/myrepo/.mnemos/mnemos.db")[0], "myrepo")
        self.assertEqual(sample.brain_label("/data/brains/work.db")[0], "work")


if __name__ == "__main__":
    unittest.main()
