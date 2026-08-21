from __future__ import annotations

from pathlib import Path
import json
import tempfile
import unittest
from types import SimpleNamespace

import verify


class ProductAdapterTests(unittest.TestCase):
    def test_codex_events_preserve_ownward_calls_and_expose_bypasses(self) -> None:
        events = [
            {"type": "thread.started", "thread_id": "session"},
            {"type": "item.completed", "item": {
                "type": "mcp_tool_call", "server": "ownward", "tool": "ownward__ownward_search",
                "status": "completed", "error": None, "arguments": {"query": "x"},
                "result": {"structured_content": {"results": []}},
            }},
            {"type": "item.completed", "item": {"type": "command_execution"}},
        ]
        trace = verify.codex_session.load_exec_events("\n".join(json.dumps(item) for item in events))
        self.assertTrue(trace.bypassed)
        self.assertEqual("ownward_search", trace.calls[0].name)
        self.assertFalse(trace.calls[0].error)

    def test_resource_evidence_is_bound_to_candidate(self) -> None:
        report = {
            "schema": "ownward.delivery-resource-report/v1",
            "candidate": "candidate",
            "release_binary_sha256": "a" * 64,
            "passed": True,
            "checks": [
                {"name": "working-resources", "actual_peak_rss_mib": 120.0},
                {"name": "embedding-throughput", "query_maximum_ms": 600.0},
            ],
        }
        self.assertEqual((120.0, True, 600.0), verify._resource_values(report, "candidate", "a" * 64))
        with self.assertRaisesRegex(RuntimeError, "another candidate"):
            verify._resource_values(report, "other", "a" * 64)

    def test_partial_scenario_cleanup_cannot_escape_evidence_root(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            scenario = root / "scenario"
            scenario.mkdir()
            (scenario / "partial").write_text("x", encoding="utf-8")
            verify._safe_reset(scenario, root)
            self.assertFalse(scenario.exists())
            outside = root / "outside"
            outside.mkdir()
            with self.assertRaisesRegex(RuntimeError, "unexpected"):
                verify._safe_reset(outside, root / "expected")

    def test_navigation_evidence_only_uses_successful_navigation_calls(self) -> None:
        events = [
            {"type": "thread.started", "thread_id": "session"},
            {"type": "item.completed", "item": {
                "type": "mcp_tool_call", "server": "ownward", "tool": "ownward_navigate",
                "status": "completed", "error": None, "arguments": {},
                "result": {"structured_content": {"items": [{"id": "from-navigation"}]}},
            }},
            {"type": "item.completed", "item": {
                "type": "mcp_tool_call", "server": "ownward", "tool": "ownward_search",
                "status": "completed", "error": None, "arguments": {},
                "result": {"structured_content": {"results": [{"id": "from-search"}]}},
            }},
        ]
        trace = verify.codex_session.load_exec_events("\n".join(json.dumps(item) for item in events))
        observed = verify._observed(trace, {"ownward_navigate"})
        self.assertEqual({"from-navigation"}, set(observed))

    def test_scenario_checkpoint_requires_all_unchanged_raw_traces(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            files = verify._scenario_evidence_files(True)
            for relative in files:
                path = root / relative
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(relative, encoding="utf-8")
            binding = {"candidate": "candidate"}
            sealed = {
                "schema": "ownward.product-scenario-checkpoint/v1",
                "binding": binding,
                "evidence": verify._scenario_evidence(root, True),
                "result": {"passed": True},
            }
            self.assertTrue(verify._sealed_scenario_valid(sealed, root, binding, True))
            (root / files[0]).write_text("changed", encoding="utf-8")
            self.assertFalse(verify._sealed_scenario_valid(sealed, root, binding, True))

    def test_valid_qualification_scenario_is_reused_by_full_without_resume_flag(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            evidence_root = root / "scenarios"
            scenario_root = evidence_root / "scenario-1"
            for relative in verify._scenario_evidence_files(False):
                path = scenario_root / relative
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text(relative, encoding="utf-8")
            codex = root / "codex.exe"
            codex.write_bytes(b"codex")
            args = SimpleNamespace(
                evidence_dir=evidence_root,
                codex_binary=codex,
                codex_model="model",
                codex_reasoning_effort="effort",
                resume=False,
            )
            task = {"scenario_id": "scenario-1", "updates": [], "information": [], "query": {}}
            binding = {
                "suite_version": "1.0.0", "candidate": "candidate", "binary_sha256": "a" * 64,
                "environment_sha256": "b" * 64, "input_manifest_sha256": "c" * 64,
                "tool_sha256": "d" * 64,
            }
            expected_binding = verify._scenario_binding(args, task, binding, "e" * 64)
            result = {"scenario_id": "scenario-1", "passed": True}
            verify.write_json(scenario_root / "result.json", {
                "schema": "ownward.product-scenario-checkpoint/v1",
                "binding": expected_binding,
                "evidence": verify._scenario_evidence(scenario_root, False),
                "result": result,
            })
            actual = verify._run_scenario(args, task, binding, 0, True, 1, "e" * 64, 0)
            self.assertEqual(result, actual)


if __name__ == "__main__":
    unittest.main()
