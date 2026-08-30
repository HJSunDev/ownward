from __future__ import annotations

import copy
from pathlib import Path
import unittest

import kernel_iteration_evidence as evidence
import kernel_iteration_stage4_resource_cost_raw_vector_lifecycle as lifecycle
import kernel_iteration_validation as validation


class RawVectorLifecycleTests(unittest.TestCase):
    def setUp(self) -> None:
        self.suite_root = Path(__file__).parent
        self.repository = self.suite_root.parents[2]
        self.contract = lifecycle.load_contract(self.suite_root)
        self.sources = {
            name: lifecycle._verified_text(self.repository, item, name)
            for name, item in self.contract["source_files"].items()
        }
        self.matched_create = lifecycle._verified_json(
            self.repository,
            self.contract["evidence"]["matched_create"],
            "matched-create",
        )
        self.non_create = lifecycle._verified_json(
            self.repository,
            self.contract["evidence"]["non_create_decomposition"],
            "non-create",
        )

    def evaluate(self) -> dict:
        return lifecycle.evaluate(
            self.contract,
            self.sources,
            self.matched_create,
            self.non_create,
            self.contract["formal_state"]["sha256"],
        )

    def test_lifecycle_rejects_deferral_before_implementation(self) -> None:
        result = self.evaluate()
        content = {key: item for key, item in result.items() if key != "identity"}
        self.assertEqual(result["identity"], evidence.canonical_sha256(content))
        self.assertFalse(result["authorization"]["implementation_authorized"])
        self.assertFalse(result["authorization"]["candidate_built"])
        self.assertEqual(0, result["execution"]["model_executions"])
        self.assertEqual(0, result["execution"]["product_executions"])
        self.assertEqual(
            "reject-raw-vector-deferral-before-implementation-and-keep-stage4-open",
            result["decision"],
        )

    def test_gross_envelope_has_margin_but_exact_lifecycle_has_none(self) -> None:
        cost = self.evaluate()["cost_lower_bound"]
        self.assertGreater(cost["gross_opportunity_margin_seconds"], 0)
        self.assertAlmostEqual(cost["observed_short_raw_embedding_after_common_startup_seconds"], 4.9529071999999985)
        self.assertAlmostEqual(cost["required_real_improvement_seconds"], 3.796496725001143)
        self.assertEqual(cost["proven_net_removable_lower_bound_seconds"], 0)
        self.assertEqual(cost["proven_net_removable_upper_bound_under_exact_work_identity_seconds"], 0)

    def test_pending_ready_and_rebuild_duties_are_all_registered(self) -> None:
        result = self.evaluate()
        stages = {item["stage"]: item for item in result["lifecycle"]}
        self.assertIn("semantic-work-freeze", stages)
        self.assertIn("ready-query", stages)
        self.assertIn("restart-and-rebuild", stages)
        self.assertIn("embedding-failure", stages)
        self.assertIn("candidate-selection", stages["pending-preparation"]["raw_vector_duty"])
        self.assertIn("normal post-submit", stages["ready-query"]["raw_vector_duty"])
        self.assertTrue(all(not item["authorized"] for item in result["route_assessment"]))

    def test_source_and_gate_drift_fail_closed(self) -> None:
        changed_sources = dict(self.sources)
        changed_sources["collaboration"] = changed_sources["collaboration"].replace(
            "candidates := s.semanticCandidates(value, vectors, indexes...)",
            "candidates := nil",
            1,
        )
        with self.assertRaises(validation.KernelIterationValidationError):
            lifecycle.evaluate(
                self.contract,
                changed_sources,
                self.matched_create,
                self.non_create,
                self.contract["formal_state"]["sha256"],
            )
        changed_gate = copy.deepcopy(self.matched_create)
        changed_gate["candidate_controlled_gate"]["controlled_half_maximum_seconds"] += 0.1
        with self.assertRaises(validation.KernelIterationValidationError):
            lifecycle.evaluate(
                self.contract,
                self.sources,
                changed_gate,
                self.non_create,
                self.contract["formal_state"]["sha256"],
            )


if __name__ == "__main__":
    unittest.main()
