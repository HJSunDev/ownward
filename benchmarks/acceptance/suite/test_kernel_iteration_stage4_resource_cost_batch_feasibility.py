from __future__ import annotations

import unittest

import kernel_iteration_stage4_resource_cost_batch_feasibility as feasibility


class ResourceCostBatchFeasibilityTests(unittest.TestCase):
    def test_rejects_exact_batch_when_conservative_wall_margin_is_missing(self) -> None:
        result = feasibility.evaluate(
            {
                "identity": "contract",
                "source_candidate": {"subject_identity": "candidate"},
                "route_authorization": {"minimum_conservative_route_improvement_seconds": 4.8},
            },
            {"baseline": {"identity": "baseline"}, "batch": {"identity": "batch"}},
            [
                self._round(1, 5.0, 1.0),
                self._round(2, 5.0, 5.5),
            ],
            1.0,
            "state",
        )
        self.assertTrue(result["measurement"]["vector_identity_exact"])
        self.assertFalse(result["decision"]["route_authorized"])
        self.assertEqual(-0.5, result["measurement"]["conservative_improvement_seconds"])

    def test_authorizes_only_when_both_balanced_pairs_cross_frozen_gate(self) -> None:
        result = feasibility.evaluate(
            {
                "identity": "contract",
                "source_candidate": {"subject_identity": "candidate"},
                "route_authorization": {"minimum_conservative_route_improvement_seconds": 4.8},
            },
            {"baseline": {"identity": "baseline"}, "batch": {"identity": "batch"}},
            [
                self._round(1, 10.0, 5.0),
                self._round(2, 10.2, 5.1),
            ],
            1.0,
            "state",
        )
        self.assertTrue(result["decision"]["route_authorized"])

    @staticmethod
    def _round(number: int, baseline_seconds: float, batch_seconds: float) -> dict:
        question = {
            "assets": 3,
            "embedding_inputs": 3,
            "vector_identities": ["a", "b", "c"],
        }
        return {
            "round": number,
            "variant_order": ["baseline", "batch"],
            "variants": {
                "baseline": {
                    "envelope_sum_seconds": baseline_seconds,
                    "embedding_calls": 3,
                    "questions": {"v2r-user": question},
                },
                "batch": {
                    "envelope_sum_seconds": batch_seconds,
                    "embedding_calls": 1,
                    "questions": {"v2r-user": question},
                },
            },
        }


if __name__ == "__main__":
    unittest.main()
