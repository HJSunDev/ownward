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


if __name__ == "__main__":
    unittest.main()
