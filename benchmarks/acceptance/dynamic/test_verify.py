from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import sys
import tempfile
import threading
import unittest
from unittest import mock

import verify


class DynamicVerifierTests(unittest.TestCase):
    def test_agent_and_generation_traces_reject_non_json_output(self) -> None:
        with self.assertRaisesRegex(RuntimeError, "non-JSON"):
            verify.parse_agent_trace('{"type":"thread.started","thread_id":"thread-1"}\nnot-json\n')
        with tempfile.TemporaryDirectory() as root_value:
            path = Path(root_value) / "events.jsonl"
            path.write_text('{"type":"thread.started","thread_id":"thread-1"}\nnot-json\n', encoding="utf-8")
            with self.assertRaisesRegex(RuntimeError, "non-JSON"):
                verify._validate_generation_trace(path)

    def test_agent_trace_rejects_non_ownward_operations(self) -> None:
        events = [
            {"type": "thread.started", "thread_id": "thread-1"},
            {
                "type": "item.completed",
                "item": {"type": "command_execution", "status": "completed"},
            },
        ]
        trace = verify.parse_agent_trace("\n".join(json.dumps(value) for value in events))
        self.assertTrue(trace.bypassed)
        self.assertEqual(trace.bypass_operations, ("command_execution",))

    def test_tool_evidence_is_bound_to_information_ids_and_successful_calls(self) -> None:
        evidence = verify._observed_tool_evidence(
            (
                verify.AgentToolCall("ownward_read", {}, {"id": "one", "content": "grounded fact"}, ""),
                verify.AgentToolCall("ownward_read", {}, {"id": "two", "content": "failed fact"}, "failed"),
            )
        )
        self.assertIn("grounded fact", evidence["one"])
        self.assertNotIn("two", evidence)

    def test_semantic_collaboration_allows_only_bounded_correction(self) -> None:
        trace = verify.AgentTrace(
            "thread-1",
            (
                verify.AgentToolCall("ownward_semantic_work", {}, {}, ""),
                verify.AgentToolCall("ownward_semantic_submit_batch", {}, {}, "invalid schema"),
                verify.AgentToolCall("ownward_semantic_submit_batch", {}, {}, ""),
            ),
            False,
            (),
        )
        verify._validate_semantic_collaboration(trace)
        with self.assertRaisesRegex(RuntimeError, "bounded submission attempts"):
            verify._validate_semantic_collaboration(
                verify.AgentTrace(trace.session_id, trace.calls[:1] + trace.calls[1:2] * 3 + trace.calls[-1:], False, ())
            )
        with self.assertRaisesRegex(RuntimeError, "valid submission batch"):
            verify._validate_semantic_collaboration(
                verify.AgentTrace(trace.session_id, (trace.calls[0], trace.calls[2], trace.calls[1]), False, ())
            )

    def test_agent_trace_marks_rejected_semantic_batch_items_as_an_error(self) -> None:
        events = [
            {"type": "thread.started", "thread_id": "thread-1"},
            {
                "type": "item.completed",
                "item": {
                    "type": "mcp_tool_call",
                    "server": "ownward",
                    "tool": "ownward_semantic_submit_batch",
                    "status": "completed",
                    "error": None,
                    "result": {"structured_content": {"results": [{"error": "rejected"}]}},
                },
            },
        ]
        trace = verify.parse_agent_trace("\n".join(json.dumps(value) for value in events))
        self.assertEqual(trace.calls[0].error, "Ownward semantic batch contains rejected items")

    def test_dataset_generation_trace_rejects_tool_use(self) -> None:
        with tempfile.TemporaryDirectory() as root_value:
            path = Path(root_value) / "events.jsonl"
            path.write_text(
                "\n".join(
                    json.dumps(value)
                    for value in (
                        {"type": "thread.started", "thread_id": "thread-1"},
                        {"type": "item.completed", "item": {"type": "command_execution"}},
                    )
                )
                + "\n",
                encoding="utf-8",
            )
            with self.assertRaisesRegex(RuntimeError, "used a tool"):
                verify._validate_generation_trace(path)

    def test_dataset_stage_resume_is_bound_to_runtime_and_prompt(self) -> None:
        with tempfile.TemporaryDirectory() as root_value:
            root = Path(root_value)
            protocol_path = root / "protocol.json"
            codex_binary = root / "codex.ps1"
            output_path = root / "output.json"
            events_path = root / "events.jsonl"
            run_path = root / "run.json"
            protocol_path.write_text("protocol", encoding="utf-8")
            codex_binary.write_text("codex", encoding="utf-8")
            output_path.write_text("{}", encoding="utf-8")
            events_path.write_text(json.dumps({"type": "thread.started", "thread_id": "thread-1"}) + "\n", encoding="utf-8")
            prompt = "frozen prompt"
            binding = {
                "candidate": "a" * 40,
                "protocol_sha256": verify.sha256(protocol_path),
                "codex_binary_sha256": verify.sha256(codex_binary),
                "model": "generator",
                "reasoning_effort": "medium",
                "prompt_sha256": hashlib.sha256(prompt.encode("utf-8")).hexdigest(),
            }
            verify.write_json(
                run_path,
                {
                    "schema": "ownward.dynamic-dataset-stage/v2",
                    "binding": binding,
                    "output_sha256": verify.sha256(output_path),
                    "events_sha256": verify.sha256(events_path),
                    "elapsed_seconds": 1.0,
                },
            )
            args = argparse.Namespace(
                resume=True,
                candidate="a" * 40,
                protocol_path=protocol_path,
                codex_binary=codex_binary,
            )
            value = verify._dataset_stage(
                args,
                name="hidden world",
                model={"model": "generator", "reasoning_effort": "medium"},
                prompt=prompt,
                schema={},
                output_path=output_path,
                events_path=events_path,
                run_path=run_path,
                environment={},
            )
            self.assertEqual(value, {})
            codex_binary.write_text("changed", encoding="utf-8")
            with self.assertRaisesRegex(RuntimeError, "run binding changed"):
                verify._dataset_stage(
                    args,
                    name="hidden world",
                    model={"model": "generator", "reasoning_effort": "medium"},
                    prompt=prompt,
                    schema={},
                    output_path=output_path,
                    events_path=events_path,
                    run_path=run_path,
                    environment={},
                )

    def test_incomplete_codex_stage_can_restart_without_touching_sealed_evidence(self) -> None:
        with tempfile.TemporaryDirectory() as root_value:
            root = Path(root_value)
            output = root / "answers.json"
            events = root / "events.jsonl"
            run = root / "run.json"
            work = events.with_suffix("")
            output.write_text("partial", encoding="utf-8")
            work.mkdir()
            (work / "scratch").write_text("partial", encoding="utf-8")
            verify._clear_incomplete_codex_stage(output, events, run)
            self.assertFalse(output.exists())
            self.assertFalse(work.exists())
            run.write_text("sealed", encoding="utf-8")
            with self.assertRaisesRegex(RuntimeError, "sealed run metadata"):
                verify._clear_incomplete_codex_stage(output, events, run)

    def test_codex_watchdog_stops_a_process_that_exceeds_its_stage_boundary(self) -> None:
        with tempfile.TemporaryDirectory() as root_value:
            events = Path(root_value) / "events.jsonl"
            with self.assertRaisesRegex(RuntimeError, "execution limit"):
                verify._run_codex_with_inactivity_watchdog(
                    [sys.executable, "-c", "import time; time.sleep(5)"],
                    environment=dict(os.environ),
                    events_path=events,
                    inactivity_seconds=2,
                    maximum_seconds=0.2,
                )

    def test_agent_conditions_are_executed_as_a_parallel_pair(self) -> None:
        barrier = threading.Barrier(2, timeout=2)

        def run_agents(*_args: object, condition: str, task_classes: list[str], **_kwargs: object):
            barrier.wait()
            task_class = task_classes[0]
            return ({task_class: {"condition": condition}}, {condition: Path(condition)})

        protocol = {"generation": {"task_classes": ["cross_time"]}, "execution": {"parallel_conditions": 2}}
        with mock.patch.object(verify, "_run_agents", side_effect=run_agents):
            full, _, baseline, _ = verify._run_agent_pairs(
                argparse.Namespace(),
                {},
                {},
                {},
                {},
                {},
                protocol,
                "http://127.0.0.1:1",
                "http://127.0.0.1:2",
            )
        self.assertEqual(full["cross_time"]["condition"], "full")
        self.assertEqual(baseline["cross_time"]["condition"], "baseline")

    def test_asset_conditions_are_executed_as_a_parallel_pair(self) -> None:
        barrier = threading.Barrier(2, timeout=2)

        def verify_assets(*_args: object, condition: str, **_kwargs: object):
            barrier.wait()
            return ({"condition": condition}, [{"condition": condition}])

        args = argparse.Namespace(protocol={"execution": {"parallel_conditions": 2}})
        with mock.patch.object(verify, "_verify_assets", side_effect=verify_assets):
            full, baseline = verify._verify_asset_pair(args, {}, {}, {}, {}, {})
        self.assertEqual(full[0]["condition"], "full")
        self.assertEqual(baseline[0]["condition"], "baseline")

    def test_completed_reports_are_immutable_when_resuming(self) -> None:
        with tempfile.TemporaryDirectory() as root_value:
            path = Path(root_value) / "report.json"
            original = {"schema": "report/v1", "measured_at": "first", "passed": True}
            self.assertEqual(verify._seal_report(path, dict(original), resume=False), original)
            regenerated = {"schema": "report/v1", "measured_at": "second", "passed": True}
            self.assertEqual(verify._seal_report(path, regenerated, resume=True), original)
            changed = {"schema": "report/v1", "measured_at": "third", "passed": False}
            with self.assertRaisesRegex(RuntimeError, "sealed acceptance report changed"):
                verify._seal_report(path, changed, resume=True)

    def test_relation_ablation_reuses_the_frozen_full_state(self) -> None:
        with tempfile.TemporaryDirectory() as root_value:
            root = Path(root_value)
            binary = root / "ownward.exe"
            protocol_path = root / "protocol.json"
            dataset_path = root / "valid-dataset.json"
            for path, value in (
                (binary, "binary"),
                (protocol_path, "protocol"),
                (dataset_path, "dataset"),
            ):
                path.write_text(value, encoding="utf-8")
            full_mapping = {
                "schema": "ownward.dynamic-ingestion/v2",
                "condition": "full",
                "disable_relations": False,
                "data_directory": "full-data",
                "binding": {"candidate": "a" * 40},
                "stable_ids": {"scenario/node": "stable-id"},
                "revisions": {"scenario/node": 2},
                "operation_count": 2,
                "organization_seconds": 1.25,
                "organization_seconds_max": 0.75,
                "organization_seconds_p95": 0.75,
            }
            full_mapping_path = root / "full-mapping.json"
            verify.write_json(full_mapping_path, full_mapping)
            full_data = root / "full-data"
            full_data.mkdir()
            (full_data / "state").write_text("frozen", encoding="utf-8")
            protocol = {
                "product_runtime": {
                    "mode": "release-defaults",
                    "prohibited_environment": list(verify.LEGACY_MODEL_ENVIRONMENT),
                }
            }
            args = argparse.Namespace(
                evidence_dir=root,
                resume=False,
                candidate="a" * 40,
                binary=binary,
                protocol_path=protocol_path,
                protocol=protocol,
            )
            with mock.patch.dict("os.environ", {"OPENAI_API_KEY": "must-not-leak"}):
                baseline, environment = verify._ingest_condition(
                    args,
                    {},
                    condition="baseline",
                    disable_relations=True,
                )
            self.assertEqual(baseline["stable_ids"], full_mapping["stable_ids"])
            self.assertEqual(baseline["revisions"], full_mapping["revisions"])
            self.assertEqual(baseline["operation_count"], full_mapping["operation_count"])
            self.assertEqual(baseline["source_mapping_sha256"], verify.sha256(full_mapping_path))
            self.assertEqual(baseline["source_state_tree_sha256"], baseline["baseline_state_tree_sha256"])
            self.assertEqual(baseline["data_directory"], "baseline-data")
            self.assertEqual(environment["OWNWARD_DISABLE_RELATIONS"], "true")
            self.assertNotIn("OPENAI_API_KEY", environment)
            self.assertTrue((root / "baseline-data").is_dir())

    def test_resumed_agent_runs_keep_original_elapsed_time(self) -> None:
        with tempfile.TemporaryDirectory() as root_value:
            root = Path(root_value)
            task_classes = ["cross_time", "multi_hop", "context_applicability", "information_update"]
            scenarios = []
            stable_ids: dict[str, str] = {}
            for index, task_class in enumerate(task_classes):
                scenario_id = f"scenario-{index}"
                node_id = f"node-{index}"
                stable_id = f"stable-{index}"
                stable_ids[f"{scenario_id}/{node_id}"] = stable_id
                scenarios.append(
                    {
                        "truth": {
                            "id": scenario_id,
                            "task_class": task_class,
                            "query": {
                                "expected_ids": [node_id],
                                "forbidden_ids": [],
                                "answer_facts": [f"fact-{index}"],
                            },
                        },
                        "expression": {"query": {"question": f"question-{index}"}},
                    }
                )
            dataset = {"valid_scenarios": scenarios}
            mapping = {"stable_ids": stable_ids, "data_directory": "full-data"}
            protocol = {
                "generation": {"task_classes": task_classes},
                "models": {"external_agent": {"model": "agent-model", "reasoning_effort": "low"}},
                "execution": {
                    "agent_tool_calls_per_query": 8,
                    "agent_seconds_per_question_max": 60,
                },
            }
            binary = root / "ownward.exe"
            protocol_path = root / "protocol.json"
            codex_binary = root / "codex.ps1"
            dataset_path = root / "valid-dataset.json"
            mapping_path = root / "full-mapping.json"
            for path, value in (
                (binary, "binary"),
                (protocol_path, json.dumps(protocol)),
                (codex_binary, "codex"),
                (dataset_path, json.dumps(dataset)),
                (mapping_path, json.dumps(mapping)),
            ):
                path.write_text(value, encoding="utf-8")
            binding = {
                "candidate": "a" * 40,
                "release_binary_sha256": verify.sha256(binary),
                "protocol_sha256": verify.sha256(protocol_path),
                "dataset_sha256": verify.sha256(dataset_path),
                "mapping_sha256": verify.sha256(mapping_path),
                "codex_binary_sha256": verify.sha256(codex_binary),
            }
            for index, task_class in enumerate(task_classes):
                scenario = scenarios[index]
                prompt = verify._agent_prompt(
                    [
                        {
                            "query_id": scenario["truth"]["id"],
                            "question": scenario["expression"]["query"]["question"],
                        }
                    ]
                )
                output = root / f"full-{task_class}-answers.json"
                output.write_text(
                    json.dumps(
                        {
                            "answers": [
                                {
                                    "query_id": scenario["truth"]["id"],
                                    "answer_facts": scenario["truth"]["query"]["answer_facts"],
                                    "information_ids": [stable_ids[f"{scenario['truth']['id']}/node-{index}"]],
                                }
                            ]
                        }
                    ),
                    encoding="utf-8",
                )
                events = root / f"full-{task_class}.events.jsonl"
                events.write_text(
                    "\n".join(
                        json.dumps(value)
                        for value in [
                            {"type": "thread.started", "thread_id": f"thread-{index}"},
                            {
                                "type": "item.completed",
                                "item": {
                                    "type": "mcp_tool_call",
                                    "server": "ownward",
                                    "tool": "ownward_search",
                                    "status": "completed",
                                    "arguments": {},
                                    "result": {
                                        "structured_content": {
                                            "results": [
                                                {
                                                    "id": stable_ids[f"{scenario['truth']['id']}/node-{index}"],
                                                    "content": f"fact-{index}",
                                                }
                                            ]
                                        }
                                    },
                                },
                            },
                        ]
                    )
                    + "\n",
                    encoding="utf-8",
                )
                run = root / f"full-{task_class}-run.json"
                verify.write_json(
                    run,
                    {
                        "schema": "ownward.dynamic-agent-run/v1",
                        "model": "agent-model",
                        "reasoning_effort": "low",
                        "prompt_sha256": hashlib.sha256(prompt.encode("utf-8")).hexdigest(),
                        "answers_sha256": verify.sha256(output),
                        "events_sha256": verify.sha256(events),
                        "elapsed_seconds": 12.5 + index,
                        "binding": binding,
                    },
                )
            runs, evidence = verify._run_agents(
                argparse.Namespace(
                    evidence_dir=root,
                    resume=True,
                    candidate="a" * 40,
                    binary=binary,
                    protocol_path=protocol_path,
                    codex_binary=codex_binary,
                ),
                dataset,
                mapping,
                {},
                protocol,
                "http://127.0.0.1:1",
                condition="full",
            )
            self.assertEqual(runs["cross_time"]["elapsed_seconds"], 12.5)
            self.assertEqual(len(evidence), 12)
            changed = verify.load_json(root / "full-cross_time-run.json")
            changed["binding"]["candidate"] = "b" * 40
            verify.write_json(root / "full-cross_time-run.json", changed)
            with self.assertRaisesRegex(RuntimeError, "run binding changed"):
                verify._run_agents(
                    argparse.Namespace(
                        evidence_dir=root,
                        resume=True,
                        candidate="a" * 40,
                        binary=binary,
                        protocol_path=protocol_path,
                        codex_binary=codex_binary,
                    ),
                    dataset,
                    mapping,
                    {},
                    protocol,
                    "http://127.0.0.1:1",
                    condition="full",
                )


if __name__ == "__main__":
    unittest.main()
