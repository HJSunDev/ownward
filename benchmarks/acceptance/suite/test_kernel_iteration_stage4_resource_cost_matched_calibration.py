from __future__ import annotations

import unittest

import kernel_iteration_stage4_resource_cost_matched_calibration as calibration


class MatchedCalibrationTests(unittest.TestCase):
    def test_schema_matches_frozen_semantic_shape(self) -> None:
        schema = calibration.semantic_output_schema(["work-a", "work-b"])
        analyses = schema["properties"]["analyses"]
        self.assertEqual(analyses["minItems"], 2)
        self.assertEqual(analyses["maxItems"], 2)
        self.assertEqual(analyses["items"]["properties"]["work_id"]["enum"], ["work-a", "work-b"])

    def test_gate_migration_uses_only_v0_component_baseline(self) -> None:
        subjects = {
            "v0": {
                "token_ledger": {
                    "fixed_host_schema_and_minimum_request_tokens": 80,
                    "candidate_controllable_increment_tokens": 20,
                },
                "semantic_minimum_four_worker_wall_lower_bound_seconds": 20.0,
                "wall_critical_path": {
                    "true_wall_seconds": 100.0,
                    "shared_reader_judge_seconds": 30.0,
                    "runner_and_unclassified_seconds": 10.0,
                },
            },
            "v2": {
                "token_ledger": {
                    "fixed_host_schema_and_minimum_request_tokens": 81,
                    "candidate_controllable_increment_tokens": 9,
                },
                "semantic_minimum_four_worker_wall_lower_bound_seconds": 21.0,
                "wall_critical_path": {
                    "true_wall_seconds": 72.0,
                    "shared_reader_judge_seconds": 30.0,
                    "runner_and_unclassified_seconds": 10.0,
                },
            },
        }
        contract = {
            "existing_global_gates": {"v0_semantic_input_tokens": 100, "v0_end_to_end_wall_seconds": 100.0},
            "closed_storage_dimension": {"candidate_ratio": 0.407259},
        }
        result = calibration.evaluate_gate_migration(subjects, contract)
        self.assertTrue(result["semantic_input_tokens"]["original_global_gate_mathematically_unreachable"])
        self.assertEqual(result["semantic_input_tokens"]["candidate_component_maximum"], 10)
        self.assertTrue(result["semantic_input_tokens"]["candidate_component_passed"])
        self.assertTrue(result["end_to_end_wall_seconds"]["original_global_gate_mathematically_unreachable"])
        self.assertFalse(result["formal_acceptance_contract_changed"])

    def test_global_gate_remains_when_fixed_floor_does_not_block_half(self) -> None:
        subjects = {
            subject: {
                "token_ledger": {
                    "fixed_host_schema_and_minimum_request_tokens": 20,
                    "candidate_controllable_increment_tokens": 80 if subject == "v0" else 60,
                },
                "semantic_minimum_four_worker_wall_lower_bound_seconds": 5.0,
                "wall_critical_path": {
                    "true_wall_seconds": 100.0 if subject == "v0" else 80.0,
                    "shared_reader_judge_seconds": 10.0,
                    "runner_and_unclassified_seconds": 5.0,
                },
            }
            for subject in ("v0", "v2")
        }
        contract = {
            "existing_global_gates": {"v0_semantic_input_tokens": 100, "v0_end_to_end_wall_seconds": 100.0},
            "closed_storage_dimension": {"candidate_ratio": 0.407259},
        }
        result = calibration.evaluate_gate_migration(subjects, contract)
        self.assertFalse(result["semantic_input_tokens"]["migration_allowed"])
        self.assertFalse(result["end_to_end_wall_seconds"]["migration_allowed"])


if __name__ == "__main__":
    unittest.main()
