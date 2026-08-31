from __future__ import annotations

import copy
import json
from pathlib import Path
import unittest
from unittest import mock

import kernel_iteration_evidence as evidence
import kernel_iteration_stage4_resource_cost_raw_vector_lifecycle as lifecycle
import kernel_iteration_validation as validation


class RawVectorLifecycleTests(unittest.TestCase):
    def setUp(self) -> None:
        self.suite_root = Path(__file__).parent
        self.repository = self.suite_root.parents[2]
        self.contract = lifecycle.load_contract(self.suite_root)
        self.sources = {
            name: lifecycle._verified_text(self.repository, item, name)
            for name, item in self.contract["source_files"].items()
        }
        self.matched_create = lifecycle._verified_json(
            self.repository,
            self.contract["evidence"]["matched_create"],
            "matched-create",
        )
        self.non_create = lifecycle._verified_json(
            self.repository,
            self.contract["evidence"]["non_create_decomposition"],
            "non-create",
        )

    def evaluate(self) -> dict:
        return lifecycle.evaluate(
            self.contract,
            self.sources,
            self.matched_create,
            self.non_create,
            self.contract["formal_state"]["sha256"],
        )

    def test_lifecycle_rejects_deferral_before_implementation(self) -> None:
        result = self.evaluate()
        content = {key: item for key, item in result.items() if key != "identity"}
        self.assertEqual(result["identity"], evidence.canonical_sha256(content))
        self.assertFalse(result["authorization"]["implementation_authorized"])
        self.assertFalse(result["authorization"]["candidate_built"])
        self.assertEqual(0, result["execution"]["model_executions"])
        self.assertEqual(0, result["execution"]["product_executions"])
        self.assertEqual(
            "reject-raw-vector-deferral-before-implementation-and-keep-stage4-open",
            result["decision"],
        )

    def test_gross_envelope_has_margin_but_exact_lifecycle_has_none(self) -> None:
        cost = self.evaluate()["cost_lower_bound"]
        self.assertGreater(cost["gross_opportunity_margin_seconds"], 0)
        self.assertAlmostEqual(cost["observed_short_raw_embedding_after_common_startup_seconds"], 4.9529071999999985)
        self.assertAlmostEqual(cost["required_real_improvement_seconds"], 3.796496725001143)
        self.assertEqual(cost["proven_net_removable_lower_bound_seconds"], 0)
        self.assertEqual(cost["proven_net_removable_upper_bound_under_exact_work_identity_seconds"], 0)

    def test_pending_ready_and_rebuild_duties_are_all_registered(self) -> None:
        result = self.evaluate()
        stages = {item["stage"]: item for item in result["lifecycle"]}
        self.assertIn("semantic-work-freeze", stages)
        self.assertIn("ready-query", stages)
        self.assertIn("restart-and-rebuild", stages)
        self.assertIn("embedding-failure", stages)
        self.assertIn("candidate-selection", stages["pending-preparation"]["raw_vector_duty"])
        self.assertIn("normal post-submit", stages["ready-query"]["raw_vector_duty"])
        self.assertTrue(all(not item["authorized"] for item in result["route_assessment"]))

    def test_source_and_gate_drift_fail_closed(self) -> None:
        changed_sources = dict(self.sources)
        changed_sources["collaboration"] = changed_sources["collaboration"].replace(
            "candidates := s.semanticCandidates(value, vectors, indexes...)",
            "candidates := nil",
            1,
        )
        with self.assertRaises(validation.KernelIterationValidationError):
            lifecycle.evaluate(
                self.contract,
                changed_sources,
                self.matched_create,
                self.non_create,
                self.contract["formal_state"]["sha256"],
            )
        changed_gate = copy.deepcopy(self.matched_create)
        changed_gate["candidate_controlled_gate"]["controlled_half_maximum_seconds"] += 0.1
        with self.assertRaises(validation.KernelIterationValidationError):
            lifecycle.evaluate(
                self.contract,
                self.sources,
                changed_gate,
                self.non_create,
                self.contract["formal_state"]["sha256"],
            )

    def test_dependency_migration_receipt_rejects_any_unlisted_drift(self) -> None:
        original = evidence.file_sha256

        def drift(path: Path) -> str:
            if Path(path).name == "kernel_iteration_run.py":
                return "f" * 64
            return original(path)

        with mock.patch.object(evidence, "file_sha256", side_effect=drift):
            with self.assertRaisesRegex(validation.KernelIterationValidationError, "不在精确迁移收据内"):
                lifecycle.load_contract(self.suite_root)

    def test_dependency_migration_is_exactly_unrelated_cli_maintenance(self) -> None:
        receipt = json.loads((self.suite_root / lifecycle.DEPENDENCY_MIGRATION_PATH).read_text(encoding="utf-8"))
        changes = {item["path"]: item for item in receipt["changes"]}
        run_path = "benchmarks/acceptance/suite/kernel_iteration_run.py"
        validator_path = "benchmarks/acceptance/suite/kernel_iteration_stage4_resource_cost_raw_vector_lifecycle.py"
        self.assertEqual(
            "non-stage4-diagnostic-maintenance-changes-only-explicitly-listed-callers-without-changing-frozen-stage4-contracts-or-results",
            receipt["reason"],
        )
        self.assertEqual(
            "additive-non-stage4-cli-dispatch-only",
            changes[run_path]["classification"],
        )
        self.assertEqual("dependency-receipt-validation-only", changes[validator_path]["classification"])
        self.assertEqual(evidence.file_sha256(self.repository / run_path), changes[run_path]["current_sha256"])
        self.assertEqual(evidence.file_sha256(self.repository / validator_path), changes[validator_path]["current_sha256"])
        related = receipt["related_contract_migrations"]
        self.assertEqual(
            {
                "125278c6aa9d6a34dc91bfe1ced32b22b93dad60ce6b5bc34aa14c3c2561abb7",
                "84d30456e65da337f32d73769deafb26bf8f61f4a82053d87cd14e19843887bb",
                "e39272da7f832ed8275f99284aa03ad8fdf1b68b7833a368b9bece116ef93ce8",
                "db0de5be1d737ea23e9fd6a14ee1e8288b7fc1fac6bef905e66adfb52e71a927",
            },
            set(related),
        )
        for contract_identity in related:
            runner = next(
                item for item in related[contract_identity]["changes"]
                if item["path"] == "benchmarks/longmemeval_s/run.py"
            )
            self.assertEqual("reader-profile-validation-and-formal-protocol-unification-only", runner["classification"])
            self.assertEqual(
                evidence.file_sha256(self.repository / runner["path"]),
                runner["current_sha256"],
            )
        self.assertEqual(
            {
                "contract_identity": True,
                "source_files": True,
                "thresholds": True,
                "evidence_identities": True,
                "formal_state_sha256": True,
            },
            receipt["preserved"],
        )
        self.assertEqual("125278c6aa9d6a34dc91bfe1ced32b22b93dad60ce6b5bc34aa14c3c2561abb7", self.contract["identity"])
        self.assertAlmostEqual(6.711906625000376, self.contract["active_gate"]["controlled_half_maximum_seconds"])
        self.assertEqual("3c7826e0ef86a82ddfab676886e384e2674a23dc80f6df3612d4443be00ffcdc", self.contract["formal_state"]["sha256"])


if __name__ == "__main__":
    unittest.main()
