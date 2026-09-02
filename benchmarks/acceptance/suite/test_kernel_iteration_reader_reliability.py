from __future__ import annotations

import unittest
from pathlib import Path

import kernel_iteration_reader_reliability as reliability


HERE = Path(__file__).resolve().parent


class ReaderReliabilityTests(unittest.TestCase):
    def test_contract_freezes_lowest_stable_selection_and_first_answer_rule(self) -> None:
        contract = reliability.load_contract(HERE)
        self.assertEqual(contract["reader_selection"]["ordered_efforts"], ["medium", "high", "xhigh", "max"])
        self.assertEqual(contract["reader_selection"]["mechanical_correctness_required"], 1.0)
        self.assertTrue(contract["reader_selection"]["semantic_or_wording_variation_is_not_failure"])
        self.assertTrue(contract["stage6_failure_attribution"]["first_answer_is_immutable_gate_decision"])
        self.assertTrue(contract["stage6_failure_attribution"]["diagnostic_repetitions_can_never_convert_a_failed_first_answer_to_pass"])

    def test_conservative_reader_projection_preserves_every_existing_budget(self) -> None:
        contract = reliability.load_contract(HERE)
        proof = reliability._budget_proof(contract, 10.0)
        self.assertTrue(all(item["passed"] for item in proof["levels"].values()))
        self.assertFalse(proof["quality_or_time_gate_relaxed"])

    def test_budget_rejects_reader_that_cannot_fit_original_levels(self) -> None:
        contract = reliability.load_contract(HERE)
        proof = reliability._budget_proof(contract, 100.0)
        self.assertFalse(proof["levels"]["5"]["passed"])

    def test_formal_xhigh_reader_cost_migration_keeps_full_ceiling_and_requires_preflight(self) -> None:
        source = reliability.load_formal_cost_migration(HERE)
        proof = reliability.load_active_retrieval_cost_migration(HERE, source)
        self.assertLessEqual(proof["migrated_projection"]["required_ceiling_wall_seconds"], 20400)
        self.assertGreater(proof["migrated_projection"]["margin_seconds"], 0)
        self.assertEqual(
            proof["policy"]["formal_preflight_status"],
            "pending",
        )
        self.assertTrue(proof["policy"]["active_retrieval_fully_charged_without_parallelism_credit"])


if __name__ == "__main__":
    unittest.main()
