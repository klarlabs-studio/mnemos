"""Tests for the Phase B labelling server."""

from __future__ import annotations

import contextlib
import http.client
import io
import json
import os
import stat
import sys
import tempfile
import threading
import unittest
from http.server import HTTPServer
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[2]))

from benchmarks.phase_b import _brain_fixture, label, sample  # noqa: E402


class LabelTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        root = Path(self.tmp.name)
        brain = root / "brain.db"
        _brain_fixture.build(brain)
        self.out = root / "out"
        with contextlib.redirect_stdout(io.StringIO()):
            self.assertEqual(sample.main([str(brain), "--out", str(self.out), "--n", "12", "--kappa-n", "4"]), 0)
        self.items = json.loads((self.out / "sample.json").read_text())["items"]

    def tearDown(self):
        self.tmp.cleanup()

    def test_pass_one_sees_every_item_and_no_anchoring_fields(self):
        s = label.Session(self.out, "felix", 1)
        v = s.view()
        self.assertEqual(len(v["items"]), 12)
        for it in v["items"]:
            self.assertNotIn("split", it)
            for c in it["claims"] + it["neighbour_claims"]:
                for hidden in ("trust_score", "status", "valid_to", "confidence"):
                    self.assertNotIn(hidden, c)

    def test_saves_owner_only_with_labeller_and_pass(self):
        s = label.Session(self.out, "felix", 1)
        iid = self.items[0]["item_id"]
        s.save(iid, {"done": {"truth": True}, "truth": {"x": "true"}, "junk": 1})
        data = json.loads(s.path.read_text())
        self.assertEqual((data["labeller"], data["pass"]), ("felix", 1))
        self.assertEqual(data["items"][iid]["truth"], {"x": "true"})
        self.assertNotIn("junk", data["items"][iid])
        self.assertEqual(stat.S_IMODE(os.stat(s.path).st_mode), 0o600)
        self.assertEqual(label.Session(self.out, "felix", 1).data["items"][iid]["done"], {"truth": True})
        with self.assertRaises(KeyError):
            s.save("not/an-item", {})

    def test_pass_two_is_kappa_only_separate_and_another_person(self):
        label.Session(self.out, "felix", 1).save(self.items[0]["item_id"], {"notes": "p1"})
        with self.assertRaises(ValueError):
            label.Session(self.out, "felix", 2)
        s2 = label.Session(self.out, "anna", 2)
        self.assertEqual(len(s2.items), 4)
        self.assertTrue(all(i["kappa"] for i in s2.items))
        self.assertEqual(s2.view()["labels"], {})  # pass-1 labels never reach pass 2
        self.assertEqual(s2.path.name, "pass2-anna.json")
        self.assertTrue(label.Session(self.out, "felix", 2, allow_same=True))

    def test_refuses_labels_made_against_another_sample(self):
        s = label.Session(self.out, "felix", 1)
        s.save(self.items[0]["item_id"], {})
        data = json.loads(s.path.read_text())
        data["sample_sha256"] = "0" * 64
        s.path.write_text(json.dumps(data))
        with self.assertRaises(ValueError):
            label.Session(self.out, "felix", 1)

    def test_rejects_bad_labeller_ids(self):
        with self.assertRaises(ValueError):
            label.Session(self.out, "../x", 1)

    def test_http_endpoints_and_loopback_guards(self):
        s = label.Session(self.out, "felix", 1)
        server = HTTPServer(("127.0.0.1", 0), None)
        port = server.server_port
        server.RequestHandlerClass = label.handler(s, port)
        t = threading.Thread(target=server.serve_forever, daemon=True)
        t.start()
        try:
            def req(method, path, body=None, headers=None):
                c = http.client.HTTPConnection("127.0.0.1", port, timeout=5)
                h = {"Host": f"127.0.0.1:{port}"}
                h.update(headers or {})
                c.request(method, path, body=body, headers=h)
                r = c.getresponse()
                data = r.read()
                c.close()
                return r.status, data

            status, page = req("GET", "/")
            self.assertEqual(status, 200)
            self.assertIn(b"Phase B labelling", page)
            status, body = req("GET", "/api/session")
            self.assertEqual((status, json.loads(body)["labeller"]), (200, "felix"))
            self.assertEqual(req("GET", "/api/session", headers={"Host": "evil.example:80"})[0], 403)
            iid = self.items[0]["item_id"]
            payload = json.dumps({"item_id": iid, "label": {"notes": "n"}})
            self.assertEqual(req("POST", "/api/label", payload, {"Content-Type": "text/plain"})[0], 415)
            self.assertEqual(req("POST", "/api/label", payload, {"Content-Type": "application/json",
                                                                 "Origin": "https://evil.example"})[0], 403)
            status, _ = req("POST", "/api/label", payload, {"Content-Type": "application/json"})
            self.assertEqual(status, 200)
            self.assertEqual(json.loads(s.path.read_text())["items"][iid]["notes"], "n")
            withc = next(i for i in self.items if i["claims"])
            status, body = req("POST", "/api/match", json.dumps(
                {"item_id": withc["item_id"], "gold": [withc["claims"][0]["text"]], "links": {}}),
                {"Content-Type": "application/json"})
            self.assertEqual(json.loads(body)["pairs"], {withc["claims"][0]["id"]: 0})
        finally:
            server.shutdown()
            server.server_close()


if __name__ == "__main__":
    unittest.main()
