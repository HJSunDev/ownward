from __future__ import annotations

import json
from pathlib import Path
import sys
import unittest


HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

import kernel_iteration_stage4_resource_cost as resource_cost  # noqa: E402


class Stage4ResourceCostTests(unittest.TestCase):
    def test_contract_freezes_three_independent_half_gates(self) -> None:
        with self.assertRaises(Exception):
            resource_cost.load_contract(HERE)
        contract = json.loads((HERE / "iteration/v2/stage4-end-to-end-resource-cost-contract.json").read_text(encoding="utf-8"))
        self.assertEqual(
            contract["gates"],
            {
                "semantic_input_tokens": {"maximum_ratio": 0.5},
                "end_to_end_wall_seconds": {"maximum_ratio": 0.5},
                "ownward_data_bytes": {"maximum_ratio": 0.5},
            },
        )
        self.assertFalse(contract["formal_isolation"]["may_write_acceptance_state"])

    def test_evaluation_never_compensates_between_dimensions(self) -> None:
        contract = {"gates": {
            "semantic_input_tokens": {"maximum_ratio": 0.5},
            "end_to_end_wall_seconds": {"maximum_ratio": 0.5},
            "ownward_data_bytes": {"maximum_ratio": 0.5},
        }}
        subjects = {
            "v0": {"semantic_input_tokens": 100, "end_to_end_wall_seconds": 100, "ownward_data_bytes": 100},
            "v2": {"semantic_input_tokens": 49, "end_to_end_wall_seconds": 51, "ownward_data_bytes": 1},
        }
        result = resource_cost.evaluate(subjects, contract)
        self.assertEqual(result["root_status"], "open")
        self.assertFalse(result["dimensions"]["end_to_end_wall_seconds"]["passed"])

    def test_all_dimensions_must_independently_pass(self) -> None:
        contract = {"gates": {
            "semantic_input_tokens": {"maximum_ratio": 0.5},
            "end_to_end_wall_seconds": {"maximum_ratio": 0.5},
            "ownward_data_bytes": {"maximum_ratio": 0.5},
        }}
        subjects = {
            "v0": {"semantic_input_tokens": 100, "end_to_end_wall_seconds": 100, "ownward_data_bytes": 100},
            "v2": {"semantic_input_tokens": 50, "end_to_end_wall_seconds": 49, "ownward_data_bytes": 50},
        }
        self.assertEqual(resource_cost.evaluate(subjects, contract)["root_status"], "closed")


if __name__ == "__main__":
    unittest.main()
