import copy
import unittest
from pathlib import Path

import product
from contract import load_contract


class ProductExecutionTests(unittest.TestCase):
    def setUp(self) -> None:
        self.contract = load_contract(Path(__file__).with_name("contract.json"))
        categories = self.contract["evidence_layers"]["product"]["categories"]
        scenarios = []
        qualification_ids = []
        for category in categories:
            for index in range(6):
                identifier = f"{category}-{index}"
                node_ids = [f"{identifier}-n{node}" for node in range(5)]
                scenarios.append({
                    "truth": {
                        "id": identifier,
                        "task_class": category,
                        "query": {"expected_ids": node_ids[:3], "forbidden_ids": node_ids[3:], "answer_facts": []},
                    },
                    "expression": {
                        "id": identifier,
                        "information": [{"node_id": node, "content": f"content {node}"} for node in node_ids],
                        "updates": [],
                        "query": {"question": f"question {identifier}"},
                    },
                })
                if index < 2:
                    qualification_ids.append(identifier)
        self.dataset = {"version": "ownward-product-dataset/v1", "scenarios": scenarios}
        self.qualification = {"scenario_ids": qualification_ids}
        self.binding = {
            "candidate": "candidate",
            "binary_sha256": "a" * 64,
            "environment": {"sha256": "b" * 64},
            "inputs": {"sha256": "c" * 64},
        }

    def test_prepare_hides_truth_and_fixes_scope(self) -> None:
        tasks = product.prepare_tasks(self.dataset, self.qualification, "qualification")
        self.assertEqual(len(tasks["tasks"]), 8)
        self.assertTrue(all("truth" not in task for task in tasks["tasks"]))
        self.assertIn("test-only product path", tasks["execution"]["prohibited"])

    def test_score_accepts_complete_results_and_rejects_unmapped_identity(self) -> None:
        tasks = product.prepare_tasks(self.dataset, self.qualification, "qualification")
        results = []
        for task in tasks["tasks"]:
            node_ids = [item["node_id"] for item in task["information"]]
            results.append({
                "scenario_id": task["scenario_id"],
                "direct_ids": node_ids[:2],
                "returned_ids": node_ids[:3],
                "navigation_ids": node_ids[2:3],
                "answer_facts": [],
                "grounded": True,
                "used_navigation": True,
                "latency_ms": 1.0,
                "semantic_ms": 2.0,
                "agent_query_ms": 3.0,
                "end_to_end_ms": 6.0,
                "peak_mib": 1.0,
                "within_latency_budget": True,
                "within_resource_budget": True,
            })
        envelope = {
            "schema": "ownward.product-results/v1",
            "dataset_version": tasks["dataset_version"],
            "mode": "qualification",
            "results": results,
        }
        report = product.score_results(self.contract, self.dataset, self.qualification, tasks, envelope, self.binding)
        self.assertTrue(report["passed"])
        missing_timing = copy.deepcopy(envelope)
        missing_timing["results"][0].pop("agent_query_ms")
        with self.assertRaisesRegex(product.ProductExecutionError, "agent_query_ms"):
            product.score_results(self.contract, self.dataset, self.qualification, tasks, missing_timing, self.binding)
        broken = copy.deepcopy(envelope)
        broken["results"][0]["returned_ids"].append("unknown")
        with self.assertRaisesRegex(product.ProductExecutionError, "未映射"):
            product.score_results(self.contract, self.dataset, self.qualification, tasks, broken, self.binding)


if __name__ == "__main__":
    unittest.main()
