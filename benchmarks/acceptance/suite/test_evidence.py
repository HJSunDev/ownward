import copy
import tempfile
import unittest
from pathlib import Path

import evidence
from contract import load_contract
from materials import load_json


class EvidenceLayerTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.root = Path(__file__).resolve().parent
        cls.contract = load_contract(cls.root / "contract.json")
        cls.binding = {"candidate": "candidate-1", "binary_sha256": "a" * 64}
        cls.binding.update({"environment": {"sha256": "b" * 64}, "inputs": {"sha256": "c" * 64}})

    def common(self, schema: str) -> dict:
        return {
            "schema": schema,
            "suite_version": "1.0.0",
            **self.binding,
            "environment": {"sha256": "b" * 64},
            "inputs": {"sha256": "c" * 64},
            "started_at": "2026-08-21T00:00:00Z",
            "finished_at": "2026-08-21T00:00:01Z",
            "passed": True,
        }

    def core_report(self) -> dict:
        report = self.common("ownward.core-baseline-report/v1")
        report["invariants"] = {
            name: True for name in self.contract["evidence_layers"]["core"]["required_invariants"]
        }
        return report

    def product_report(self) -> dict:
        report = self.common("ownward.product-report/v1")
        report.update({
            "dataset_version": "ownward-product-dataset/v1",
            "mode": "qualification",
            "categories": {name: {"scenarios": 2, "passed": True} for name in self.contract["evidence_layers"]["product"]["categories"]},
            "organization_gain": {"passed": True},
            "quality": {"passed": True},
            "latency": {"passed": True},
            "resources": {"passed": True},
        })
        return report

    def community_report(self) -> dict:
        report = self.common("ownward.longmemeval-report/v1")
        report.update({
            "official_version": "longmemeval-v2/2cc8c540bdb87fe6761629b585e727e1c4704520",
            "domains": {"web": {"passed": True}, "enterprise": {"passed": True}},
            "submission": {
                "package_sha256": "d" * 64, "lafs": 0.5, "accuracy": 0.75,
                "latency_seconds": 10.0, "frontier_eligible": True,
                "reference_frontier": [{"accuracy": 74.9, "latency_seconds": 108.3}],
            },
        })
        return report

    def test_three_layer_adapters_and_materials_are_consistent(self):
        evidence.validate_suite_inputs(self.root)

    def test_accepts_complete_reports_for_exactly_three_layers(self):
        for layer, report in (("core", self.core_report()), ("product", self.product_report()), ("community", self.community_report())):
            with self.subTest(layer=layer):
                evidence.validate_layer_report(self.contract, layer, report, expected_binding=self.binding)

    def test_report_artifacts_are_portable_and_tamper_evident(self):
        with tempfile.TemporaryDirectory() as directory:
            workspace = Path(directory) / "workspace"
            report_path = workspace / "reports" / "core.json"
            raw = workspace / "evidence" / "core" / "raw.json"
            raw.parent.mkdir(parents=True)
            raw.write_text('{"passed":true}\n', encoding="utf-8")
            report = self.core_report()
            evidence.attach_artifacts(report, report_path, [raw.parent])
            manifest_sha = evidence.validate_report_artifacts(report_path, report)
            self.assertEqual(report["artifacts"]["manifest_sha256"], manifest_sha)
            self.assertFalse(Path(report["artifacts"]["files"][0]["path"]).is_absolute())
            raw.write_text('{"passed":false}\n', encoding="utf-8")
            with self.assertRaisesRegex(evidence.EvidenceError, "原始证据"):
                evidence.validate_report_artifacts(report_path, report)

    def test_rejects_missing_core_invariant(self):
        report = self.core_report()
        report["invariants"].pop("backup_restore")
        with self.assertRaisesRegex(evidence.EvidenceError, "不变量"):
            evidence.validate_layer_report(self.contract, "core", report)

    def test_rejects_partial_or_wrong_product_evidence(self):
        report = self.product_report()
        report["categories"]["cross_time"]["passed"] = False
        with self.assertRaisesRegex(evidence.EvidenceError, "总判定"):
            evidence.validate_layer_report(self.contract, "product", report)

    def test_valid_failure_report_is_preserved_as_evidence(self):
        report = self.community_report()
        report["submission"]["lafs"] = 0.0
        report["submission"]["frontier_eligible"] = False
        report["passed"] = False
        evidence.validate_layer_report(self.contract, "community", report)

    def test_rejects_wrong_official_revision_or_incomplete_domains(self):
        report = self.community_report()
        report["official_version"] = "longmemeval-v2/latest"
        with self.assertRaisesRegex(evidence.EvidenceError, "官方版本"):
            evidence.validate_layer_report(self.contract, "community", report)

    def test_product_scorer_accepts_complete_evidence_and_rejects_relation_regression(self):
        dataset = load_json(self.root / "materials" / "product" / "v1" / "dataset.json")
        qualification = load_json(self.root / "materials" / "product" / "v1" / "qualification.json")
        scenarios = {item["truth"]["id"]: item for item in dataset["scenarios"]}
        results = []
        for identifier in qualification["scenario_ids"]:
            truth = scenarios[identifier]["truth"]["query"]
            results.append({
                "scenario_id": identifier,
                "returned_ids": list(truth["expected_ids"]),
                "direct_ids": list(truth["expected_ids"][:-1]),
                "navigation_ids": list(truth["expected_ids"][-1:]),
                "answer_facts": list(truth.get("answer_facts", [])),
                "grounded": True,
                "used_navigation": True,
                "latency_ms": 1.0,
                "semantic_ms": 2.0,
                "agent_query_ms": 3.0,
                "end_to_end_ms": 6.0,
                "peak_mib": 10.0,
                "within_latency_budget": True,
                "within_resource_budget": True,
            })
        report = evidence.score_product(self.contract, dataset, qualification, "qualification", results, self.binding)
        self.assertTrue(report["passed"])
        self.assertEqual(1.0, report["latency"]["ownward_query_max_ms"])
        self.assertEqual(2.0, report["latency"]["semantic_collaboration_max_ms"])
        self.assertEqual(3.0, report["latency"]["agent_query_max_ms"])
        self.assertEqual(6.0, report["latency"]["scenario_end_to_end_max_ms"])
        broken = copy.deepcopy(results)
        broken[0]["returned_ids"] = list(broken[0]["direct_ids"])
        failed = evidence.score_product(self.contract, dataset, qualification, "qualification", broken, self.binding)
        self.assertFalse(failed["passed"])
        no_navigation = copy.deepcopy(results)
        for item in no_navigation:
            item["used_navigation"] = False
            item["navigation_ids"] = []
        failed = evidence.score_product(self.contract, dataset, qualification, "qualification", no_navigation, self.binding)
        self.assertFalse(failed["organization_gain"]["passed"])
        report = self.community_report()
        report["domains"].pop("enterprise")
        with self.assertRaisesRegex(evidence.EvidenceError, "领域"):
            evidence.validate_layer_report(self.contract, "community", report)


if __name__ == "__main__":
    unittest.main()
