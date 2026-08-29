from __future__ import annotations

import unittest
from pathlib import Path

import kernel_iteration_stage4_performance as performance
import kernel_iteration_stage4_protection_performance as protection
import kernel_iteration_validation as validation


HERE = Path(__file__).resolve().parent


def sample(case: str, total: float, trace: str = "stable") -> dict[str, object]:
    return {
        "case_identity": case,
        "trace_sha256": trace,
        "search_ms": total - 20,
        "evidence_search_ms": 10,
        "read_ms": 10,
        "total_ms": total,
    }


class Stage4PerformanceTests(unittest.TestCase):
    def test_contracts_freeze_read_only_balanced_replay_before_execution(self) -> None:
        multisource = performance.load_contract(HERE)
        development = protection.load_contract(HERE)
        for contract in (multisource, development):
            self.assertTrue(contract["frozen_before_performance_replay"])
            self.assertFalse(contract["random_rerun_allowed"])
            self.assertFalse(contract["model_or_answer_execution"])
            self.assertEqual(
                [["baseline", "candidate"], ["candidate", "baseline"]],
                contract["schedule"]["balanced_order"],
            )

    def test_pair_accepts_only_frozen_repeat_delta(self) -> None:
        result = performance.evaluate(
            {
                "baseline": [sample("a", 300), sample("b", 320)],
                "candidate": [sample("a", 330), sample("b", 360)],
            },
            47,
        )
        self.assertTrue(result["passed"])
        self.assertEqual(result["candidate_minus_baseline_p95_ms"], 40)

    def test_pair_rejects_regression_and_nondeterministic_trace(self) -> None:
        result = performance.evaluate(
            {"baseline": [sample("a", 300)], "candidate": [sample("a", 348)]},
            47,
        )
        self.assertFalse(result["passed"])
        with self.assertRaisesRegex(validation.KernelIterationValidationError, "不确定"):
            performance.evaluate(
                {"baseline": [sample("a", 300)], "candidate": [sample("a", 300), sample("a", 301, "changed")]},
                47,
            )


if __name__ == "__main__":
    unittest.main()
