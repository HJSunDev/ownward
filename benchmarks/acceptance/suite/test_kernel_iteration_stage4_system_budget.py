from __future__ import annotations

from pathlib import Path
import sys
import unittest


HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

import kernel_iteration_candidate_system_budget as candidate  # noqa: E402
import kernel_iteration_stage4_system_budget as system_budget  # noqa: E402


class Stage4SystemBudgetTests(unittest.TestCase):
    def test_frozen_contract_binds_native_runtime_and_system_budget(self) -> None:
        contract = system_budget.load_contract(HERE)
        self.assertEqual(contract["runtime_configuration"], candidate.SYSTEM_RUNTIME)
        self.assertEqual(contract["system_thread_budget"]["logical_processors"], 12)
        self.assertEqual(contract["system_thread_budget"]["aggregate_inference_threads"], 8)
        self.assertEqual(contract["gates"]["route_p95_ms_maximum"], 533.0)
        self.assertEqual(contract["schedule"]["expected_measured_samples"], 36)

    def test_route_closes_only_with_quality_resources_and_engineering_margin(self) -> None:
        contract = system_budget.load_contract(HERE)
        summary = self._summary(520.0)
        result = system_budget.evaluate(summary, contract)
        self.assertEqual(result["root_status"], "closed")
        self.assertFalse(result["three_thread_probe_allowed"])

        broken_quality = {**summary, "stable_selection_trace_per_case": False}
        result = system_budget.evaluate(broken_quality, contract)
        self.assertEqual(result["root_status"], "open")

        broken_resource = {**summary, "read_calls_max": 9}
        result = system_budget.evaluate(broken_resource, contract)
        self.assertEqual(result["root_status"], "open")

    def test_three_thread_probe_is_only_admitted_by_frozen_ideal_bound(self) -> None:
        contract = system_budget.load_contract(HERE)
        admissible = system_budget.evaluate(self._summary(540.0), contract)
        self.assertTrue(admissible["three_thread_probe_allowed"])
        self.assertLessEqual(admissible["ideal_3_1_projected_p95_ms"], 533.0)

        impossible = system_budget.evaluate(self._summary(570.0), contract)
        self.assertFalse(impossible["three_thread_probe_allowed"])
        self.assertEqual(impossible["next_validation"], "reject-cross-process-thread-budget-route-without-testing-3-1")

    @staticmethod
    def _summary(p95: float) -> dict[str, object]:
        return {
            "samples": 36,
            "mean_ms": p95 - 40.0,
            "p95_ms": p95,
            "max_ms": p95 + 2.0,
            "target_delivery_complete": True,
            "stable_selection_trace_per_case": True,
            "read_calls_max": 8,
            "context_chars_max": 24000,
        }


if __name__ == "__main__":
    unittest.main()
