from __future__ import annotations

import copy
from pathlib import Path
import unittest

import kernel_iteration_evidence as evidence
import kernel_iteration_stage4_resource_cost_representation_finalize as finalize
import kernel_iteration_validation as validation


class RepresentationLifecycleFinalTests(unittest.TestCase):
    def setUp(self) -> None:
        self.suite_root = Path(__file__).parent
        self.contract = finalize.load_contract(self.suite_root)
        self.rounds = [
            {
                "round": 1,
                "subject_order": ["v0", "v2"],
                "subjects": {
                    "v0": {"behavior_identity": "same", "candidate_controlled_critical_seconds": 13.0},
                    "v2": {"behavior_identity": "same", "candidate_controlled_critical_seconds": 4.5},
                },
            },
            {
                "round": 2,
                "subject_order": ["v2", "v0"],
                "subjects": {
                    "v0": {"behavior_identity": "same", "candidate_controlled_critical_seconds": 13.5},
                    "v2": {"behavior_identity": "same", "candidate_controlled_critical_seconds": 4.7},
                },
            },
        ]
        observation = {
            "final_answer_accuracy": 1.0,
            "fact_delivery": {"complete": True},
            "resources": {"ownward_data_bytes": 100000, "semantic_input_tokens": 20000},
            "latency": {"retrieval_p95_ms": 300.0},
        }
        self.quality = {
            "development": {"observation": copy.deepcopy(observation)},
            "regression": {"observation": copy.deepcopy(observation)},
        }

    def test_final_gate_binds_behavior_quality_cost_and_identity(self) -> None:
        result = finalize.evaluate(self.contract, self.rounds, self.quality, self.contract["formal_state"]["sha256"])
        content = {key: value for key, value in result.items() if key != "identity"}
        self.assertEqual(result["identity"], evidence.canonical_sha256(content))
        self.assertTrue(result["candidate_controlled_gate"]["passed"])
        self.assertEqual(result["decision"], "close-end-to-end-resource-cost-and-stage4")

    def test_behavior_or_closed_dimension_drift_fails_closed(self) -> None:
        rounds = copy.deepcopy(self.rounds)
        rounds[1]["subjects"]["v2"]["behavior_identity"] = "drift"
        with self.assertRaises(validation.KernelIterationValidationError):
            finalize.evaluate(self.contract, rounds, self.quality, self.contract["formal_state"]["sha256"])
        quality = copy.deepcopy(self.quality)
        quality["regression"]["observation"]["resources"]["ownward_data_bytes"] = 200000
        with self.assertRaises(validation.KernelIterationValidationError):
            finalize.evaluate(self.contract, self.rounds, quality, self.contract["formal_state"]["sha256"])

    def test_repeatability_margin_cannot_be_removed(self) -> None:
        rounds = copy.deepcopy(self.rounds)
        for round_value in rounds:
            round_value["subjects"]["v2"]["candidate_controlled_critical_seconds"] = 6.0
        with self.assertRaises(validation.KernelIterationValidationError):
            finalize.evaluate(self.contract, rounds, self.quality, self.contract["formal_state"]["sha256"])


if __name__ == "__main__":
    unittest.main()
