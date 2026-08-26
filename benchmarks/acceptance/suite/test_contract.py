from __future__ import annotations

import copy
from pathlib import Path
import unittest

import contract


CONTRACT_PATH = Path(__file__).with_name("contract.json")


class AcceptanceSuiteContractTests(unittest.TestCase):
    def setUp(self) -> None:
        self.value = contract.load_contract(CONTRACT_PATH)

    def test_contract_has_one_loop_and_exactly_three_evidence_layers(self) -> None:
        self.assertEqual(set(self.value["evidence_layers"]), {"core", "product", "community"})
        self.assertEqual(set(self.value["optimization_loop"]["modes"]), {"targeted", "full"})

    def test_contract_rejects_compensating_score(self) -> None:
        changed = copy.deepcopy(self.value)
        changed["optimization_loop"]["allow_compensating_score"] = True
        with self.assertRaisesRegex(ValueError, "综合分"):
            contract.validate_contract(changed)

    def test_contract_rejects_fourth_evidence_layer(self) -> None:
        changed = copy.deepcopy(self.value)
        changed["evidence_layers"]["dynamic"] = copy.deepcopy(changed["evidence_layers"]["product"])
        with self.assertRaisesRegex(ValueError, "三层"):
            contract.validate_contract(changed)

    def test_contract_rejects_estimated_external_frontier(self) -> None:
        changed = copy.deepcopy(self.value)
        changed["optimization_loop"]["external_frontier"]["allow_estimated_conversion"] = True
        with self.assertRaisesRegex(ValueError, "估算换算"):
            contract.validate_contract(changed)

    def test_self_check_cannot_emit_formal_result(self) -> None:
        changed = copy.deepcopy(self.value)
        changed["execution"]["self_check"]["may_emit_acceptance_result"] = True
        with self.assertRaisesRegex(ValueError, "体系自检"):
            contract.validate_contract(changed)

    def test_formal_reports_must_bind_raw_evidence(self) -> None:
        changed = copy.deepcopy(self.value)
        changed["execution"]["artifact_manifest_required"] = False
        with self.assertRaisesRegex(ValueError, "原始证据"):
            contract.validate_contract(changed)

    def test_community_contract_rejects_cross_profile_accuracy_threshold(self) -> None:
        changed = copy.deepcopy(self.value)
        changed["evidence_layers"]["community"]["minimum_accuracy"] = 0.822
        with self.assertRaisesRegex(ValueError, "硬门槛"):
            contract.validate_contract(changed)

    def test_report_contract_accepts_complete_report_and_rejects_missing_field(self) -> None:
        report_contract = self.value["reports"]["core"]
        report = {name: "value" for name in report_contract["required"]}
        report["schema"] = report_contract["schema"]
        report["suite_version"] = self.value["suite_version"]
        contract.validate_report(self.value, "core", report)
        del report["candidate"]
        with self.assertRaisesRegex(ValueError, "candidate"):
            contract.validate_report(self.value, "core", report)


if __name__ == "__main__":
    unittest.main()
