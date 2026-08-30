from __future__ import annotations

import copy
from pathlib import Path
import unittest

import kernel_iteration_evidence as evidence
import kernel_iteration_stage4_resource_cost_representation_lifecycle as lifecycle
import kernel_iteration_validation as validation


class RepresentationLifecycleFeasibilityTests(unittest.TestCase):
    def setUp(self) -> None:
        self.suite_root = Path(__file__).parent
        self.repository = self.suite_root.parents[2]
        self.contract = lifecycle.load_contract(self.suite_root)
        self.sources = {
            name: lifecycle._verified_text(self.repository, item, name)
            for name, item in self.contract["source_files"].items()
        }
        self.matched = lifecycle._verified_json(self.repository, self.contract["evidence"]["matched_create"], "matched")
        self.non_create = lifecycle._verified_json(self.repository, self.contract["evidence"]["non_create"], "non-create")
        self.simple = lifecycle._verified_json(self.repository, self.contract["evidence"]["simple_deferral_audit"], "simple")
        self.materials = [{
            "name": "synthetic",
            "cases": 1,
            "truth_claims": 2,
            "semantic_work_items": 3,
            "semantic_similarity_candidates": 1,
            "accepted_relations": 0,
            "semantic_request_wall_seconds": [13.0],
            "semantic_request_wall_seconds_by_question": {
                "v2d-long-multifact": 13.0,
                "v2r-user": 13.0,
                "v2r-short-structure": 13.0,
            },
            "all_required_truth_sources_are_work_assets": True,
            "all_current_work_assets_have_full_authority_bodies": True,
            "all_current_work_items_have_accepted_analysis": True,
        }]

    def evaluate(self) -> dict:
        return lifecycle.evaluate(
            self.contract,
            self.sources,
            self.matched,
            self.non_create,
            self.simple,
            self.materials,
            self.contract["formal_state"]["sha256"],
        )

    def test_authorizes_only_new_subject_after_external_and_math_gates(self) -> None:
        result = self.evaluate()
        content = {key: item for key, item in result.items() if key != "identity"}
        self.assertEqual(result["identity"], evidence.canonical_sha256(content))
        self.assertTrue(result["authorization"]["candidate_implementation_authorized"])
        self.assertTrue(result["authorization"]["candidate_must_receive_new_subject_identity"])
        self.assertTrue(result["cost_feasibility"]["passed"])
        self.assertLessEqual(
            result["cost_feasibility"]["projected_with_repeatability_error_seconds"],
            result["cost_feasibility"]["controlled_half_maximum_seconds"],
        )

    def test_internal_work_identity_is_not_mistaken_for_external_contract(self) -> None:
        result = self.evaluate()
        self.assertTrue(result["external_contract"]["candidate_and_work_reference_may_change"])
        self.assertEqual(result["external_contract"]["simple_exact-work-deferral_audit_preserved"], self.simple["identity"])
        self.assertGreater(result["quality_feasibility"]["semantic_similarity_candidates_in_current_work"], 0)

    def test_missing_authority_fact_or_short_semantic_window_fails_closed(self) -> None:
        changed = copy.deepcopy(self.materials)
        changed[0]["all_required_truth_sources_are_work_assets"] = False
        with self.assertRaises(validation.KernelIterationValidationError):
            lifecycle.evaluate(self.contract, self.sources, self.matched, self.non_create, self.simple, changed, self.contract["formal_state"]["sha256"])
        changed = copy.deepcopy(self.materials)
        changed[0]["semantic_request_wall_seconds"] = [4.0]
        changed[0]["semantic_request_wall_seconds_by_question"]["v2r-user"] = 4.0
        with self.assertRaises(validation.KernelIterationValidationError):
            lifecycle.evaluate(self.contract, self.sources, self.matched, self.non_create, self.simple, changed, self.contract["formal_state"]["sha256"])

    def test_stale_source_or_cost_gate_fails_closed(self) -> None:
        changed_sources = dict(self.sources)
        changed_sources["collaboration"] = changed_sources["collaboration"].replace(
            "candidates := s.semanticCandidates(value, vectors, indexes...)",
            "candidates := nil",
            1,
        )
        with self.assertRaises(validation.KernelIterationValidationError):
            lifecycle.evaluate(self.contract, changed_sources, self.matched, self.non_create, self.simple, self.materials, self.contract["formal_state"]["sha256"])
        changed_gate = copy.deepcopy(self.matched)
        changed_gate["candidate_controlled_gate"]["controlled_half_maximum_seconds"] += 0.01
        with self.assertRaises(validation.KernelIterationValidationError):
            lifecycle.evaluate(self.contract, self.sources, changed_gate, self.non_create, self.simple, self.materials, self.contract["formal_state"]["sha256"])


if __name__ == "__main__":
    unittest.main()
