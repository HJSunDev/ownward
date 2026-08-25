from __future__ import annotations

import json
from pathlib import Path
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

    def judge(self, prompt: str, _settings: dict):
        return "Kyoto" in prompt, "yes", {"input_tokens": 12, "output_tokens": 1}


class DelayedCodex(FakeCodex):
    def semantics(self, work: list[dict], settings: dict, stage: Path):
        first = int(str(work[0]["asset"]["id"]).split("-")[-1])
        time.sleep({1: 0.06, 21: 0.03, 41: 0.0}[first])
        return super().semantics(work, settings, stage)


class LongMemEvalSAdapterTests(unittest.TestCase):
    def setUp(self) -> None:
        self.protocol = adapter.load_json(Path(adapter.__file__).with_name("protocol.json"))

    def test_protocol_freezes_official_identity_models_and_cost_inventory(self) -> None:
        adapter.validate_protocol(self.protocol)
        self.assertEqual(adapter.OFFICIAL_DATA_SHA256, self.protocol["official"]["data_sha256"])
        self.assertEqual("gpt-5.4", self.protocol["reader"]["model"])
        self.assertEqual("gpt-4o-2024-08-06", self.protocol["judge"]["model"])
        self.assertEqual("codex", self.protocol["memory"]["capability_source"])
        self.assertEqual("codex", self.protocol["reader"]["capability_source"])
        self.assertEqual("official-openai", self.protocol["judge"]["capability_source"])
        self.assertEqual(23867, self.protocol["execution"]["total_sessions"])
        self.assertEqual(1498, self.protocol["execution"]["semantic_batches"])
        self.assertEqual(1498, self.protocol["execution"]["semantic_work_requests"])
        self.assertEqual(8, self.protocol["execution"]["codex_max_active"])
        self.assertEqual(300000, self.protocol["memory"]["semantic_analysis_max_input_chars"])

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
            self.assertNotIn("has_answer", content)
            self.assertNotIn("True", content)

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
                "question": "Which city?", "answer": "Kyoto", "question_date": "today",
                "haystack_dates": ["yesterday"], "haystack_session_ids": ["session-1"],
                "haystack_sessions": [[{"role": "user", "content": "I chose Kyoto."}]],
            }
            FakeRuntime.starts = 0
            with mock.patch.object(adapter, "OwnwardRuntime", FakeRuntime), adapter.CodexScheduler(8) as scheduler:
                result = adapter.process_question(
                    question, root / "run", "identity", binary, embedding,
                    self.protocol, evaluator, lambda: FakeCodex(), lambda: FakeCodex(), scheduler,
                )
                resumed = adapter.process_question(
                    question, root / "run", "identity", binary, embedding,
                    self.protocol, evaluator, lambda: FakeCodex(), lambda: FakeCodex(), scheduler,
                )
            self.assertTrue(result["complete"])
            self.assertTrue(result["autoeval_label"]["label"])
            self.assertEqual(["info-1"], result["retrieval"]["read_ids"])
            self.assertEqual(result, resumed)
            self.assertEqual(1, FakeRuntime.starts)
            checkpoint = adapter.load_json(root / "run" / "questions" / "fixture" / "checkpoint.json")
            self.assertEqual(1, checkpoint["organized_batches"])

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
        settings["semantic_analysis_max_input_chars"] = (len(one_prompt) + len(two_prompt)) // 2
        units = adapter.semantic_analysis_units(frozen, settings, capability)
        self.assertEqual(3, len(units))
        self.assertEqual([item["id"] for item in work], [work_id for unit in units for work_id in unit["work_ids"]])
        self.assertTrue(all(unit["input_chars"] <= settings["semantic_analysis_max_input_chars"] for unit in units))
        self.assertEqual(len({unit["identity"] for unit in units}), len(units))

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

    def test_three_batches_analyze_concurrently_but_submit_in_original_order(self) -> None:
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
                    lambda: DelayedCodex(), lambda: FakeCodex(), scheduler,
                )
            operations = FakeRuntime.last_client.operations
            self.assertEqual(["ownward_semantic_work"] * 3, [name for name, _ in operations[:3]])
            self.assertEqual(["ownward_semantic_submit_batch"] * 3, [name for name, _ in operations[3:]])
            self.assertEqual(
                [2, 1, 0],
                [item["batch_index"] for item in result["semantic_execution"]["analysis_completion_order"]],
            )
            self.assertEqual([0, 1, 2], result["semantic_execution"]["submission_order"])
            self.assertTrue(result["semantic_execution"]["serial_concurrent_equivalent"])

    def test_codex_failure_is_bounded_by_frozen_attempt_count(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = root / "codex.exe"
            auth = root / "auth.json"
            binary.write_bytes(b"codex")
            auth.write_text("{}\n", encoding="utf-8")
            capability = adapter.CodexCapability(binary, auth)
            with mock.patch.object(
                adapter.process_control,
                "run",
                side_effect=adapter.process_control.ProcessTimeout("timeout"),
            ) as run:
                with self.assertRaisesRegex(adapter.AdapterError, "after 3 bounded attempts"):
                    capability._invoke(
                        prompt="prompt", schema={"type": "object"}, stage=root / "stage",
                        model="model", effort="low", timeout_seconds=1, attempts=3,
                    )
            self.assertEqual(3, run.call_count)
            self.assertEqual(3, len(list((root / "stage").glob("attempt-*"))))

    def test_codex_output_validation_retries_inside_the_frozen_bound(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = root / "codex.exe"
            auth = root / "auth.json"
            binary.write_bytes(b"codex")
            auth.write_text("{}\n", encoding="utf-8")
            invocations = 0

            def run(command, **_kwargs):
                nonlocal invocations
                invocations += 1
                output = Path(command[command.index("-o") + 1])
                output.write_text(json.dumps({"items": [] if invocations == 1 else ["ok"]}), encoding="utf-8")
                return subprocess.CompletedProcess(command, 0, '{"type":"turn.completed","usage":{}}\n', "")

            with mock.patch.object(adapter.process_control, "run", side_effect=run):
                value, usage = adapter.CodexCapability(binary, auth)._invoke(
                    prompt="prompt", schema={"type": "object"}, stage=root / "stage",
                    model="model", effort="low", timeout_seconds=1, attempts=3,
                    validate=lambda result: adapter.require(result["items"] == ["ok"], "incomplete output"),
                )
            self.assertEqual({"items": ["ok"]}, value)
            self.assertEqual(2, usage["attempts"])
            self.assertEqual(1, usage["retries"])

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
            manifest = adapter.load_json(adapter.checkpoint_manifest(root, "identity"))
            paths = {item["path"] for item in manifest["files"]}
            self.assertIn("questions/q/reader/codex/complete.json", paths)
            self.assertFalse(any("ownward-data" in path for path in paths))

    def test_codex_is_the_only_semantic_and_reader_capability(self) -> None:
        source = Path(adapter.__file__).read_text(encoding="utf-8")
        self.assertNotIn('client.answer(_answer_prompt', source)
        self.assertIn('capability.answer,', source)
        self.assertIn('codex_scheduler.submit(', source)
        self.assertIn('"capability": {"id": "codex"', source)
        self.assertNotIn('"/responses"', source)

    def test_codex_capability_command_has_no_product_or_openai_api_transport(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary, auth, work, schema, output = (root / name for name in ("codex.exe", "auth.json", "work", "schema.json", "output.json"))
            binary.write_bytes(b"codex")
            auth.write_text("{}\n", encoding="utf-8")
            work.mkdir()
            command = adapter.CodexCapability(binary, auth)._command("model", "low", schema, output, work)
            serialized = " ".join(command)
            self.assertNotIn("mcp_servers", serialized)
            self.assertNotIn("OPENAI_API_KEY", serialized)
            self.assertIn("--output-schema", command)


if __name__ == "__main__":
    unittest.main()
