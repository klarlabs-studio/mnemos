"""Tests for the Phase B scorer."""

from __future__ import annotations

import contextlib
import io
import json
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))

from benchmarks.phase_a import stats  # noqa: E402
from benchmarks.phase_b import _brain_fixture, privacy, sample, score  # noqa: E402


def claim(cid, text, **kw):
    return {"id": cid, "text": text, **kw}


class MatchingTest(unittest.TestCase):
    def test_normalise_keeps_identifiers_and_drops_stopwords(self):
        self.assertEqual(score.normalise("The CLI uses Go 1.22 and v0.85.0 in foo_bar"),
                         ["cli", "use", "go", "1.22", "v0.85.0", "foo_bar"])

    def test_similarity(self):
        self.assertEqual(score.similarity("Postgres is the store", "the store is Postgres"), 1.0)
        self.assertEqual(score.similarity("", "x"), 0.0)
        self.assertAlmostEqual(score.similarity("alpha beta", "alpha gamma"), 0.5)

    def test_greedy_one_to_one(self):
        gold = ["The API rate limit is 100 requests per minute", "Deploys run on merge to main"]
        pred = [claim("c1", "API rate limit is 100 requests per minute"),
                claim("c2", "The API rate limit is 100 per minute"),  # a duplicate: cannot take the same gold
                claim("c3", "Deploys happen on merge to main"),
                claim("c4", "Something unrelated entirely")]
        m = score.match_claims(gold, pred)
        self.assertEqual(m["pairs"], {"c1": 0, "c3": 1})
        self.assertEqual((m["matched"], m["gold"], m["predicted"]), (2, 2, 4))

    def test_manual_links_override(self):
        gold = ["Staging uses the eu-west region"]
        pred = [claim("c1", "The staging environment is hosted in Ireland"), claim("c2", "Staging uses eu-west region")]
        self.assertEqual(score.match_claims(gold, pred)["pairs"], {"c2": 0})
        m = score.match_claims(gold, pred, {"c1": 0})
        self.assertEqual(m["pairs"], {"c1": 0})  # c2 finds its gold taken
        self.assertEqual(score.match_claims(gold, pred, {"c2": None})["pairs"], {})
        self.assertEqual(score.match_claims(gold, pred, {"c2": None})["manual"], 1)

    def test_live_filter(self):
        self.assertTrue(score.is_live(claim("a", "x", status="active")))
        self.assertFalse(score.is_live(claim("a", "x", status="deprecated")))
        self.assertFalse(score.is_live(claim("a", "x", status="active", merged_into="b")))


def item(**kw):
    base = {"item_id": "b/e1", "split": "dev", "kappa": False, "episode": {"id": "e1"},
            "claims": [claim("a", "A"), claim("b", "B"), claim("c", "C", valid_to="2026-01-01T00:00:00Z")],
            "neighbour_claims": [claim("n", "N")], "relationships": []}
    base.update(kw)
    return base


def rel(rid, kind, a, b):
    return {"id": rid, "type": kind, "from_claim_id": a, "to_claim_id": b}


class RelationsTest(unittest.TestCase):
    def test_contradiction_with_one_invalidated_end_reads_as_supersedes(self):
        it = item(relationships=[rel("r1", "contradicts", "c", "a"), rel("r2", "contradicts", "a", "b"),
                                 rel("r3", "supports", "b", "n"), rel("r4", "causes", "a", "b")])
        self.assertEqual(score.predicted_edges(it), {("supersedes", "a", "c"), ("contradicts", "a", "b"),
                                                     ("supports", "b", "n")})

    def test_precision_recall_by_type(self):
        it = item(relationships=[rel("r1", "contradicts", "c", "a"), rel("r2", "supports", "a", "b"),
                                 rel("r3", "supports", "b", "n")])
        label = {"relations": {"edges": [
            {"a": "a", "b": "c", "type": "supersedes"},   # agrees, direction included
            {"a": "b", "b": "a", "type": "contradicts"},  # brain said supports
            {"a": "a", "b": "zzz", "type": "supports"},   # not displayed: invalid
        ]}}
        gold, invalid = score.gold_edges(it, label)
        self.assertEqual(invalid, 1)
        r = score.relation_scores([(score.predicted_edges(it), gold)])
        self.assertEqual(r["supersedes"], {"tp": 1, "fp": 0, "fn": 0, "precision": 1.0, "recall": 1.0})
        self.assertEqual((r["supports"]["tp"], r["supports"]["fp"], r["supports"]["fn"]), (0, 2, 0))
        self.assertEqual((r["contradicts"]["tp"], r["contradicts"]["fn"]), (0, 1))
        self.assertEqual(r["any"]["tp"], 2)  # the pair a-b is related either way
        inside = score.relation_scores([(score.within(it, score.predicted_edges(it)), score.within(it, gold))])
        self.assertEqual(inside["supports"]["fp"], 1)  # b-n leaves the episode

    def test_wrong_supersedes_direction_is_an_error(self):
        it = item(relationships=[rel("r1", "contradicts", "c", "a")])
        gold, _ = score.gold_edges(it, {"relations": {"edges": [{"a": "c", "b": "a", "type": "supersedes"}]}})
        r = score.relation_scores([(score.predicted_edges(it), gold)])
        self.assertEqual((r["supersedes"]["tp"], r["supersedes"]["fp"], r["supersedes"]["fn"]), (0, 1, 1))


class CalibrationTest(unittest.TestCase):
    def test_brier_skill_and_ece(self):
        pts = [(0.9, True)] * 9 + [(0.9, False)] + [(0.1, False)] * 9 + [(0.1, True)]
        c = score.calibration(pts)
        self.assertAlmostEqual(c["brier"], 0.09)
        self.assertAlmostEqual(c["base_rate"], 0.5)
        self.assertAlmostEqual(c["brier_skill"], 1 - 0.09 / 0.25)
        self.assertAlmostEqual(c["ece"], 0.0)
        self.assertEqual(sum(b["n"] for b in c["reliability"]), 20)
        self.assertEqual(c["reliability"][9]["fraction_true"], 0.9)

    def test_overconfident_and_edge_cases(self):
        c = score.calibration([(1.0, False), (1.0, True)])
        self.assertAlmostEqual(c["ece"], 0.5)
        self.assertEqual(c["reliability"][9]["n"], 2)  # 1.0 lands in the top bin
        self.assertIsNone(score.calibration([(0.7, True)])["brier_skill"])  # base rate 1: no reference
        self.assertEqual(score.calibration([])["n"], 0)
        self.assertEqual(score.calibration([(1.2, True)])["trust_out_of_range"], 1)


class KappaTest(unittest.TestCase):
    def test_categorical_matches_binary_on_two_categories(self):
        x = [True, True, False, False, True, False, True, True]
        y = [True, False, False, False, True, True, True, True]
        self.assertAlmostEqual(score.categorical_kappa(x, y), stats.cohen_kappa(x, y))
        self.assertEqual(score.categorical_kappa(["a", "b"], ["a", "b"]), 1.0)
        self.assertIsNone(score.categorical_kappa([], []))


class AnsweringHookTest(unittest.TestCase):
    def test_invalidated_memory_counts_wrong(self):
        qs = [{"id": "q1", "type": "knowledge-update"}, {"id": "q2", "type": "temporal"},
              {"id": "q3", "type": "temporal"}]
        v = [{"question_id": "q1", "system": "mnemos", "correct": True, "used_invalidated": True},
             {"question_id": "q2", "system": "mnemos", "correct": True},
             {"question_id": "q3", "system": "mnemos", "correct": False},
             {"question_id": "q1", "system": "bm25", "correct": True},
             {"question_id": "q2", "system": "bm25", "correct": True}]
        r = score.score_answers(qs, v)
        self.assertEqual(r["paired_questions"], 2)  # q3 has no bm25 verdict
        self.assertEqual(r["systems"]["mnemos"]["accuracy"], 0.5)
        self.assertEqual(r["systems"]["mnemos"]["used_invalidated_rate"], 0.5)
        self.assertEqual(r["systems"]["bm25"]["accuracy"], 1.0)
        self.assertIn("bm25 - mnemos", r["comparisons"])


class ReportTest(unittest.TestCase):
    """End to end: sample a synthetic brain, write labels, score them."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        root = Path(self.tmp.name)
        brain = root / "brain.db"
        _brain_fixture.build(brain)
        self.out = root / "out"
        with contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(sample.main([str(brain), "--out", str(self.out), "--n", "40", "--kappa-n", "10"]), 0)
        self.sample = json.loads((self.out / "sample.json").read_text())

    def tearDown(self):
        self.tmp.cleanup()

    def labels(self, flip_truth: bool = False) -> dict:
        out = {}
        for it in self.sample["items"]:
            gold = [c["text"] for c in it["claims"]] + ["A claim the extractor missed entirely"]
            out[it["item_id"]] = {
                "done": {"extraction": True, "relations": True, "truth": True, "questions": True},
                "extraction": {"gold": gold, "links": {}},
                "relations": {"edges": [{"a": r["from_claim_id"], "b": r["to_claim_id"], "type": r["type"]}
                                        for r in it["relationships"] if r["type"] == "supports"]},
                "truth": {c["id"]: ("false" if flip_truth else "true") if i == 0 else "unknown"
                          for i, c in enumerate(it["claims"])},
                "questions": [{"question": "Where is the build cache?", "answer": "cache1",
                               "type": "single-episode", "as_of": "2026-11-20"}],
            }
        return out

    def test_report_scores_each_stage_on_dev_only_by_default(self):
        labels = self.labels()
        r = score.report(self.sample, labels, "dev")
        dev = [i for i in self.sample["items"] if i["split"] == "dev"]
        self.assertEqual(r["items"], len(dev))
        ext = r["extraction"]
        self.assertEqual(ext["precision"]["value"], 1.0)
        n_claims = sum(len(i["claims"]) for i in dev)
        self.assertAlmostEqual(ext["recall"]["value"], n_claims / (n_claims + len(dev)))
        self.assertLessEqual(ext["recall"]["ci_low"], ext["recall"]["value"])
        self.assertEqual(r["relations"]["within_episode"]["supports"]["precision"], 1.0)
        self.assertEqual(r["trust"]["n"], sum(1 for i in dev if i["claims"]))
        self.assertEqual(r["questions"]["by_type"]["single-episode"], len(dev))
        held = score.report(self.sample, labels, "heldout")
        self.assertEqual(held["items"] + r["items"], len(self.sample["items"]))

    def test_agreement_on_the_kappa_subset(self):
        r = score.report(self.sample, self.labels(), "all", second=self.labels())
        a = r["agreement"]
        self.assertEqual(a["items"], 10)
        self.assertEqual(a["extraction_matched"]["kappa"], 1.0)
        self.assertEqual(a["relations"]["observed_agreement"], 1.0)
        r2 = score.report(self.sample, self.labels(), "all", second=self.labels(flip_truth=True))
        self.assertEqual(r2["agreement"]["truth_binary"]["observed_agreement"], 0.0)

    def test_exported_questions_have_the_phase_a_shape(self):
        qs = score.export_questions(self.sample["items"], self.labels())
        self.assertEqual(len(qs), len(self.sample["items"]))
        self.assertEqual(set(qs[0]) >= {"id", "text", "answer", "type", "date", "evidence_sessions", "abstention"},
                         True)
        self.assertEqual(qs[0]["date"], "2026-11-20")

    def test_cli_reads_label_files_and_refuses_in_repo_export(self):
        labels_dir = self.out / "labels"
        privacy.write_json(labels_dir / "pass1-felix.json", {"labeller": "felix", "pass": 1, "items": self.labels()})
        buf = io.StringIO()
        with contextlib.redirect_stdout(buf), contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(score.main(["--dir", str(self.out)]), 0)
        self.assertEqual(json.loads(buf.getvalue())["split"], "dev")
        dest = privacy.REPO_ROOT / "benchmarks" / "phase_b" / "questions.jsonl"
        with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            self.assertEqual(score.main(["--dir", str(self.out), "--export-questions", str(dest)]), 2)
        self.assertFalse(dest.exists())
        ok = self.out / "questions.jsonl"
        with contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(score.main(["--dir", str(self.out), "--export-questions", str(ok)]), 0)
        self.assertTrue(ok.read_text().strip())


if __name__ == "__main__":
    unittest.main()
