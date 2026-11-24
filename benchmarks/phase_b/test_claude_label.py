"""Tests for the blind model labeller. claude_cli.complete is mocked: no real calls."""

from __future__ import annotations

import builtins
import contextlib
import io
import json
import os
import re
import stat
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))

from benchmarks.phase_b import _brain_fixture, claude_label, privacy, sample, score  # noqa: E402

HIDDEN = ("trust_score", "status", "valid_to", "confidence", "lifecycle", "merged_into")
PASS1_MARKER = "pass-one-only-7f3a9c"  # written into pass-1 labels; must never reach the model


def data_block(prompt: str) -> dict:
    return json.loads(re.search(r"<<<DATA\n(.*)\nDATA>>>", prompt, re.S).group(1))


class FakeModel:
    """Answers both steps from what the prompt shows, and records every prompt."""

    def __init__(self, bad_first: int = 0, always_bad: bool = False):
        self.prompts: list[list[dict]] = []
        self.bad_first, self.always_bad = bad_first, always_bad

    def __call__(self, model: str, messages: list[dict], timeout: int = 300):
        self.prompts.append(messages)
        if self.always_bad or len(self.prompts) <= self.bad_first:
            return "Sure! Here are the labels you asked for.", 0.001
        user = messages[1]["content"]
        if user.startswith("STEP 1"):
            episode = re.search(r"<<<EPISODE\n(.*)\nEPISODE>>>", user, re.S).group(1)
            first = episode.split(". ", 1)[1].split(". ")[0]  # one sentence of the episode, as a gold claim
            return "```json\n" + json.dumps({"gold": [first]}) + "\n```", 0.001
        d = data_block(user)
        return json.dumps({
            "links": {},
            "relations": {"judged": {e["id"]: "supports" if e["stored_type"] == "supports" else "none"
                                     for e in d["stored_edges"]}, "added": []},
            "truth": {c["id"]: "true" for c in d["extracted_claims"]},
        }), 0.002


class ClaudeLabelTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        root = Path(self.tmp.name)
        brain = root / "brain.db"
        _brain_fixture.build(brain)
        self.out = root / "out"
        with contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(sample.main([str(brain), "--out", str(self.out), "--n", "24", "--kappa-n", "8"]), 0)
        self.sample = json.loads((self.out / "sample.json").read_text())
        self.kappa = [i for i in self.sample["items"] if i["kappa"]]

    def tearDown(self):
        self.tmp.cleanup()

    def run_labeller(self, fake, **kw):
        with mock.patch.object(claude_label.claude_cli, "complete", fake):
            return claude_label.run(self.out, as_of="2026-11-21", **kw)

    def write_pass1(self) -> Path:
        labels = {}
        for it in self.sample["items"]:
            first = it["episode"]["content"].split(". ", 1)[1].split(". ")[0]
            labels[it["item_id"]] = {
                "done": {"extraction": True, "relations": True, "truth": True},
                "extraction": {"gold": [first], "links": {}},
                "relations": {"edges": [{"a": r["from_claim_id"], "b": r["to_claim_id"], "type": "supports"}
                                        for r in it["relationships"] if r["type"] == "supports"]},
                "truth": {c["id"]: "true" for c in it["claims"]},
                "notes": PASS1_MARKER,
            }
        path = self.out / "labels" / "pass1-felix.json"
        privacy.write_json(path, {"format": "mnemos-phase-b-labels/1", "labeller": "felix", "pass": 1,
                                  "items": labels})
        return path

    def test_blind_never_opens_pass1_and_reads_only_sample_and_guide(self):
        p1 = self.write_pass1()
        opened: list[str] = []
        real_open = builtins.open

        def spy(file, mode="r", *a, **kw):
            if not isinstance(file, int) and "r" in mode and not any(c in mode for c in "wax+"):
                opened.append(str(Path(file).resolve()))
            return real_open(file, mode, *a, **kw)

        fake = FakeModel()
        with mock.patch("builtins.open", spy), mock.patch("io.open", spy):  # Path.read_text goes via io.open
            r = self.run_labeller(fake)
        self.assertEqual(r["labelled"], len(self.kappa))
        self.assertIn(str((self.out / "sample.json").resolve()), opened, "the spy sees reads")
        self.assertNotIn(str(p1.resolve()), opened)
        allowed = {str((self.out / "sample.json").resolve()), str(claude_label.GUIDE.resolve())}
        self.assertTrue(set(opened) <= allowed, f"unexpected reads: {set(opened) - allowed}")
        # Nothing of pass 1, and nothing the page hides, reaches the model. The
        # guide may name the pass-1 file convention (labels/pass1-<model>.json);
        # what must never appear is this pass 1's file or its contents.
        for messages in fake.prompts:
            text = json.dumps(messages)
            self.assertNotIn(PASS1_MARKER, text)
            self.assertNotIn(p1.name, text)
            for h in HIDDEN:
                self.assertNotIn(f'"{h}"', messages[1]["content"])

    def test_extraction_is_asked_before_any_claim_is_shown(self):
        fake = FakeModel()
        self.run_labeller(fake)
        claim_ids = {c["id"] for it in self.sample["items"] for c in it["claims"]}
        step1 = [m for m in fake.prompts if m[1]["content"].startswith("STEP 1")]
        step2 = [m for m in fake.prompts if m[1]["content"].startswith("STEP 2")]
        self.assertEqual(len(step1), len(self.kappa))
        for m in step1:
            self.assertFalse(any(cid in m[1]["content"] for cid in claim_ids))
        # Each item's step 2 comes after its step 1, in the page's item order.
        order = [m[1]["content"][:6] for m in fake.prompts]
        self.assertEqual(order, ["STEP 1", "STEP 2"] * len(self.kappa))
        self.assertIn(claude_label.GUIDE.read_text()[:200], fake.prompts[0][0]["content"])

    def test_output_has_the_page_format_and_score_computes_kappa(self):
        self.write_pass1()
        r = self.run_labeller(FakeModel())
        path = self.out / "labels" / "pass2-claude-haiku-5-5.json"
        self.assertEqual(r["labels"], str(path.resolve()))
        data = json.loads(path.read_text())
        self.assertEqual((data["labeller"], data["pass"], data["format"]),
                         ("claude-haiku-5-5", 2, "mnemos-phase-b-labels/1"))
        self.assertEqual(set(data["items"]), {i["item_id"] for i in self.kappa})
        self.assertEqual(stat.S_IMODE(os.stat(path).st_mode), 0o600)
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf), contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(score.main(["--dir", str(self.out), "--split", "all"]), 0)
        rep = json.loads(buf.getvalue())
        self.assertEqual(rep["labels"]["pass2"], "pass2-claude-haiku-5-5.json")
        a = rep["agreement"]
        self.assertEqual(a["items"], len(self.kappa))
        self.assertEqual(a["truth"]["observed_agreement"], 1.0)
        self.assertEqual(a["relations"]["observed_agreement"], 1.0)
        self.assertIsNotNone(a["extraction_matched"]["kappa"])

    def test_invalid_responses_are_retried_then_recorded_not_invented(self):
        r = self.run_labeller(FakeModel(bad_first=1))
        self.assertEqual((r["labelled"], r["failed"]), (len(self.kappa), 0))  # one bad answer, then valid
        other = self.out / "fresh"
        # A fresh sample dir in which the model never answers validly.
        with contextlib.redirect_stdout(io.StringIO()):
            sample.main([str(Path(self.tmp.name) / "brain.db"), "--out", str(other), "--n", "6", "--kappa-n", "2"])
        fake = FakeModel(always_bad=True)
        with mock.patch.object(claude_label.claude_cli, "complete", fake):
            r = claude_label.run(other, as_of="2026-11-21", retries=2)
        self.assertEqual((r["labelled"], r["failed"]), (0, 2))
        self.assertEqual(len(fake.prompts), 2 * 3)  # step 1 only, 1 + 2 retries each
        self.assertIn("That response was invalid", fake.prompts[1][-1]["content"])
        items = json.loads((other / "labels" / "pass2-claude-haiku-5-5.json").read_text())["items"]
        for lab in items.values():
            self.assertEqual(lab["done"], {})
            self.assertIn("FAILED", lab["notes"])
            self.assertNotIn("truth", lab)

    def test_validation_rules(self):
        it = next(i for i in self.kappa if i["claims"] and i["relationships"])
        claims = [c["id"] for c in it["claims"]]
        edges = {r["id"]: "none" for r in it["relationships"]}
        ok = {"links": {}, "relations": {"judged": edges, "added": []}, "truth": {c: "true" for c in claims}}
        claude_label.validate_step2(ok, it, 1)
        bad = [
            {**ok, "truth": {}},
            {**ok, "relations": {"judged": {}, "added": []}},
            {**ok, "relations": {"judged": {k: "maybe" for k in edges}, "added": []}},
            {**ok, "links": {claims[0]: 5}},
            {**ok, "links": {"nope": 0}},
            {**ok, "relations": {"judged": edges, "added": [{"a": claims[0], "b": "ghost", "type": "supports"}]}},
            {**ok, "extra": 1},
        ]
        for b in bad:
            with self.assertRaises(ValueError):
                claude_label.validate_step2(b, it, 1)
        with self.assertRaises(ValueError):
            claude_label.validate_step1({"gold": ["two\nlines"]})
        with self.assertRaises(ValueError):
            claude_label.validate_step1({"gold": "not a list"})
        self.assertEqual(claude_label.validate_step1({"gold": [" a claim "]}), ["a claim"])

    def test_cached_rerun_is_free_and_identical(self):
        first = FakeModel()
        r1 = self.run_labeller(first)
        path = self.out / "labels" / "pass2-claude-haiku-5-5.json"
        before = json.loads(path.read_text())["items"]
        path.unlink()  # relabel from scratch: every request should now hit the cache
        second = FakeModel()
        r2 = self.run_labeller(second)
        self.assertEqual(len(second.prompts), 0)
        self.assertEqual((r2["calls"], r2["cache_hits"]), (0, r1["calls"]))
        after = json.loads(path.read_text())["items"]
        strip = lambda items: {k: {kk: vv for kk, vv in v.items() if kk != "updated_at"}  # noqa: E731
                               for k, v in items.items()}
        self.assertEqual(strip(before), strip(after))
        cache = list((self.out / "labels" / "cache-claude-haiku-5-5").glob("*.json"))
        self.assertEqual(len(cache), r1["calls"])
        self.assertEqual(stat.S_IMODE(os.stat(cache[0]).st_mode), 0o600)
        # A finished item is skipped outright on the next run.
        self.assertEqual(self.run_labeller(FakeModel())["already_done"], len(self.kappa))

    def test_model_override_names_the_labeller(self):
        r = self.run_labeller(FakeModel(), model="claude-sonnet-5.1")
        self.assertTrue(r["labels"].endswith("pass2-claude-sonnet-5-1.json"))

    def test_pass1_labels_every_item(self):
        r = self.run_labeller(FakeModel(), model="claude-sonnet-5.5", pass_no=1)
        self.assertTrue(r["labels"].endswith("pass1-claude-sonnet-5-5.json"))
        self.assertEqual((r["labelled"], r["failed"]), (len(self.sample["items"]), 0))
        self.assertGreater(len(self.sample["items"]), len(self.kappa))
        data = json.loads(Path(r["labels"]).read_text())
        self.assertEqual(data["pass"], 1)
        self.assertEqual(set(data["items"]), {i["item_id"] for i in self.sample["items"]})

    def test_rejects_an_unknown_pass(self):
        with self.assertRaises(ValueError):
            self.run_labeller(FakeModel(), pass_no=3)

    def test_refuses_output_inside_a_git_worktree(self):
        with self.assertRaises(privacy.UnsafeOutputDir):
            claude_label.run(privacy.REPO_ROOT / "benchmarks" / "phase_b" / "out")


if __name__ == "__main__":
    unittest.main()
