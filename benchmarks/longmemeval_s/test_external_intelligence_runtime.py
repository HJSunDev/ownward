from __future__ import annotations

import tempfile
from pathlib import Path
import unittest
from unittest import mock

import external_intelligence_runtime as subject


class ExternalIntelligenceRuntimeTests(unittest.TestCase):
    def test_current_adapter_owns_legacy_execution_field_translation(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = root / "codex.exe"
            credential = root / "auth.json"
            binary.write_bytes(b"binary")
            credential.write_bytes(b"credential")
            configuration = subject.configuration_from_execution({
                "codex_binary": str(binary),
                "codex_auth_file": str(credential),
            })
            subject.validate_configuration(configuration)
            self.assertEqual("codex-app-server/v1", configuration.driver)
            self.assertEqual(binary.resolve(), configuration.binary)
            self.assertEqual(credential.resolve(), configuration.credential_file)

    def test_current_adapter_owns_role_field_translation(self) -> None:
        roles = subject.role_profile_from_execution({
            "codex_semantic_model": "semantic",
            "codex_semantic_reasoning_effort": "low",
            "codex_reader_model": "reader",
            "codex_reader_reasoning_effort": "medium",
            "codex_judge_model": "judge",
            "codex_judge_reasoning_effort": "high",
        })
        self.assertEqual({"model": "semantic", "reasoning_effort": "low"}, roles["semantic"])
        self.assertEqual({"model": "reader", "reasoning_effort": "medium"}, roles["reader"])
        self.assertEqual({"model": "judge", "reasoning_effort": "high"}, roles["judge"])

    def test_default_opencode_go_configuration_uses_qualified_qwen_profile(self) -> None:
        roles = {
            role: {"model": "qwen3.8-flash", "reasoning_effort": "xhigh"}
            for role in subject.EXPLICIT_ROLE_KEYS
        }
        roles["judge"] = {"model": "qwen3.8-flash", "reasoning_effort": "medium"}
        value = {
            "external_intelligence": {
                "binary": "opencode.cmd", "credential_file": "auth.json",
            },
        }
        configuration = subject.configuration_from_execution(value)
        self.assertEqual("opencode-server/v1", configuration.driver)
        self.assertEqual(roles, subject.role_profile_from_execution(value))

    def test_explicit_codex_selection_remains_available(self) -> None:
        value = {
            "external_intelligence": {
                "driver": "codex-app-server/v1", "binary": "codex.exe", "credential_file": "auth.json",
            },
        }
        self.assertEqual("codex-app-server/v1", subject.configuration_from_execution(value).driver)
        self.assertEqual("gpt-5.6-luna", subject.role_profile_from_execution(value)["reader"]["model"])

    def test_current_adapter_selection_is_exact_and_auditable(self) -> None:
        self.assertEqual("opencode-server/v1", subject.CURRENT_DRIVER)
        self.assertEqual("opencode-go", subject.CURRENT_PROVIDER)
        self.assertEqual("persistent-independent-worker-pool/v1", subject.CURRENT_TRANSPORT)
        self.assertEqual("one-active-turn-per-worker", subject.CURRENT_WORKER_ISOLATION)

    def test_current_runtime_identity_binds_driver_artifact_and_credential_locator(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = root / "codex.exe"
            credential = root / "auth.json"
            binary.write_bytes(b"binary")
            credential.write_bytes(b"secret-one")
            value = subject.current_runtime_identity(
                driver=subject.CURRENT_DRIVER,
                binary=binary,
                credential_file=credential,
                max_active=2,
                worker_processes=2,
            )
            credential.write_bytes(b"secret-two")
            repeated = subject.current_runtime_identity(
                driver=subject.CURRENT_DRIVER,
                binary=binary,
                credential_file=credential,
                max_active=2,
                worker_processes=2,
            )
            self.assertEqual(value, repeated)
            self.assertFalse(value["credential_content_read"])
            self.assertEqual(subject.CURRENT_DRIVER, value["driver"])

    def test_unknown_driver_fails_before_runtime_creation(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = root / "runtime.exe"
            credential = root / "credential.json"
            binary.write_bytes(b"binary")
            credential.write_bytes(b"secret")
            with self.assertRaisesRegex(Exception, "unsupported external-intelligence driver"):
                subject.current_runtime_identity(
                    driver="unknown/v1",
                    binary=binary,
                    credential_file=credential,
                    max_active=1,
                    worker_processes=1,
                )

    def test_adapter_translates_provider_failure_at_the_boundary(self) -> None:
        pool = mock.Mock()
        codex = subject.selected_implementation("codex-app-server/v1")
        adapter = subject.codex_external_intelligence.CodexTransport(pool, {"driver": codex["driver"]}, codex["provider"])
        pool.invoke.side_effect = subject.codex_external_intelligence.AppServerError("provider failed")
        with self.assertRaisesRegex(subject.ExternalIntelligenceError, "provider failed"):
            subject._StableTransport(adapter).invoke(prompt="test")


if __name__ == "__main__":
    unittest.main()
