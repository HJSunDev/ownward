from __future__ import annotations

import copy
import json
import tempfile
import time
from pathlib import Path
import unittest

import external_intelligence as subject


def runtime_identity() -> dict[str, object]:
    return subject.RuntimeIdentity(
        driver="test-driver/v1",
        provider="test-provider",
        transport="in-process-test/v1",
        selection_sha256="d" * 64,
        artifact_sha256="a" * 64,
        implementation_sha256="e" * 64,
        credential_locator_sha256="b" * 64,
        max_active=2,
        worker_processes=1,
    ).value()


class ExternalIntelligenceContractTests(unittest.TestCase):
    def test_interrupted_work_is_request_local_and_retry_budget_is_unchanged(self):
        class Transport:
            identity = runtime_identity()
            def invoke(self, **request):
                captured.append(request)
                if len(captured) < 3:
                    error = subject.ExternalIntelligenceTimeout('interrupted')
                    error.working_notes = f'progress {len(captured)}'
                    raise error
                return {'answer': 'done'}, {}, {}
            def diagnostics(self):
                return {'rate_limit_observed': False}
        with tempfile.TemporaryDirectory() as directory:
            captured = []
            executor = subject.ExternalIntelligenceExecutor(Transport())
            arguments = dict(role='reader', prompt='original task', schema={'type': 'object'},
                             model='model', effort='xhigh', timeout_seconds=240, attempts=3,
                             lifecycle=subject.InvocationLifecycle(retrieval_mode='no-tools',
                                 resume_prompt=lambda notes: 'original task\n' + notes))
            _, usage = executor.invoke(stage=Path(directory)/'first', **arguments)
            self.assertEqual(['original task', 'original task\nprogress 1',
                              'original task\nprogress 1\n\nprogress 2'], [r['prompt'] for r in captured])
            self.assertEqual(3, usage['attempts'])
            self.assertTrue(all(r['timeout_seconds'] == 240 and r['effort'] == 'xhigh' for r in captured))
            self.assertTrue((Path(directory)/'first/attempt-001/interrupted-work.json').is_file())
            executor.invoke(stage=Path(directory)/'second', **arguments)
            self.assertEqual('original task', captured[-1]['prompt'])

    def test_ordinary_errors_empty_progress_and_tool_turns_do_not_resume(self):
        for kind in ('ordinary', 'empty', 'tools', 'no-hook'):
            with self.subTest(kind=kind), tempfile.TemporaryDirectory() as directory:
                captured = []
                class Transport:
                    identity = runtime_identity()
                    def invoke(self, **request):
                        captured.append(request)
                        if len(captured) == 1:
                            error = (subject.ExternalIntelligenceError('failed') if kind == 'ordinary'
                                     else subject.ExternalIntelligenceTimeout('interrupted'))
                            error.working_notes = '' if kind == 'empty' else 'progress'
                            raise error
                        return {'answer': 'done'}, {}, {}
                    def diagnostics(self):
                        return {'rate_limit_observed': False}
                subject.ExternalIntelligenceExecutor(Transport()).invoke(
                    role='reader', prompt='original', schema={'type': 'object'}, stage=Path(directory),
                    model='model', effort='xhigh', timeout_seconds=240, attempts=2,
                    lifecycle=subject.InvocationLifecycle(retrieval_mode='no-tools',
                        dynamic_tools=[] if kind == 'tools' else None,
                        resume_prompt=None if kind == 'no-hook' else lambda _: 'resumed'))
                self.assertEqual(['original', 'original'], [r['prompt'] for r in captured])

    def test_judges_use_xhigh_in_transport_and_checkpoint(self) -> None:
        class Transport:
            identity = runtime_identity()

            def invoke(self, **request):
                captured.append(request)
                return {"answer": "done"}, {}, {}

            def diagnostics(self):
                return {"rate_limit_observed": False}

        for role in ("judge", "quality-admission", "quality_admission", "quality-admission-qualification", "reader", "semantic"):
            with self.subTest(role=role), tempfile.TemporaryDirectory() as directory:
                captured = []
                executor = subject.ExternalIntelligenceExecutor(Transport())
                arguments = dict(role=role, prompt="task", schema={"type": "object"},
                                 stage=Path(directory), model="model", effort="medium", timeout_seconds=10, attempts=1)
                executor.invoke(**arguments)
                expected = "medium" if role in {"reader", "semantic"} else "xhigh"
                self.assertEqual(expected, captured[0]["effort"])
                request = json.loads((Path(directory) / "request.json").read_text())
                self.assertEqual(expected, request["reasoning_effort"])
                executor.invoke(**arguments)
                self.assertEqual(1, len(captured))

    def test_instructions_reach_transport_with_and_without_tools(self) -> None:
        class Transport:
            identity = runtime_identity()

            def invoke(self, **request):
                captured.append(request)
                return {"answer": "done"}, {}, {"transport": "fixture"}

            def diagnostics(self):
                return {"rate_limit_observed": False}

        for with_tools in (False, True):
            with self.subTest(with_tools=with_tools), tempfile.TemporaryDirectory() as directory:
                captured = []
                subject.ExternalIntelligenceExecutor(Transport()).invoke(
                    role="reader", prompt="task", schema={"type": "object"}, stage=Path(directory),
                    model="model", effort="high", timeout_seconds=10, attempts=1,
                    lifecycle=subject.InvocationLifecycle(
                        retrieval_mode="tools/v1" if with_tools else "no-tools",
                        base_instructions="Follow the source evidence contract.",
                        dynamic_tools=[] if with_tools else None,
                        tool_handler=(lambda name, args: {}) if with_tools else None,
                    ),
                )
                self.assertEqual("Follow the source evidence contract.", captured[0]["base_instructions"])
                self.assertEqual(with_tools, "dynamic_tools" in captured[0])

    def test_tool_evidence_survives_timeout_and_is_separate_for_each_attempt(self) -> None:
        calls = []

        def tool(name, arguments):
            calls.append({"tool": name, "arguments": arguments})
            if arguments.get("fail"):
                raise ValueError("read budget exhausted")
            return {"content": "source evidence"}

        class Transport:
            identity = runtime_identity()
            count = 0

            def invoke(self, **request):
                self.count += 1
                request["tool_handler"]("search", {"attempt": self.count})
                trace = request["work_dir"].parent / "active-retrieval.json"
                self_test.assertTrue(trace.is_file())
                if self.count == 1:
                    with self_test.assertRaisesRegex(ValueError, "read budget"):
                        request["tool_handler"]("read", {"fail": True})
                    self_test.assertEqual(2, len(json.loads(trace.read_text())["selection_steps"]))
                    raise subject.ExternalIntelligenceTimeout("model timed out after reading")
                return {"answer": "done"}, {}, {"transport": "fixture"}

            def diagnostics(self):
                return {"rate_limit_observed": False, "transport": "fixture"}

        self_test = self
        with tempfile.TemporaryDirectory() as directory:
            stage = Path(directory)
            subject.ExternalIntelligenceExecutor(Transport()).invoke(
                role="reader", prompt="task", schema={"type": "object"}, stage=stage,
                model="model", effort="high", timeout_seconds=10, attempts=2,
                lifecycle=subject.InvocationLifecycle(
                    retrieval_mode="tools/v1", dynamic_tools=[], tool_handler=tool,
                    reset_attempt=calls.clear, report=lambda: {"selection_steps": list(calls)},
                ),
            )
            first = json.loads((stage / "attempt-001/active-retrieval.json").read_text())
            second = json.loads((stage / "attempt-002/active-retrieval.json").read_text())
            self.assertEqual(2, len(first["selection_steps"]))
            self.assertEqual(1, first["selection_steps"][0]["arguments"]["attempt"])
            self.assertEqual(2, second["selection_steps"][0]["arguments"]["attempt"])

    def test_executor_preserves_role_and_reuses_atomic_checkpoint(self) -> None:
        class Transport:
            def __init__(self) -> None:
                self.calls: list[dict[str, object]] = []

            @property
            def identity(self) -> dict[str, object]:
                return runtime_identity()

            def invoke(self, **request: object):
                self.calls.append(request)
                return {"answer": "stable"}, {"input_tokens": 1, "output_tokens": 1}, {"transport": "fixture"}

            def diagnostics(self) -> dict[str, object]:
                return {"rate_limit_observed": False, "transport": "fixture"}

        with tempfile.TemporaryDirectory() as directory:
            transport = Transport()
            executor = subject.ExternalIntelligenceExecutor(transport)
            arguments = {
                "role": "generator",
                "prompt": "prompt",
                "schema": {"type": "object"},
                "stage": Path(directory) / "stage",
                "model": "model",
                "effort": "high",
                "timeout_seconds": 10,
                "attempts": 1,
            }
            first, _usage = executor.invoke(**arguments)
            second, _reused_usage = executor.invoke(**arguments)
            self.assertEqual(first, second)
            self.assertEqual(1, len(transport.calls))
            request = json.loads((Path(directory) / "stage" / "request.json").read_text(encoding="utf-8"))
            self.assertEqual("generator", request["role"])

    def test_provider_server_failure_resumes_without_losing_failed_attempts(self) -> None:
        class Transport:
            identity = runtime_identity()
            unavailable = True
            calls = 0

            def invoke(self, **request):
                self.calls += 1
                if self.unavailable:
                    raise subject.ExternalIntelligenceError('OpenCode turn failed: {"data":{"statusCode":500}}')
                return {"answer": "stable"}, {}, {"transport": "fixture"}

            def diagnostics(self):
                return {"rate_limit_observed": False, "transport": "fixture"}

        with tempfile.TemporaryDirectory() as directory:
            stage = Path(directory)
            transport = Transport()
            executor = subject.ExternalIntelligenceExecutor(transport)
            arguments = dict(role="reader", prompt="prompt", schema={"type": "object"}, stage=stage,
                             model="model", effort="high", timeout_seconds=10, attempts=3)
            with self.assertRaisesRegex(subject.ExternalIntelligenceError, "after 3 bounded attempts"):
                executor.invoke(**arguments)
            self.assertEqual(3, transport.calls)
            failed = {p.parent.name: p.read_bytes() for p in stage.glob("attempt-*/metadata.json")}
            transport.unavailable = False
            value, _usage = executor.invoke(**arguments)
            self.assertEqual({"answer": "stable"}, value)
            self.assertEqual(4, transport.calls)
            archived = next((stage / "_audit").iterdir())
            self.assertEqual(failed, {p.parent.name: p.read_bytes() for p in archived.glob("attempt-*/metadata.json")})
            executor.invoke(**arguments)
            self.assertEqual(4, transport.calls)
            subject._write_json(stage / "attempt-001/metadata.json", {
                "outcome": "failed", "error_type": "ExternalIntelligenceError",
                "error_message": "OpenCode structured output is not strict JSON",
            })
            self.assertFalse(subject._retryable_failed_attempt(stage / "attempt-001"))

    def test_request_identity_binds_runtime_role_and_tools_without_credentials(self) -> None:
        identity, request = subject.request_identity(
            role="reader",
            prompt="prompt",
            schema={"type": "object"},
            model="model",
            effort="high",
            retrieval_mode="tools/v1",
            tool_manifest_identity="c" * 64,
            base_instructions="instructions",
            timeout_seconds=120,
            maximum_attempts=3,
            runtime_identity=runtime_identity(),
        )
        self.assertEqual(identity, request["identity"])
        self.assertEqual(subject.REQUEST_SCHEMA, request["schema"])
        self.assertEqual("reader", request["role"])
        self.assertEqual("instructions", request["base_instructions"])
        self.assertEqual(120, request["timeout_seconds"])
        self.assertEqual(3, request["maximum_attempts"])
        self.assertFalse(request["runtime_identity"]["credential_content_read"])
        changed = runtime_identity()
        changed["driver"] = "other-driver/v1"
        changed_identity, _ = subject.request_identity(
            role="reader",
            prompt="prompt",
            schema={"type": "object"},
            model="model",
            effort="high",
            retrieval_mode="tools/v1",
            tool_manifest_identity="c" * 64,
            base_instructions="instructions",
            timeout_seconds=120,
            maximum_attempts=3,
            runtime_identity=changed,
        )
        self.assertNotEqual(identity, changed_identity)
        changed_role_identity, _ = subject.request_identity(
            role="judge",
            prompt="prompt",
            schema={"type": "object"},
            model="model",
            effort="high",
            retrieval_mode="tools/v1",
            tool_manifest_identity="c" * 64,
            base_instructions="instructions",
            timeout_seconds=120,
            maximum_attempts=3,
            runtime_identity=runtime_identity(),
        )
        self.assertNotEqual(identity, changed_role_identity)

    def test_invalid_runtime_identity_fails_before_use(self) -> None:
        for mutation in (
            lambda value: value.pop("driver"),
            lambda value: value.__setitem__("credential_content_read", True),
            lambda value: value.__setitem__("artifact_sha256", "not-a-digest"),
        ):
            value = copy.deepcopy(runtime_identity())
            mutation(value)
            with self.assertRaises(subject.ExternalIntelligenceError):
                subject.validate_runtime_identity(value)

    def test_runtime_selection_is_versioned_and_provider_explicit(self) -> None:
        selection = subject.load_runtime_selection(Path(__file__).with_name("external-intelligence-runtime.json"))
        self.assertEqual(subject.CONTRACT_SCHEMA, selection["contract"])
        self.assertEqual("opencode-go-api/v1", selection["default_driver"])
        self.assertEqual("opencode-go-api/v1", selection["driver"])
        self.assertEqual("aliyun-bailian", selection["provider"])
        self.assertEqual("opencode-server/v1", subject.select_runtime_implementation(selection, "opencode-server/v1")["driver"])
        qwen = subject.select_runtime_role_profile(selection)
        self.assertEqual({"model": "qwen3.8-flash", "reasoning_effort": "xhigh"}, qwen["reader"])
        self.assertEqual({"model": "qwen3.8-flash", "reasoning_effort": "xhigh"}, qwen["judge"])
        self.assertEqual(64, len(selection["selection_sha256"]))

    def test_implementation_identity_changes_only_for_its_direct_selection(self) -> None:
        selection = subject.load_runtime_selection(Path(__file__).with_name("external-intelligence-runtime.json"))
        codex = subject.select_runtime_implementation(selection, "codex-app-server/v1")["selection_sha256"]
        qwen = subject.select_runtime_implementation(selection, "opencode-server/v1")["selection_sha256"]
        changed = copy.deepcopy(selection)
        next(item for item in changed["implementations"] if item["driver"] == "opencode-server/v1")["models"].append("future-model")
        self.assertEqual(codex, subject.select_runtime_implementation(changed, "codex-app-server/v1")["selection_sha256"])
        self.assertNotEqual(qwen, subject.select_runtime_implementation(changed, "opencode-server/v1")["selection_sha256"])

    def test_runtime_catalog_rejects_duplicate_unknown_and_missing_implementations(self) -> None:
        source = json.loads(Path(__file__).with_name("external-intelligence-runtime.json").read_text(encoding="utf-8"))
        mutations = []
        duplicate = copy.deepcopy(source)
        duplicate["implementations"].append(copy.deepcopy(duplicate["implementations"][0]))
        mutations.append(duplicate)
        unknown_default = copy.deepcopy(source)
        unknown_default["default_driver"] = "missing/v1"
        mutations.append(unknown_default)
        missing = copy.deepcopy(source)
        missing["implementations"] = []
        mutations.append(missing)
        with tempfile.TemporaryDirectory() as directory:
            for index, value in enumerate(mutations):
                path = Path(directory) / f"invalid-{index}.json"
                path.write_text(json.dumps(value), encoding="utf-8")
                with self.assertRaises(subject.ExternalIntelligenceError):
                    subject.load_runtime_selection(path)

    def test_scheduler_is_provider_neutral_and_bounded(self) -> None:
        with subject.BoundedScheduler(2) as scheduler:
            futures = [scheduler.submit(lambda: (time.sleep(0.01), 1)[1]) for _ in range(4)]
            self.assertEqual([1, 1, 1, 1], [future.result() for future in futures])
            self.assertEqual(2, scheduler.snapshot()["max_active"])

    def test_active_longmemeval_orchestration_does_not_import_provider_transport(self) -> None:
        repository = Path(__file__).resolve().parents[2]
        business_paths = (
            repository / "benchmarks" / "longmemeval_s" / "run.py",
            repository / "benchmarks" / "acceptance" / "suite" / "community.py",
            repository / "benchmarks" / "acceptance" / "suite" / "preflight.py",
            repository / "benchmarks" / "acceptance" / "suite" / "kernel_iteration_validation.py",
            repository / "benchmarks" / "acceptance" / "suite" / "kernel_iteration_blind_gate.py",
            repository / "benchmarks" / "acceptance" / "suite" / "kernel_iteration_blind_suite.py",
            repository / "benchmarks" / "acceptance" / "suite" / "kernel_iteration_longmemeval.py",
            repository / "benchmarks" / "acceptance" / "suite" / "kernel_iteration_admission_reliability.py",
            repository / "benchmarks" / "acceptance" / "suite" / "kernel_iteration_answer_sufficiency.py",
        )
        for path in business_paths:
            source = path.read_text(encoding="utf-8")
            self.assertNotIn("import codex_app_server", source, path.name)
            self.assertNotIn("from codex_app_server import", source, path.name)
            for provider in ("codex", "opencode", "go_api"):
                self.assertNotIn(f"import {provider}_external_intelligence", source, path.name)
            if path.name == "kernel_iteration_validation.py":
                self.assertNotIn("module.ExternalIntelligenceCapability", source)
            self.assertNotIn('community.get("codex_binary"', source, path.name)
            self.assertNotIn('community.get("codex_auth_file"', source, path.name)
            self.assertNotIn('config["codex_binary"]', source, path.name)
            self.assertNotIn('config["codex_auth_file"]', source, path.name)
        adapter_source = (
            repository / "benchmarks" / "longmemeval_s" / "external_intelligence_runtime.py"
        ).read_text(encoding="utf-8")
        self.assertNotIn("CodexAppServer(", adapter_source)
        self.assertNotIn("OpenCodeServer(", adapter_source)


if __name__ == "__main__":
    unittest.main()
