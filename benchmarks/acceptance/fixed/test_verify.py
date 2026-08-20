from __future__ import annotations

from pathlib import Path
import unittest

import verify


class FixedVerifierTests(unittest.TestCase):
    def test_v5_fixture_paths_resolve_and_cover_all_query_types(self) -> None:
        fixtures = verify.load_fixtures(Path(__file__).resolve().parents[1] / "v5" / "baseline.json")
        self.assertEqual(len(fixtures["information"]), 30)
        self.assertEqual(len(fixtures["relations"]), 15)
        self.assertEqual(
            {item["type"] for item in fixtures["queries"]},
            {"explicit_object", "semantic_intent", "relation_constraint", "context_applicability"},
        )

    def test_related_to_is_canonicalized_without_changing_directional_relations(self) -> None:
        self.assertEqual(
            verify.canonical_relation("B", "related_to", "A"),
            verify.canonical_relation("A", "related_to", "B"),
        )
        self.assertNotEqual(
            verify.canonical_relation("B", "supports", "A"),
            verify.canonical_relation("A", "supports", "B"),
        )

    def test_semantic_trace_requires_the_two_formal_contract_calls(self) -> None:
        text = "\n".join(
            [
                '{"type":"thread.started","thread_id":"one"}',
                '{"type":"item.completed","item":{"type":"mcp_tool_call","server":"ownward","tool":"ownward_semantic_work","status":"completed","error":null}}',
                '{"type":"item.completed","item":{"type":"mcp_tool_call","server":"ownward","tool":"ownward_semantic_submit_batch","status":"completed","error":null}}',
            ]
        )
        self.assertEqual(
            verify.semantic_trace_calls(text),
            [("ownward_semantic_work", True), ("ownward_semantic_submit_batch", True)],
        )


if __name__ == "__main__":
    unittest.main()
