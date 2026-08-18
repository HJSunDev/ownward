from __future__ import annotations

import importlib
import os
from pathlib import Path
import sys
import tempfile
import types
import unittest


def _load_adapter():
    adapter_dir = str(Path(__file__).resolve().parent)
    if adapter_dir not in sys.path:
        sys.path.insert(0, adapter_dir)
    memory_module = types.ModuleType("memory_modules.memory")

    class Memory:
        def __init__(self, memory_params):
            self.memory_params = dict(memory_params)

        def get_query_context(self):
            return {}

    def require(condition, message):
        if not condition:
            raise RuntimeError(message)

    def register_memory(memory_class):
        return memory_class

    memory_module.Memory = Memory
    memory_module.MemoryConfig = dict
    memory_module.MemoryContextItem = dict
    memory_module.register_memory = register_memory
    memory_module.require = require
    package = types.ModuleType("memory_modules")
    package.__path__ = []
    sys.modules["memory_modules"] = package
    sys.modules["memory_modules.memory"] = memory_module
    sys.modules.pop("ownward_memory", None)
    return importlib.import_module("ownward_memory")


class OwnwardMemoryTest(unittest.TestCase):
    def test_mcp_evidence_gate_requires_a_successful_ownward_call(self) -> None:
        adapter = _load_adapter()
        failed = (
            '{"type":"item.completed","item":{"type":"mcp_tool_call",'
            '"server":"ownward","tool":"ownward_search","status":"failed",'
            '"error":{"message":"cancelled"}}}'
        )
        completed = (
            '{"type":"item.completed","item":{"type":"mcp_tool_call",'
            '"server":"ownward","tool":"ownward_search","status":"completed",'
            '"error":null}}'
        )

        self.assertFalse(adapter.OwnwardMemory._used_ownward_mcp(failed))
        self.assertTrue(adapter.OwnwardMemory._used_ownward_mcp(completed))

    @unittest.skipUnless(os.environ.get("OWNWARD_BENCHMARK_BINARY"), "requires a built Ownward binary")
    def test_direct_query_and_portable_asset_restore(self) -> None:
        adapter = _load_adapter()
        binary = str(Path(os.environ["OWNWARD_BENCHMARK_BINARY"]).resolve())
        trajectory = {
            "id": "trajectory-smoke",
            "goal": "Remember the release codename",
            "outcome": "The codename was recorded",
            "start_url": "https://example.test/releases",
            "states": [
                {
                    "url": "https://example.test/releases",
                    "action": "read release notes",
                    "thought": None,
                    "text": "The release codename is Silver Swift.",
                }
            ],
        }
        with tempfile.TemporaryDirectory() as root:
            workspace = Path(root) / "workspace"
            saved = Path(root) / "saved"
            saved.mkdir()
            memory = adapter.OwnwardMemory(
                {
                    "workspace_dir": str(workspace),
                    "ownward_binary": binary,
                    "query_mode": "direct",
                    "require_model": False,
                    "max_chunk_chars": 1000,
                }
            )

            memory.insert(trajectory)
            context = memory.query("What is the release codename?")
            memory._save_backend(saved)

            self.assertIn("Silver Swift", context[0]["value"])
            restored = adapter.OwnwardMemory(
                {
                    "ownward_binary": binary,
                    "query_mode": "direct",
                    "require_model": False,
                    "max_chunk_chars": 1000,
                }
            )
            restored._load_backend(saved)
            restored_context = restored.query("release codename")
            self.assertIn("Silver Swift", restored_context[0]["value"])
            restored._temporary_workspace.cleanup()

    @unittest.skipUnless(os.environ.get("OWNWARD_BENCHMARK_ACTIVE_SMOKE") == "1", "active smoke test is opt-in")
    def test_external_codex_retrieves_through_ownward_mcp(self) -> None:
        adapter = _load_adapter()
        trajectory = {
            "id": "trajectory-active-smoke",
            "goal": "Remember the release codename",
            "outcome": "The codename was recorded",
            "start_url": "https://example.test/releases",
            "states": [
                {
                    "url": "https://example.test/releases",
                    "action": "read release notes",
                    "thought": None,
                    "text": "The release codename is Silver Swift.",
                }
            ],
        }
        with tempfile.TemporaryDirectory() as root:
            memory = adapter.OwnwardMemory(
                {
                    "workspace_dir": str(Path(root) / "workspace"),
                    "query_trace_dir": os.environ.get("OWNWARD_BENCHMARK_TRACE_DIR", ""),
                    "ownward_binary": os.environ["OWNWARD_BENCHMARK_BINARY"],
                    "query_mode": "codex",
                    "require_model": False,
                    "max_chunk_chars": 1000,
                    "codex_binary": os.environ["OWNWARD_BENCHMARK_CODEX_BINARY"],
                    "codex_model": os.environ.get("OWNWARD_BENCHMARK_CODEX_MODEL", "gpt-5.4"),
                    "codex_reasoning_effort": os.environ.get("OWNWARD_BENCHMARK_CODEX_EFFORT", "high"),
                    "codex_timeout_seconds": 300,
                }
            )

            memory.insert(trajectory)
            context = memory.query("What is the release codename?")

            self.assertIn("Silver Swift", context[0]["value"])


if __name__ == "__main__":
    unittest.main()
