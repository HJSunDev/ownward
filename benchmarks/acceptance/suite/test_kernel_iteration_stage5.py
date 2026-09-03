from __future__ import annotations

import hashlib
import json
from pathlib import Path
import unittest

import evidence_identity


class Stage5GenericBoundaryTests(unittest.TestCase):
    def setUp(self) -> None:
        self.suite_root = Path(__file__).resolve().parent
        self.repository = self.suite_root.parents[2]

    def test_contract_consumes_one_generic_stage4_closure(self) -> None:
        contract_path = self.suite_root / "iteration" / "v2" / "stage5-internal-validation-contract.json"
        contract = json.loads(contract_path.read_text(encoding="utf-8"))
        content = {name: value for name, value in contract.items() if name != "identity"}
        self.assertEqual(contract["identity"], evidence_identity.canonical_sha256(content))
        self.assertNotIn("stage4_evidence", contract)
        reference = contract["stage4_closure"]
        path = self.repository / reference["path"]
        raw = path.read_bytes()
        self.assertEqual(reference["sha256"], hashlib.sha256(raw).hexdigest())
        closure = json.loads(raw)
        closure_content = {name: value for name, value in closure.items() if name != "identity"}
        self.assertEqual(reference["identity"], evidence_identity.canonical_sha256(closure_content))
        self.assertEqual(
            {"quality", "complete_consumer_latency", "semantic_cost", "storage_cost", "controlled_wall", "recovery"},
            set(closure["dimensions"]),
        )

    def test_high_level_controller_has_no_incident_specific_roles(self) -> None:
        source = (self.suite_root / "kernel_iteration_stage5.py").read_text(encoding="utf-8")
        for forbidden in ("answer-sufficiency", "current-latency", "stage4-reclosure", "diagnosis_reader"):
            self.assertNotIn(forbidden, source)


if __name__ == "__main__":
    unittest.main()
