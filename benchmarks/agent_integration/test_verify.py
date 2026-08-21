from __future__ import annotations

import json
from argparse import Namespace
from pathlib import Path
import tempfile
import unittest

import verify


def _mutation_trace(initial: str, final: str, prompt: str) -> verify.SessionTrace:
    information_v1 = {"id": "I1", "revision": 1, "content": initial}
    information_v2 = {"id": "I1", "revision": 2, "content": final}
    calls = [
        verify.ToolCall("ownward_rules", {}, {"rules": "只保存长期复用的信息，不保存临时工作状态。"}),
        verify.ToolCall("ownward_search", {"query": initial}, {"results": []}),
        verify.ToolCall("ownward_create", {"content": initial}, {"result": {"information": information_v1}}),
        verify.ToolCall("ownward_semantic_work", {"asset_ids": ["I1"]}, {"works": [{"id": "W1"}]}),
        verify.ToolCall(
            "ownward_semantic_submit_batch",
            {"submissions": [{"work_id": "W1", "asset_id": "I1", "status": "complete", "capability": {"id": "codex", "version": "gpt-5.4", "execution": "agent-integration"}}]},
            {"results": [{"work_id": "W1", "organization": {"status": "ready"}}]},
        ),
        verify.ToolCall("ownward_read", {"id": "I1"}, {"information": information_v1}),
        verify.ToolCall("ownward_search", {"query": initial}, {"results": [{"id": "I1"}]}),
        verify.ToolCall(
            "ownward_update",
            {"id": "I1", "expected_revision": 1, "content": final},
            {"result": {"information": information_v2}},
        ),
        verify.ToolCall("ownward_semantic_work", {"asset_ids": ["I1"]}, {"works": [{"id": "W2"}]}),
        verify.ToolCall(
            "ownward_semantic_submit_batch",
            {"submissions": [{"work_id": "W2", "asset_id": "I1", "status": "complete", "capability": {"id": "codex", "version": "gpt-5.4", "execution": "agent-integration"}}]},
            {"results": [{"work_id": "W2", "organization": {"status": "ready"}}]},
        ),
        verify.ToolCall("ownward_read", {"id": "I1"}, {"information": information_v2}),
        verify.ToolCall("ownward_search", {"query": final}, {"results": [{"id": "I1"}]}),
    ]
    return verify.SessionTrace(
        "session-one",
        "Codex/openai",
        calls,
        False,
        prompt,
        verify.EXPECTED_CODEX_MODEL,
        verify.EXPECTED_CODEX_REASONING_EFFORT,
        len(calls),
    )


class VerifyTests(unittest.TestCase):
    def test_codex_0117_command_uses_an_isolated_home_instead_of_unsupported_flags(self) -> None:
        args = Namespace(
            codex_binary=Path("codex.exe"),
            codex_model=verify.EXPECTED_CODEX_MODEL,
            codex_reasoning_effort=verify.EXPECTED_CODEX_REASONING_EFFORT,
            codex_service_tier="",
            binary=Path("ownward.exe"),
            data_dir=Path("data"),
            runtime_dir=Path("runtime"),
        )
        command = verify._codex_command(args, Path("agent"))
        self.assertNotIn("--ignore-user-config", command)
        self.assertNotIn("--ignore-rules", command)
        self.assertIn("features.plugins=false", command)
        self.assertIn("features.shell_tool=false", command)
        self.assertIn('web_search="disabled"', command)
        self.assertFalse(any("approval_mode" in value for value in command))
        mutation_command = verify._codex_command(args, Path("agent"), allow_mutation=True)
        self.assertIn('mcp_servers.ownward.tools.ownward_create.approval_mode="approve"', mutation_command)
        self.assertIn('mcp_servers.ownward.tools.ownward_update.approval_mode="approve"', mutation_command)
        self.assertIn('mcp_servers.ownward.tools.ownward_semantic_submit_batch.approval_mode="approve"', mutation_command)
        self.assertIn('mcp_servers.ownward.tools.ownward_semantic_submit.approval_mode="approve"', mutation_command)

        with tempfile.TemporaryDirectory() as root:
            root_path = Path(root)
            auth_file = root_path / "source-auth.json"
            auth_file.write_text("{}", encoding="utf-8")
            environment = verify._isolated_codex_environment(auth_file, root_path / "codex-home")
            self.assertEqual(environment["CODEX_HOME"], str(root_path / "codex-home"))
            self.assertNotIn("OPENAI_API_KEY", environment)
            self.assertEqual((root_path / "codex-home" / "auth.json").read_text(encoding="utf-8"), "{}")

    def test_exec_trace_proves_ordered_mutation_lifecycle(self) -> None:
        trace = _mutation_trace("before", "after", "transient scratch")
        self.assertEqual(
            verify.validate_mutation_session(trace, excluded_transient_content="transient scratch"),
            {"id": "I1", "revision": 2, "content": "after"},
        )

    def test_mutation_lifecycle_accepts_single_semantic_submission(self) -> None:
        trace = _mutation_trace("before", "after", "transient scratch")
        calls = list(trace.calls)
        for index, call in enumerate(calls):
            if call.name != "ownward_semantic_submit_batch":
                continue
            submission = call.arguments["submissions"][0]
            organization = call.result["results"][0]["organization"]
            calls[index] = verify.ToolCall(
                "ownward_semantic_submit",
                {"submission": submission},
                {"organization": organization},
            )
        singular = verify.SessionTrace(
            trace.session_id,
            trace.agent,
            calls,
            trace.bypassed,
            trace.user_text,
            trace.model,
            trace.reasoning_effort,
            len(calls),
        )
        self.assertEqual(
            verify.validate_mutation_session(singular, excluded_transient_content="transient scratch"),
            {"id": "I1", "revision": 2, "content": "after"},
        )

    def test_mutation_session_requires_fixed_prompt_and_model(self) -> None:
        trace = verify.SessionTrace(
            "session-one",
            "Codex/openai",
            [],
            False,
            verify.MUTATION_PROMPT + "\nPreloaded rule: persist only durable information.",
            verify.EXPECTED_CODEX_MODEL,
            verify.EXPECTED_CODEX_REASONING_EFFORT,
        )
        with self.assertRaisesRegex(RuntimeError, "fixed acceptance prompt"):
            verify.validate_mutation_session(
                trace,
                expected_prompt=verify.MUTATION_PROMPT,
                expected_model=verify.EXPECTED_CODEX_MODEL,
                expected_reasoning_effort=verify.EXPECTED_CODEX_REASONING_EFFORT,
            )

    def test_exec_events_require_real_mcp_calls(self) -> None:
        information = {"id": "I1", "revision": 2, "content": "after"}
        lines = [
            json.dumps({"type": "thread.started", "thread_id": "session-two"}),
            json.dumps({"type": "item.completed", "item": {"type": "mcp_tool_call", "server": "ownward", "tool": "ownward_search", "arguments": {"query": "after"}, "result": {"structured_content": {"results": [{"id": "I1"}]}}, "error": None, "status": "completed"}}),
            json.dumps({"type": "item.completed", "item": {"type": "mcp_tool_call", "server": "ownward", "tool": "ownward_read", "arguments": {"id": "I1"}, "result": {"structured_content": {"information": information}}, "error": None, "status": "completed"}}),
        ]
        trace = verify.load_exec_events("\n".join(lines))
        verify.validate_read_session(trace, information)

    def test_exec_events_reject_any_other_completed_tool(self) -> None:
        lines = [
            json.dumps({"type": "thread.started", "thread_id": "session-two"}),
            json.dumps({"type": "item.completed", "item": {"type": "file_change", "path": "answer.txt"}}),
        ]
        trace = verify.load_exec_events("\n".join(lines))
        self.assertTrue(trace.bypassed)

    def test_exec_events_allow_internal_planning_without_counting_it_as_a_tool(self) -> None:
        lines = [
            json.dumps({"type": "thread.started", "thread_id": "session-two"}),
            json.dumps({"type": "item.completed", "item": {"type": "todo_list"}}),
            json.dumps(
                {
                    "type": "item.completed",
                    "item": {
                        "type": "mcp_tool_call",
                        "server": "ownward",
                        "tool": "ownward_search",
                        "arguments": {"query": "fact"},
                        "result": {"structured_content": {"results": []}},
                        "error": None,
                        "status": "completed",
                    },
                }
            ),
        ]
        trace = verify.load_exec_events("\n".join(lines))
        self.assertFalse(trace.bypassed)
        self.assertEqual(trace.tool_call_count, 1)

    def test_evidence_trace_contains_only_session_and_ownward_calls(self) -> None:
        trace = verify.SessionTrace(
            "session-one",
            "Codex/openai",
            [verify.ToolCall("ownward_search", {"query": "Borealis"}, {"results": []})],
            False,
        )
        with tempfile.TemporaryDirectory() as root:
            path = Path(root) / "trace.jsonl"
            verify.write_evidence_trace(path, trace)
            encoded = path.read_text(encoding="utf-8")
            events = [json.loads(line) for line in encoded.splitlines()]
        self.assertEqual(events[0]["session_id"], "session-one")
        self.assertEqual(events[0]["agent"], "Codex/openai")
        self.assertEqual(events[0]["tool_call_count"], 1)
        self.assertFalse(events[0]["bypassed"])
        self.assertEqual(events[1]["name"], "ownward_search")
        self.assertNotIn("Use only the connected Ownward tools", encoded)


if __name__ == "__main__":
    unittest.main()
