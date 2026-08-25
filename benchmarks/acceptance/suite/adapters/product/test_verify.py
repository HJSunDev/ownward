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
    def _write_semantic_infrastructure_attempt(
        self,
        scenario_root: Path,
        *,
        infrastructure_marker: bool = True,
    ) -> None:
        stage = scenario_root / "semantic-initial" / "unit" / "attempt-001"
        stage.mkdir(parents=True)
        events = [
            {"type": "thread.started", "thread_id": "session"},
            {"type": "turn.started"},
            {"type": "item.completed", "item": {
                "type": "mcp_tool_call",
                "server": "ownward",
                "tool": "ownward_semantic_work",
                "status": "completed",
                "error": None,
                "arguments": {"asset_ids": ["asset"]},
                "result": {"structured_content": {"work": [{
                    "id": "work",
                    "asset": {"id": "asset", "revision": 1},
                }]}},
            }},
        ]
        (stage / "events.jsonl").write_text("\n".join(json.dumps(item) for item in events), encoding="utf-8")
        stderr = (
            "codex_models_manager::manager: timeout waiting for child process to exit"
            if infrastructure_marker
            else ""
        )
        (stage / "stderr.txt").write_text(stderr, encoding="utf-8")
        verify.write_json(stage / "attempt.json", {
            "schema": "ownward.semantic-attempt/v1",
            "status": "rejected",
            "reason": "no-successful-semantic-submit",
            "elapsed_seconds": 240.1,
            "tool_calls": 1,
            "failed_tool_calls": 0,
            "protocol_operations": [],
        })
        evidence = verify._relative_evidence(scenario_root, stage)
        verify.write_json(scenario_root / "progress.json", {
            "schema": "ownward.product-scenario-progress/v1",
            "completed_units": {},
            "evidence": evidence,
        })

    def test_scenarios_run_concurrently_and_return_in_frozen_order(self) -> None:
        tasks = [{"scenario_id": f"scenario-{index}"} for index in range(4)]
        barrier = threading.Barrier(4)
        active = 0
        maximum_active = 0
        live_direct_active = 0
        live_direct_maximum = 0
        live_direct_scenarios: list[str] = []
        lock = threading.Lock()

        def run(_args: object, task: dict[str, object], *_positional: object, **_keyword: object) -> dict[str, object]:
            nonlocal active, maximum_active, live_direct_active, live_direct_maximum
            with lock:
                active += 1
                maximum_active = max(maximum_active, active)
            barrier.wait(timeout=2)
            time.sleep(0.01 * (4 - int(str(task["scenario_id"]).split("-")[-1])))
            with lock:
                active -= 1
            direct_barrier = _keyword["direct_barrier"]
            direct_lock = _keyword["direct_lock"]
            direct_barrier.wait(timeout=2)
            with direct_lock:
                with lock:
                    live_direct_active += 1
                    live_direct_maximum = max(live_direct_maximum, live_direct_active)
                    live_direct_scenarios.append(str(task["scenario_id"]))
                time.sleep(0.005)
                with lock:
                    live_direct_active -= 1
            return {"scenario_id": task["scenario_id"]}

        direct_active = 0
        direct_maximum_active = 0
        direct_order: list[str] = []

        def complete(_args: object, task: dict[str, object], _binding: object, result: dict[str, object], *_positional: object, **_keyword: object) -> dict[str, object]:
            nonlocal direct_active, direct_maximum_active
            with lock:
                direct_active += 1
                direct_maximum_active = max(direct_maximum_active, direct_active)
                direct_order.append(str(task["scenario_id"]))
            time.sleep(0.005)
            with lock:
                direct_active -= 1
            return result

        with mock.patch.object(verify, "_run_scenario", side_effect=run), mock.patch.object(
            verify, "_complete_direct_measurement", side_effect=complete,
        ):
            results = verify._run_scenarios(
                SimpleNamespace(evidence_dir=Path("not-created-product-test-evidence")),
                tasks, {}, 0.0, True, 1.0, "a" * 64, time.monotonic() + 5,
            )
        self.assertEqual(4, maximum_active)
        self.assertEqual(1, live_direct_maximum)
        self.assertEqual({task["scenario_id"] for task in tasks}, set(live_direct_scenarios))
        self.assertEqual(1, direct_maximum_active)
        self.assertEqual([task["scenario_id"] for task in tasks], direct_order)
        self.assertEqual([task["scenario_id"] for task in tasks], [result["scenario_id"] for result in results])

    def test_parallel_scenarios_report_all_root_failures_without_barrier_noise(self) -> None:
        tasks = [{"scenario_id": f"scenario-{index}"} for index in range(4)]
        barrier_message = "parallel agent phase did not reach the live direct-measurement checkpoint"
        errors = {
            "scenario-0": RuntimeError(barrier_message),
            "scenario-1": RuntimeError("semantic capability used an invalid path"),
            "scenario-2": RuntimeError("semantic attempt exceeded corrected submission limit"),
            "scenario-3": threading.BrokenBarrierError(),
        }

        def run(_args: object, task: dict[str, object], *_positional: object, **_keyword: object) -> dict[str, object]:
            raise errors[str(task["scenario_id"])]

        with mock.patch.object(verify, "_run_scenario", side_effect=run):
            with self.assertRaisesRegex(RuntimeError, "parallel scenario phase failed") as captured:
                verify._run_scenarios(
                    SimpleNamespace(evidence_dir=Path("not-created-product-test-evidence")),
                    tasks, {}, 0.0, True, 1.0, "a" * 64, time.monotonic() + 5,
                )
        message = str(captured.exception)
        self.assertIn("scenario-1: RuntimeError: semantic capability used an invalid path", message)
        self.assertIn("scenario-2: RuntimeError: semantic attempt exceeded corrected submission limit", message)
        self.assertNotIn(barrier_message, message)

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

    def test_preflight_projection_reuses_the_observed_serial_direct_wave_profile(self) -> None:
        preflight_tasks = [
            {"information": [{}, {}], "updates": []}
            for _ in range(4)
        ]
        results = [
            {
                "semantic_ms": 2000.0,
                "rollback_ms": 0.0,
                "agent_query_ms": 1000.0,
                "direct_stage_ms": direct_ms,
                "end_to_end_ms": 3000.0 + direct_ms,
            }
            for direct_ms in (3000.0, 1000.0, 1000.0, 1000.0)
        ]
        formal_tasks = [
            {"information": [{}], "updates": []}
            for _ in range(8)
        ]
        projection = verify._project_qualification_wall(
            formal_tasks, preflight_tasks, results, workers=4, batch_wall_seconds=9.0,
        )
        self.assertEqual(12.0, projection["projected_direct_stage_seconds"])
        self.assertEqual(16.0, projection["scheduled_wall_seconds"])
        self.assertEqual(20.0, projection["wall_seconds"])

    def test_preflight_projection_reserves_one_observed_semantic_tail_per_formal_scenario(self) -> None:
        preflight_tasks = [
            {"information": [{}, {}], "updates": []}
            for _ in range(4)
        ]
        results = [
            {
                "semantic_ms": semantic_ms,
                "rollback_ms": 0.0,
                "agent_query_ms": 1000.0,
                "direct_stage_ms": 500.0,
                "end_to_end_ms": semantic_ms + 1500.0,
            }
            for semantic_ms in (2000.0, 2000.0, 2000.0, 10000.0)
        ]
        formal_tasks = [
            {"information": [{}] * 5, "updates": []}
            for _ in range(8)
        ]
        projection = verify._project_qualification_wall(
            formal_tasks, preflight_tasks, results, workers=4, batch_wall_seconds=13.0,
        )
        self.assertEqual(1.0, projection["per_semantic_seconds"])
        self.assertEqual(8.0, projection["per_semantic_outlier_seconds"])
        self.assertEqual(32.0, projection["scheduled_wall_seconds"])
        self.assertEqual(40.0, projection["wall_seconds"])

    def test_normal_semantic_timeout_cannot_use_infrastructure_recovery(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            scenario_root = Path(directory) / "scenario"
            self._write_semantic_infrastructure_attempt(scenario_root, infrastructure_marker=False)
            self.assertEqual([], verify._preflight_infrastructure_failures(scenario_root, 240.0))

    def test_preflight_infrastructure_recovery_is_audited_once_and_then_stops(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            evidence_root = root / "scenarios"
            scenario_root = evidence_root / "scenario"
            self._write_semantic_infrastructure_attempt(scenario_root)
            verify.write_json(scenario_root / "result.json", {"schema": "checkpoint"})
            codex = root / "codex.exe"
            codex.write_bytes(b"codex")
            args = SimpleNamespace(
                evidence_dir=evidence_root,
                codex_binary=codex,
                codex_model="model",
                codex_reasoning_effort="effort",
                stage_timeout=240.0,
                resume=True,
            )
            task = {
                "scenario_id": "scenario",
                "information": [],
                "updates": [],
                "query": {"question": "question"},
            }
            binding = {
                "suite_version": "1.0.0",
                "candidate": "candidate",
                "binary_sha256": "a" * 64,
                "environment_sha256": "b" * 64,
                "input_manifest_sha256": "c" * 64,
                "tool_sha256": "d" * 64,
            }
            args.resume = False
            with mock.patch.object(verify, "_sealed_scenario_valid", return_value=True):
                with self.assertRaisesRegex(RuntimeError, "use --resume"):
                    verify._recover_preflight_infrastructure_samples(
                        args, [task], binding, "e" * 64,
                    )
            self.assertTrue(scenario_root.is_dir())

            args.resume = True
            with mock.patch.object(verify, "_sealed_scenario_valid", return_value=True):
                recovered = verify._recover_preflight_infrastructure_samples(
                    args, [task], binding, "e" * 64,
                )
            self.assertEqual(["scenario"], [item["scenario_id"] for item in recovered])
            self.assertFalse(scenario_root.exists())
            archives = list((evidence_root / "_audit").glob("scenario-*/archive.json"))
            self.assertEqual(1, len(archives))
            archived = verify.load_json(archives[0])
            self.assertEqual(
                "models-manager-child-exit-timeout",
                archived["failures"][0]["signature"],
            )
            self.assertEqual(
                archived["evidence_sha256"],
                verify._preflight_archive_payload_sha256(archives[0].parent),
            )

            self._write_semantic_infrastructure_attempt(scenario_root)
            verify.write_json(scenario_root / "result.json", {"schema": "checkpoint"})
            with mock.patch.object(verify, "_sealed_scenario_valid", return_value=True):
                with self.assertRaisesRegex(RuntimeError, "recovery exhausted"):
                    verify._recover_preflight_infrastructure_samples(
                        args, [task], binding, "e" * 64,
                    )
            self.assertTrue(scenario_root.is_dir())
            self.assertEqual(1, len(list((evidence_root / "_audit").glob("scenario-*/archive.json"))))

    def test_clean_preflight_runner_never_returns_polluted_sample(self) -> None:
        args = SimpleNamespace()
        tasks = [{"scenario_id": "scenario"}]
        polluted = [{"semantic_ms": 240000.0}]
        clean = [{"semantic_ms": 1000.0}]
        with (
            mock.patch.object(
                verify,
                "_recover_preflight_infrastructure_samples",
                side_effect=[[], [{"scenario_id": "scenario"}], [], []],
            ) as recover,
            mock.patch.object(verify, "_run_scenarios", side_effect=[polluted, clean]) as run,
        ):
            results, _wall = verify._run_clean_preflight_scenarios(
                args, tasks, {}, 0.0, True, 1.0, "a" * 64, time.monotonic() + 5,
            )
        self.assertEqual(clean, results)
        self.assertEqual(2, run.call_count)
        self.assertEqual(4, recover.call_count)

    def test_answer_schema_uses_the_codex_subset_and_runtime_enforces_uniqueness(self) -> None:
        for value in verify.ANSWER_SCHEMA["properties"].values():
            self.assertNotIn("uniqueItems", value)
        self.assertEqual((["one"], ["fact"]), verify._validated_answer({"information_ids": ["one"], "answer_facts": ["fact"]}))
        with self.assertRaisesRegex(RuntimeError, "invalid information IDs"):
            verify._validated_answer({"information_ids": [], "answer_facts": []})
        with self.assertRaisesRegex(RuntimeError, "duplicate information IDs"):
            verify._validated_answer({"information_ids": ["one", "one"], "answer_facts": ["fact"]})
        with self.assertRaisesRegex(RuntimeError, "duplicate answer facts"):
            verify._validated_answer({"information_ids": ["one"], "answer_facts": ["fact", "fact"]})
        with self.assertRaisesRegex(RuntimeError, "not one-to-one"):
            verify._validated_answer({"information_ids": ["one", "two"], "answer_facts": ["fact"]})

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
            enabled_tools=verify.QUERY_TOOLS,
        )
        overrides = [command[index + 1] for index, value in enumerate(command[:-1]) if value == "-c"]
        self.assertIn("project_doc_max_bytes=0", overrides)
        self.assertIn(
            'mcp_servers.ownward.enabled_tools=["ownward_search","ownward_read","ownward_navigate"]',
            overrides,
        )

    def test_agent_prompts_state_the_irrecoverable_execution_contracts(self) -> None:
        semantic = verify._semantic_prompt("asset", SimpleNamespace(codex_model="model"))
        query = verify._query_prompt("question")
        self.assertIn("successful `ownward_semantic_submit` is mandatory", semantic)
        self.assertIn("do not exhaustively compare every candidate", semantic)
        self.assertIn("Do not create a plan or todo list", semantic)
        self.assertIn("immediately submit complete with no relation", semantic)
        self.assertIn("Immediately use the semantic tools", semantic)
        self.assertIn("exactly one top-level argument named `submission`", semantic)
        self.assertIn('"analysis":{"summary":"<summary>","topics":[],"cues":[],"inferred_contexts":[],"relations":[]}', semantic)
        self.assertIn("Never move those fields to the tool-call root", semantic)
        self.assertIn("identify every distinct fact requested by the question", query)
        self.assertIn("intermediate or modifying clauses as required parts", query)
        self.assertEqual(3, verify.MAX_SEMANTIC_STAGE_ATTEMPTS)
        self.assertIn("copy only the exact top-level `id` field", query)
        self.assertIn("Never construct, infer, transform, autocomplete, or copy an ID", query)
        self.assertIn("read each candidate once and answer immediately", query)
        self.assertIn("Search again or navigate only when", query)
        self.assertIn("answer_facts[i]", query)
        self.assertIn("Never answer from or paraphrase a search or navigation summary", query)
        self.assertEqual(3, verify.MAX_QUERY_ATTEMPTS)
        self.assertEqual(3, verify.WARM_READINESS_REQUIRED_CONSECUTIVE)
        self.assertTrue(all(len(stem) >= 70 for stem in verify.WARM_READINESS_PROBE_STEMS))

    def test_query_trace_accepts_groundable_read_recovery_but_rejects_nonpublic_paths(self) -> None:
        search = verify.codex_session.ToolCall(
            "ownward_search",
            {"query": "x"},
            {"results": [{"id": "observed", "source": {"id": "metadata"}}]},
            False,
        )
        failed = verify.codex_session.ToolCall("ownward_read", {"id": "observed"}, None, True)
        recovered = verify.codex_session.ToolCall(
            "ownward_read", {"id": "observed"}, {"information": {"id": "observed", "content": "fact"}}, False,
        )
        trace = SimpleNamespace(calls=[search, failed, recovered], bypassed=False)
        self.assertEqual(
            {"tool_calls": 3, "successful_tool_calls": 2, "failed_tool_calls": 1},
            verify._query_trace_metrics(trace),
        )
        self.assertEqual({"observed"}, verify._successfully_read_ids(trace))
        self.assertTrue(verify._grounded_query_answer(trace, ["observed"], ["fact"]))
        summarized = verify.codex_session.ToolCall(
            "ownward_search", {"query": "x"}, {"results": [{"id": "observed", "summary": "A paraphrased fact"}]}, False,
        )
        self.assertFalse(verify._grounded_query_answer(
            SimpleNamespace(calls=[summarized, recovered]), ["observed"], ["A paraphrased fact"],
        ))
        second_read = verify.codex_session.ToolCall(
            "ownward_read", {"id": "second"}, {"information": {"id": "second", "content": "second fact"}}, False,
        )
        self.assertFalse(verify._grounded_query_answer(
            SimpleNamespace(calls=[recovered, second_read]), ["observed", "second"], ["second fact", "fact"],
        ))
        with self.assertRaisesRegex(RuntimeError, "was not successfully read"):
            verify._grounded_query_answer(SimpleNamespace(calls=[search]), ["observed"], ["fact"])
        mutation = verify.codex_session.ToolCall("ownward_update", {}, {}, False)
        with self.assertRaisesRegex(RuntimeError, "outside public search/read/navigate"):
            verify._query_trace_metrics(SimpleNamespace(calls=[search, mutation], bypassed=False))
        for unobserved in ("missing", "metadata"):
            fabricated = verify.codex_session.ToolCall("ownward_read", {"id": unobserved}, None, True)
            with self.assertRaisesRegex(RuntimeError, "not observed from an earlier public result"):
                verify._query_trace_metrics(SimpleNamespace(calls=[search, fabricated], bypassed=False))
        invalid_navigation = verify.codex_session.ToolCall(
            "ownward_navigate", {"start_ids": ["observed", "missing"]}, None, True,
        )
        with self.assertRaisesRegex(RuntimeError, "not observed from an earlier public result"):
            verify._query_trace_metrics(SimpleNamespace(calls=[search, invalid_navigation], bypassed=False))
        mismatched_read = verify.codex_session.ToolCall(
            "ownward_read", {"id": "observed"}, {"information": {"id": "other"}}, False,
        )
        with self.assertRaisesRegex(RuntimeError, "did not bind the requested information ID"):
            verify._query_trace_metrics(SimpleNamespace(calls=[search, mismatched_read], bypassed=False))

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

    def test_empty_ownward_resource_discovery_is_protocol_metadata_not_a_product_bypass(self) -> None:
        events = [
            {"type": "thread.started", "thread_id": "session"},
            {"type": "item.completed", "item": {
                "type": "mcp_tool_call", "server": "ownward", "tool": "list_mcp_resources",
                "status": "completed", "error": None, "arguments": {"server": "ownward"},
                "result": {"content": [{"type": "text", "text": '{"server":"ownward","resources":[]}'}], "structured_content": None},
            }},
            {"type": "item.completed", "item": {
                "type": "mcp_tool_call", "server": "ownward", "tool": "ownward_semantic_work",
                "status": "completed", "error": None, "arguments": {"asset_ids": ["asset"]},
                "result": {"structured_content": {"work": []}},
            }},
        ]
        trace = verify.codex_session.load_exec_events("\n".join(json.dumps(item) for item in events))
        self.assertFalse(trace.bypassed)
        self.assertEqual(("list_mcp_resources:empty",), trace.protocol_operations)
        self.assertEqual(["ownward_semantic_work"], [call.name for call in trace.calls])

        events[1]["item"]["result"]["content"][0]["text"] = '{"server":"ownward","resources":[{"uri":"secret"}]}'
        trace = verify.codex_session.load_exec_events("\n".join(json.dumps(item) for item in events))
        self.assertTrue(trace.bypassed)

    def test_semantic_unit_retries_only_a_no_call_attempt_and_preserves_both_traces(self) -> None:
        asset_id = "asset"
        work_id = "work"
        no_call = SimpleNamespace(calls=[], bypassed=False, bypass_operations=(), protocol_operations=())
        work = verify.codex_session.ToolCall(
            "ownward_semantic_work",
            {"asset_ids": [asset_id]},
            {"work": [{"id": work_id, "asset": {"id": asset_id, "revision": 1}}]},
            False,
        )
        submit = verify.codex_session.ToolCall(
            "ownward_semantic_submit",
            {"submission": {"work_id": work_id, "asset_id": asset_id, "asset_revision": 1}},
            {"organization": {"status": "ready"}},
            False,
        )
        valid = SimpleNamespace(
            calls=[work, submit], bypassed=False, bypass_operations=(),
            protocol_operations=("list_mcp_resources:empty",),
        )

        def run(*_positional: object, **keyword: object) -> tuple[dict[str, int], object, float]:
            stage = keyword["stage"]
            assert isinstance(stage, Path)
            stage.mkdir(parents=True)
            (stage / "output.json").write_text('{"processed":1,"uncertain":0}', encoding="utf-8")
            (stage / "events.jsonl").write_text("events", encoding="utf-8")
            (stage / "stderr.txt").write_text("", encoding="utf-8")
            trace = no_call if stage.name == "attempt-001" else valid
            return {"processed": 1, "uncertain": 0}, trace, 2.0

        runtime = SimpleNamespace(
            binding=SimpleNamespace(endpoint="http://127.0.0.1:1", bearer_token="token"),
            client=SimpleNamespace(call_tool=mock.Mock(return_value={"organization": {"status": "ready"}})),
        )
        args = SimpleNamespace(stage_timeout=240, codex_model="model")
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(verify, "_run_codex", side_effect=run):
            root = Path(directory)
            elapsed, identity, evidence = verify._complete_semantic_unit(
                args, runtime, root, root / "semantic-initial" / "unit", asset_id, 1, time.monotonic() + 300,
            )
            self.assertEqual(4.0, elapsed)
            self.assertEqual(2, identity["agent_attempts"])
            self.assertEqual(["list_mcp_resources:empty"], identity["protocol_operations"])
            self.assertIn("semantic-initial/unit/attempt-001/attempt.json", evidence)
            self.assertIn("semantic-initial/unit/attempt-002/events.jsonl", evidence)

    def test_semantic_unit_retries_work_without_submit_and_commits_complete_evidence(self) -> None:
        asset_id = "asset"
        work_id = "work"
        work = verify.codex_session.ToolCall(
            "ownward_semantic_work",
            {"asset_ids": [asset_id]},
            {"work": [{"id": work_id, "asset": {"id": asset_id, "revision": 1}}]},
            False,
        )
        partial = SimpleNamespace(
            calls=[work], bypassed=False, bypass_operations=(), protocol_operations=("list_mcp_resources:empty",),
        )
        submit = verify.codex_session.ToolCall(
            "ownward_semantic_submit",
            {"submission": {"work_id": work_id, "asset_id": asset_id, "asset_revision": 1}},
            {"organization": {"status": "ready"}},
            False,
        )
        valid = SimpleNamespace(calls=[work, submit], bypassed=False, bypass_operations=(), protocol_operations=())

        def run(*_positional: object, **keyword: object) -> tuple[dict[str, int], object, float]:
            stage = keyword["stage"]
            assert isinstance(stage, Path)
            stage.mkdir(parents=True)
            (stage / "output.json").write_text('{"processed":1,"uncertain":0}', encoding="utf-8")
            (stage / "events.jsonl").write_text("events", encoding="utf-8")
            (stage / "stderr.txt").write_text("", encoding="utf-8")
            return {"processed": 1, "uncertain": 0}, partial if stage.name == "attempt-001" else valid, 2.0

        runtime = SimpleNamespace(
            binding=SimpleNamespace(endpoint="http://127.0.0.1:1", bearer_token="token"),
            client=SimpleNamespace(call_tool=mock.Mock(return_value={"organization": {"status": "ready"}})),
        )
        args = SimpleNamespace(stage_timeout=240, codex_model="model")
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(verify, "_run_codex", side_effect=run):
            root = Path(directory)
            elapsed, identity, evidence = verify._complete_semantic_unit(
                args, runtime, root, root / "semantic-initial" / "unit", asset_id, 1, time.monotonic() + 300,
            )
            progress = {"completed_units": {"initial:asset": identity}, "evidence": evidence}
            self.assertEqual(4.0, elapsed)
            self.assertEqual(2, identity["agent_attempts"])
            self.assertTrue(verify._progress_evidence_complete(progress))
            rejected = verify.load_json(root / "semantic-initial" / "unit" / "attempt-001" / "attempt.json")
            self.assertEqual("no-successful-semantic-submit", rejected["reason"])

            evidence.pop("semantic-initial/unit/attempt-001/attempt.json")
            self.assertFalse(verify._progress_evidence_complete(progress))

    def test_semantic_unit_classifies_an_interrupted_partial_attempt_before_retrying(self) -> None:
        asset_id = "asset"
        work_id = "work"
        work = verify.codex_session.ToolCall(
            "ownward_semantic_work",
            {"asset_ids": [asset_id]},
            {"work": [{"id": work_id, "asset": {"id": asset_id, "revision": 1}}]},
            False,
        )
        submit = verify.codex_session.ToolCall(
            "ownward_semantic_submit",
            {"submission": {"work_id": work_id, "asset_id": asset_id, "asset_revision": 1}},
            {"organization": {"status": "ready"}},
            False,
        )
        valid = SimpleNamespace(calls=[work, submit], bypassed=False, bypass_operations=(), protocol_operations=())

        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            first = root / "semantic-initial" / "unit" / "attempt-001"
            first.mkdir(parents=True)
            events = [
                {"type": "thread.started", "thread_id": "session"},
                {"type": "item.completed", "item": {
                    "type": "mcp_tool_call", "server": "ownward", "tool": "ownward_semantic_work",
                    "status": "completed", "error": None, "arguments": {"asset_ids": [asset_id]},
                    "result": {"structured_content": {"work": [{
                        "id": work_id, "asset": {"id": asset_id, "revision": 1},
                    }]}},
                }},
            ]
            (first / "events.jsonl").write_text("\n".join(json.dumps(item) for item in events), encoding="utf-8")
            (first / "stderr.txt").write_text("", encoding="utf-8")

            def run(*_positional: object, **keyword: object) -> tuple[dict[str, int], object, float]:
                stage = keyword["stage"]
                assert isinstance(stage, Path)
                stage.mkdir(parents=True)
                (stage / "output.json").write_text('{"processed":1,"uncertain":0}', encoding="utf-8")
                (stage / "events.jsonl").write_text("events", encoding="utf-8")
                (stage / "stderr.txt").write_text("", encoding="utf-8")
                return {"processed": 1, "uncertain": 0}, valid, 2.0

            runtime = SimpleNamespace(
                binding=SimpleNamespace(endpoint="http://127.0.0.1:1", bearer_token="token"),
                client=SimpleNamespace(call_tool=mock.Mock(return_value={"organization": {"status": "ready"}})),
            )
            args = SimpleNamespace(stage_timeout=240, codex_model="model")
            with mock.patch.object(verify, "_run_codex", side_effect=run):
                elapsed, identity, evidence = verify._complete_semantic_unit(
                    args, runtime, root, root / "semantic-initial" / "unit",
                    asset_id, 1, time.monotonic() + 300,
                )
            record = verify.load_json(first / "attempt.json")
            self.assertEqual("interrupted-no-successful-semantic-submit", record["reason"])
            self.assertTrue(record["elapsed_unavailable"])
            self.assertEqual(2, identity["agent_attempts"])
            self.assertEqual(2.0, elapsed)
            self.assertTrue(verify._progress_evidence_complete({
                "completed_units": {"initial:asset": identity}, "evidence": evidence,
            }))

    def test_semantic_unit_recovers_an_interrupted_successful_submit_without_retrying(self) -> None:
        asset_id = "asset"
        work_id = "work"
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            first = root / "semantic-initial" / "unit" / "attempt-001"
            first.mkdir(parents=True)
            events = [
                {"type": "thread.started", "thread_id": "session"},
                {"type": "item.completed", "item": {
                    "type": "mcp_tool_call", "server": "ownward", "tool": "ownward_semantic_work",
                    "status": "completed", "error": None, "arguments": {"asset_ids": [asset_id]},
                    "result": {"structured_content": {"work": [{
                        "id": work_id, "asset": {"id": asset_id, "revision": 1},
                    }]}},
                }},
                {"type": "item.completed", "item": {
                    "type": "mcp_tool_call", "server": "ownward", "tool": "ownward_semantic_submit",
                    "status": "completed", "error": None,
                    "arguments": {"submission": {
                        "work_id": work_id, "asset_id": asset_id, "asset_revision": 1,
                    }},
                    "result": {"structured_content": {"organization": {"status": "ready"}}},
                }},
            ]
            (first / "events.jsonl").write_text("\n".join(json.dumps(item) for item in events), encoding="utf-8")
            (first / "stderr.txt").write_text("", encoding="utf-8")
            runtime = SimpleNamespace(
                binding=SimpleNamespace(endpoint="http://127.0.0.1:1", bearer_token="token"),
                client=SimpleNamespace(call_tool=mock.Mock(return_value={"organization": {"status": "ready"}})),
            )
            args = SimpleNamespace(stage_timeout=240, codex_model="model")
            with mock.patch.object(verify, "_run_codex") as run:
                elapsed, identity, evidence = verify._complete_semantic_unit(
                    args, runtime, root, root / "semantic-initial" / "unit",
                    asset_id, 1, time.monotonic() + 300,
                )
            run.assert_not_called()
            self.assertEqual(0.0, elapsed)
            self.assertEqual("terminal-submit-recovered-after-suite-interruption", identity["completion"])
            self.assertTrue(identity["elapsed_unavailable"])
            self.assertIn("semantic-initial/unit/attempt-001/terminal.json", evidence)

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
                    enabled_tools=verify.SEMANTIC_TOOLS,
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

    def test_semantic_timeout_without_successful_submit_exhausts_bounded_recovery(self) -> None:
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
            with self.assertRaisesRegex(RuntimeError, "without a successful submit in 3 bounded attempts"):
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

    def test_interrupted_codex_temporary_roots_are_scrubbed_without_removing_raw_evidence(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            attempt = root / "scenarios" / "_audit" / "scenario" / "query" / "attempt-001"
            temporary = attempt / "codex-interrupted"
            auth = temporary / "codex-home" / "auth.json"
            auth.parent.mkdir(parents=True)
            auth.write_text("secret", encoding="utf-8")
            events = attempt / "events.jsonl"
            events.write_text("events", encoding="utf-8")
            unrelated = root / "codex-preserved"
            unrelated.mkdir()

            verify._scrub_ephemeral_codex_roots(root)

            self.assertFalse(temporary.exists())
            self.assertTrue(events.is_file())
            self.assertTrue(unrelated.is_dir())

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
            queries = [root / "query" / f"attempt-{index:03d}" for index in (1, 2)]
            for index, query in enumerate(queries):
                query.mkdir(parents=True)
                for name in ("output.json", "events.jsonl", "stderr.txt"):
                    (query / name).write_text(name, encoding="utf-8")
                verify.write_json(query / "attempt.json", {
                    "schema": "ownward.product-query-attempt/v1",
                    "path": query.relative_to(root).as_posix(),
                    "elapsed_seconds": 1.0,
                    "tool_calls": 1,
                    "failed_tool_calls": 1 if index == 0 else 0,
                    "status": "rejected" if index == 0 else "accepted",
                })
            evidence = dict(progress["evidence"])
            for query in queries:
                evidence.update(verify._relative_evidence(root, query))
            attempts = [verify.load_json(query / "attempt.json") for query in queries]
            sealed = {
                "schema": "ownward.product-scenario-agent-checkpoint/v2",
                "binding": {"candidate": "candidate"},
                "progress_sha256": verify.sha256(root / "progress.json"),
                "evidence": evidence,
                "query_attempts": attempts,
                "result": {"passed": True, "agent_query_attempts": 2},
            }
            self.assertTrue(verify._agent_checkpoint_valid(sealed, root, sealed["binding"]))
            sealed["evidence"].pop(next(path for path in sealed["evidence"] if path.endswith("events.jsonl") and path.startswith("query/attempt-002/")))
            self.assertFalse(verify._agent_checkpoint_valid(sealed, root, sealed["binding"]))

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
            binding = {"candidate": "candidate", "question_sha256": "a" * 64, "question_chars": 80}
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
                "schema": "ownward.product-direct-measurement/v4",
                "binding": binding,
                "progress_sha256": verify.json_sha256(progress),
                "agent_checkpoint_sha256": verify.sha256(root / "agent-result.json"),
                "data_tree_sha256": progress["data_tree_sha256"],
                "question_sha256": "a" * 64,
                "query_limit_ms": 600.0,
                "warmup_probe_chars": 80,
                "prior_readiness_failures": {},
                "warmup_samples_ms": [200.0] * 3,
                "warmup_ms": 600.0,
                "latency_ms": 300.0,
                "stage_ms": 2500.0,
                "sampled_peak_mib": 12.0,
                "direct_result": {"results": [{"id": "stable", "signals": ["relation"]}]},
                "direct_ids": ["node"],
                "relation_ids": ["node"],
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
            measurement["question_sha256"] = "b" * 64
            verify.write_json(direct, measurement)
            sealed["direct_evidence_sha256"] = verify.sha256(direct)
            self.assertFalse(verify._sealed_scenario_valid(sealed, root, binding, False))
            measurement["question_sha256"] = "a" * 64
            measurement["latency_ms"] = 700.0
            verify.write_json(direct, measurement)
            self.assertFalse(verify._sealed_scenario_valid(sealed, root, binding, False))

    def test_direct_measurement_uses_the_serialized_live_scenario_runtime(self) -> None:
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
            args.resume = True
            with mock.patch.object(verify.support, "OwnwardRuntime", side_effect=AssertionError("valid agent checkpoint repeated product work")):
                self.assertEqual(
                    agent_result,
                    verify._run_scenario(args, task, binding, 10.0, True, 600.0, "r" * 64, time.monotonic() + 30),
                )
            client = mock.Mock()
            client.call_tool.side_effect = [
                {"results": []},
                {"results": []},
                {"results": []},
                {"results": [{"id": "stable"}]},
            ]
            active = SimpleNamespace(client=client, process=SimpleNamespace(pid=os.getpid()))
            with mock.patch.object(verify.support, "OwnwardRuntime", side_effect=AssertionError("unexpected restart")):
                with mock.patch.object(
                    verify,
                    "_establish_warm_query_readiness",
                    side_effect=verify.WarmReadinessError("not ready", [700.0] * verify.WARM_READINESS_MAX_SAMPLES),
                ):
                    with self.assertRaisesRegex(verify.WarmReadinessError, "not ready"):
                        verify._complete_direct_measurement(
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
                readiness = scenario / "direct" / "attempt-001" / "readiness.json"
                self.assertTrue(readiness.is_file())
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
            self.assertEqual([], result["relation_ids"])
            self.assertTrue(result["within_latency_budget"])
            self.assertEqual(4, client.call_tool.call_count)
            self.assertTrue((scenario / "result.json").is_file())
            measurement = verify.load_json(scenario / "direct" / "attempt-002" / "measurement.json")
            self.assertEqual({readiness.relative_to(scenario).as_posix()}, set(measurement["prior_readiness_failures"]))

            not_ready = mock.Mock()
            not_ready.call_tool.return_value = {"results": []}
            with mock.patch.object(verify.time, "perf_counter", side_effect=[
                value for index in range(verify.WARM_READINESS_MAX_SAMPLES) for value in (index * 0.7, index * 0.7 + 0.7)
            ]):
                with self.assertRaisesRegex(RuntimeError, "did not reach 3 consecutive"):
                    verify._establish_warm_query_readiness(not_ready, 600.0, time.monotonic() + 30, "test", 80)
            self.assertEqual(verify.WARM_READINESS_MAX_SAMPLES, not_ready.call_tool.call_count)

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
            task = {"scenario_id": "scenario-1", "updates": [], "information": [], "query": {"question": "question"}}
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

    def test_full_information_bound_query_cannot_be_selectively_rerun(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            source = {"information": [{"node_id": str(index), "content": str(index)} for index in range(5)], "query": {"question": "question"}}
            binding = {"candidate": "candidate"}
            identity = {
                "binding": binding,
                "task_sha256": verify.json_sha256(source),
                "resource_report_sha256": "r" * 64,
                "query_limit_ms": 600.0,
            }
            verify.write_json(root / "report.json", {
                "schema": "ownward.product-full-information-query-preflight/v3",
                "identity": identity,
                "readiness_failures": {},
                "passed": False,
            })
            args = SimpleNamespace(resume=True)
            with mock.patch.object(verify.support, "OwnwardRuntime", side_effect=AssertionError("bound query repeated")):
                with self.assertRaisesRegex(RuntimeError, "selective rerun is prohibited"):
                    verify._run_full_information_query_preflight(args, source, binding, 600.0, "r" * 64, root)

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
            task = {"scenario_id": "scenario-1", "updates": [], "information": [], "query": {"question": "question"}}
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
