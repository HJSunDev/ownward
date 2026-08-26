from __future__ import annotations

import json
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
import queue
import subprocess
import tempfile
import time
import unittest
from unittest import mock

import run as adapter


class FakeToolClient:
    def __init__(self) -> None:
        self.contents: dict[str, str] = {}
        self.operations: list[tuple[str, list[str]]] = []

    def call_tool(self, name: str, arguments: dict):
        if name == "ownward_create_batch":
            values = []
            for item in arguments["items"]:
                identifier = f"info-{len(self.contents) + 1}"
                self.contents[identifier] = item["content"]
                values.append({"result": {"information": {"id": identifier}}})
            return {"results": values}
        if name == "ownward_search":
            return {"results": [{"id": identifier, "score": 1.0, "signals": ["lexical"]} for identifier in self.contents]}
        if name == "ownward_read":
            identifier = arguments["id"]
            return {"information": {"id": identifier, "content": self.contents[identifier]}}
        if name == "ownward_semantic_work":
            self.operations.append((name, list(arguments["asset_ids"])))
            return {"work": [
                {"id": f"work-{identifier}", "asset": {"id": identifier, "revision": 1, "content": self.contents[identifier]}, "candidates": []}
                for identifier in arguments["asset_ids"]
            ]}
        if name == "ownward_semantic_submit_batch":
            self.operations.append((name, [item["asset_id"] for item in arguments["submissions"]]))
            return {"results": [{"result": {"organization": {"status": "ready"}}} for _ in arguments["submissions"]]}
        raise AssertionError(name)


class FakeRuntime:
    starts = 0
    last_client: FakeToolClient | None = None

    def __init__(self, *_args, **_kwargs) -> None:
        self.client = FakeToolClient()
        FakeRuntime.last_client = self.client
        self.binding = object()

    def __enter__(self):
        FakeRuntime.starts += 1
        return self

    def __exit__(self, *_args) -> None:
        return None


class FakeCodex:
    def semantics(self, work: list[dict], _settings: dict, _stage: Path):
        return ([{"work_id": item["id"], "summary": item["asset"]["content"], "topics": [], "cues": []} for item in work], {
            "input_tokens": 10, "output_tokens": 2, "calls": 1, "attempts": 1, "retries": 0,
            "rate_limit_events": 0, "interrupted_attempts": 0, "wall_seconds": 0.01,
        })

    def semantic_request(self, work: list[dict], settings: dict):
        real = object.__new__(adapter.CodexCapability)
        return adapter.CodexCapability.semantic_request(real, work, settings)

    def answer(self, prompt: str, _settings: dict, _stage: Path):
        if "Kyoto" in prompt:
            return "Kyoto", {"input_tokens": 10, "output_tokens": 1}
        return "unknown", {
            "input_tokens": 10, "output_tokens": 1, "calls": 1, "attempts": 1, "retries": 0,
            "rate_limit_events": 0, "interrupted_attempts": 0, "wall_seconds": 0.01,
        }

    def judge(self, prompt: str, _settings: dict, _stage: Path):
        return "Kyoto" in prompt, "yes", {
            "input_tokens": 12, "output_tokens": 1, "calls": 1, "attempts": 1, "retries": 0,
            "rate_limit_events": 0, "interrupted_attempts": 0, "wall_seconds": 0.01,
        }


class DelayedCodex(FakeCodex):
    def semantics(self, work: list[dict], settings: dict, stage: Path):
        first = int(str(work[0]["asset"]["id"]).split("-")[-1])
        time.sleep({1: 0.06, 21: 0.03, 41: 0.0}[first])
        return super().semantics(work, settings, stage)


class FakeTransport:
    def __init__(self, outputs: list[dict] | None = None, error: Exception | None = None) -> None:
        self.outputs = list(outputs or [{"items": ["ok"]}])
        self.error = error
        self.calls = 0

    def invoke(self, **_kwargs):
        self.calls += 1
        if self.error is not None:
            raise self.error
        value = self.outputs.pop(0)
        return value, {"input_tokens": 1, "cached_input_tokens": 0, "output_tokens": 1, "reasoning_output_tokens": 0}, {
            "transport": "codex-app-server-stdio", "server_instance": "fixture", "thread_id": f"thread-{self.calls}",
            "turn_id": f"turn-{self.calls}", "thread_ephemeral": True, "sandbox": "read-only", "status": "completed",
        }

    def diagnostics(self):
        return {"rate_limit_observed": False}


class LongMemEvalSAdapterTests(unittest.TestCase):
    def setUp(self) -> None:
        self.protocol = adapter.load_json(Path(adapter.__file__).with_name("protocol.json"))

    def test_protocol_freezes_official_identity_models_and_cost_inventory(self) -> None:
        adapter.validate_protocol(self.protocol)
        self.assertEqual(adapter.OFFICIAL_DATA_SHA256, self.protocol["official"]["data_sha256"])
        self.assertEqual("gpt-5.6-luna", self.protocol["memory"]["semantic_model"])
        self.assertEqual("gpt-5.6-luna", self.protocol["reader"]["model"])
        self.assertEqual("gpt-5.6-terra", self.protocol["judge"]["model"])
        self.assertEqual("codex", self.protocol["memory"]["capability_source"])
        self.assertEqual("codex", self.protocol["reader"]["capability_source"])
        self.assertEqual("codex", self.protocol["judge"]["capability_source"])
        self.assertEqual(adapter.PRODUCTION_PROFILE, self.protocol["acceptance"]["profile"])
        self.assertNotIn("minimum_accuracy", self.protocol["acceptance"])
        self.assertEqual(23867, self.protocol["execution"]["total_sessions"])
        self.assertEqual(1498, self.protocol["execution"]["semantic_batches"])
        self.assertEqual(1498, self.protocol["execution"]["semantic_work_requests"])
        self.assertEqual(8, self.protocol["execution"]["codex_max_active"])
        self.assertEqual("app-server-pool-stdio", self.protocol["execution"]["codex_transport"])
        self.assertEqual(20, self.protocol["memory"]["semantic_analysis_max_works"])
        self.assertEqual("ownward.semantic-deduplicated-body-table/v1", self.protocol["memory"]["semantic_input_representation"])
        self.assertEqual(1050000, self.protocol["memory"]["semantic_context_window_tokens"])
        self.assertEqual(850000, self.protocol["memory"]["semantic_analysis_input_token_upper_bound"])
        self.assertEqual(20400, self.protocol["execution"]["full_wall_seconds"])
        self.assertEqual("not_determined", self.protocol["acceptance"]["quality_assessment_status"])

    def test_official_answer_labels_are_validated_but_never_enter_memory_content(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "fixture.json"
            path.write_text(json.dumps([{
                "question_id": "q", "question_type": "single-session-user", "question": "q?", "answer": "a",
                "haystack_dates": ["date"], "haystack_session_ids": ["s"],
                "haystack_sessions": [[{"role": "user", "content": "memory", "has_answer": True}]],
            }]), encoding="utf-8")
            dataset = adapter.validate_dataset(path, formal=False)
            content = adapter.session_content("s", "date", dataset[0]["haystack_sessions"][0])
            product_question = adapter._product_question({**dataset[0], "answer_session_ids": ["s"]})
            self.assertNotIn("has_answer", content)
            self.assertNotIn("True", content)
            self.assertNotIn("answer", product_question)
            self.assertNotIn("answer_session_ids", product_question)

    def test_question_lifecycle_uses_public_paths_and_reuses_complete_checkpoint(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = root / "ownward.exe"
            codex = root / "codex.exe"
            auth = root / "auth.json"
            embedding = root / "embedding"
            for path in (binary, codex, auth):
                path.write_bytes(b"fixture")
            embedding.mkdir()
            evaluator = root / "evaluate_qa.py"
            evaluator.write_text("def get_anscheck_prompt(task, question, answer, response, abstention=False):\n    return f'{question} {answer} {response}'\n", encoding="utf-8")
            question = {
                "question_id": "fixture", "question_type": "single-session-user",
                "question": "Which city?", "answer": "Kyoto city", "answer_session_ids": ["session-1"], "question_date": "today",
                "haystack_dates": ["yesterday"], "haystack_session_ids": ["session-1"],
                "haystack_sessions": [[{"role": "user", "content": "I chose Kyoto."}]],
            }
            FakeRuntime.starts = 0
            with mock.patch.object(adapter, "OwnwardRuntime", FakeRuntime), adapter.CodexScheduler(8) as scheduler:
                result = adapter.process_question(
                    question, root / "run", "identity", binary, embedding,
                    self.protocol, evaluator, lambda: FakeCodex(), scheduler,
                )
                resumed = adapter.process_question(
                    question, root / "run", "identity", binary, embedding,
                    self.protocol, evaluator, lambda: FakeCodex(), scheduler,
                )
            self.assertTrue(result["complete"])
            self.assertTrue(result["autoeval_label"]["label"])
            self.assertEqual(["info-1"], result["retrieval"]["read_ids"])
            self.assertEqual(result, resumed)
            self.assertEqual(1, FakeRuntime.starts)
            checkpoint = adapter.load_json(root / "run" / "questions" / "fixture" / "checkpoint.json")
            self.assertEqual(1, checkpoint["organized_batches"])
            reader_input = adapter.load_json(root / "run" / "questions" / "fixture" / "reader" / "input.json")
            judge_input = adapter.load_json(root / "run" / "questions" / "fixture" / "judge" / "input.json")
            diagnostic = adapter.load_json(root / "run" / "questions" / "fixture" / "diagnostic.json")
            self.assertNotIn("Kyoto city", reader_input["prompt"])
            self.assertIn("Kyoto city", judge_input["official_prompt"])
            self.assertTrue(diagnostic["post_answer_only"] if "post_answer_only" in diagnostic else diagnostic["diagnostic_only"])
            self.assertEqual(["info-1"], diagnostic["evidence_coverage"]["read_expected"])

    def test_diagnostics_separate_pipeline_failures_and_capability_types(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            trace = root / "semantic" / "batch"
            trace.mkdir(parents=True)
            adapter.write_json(trace / "submission.json", {"submitted": True})
            artifacts = {}
            for name in ("checkpoint", "semantic-plan", "retrieval", "reader-input", "reader-output", "answer", "judge-input", "judge-output"):
                path = root / f"{name}.json"
                adapter.write_json(path, {"name": name})
                artifacts[name] = path
            base = {
                "question_id": "q", "question_type": "multi-session", "answer_session_ids": ["session-1"],
            }
            common = {
                "identity": "identity", "answer": "answer", "correct": False, "assets": ["info-1"],
                "asset_sources": [{"session_id": "session-1", "asset_id": "info-1"}],
                "semantic_trace_root": root / "semantic", "reader_input": artifacts["reader-input"],
                "reader_output": artifacts["reader-output"], "judge_input": artifacts["judge-input"],
                "judge_output": artifacts["judge-output"], "phase_seconds": {}, "usage": {},
                "checkpoint_path": artifacts["checkpoint"], "semantic_plan_path": artifacts["semantic-plan"],
                "retrieval_path": artifacts["retrieval"], "answer_path": artifacts["answer"],
            }
            cases = (
                ([], {"returned": [], "read_ids": []}, "target_evidence_not_search_returned"),
                (["info-1"], {"returned": [], "read_ids": []}, "target_evidence_not_search_returned"),
                (["info-1"], {"returned": [{"id": "info-1"}], "read_ids": []}, "target_evidence_not_read"),
                (["info-1"], {"returned": [{"id": "info-1"}], "read_ids": ["info-1"]}, "evidence_read_answer_incorrect"),
            )
            for organized, retrieval, expected in cases:
                record = adapter._diagnostic_record(base, organized_asset_ids=organized, retrieval=retrieval, **common)
                self.assertEqual(expected, record["first_observed_gap"])
                self.assertEqual("not_determined", record["causal_interpretation"]["status"])
                self.assertEqual("cross_session", record["capability"])
            for question_type, capability in (
                ("knowledge-update", "knowledge_update"),
                ("temporal-reasoning", "temporal_reasoning"),
                ("single-session-assistant", "assistant_memory"),
            ):
                record = adapter._diagnostic_record(
                    {**base, "question_type": question_type}, organized_asset_ids=["info-1"],
                    retrieval={"returned": [{"id": "info-1"}], "read_ids": ["info-1"]}, **common,
                )
                self.assertEqual(capability, record["capability"])
            abstention = adapter._diagnostic_record(
                {"question_id": "q_abs", "question_type": "single-session-user", "answer_session_ids": []},
                organized_asset_ids=["info-1"], retrieval={"returned": [], "read_ids": []}, **common,
            )
            self.assertEqual("abstention_response_incorrect", abstention["first_observed_gap"])
            self.assertEqual("abstention", abstention["capability"])

    def test_failure_record_distinguishes_source_and_semantic_submission_without_gold(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory)
            question = {
                "question_id": "q", "question_type": "multi-session", "question": "q?",
                "haystack_session_ids": ["s1", "s2"], "haystack_dates": ["d1", "d2"],
                "haystack_sessions": [[], []], "answer": "secret", "answer_session_ids": ["s2"],
            }
            adapter.record_question_failure(output, question, RuntimeError("stopped"))
            failure = adapter.load_json(output / "questions" / "q" / "failure.json")
            self.assertEqual("source_session_not_created", failure["first_observed_gap"])
            self.assertFalse(failure["diagnostic_gold_used"])
            self.assertNotIn("answer_session_ids", json.dumps(failure))
            checkpoint = output / "questions" / "q" / "checkpoint.json"
            adapter.write_json(checkpoint, {"asset_sources": [{"session_id": "s1"}, {"session_id": "s2"}], "organized_batches": 0})
            adapter.write_json(output / "questions" / "q" / "semantic-plan.json", {"batches": [{"batch_id": "b"}]})
            adapter.record_question_failure(output, question, RuntimeError("stopped"))
            failure = adapter.load_json(output / "questions" / "q" / "failure.json")
            self.assertEqual("semantic_work_not_fully_submitted", failure["first_observed_gap"])

    def test_semantic_host_uses_public_work_and_submit_paths_with_codex_output(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            runtime = FakeRuntime()
            runtime.client.contents["info-1"] = "I chose Kyoto."
            capability = FakeCodex()
            frozen = adapter.freeze_semantic_batch(runtime, ["info-1"], Path(directory), "question", 0)
            units = adapter.semantic_analysis_units(frozen, self.protocol["memory"], capability)
            results = {unit["unit_index"]: adapter.analyze_semantic_unit(unit, Path(directory), self.protocol["memory"], capability) for unit in units}
            analysis = adapter.combine_semantic_batch(frozen, units, results, Path(directory), self.protocol["memory"])
            trace_value = adapter.submit_semantic_batch(runtime, frozen, analysis, Path(directory))
            trace = next(Path(directory).rglob("submission.json"))
            submitted = adapter.load_json(trace)["submissions"][0]
            self.assertEqual("codex", submitted["capability"]["id"])
            self.assertEqual("work-info-1", submitted["work_id"])
            self.assertEqual(10, trace_value["usage"]["input_tokens"])

    def test_analyzed_unsubmitted_batch_reuses_exact_frozen_result(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            runtime = FakeRuntime()
            runtime.client.contents["info-1"] = "I chose Kyoto."
            capability = mock.Mock(wraps=FakeCodex())
            frozen = adapter.freeze_semantic_batch(runtime, ["info-1"], root, "question", 0)
            units = adapter.semantic_analysis_units(frozen, self.protocol["memory"], capability)
            first_results = {unit["unit_index"]: adapter.analyze_semantic_unit(unit, root, self.protocol["memory"], capability) for unit in units}
            first = adapter.combine_semantic_batch(frozen, units, first_results, root, self.protocol["memory"])
            restored = adapter.freeze_semantic_batch(runtime, ["info-1"], root, "question", 0)
            restored_units = adapter.semantic_analysis_units(restored, self.protocol["memory"], capability)
            second_results = {unit["unit_index"]: adapter.analyze_semantic_unit(unit, root, self.protocol["memory"], capability) for unit in restored_units}
            second = adapter.combine_semantic_batch(restored, restored_units, second_results, root, self.protocol["memory"])
            self.assertEqual(first, second)
            self.assertEqual(1, capability.semantics.call_count)
            self.assertEqual(1, len([item for item in runtime.client.operations if item[0] == "ownward_semantic_work"]))
            self.assertFalse(any(item[0] == "ownward_semantic_submit_batch" for item in runtime.client.operations))

    def test_analysis_units_preserve_every_work_item_and_request_contract(self) -> None:
        capability = FakeCodex()
        work = [
            {"id": f"work-{index}", "asset": {"id": f"info-{index}", "revision": 1, "content": "x" * 2000}, "candidates": []}
            for index in range(3)
        ]
        frozen = {
            "question_identity": "question", "batch_index": 0, "batch_id": "batch",
            "asset_ids": [item["asset"]["id"] for item in work], "work": work, "work_sha256": adapter.canonical_sha256(work),
        }
        settings = dict(self.protocol["memory"])
        one_prompt, _, _ = capability.semantic_request(work[:1], settings)
        two_prompt, _, _ = capability.semantic_request(work[:2], settings)
        settings["semantic_analysis_input_token_upper_bound"] = (len(one_prompt.encode("utf-8")) + len(two_prompt.encode("utf-8"))) // 2
        settings["semantic_analysis_output_token_upper_bound"] = 120000
        settings["semantic_analysis_max_works"] = 60
        units = adapter.semantic_analysis_units(frozen, settings, capability)
        self.assertEqual(3, len(units))
        self.assertEqual([item["id"] for item in work], [work_id for unit in units for work_id in unit["work_ids"]])
        self.assertTrue(all(unit["input_utf8_bytes"] <= settings["semantic_analysis_input_token_upper_bound"] for unit in units))
        self.assertEqual(len({unit["identity"] for unit in units}), len(units))

    def test_deduplicated_semantic_input_is_lossless_and_transmits_each_body_once(self) -> None:
        shared = {
            "id": "info-shared", "revision": 2, "content": "shared body",
            "explicit_contexts": [{"key": "r", "value": "v"}],
            "relations": [{"source_id": "info-1", "target_id": "info-shared", "kind": "related"}],
        }
        work = [
            {"id": "work-1", "asset": {"id": "info-1", "revision": 1, "content": "asset one", "contexts": []}, "candidates": [shared]},
            {"id": "work-2", "asset": {"id": "info-shared", "revision": 2, "content": "shared body", "contexts": []}, "candidates": []},
        ]
        encoded = adapter.CodexCapability.semantic_input(work)
        proof = adapter.CodexCapability.validate_semantic_input(work, encoded)
        serialized = json.dumps(encoded, ensure_ascii=False)
        self.assertTrue(proof["equivalent"])
        self.assertEqual(2, proof["body_count"])
        self.assertEqual(1, serialized.count("shared body"))
        self.assertEqual(["work-1", "work-2"], proof["work_ids"])
        renamed = json.loads(json.dumps(work))
        replacements = {"work-1": "new-work-a", "work-2": "new-work-b", "info-1": "new-info-a", "info-shared": "new-info-b"}

        def replace(value):
            if isinstance(value, str):
                return replacements.get(value, value)
            if isinstance(value, list):
                return [replace(item) for item in value]
            if isinstance(value, dict):
                return {key: replace(item) for key, item in value.items()}
            return value

        renamed = replace(renamed)
        self.assertEqual(
            adapter.CodexCapability.semantic_fact_equivalence_sha256(work),
            adapter.CodexCapability.semantic_fact_equivalence_sha256(renamed),
        )

    def test_global_codex_scheduler_never_exceeds_its_bound(self) -> None:
        active = 0
        maximum = 0

        def work() -> None:
            nonlocal active, maximum
            active += 1
            maximum = max(maximum, active)
            time.sleep(0.02)
            active -= 1

        with adapter.CodexScheduler(8) as scheduler:
            futures = [scheduler.submit(work) for _ in range(24)]
            for future in futures:
                future.result()
            snapshot = scheduler.snapshot()
        self.assertEqual(8, maximum)
        self.assertEqual({"limit": 8, "max_active": 8, "submitted": 24}, snapshot)

    def test_run_rebind_preserves_semantics_and_invalidates_only_changed_downstream_stages(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            question = root / "questions" / "q"
            semantic = question / "semantic-traces" / "batch" / "analysis.json"
            checkpoint = question / "checkpoint.json"
            retrieval = question / "retrieval.json"
            reader = question / "reader" / "output.json"
            judge = question / "judge" / "output.json"
            for path in (semantic, checkpoint, retrieval, reader, judge, question / "result.json"):
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text("{}\n", encoding="utf-8")
            previous = {
                "sha256": "old", "stage_dependencies": {
                    "semantic": "s", "retrieval": "r1", "reader": "a1", "judge": "j1", "diagnostic": "d1",
                },
            }
            current = {
                "sha256": "new", "stage_dependencies": {
                    "semantic": "s", "retrieval": "r2", "reader": "a2", "judge": "j2", "diagnostic": "d2",
                },
            }
            adapter.write_json(root / "identity.json", previous)
            adapter.rebind_run_identity(root, previous, current)
            self.assertTrue(semantic.is_file())
            self.assertTrue(checkpoint.is_file())
            self.assertFalse(retrieval.exists())
            self.assertFalse(reader.exists())
            self.assertFalse(judge.exists())
            self.assertTrue((root / "_audit" / "old" / "questions" / "q" / "retrieval.json").is_file())
            self.assertEqual(current, adapter.load_json(root / "identity.json"))

    def test_interrupted_app_server_runtime_is_cleaned_without_reading_credentials(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            stale = root / ".codex-runtime" / "codex-app-server-stale"
            stale.mkdir(parents=True)
            (stale / "auth.json").write_text("secret fixture", encoding="utf-8")
            cleaned = adapter.clean_stale_codex_runtime_roots(root)
            self.assertEqual(["codex-app-server-stale"], cleaned)
            self.assertFalse((root / ".codex-runtime").exists())
            audit = (root / "_audit" / "transport-cleanup.jsonl").read_text(encoding="utf-8")
            self.assertNotIn("secret fixture", audit)
            self.assertIn('"credential_content_read":false', audit)

    def test_three_natural_batches_analyze_concurrently_but_submit_in_original_order(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = root / "ownward.exe"
            embedding = root / "embedding"
            binary.write_bytes(b"fixture")
            embedding.mkdir()
            evaluator = root / "evaluate_qa.py"
            evaluator.write_text("def get_anscheck_prompt(task, question, answer, response, abstention=False):\n    return response\n", encoding="utf-8")
            sessions = [[{"role": "user", "content": f"Memory {index}."}] for index in range(45)]
            sessions[0] = [{"role": "user", "content": "I chose Kyoto."}]
            question = {
                "question_id": "three-batch", "question_type": "multi-session", "question": "Which city?", "answer": "Kyoto",
                "question_date": "today", "haystack_dates": ["yesterday"] * 45,
                "haystack_session_ids": [f"session-{index}" for index in range(45)], "haystack_sessions": sessions,
            }
            with mock.patch.object(adapter, "OwnwardRuntime", FakeRuntime), adapter.CodexScheduler(8) as scheduler:
                result = adapter.process_question(
                    question, root / "run", "identity", binary, embedding, self.protocol, evaluator,
                    lambda: DelayedCodex(), scheduler,
                )
            operations = FakeRuntime.last_client.operations
            self.assertEqual(["ownward_semantic_work"] * 3, [name for name, _ in operations[:3]])
            self.assertEqual(["ownward_semantic_submit_batch"] * 3, [name for name, _ in operations[3:]])
            self.assertEqual(3, result["semantic_execution"]["analysis_units"])
            self.assertEqual([[0], [1], [2]], sorted(item["batch_indexes"] for item in result["semantic_execution"]["analysis_completion_order"]))
            self.assertEqual([0, 1, 2], result["semantic_execution"]["submission_order"])
            self.assertTrue(result["semantic_execution"]["serial_concurrent_equivalent"])

    def test_dry_plan_uses_real_public_work_without_model_or_submission_and_resumes_exactly(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = root / "ownward.exe"
            embedding = root / "embedding"
            binary.write_bytes(b"fixture")
            embedding.mkdir()
            question = {
                "question_id": "dry", "question_type": "multi-session", "question": "unused",
                "haystack_dates": ["date"] * 45,
                "haystack_session_ids": [f"session-{index}" for index in range(45)],
                "haystack_sessions": [[{"role": "user", "content": f"Memory {index}."}] for index in range(45)],
            }
            with mock.patch.object(adapter, "OwnwardRuntime", FakeRuntime):
                result = adapter.dry_plan_question(question, root / "plan", "identity", binary, embedding, self.protocol)
            self.assertEqual(3, result["semantic_work_batches"])
            self.assertEqual(3, result["analysis_calls"])
            self.assertTrue(result["all_work_preserved"])
            self.assertFalse(result["model_invoked"])
            self.assertFalse(any(name == "ownward_semantic_submit_batch" for name, _ in FakeRuntime.last_client.operations))
            self.assertFalse((root / "plan" / "questions" / "dry" / "ownward-data").exists())
            with mock.patch.object(adapter, "OwnwardRuntime", side_effect=AssertionError("must not restart")):
                resumed = adapter.dry_plan_question(question, root / "plan", "identity", binary, embedding, self.protocol)
            self.assertEqual(result, resumed)

    def test_codex_failure_is_bounded_by_frozen_attempt_count(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            transport = FakeTransport(error=adapter.AppServerTimeout("timeout"))
            capability = adapter.CodexCapability(transport)
            with self.assertRaisesRegex(adapter.AdapterError, "after 3 bounded attempts"):
                capability._invoke(
                    prompt="prompt", schema={"type": "object"}, stage=root / "stage",
                    model="model", effort="low", timeout_seconds=1, attempts=3,
                )
            self.assertEqual(3, transport.calls)
            self.assertEqual(3, len(list((root / "stage").glob("attempt-*"))))

    def test_codex_output_validation_retries_inside_the_frozen_bound(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            transport = FakeTransport(outputs=[{"items": []}, {"items": ["ok"]}])
            value, usage = adapter.CodexCapability(transport)._invoke(
                prompt="prompt", schema={"type": "object"}, stage=root / "stage",
                model="model", effort="low", timeout_seconds=1, attempts=3,
                validate=lambda result: adapter.require(result["items"] == ["ok"], "incomplete output"),
            )
            self.assertEqual({"items": ["ok"]}, value)
            self.assertEqual(2, usage["attempts"])
            self.assertEqual(1, usage["retries"])

    def test_app_server_timeout_interrupts_the_exact_fresh_turn(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            work = root / "work"
            work.mkdir()
            server = adapter.CodexAppServer(root / "codex.exe", root / "auth.json", root / "runtime", ["codex"], {})
            responses = [
                {"thread": {"id": "thread-1"}},
                {"turn": {"id": "turn-1"}},
                {},
            ]
            target = mock.Mock()
            target.get.side_effect = [queue.Empty(), queue.Empty()]
            with mock.patch.object(server, "request", side_effect=responses) as request, mock.patch("codex_app_server.queue.Queue", return_value=target):
                with self.assertRaisesRegex(adapter.AppServerTimeout, "timed out"):
                    server.invoke(
                        prompt="prompt", schema={"type": "object"}, model="model", effort="low",
                        work_dir=work, timeout_seconds=0.001,
                    )
            self.assertEqual(
                mock.call("turn/interrupt", {"threadId": "thread-1", "turnId": "turn-1"}, timeout_seconds=10),
                request.call_args_list[-1],
            )

    def test_app_server_recovers_and_interrupts_a_turn_start_timeout(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            work = root / "work"
            work.mkdir()
            server = adapter.CodexAppServer(root / "codex.exe", root / "auth.json", root / "runtime", ["codex"], {})
            responses = [
                {"thread": {"id": "thread-1"}},
                adapter.AppServerTimeout("turn/start timeout"),
                {"thread": {"turns": [{"id": "turn-1", "status": "inProgress"}]}},
                {},
            ]
            with mock.patch.object(server, "request", side_effect=responses) as request:
                with self.assertRaisesRegex(adapter.AppServerTimeout, "orphan_turn_interrupted=true"):
                    server.invoke(
                        prompt="prompt", schema={"type": "object"}, model="model", effort="low",
                        work_dir=work, timeout_seconds=1,
                    )
            self.assertEqual(
                mock.call("turn/interrupt", {"threadId": "thread-1", "turnId": "turn-1"}, timeout_seconds=10),
                request.call_args_list[-1],
            )

    def test_app_server_pool_allows_only_one_active_turn_per_worker(self) -> None:
        class Worker:
            def __init__(self, index: int, generation: int) -> None:
                self.index = index
                self.generation = generation
                self.active = 0
                self.maximum = 0

            def __enter__(self):
                return self

            def __exit__(self, *_args):
                return None

            def invoke(self, **_request):
                self.active += 1
                self.maximum = max(self.maximum, self.active)
                try:
                    time.sleep(0.02)
                    return {"ok": True}, {}, {"transport": "codex-app-server-stdio"}
                finally:
                    self.active -= 1

            def diagnostics(self):
                return {"rate_limit_observed": False}

        workers: list[Worker] = []

        def factory(index: int, generation: int):
            worker = Worker(index, generation)
            workers.append(worker)
            return worker

        with adapter.CodexAppServerPool(2, factory) as pool:
            with ThreadPoolExecutor(max_workers=4) as executor:
                values = list(executor.map(lambda _: pool.invoke(), range(4)))
            diagnostics = pool.diagnostics()
        self.assertEqual(4, len(values))
        self.assertTrue(all(worker.maximum == 1 for worker in workers))
        self.assertEqual(2, diagnostics["max_active"])
        self.assertEqual(1, diagnostics["per_worker_max_active"])

    def test_app_server_pool_restarts_only_the_failed_worker(self) -> None:
        created: list[tuple[int, int]] = []

        class Worker:
            def __init__(self, index: int, generation: int) -> None:
                self.index = index
                self.generation = generation

            def __enter__(self):
                return self

            def __exit__(self, *_args):
                return None

            def invoke(self, **_request):
                if self.generation == 0:
                    raise adapter.AppServerError("worker failed")
                return {"ok": True}, {}, {"transport": "codex-app-server-stdio"}

            def diagnostics(self):
                return {"rate_limit_observed": False}

        def factory(index: int, generation: int):
            created.append((index, generation))
            return Worker(index, generation)

        with adapter.CodexAppServerPool(1, factory) as pool:
            with self.assertRaises(adapter.AppServerError):
                pool.invoke()
            value, _, metadata = pool.invoke()
            diagnostics = pool.diagnostics()
        self.assertEqual({"ok": True}, value)
        self.assertEqual(1, metadata["pool_worker_generation"])
        self.assertEqual([(0, 0), (0, 1)], created)
        self.assertEqual(1, diagnostics["worker_restarts"])

    def test_submission_package_is_deterministic(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            first = root / "a.json"
            second = root / "b.json"
            first.write_text("a\n", encoding="utf-8")
            second.write_text("b\n", encoding="utf-8")
            package = adapter.deterministic_package(root / "submission.zip", [second, first], root)
            digest = adapter.sha256(package)
            adapter.deterministic_package(package, [first, second], root)
            self.assertEqual(digest, adapter.sha256(package))

    def test_checkpoint_manifest_excludes_mutable_product_data_and_binds_agent_evidence(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            evidence = root / "questions" / "q" / "reader" / "codex" / "complete.json"
            product = root / "questions" / "q" / "ownward-data" / "store.db"
            evidence.parent.mkdir(parents=True)
            product.parent.mkdir(parents=True)
            evidence.write_text("{}\n", encoding="utf-8")
            product.write_bytes(b"data")
            for name in ("hypotheses.jsonl", "official-evaluation.jsonl", "diagnostics.jsonl", "diagnostic-summary.json"):
                (root / name).write_text("{}\n", encoding="utf-8")
            manifest = adapter.load_json(adapter.checkpoint_manifest(root, "identity"))
            paths = {item["path"] for item in manifest["files"]}
            self.assertIn("questions/q/reader/codex/complete.json", paths)
            self.assertFalse(any("ownward-data" in path for path in paths))

    def test_codex_is_the_only_semantic_reader_and_judge_capability(self) -> None:
        source = Path(adapter.__file__).read_text(encoding="utf-8")
        self.assertNotIn('client.answer(_answer_prompt', source)
        self.assertIn('capability.answer,', source)
        self.assertIn('capability.judge,', source)
        self.assertIn('codex_scheduler.submit(', source)
        self.assertIn('"capability": {"id": "codex"', source)
        self.assertNotIn('"/responses"', source)
        self.assertNotIn("OfficialJudgeClient", source)
        self.assertNotIn("OPENAI_API_KEY", source)

    def test_codex_capability_uses_one_app_server_without_ephemeral_cli_processes(self) -> None:
        command = adapter.CodexAppServer.command(["codex"])
        serialized = " ".join(command)
        self.assertIn("app-server", command)
        self.assertNotIn("exec", command)
        self.assertNotIn("--ephemeral", command)
        self.assertNotIn("mcp_servers", serialized)
        self.assertNotIn("OPENAI_API_KEY", serialized)

    def test_app_server_resolves_the_native_codex_process_behind_a_powershell_entry(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            entry = root / "codex.ps1"
            native = root / "node_modules" / "@openai" / "codex" / "node_modules" / "@openai" / "codex-win32-x64" / "vendor" / "target" / "bin" / "codex.exe"
            entry.write_text("wrapper", encoding="utf-8")
            native.parent.mkdir(parents=True)
            native.write_bytes(b"native")
            self.assertEqual([str(native.resolve())], adapter.CodexAppServer.direct_command_prefix(entry, ["pwsh", str(entry)]))


if __name__ == "__main__":
    unittest.main()
