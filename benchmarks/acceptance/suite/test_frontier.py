import copy
import unittest
from pathlib import Path

import frontier
from contract import load_contract
from materials import load_json


class FrontierLoopTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.root = Path(__file__).resolve().parent
        cls.contract = load_contract(cls.root / "contract.json")
        cls.calibration = load_json(cls.root / "materials" / "frontier" / "v1" / "calibration.json")
        dataset = load_json(cls.root / "materials" / "core" / "v1" / "dataset.json")
        cls.metrics = frontier._fixture_metrics(dataset)

    def observation(self, name: str, metrics=None):
        return frontier._observation(self.contract, name, "a" * 64, metrics or copy.deepcopy(self.metrics))

    def changed(self, name: str, delta: float):
        metrics = copy.deepcopy(self.metrics)
        item = next(value for value in metrics if value["name"] == name)
        item["value"] += delta
        return metrics

    def test_bootstrap_is_not_a_promoted_baseline(self):
        report = frontier.compare(self.contract, None, self.observation("first"), self.calibration)
        self.assertEqual("bootstrap_reference", report["decision"])
        self.assertFalse(report["baseline_promoted"])

    def test_detects_recall_ranking_relation_context_and_performance_regressions(self):
        for name, delta in (
            ("fusion_recall", -0.02),
            ("fusion_ndcg", -0.02),
            ("relation_precision", -0.02),
            ("context_precision", -0.02),
            ("query_p95_ms", 5.0),
        ):
            with self.subTest(name=name):
                report = frontier.compare(
                    self.contract,
                    self.observation("baseline"),
                    self.observation("candidate", self.changed(name, delta)),
                    self.calibration,
                )
                self.assertEqual("rejected_regression", report["decision"])

    def test_improvement_cannot_compensate_for_regression(self):
        metrics = self.changed("fusion_recall", 0.02)
        next(item for item in metrics if item["name"] == "query_p95_ms")["value"] += 5.0
        report = frontier.compare(
            self.contract,
            self.observation("baseline"),
            self.observation("candidate", metrics),
            self.calibration,
        )
        self.assertEqual("rejected_regression", report["decision"])

    def test_improvement_only_allows_qualification_and_never_promotes(self):
        report = frontier.compare(
            self.contract,
            self.observation("baseline"),
            self.observation("candidate", self.changed("fusion_recall", 0.02)),
            self.calibration,
        )
        self.assertEqual("eligible_for_qualification", report["decision"])
        self.assertTrue(report["qualification_required"])
        self.assertFalse(report["baseline_promoted"])

    def test_full_observation_cannot_omit_a_protected_stage(self):
        observation = self.observation("candidate")
        observation["metrics"] = [item for item in observation["metrics"] if item["stage"] != "relations"]
        observation["requested_stages"] = [name for name in observation["requested_stages"] if name != "relations"]
        with self.assertRaisesRegex(frontier.FrontierError, "全部受保护阶段"):
            frontier.validate_observation(self.contract, observation)

    def test_self_check_is_fast_and_not_formal_evidence(self):
        report = frontier.run_self_check(self.root)
        self.assertLess(report["elapsed_seconds"], 180)
        self.assertLess(report["mode_elapsed_seconds"]["targeted"], 180)
        self.assertLess(report["mode_elapsed_seconds"]["full"], 600)
        self.assertTrue(report["self_check"])
        self.assertFalse(report["formal_evidence"])


if __name__ == "__main__":
    unittest.main()
