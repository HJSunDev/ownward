from __future__ import annotations

import json
from pathlib import Path
import sys
import unittest


HERE = Path(__file__).resolve().parent
REPOSITORY = HERE.parents[2]
sys.path.insert(0, str(HERE))

import kernel_iteration_longmemeval as stage3_adapter  # noqa: E402


class _Client:
    def __init__(self, *, evidence_available: bool) -> None:
        self.evidence_available = evidence_available
        self.calls: list[str] = []
        self.contents = {"source-1": "complete first source", "source-2": "second distractor"}

    def call_tool(self, name: str, arguments: dict):
        self.calls.append(name)
        if name == "ownward_search":
            return {"results": [{"id": name, "score": 1.0, "signals": ["lexical"]} for name in self.contents]}
        if name == "ownward_evidence_search":
            if not self.evidence_available:
                raise stage3_adapter.adapter.MCPError('unknown tool "ownward_evidence_search"')
            return {"evidence": []}
        if name == "ownward_read":
            identifier = arguments["id"]
            return {"information": {"id": identifier, "content": self.contents[identifier]}}
        raise AssertionError(name)


class _Runtime:
    def __init__(self, *, evidence_available: bool) -> None:
        self.client = _Client(evidence_available=evidence_available)


class KernelIterationLongMemEvalTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.protocol = json.loads((REPOSITORY / "benchmarks" / "longmemeval_s" / "protocol.json").read_text(encoding="utf-8"))

    def test_v0_missing_evidence_tools_use_original_ranked_full_read_contract(self) -> None:
        runtime = _Runtime(evidence_available=False)
        evidence, trace = stage3_adapter.retrieve_with_v0_compatibility(runtime, "complete source", self.protocol)
        self.assertEqual("v0-ranked-full-read/v1", trace["selection_policy"])
        self.assertEqual(["source-1", "source-2"], trace["read_ids"])
        self.assertEqual(["complete first source", "second distractor"], [item["content"] for item in evidence])

    def test_current_public_evidence_path_is_not_replaced(self) -> None:
        runtime = _Runtime(evidence_available=True)
        _evidence, trace = stage3_adapter.retrieve_with_v0_compatibility(runtime, "complete source", self.protocol)
        self.assertEqual(self.protocol["retrieval"]["evidence_selection_policy"], trace["selection_policy"])
        self.assertIn("ownward_evidence_search", runtime.client.calls)


if __name__ == "__main__":
    unittest.main()
