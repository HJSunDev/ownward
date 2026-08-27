from __future__ import annotations

import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock

from adapters.product import replay
from adapters.product import verify


class ProductReplayTests(unittest.TestCase):
    def _manifests(self, root: Path, *, changed_raw_path: str | None = None) -> tuple[str, str]:
        raw = {
            "benchmarks/acceptance/suite/adapters/product/verify.py": "9" * 64,
            "benchmarks/acceptance/suite/adapters/product/codex_transport.py": "1" * 64,
            "benchmarks/acceptance/suite/adapters/product_resource/verify.py": "2" * 64,
            "benchmarks/support/ownward_mcp.py": "8" * 64,
            "benchmarks/acceptance/suite/execution_product.py": "7" * 64,
            "benchmarks/acceptance/suite/product.py": "3" * 64,
        }
        current_raw = {**raw}
        if changed_raw_path is not None:
            current_raw[changed_raw_path] = "f" * 64
        old = self._v5_manifest(raw, {"benchmarks/acceptance/suite/adapters/product/codex_session.py": "6" * 64}, "a" * 40)
        current = self._v5_manifest(current_raw, {"benchmarks/acceptance/suite/adapters/product/codex_session.py": "4" * 64}, "b" * 40)
        old_path = root / "generations" / "old" / "product-tools.json"
        current_path = root / "generations" / "current" / "product-tools.json"
        verify.write_json(old_path, old)
        verify.write_json(current_path, current)
        return verify.sha256(old_path), verify.sha256(current_path)

    def _v5_manifest(
        self, raw: dict[str, str], derivation: dict[str, str], commit: str,
    ) -> dict[str, object]:
        raw_files = [{"path": path, "sha256": digest} for path, digest in sorted(raw.items())]
        derivation_files = [{"path": path, "sha256": digest} for path, digest in sorted(derivation.items())]
        raw_identity = {
            "schema": "ownward.product-raw-execution-identity/v1",
            "files": raw_files,
        }
        raw_identity["sha256"] = verify.json_sha256(raw_identity)
        derivation_identity = {
            "schema": "ownward.product-derivation-identity/v1",
            "files": derivation_files,
        }
        derivation_identity["sha256"] = verify.json_sha256(derivation_identity)
        return {
            "schema": "ownward.acceptance-tool-manifest/v5",
            "scope": "product",
            "repository_commit": commit,
            "files": sorted(raw_files + derivation_files, key=lambda value: value["path"]),
            "responsibilities": {
                "raw_execution": raw_identity,
                "derivation": derivation_identity,
            },
            "legacy_derivation_replay": [],
        }

    def _legacy_manifests(self, root: Path, *, declared_source_files: bool = True) -> tuple[str, str]:
        old = {
            "schema": "ownward.acceptance-tool-manifest/v4",
            "scope": "product",
            "repository_commit": "a" * 40,
            "files": [
                {"path": "legacy.py", "sha256": "1" * 64},
                {"path": "benchmarks/acceptance/suite/adapters/product/codex_session.py", "sha256": "4" * 64},
            ],
        }
        old_path = root / "generations" / "old" / "product-tools.json"
        verify.write_json(old_path, old)
        old_tool = verify.sha256(old_path)
        current = self._v5_manifest(
            {"raw.py": "2" * 64}, {"parser.py": "3" * 64}, "b" * 40,
        )
        current["legacy_derivation_replay"] = [{
            "migration_id": "fixture-migration",
            "source_tool_sha256": old_tool,
            "source_files_sha256": verify.json_sha256(old["files"]) if declared_source_files else "f" * 64,
            "source_parser_sha256": "4" * 64,
            "target_raw_execution_sha256": current["responsibilities"]["raw_execution"]["sha256"],
            "target_derivation_sha256": current["responsibilities"]["derivation"]["sha256"],
            "proof": "fixture exact migration",
        }]
        current_path = root / "generations" / "current" / "product-tools.json"
        verify.write_json(current_path, current)
        return old_tool, verify.sha256(current_path)

    def _sealed_scenario(
        self,
        root: Path,
        task: dict[str, object],
        binding: dict[str, str],
        args: SimpleNamespace,
        resource_sha: str,
    ) -> tuple[dict[str, object], bytes]:
        scenario = root / str(task["scenario_id"])
        data = scenario / "data"
        data.mkdir(parents=True)
        (data / "state.json").write_text("state", encoding="utf-8")
        expected = verify._scenario_binding(args, task, binding, resource_sha)
        stable, work_id, fact = "stable-id", "work-id", "stored fact"
        submission = {
            "schema": "ownward.semantic-submission/v1",
            "work_id": work_id,
            "asset_id": stable,
            "asset_revision": 1,
        }
        semantic_events = [
            {"type": "thread.started", "thread_id": "semantic-session"},
            {"type": "item.completed", "item": {
                "type": "mcp_tool_call", "server": "codex", "tool": "list_mcp_resources",
                "status": "completed", "error": None, "arguments": {},
                "result": {"content": [{"type": "text", "text": '{"resources":[]}'}]},
            }},
            {"type": "item.completed", "item": {
                "type": "mcp_tool_call", "server": "ownward", "tool": "ownward_semantic_work",
                "status": "completed", "error": None, "arguments": {"asset_ids": [stable]},
                "result": {"structured_content": {"work": [{"id": work_id, "asset": {"id": stable, "revision": 1}}]}},
            }},
            {"type": "item.completed", "item": {
                "type": "mcp_tool_call", "server": "ownward", "tool": "ownward_semantic_submit",
                "status": "completed", "error": None, "arguments": {"submission": submission},
                "result": {"structured_content": {"accepted": True}},
            }},
        ]
        unit = "initial:n1"
        semantic = scenario / "semantic-initial" / verify.json_sha256(unit)[:16] / "attempt-001"
        semantic.mkdir(parents=True)
        raw_events = "\n".join(json.dumps(item) for item in semantic_events).encode()
        (semantic / "events.jsonl").write_bytes(raw_events)
        verify.write_json(semantic / "output.json", {"processed": 1, "uncertain": 0})
        (semantic / "stderr.txt").write_text("", encoding="utf-8")
        semantic_evidence = verify._relative_evidence(scenario, semantic)
        progress = {
            "schema": "ownward.product-scenario-progress/v1",
            "binding": expected,
            "stable_by_node": {"n1": stable},
            "revisions": {"n1": 1},
            "completed_units": {unit: {
                "asset_id": stable,
                "revision": 1,
                "elapsed_seconds": 1.0,
                "rollback_seconds": 0.1,
                "work_id": work_id,
                "submission_sha256": verify.json_sha256(submission),
                "protocol_operations": [],
                "terminal_status": "ready",
                "agent_attempts": 1,
            }},
            "evidence": semantic_evidence,
            "data_tree_sha256": verify._tree_sha256(data),
        }
        verify.write_json(scenario / "progress.json", progress)

        query_events = [
            {"type": "thread.started", "thread_id": "query-session"},
            {"type": "item.completed", "item": {
                "type": "mcp_tool_call", "server": "ownward", "tool": "ownward_search",
                "status": "completed", "error": None, "arguments": {"query": "question"},
                "result": {"structured_content": {"results": [{"id": stable}]}},
            }},
            {"type": "item.completed", "item": {
                "type": "mcp_tool_call", "server": "ownward", "tool": "ownward_read",
                "status": "completed", "error": None, "arguments": {"id": stable},
                "result": {"structured_content": {"information": {"id": stable, "content": fact}}},
            }},
        ]
        query = scenario / "query" / "attempt-001"
        query.mkdir(parents=True)
        (query / "events.jsonl").write_text("\n".join(json.dumps(item) for item in query_events), encoding="utf-8")
        verify.write_json(query / "output.json", {"information_ids": [stable], "answer_facts": [fact]})
        (query / "stderr.txt").write_text("", encoding="utf-8")
        attempt = {
            "path": "query/attempt-001",
            "elapsed_seconds": 2.0,
            "tool_calls": 2,
            "failed_tool_calls": 0,
            "status": "accepted",
        }
        verify.write_json(query / "attempt.json", {"schema": "ownward.product-query-attempt/v1", **attempt})
        evidence = {**semantic_evidence, **verify._relative_evidence(scenario, query)}
        agent_result = {
            "scenario_id": task["scenario_id"],
            "returned_ids": ["n1"],
            "answer_facts": [fact],
            "navigation_ids": [],
            "grounded": True,
            "semantic_ms": 1000.0,
            "rollback_ms": 100.0,
            "agent_query_ms": 2000.0,
            "end_to_end_ms": 3500.0,
            "peak_mib": 10.0,
            "used_navigation": False,
            "within_resource_budget": True,
            "agent_query_attempts": 1,
            "agent_tool_calls": 2,
            "agent_successful_tool_calls": 2,
            "agent_failed_tool_calls": 0,
        }
        agent = {
            "schema": "ownward.product-scenario-agent-checkpoint/v2",
            "binding": expected,
            "progress_sha256": verify.sha256(scenario / "progress.json"),
            "evidence": evidence,
            "query_attempts": [attempt],
            "result": agent_result,
        }
        verify.write_json(scenario / "agent-result.json", agent)
        measurement = {
            "schema": "ownward.product-direct-measurement/v4",
            "binding": expected,
            "progress_sha256": verify.json_sha256(progress),
            "agent_checkpoint_sha256": verify.sha256(scenario / "agent-result.json"),
            "data_tree_sha256": progress["data_tree_sha256"],
            "question_sha256": expected["question_sha256"],
            "query_limit_ms": 600.0,
            "warmup_probe_chars": expected["question_chars"],
            "prior_readiness_failures": {},
            "warmup_samples_ms": [100.0, 100.0, 100.0],
            "warmup_ms": 300.0,
            "latency_ms": 200.0,
            "stage_ms": 400.0,
            "sampled_peak_mib": 5.0,
            "direct_result": {"results": [{"id": stable, "signals": ["relation"]}]},
            "direct_ids": ["n1"],
            "relation_ids": ["n1"],
            "within_latency_budget": True,
        }
        direct = scenario / "direct" / "attempt-001" / "measurement.json"
        verify.write_json(direct, measurement)
        verify.write_json(scenario / "result.json", {
            "schema": "ownward.product-scenario-checkpoint/v3",
            "binding": expected,
            "agent_checkpoint_sha256": verify.sha256(scenario / "agent-result.json"),
            "direct_evidence_path": direct.relative_to(scenario).as_posix(),
            "direct_evidence_sha256": verify.sha256(direct),
            "result": verify._merge_direct_result(agent_result, measurement),
        })
        return expected, raw_events

    def test_parser_only_change_replays_raw_trace_without_model_work(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            old_tool, current_tool = self._manifests(root / "binding")
            codex = root / "codex.exe"
            codex.write_bytes(b"codex")
            args = SimpleNamespace(
                evidence_dir=root / "scenarios",
                codex_binary=codex,
                codex_model="model",
                codex_reasoning_effort="effort",
            )
            task = {
                "scenario_id": "scenario",
                "information": [{"node_id": "n1", "content": "stored fact"}],
                "updates": [],
                "query": {"question": "question"},
            }
            common = {
                "suite_version": "1.0.0",
                "candidate": "c" * 40,
                "binary_sha256": "b" * 64,
                "environment_sha256": "e" * 64,
                "input_manifest_sha256": "i" * 64,
            }
            old_binding = {**common, "tool_sha256": old_tool}
            current_binding = {**common, "tool_sha256": current_tool}
            resource_sha = "r" * 64
            previous, raw_events = self._sealed_scenario(args.evidence_dir, task, old_binding, args, resource_sha)
            receipts = replay._rebind_scenarios(
                args, [task], current_binding, resource_sha, root / "binding",
            )
            self.assertEqual(1, len(receipts))
            scenario = args.evidence_dir / "scenario"
            current = verify._scenario_binding(args, task, current_binding, resource_sha)
            sealed = verify.load_json(scenario / "result.json")
            self.assertTrue(verify._sealed_scenario_valid(sealed, scenario, current, False))
            progress = verify.load_json(scenario / "progress.json")
            self.assertEqual(["list_mcp_resources:empty"], progress["completed_units"]["initial:n1"]["protocol_operations"])
            semantic = scenario / "semantic-initial" / verify.json_sha256("initial:n1")[:16] / "attempt-001" / "events.jsonl"
            self.assertEqual(raw_events, semantic.read_bytes())
            archived = scenario / "derivation-audit" / f"{old_tool[:12]}-{current_tool[:12]}" / "progress.json"
            self.assertEqual(previous, verify.load_json(archived)["binding"])
            self.assertTrue((scenario / "derivation-replay" / f"{current_tool}.json").is_file())

    def test_raw_execution_change_cannot_use_derivation_replay(self) -> None:
        paths = {
            "command construction": "benchmarks/acceptance/suite/execution_product.py",
            "timeout and execution orchestration": "benchmarks/acceptance/suite/execution_product.py",
            "isolated environment": "benchmarks/acceptance/suite/adapters/product/codex_transport.py",
            "retry policy": "benchmarks/acceptance/suite/adapters/product/verify.py",
            "MCP tool scope": "benchmarks/acceptance/suite/adapters/product/verify.py",
            "resource adapter": "benchmarks/acceptance/suite/adapters/product_resource/verify.py",
            "frozen task generation": "benchmarks/acceptance/suite/product.py",
        }
        for label, path in paths.items():
            with self.subTest(label=label), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                old_tool, current_tool = self._manifests(root, changed_raw_path=path)
                common = {
                    "suite_version": "1.0.0", "candidate": "candidate",
                    "binary_sha256": "b" * 64, "environment_sha256": "e" * 64,
                    "input_manifest_sha256": "i" * 64,
                }
                self.assertFalse(replay._bindings_replay_compatible(
                    {**common, "tool_sha256": old_tool},
                    {**common, "tool_sha256": current_tool},
                    root,
                ))

    def test_scoring_only_identity_change_can_replay(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            raw = {"raw.py": "1" * 64}
            old = self._v5_manifest(raw, {"product_scoring.py": "2" * 64}, "a" * 40)
            current = self._v5_manifest(raw, {"product_scoring.py": "3" * 64}, "b" * 40)
            old_path = root / "old" / "product-tools.json"
            current_path = root / "current" / "product-tools.json"
            verify.write_json(old_path, old)
            verify.write_json(current_path, current)
            common = {
                "suite_version": "1.0.0", "candidate": "candidate", "binary_sha256": "b" * 64,
                "environment_sha256": "e" * 64, "input_manifest_sha256": "i" * 64,
            }
            self.assertTrue(replay._bindings_replay_compatible(
                {**common, "tool_sha256": verify.sha256(old_path)},
                {**common, "tool_sha256": verify.sha256(current_path)},
                root,
            ))

    def test_model_task_resource_and_codex_identity_changes_cannot_replay(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            old_tool, current_tool = self._manifests(root)
            previous = {
                "suite_version": "1.0.0", "candidate": "candidate", "binary_sha256": "b" * 64,
                "environment_sha256": "e" * 64, "input_manifest_sha256": "i" * 64,
                "tool_sha256": old_tool, "task_sha256": "t" * 64,
                "resource_report_sha256": "r" * 64, "question_sha256": "q" * 64,
                "question_chars": 8, "codex_binary_sha256": "x" * 64,
                "codex_model": "model", "codex_reasoning_effort": "effort",
            }
            for field, value in {
                "candidate": "other", "binary_sha256": "a" * 64,
                "environment_sha256": "z" * 64, "input_manifest_sha256": "j" * 64,
                "task_sha256": "u" * 64, "resource_report_sha256": "s" * 64,
                "question_sha256": "w" * 64, "question_chars": 9,
                "codex_binary_sha256": "y" * 64, "codex_model": "other-model",
                "codex_reasoning_effort": "other-effort",
            }.items():
                with self.subTest(field=field):
                    current = {**previous, "tool_sha256": current_tool, field: value}
                    self.assertFalse(replay._bindings_replay_compatible(previous, current, root))

    def test_legacy_replay_requires_exact_one_time_source_proof(self) -> None:
        common = {
            "suite_version": "1.0.0", "candidate": "candidate", "binary_sha256": "b" * 64,
            "environment_sha256": "e" * 64, "input_manifest_sha256": "i" * 64,
        }
        for declared, expected in ((True, True), (False, False)):
            with self.subTest(declared=declared), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                old_tool, current_tool = self._legacy_manifests(root, declared_source_files=declared)
                self.assertEqual(expected, replay._bindings_replay_compatible(
                    {**common, "tool_sha256": old_tool},
                    {**common, "tool_sha256": current_tool},
                    root,
                ))

    def test_missing_mutated_or_parser_inconsistent_raw_events_are_rejected(self) -> None:
        for failure in ("missing", "mutated", "parser-mismatch"):
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                old_tool, current_tool = self._manifests(root / "binding")
                codex = root / "codex.exe"
                codex.write_bytes(b"codex")
                args = SimpleNamespace(
                    evidence_dir=root / "scenarios", codex_binary=codex,
                    codex_model="model", codex_reasoning_effort="effort",
                )
                task = {
                    "scenario_id": "scenario",
                    "information": [{"node_id": "n1", "content": "stored fact"}],
                    "updates": [], "query": {"question": "question"},
                }
                common = {
                    "suite_version": "1.0.0", "candidate": "c" * 40,
                    "binary_sha256": "b" * 64, "environment_sha256": "e" * 64,
                    "input_manifest_sha256": "i" * 64,
                }
                old_binding = {**common, "tool_sha256": old_tool}
                current_binding = {**common, "tool_sha256": current_tool}
                self._sealed_scenario(args.evidence_dir, task, old_binding, args, "r" * 64)
                events = next((args.evidence_dir / "scenario" / "semantic-initial").rglob("events.jsonl"))
                parser = mock.patch.object(
                    replay.verify.codex_session,
                    "load_exec_events",
                    return_value=replay.verify.codex_session.SessionTrace(
                        "session", [], True, ("unapproved",), (),
                    ),
                ) if failure == "parser-mismatch" else mock.patch.object(
                    replay.verify.codex_session, "load_exec_events", wraps=replay.verify.codex_session.load_exec_events,
                )
                if failure == "missing":
                    events.unlink()
                elif failure == "mutated":
                    events.write_bytes(events.read_bytes() + b"\n{}")
                with parser, self.assertRaises(RuntimeError):
                    replay._rebind_scenarios(
                        args, [task], current_binding, "r" * 64, root / "binding",
                    )

    def test_preflight_report_is_rebound_only_after_all_raw_scenarios_validate(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            old_tool, current_tool = self._manifests(root / "binding")
            common = {
                "suite_version": "1.0.0",
                "candidate": "candidate",
                "binary_sha256": "b" * 64,
                "environment_sha256": "e" * 64,
                "input_manifest_sha256": "i" * 64,
            }
            previous = {**common, "tool_sha256": old_tool}
            current = {**common, "tool_sha256": current_tool}
            tasks = {
                "schema": "ownward.product-tasks/v1",
                "tasks": [
                    {
                        "scenario_id": f"s{index}",
                        "information": [{"node_id": "a"}, {"node_id": "b"}],
                        "updates": [],
                        "query": {"question": "question"},
                    }
                    for index in range(4)
                ],
            }
            report_root = root / "preflight"
            full_query = report_root / "full-information-query" / "report.json"
            verify.write_json(full_query, {"passed": True, "identity": {"binding": previous}})
            verify.write_json(report_root / "report.json", {
                "passed": True,
                "qualification_binding": previous,
                "task_set_sha256": verify.json_sha256(tasks),
                "resource_report_sha256": "r" * 64,
                "bindings": [],
                "full_information_query": {"path": str(full_query), "sha256": verify.sha256(full_query)},
            })
            args = SimpleNamespace(
                evidence_dir=root / "formal",
                codex_binary=root / "codex.exe",
                codex_model="model",
                codex_reasoning_effort="effort",
            )
            args.codex_binary.write_bytes(b"codex")
            for task in verify._preflight_tasks(tasks):
                verify.write_json(
                    report_root / "scenarios" / "_preflight" / str(task["scenario_id"]) / "result.json",
                    {"schema": "fixture"},
                )
            with (
                mock.patch.object(replay, "_rebind_scenarios", return_value=[]),
                mock.patch.object(verify, "_sealed_scenario_valid", return_value=True),
            ):
                replay._rebind_preflight(
                    args, tasks, current, "r" * 64, root / "binding", report_root,
                )
            rebound = json.loads((report_root / "report.json").read_text(encoding="utf-8"))
            self.assertEqual(current, rebound["qualification_binding"])
            self.assertEqual(current, json.loads(full_query.read_text(encoding="utf-8"))["identity"]["binding"])
            self.assertTrue(list((report_root / "_audit" / "derivation-replay" / "reports").glob("*.json")))


if __name__ == "__main__":
    unittest.main()
