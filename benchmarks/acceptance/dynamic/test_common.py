from __future__ import annotations

import copy
import importlib.util
from pathlib import Path
import unittest


MODULE_PATH = Path(__file__).with_name("common.py")
SPEC = importlib.util.spec_from_file_location("ownward_dynamic_common", MODULE_PATH)
assert SPEC is not None and SPEC.loader is not None
common = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(common)


class DynamicCommonTests(unittest.TestCase):
    def setUp(self) -> None:
        self.protocol = {
            "schema": common.PROTOCOL_SCHEMA,
            "runtime": {"codex_cli_version": "codex-cli 0.117.0"},
            "semantic_provider": {
                "chat_model": "chat",
                "embedding_model": "embedding",
                "embedding_dimensions": 384,
            },
            "generation": {
                "generated_scenarios": 4,
                "minimum_valid_scenarios": 4,
                "information_per_scenario": 4,
                "task_classes": ["cross_time", "multi_hop", "context_applicability", "information_update"],
                "minimum_scenarios_per_task_class": 1,
                "information_scope": [f"scope-{index}" for index in range(10)],
            },
            "models": {
                "generator": {"model": "one", "reasoning_effort": "medium"},
                "validator": {"model": "two", "reasoning_effort": "high"},
                "external_agent": {"model": "three", "reasoning_effort": "low"},
            },
            "budgets": {
                "generation_seconds": 1,
                "validation_seconds": 1,
                "organization_seconds_per_item": 1,
                "agent_seconds_per_condition": 1,
                "agent_tool_calls_per_query": 1,
            },
            "statistics": {
                "confidence_level": 0.95,
                "dynamic_task_success_wilson_lower_min": 0.6,
                "relation_precision_wilson_lower_min": 0.8,
                "relation_recall_wilson_lower_min": 0.75,
                "ablation_equivalence_margin": 0.05,
                "minimum_quality_gain": 0.1,
                "minimum_latency_or_cost_reduction": 0.15,
                "basis": "test",
            },
        }
        scenarios = []
        expressions = []
        validations = []
        for index, task_class in enumerate(self.protocol["generation"]["task_classes"]):
            nodes = [{"id": f"n{index}-{node}", "facts": [f"fact {index}-{node}"]} for node in range(4)]
            relations = [{"source_id": nodes[0]["id"], "type": "supports", "target_id": nodes[1]["id"]}]
            expected_ids = [nodes[0]["id"]]
            forbidden_ids: list[str] = []
            if task_class == "cross_time":
                relations = [{"source_id": nodes[0]["id"], "type": "broader_than", "target_id": nodes[1]["id"]}]
                expected_ids = [nodes[0]["id"], nodes[1]["id"]]
            elif task_class == "multi_hop":
                relations = [
                    {"source_id": nodes[0]["id"], "type": "part_of", "target_id": nodes[1]["id"]},
                    {"source_id": nodes[1]["id"], "type": "part_of", "target_id": nodes[2]["id"]},
                ]
                expected_ids = [nodes[0]["id"], nodes[1]["id"], nodes[2]["id"]]
            elif task_class == "context_applicability":
                relations = [{"source_id": nodes[0]["id"], "type": "applies_in", "target_id": nodes[1]["id"]}]
                forbidden_ids = [nodes[1]["id"]]
            scenarios.append(
                {
                    "id": f"s{index}",
                    "task_class": task_class,
                    "information_scope": [f"scope-{value}" for value in range(index * 3, min(index * 3 + 3, 10))]
                    or ["scope-9"],
                    "nodes": nodes,
                    "relations": relations,
                    "updates": [{"node_id": nodes[0]["id"], "replacement_facts": ["new"]}] if task_class == "information_update" else [],
                    "query": {
                        "expected_ids": expected_ids,
                        "forbidden_ids": forbidden_ids,
                        "answer_facts": ["new"] if task_class == "information_update" else [nodes[0]["facts"][0]],
                    },
                }
            )
            expressions.append(
                {
                    "id": f"s{index}",
                    "information": [{"node_id": node["id"], "content": node["facts"][0]} for node in nodes],
                    "updates": [{"node_id": nodes[0]["id"], "content": "new"}] if task_class == "information_update" else [],
                    "query": {"question": f"question {index}"},
                }
            )
            validations.append({"id": f"s{index}", "valid": True, "issues": []})
        scenarios[-1]["information_scope"].append("scope-9")
        self.hidden = {"scenarios": scenarios}
        self.expression = {"scenarios": expressions}
        self.validation = {"scenarios": validations}

    def test_wilson_lower_matches_frozen_sample_basis(self) -> None:
        self.assertGreater(common.wilson_lower(6, 6, 0.95), 0.60)
        self.assertLess(common.wilson_lower(5, 6, 0.95), 0.60)

    def test_inverse_relation_labels_have_one_semantic_identity(self) -> None:
        self.assertEqual(
            common.canonical_relation("child", "narrower_than", "parent"),
            common.canonical_relation("parent", "broader_than", "child"),
        )
        self.assertEqual(
            common.canonical_relation("whole", "has_part", "part"),
            common.canonical_relation("part", "part_of", "whole"),
        )

    def test_merges_only_independently_valid_scenarios(self) -> None:
        dataset = common.merge_valid_dataset(self.hidden, self.expression, self.validation, self.protocol)
        self.assertEqual(len(dataset["valid_scenarios"]), 4)
        self.assertEqual(dataset["rejected_scenarios"], [])

    def test_rejects_changed_hidden_truth(self) -> None:
        invalid = copy.deepcopy(self.hidden)
        invalid["scenarios"][0]["relations"][0]["target_id"] = "invented"
        with self.assertRaisesRegex(RuntimeError, "invalid relation target"):
            common.validate_hidden_world(invalid, self.protocol)

    def test_rejects_answer_truth_not_grounded_in_current_expected_information(self) -> None:
        invalid = copy.deepcopy(self.hidden)
        invalid["scenarios"][0]["query"]["answer_facts"] = ["invented answer"]
        with self.assertRaisesRegex(RuntimeError, "answer truth is absent"):
            common.merge_valid_dataset(invalid, self.expression, self.validation, self.protocol)

    def test_rejects_answer_truth_leaked_by_unexpected_information(self) -> None:
        invalid = copy.deepcopy(self.expression)
        invalid["scenarios"][0]["information"][3]["content"] += " fact 0-0"
        with self.assertRaisesRegex(RuntimeError, "not unique to expected information"):
            common.merge_valid_dataset(self.hidden, invalid, self.validation, self.protocol)


if __name__ == "__main__":
    unittest.main()
