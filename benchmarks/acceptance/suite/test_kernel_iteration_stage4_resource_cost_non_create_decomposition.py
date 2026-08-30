from __future__ import annotations

import copy
from pathlib import Path
import unittest

import kernel_iteration_evidence as evidence
import kernel_iteration_stage4_resource_cost_non_create_decomposition as decomposition
import kernel_iteration_validation as validation


class NonCreateDecompositionTests(unittest.TestCase):
    def setUp(self) -> None:
        self.suite_root = Path(__file__).parent
        self.repository = self.suite_root.parents[2]
        self.contract = decomposition.load_contract(self.suite_root)
        self.sources = {
            name: decomposition._verified_source(self.repository, item, name)
            for name, item in self.contract["sources"].items()
        }

    def test_same_observation_path_closes_and_has_no_route_margin(self) -> None:
        result = decomposition.evaluate(
            self.contract,
            self.sources,
            self.contract["formal_state"]["sha256"],
        )
        content = {key: item for key, item in result.items() if key != "identity"}
        self.assertEqual(result["identity"], evidence.canonical_sha256(content))
        path = result["same_observation_critical_path"]
        self.assertAlmostEqual(
            path["candidate_local_seconds"],
            path["create_batch_seconds"]
            + path["semantic_submission_and_product_execution_seconds"]
            + path["retrieval_and_evidence_loading_seconds"],
        )
        self.assertAlmostEqual(path["non_create_seconds"], 0.8784781000080386)
        self.assertLess(
            result["route_authorization"]["maximum_evidenced_non_create_improvement_seconds"],
            result["route_authorization"]["required_improvement_seconds"],
        )
        self.assertEqual(result["route_authorization"]["authorized_routes"], [])
        self.assertFalse(result["route_authorization"]["implementation_authorized"])

    def test_cross_observation_residual_is_not_promoted_to_a_path(self) -> None:
        result = decomposition.evaluate(
            self.contract,
            self.sources,
            self.contract["formal_state"]["sha256"],
        )
        mixed = result["cross_observation_arithmetic"]
        self.assertAlmostEqual(
            mixed["mixed_residual_seconds"],
            mixed["same_observation_non_create_seconds"] + mixed["cross_observation_create_delta_seconds"],
        )
        self.assertEqual(mixed["classification"], "diagnostic-only-not-an-observed-critical-path")
        self.assertGreater(mixed["cross_observation_create_delta_seconds"], 3.0)

    def test_source_or_active_gate_drift_fails_closed(self) -> None:
        changed = copy.deepcopy(self.sources)
        changed["matched_calibration"]["subjects"]["v2"]["wall_critical_path"]["critical_chain_phase_seconds"]["retrieval"] += 1.0
        with self.assertRaises(validation.KernelIterationValidationError):
            decomposition.evaluate(
                self.contract,
                changed,
                self.contract["formal_state"]["sha256"],
            )
        changed = copy.deepcopy(self.sources)
        changed["matched_create"]["candidate_controlled_gate"]["controlled_half_maximum_seconds"] += 0.1
        with self.assertRaises(validation.KernelIterationValidationError):
            decomposition.evaluate(
                self.contract,
                changed,
                self.contract["formal_state"]["sha256"],
            )


if __name__ == "__main__":
    unittest.main()
