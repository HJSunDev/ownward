from __future__ import annotations

import json
from pathlib import Path
import sys
import tempfile
import unittest

HERE = Path(__file__).resolve().parent
REPOSITORY = HERE.parents[2]
sys.path.insert(0, str(HERE))

import kernel_iteration_evidence as evidence  # noqa: E402
import kernel_iteration_candidate_multisource as candidate  # noqa: E402
import kernel_iteration_stage4_multisource as multisource  # noqa: E402


class Stage4MultisourceTests(unittest.TestCase):
    def test_contract_freezes_independent_materials_and_gate_before_diagnosis(self) -> None:
        value = multisource.load_contract(HERE)
        self.assertTrue(value["frozen_before_current_candidate_multisource_results"])
        self.assertEqual(3, len(value["materials"]["cases"]))
        self.assertEqual(value["sources"]["materials"], value["input"]["shared_conditions"]["dataset"])
        self.assertFalse(value["stage4_may_complete"])
        self.assertFalse(value["blind_gate_allowed"])

    def test_route_is_frozen_after_diagnosis_and_before_new_candidate_results(self) -> None:
        route = multisource.load_route(HERE)
        self.assertEqual("proven", route["root_status"])
        self.assertEqual("source-depth-consumed-read-budget-before-target-rank", route["first_proven_mechanism"])
        self.assertEqual(candidate.CANDIDATE_POLICY, route["route"]["policy"])
        self.assertTrue(route["frozen_before_new_candidate_results"])

    def test_candidate_source_transform_is_exact_and_does_not_edit_current_source(self) -> None:
        source = REPOSITORY / "internal" / "core" / "service.go"
        original = source.read_bytes()
        transform = REPOSITORY / "manifests" / "kernel-candidates" / "v2" / "multisource-complete" / "service-transform.json"
        with tempfile.TemporaryDirectory(dir=REPOSITORY) as temporary:
            output = Path(temporary) / "core-service.go"
            candidate._render_transformed_source(REPOSITORY, transform, output)
            rendered = output.read_text(encoding="utf-8")
        self.assertEqual(original, source.read_bytes())
        self.assertIn("evidencePlans *kernelv2candidate.EvidencePlans", rendered)
        self.assertIn("s.evidencePlans.ObserveSearch(input.Query, sourceIDs)", rendered)
        self.assertIn("s.evidencePlans.References", rendered)

    def test_diagnosis_records_first_post_search_deviation_without_candidate_decision(self) -> None:
        contract = multisource.load_contract(HERE)
        with tempfile.TemporaryDirectory(dir=REPOSITORY) as temporary:
            root = Path(temporary)
            subject = {
                "schema": "ownward.kernel-iteration-subject/v1", "role": "v2-candidate",
                "kernel_generation_identity": "1" * 64, "kernel_effect_identity": "2" * 64,
                "direct_dependencies": {"authority-substrate": "3" * 64, "product-rules": "4" * 64, "semantic": "5" * 64, "vector": "6" * 64},
                "artifacts": {"binary": "7" * 64},
            }
            subject["identity"] = contract["sources"]["first_candidate_subject"]
            subject["name"] = "fixture"
            subject_path = root / "subject.json"
            subject_path.write_text(json.dumps(subject), encoding="utf-8")
            cases = []
            for index in range(3):
                cases.append({
                    "case_identity": str(index + 1) * 64, "coverage": f"coverage-{index}", "correct": False,
                    "expected_sources": 1, "search_returned_sources": 1, "read_sources": 0,
                    "delivered_truth_claims": 0, "truth_claims": 1,
                    "first_proven_mechanism": "source-depth-consumed-read-budget-before-target-rank",
                    "selection": {
                        "selected_units": 8, "selected_sources": 4, "selected_depth_units": 4,
                        "selected_nonrequired_sources": 4, "expected_sources": [], "context_chars": 6000,
                    },
                })
            result_content = {
                "schema": "ownward.kernel-iteration-execution-evidence/v3", "plan_identity": "8" * 64,
                "subject_identity": subject["identity"], "subject_role": "v2-candidate", "subject_name": "fixture",
                "comparison_purpose": "candidate-evaluation", "evidence_type": "development", "status": "failed",
                "passed": False, "candidate_decision": False, "formal": False, "formal_state_written": False,
                "observation": {"case_evidence": cases}, "failure_feedback": [],
                "input_identity": contract["sources"]["input"],
                "shared_conditions": {"dataset": contract["sources"]["materials"]},
                "direct_dependencies": {},
            }
            result = {**result_content, "identity": evidence.canonical_sha256(result_content)}
            result_path = root / "execution-result.json"
            result_path.write_text(json.dumps(result), encoding="utf-8")
            state = root / "state.json"
            state.write_bytes(b"fixture-state")
            contract["sources"]["formal_state_sha256"] = evidence.file_sha256(state)
            original_load = multisource.load_contract
            original_validate = evidence.validate_v2_subject
            try:
                multisource.load_contract = lambda _root: contract
                evidence.validate_v2_subject = lambda _contract, value: value
                diagnosed = multisource.diagnose(HERE, root, subject_path, result_path, state)
            finally:
                multisource.load_contract = original_load
                evidence.validate_v2_subject = original_validate
            self.assertEqual("proven", diagnosed["root_status"])
            self.assertEqual("source-depth-consumed-read-budget-before-target-rank", diagnosed["first_proven_mechanism"])
            self.assertIsNone(diagnosed["candidate_decision"])
            self.assertFalse(diagnosed["formal_state_written"])

    def test_performance_report_identity_is_not_widened(self) -> None:
        with tempfile.TemporaryDirectory(dir=REPOSITORY) as temporary:
            path = Path(temporary) / "report.json"
            content = {"schema": "fixture/v1", "passed": True}
            value = {**content, "identity": evidence.canonical_sha256(content)}
            path.write_text(json.dumps(value), encoding="utf-8")
            self.assertEqual(value, multisource._load_performance(path, "fixture/v1"))
            value["passed"] = False
            path.write_text(json.dumps(value), encoding="utf-8")
            with self.assertRaisesRegex(Exception, "身份漂移"):
                multisource._load_performance(path, "fixture/v1")


if __name__ == "__main__":
    unittest.main()
