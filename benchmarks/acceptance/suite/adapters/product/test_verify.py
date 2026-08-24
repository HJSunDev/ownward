from __future__ import annotations

from pathlib import Path
import json
import os
import tempfile
import threading
import time
import unittest
from types import SimpleNamespace
from unittest import mock

import verify


class ProductAdapterTests(unittest.TestCase):
    def test_scenarios_run_concurrently_and_return_in_frozen_order(self) -> None:
        tasks = [{"scenario_id": f"scenario-{index}"} for index in range(4)]
        barrier = threading.Barrier(4)
        active = 0
        maximum_active = 0
        lock = threading.Lock()

        def run(_args: object, task: dict[str, object], *_positional: object, **_keyword: object) -> dict[str, object]:
            nonlocal active, maximum_active
            with lock:
                active += 1
                maximum_active = max(maximum_active, active)
            barrier.wait(timeout=2)
            time.sleep(0.01 * (4 - int(str(task["scenario_id"]).split("-")[-1])))
            with lock:
                active -= 1
            return {"scenario_id": task["scenario_id"]}

        direct_barrier = threading.Barrier(4)
        direct_active = 0
        direct_maximum_active = 0

        def complete(_args: object, task: dict[str, object], _binding: object, result: dict[str, object], *_positional: object, **_keyword: object) -> dict[str, object]:
            nonlocal direct_active, direct_maximum_active
            with lock:
                direct_active += 1
                direct_maximum_active = max(direct_maximum_active, direct_active)
            direct_barrier.wait(timeout=2)
            with lock:
                direct_active -= 1
            return result

        with mock.patch.object(verify, "_run_scenario", side_effect=run), mock.patch.object(
            verify, "_complete_direct_measurement", side_effect=complete,
        ):
            results = verify._run_scenarios(
                SimpleNamespace(), tasks, {}, 0.0, True, 1.0, "a" * 64, time.monotonic() + 5,
            )
        self.assertEqual(4, maximum_active)
        self.assertEqual(4, direct_maximum_active)
        self.assertEqual([task["scenario_id"] for task in tasks], [result["scenario_id"] for result in results])

    def test_qualification_projection_uses_four_worker_schedule_and_safety_margin(self) -> None:
        preflight_tasks = [
            {"information": [{}, {}], "updates": []}
            for _ in range(4)
        ]
        results = [
            {"semantic_ms": 2000.0, "rollback_ms": 200.0, "agent_query_ms": 1000.0, "direct_stage_ms": 500.0, "end_to_end_ms": 4000.0}
            for _ in range(4)
        ]
        formal_tasks = [
            {"information": [{}] * 5, "updates": ([{}] if index in {0, 2} else [])}
            for index in range(8)
        ]
        projection = verify._project_qualification_wall(
            formal_tasks, preflight_tasks, results, workers=4, batch_wall_seconds=5.6,
        )
        self.assertEqual(4, verify.SCENARIO_WORKERS)
        self.assertAlmostEqual(23.625, projection["wall_seconds"], places=3)

    def test_answer_schema_uses_the_codex_subset_and_runtime_enforces_uniqueness(self) -> None:
        for value in verify.ANSWER_SCHEMA["properties"].values():
            self.assertNotIn("uniqueItems", value)
        self.assertEqual((["one"], ["fact"]), verify._validated_answer({"information_ids": ["one"], "answer_facts": ["fact"]}))
        with self.assertRaisesRegex(RuntimeError, "duplicate information IDs"):
            verify._validated_answer({"information_ids": ["one", "one"], "answer_facts": ["fact"]})
        with self.assertRaisesRegex(RuntimeError, "duplicate answer facts"):
            verify._validated_answer({"information_ids": ["one"], "answer_facts": ["fact", "fact"]})

    def test_codex_command_excludes_repository_project_instructions(self) -> None:
        args = SimpleNamespace(
            codex_binary=Path("codex.exe"),
            codex_model="model",
            codex_reasoning_effort="effort",
        )
        command = verify._codex_command(
            args,
            work_dir=Path("work"),
            schema_path=Path("schema.json"),
            output_path=Path("output.json"),
            endpoint="http://127.0.0.1:1",
        )
        overrides = [command[index + 1] for index, value in enumerate(command[:-1]) if value == "-c"]
        self.assertIn("project_doc_max_bytes=0", overrides)

    def test_isolated_codex_environment_bypasses_proxy_for_loopback_mcp(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            auth = root / "auth.json"
            auth.write_text("{}", encoding="utf-8")
            with mock.patch.dict(
                os.environ,
                {"HTTP_PROXY": "http://127.0.0.1:7890", "NO_PROXY": "example.test"},
                clear=True,
            ):
                environment = verify.codex_session.isolated_environment(auth, root / "codex-home")
            self.assertEqual("http://127.0.0.1:7890", environment["HTTP_PROXY"])
            for value in ("example.test", "127.0.0.1", "localhost", "::1"):
                self.assertIn(value, environment["NO_PROXY"].split(","))
                self.assertIn(value, environment["no_proxy"].split(","))

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

    def test_codex_timeout_accepts_already_persisted_structured_output(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            auth = root / "auth.json"
            auth.write_text("{}", encoding="utf-8")
            args = SimpleNamespace(
                codex_binary=root / "codex.exe",
                codex_auth_file=auth,
                codex_model="model",
                codex_reasoning_effort="effort",
            )
            events = json.dumps({"type": "thread.started", "thread_id": "session"}) + "\n"

            def timeout(command: list[str], **keyword: object) -> object:
                output = Path(command[command.index("-o") + 1])
                output.write_text('{"processed":1,"uncertain":0}', encoding="utf-8")
                stdout_path = keyword["stdout_path"]
                stderr_path = keyword["stderr_path"]
                assert isinstance(stdout_path, Path) and isinstance(stderr_path, Path)
                stdout_path.write_text(events, encoding="utf-8")
                stderr_path.write_text("", encoding="utf-8")
                raise verify.process_control.ProcessTimeout("timeout", events, "")

            with mock.patch.object(verify.process_control, "run", side_effect=timeout):
                output, trace, elapsed = verify._run_codex(
                    args,
                    stage=root / "stage",
                    prompt="prompt",
                    schema=verify.SEMANTIC_SCHEMA,
                    endpoint="http://127.0.0.1:1",
                    bearer_token="token",
                    timeout_seconds=1,
                )
            self.assertEqual({"processed": 1, "uncertain": 0}, output)
            self.assertEqual("session", trace.session_id)
            self.assertGreaterEqual(elapsed, 0)

    def test_semantic_trace_accepts_only_bounded_rejected_submissions_before_success(self) -> None:
        asset_id = "asset"
        work_id = "work"
        work = verify.codex_session.ToolCall(
            "ownward_semantic_work",
            {"asset_ids": [asset_id]},
            {"work": [{"id": work_id, "asset": {"id": asset_id, "revision": 1}}]},
            False,
        )
        rejected = verify.codex_session.ToolCall("ownward_semantic_submit", {"submission": {}}, None, True)
        successful = verify.codex_session.ToolCall(
            "ownward_semantic_submit",
            {"submission": {"work_id": work_id, "asset_id": asset_id, "asset_revision": 1}},
            {"organization": {"status": "ready"}},
            False,
        )
        identity = verify._semantic_trace_identity(SimpleNamespace(calls=[work, rejected, successful]), asset_id, 1)
        self.assertEqual(work_id, identity["work_id"])

        with self.assertRaisesRegex(RuntimeError, "one successful submit"):
            verify._semantic_trace_identity(SimpleNamespace(calls=[work, successful, successful]), asset_id, 1)
        with self.assertRaisesRegex(RuntimeError, "at most two rejected"):
            verify._semantic_trace_identity(
                SimpleNamespace(calls=[work, rejected, rejected, rejected, successful]), asset_id, 1,
            )

    def test_semantic_timeout_after_successful_submit_uses_terminal_product_state(self) -> None:
        asset_id = "asset"
        work_id = "work"
        work = verify.codex_session.ToolCall(
            "ownward_semantic_work",
            {"asset_ids": [asset_id]},
            {"work": [{"id": work_id, "asset": {"id": asset_id, "revision": 1}}]},
            False,
        )
        successful = verify.codex_session.ToolCall(
            "ownward_semantic_submit",
            {"submission": {"work_id": work_id, "asset_id": asset_id, "asset_revision": 1}},
            {"organization": {"status": "ready"}},
            False,
        )
        trace = SimpleNamespace(calls=[work, successful], bypassed=False)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)

            def timeout(*_positional: object, **keyword: object) -> object:
                stage = keyword["stage"]
                assert isinstance(stage, Path)
                stage.mkdir(parents=True)
                (stage / "events.jsonl").write_text("events", encoding="utf-8")
                (stage / "stderr.txt").write_text("", encoding="utf-8")
                raise verify.CodexStageTimeout("timeout", trace, 240.0)

            client = SimpleNamespace(call_tool=mock.Mock(return_value={"organization": {"status": "ready"}}))
            runtime = SimpleNamespace(
                binding=SimpleNamespace(endpoint="http://127.0.0.1:1", bearer_token="token"),
                client=client,
            )
            args = SimpleNamespace(stage_timeout=240, codex_model="model")
            with mock.patch.object(verify, "_run_codex", side_effect=timeout):
                elapsed, identity, evidence = verify._complete_semantic_unit(
                    args, runtime, root, root / "semantic-initial" / "unit", asset_id, 1, time.monotonic() + 300,
                )
            self.assertEqual(240.0, elapsed)
            self.assertEqual("terminal-submit-recovered-after-stage-timeout", identity["completion"])
            self.assertEqual(
                {"terminal.json", "events.jsonl", "stderr.txt"},
                {Path(path).name for path in evidence},
            )
            progress = {"completed_units": {"initial:asset": identity}, "evidence": evidence}
            self.assertTrue(verify._progress_evidence_complete(progress))

    def test_semantic_timeout_without_successful_submit_remains_a_failure(self) -> None:
        asset_id = "asset"
        work = verify.codex_session.ToolCall(
            "ownward_semantic_work",
            {"asset_ids": [asset_id]},
            {"work": [{"id": "work", "asset": {"id": asset_id, "revision": 1}}]},
            False,
        )
        trace = SimpleNamespace(calls=[work], bypassed=False)
        runtime = SimpleNamespace(
            binding=SimpleNamespace(endpoint="http://127.0.0.1:1", bearer_token="token"),
            client=SimpleNamespace(call_tool=mock.Mock(return_value={"organization": {"status": "pending"}})),
        )
        args = SimpleNamespace(stage_timeout=240, codex_model="model")
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(
            verify, "_run_codex", side_effect=verify.CodexStageTimeout("timeout", trace, 240.0),
        ):
            with self.assertRaisesRegex(RuntimeError, "one successful submit"):
                verify._complete_semantic_unit(
                    args, runtime, Path(directory), Path(directory) / "semantic-initial" / "unit",
                    asset_id, 1, time.monotonic() + 300,
                )

    def test_semantic_timeout_rejects_mismatched_binding_and_nonterminal_state(self) -> None:
        asset_id = "asset"
        work_id = "work"
        work = verify.codex_session.ToolCall(
            "ownward_semantic_work",
            {"asset_ids": [asset_id]},
            {"work": [{"id": work_id, "asset": {"id": asset_id, "revision": 1}}]},
            False,
        )
        mismatched = verify.codex_session.ToolCall(
            "ownward_semantic_submit",
            {"submission": {"work_id": work_id, "asset_id": "other", "asset_revision": 1}},
            {"organization": {"status": "ready"}},
            False,
        )
        with self.assertRaisesRegex(RuntimeError, "do not bind"):
            verify._semantic_trace_identity(SimpleNamespace(calls=[work, mismatched]), asset_id, 1)

        successful = verify.codex_session.ToolCall(
            "ownward_semantic_submit",
            {"submission": {"work_id": work_id, "asset_id": asset_id, "asset_revision": 1}},
            {"organization": {"status": "ready"}},
            False,
        )
        trace = SimpleNamespace(calls=[work, successful], bypassed=False)
        runtime = SimpleNamespace(
            binding=SimpleNamespace(endpoint="http://127.0.0.1:1", bearer_token="token"),
            client=SimpleNamespace(call_tool=mock.Mock(return_value={"organization": {"status": "pending"}})),
        )
        args = SimpleNamespace(stage_timeout=240, codex_model="model")
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(
            verify, "_run_codex", side_effect=verify.CodexStageTimeout("timeout", trace, 240.0),
        ):
            with self.assertRaisesRegex(RuntimeError, "terminal state"):
                verify._complete_semantic_unit(
                    args, runtime, Path(directory), Path(directory) / "semantic-initial" / "unit",
                    asset_id, 1, time.monotonic() + 300,
                )

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

    def test_temporary_cleanup_retries_transient_windows_file_locks(self) -> None:
        with mock.patch.object(
            verify.shutil,
            "rmtree",
            side_effect=[PermissionError("locked"), None],
        ) as remove, mock.patch.object(verify.time, "sleep"):
            verify._cleanup_temporary(Path("temporary-codex-home"))
        self.assertEqual(2, remove.call_count)

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
            removed = next(iter(sealed["evidence"]))
            sealed["evidence"].pop(removed)
            self.assertFalse(verify._sealed_scenario_valid(sealed, root, binding, True))
            sealed["evidence"] = verify._scenario_evidence(root, True)
            (root / files[0]).write_text("changed", encoding="utf-8")
            self.assertFalse(verify._sealed_scenario_valid(sealed, root, binding, True))

    def test_transactional_checkpoint_requires_committed_semantics_and_one_complete_query_attempt(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            semantic = root / "semantic-initial" / "unit" / "attempt-001" / "events.jsonl"
            semantic.parent.mkdir(parents=True)
            semantic.write_text("semantic", encoding="utf-8")
            progress = {
                "schema": "ownward.product-scenario-progress/v1",
                "completed_units": {"initial:one": {"done": True}},
                "evidence": {semantic.relative_to(root).as_posix(): verify.sha256(semantic)},
            }
            for name in ("output.json", "stderr.txt"):
                path = semantic.parent / name
                path.write_text(name, encoding="utf-8")
                progress["evidence"][path.relative_to(root).as_posix()] = verify.sha256(path)
            verify.write_json(root / "progress.json", progress)
            query = root / "query" / "attempt-001"
            query.mkdir(parents=True)
            for name in ("output.json", "events.jsonl", "stderr.txt"):
                (query / name).write_text(name, encoding="utf-8")
            evidence = dict(progress["evidence"])
            evidence.update(verify._relative_evidence(root, query))
            sealed = {
                "schema": "ownward.product-scenario-checkpoint/v2",
                "binding": {"candidate": "candidate"},
                "progress_sha256": verify.sha256(root / "progress.json"),
                "evidence": evidence,
                "result": {"passed": True},
            }
            self.assertTrue(verify._sealed_scenario_valid(sealed, root, sealed["binding"], False))
            sealed["evidence"].pop(next(path for path in sealed["evidence"] if path.endswith("events.jsonl") and path.startswith("query/")))
            self.assertFalse(verify._sealed_scenario_valid(sealed, root, sealed["binding"], False))

    def test_final_checkpoint_combines_agent_evidence_with_isolated_direct_measurement(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            data = root / "data"
            data.mkdir()
            (data / "state.json").write_text("state", encoding="utf-8")
            semantic = root / "semantic-initial" / "unit" / "attempt-001"
            semantic.mkdir(parents=True)
            query = root / "query" / "attempt-001"
            query.mkdir(parents=True)
            evidence: dict[str, str] = {}
            for stage in (semantic, query):
                for name in ("output.json", "events.jsonl", "stderr.txt"):
                    path = stage / name
                    path.write_text(f"{stage.name}-{name}", encoding="utf-8")
                    evidence[path.relative_to(root).as_posix()] = verify.sha256(path)
            progress = {
                "schema": "ownward.product-scenario-progress/v1",
                "stable_by_node": {"node": "stable"},
                "completed_units": {"initial:node": {"done": True}},
                "evidence": {path: digest for path, digest in evidence.items() if path.startswith("semantic-initial/")},
                "data_tree_sha256": verify._tree_sha256(data),
            }
            verify.write_json(root / "progress.json", progress)
            binding = {"candidate": "candidate"}
            agent_result = {
                "scenario_id": "scenario", "end_to_end_ms": 1000.0, "peak_mib": 10.0,
                "returned_ids": ["node"], "within_resource_budget": True,
            }
            agent = {
                "schema": "ownward.product-scenario-agent-checkpoint/v1",
                "binding": binding,
                "progress_sha256": verify.sha256(root / "progress.json"),
                "evidence": evidence,
                "result": agent_result,
            }
            verify.write_json(root / "agent-result.json", agent)
            direct = root / "direct" / "attempt-001" / "measurement.json"
            measurement = {
                "schema": "ownward.product-direct-measurement/v1",
                "binding": binding,
                "progress_sha256": verify.json_sha256(progress),
                "agent_checkpoint_sha256": verify.sha256(root / "agent-result.json"),
                "data_tree_sha256": progress["data_tree_sha256"],
                "question_sha256": "a" * 64,
                "query_limit_ms": 600.0,
                "warmup_ms": 200.0,
                "latency_ms": 300.0,
                "stage_ms": 2500.0,
                "sampled_peak_mib": 12.0,
                "direct_result": {"results": [{"id": "stable"}]},
                "direct_ids": ["node"],
                "within_latency_budget": True,
            }
            verify.write_json(direct, measurement)
            sealed = {
                "schema": "ownward.product-scenario-checkpoint/v3",
                "binding": binding,
                "agent_checkpoint_sha256": verify.sha256(root / "agent-result.json"),
                "direct_evidence_path": direct.relative_to(root).as_posix(),
                "direct_evidence_sha256": verify.sha256(direct),
                "result": verify._merge_direct_result(agent_result, measurement),
            }
            verify.write_json(root / "result.json", sealed)
            verify._safe_reset(data, root)
            self.assertTrue(verify._sealed_scenario_valid(sealed, root, binding, False))
            measurement["latency_ms"] = 700.0
            verify.write_json(direct, measurement)
            self.assertFalse(verify._sealed_scenario_valid(sealed, root, binding, False))

    def test_direct_measurement_reuses_the_active_scenario_runtime(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            scenario = root / "scenario"
            data = scenario / "data"
            data.mkdir(parents=True)
            (data / "state.json").write_text("state", encoding="utf-8")
            codex = root / "codex.exe"
            codex.write_text("codex", encoding="utf-8")
            args = SimpleNamespace(
                evidence_dir=root,
                binary=root / "missing-ownward.exe",
                codex_binary=codex,
                codex_model="model",
                codex_reasoning_effort="effort",
            )
            task = {"scenario_id": "scenario", "query": {"question": "question"}, "updates": []}
            binding = {
                "suite_version": "1.0.0",
                "candidate": "candidate",
                "binary_sha256": "b" * 64,
                "environment_sha256": "e" * 64,
                "input_manifest_sha256": "i" * 64,
                "tool_sha256": "t" * 64,
            }
            expected = verify._scenario_binding(args, task, binding, "r" * 64)
            semantic = scenario / "semantic-initial" / "unit" / "attempt-001"
            query = scenario / "query" / "attempt-001"
            evidence: dict[str, str] = {}
            for stage in (semantic, query):
                stage.mkdir(parents=True)
                for name in ("output.json", "events.jsonl", "stderr.txt"):
                    path = stage / name
                    path.write_text(f"{stage.name}-{name}", encoding="utf-8")
                    evidence[path.relative_to(scenario).as_posix()] = verify.sha256(path)
            progress = {
                "schema": "ownward.product-scenario-progress/v1",
                "binding": expected,
                "stable_by_node": {"node": "stable"},
                "completed_units": {"initial:node": {"done": True}},
                "evidence": {path: digest for path, digest in evidence.items() if path.startswith("semantic-initial/")},
                "data_tree_sha256": verify._tree_sha256(data),
            }
            verify.write_json(scenario / "progress.json", progress)
            agent_result = {
                "scenario_id": "scenario",
                "end_to_end_ms": 1000.0,
                "peak_mib": 10.0,
                "returned_ids": ["node"],
                "within_resource_budget": True,
            }
            verify.write_json(scenario / "agent-result.json", {
                "schema": "ownward.product-scenario-agent-checkpoint/v1",
                "binding": expected,
                "progress_sha256": verify.sha256(scenario / "progress.json"),
                "evidence": evidence,
                "result": agent_result,
            })
            client = mock.Mock()
            client.call_tool.side_effect = [
                {"results": []},
                {"results": [{"id": "stable"}]},
            ]
            active = SimpleNamespace(client=client, process=SimpleNamespace(pid=os.getpid()))
            with mock.patch.object(verify.support, "OwnwardRuntime", side_effect=AssertionError("unexpected restart")):
                result = verify._complete_direct_measurement(
                    args,
                    task,
                    binding,
                    agent_result,
                    600.0,
                    "r" * 64,
                    time.monotonic() + 30,
                    cleanup_data=False,
                    active_runtime=active,
                )
            self.assertEqual(["node"], result["direct_ids"])
            self.assertTrue(result["within_latency_budget"])
            self.assertEqual(2, client.call_tool.call_count)
            self.assertTrue((scenario / "result.json").is_file())

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

    def test_resume_archives_a_scenario_from_the_previous_binding(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            evidence_root = root / "scenarios"
            scenario_root = evidence_root / "scenario-1"
            scenario_root.mkdir(parents=True)
            verify.write_json(scenario_root / "result.json", {
                "schema": "ownward.product-scenario-checkpoint/v1",
                "binding": {"candidate": "previous"},
                "evidence": {},
                "result": {"scenario_id": "scenario-1", "passed": False},
            })
            codex = root / "codex.exe"
            codex.write_bytes(b"codex")
            args = SimpleNamespace(
                evidence_dir=evidence_root,
                binary=codex,
                codex_binary=codex,
                codex_model="model",
                codex_reasoning_effort="effort",
                resume=True,
            )
            task = {"scenario_id": "scenario-1", "updates": [], "information": [], "query": {}}
            binding = {
                "suite_version": "1.0.0", "candidate": "current", "binary_sha256": "a" * 64,
                "environment_sha256": "b" * 64, "input_manifest_sha256": "c" * 64,
                "tool_sha256": "d" * 64,
            }
            with mock.patch.object(verify.support, "OwnwardRuntime", side_effect=RuntimeError("stop after archive")):
                with self.assertRaisesRegex(RuntimeError, "stop after archive"):
                    verify._run_scenario(args, task, binding, 0, True, 1, "e" * 64, 0)
            archives = list((evidence_root / "_audit").iterdir())
            self.assertEqual(1, len(archives))
            self.assertTrue((archives[0] / "result.json").is_file())
            self.assertEqual(
                "sealed scenario binding or evidence changed",
                json.loads((archives[0] / "archive.json").read_text(encoding="utf-8"))["reason"],
            )

    def test_uncommitted_mutation_restores_only_current_unit(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            data = root / "data"
            data.mkdir()
            (data / "state.json").write_text("before", encoding="utf-8")
            (data / ".ownward.lock").write_text("transient", encoding="utf-8")
            progress = {"data_tree_sha256": verify._tree_sha256(data), "evidence": {}, "completed_units": {"initial:one": {"done": True}}}
            verify.write_json(root / "progress.json", progress)
            verify._begin_rollback(root, progress, "initial:two")
            self.assertFalse((root / "rollback" / "data" / ".ownward.lock").exists())
            (data / "state.json").write_text("half-committed", encoding="utf-8")
            verify._recover_rollback(root, progress)
            self.assertEqual("before", (data / "state.json").read_text(encoding="utf-8"))
            self.assertEqual({"initial:one": {"done": True}}, progress["completed_units"])
            self.assertFalse((data / ".ownward.lock").exists())
            self.assertFalse((root / "rollback").exists())

    def test_data_tree_identity_ignores_the_runtime_lock_file(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "state.json").write_text("persistent", encoding="utf-8")
            lock = root / ".ownward.lock"
            lock.write_text("first", encoding="utf-8")
            first = verify._tree_sha256(root)
            lock.write_text("second", encoding="utf-8")
            self.assertEqual(first, verify._tree_sha256(root))

    def test_committed_progress_survives_crash_before_rollback_cleanup(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            data = root / "data"
            data.mkdir()
            (data / "state.json").write_text("before", encoding="utf-8")
            progress = {"data_tree_sha256": verify._tree_sha256(data), "evidence": {}, "completed_units": {}}
            verify._begin_rollback(root, progress, "initial:one")
            (data / "state.json").write_text("after", encoding="utf-8")
            evidence = root / "semantic" / "events.jsonl"
            evidence.parent.mkdir()
            evidence.write_text("evidence", encoding="utf-8")
            progress["completed_units"]["initial:one"] = {"done": True}
            progress["evidence"] = {"semantic/events.jsonl": verify.sha256(evidence)}
            progress["data_tree_sha256"] = verify._tree_sha256(data)
            verify.write_json(root / "progress.json", progress)
            verify._recover_rollback(root, progress)
            self.assertEqual("after", (data / "state.json").read_text(encoding="utf-8"))
            self.assertFalse((root / "rollback").exists())


if __name__ == "__main__":
    unittest.main()
