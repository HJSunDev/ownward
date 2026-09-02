from __future__ import annotations

from pathlib import Path
import tempfile
import unittest
from unittest import mock

import opencode_external_intelligence as subject
import opencode_qualification as qualification


class OpenCodeExternalIntelligenceTests(unittest.TestCase):
    def test_turn_pins_qwen_model_effort_and_disables_every_unowned_tool(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = root / "opencode.exe"
            credential = root / "auth.json"
            binary.write_bytes(b"runtime")
            credential.write_bytes(b"credential")
            server = subject.OpenCodeServer(
                binary, credential, root / "runtime", provider="opencode-go",
                models=("qwen3.8-flash",), reasoning_efforts=("medium", "xhigh"),
            )
            requests: list[tuple[str, str, object]] = []

            def http(method: str, path: str, body: object = None, **_kwargs: object) -> object:
                requests.append((method, path, body))
                if path.startswith("/experimental/tool/ids"):
                    return ["bash", "read", "write"]
                if path == "/session":
                    return {"id": "session-1"}
                if path.endswith("/message"):
                    return {
                        "info": {
                            "providerID": "opencode-go", "modelID": "qwen3.8-flash", "variant": "xhigh",
                            "finish": "stop", "tokens": {"input": 3, "output": 2, "reasoning": 1, "cache": {"read": 4}},
                        },
                        "parts": [{"type": "text", "text": '{"status":"ok"}'}],
                    }
                return True

            with mock.patch.object(server, "_http", side_effect=http):
                value, usage, metadata = server.invoke(
                    prompt="return status", schema={
                        "type": "object", "additionalProperties": False, "required": ["status"],
                        "properties": {"status": {"type": "string", "enum": ["ok"]}},
                    }, model="opencode-go/qwen3.8-flash", effort="xhigh", work_dir=root,
                    timeout_seconds=10,
                )
            self.assertEqual({"status": "ok"}, value)
            self.assertEqual(4, usage["cached_input_tokens"])
            message = next(body for method, path, body in requests if method == "POST" and path.endswith("/message"))
            self.assertEqual({"providerID": "opencode-go", "modelID": "qwen3.8-flash"}, message["model"])
            self.assertEqual("xhigh", message["variant"])
            self.assertEqual({"bash": False, "read": False, "write": False}, message["tools"])
            self.assertFalse(metadata["dynamic_tools_enabled"])

    def test_turn_rejects_unsupported_model_effort_and_schema_drift(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            binary = root / "opencode.exe"
            credential = root / "auth.json"
            binary.write_bytes(b"runtime")
            credential.write_bytes(b"credential")
            server = subject.OpenCodeServer(
                binary, credential, root / "runtime", provider="opencode-go",
                models=("qwen3.8-flash",), reasoning_efforts=("medium", "xhigh"),
            )
            common = dict(prompt="x", schema={"type": "object"}, work_dir=root, timeout_seconds=10)
            with self.assertRaisesRegex(subject.OpenCodeError, "not declared"):
                server.invoke(model="other", effort="xhigh", **common)
            with self.assertRaisesRegex(subject.OpenCodeError, "not declared"):
                server.invoke(model="qwen3.8-flash", effort="low", **common)

            def http(_method: str, path: str, _body: object = None, **_kwargs: object) -> object:
                if path.startswith("/experimental/tool/ids"):
                    return []
                if path == "/session":
                    return {"id": "session-1"}
                if path.endswith("/message"):
                    return {
                        "info": {"providerID": "opencode-go", "modelID": "qwen3.8-flash", "variant": "xhigh", "finish": "stop", "tokens": {}},
                        "parts": [{"type": "text", "text": '{"answer":7}'}],
                    }
                return True

            with mock.patch.object(server, "_http", side_effect=http):
                with self.assertRaisesRegex(subject.OpenCodeError, "expected string"):
                    server.invoke(
                        model="qwen3.8-flash", effort="xhigh", prompt="x", work_dir=root, timeout_seconds=10,
                        schema={"type": "object", "required": ["answer"], "properties": {"answer": {"type": "string"}}},
                    )

    def test_schema_validation_covers_generation_constraints(self) -> None:
        schema = {
            "type": "object", "required": ["date", "scores"], "additionalProperties": False,
            "properties": {
                "date": {"type": "string", "minLength": 10, "maxLength": 10, "pattern": "^[0-9]{4}-[0-9]{2}-[0-9]{2}$"},
                "scores": {"type": "array", "minItems": 2, "uniqueItems": True, "items": {"type": "integer", "minimum": 1, "maximum": 5}},
            },
        }
        subject._validate_schema({"date": "2026-09-02", "scores": [1, 5]}, schema)
        for invalid in (
            {"date": "September", "scores": [1, 5]},
            {"date": "2026-09-02", "scores": [1, 1]},
            {"date": "2026-09-02", "scores": [0, 5]},
        ):
            with self.assertRaises(subject.OpenCodeError):
                subject._validate_schema(invalid, schema)

    def test_mcp_manifest_projects_only_declared_dynamic_tools(self) -> None:
        tools = subject.OpenCodeServer._mcp_tools([{
            "type": "function", "name": "ownward_search", "description": "search",
            "inputSchema": {"type": "object"}, "deferLoading": False,
        }])
        self.assertEqual([{
            "name": "ownward_search", "description": "search", "inputSchema": {"type": "object"},
        }], tools)
        with self.assertRaises(subject.OpenCodeError):
            subject.OpenCodeServer._mcp_tools([{"name": "missing-contract"}])

    def test_native_launcher_resolution_and_implementation_identity_are_stable(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            launcher = root / "opencode.cmd"
            native = root / "node_modules" / "opencode-ai" / "bin" / "opencode.exe"
            native.parent.mkdir(parents=True)
            launcher.write_bytes(b"launcher")
            native.write_bytes(b"native")
            self.assertEqual(native.resolve(), subject.resolve_native_binary(launcher))
            self.assertEqual(subject.implementation_sha256(), subject.implementation_sha256())

    def test_pool_recovers_when_a_replacement_worker_cannot_start_once(self) -> None:
        class Worker:
            def __init__(self, generation: int) -> None:
                self.generation = generation

            def __enter__(self):
                if self.generation == 1:
                    raise subject.OpenCodeError("replacement failed")
                return self

            def __exit__(self, *_args: object) -> None:
                return None

            def invoke(self, **_request: object):
                if self.generation == 0:
                    raise subject.OpenCodeError("turn failed")
                return {"ok": True}, {}, {"transport": "fixture"}

            def diagnostics(self):
                return {"rate_limit_observed": False}

        pool = subject.OpenCodePool(1, lambda _index, generation: Worker(generation))
        with pool:
            with self.assertRaisesRegex(subject.OpenCodeError, "replacement failed"):
                pool.invoke()
            value, _usage, metadata = pool.invoke()
        self.assertEqual({"ok": True}, value)
        self.assertEqual(2, metadata["pool_worker_generation"])

    def test_qualification_selects_reasoning_effort_for_each_role_independently(self) -> None:
        calls: list[tuple[str, str]] = []

        def generator(effort: str) -> dict[str, object]:
            calls.append(("generator", effort))
            return {"passed": True}

        def judge(effort: str) -> dict[str, object]:
            calls.append(("judge", effort))
            return {"passed": True}

        generator_effort, _result, generator_failures = qualification._select_role_effort("generator", generator)
        judge_effort, _result, judge_failures = qualification._select_role_effort("judge", judge)

        self.assertEqual("xhigh", generator_effort)
        self.assertEqual({}, generator_failures)
        self.assertEqual("medium", judge_effort)
        self.assertEqual({}, judge_failures)
        self.assertEqual([("generator", "xhigh"), ("judge", "medium")], calls)

    def test_terminal_selection_requires_current_qualification_identity_and_is_read_only(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "selection.json"
            content = {
                "schema": qualification.SCHEMA,
                "qualification_identity": "a" * 64,
                "passed": True,
                "roles": {},
            }
            selection = {**content, "selection_identity": qualification.canonical_sha256(content)}
            qualification._atomic_json(path, selection)
            before = path.read_bytes()
            self.assertEqual(selection, qualification._load_terminal_selection(path, "a" * 64))
            self.assertEqual(before, path.read_bytes())
            self.assertIsNone(qualification._load_terminal_selection(path, "b" * 64))
            self.assertFalse(path.exists())
            self.assertEqual(before, (path.parent / "_audit" / f"selection-{'a' * 64}.json").read_bytes())


if __name__ == "__main__":
    unittest.main()
