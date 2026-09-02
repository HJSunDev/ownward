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
        self.assertEqual("opencode-server/v1", selection["default_driver"])
        self.assertEqual("opencode-server/v1", selection["driver"])
        self.assertEqual("opencode-go", selection["provider"])
        self.assertEqual("opencode-server/v1", subject.select_runtime_implementation(selection, "opencode-server/v1")["driver"])
        qwen = subject.select_runtime_role_profile(selection)
        self.assertEqual({"model": "qwen3.8-flash", "reasoning_effort": "xhigh"}, qwen["reader"])
        self.assertEqual({"model": "qwen3.8-flash", "reasoning_effort": "medium"}, qwen["judge"])
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
            if path.name == "kernel_iteration_validation.py":
                self.assertNotIn("module.ExternalIntelligenceCapability", source)
            self.assertNotIn('community.get("codex_binary"', source, path.name)
            self.assertNotIn('community.get("codex_auth_file"', source, path.name)
            self.assertNotIn('config["codex_binary"]', source, path.name)
            self.assertNotIn('config["codex_auth_file"]', source, path.name)
        adapter_source = (
            repository / "benchmarks" / "longmemeval_s" / "external_intelligence_runtime.py"
        ).read_text(encoding="utf-8")
        self.assertIn("import codex_external_intelligence", adapter_source)
        self.assertIn("import opencode_external_intelligence", adapter_source)
        self.assertNotIn("CodexAppServer(", adapter_source)
        self.assertNotIn("OpenCodeServer(", adapter_source)


if __name__ == "__main__":
    unittest.main()
