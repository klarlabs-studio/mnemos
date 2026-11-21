"""Tests for the headless-Claude backend (#441, deviation 7).

Run with: python3 -m unittest benchmarks.phase_a.test_claude_cli

The isolation flags are pinned here because dropping one is silent and
harmful: without disableAllHooks, the operator's Mnemos SessionEnd hook would
capture evaluation prompts into their real brain.
"""

from __future__ import annotations

import json
import os
import tempfile
import unittest
from unittest import mock

from benchmarks.phase_a import claude_cli
from benchmarks.phase_a.llm import CLAUDE_CLI, Client, Model


class CommandIsolation(unittest.TestCase):
    def test_every_call_is_isolated_from_the_operator_environment(self):
        argv = claude_cli.command("claude-haiku-5-5", "sys")
        flags = dict(zip(argv, argv[1:]))
        self.assertEqual(flags["--setting-sources"], "")
        self.assertEqual(json.loads(flags["--settings"]), {"disableAllHooks": True})
        self.assertIn("--strict-mcp-config", argv)
        self.assertEqual(flags["--tools"], "")
        self.assertIn("--no-session-persistence", argv)
        self.assertEqual(flags["--model"], "claude-haiku-5-5")
        self.assertEqual(flags["--output-format"], "json")

    def test_subprocess_runs_outside_any_project_with_a_memory_store(self):
        seen = {}

        def fake_run(argv, **kw):
            seen.update(kw)
            return mock.Mock(stdout=json.dumps({"result": "ok", "is_error": False, "total_cost_usd": 0.01}),
                             stderr="", returncode=0)

        with mock.patch("subprocess.run", fake_run):
            text, cost = claude_cli.complete("m", [{"role": "user", "content": "hi"}])
        self.assertEqual((text, cost), ("ok", 0.01))
        self.assertEqual(seen["env"]["MNEMOS_DB_URL"], "memory://")
        self.assertEqual(seen["cwd"], claude_cli._WORKDIR)
        self.assertEqual(seen["input"], "hi")

    def test_an_error_response_is_never_returned_as_an_answer(self):
        bad = mock.Mock(stdout=json.dumps({"result": "Not logged in", "is_error": True}), stderr="", returncode=1)
        with mock.patch("subprocess.run", return_value=bad), mock.patch("time.sleep"):
            with self.assertRaises(RuntimeError):
                claude_cli.complete("m", [{"role": "user", "content": "hi"}])


class Render(unittest.TestCase):
    def test_system_and_single_user_message(self):
        self.assertEqual(
            claude_cli.render([{"role": "system", "content": "S"}, {"role": "user", "content": "U"}]), ("S", "U"))

    def test_longer_exchange_becomes_a_transcript(self):
        _, prompt = claude_cli.render([
            {"role": "user", "content": "a"}, {"role": "assistant", "content": "b"}, {"role": "user", "content": "c"}])
        self.assertEqual(prompt, "User: a\n\nAssistant: b\n\nUser: c")


class Cache(unittest.TestCase):
    def test_second_identical_call_is_served_from_cache(self):
        calls = []

        def fake_complete(model, messages, timeout=300):
            calls.append(model)
            return "CORRECT", 0.002

        path = os.path.join(tempfile.mkdtemp(), "cache.sqlite")
        c = Client(path, budget_usd=1.0)  # no OpenRouter key needed for claude-cli
        m = Model("claude-haiku-5-5", CLAUDE_CLI)
        msgs = [{"role": "user", "content": "q"}]
        with mock.patch.object(claude_cli, "complete", fake_complete):
            self.assertEqual(c.complete(m, msgs), "CORRECT")
            self.assertEqual(c.complete(m, msgs), "CORRECT")
        self.assertEqual(calls, ["claude-haiku-5-5"])
        self.assertAlmostEqual(c.spent, 0.002)


if __name__ == "__main__":
    unittest.main()
