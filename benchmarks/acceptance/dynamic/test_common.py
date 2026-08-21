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
            "product_runtime": {
                "mode": "release-defaults",
                "prohibited_environment": ["OPENAI_API_KEY"],
            },
            "generation": {
                "generated_scenarios": 4,
                "minimum_valid_scenarios": 4,
                "information_per_scenario": 4,
                "validation_scenarios_per_batch": 1,
                "task_classes": ["cross_time", "multi_hop", "context_applicability", "information_update"],
                "minimum_scenarios_per_task_class": 1,
                "information_scope": [f"scope-{index}" for index in range(10)],
            },
            "models": {
                "generator": {"model": "one", "reasoning_effort": "medium"},
                "validator": {"model": "two", "reasoning_effort": "high"},
                "external_agent": {"model": "three", "reasoning_effort": "low"},
            },
            "relation_contract": {
                "direction": "Every relation is source_id TYPE target_id.",
                "types": {relation_type: f"meaning of {relation_type}" for relation_type in common.RELATION_TYPES},
            },
            "execution": {
                "dataset_stage_seconds_max": 1,
                "codex_inactivity_seconds": 1,
                "inspection_operation_stall_seconds": 1,
                "ownward_operation_stall_seconds": 2,
                "semantic_stage_seconds_max": 3,
                "agent_seconds_per_question_max": 1,
                "agent_tool_calls_per_query": 1,
                "dataset_parallelism": 2,
                "parallel_conditions": 2,
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

    def test_protocol_rejects_the_obsolete_test_model_path(self) -> None:
        common.validate_protocol(self.protocol)
        obsolete = copy.deepcopy(self.protocol)
        obsolete.pop("product_runtime")
        obsolete["semantic_provider"] = {
            "chat_model": "test-chat",
            "embedding_model": "test-embedding",
            "embedding_dimensions": 384,
        }
        with self.assertRaisesRegex(RuntimeError, "formal product runtime"):
            common.validate_protocol(obsolete)

    def test_protocol_requires_paired_conditions_and_bounded_operations(self) -> None:
        invalid_parallelism = copy.deepcopy(self.protocol)
        invalid_parallelism["execution"]["parallel_conditions"] = 1
        with self.assertRaisesRegex(RuntimeError, "parallel pair"):
            common.validate_protocol(invalid_parallelism)

        invalid_stall = copy.deepcopy(self.protocol)
        invalid_stall["execution"]["ownward_operation_stall_seconds"] = 0.5
        with self.assertRaisesRegex(RuntimeError, "must not exceed"):
            common.validate_protocol(invalid_stall)

        invalid_batch = copy.deepcopy(self.protocol)
        invalid_batch["generation"]["validation_scenarios_per_batch"] = 2
        with self.assertRaisesRegex(RuntimeError, "generated pool"):
            common.validate_protocol(invalid_batch)

    def test_protocol_requires_complete_canonical_relation_contract(self) -> None:
        invalid = copy.deepcopy(self.protocol)
        del invalid["relation_contract"]["types"]["supports"]
        with self.assertRaisesRegex(RuntimeError, "definitions are incomplete"):
            common.validate_protocol(invalid)

    def test_dataset_prompts_share_the_frozen_relation_contract(self) -> None:
        hidden = common.generation_prompt(self.protocol, "seed")
        expression = common.expression_prompt(self.hidden, self.protocol)
        validation = common.validation_prompt(self.hidden, self.expression, self.protocol)
        for prompt in (hidden, expression, validation):
            self.assertIn("meaning of supports", prompt)
        self.assertIn("structure, identities, relation topology", hidden)
        self.assertIn("Multi-hop facts must make the origin, intermediate conclusion, and downstream result independently necessary", hidden)
        self.assertIn("A related_to pair must express a direct association", hidden)
        self.assertIn("context_applicability asks for both the current context", hidden)
        self.assertIn("cross_time asks separately for the earlier basis", hidden)
        self.assertIn("the initial update-target node must state only the old value", hidden)
        self.assertIn("one exact distinguishing detail found nowhere else", hidden)
        self.assertIn("unique provenance, cause, or observation", hidden)
        self.assertIn("only the required-context node may describe the user's current situation", hidden)
        self.assertIn("no proper subset of the expected nodes can fully answer", hidden)
        self.assertIn("any proper subset of expected nodes semantically answers", validation)
        self.assertIn("Forbidden nodes are deliberate distractors that must remain present", validation)

    def test_hidden_structure_is_deterministic_valid_and_seeded(self) -> None:
        first = common.build_hidden_structure(self.protocol, "seed-one")
        self.assertEqual(first, common.build_hidden_structure(self.protocol, "seed-one"))
        self.assertNotEqual(first, common.build_hidden_structure(self.protocol, "seed-two"))
        content = {"scenarios": {}}
        for scenario in first["scenarios"]:
            content["scenarios"][scenario["id"]] = {
                "nodes": {node["id"]: [f"fact for {node['id']}"] for node in scenario["nodes"]},
                "updates": {node_id: [f"current fact for {node_id}"] for node_id in scenario["update_node_ids"]},
                "question": f"What is current in scenario {scenario['id']}?",
            }
        hidden = common.assemble_hidden_world(first, content)
        common.validate_hidden_world(hidden, self.protocol)
        expression = common.build_natural_expression(hidden, content)
        self.assertEqual(len(expression["scenarios"]), len(hidden["scenarios"]))
        by_id = {value["id"]: value for value in hidden["scenarios"]}
        for scenario in first["scenarios"]:
            assembled = by_id[scenario["id"]]
            self.assertEqual(assembled["relations"], scenario["relations"])
            self.assertEqual(assembled["query"]["expected_ids"], scenario["query"]["expected_ids"])
            if scenario["task_class"] == "information_update":
                self.assertIn("unique detail absent from all other nodes", scenario["nodes"][0]["role"])
            if scenario["task_class"] == "context_applicability":
                self.assertEqual(scenario["nodes"][1]["role"], "the only current context in the scenario")
                self.assertEqual(scenario["nodes"][3]["role"], "clearly non-current alternative context")
            for node in scenario["nodes"][4:]:
                self.assertIn("not its category", node["role"])
        partitions = common.content_partitions(hidden, self.protocol)
        self.assertEqual(
            [value["task_class"] for value in partitions],
            self.protocol["generation"]["task_classes"],
        )
        self.assertTrue(all(len(value["hidden"]["scenarios"]) <= 4 for value in partitions))

    def test_validation_partitions_bound_each_model_call_without_losing_scenarios(self) -> None:
        hidden = {
            "scenarios": [
                {"id": f"{task_class}-{index}", "task_class": task_class}
                for task_class in self.protocol["generation"]["task_classes"]
                for index in range(7)
            ]
        }
        content = common.content_partitions(hidden, self.protocol)
        protocol = copy.deepcopy(self.protocol)
        protocol["generation"]["validation_scenarios_per_batch"] = 3
        validation = common.validation_partitions(hidden, protocol)
        self.assertEqual([len(value["hidden"]["scenarios"]) for value in content], [4, 3] * 4)
        self.assertEqual([len(value["hidden"]["scenarios"]) for value in validation], [3, 3, 1] * 4)
        self.assertEqual(sum(len(value["hidden"]["scenarios"]) for value in content), 28)
        self.assertEqual(sum(len(value["hidden"]["scenarios"]) for value in validation), 28)

    def test_agent_prompt_exposes_the_frozen_tool_budget(self) -> None:
        prompt = common.agent_prompt(
            [{"query_id": "one", "question": "first"}, {"query_id": "two", "question": "second"}],
            8,
        )
        self.assertIn("at most 16 Ownward tool calls", prompt)
        self.assertIn("no more than 8 calls allocated to any question", prompt)
        self.assertIn("Do not inspect unrelated candidates", prompt)

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
        self.assertEqual(dataset["schema"], "ownward.dynamic-dataset/v2")
        self.assertEqual(len(dataset["valid_scenarios"]), 4)
        self.assertEqual(dataset["reserve_scenarios"], [])
        self.assertEqual(dataset["rejected_scenarios"], [])

    def test_rejects_updates_that_do_not_belong_to_the_update_task(self) -> None:
        invalid = copy.deepcopy(self.hidden)
        invalid["scenarios"][0]["updates"] = [
            {"node_id": invalid["scenarios"][0]["nodes"][0]["id"], "replacement_facts": ["changed"]}
        ]
        with self.assertRaisesRegex(RuntimeError, "unnecessary update"):
            common.validate_hidden_world(invalid, self.protocol)

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
