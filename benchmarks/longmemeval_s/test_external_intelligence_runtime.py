from __future__ import annotations

import tempfile
import subprocess
import sys
from pathlib import Path
import unittest
from unittest import mock

import external_intelligence_runtime as subject
from external_intelligence import ExternalIntelligenceExecutor, InvocationLifecycle


class ExternalIntelligenceRuntimeTests(unittest.TestCase):
    def test_adapter_timeout_progress_reaches_the_shared_retry_loop(self):
        for timeout_type in (TimeoutError, subject.ExternalIntelligenceTimeout):
            with self.subTest(timeout_type=timeout_type), tempfile.TemporaryDirectory() as directory:
                transport = mock.Mock()
                transport.identity = subject.RuntimeIdentity(
                    driver='test/v1', provider='test', transport='in-process-test/v1',
                    selection_sha256='d'*64, artifact_sha256='a'*64, implementation_sha256='e'*64,
                    credential_locator_sha256='b'*64, max_active=1, worker_processes=1).value()
                transport.new_scope.return_value = transport
                transport.diagnostics.return_value = {'rate_limit_observed': False}
                error = timeout_type('interrupted')
                error.working_notes = 'preserved work'
                transport.invoke.side_effect = [error, ({'answer': 'done'}, {}, {})]
                adapter = mock.Mock(TransportTimeout=timeout_type, TransportError=RuntimeError)
                stable = subject._StableTransport(transport, adapter).new_scope()
                value, usage = ExternalIntelligenceExecutor(stable).invoke(
                    role='reader', prompt='original', schema={'type': 'object'}, stage=Path(directory),
                    model='model', effort='xhigh', timeout_seconds=240, attempts=2,
                    lifecycle=InvocationLifecycle(retrieval_mode='no-tools',
                        resume_prompt=lambda notes: 'original\n' + notes))
                self.assertEqual({'answer': 'done'}, value)
                self.assertEqual(['original', 'original\npreserved work'],
                                 [call.kwargs['prompt'] for call in transport.invoke.call_args_list])
                self.assertEqual(2, usage['attempts'])
                self.assertTrue((Path(directory)/'attempt-001/interrupted-work.json').is_file())

    def test_each_driver_loads_without_other_implementations(self) -> None:
        for driver, module in subject._ADAPTERS.items():
            unavailable = [name for name in subject._ADAPTERS.values() if name != module]
            code = (
                "import sys; "
                f"sys.modules.update(dict.fromkeys({unavailable!r})); "
                "import external_intelligence_runtime as runtime; "
                f"assert runtime._adapter({driver!r}).DRIVER == {driver!r}"
            )
            with self.subTest(driver=driver):
                completed = subprocess.run(
                    [sys.executable, "-c", code], cwd=Path(subject.__file__).parent,
                    capture_output=True, text=True, timeout=30,
                )
                self.assertEqual(0, completed.returncode, completed.stderr)

    def test_unselected_implementation_changes_do_not_change_selected_identity(self) -> None:
        original = Path.read_bytes
        for driver, module in subject._ADAPTERS.items():
            adapter = subject._adapter(driver)
            baseline = subject._implementation_identity(adapter)
            unrelated = {name + ".py" for name in subject._ADAPTERS.values() if name != module}
            def changed(path):
                return b"unrelated implementation changed" if path.name in unrelated else original(path)
            with self.subTest(driver=driver), mock.patch.object(Path, "read_bytes", changed):
                self.assertEqual(baseline, subject._implementation_identity(adapter))
            def selected_changed(path):
                return b"selected implementation changed" if path.name == module + ".py" else original(path)
            with self.subTest(driver=driver), mock.patch.object(Path, "read_bytes", selected_changed):
                self.assertNotEqual(baseline, subject._implementation_identity(adapter))

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
        self.assertEqual({"model": "judge", "reasoning_effort": "xhigh"}, roles["judge"])

    def test_default_opencode_go_configuration_uses_qualified_qwen_profile(self) -> None:
        roles = {
            role: {"model": "qwen3.8-flash", "reasoning_effort": "xhigh"}
            for role in subject.EXPLICIT_ROLE_KEYS
        }
        roles["judge"] = {"model": "qwen3.8-flash", "reasoning_effort": "xhigh"}
        roles["semantic"] = {"model": "qwen3.8-flash", "reasoning_effort": "medium"}
        value = {
            "external_intelligence": {
                "binary": "opencode.cmd", "credential_file": "auth.json",
            },
        }
        configuration = subject.configuration_from_execution(value)
        self.assertEqual("opencode-go-api/v1", configuration.driver)
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
        self.assertEqual("opencode-go-api/v1", subject.CURRENT_DRIVER)
        self.assertEqual("aliyun-bailian", subject.CURRENT_PROVIDER)
        self.assertEqual("in-process-http/v1", subject.CURRENT_TRANSPORT)
        self.assertEqual("request-local-context", subject.CURRENT_WORKER_ISOLATION)

    def test_current_runtime_identity_binds_driver_artifact_and_credential_locator(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = Path(subject._adapter(subject.CURRENT_DRIVER).__file__)
            credential = root / "auth.json"
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
        adapter = subject._adapter("codex-app-server/v1").CodexTransport(pool, {"driver": codex["driver"]}, codex["provider"])
        pool.invoke.side_effect = subject._adapter("codex-app-server/v1").AppServerError("provider failed")
        with self.assertRaisesRegex(subject.ExternalIntelligenceError, "provider failed"):
            subject._StableTransport(adapter, subject._adapter("codex-app-server/v1")).invoke(prompt="test")


if __name__ == "__main__":
    unittest.main()
