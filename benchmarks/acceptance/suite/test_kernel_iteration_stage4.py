from __future__ import annotations

import copy
from pathlib import Path
import sys
import unittest

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

import kernel_iteration_stage4 as stage4  # noqa: E402
import kernel_iteration_candidate as candidate  # noqa: E402


class Stage4EvidenceTests(unittest.TestCase):
    def setUp(self) -> None:
        self.contract = stage4.load_contract(HERE)
        self.subject = {
            "identity": "a" * 64,
            "content": {"kernel_generation_identity": "b" * 64, "kernel_effect_identity": "c" * 64},
        }
        self.development = self._result("development", 4, [
            self._case("long-session-multi-fact", 1, 1, 5, 5),
            self._case("temporal-update-conflict", 1, 1, 4, 4),
            self._case("structured-boundary", 1, 1, 4, 4),
            self._case("multi-session-answer-sufficiency", 3, 3, 3, 3),
        ], 297.0, 48.156)
        self.regression = self._result("regression", 8, [
            self._case(f"coverage-{index}", 1, 1, 1, 1) for index in range(8)
        ], 203.0, 56.875)

    def test_frozen_contract_and_complete_candidate_results_pass(self) -> None:
        self.assertEqual(self.contract["mechanism"]["policy"], candidate.CANDIDATE_POLICY)
        metrics = stage4.validate_results(self.contract, self.subject, self.development, self.regression)
        self.assertEqual(metrics["long_multifact_delivered_claims"], 5)
        self.assertEqual(metrics["regression_accuracy"], 1.0)
        self.assertEqual(metrics["long_asset_semantic_recall"], 1.0)
        self.assertLess(metrics["affected_feedback_wall_seconds"], 600)

    def test_partial_same_source_delivery_is_rejected(self) -> None:
        value = copy.deepcopy(self.development)
        value["observation"]["case_evidence"][0]["delivered_truth_claims"] = 2
        with self.assertRaisesRegex(Exception, "完整片段交付"):
            stage4.validate_results(self.contract, self.subject, value, self.regression)

    def test_long_asset_source_recall_regression_is_rejected(self) -> None:
        value = copy.deepcopy(self.development)
        value["observation"]["case_evidence"][1]["search_returned_sources"] = 0
        with self.assertRaisesRegex(Exception, "语义召回"):
            stage4.validate_results(self.contract, self.subject, value, self.regression)

    def test_subject_or_latency_drift_is_rejected(self) -> None:
        subject_drift = copy.deepcopy(self.regression)
        subject_drift["subject_identity"] = "d" * 64
        with self.assertRaisesRegex(Exception, "没有绑定 V2 候选"):
            stage4.validate_results(self.contract, self.subject, self.development, subject_drift)
        latency_drift = copy.deepcopy(self.regression)
        latency_drift["observation"]["latency"]["retrieval_p95_ms"] = 407.0
        with self.assertRaisesRegex(Exception, "p95"):
            stage4.validate_results(self.contract, self.subject, self.development, latency_drift)

    def _result(self, evidence_type: str, questions: int, cases: list[dict], p95: float, wall: float) -> dict:
        return {
            "subject_identity": self.subject["identity"],
            "subject_role": "v2-candidate",
            "evidence_type": evidence_type,
            "passed": True,
            "candidate_decision": True,
            "observation": {
                "questions": questions,
                "final_answer_accuracy": 1.0,
                "fact_delivery": {"complete": True},
                "case_evidence": cases,
                "latency": {"retrieval_p95_ms": p95, "wall_seconds": wall},
            },
        }

    @staticmethod
    def _case(coverage: str, returned: int, expected: int, delivered: int, truth: int) -> dict:
        return {
            "coverage": coverage,
            "search_returned_sources": returned,
            "expected_sources": expected,
            "delivered_truth_claims": delivered,
            "truth_claims": truth,
        }


if __name__ == "__main__":
    unittest.main()
