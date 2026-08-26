from __future__ import annotations

import copy
import unittest

import kernel_iteration


class KernelIterationTests(unittest.TestCase):
    def test_classifies_only_proven_overflow_as_first_direction(self) -> None:
        mechanism, direction, status, samples = kernel_iteration.classify_problem(
            "target_evidence_not_read", True, True, True, 24001, "multi-session",
        )
        self.assertEqual(mechanism, "session_unit_context_overflow")
        self.assertEqual(direction, "information_representation_and_organization")
        self.assertEqual(status, "proven_general_mechanism")
        self.assertIn("granularity-multi-source", samples)

        pending = kernel_iteration.classify_problem(
            "target_evidence_not_read", True, True, True, 24000, "multi-session",
        )
        self.assertEqual(pending[0], "pending_search_order_or_context_selection")

    def test_search_miss_remains_cross_direction_pending(self) -> None:
        result = kernel_iteration.classify_problem(
            "target_evidence_not_search_returned", True, True, False, 12000, "temporal-reasoning",
        )
        self.assertEqual(result[0], "pending_semantic_organization_or_retrieval")
        self.assertEqual(result[1], "pending_cross_direction_evidence")

    def test_problem_pool_rejects_leaked_answer_field(self) -> None:
        problems = []
        for index in range(258):
            mechanism = "session_unit_context_overflow" if index < 119 else (
                "pending_search_order_or_context_selection" if index < 133 else (
                    "pending_semantic_organization_or_retrieval" if index < 249 else "reader_failure_after_complete_evidence"
                )
            )
            problems.append({"formal_question_identity": f"q-{index}", "mechanism": mechanism})
        pool = {"schema": kernel_iteration.POOL_SCHEMA, "problem_count": 258, "problems": problems}
        kernel_iteration.validate_problem_pool(pool, strict=True)
        leaked = copy.deepcopy(pool)
        leaked["problems"][0]["product_answer"] = "forbidden"
        with self.assertRaises(kernel_iteration.KernelIterationError):
            kernel_iteration.validate_problem_pool(leaked, strict=True)

    def test_builds_baseline_and_candidate_gate_results(self) -> None:
        protected = [
            {"name": name, "value": 1.0}
            for name in ("budget_fit_protection", "identity_stability", "fusion_recall", "fusion_ndcg")
        ]
        efficiency = [
            {"name": name, "value": 0.0, "repeatability_error": 0.0}
            for name in (
                "organization_input_overhead_ratio", "organization_vector_overhead_ratio",
                "derived_record_overhead_ratio", "derived_vector_overhead_ratio",
                "rebuild_input_overhead_ratio", "rebuild_vector_overhead_ratio",
                "rebuilt_record_overhead_ratio", "rebuilt_vector_overhead_ratio",
            )
        ]
        efficiency.extend([
            {"name": "v0_query_workflow_p95_ms", "value": 10.0, "repeatability_error": 1.0},
            {"name": "candidate_query_workflow_p95_ms", "value": 11.0, "repeatability_error": 0.5},
            {"name": "v0_query_content_runes", "value": 1000.0, "repeatability_error": 0.0},
            {"name": "candidate_query_content_runes", "value": 500.0, "repeatability_error": 0.0},
            {"name": "v0_query_serialized_bytes", "value": 3000.0, "repeatability_error": 0.0},
            {"name": "candidate_query_serialized_bytes", "value": 2000.0, "repeatability_error": 0.0},
            {"name": "v0_organization_real_p95_ms", "value": 20.0, "repeatability_error": 1.0},
            {"name": "candidate_organization_real_p95_ms", "value": 21.0, "repeatability_error": 1.0},
            {"name": "v0_rebuild_real_p95_ms", "value": 40.0, "repeatability_error": 1.0},
            {"name": "candidate_rebuild_real_p95_ms", "value": 41.0, "repeatability_error": 1.0},
            {"name": "v0_derived_storage_bytes_per_source_rune", "value": 2.0, "repeatability_error": 0.1},
            {"name": "candidate_derived_storage_bytes_per_source_rune", "value": 2.1, "repeatability_error": 0.1},
        ])
        observations = {
            "direction": {"metrics": [
                {"name": "required_evidence_budget_recall", "value": 1.0},
                {"name": "required_evidence_budget_error_rate", "value": 0.0},
                {"name": "scale_evidence_recall", "value": 1.0},
                *efficiency,
                *protected[:2],
            ]},
            "protection": {"metrics": protected[2:]},
        }
        pool = {"selection": {"cluster": "session_unit_context_overflow"}}
        durations = {"direction": 1.0, "protection": 0.5}
        baseline = kernel_iteration.build_baseline({}, pool, observations, durations)
        self.assertEqual(baseline["schema"], kernel_iteration.BASELINE_SCHEMA)
        self.assertEqual(baseline["baseline"]["required_evidence_budget_recall"], 1.0)

        view = {"optimization_view": {
            "v0_baseline": {
                "budget_fit_protection": 1.0,
                "identity_stability": 1.0,
                "fusion_recall": 1.0,
                "fusion_ndcg": 1.0,
            },
            "v0_efficiency_baseline": {
                name: 0.0 for name in (
                    "organization_input_overhead_ratio", "organization_vector_overhead_ratio",
                    "derived_record_overhead_ratio", "derived_vector_overhead_ratio",
                    "rebuild_input_overhead_ratio", "rebuild_vector_overhead_ratio",
                    "rebuilt_record_overhead_ratio", "rebuilt_vector_overhead_ratio",
                )
            },
            "frozen_gate": {
                "required_evidence_budget_recall_min": 0.7778,
                "required_evidence_budget_error_rate_max": 0.3056,
                "scale_evidence_recall_min": 1.0,
                "query_workflow_p95_ms_max": 600.0,
                "query_workflow_limit_source": "thresholds.json#limits.warm_query_p95_ms_max",
            },
        }}
        candidate = kernel_iteration.build_candidate_result({}, pool, observations, durations, view, "worktree:test")
        self.assertTrue(candidate["passed"])
        self.assertEqual(candidate["gate"]["protected_regressions"], [])
        query_cost = candidate["metrics"]["efficiency_metrics"]["candidate_query_workflow_p95_ms"]
        self.assertTrue(query_cost["relative_passed"])
        self.assertTrue(query_cost["absolute_passed"])

        observations["direction"]["metrics"] = [
            {**item, "value": 30.0}
            if item["name"] == "candidate_query_workflow_p95_ms" else item
            for item in observations["direction"]["metrics"]
        ]
        relatively_slow = kernel_iteration.build_candidate_result({}, pool, observations, durations, view, "worktree:slow")
        self.assertFalse(relatively_slow["passed"])
        slow_cost = relatively_slow["metrics"]["efficiency_metrics"]["candidate_query_workflow_p95_ms"]
        self.assertFalse(slow_cost["relative_passed"])
        self.assertTrue(slow_cost["absolute_passed"])

    def test_upper_bound_ignores_only_floating_point_roundoff(self) -> None:
        self.assertTrue(kernel_iteration._within_upper_bound(42.940033436213994, 42.94003343621399))
        self.assertFalse(kernel_iteration._within_upper_bound(42.9401, 42.94003343621399))


if __name__ == "__main__":
    unittest.main()
