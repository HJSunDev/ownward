from __future__ import annotations

import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock


HERE = Path(__file__).resolve().parent
REPOSITORY = HERE.parents[2]
import sys

sys.path.insert(0, str(HERE))

import kernel_iteration_evidence as iteration  # noqa: E402
import kernel_iteration_validation as validation  # noqa: E402


class KernelIterationValidationTests(unittest.TestCase):
    def setUp(self) -> None:
        self.contract = iteration.load_contract(HERE)
        self.validation = validation.load_validation_contract(HERE)

    def test_versioned_validation_contract_is_clean_checkout_safe_and_precalibration(self) -> None:
        self.assertTrue(self.validation["frozen_before_calibration"])
        self.assertFalse(self.validation["contains_formal_questions_answers_gold_or_content"])
        serialized = json.dumps(self.validation, ensure_ascii=False)
        self.assertNotIn(".tmp", serialized)
        self.assertEqual([5, 15, 25, 50], self.validation["blind"]["levels"])

    def test_versioned_blind_budget_is_clean_checkout_safe_and_bound_to_calibration(self) -> None:
        value = validation.load_blind_budget_archive(HERE)
        self.assertEqual(self.validation["identity"], value["validation_contract_identity"])
        self.assertEqual(4215, value["total_normal_seconds"])
        self.assertLessEqual(value["total_normal_seconds"], value["design_total_normal_seconds"])
        self.assertIsNone(value["calibration"]["candidate_decision"])

    def test_current_blind_budget_rejects_non_controller_dependency_drift(self) -> None:
        with tempfile.TemporaryDirectory(dir=REPOSITORY) as temporary:
            output = Path(temporary)
            dependencies = {
                "binary": "1" * 64,
                "comparison-contract": "2" * 64,
                "controller": "3" * 64,
                "embedding": "4" * 64,
                "environment": "5" * 64,
                "executor": "6" * 64,
                "generator": "7" * 64,
                "model-profile": "8" * 64,
                "observer": "9" * 64,
                "quality-admission": "a" * 64,
                "runtime-calibration": "b" * 64,
                "subject": "c" * 64,
                "validation-contract": self.validation["identity"],
            }
            plan_content = {
                "schema": validation.BLIND_PLAN_SCHEMA,
                "comparison_contract_identity": "2" * 64,
                "validation_contract_identity": self.validation["identity"],
                "subject_identity": "c" * 64,
                "purpose": "non-candidate-five-question-calibration",
                "candidate_decision": None,
                "seed_sha256": "d" * 64,
                "direct_dependencies": dependencies,
                "formal": False,
            }
            plan_identity = iteration.canonical_sha256(plan_content)
            plan = {**plan_content, "identity": plan_identity}
            root = output / "blind-calibration" / plan_identity
            self._write_json(root / "plan.json", plan)
            result = validation._blind_terminal(
                plan_identity, self.validation, status="quality-rejected", passed=False,
                generator_usage=self._usage(1), admission_usage=self._usage(1),
                admission={"passed": False, "questions": 5, "passed_counts": {}, "rejected_count": 5},
                controls={"passed": True, "outcomes": []}, executions=[], resume_proof=None,
                total_wall_seconds=2,
            )
            self._write_json(root / "result.json", result)
            verifier_identity = validation._blind_current_verifier_identity()
            locator_content = {
                "schema": validation.BLIND_DEPENDENCY_LOCATOR_SCHEMA,
                "plan_identity": plan_identity,
                "execution_config": str((output / "execution.json").resolve()),
                "formal_state": str((output / "state.json").resolve()),
                "current_verifier_identity": verifier_identity,
            }
            locator = {**locator_content, "identity": iteration.canonical_sha256(locator_content)}
            self._write_json(root / "dependency-locator.json", locator)
            budget = {
                "calibration": {
                    "plan_identity": plan_identity,
                    "result_identity": result["identity"],
                    "current_verifier_identity": verifier_identity,
                }
            }
            with mock.patch.object(validation, "load_blind_budget_archive", return_value=budget), mock.patch.object(validation, "_blind_current_verifier_identity", return_value=verifier_identity), mock.patch.object(validation, "_current_blind_dependencies", return_value=dependencies):
                self.assertTrue(validation.load_blind_budget(HERE, output)["current_valid"])

            drifted = dict(dependencies)
            drifted["environment"] = "e" * 64
            with mock.patch.object(validation, "load_blind_budget_archive", return_value=budget), mock.patch.object(validation, "_blind_current_verifier_identity", return_value=verifier_identity), mock.patch.object(validation, "_current_blind_dependencies", return_value=drifted):
                with self.assertRaisesRegex(validation.KernelIterationValidationError, "当前直接依赖已漂移"):
                    validation.load_blind_budget(HERE, output)

    def test_blind_role_identities_ignore_unrelated_budget_loading_but_scope_real_prompt_changes(self) -> None:
        baseline = validation._blind_implementation_identities()
        original = validation.inspect.getsource

        def changed_budget_only(callback):
            source = original(callback)
            return source + "\n# changed\n" if callback is validation.load_blind_budget else source

        with mock.patch.object(validation.inspect, "getsource", side_effect=changed_budget_only):
            self.assertEqual(baseline, validation._blind_implementation_identities())

        def changed_generator(callback):
            source = original(callback)
            return source + "\n# changed\n" if callback is validation._generator_prompt else source

        with mock.patch.object(validation.inspect, "getsource", side_effect=changed_generator):
            changed = validation._blind_implementation_identities()
        self.assertNotEqual(baseline["generator"], changed["generator"])
        self.assertEqual(baseline["quality-admission"], changed["quality-admission"])
        self.assertEqual(baseline["observer"], changed["observer"])

        def changed_controller(callback):
            source = original(callback)
            return source + "\n# changed\n" if callback is validation._destroy_blind_scratch else source

        with mock.patch.object(validation.inspect, "getsource", side_effect=changed_controller):
            changed = validation._blind_implementation_identities()
        self.assertNotEqual(baseline["controller"], changed["controller"])
        self.assertEqual(baseline["generator"], changed["generator"])

    def test_materials_reject_answer_leak_and_unbound_truth(self) -> None:
        materials = self._materials(1)
        leaked = json.loads(json.dumps(materials))
        leaked["cases"][0]["question"] += " The answer is sapphire."
        leaked["identity"] = iteration.canonical_sha256({key: item for key, item in leaked.items() if key != "identity"})
        with self.assertRaises(validation.KernelIterationValidationError):
            validation.validate_materials(leaked)

        unbound = json.loads(json.dumps(materials))
        unbound["cases"][0]["truth_claims"][0]["evidence_session_ids"] = ["c01-s03"]
        unbound["identity"] = iteration.canonical_sha256({key: item for key, item in unbound.items() if key != "identity"})
        with self.assertRaises(validation.KernelIterationValidationError):
            validation.validate_materials(unbound)

    def test_multi_part_answer_binds_each_truth_claim_to_its_own_evidence(self) -> None:
        materials = self._materials(5)
        case = materials["cases"][1]
        first_id = "c02-s01"
        second_id = "c02-s02"
        case["answer_session_ids"] = [first_id, second_id]
        case["answer"] = "copper and cedar"
        case["truth_claims"] = [
            {"claim": "copper", "evidence_session_ids": [first_id]},
            {"claim": "cedar", "evidence_session_ids": [second_id]},
        ]
        case["sessions"][0]["turns"][0]["content"] = "The first bound fact is copper."
        case["sessions"][1]["turns"][0]["content"] = "The complete result is copper and cedar; the second bound fact is cedar."
        materials["identity"] = iteration.canonical_sha256({key: item for key, item in materials.items() if key != "identity"})
        validation.validate_materials(materials, expected_questions=5)

        materials["cases"][1]["truth_claims"][1]["claim"] = "unbound-claim"
        materials["identity"] = iteration.canonical_sha256({key: item for key, item in materials.items() if key != "identity"})
        with self.assertRaisesRegex(validation.KernelIterationValidationError, "真值项没有绑定到自身逐字证据"):
            validation.validate_materials(materials, expected_questions=5)

    def test_controls_distinguish_correct_missing_wrong_stale_and_wrong_answer(self) -> None:
        result = validation.score_controls(self._materials(5))
        self.assertTrue(result["passed"])
        self.assertEqual(
            [True, False, False, False, False],
            [item["actual"] for item in result["outcomes"]],
        )

    def test_input_builder_seals_actual_material_executor_and_observer(self) -> None:
        with tempfile.TemporaryDirectory(dir=REPOSITORY) as temporary:
            root = Path(temporary)
            materials_path = root / "materials.json"
            self._write_json(materials_path, self._materials(1))
            config, _runtime = self._runtime_fixture(root)
            destination = root / "input.json"
            value = validation.build_input_manifest(HERE, materials_path, config, "development", destination)
            self.assertEqual(value, json.loads(destination.read_text(encoding="utf-8")))
            self.assertEqual(self._materials(1)["identity"], value["direct_dependencies"]["development-materials"])
            self.assertEqual(value["shared_conditions"]["executor"], value["direct_dependencies"]["executor"])

    def test_end_to_end_evidence_executes_observes_and_resumes_without_formal_state(self) -> None:
        with tempfile.TemporaryDirectory(dir=REPOSITORY) as temporary:
            root = Path(temporary)
            materials_path = root / "materials.json"
            self._write_json(materials_path, self._materials(1))
            config, runtime = self._runtime_fixture(root)
            input_path = root / "input.json"
            validation.build_input_manifest(HERE, materials_path, config, "development", input_path)
            subject_path = self._subject(root / "subject.json", runtime["binary"])
            output = root / "evidence"
            calls: list[bool] = []

            def runner(**kwargs: object) -> dict[str, object]:
                calls.append(bool(kwargs["resume"]))
                return self._report(1)

            first = validation.execute_prepared_evidence(
                HERE,
                output,
                config,
                subject_manifest=subject_path,
                evidence_type="development",
                input_manifest=input_path,
                runner=runner,
            )
            second = validation.execute_prepared_evidence(
                HERE,
                output,
                config,
                subject_manifest=subject_path,
                evidence_type="development",
                input_manifest=input_path,
                resume=True,
                runner=runner,
            )
            self.assertTrue(first["passed"])
            self.assertTrue(second["reused_execution"])
            self.assertEqual([False], calls)

    def test_execution_identity_change_invalidates_only_affected_subject_evidence(self) -> None:
        with tempfile.TemporaryDirectory(dir=REPOSITORY) as temporary:
            root = Path(temporary)
            materials_path = root / "materials.json"
            self._write_json(materials_path, self._materials(1))
            config, runtime = self._runtime_fixture(root)
            input_path = root / "input.json"
            validation.build_input_manifest(HERE, materials_path, config, "regression", input_path)
            first_subject = self._subject(root / "first.json", runtime["binary"], semantic="3" * 64)
            second_subject = self._subject(root / "second.json", runtime["binary"], semantic="4" * 64)
            first = validation.execute_prepared_evidence(
                HERE, root / "evidence", config, subject_manifest=first_subject,
                evidence_type="regression", input_manifest=input_path, runner=lambda **_kwargs: self._report(1),
            )
            second = validation.execute_prepared_evidence(
                HERE, root / "evidence", config, subject_manifest=second_subject,
                evidence_type="regression", input_manifest=input_path, runner=lambda **_kwargs: self._report(1),
            )
            self.assertNotEqual(first["subject_identity"], second["subject_identity"])
            self.assertNotEqual(first["execution_result"], second["execution_result"])

    def test_pair_comparison_requires_candidate_then_v0_and_an_absolute_pass(self) -> None:
        with tempfile.TemporaryDirectory(dir=REPOSITORY) as temporary:
            root = Path(temporary)
            left = self._execution_result("1" * 64, 0.4, role="v2-candidate")
            right = self._execution_result("2" * 64, 0.8, role="evaluation-baseline")
            left_path = root / "left.json"
            right_path = root / "right.json"
            self._write_json(left_path, left)
            self._write_json(right_path, right)
            compared = validation.compare_execution_results(left_path, right_path)
            self.assertAlmostEqual(0.4, compared["deltas_right_minus_left"]["final_answer_accuracy"])
            changed = json.loads(json.dumps(right))
            changed["direct_dependencies"]["observer"] = "9" * 64
            changed["identity"] = iteration.canonical_sha256({key: item for key, item in changed.items() if key != "identity"})
            self._write_json(right_path, changed)
            with self.assertRaises(validation.KernelIterationValidationError):
                validation.compare_execution_results(left_path, right_path)

            shared_drift = json.loads(json.dumps(right))
            shared_drift["shared_conditions"]["environment"] = "8" * 64
            shared_drift["input_identity"] = iteration.canonical_sha256({
                "shared_conditions": shared_drift["shared_conditions"],
                "direct_dependencies": shared_drift["direct_dependencies"],
            })
            shared_drift["identity"] = iteration.canonical_sha256({key: item for key, item in shared_drift.items() if key != "identity"})
            self._write_json(right_path, shared_drift)
            with self.assertRaisesRegex(validation.KernelIterationValidationError, "完整输入身份不同|共享评测条件不同"):
                validation.compare_execution_results(left_path, right_path)

            self._write_json(left_path, right)
            self._write_json(right_path, left)
            with self.assertRaisesRegex(validation.KernelIterationValidationError, "左侧必须是 V2 候选"):
                validation.compare_execution_results(left_path, right_path)

            failed = self._execution_result("1" * 64, 0.4, role="v2-candidate", passed=False)
            self._write_json(left_path, failed)
            self._write_json(right_path, right)
            with self.assertRaisesRegex(validation.KernelIterationValidationError, "未通过冻结绝对门"):
                validation.compare_execution_results(left_path, right_path)

    def test_fact_delivery_uses_first_gap_without_misclassifying_reader_error(self) -> None:
        materials = self._materials(1)
        for gap in ("target_evidence_not_search_returned", "target_evidence_not_read"):
            report = self._report(1)
            report["diagnostic_summary"] = {"by_first_observed_gap": {gap: 1}}
            observation = validation.observe_report(report, materials)
            self.assertFalse(observation["fact_delivery"]["complete"])
            self.assertEqual(1, observation["fact_delivery"]["missing_questions"])

        report = self._report(1)
        report["diagnostic_summary"] = {"by_first_observed_gap": {"evidence_read_answer_incorrect": 1}}
        observation = validation.observe_report(report, materials)
        self.assertTrue(observation["fact_delivery"]["complete"])
        self.assertEqual(0, observation["fact_delivery"]["missing_questions"])

    def test_stage3_contract_is_aggregate_only_disjoint_and_pre_result_frozen(self) -> None:
        stage3 = validation.load_stage3_contract(HERE)
        self.assertTrue(stage3["frozen_before_diagnostic_results"])
        self.assertFalse(stage3["contains_formal_questions_answers_gold_content_outputs_or_case_ids"])
        self.assertEqual(4, len(stage3["loaded"]["development"]["cases"]))
        self.assertEqual(8, len(stage3["loaded"]["regression"]["cases"]))
        serialized = json.dumps(stage3["loaded"]["problem_pool"], ensure_ascii=False)
        self.assertNotIn("question_id", serialized)
        self.assertNotIn("answer_session_ids", serialized)

    def test_current_product_diagnostic_is_explicit_non_candidate_and_precedes_v0(self) -> None:
        with tempfile.TemporaryDirectory(dir=REPOSITORY) as temporary:
            root = Path(temporary)
            materials_path = root / "materials.json"
            self._write_json(materials_path, self._materials(1))
            config, _runtime = self._runtime_fixture(root)
            input_path = root / "input.json"
            validation.build_input_manifest(HERE, materials_path, config, "development", input_path)
            with mock.patch.object(validation, "_verify_subject_binary", return_value=None), mock.patch.object(validation, "_verify_current_product_binary", return_value=None):
                with self.assertRaisesRegex(validation.KernelIterationValidationError, "只能用于明确的非候选诊断"):
                    validation.execute_prepared_evidence(
                        HERE, root / "rejected", config, selector="current-product",
                        evidence_type="development", input_manifest=input_path,
                        runner=lambda **_kwargs: self._report(1),
                    )
                current = validation.execute_prepared_evidence(
                    HERE, root / "evidence", config, selector="current-product",
                    evidence_type="development", input_manifest=input_path,
                    noncandidate_diagnostic=True, runner=lambda **_kwargs: self._report(1),
                )
                baseline = validation.execute_prepared_evidence(
                    HERE, root / "evidence", config, selector="v0",
                    evidence_type="development", input_manifest=input_path,
                    candidate_result_path=Path(current["execution_result"]),
                    noncandidate_diagnostic=True, runner=lambda **_kwargs: self._report(1),
                )
            current_value = json.loads(Path(current["execution_result"]).read_text(encoding="utf-8"))
            baseline_value = json.loads(Path(baseline["execution_result"]).read_text(encoding="utf-8"))
            self.assertIsNone(current_value["candidate_decision"])
            self.assertIsNone(baseline_value["candidate_decision"])
            self.assertEqual("stage3-current-product-diagnostic", current_value["comparison_purpose"])
            compared = validation.compare_execution_results(Path(current["execution_result"]), Path(baseline["execution_result"]))
            self.assertIsNone(compared["candidate_decision"])

    def test_stage3_case_observer_proves_fragment_gap_after_source_read(self) -> None:
        with tempfile.TemporaryDirectory(dir=REPOSITORY) as temporary:
            root = Path(temporary) / "run"
            materials = self._materials(1)
            materials["schema"] = validation.STAGE3_MATERIALS_SCHEMA
            materials["identity"] = iteration.canonical_sha256({key: item for key, item in materials.items() if key != "identity"})
            case = materials["cases"][0]
            question = root / "questions" / case["case_id"]
            retrieval = {
                "schema": "ownward.longmemeval-s-retrieval/v1", "question_identity": "1" * 64,
                "evidence": [{"id": "asset-1", "content": "A different fragment."}],
                "retrieval": {"context_chars": 21},
            }
            self._write_json(question / "retrieval.json", retrieval)
            diagnostic = {
                "correct": False,
                "first_observed_gap": "evidence_read_answer_incorrect",
                "evidence_coverage": {
                    "expected_session_ids": ["c01-s01"], "expected_asset_ids": ["asset-1"],
                    "search_returned_expected": ["asset-1"], "read_expected": ["asset-1"],
                },
                "execution_observations": {"source_creation_complete": True, "semantic_submission_complete": True},
                "artifacts": {"retrieval": {"sha256": iteration.file_sha256(question / "retrieval.json")}},
            }
            self._write_json(question / "diagnostic.json", diagnostic)
            observed = validation.observe_case_evidence(root, materials)
            self.assertEqual("fragment-incomplete-after-read", observed[0]["first_proven_mechanism"])
            self.assertEqual(1, observed[0]["read_sources"])
            self.assertEqual(0, observed[0]["delivered_truth_claims"])

    def test_stage3_finalization_requires_resume_receipts_and_never_forms_candidate_decision(self) -> None:
        with tempfile.TemporaryDirectory(dir=REPOSITORY) as temporary:
            output = Path(temporary)
            state = output / "state.json"
            state.write_bytes(b"formal-state-read-only")
            stage3 = validation.load_stage3_contract(HERE)
            comparison = iteration.load_contract(HERE)
            current_identity = iteration.select_subject(comparison, "current-product")["identity"]
            v0_identity = iteration.select_subject(comparison, "v0")["identity"]
            inputs = {"development": "1" * 64, "regression": "2" * 64}
            plan_content = {
                "schema": validation.STAGE3_PLAN_SCHEMA,
                "contract_identity": stage3["identity"], "comparison_contract_identity": comparison["identity"],
                "subjects": {"current-product": current_identity, "v0": v0_identity}, "inputs": inputs,
                "formal_state_sha256": iteration.file_sha256(state), "formal": False,
                "candidate_decision": None, "v2_candidate_exists": False,
            }
            plan_identity = iteration.canonical_sha256(plan_content)
            self._write_json(output / "stage3" / plan_identity / "plan.json", {**plan_content, "identity": plan_identity})
            paths: dict[str, Path] = {}
            for evidence_type in ("development", "regression"):
                for prefix, role, identity, data_bytes in (
                    ("current", "current-product-not-baseline", current_identity, 100),
                    ("v0", "evaluation-baseline", v0_identity, 1000),
                ):
                    name = f"{prefix}-{evidence_type}"
                    value = self._execution_result(identity, 1.0, role=role, input_identity=inputs[evidence_type])
                    value["comparison_purpose"] = "stage3-current-product-diagnostic"
                    value["candidate_decision"] = None
                    value["evidence_type"] = evidence_type
                    value["observation"].update({
                        "fact_delivery": {"complete": True, "missing_questions": 0},
                        "resources": {"ownward_data_bytes": data_bytes},
                        "case_evidence": [{
                            "coverage": "long-session-multi-fact", "expected_sources": 1,
                            "search_returned_sources": 1, "truth_claims": 2,
                            "delivered_truth_claims": 1 if prefix == "current" else 2,
                            "first_proven_mechanism": "fragment-incomplete-after-read" if prefix == "current" and evidence_type == "development" else "none",
                        }],
                    })
                    value["identity"] = iteration.canonical_sha256({key: item for key, item in value.items() if key != "identity"})
                    path = output / "results" / name / "execution-result.json"
                    self._write_json(path, value)
                    receipt_content = {
                        "schema": "ownward.kernel-iteration-execution-resume/v1", "plan_identity": value["plan_identity"],
                        "execution_result_sha256": iteration.file_sha256(path), "reused_execution": True,
                        "model_or_product_execution": False,
                    }
                    self._write_json(path.parent / "execution-resume.json", {**receipt_content, "identity": iteration.canonical_sha256(receipt_content)})
                    paths[name] = path
            result = validation.finalize_stage3(HERE, output, plan_identity, paths, state)
            self.assertTrue(result["passed"])
            self.assertIsNone(result["candidate_decision"])
            self.assertFalse(result["v2_candidate_exists"])
            (paths["current-development"].parent / "execution-resume.json").unlink()
            (output / "stage3" / plan_identity / "result.json").unlink()
            with self.assertRaisesRegex(validation.KernelIterationValidationError, "恢复收据"):
                validation.finalize_stage3(HERE, output, plan_identity, paths, state)

    def test_v0_execution_is_not_consumed_before_candidate_absolute_pass(self) -> None:
        with tempfile.TemporaryDirectory(dir=REPOSITORY) as temporary:
            root = Path(temporary)
            materials_path = root / "materials.json"
            self._write_json(materials_path, self._materials(1))
            config, runtime = self._runtime_fixture(root)
            input_path = root / "input.json"
            manifest = validation.build_input_manifest(HERE, materials_path, config, "development", input_path)
            failed_path = root / "failed-candidate.json"
            failed = self._execution_result(
                "1" * 64, 0.0, role="v2-candidate", passed=False,
                dependencies=manifest["direct_dependencies"],
                shared_conditions=manifest["shared_conditions"],
                input_identity=manifest["identity"],
            )
            self._write_json(failed_path, failed)
            with mock.patch.object(validation, "_verify_subject_binary", return_value=None):
                with self.assertRaisesRegex(validation.KernelIterationValidationError, "未通过冻结绝对门"):
                    validation.execute_prepared_evidence(
                        HERE, root / "v0-evidence", config, selector="v0",
                        evidence_type="development", input_manifest=input_path,
                        candidate_result_path=failed_path,
                        runner=lambda **_kwargs: self.fail("V0 must not run"),
                    )

    def test_valid_pair_executes_candidate_then_same_material_v0(self) -> None:
        with tempfile.TemporaryDirectory(dir=REPOSITORY) as temporary:
            root = Path(temporary)
            materials_path = root / "materials.json"
            self._write_json(materials_path, self._materials(1))
            config, runtime = self._runtime_fixture(root)
            input_path = root / "input.json"
            validation.build_input_manifest(HERE, materials_path, config, "development", input_path)
            subject_path = self._subject(root / "subject.json", runtime["binary"])
            calls: list[str] = []

            def candidate_runner(**_kwargs: object) -> dict[str, object]:
                calls.append("candidate")
                return self._report(1)

            candidate = validation.execute_prepared_evidence(
                HERE, root / "evidence", config, subject_manifest=subject_path,
                evidence_type="development", input_manifest=input_path, runner=candidate_runner,
            )

            drifted_candidate = json.loads(Path(candidate["execution_result"]).read_text(encoding="utf-8"))
            drifted_candidate["shared_conditions"]["environment"] = "f" * 64
            drifted_candidate["identity"] = iteration.canonical_sha256({key: item for key, item in drifted_candidate.items() if key != "identity"})
            drifted_path = root / "drifted-candidate.json"
            self._write_json(drifted_path, drifted_candidate)
            with mock.patch.object(validation, "_verify_subject_binary", return_value=None):
                with self.assertRaisesRegex(validation.KernelIterationValidationError, "共享评测条件不同"):
                    validation.execute_prepared_evidence(
                        HERE, root / "v0-drift", config, selector="v0",
                        evidence_type="development", input_manifest=input_path,
                        candidate_result_path=drifted_path,
                        runner=lambda **_kwargs: self.fail("shared-condition drift must reject before V0"),
                    )

            def baseline_runner(**_kwargs: object) -> dict[str, object]:
                calls.append("v0")
                return self._report(1)

            with mock.patch.object(validation, "_verify_subject_binary", return_value=None):
                baseline = validation.execute_prepared_evidence(
                    HERE, root / "evidence", config, selector="v0",
                    evidence_type="development", input_manifest=input_path,
                    candidate_result_path=Path(candidate["execution_result"]), runner=baseline_runner,
                )
            self.assertTrue(candidate["passed"])
            self.assertTrue(baseline["passed"])
            self.assertEqual(["candidate", "v0"], calls)
            paired = validation.compare_execution_results(Path(candidate["execution_result"]), Path(baseline["execution_result"]))
            self.assertEqual(candidate["subject_identity"], paired["left_subject"])

    def test_blind_calibration_admits_executes_twice_proves_resume_and_destroys_raw(self) -> None:
        with tempfile.TemporaryDirectory(dir=REPOSITORY) as temporary:
            root = Path(temporary)
            config, runtime = self._runtime_fixture(root)
            state = root / "state.json"
            state.write_bytes(b"formal-state-must-remain-byte-identical")
            output = root / "evidence"
            calls: list[tuple[str, bool]] = []

            def invoker(**kwargs: object) -> tuple[dict[str, object], dict[str, object]]:
                role = str(kwargs["role"])
                if role == "generator":
                    return self._generated_case(kwargs), self._usage(2.2)
                checks = self.validation["blind"]["quality_admission"]["required_checks"]
                return {
                    "assessments": [
                        {"case_id": case["case_id"], "checks": {name: True for name in checks}}
                        for case in self._materials(5)["cases"]
                    ]
                }, self._usage(7.0)

            def runner(**kwargs: object) -> dict[str, object]:
                run_dir = Path(kwargs["output_dir"])
                resume = bool(kwargs["resume"])
                calls.append((run_dir.name, resume))
                run_dir.mkdir(parents=True, exist_ok=True)
                if not resume:
                    self._write_json(run_dir / "report.json", self._report(5, wall=50.0 + len(calls)))
                    self._write_json(run_dir / "checkpoint-manifest.json", {"identity": "checkpoint"})
                    self._write_json(run_dir / "diagnostic-summary.json", {"by_first_observed_gap": {"none": 5}})
                return json.loads((run_dir / "report.json").read_text(encoding="utf-8"))

            patches = self._blind_patches(runtime)
            with patches[0], patches[1], patches[2]:
                result = validation.calibrate_blind(
                    HERE,
                    output,
                    config,
                    state,
                    seed="calibration-seed-0001",
                    invoker=invoker,
                    runner=runner,
                )
            self.assertTrue(result["passed"])
            self.assertEqual([("execution-1", False), ("execution-2", False), ("execution-1", True)], calls)
            terminal = json.loads(Path(result["result"]).read_text(encoding="utf-8"))
            plan = json.loads((Path(result["result"]).parent / "plan.json").read_text(encoding="utf-8"))
            self.assertEqual(validation._blind_implementation_identities()["controller"], plan["direct_dependencies"]["controller"])
            self.assertTrue(terminal["raw_materials_destroyed"])
            self.assertFalse(terminal["contains_reversible_question_answer_or_evidence"])
            self.assertNotIn("sapphire", Path(result["result"]).read_text(encoding="utf-8"))
            self.assertEqual(state.read_bytes(), b"formal-state-must-remain-byte-identical")
            scratch = runtime["runs"] / "kernel-v2-blind-calibration" / result["plan_identity"]
            self.assertFalse(scratch.exists())
            self.assertLessEqual(result["budgets"]["total_normal_seconds"], 9000)

            with patches[0], patches[1], patches[2]:
                resumed = validation.resume_blind_by_plan_identity(
                    HERE, output, result["plan_identity"],
                    invoker=lambda **_kwargs: self.fail("terminal reuse must not invoke Codex"),
                    runner=lambda **_kwargs: self.fail("terminal reuse must not execute product"),
                )
            self.assertTrue(resumed["reused"])
            self.assertEqual(0, resumed["model_calls"])
            self.assertEqual(0, resumed["product_executions"])
            self.assertEqual(3, len(calls))

            plan = json.loads((Path(result["result"]).parent / "plan.json").read_text(encoding="utf-8"))
            dependencies = dict(plan["direct_dependencies"])
            verifier_identity = validation._blind_current_verifier_identity()
            with mock.patch.object(validation, "_current_blind_dependencies", return_value=dependencies), mock.patch.object(validation, "_blind_current_verifier_identity", return_value=verifier_identity):
                locator = validation.bind_blind_dependency_locator(HERE, output, result["plan_identity"], config, state)
                current = validation.resume_current_blind_by_plan_identity(
                    HERE, output, result["plan_identity"],
                    invoker=lambda **_kwargs: self.fail("current terminal reuse must not invoke Codex"),
                    runner=lambda **_kwargs: self.fail("current terminal reuse must not execute product"),
                )
            self.assertTrue(current["current_dependencies_valid"])
            self.assertEqual(0, current["model_calls"])
            self.assertEqual(0, current["product_executions"])
            self.assertTrue(iteration.is_sha256(locator["dependency_locator_identity"]))

            drifted_dependencies = dict(dependencies)
            drifted_dependencies["binary"] = "9" * 64
            with mock.patch.object(validation, "_current_blind_dependencies", return_value=drifted_dependencies), mock.patch.object(validation, "_blind_current_verifier_identity", return_value=verifier_identity):
                with self.assertRaisesRegex(validation.KernelIterationValidationError, "当前直接依赖已漂移"):
                    validation.resume_current_blind_by_plan_identity(HERE, output, result["plan_identity"])

    def test_quality_rejection_is_not_candidate_failure_and_destroys_gate(self) -> None:
        with tempfile.TemporaryDirectory(dir=REPOSITORY) as temporary:
            root = Path(temporary)
            config, runtime = self._runtime_fixture(root)
            state = root / "state.json"
            state.write_bytes(b"formal")

            def invoker(**kwargs: object) -> tuple[dict[str, object], dict[str, object]]:
                if kwargs["role"] == "generator":
                    return self._generated_case(kwargs), self._usage(1)
                checks = self.validation["blind"]["quality_admission"]["required_checks"]
                assessments = []
                for index, case in enumerate(self._materials(5)["cases"]):
                    values = {name: True for name in checks}
                    if index == 0:
                        values["unique_answer"] = False
                    assessments.append({"case_id": case["case_id"], "checks": values})
                return {"assessments": assessments}, self._usage(1)

            patches = self._blind_patches(runtime)
            with patches[0], patches[1], patches[2]:
                result = validation.calibrate_blind(
                    HERE, root / "evidence", config, state, seed="quality-reject-seed",
                    invoker=invoker, runner=lambda **_kwargs: self.fail("rejected data must not execute"),
                )
            terminal = json.loads(Path(result["result"]).read_text(encoding="utf-8"))
            self.assertEqual("quality-rejected", terminal["status"])
            self.assertIsNone(terminal["candidate_decision"])
            self.assertTrue(terminal["raw_materials_destroyed"])

    def test_terminal_failure_is_audited_without_reversible_content_and_destroys_gate(self) -> None:
        with tempfile.TemporaryDirectory(dir=REPOSITORY) as temporary:
            root = Path(temporary)
            config, runtime = self._runtime_fixture(root)
            state = root / "state.json"
            state.write_bytes(b"formal")
            patches = self._blind_patches(runtime)
            with patches[0], patches[1], patches[2], self.assertRaises(validation.KernelIterationValidationError):
                validation.calibrate_blind(
                    HERE, root / "evidence", config, state, seed="terminal-failure-seed",
                    invoker=lambda **_kwargs: ({"cases": []}, self._usage(1)),
                    runner=lambda **_kwargs: self.fail("invalid generated data must not execute"),
                )
            plans = list((root / "evidence" / "blind-calibration").glob("*/plan.json"))
            self.assertEqual(1, len(plans))
            result_path = plans[0].with_name("result.json")
            terminal = json.loads(result_path.read_text(encoding="utf-8"))
            self.assertEqual("failed", terminal["status"])
            self.assertEqual({"stage": "generator", "error_type": "KernelIterationValidationError"}, terminal["failure"])
            self.assertEqual({}, terminal["coverage_counts"])
            self.assertTrue(terminal["raw_materials_destroyed"])
            self.assertFalse((runtime["runs"] / "kernel-v2-blind-calibration" / plans[0].parent.name).exists())
            self.assertEqual(b"formal", state.read_bytes())

    def test_interruption_retains_exact_gate_for_resume_but_dependency_drift_cannot_reuse(self) -> None:
        with tempfile.TemporaryDirectory(dir=REPOSITORY) as temporary:
            root = Path(temporary)
            config, runtime = self._runtime_fixture(root)
            state = root / "state.json"
            state.write_bytes(b"formal")

            def invoker(**kwargs: object) -> tuple[dict[str, object], dict[str, object]]:
                if kwargs["role"] == "generator":
                    return self._generated_case(kwargs), self._usage(1)
                checks = self.validation["blind"]["quality_admission"]["required_checks"]
                return {"assessments": [{"case_id": case["case_id"], "checks": {name: True for name in checks}} for case in self._materials(5)["cases"]]}, self._usage(1)

            patches = self._blind_patches(runtime)
            with patches[0], patches[1], patches[2], self.assertRaises(InterruptedError):
                validation.calibrate_blind(
                    HERE, root / "evidence", config, state, seed="interrupt-seed-0001",
                    invoker=invoker, runner=lambda **_kwargs: (_ for _ in ()).throw(InterruptedError()),
                )
            plans = list((root / "evidence" / "blind-calibration").glob("*/plan.json"))
            self.assertEqual(1, len(plans))
            first_identity = plans[0].parent.name
            scratch = runtime["runs"] / "kernel-v2-blind-calibration" / first_identity
            self.assertTrue(scratch.is_dir())
            self.assertTrue((scratch / "recovery-secret.json").is_file())
            self.assertTrue((plans[0].parent / "active.json").is_file())

            drifted = dict(runtime)
            drifted["binary"] = root / "other.exe"
            drifted["binary"].write_bytes(b"different")
            drift_patches = self._blind_patches(drifted)
            with drift_patches[0], drift_patches[1], drift_patches[2]:
                with self.assertRaises(validation.KernelIterationValidationError):
                    validation.resume_blind_by_plan_identity(
                        HERE, root / "evidence", first_identity,
                        invoker=invoker, runner=lambda **_kwargs: self._report(5),
                    )

            def completing_runner(**kwargs: object) -> dict[str, object]:
                run_dir = Path(kwargs["output_dir"])
                run_dir.mkdir(parents=True, exist_ok=True)
                if not bool(kwargs["resume"]):
                    self._write_json(run_dir / "report.json", self._report(5))
                    self._write_json(run_dir / "checkpoint-manifest.json", {"identity": "checkpoint"})
                    self._write_json(run_dir / "diagnostic-summary.json", {"by_first_observed_gap": {"none": 5}})
                return json.loads((run_dir / "report.json").read_text(encoding="utf-8"))

            with patches[0], patches[1], patches[2]:
                completed = validation.resume_blind_by_plan_identity(
                    HERE, root / "evidence", first_identity,
                    invoker=invoker, runner=completing_runner,
                )
            self.assertTrue(completed["passed"])
            self.assertFalse(scratch.exists())
            self.assertFalse((plans[0].parent / "active.json").exists())

    def test_blind_terminal_contains_no_reversible_field_names(self) -> None:
        forbidden = set(validation.FORMAL_KEYS)
        terminal = validation._blind_terminal(
            "1" * 64,
            self.validation,
            status="quality-rejected",
            passed=False,
            generator_usage=self._usage(1),
            admission_usage=self._usage(1),
            admission={"passed": False, "questions": 5, "passed_counts": {}, "rejected_count": 5},
            controls={"passed": True, "outcomes": []},
            executions=[],
            resume_proof=None,
            total_wall_seconds=2,
        )
        self.assertFalse(forbidden & set(json.dumps(terminal).split('"')))

    def _blind_patches(self, runtime: dict[str, object]) -> tuple[mock._patch, mock._patch, mock._patch]:
        calibration = {
            "runtime_calibration_identity": "a" * 64,
            "state_sha256_before": "b" * 64,
            "state_sha256_after": "b" * 64,
        }
        return (
            mock.patch.object(validation, "validate_execution_config", return_value=runtime),
            mock.patch.object(iteration, "calibrate_runtime", return_value=calibration),
            mock.patch.object(validation, "_verify_runtime_binary_binding", return_value=None),
        )

    def _runtime_fixture(self, root: Path) -> tuple[Path, dict[str, object]]:
        binary = root / "ownward.exe"
        binary.write_bytes(b"fixture-binary")
        embedding = root / "embedding"
        embedding.mkdir()
        self._write_json(embedding / "manifest.json", {"fixture": True})
        codex = root / "codex.exe"
        codex.write_bytes(b"fixture-codex")
        auth = root / "auth.json"
        auth.write_bytes(b"not-a-real-secret")
        source = root / "official-source"
        evaluator = source / "src" / "evaluation" / "evaluate_qa.py"
        evaluator.parent.mkdir(parents=True)
        evaluator.write_text("# fixture\n", encoding="utf-8")
        python_root = root / "python"
        (python_root / "Scripts").mkdir(parents=True)
        (python_root / "Scripts" / "python.exe").write_bytes(b"fixture")
        runs = root / "runs"
        runs.mkdir()
        environment_manifest = root / "environment.json"
        environment = {"schema": "ownward.longmemeval-s-environment/v1", "layout": {"source": str(source), "python": str(python_root), "runs": str(runs)}}
        self._write_json(environment_manifest, environment)
        protocol_path = root / "protocol.json"
        protocol = json.loads((REPOSITORY / "benchmarks" / "longmemeval_s" / "protocol.json").read_text(encoding="utf-8"))
        self._write_json(protocol_path, protocol)
        config = root / "execution.json"
        value = {
            "schema": "ownward.acceptance-execution/v3",
            "candidate": {"binary": str(binary), "embedding_bundle_dir": str(embedding)},
            "community": {
                "environment_manifest": str(environment_manifest), "protocol": str(protocol_path),
                "codex_binary": str(codex), "codex_auth_file": str(auth),
            },
        }
        self._write_json(config, value)
        runtime: dict[str, object] = {
            "path": config, "value": value, "binary": binary, "embedding": embedding,
            "environment_manifest": environment_manifest, "environment": environment,
            "protocol": protocol_path, "protocol_value": protocol, "codex_binary": codex,
            "codex_auth_file": auth, "runs": runs,
        }
        return config, runtime

    def _subject(self, path: Path, binary: Path, *, semantic: str = "3" * 64) -> Path:
        content = {
            "schema": iteration.SUBJECT_SCHEMA,
            "role": "v2-candidate",
            "kernel_generation_identity": "1" * 64,
            "kernel_effect_identity": "2" * 64,
            "direct_dependencies": {
                "authority-substrate": "4" * 64, "product-rules": "5" * 64,
                "semantic": semantic, "vector": "6" * 64,
            },
            "artifacts": {"binary": validation.evidence.file_sha256(binary)},
        }
        value = {**content, "name": "synthetic-v2", "identity": iteration.canonical_sha256(content), "audit": {}}
        self._write_json(path, value)
        return path

    def _materials(self, count: int) -> dict[str, object]:
        cases = []
        for index in range(count):
            coverage = validation.BLIND_COVERAGE[index % len(validation.BLIND_COVERAGE)]
            case_id = f"c{index + 1:02d}"
            question_type = {
                "knowledge-update-conflict": "knowledge-update",
                "temporal-order": "temporal-reasoning",
                "single-session-assistant-fact": "single-session-assistant",
            }.get(coverage, "multi-session")
            answer_id = f"{case_id}-s02"
            answer_ids = [answer_id]
            if coverage in {"temporal-order", "multi-session-relation", "multi-session-distractor"}:
                answer_ids = [f"{case_id}-s01", answer_id]
            stale = [f"{case_id}-s01"] if coverage == "knowledge-update-conflict" else []
            cases.append({
                "case_id": case_id,
                "coverage": coverage,
                "question_type": question_type,
                "question_date": "2032-05-04",
                "question": f"What durable result did person {index + 1} confirm after reviewing the records?",
                "answer": "sapphire",
                "answer_session_ids": answer_ids,
                "stale_session_ids": stale,
                "distractor_session_ids": [f"{case_id}-s03", f"{case_id}-s04"],
                "truth_claims": [{"claim": "sapphire", "evidence_session_ids": [answer_id]}],
                "sessions": [
                    {"session_id": f"{case_id}-s01", "date": "2032-05-01", "turns": [{"role": "user", "content": "The provisional label was amber."}]},
                    {"session_id": answer_id, "date": "2032-05-02", "turns": [{"role": "assistant", "content": "After the review, the durable result is sapphire."}]},
                    {"session_id": f"{case_id}-s03", "date": "2032-05-03", "turns": [{"role": "user", "content": "A separate project selected cobalt."}]},
                    {"session_id": f"{case_id}-s04", "date": "2032-05-03", "turns": [{"role": "assistant", "content": "A similarly named project selected topaz."}]},
                    {"session_id": f"{case_id}-s05", "date": "2032-05-04", "turns": [{"role": "user", "content": "The review meeting remained scheduled."}]},
                ],
            })
        content = {
            "schema": validation.MATERIALS_SCHEMA,
            "contains_formal_questions_answers_gold_or_content": False,
            "cases": cases,
            "criteria": {"minimum_accuracy": 0.0, "require_complete_fact_delivery": True, "category_minimums": {}},
        }
        return {**content, "identity": iteration.canonical_sha256(content)}

    def _generated_case(self, kwargs: dict[str, object]) -> dict[str, object]:
        case_id = Path(kwargs["stage"]).name
        index = int(case_id[1:]) - 1
        return {"case": self._materials(5)["cases"][index]}

    @staticmethod
    def _usage(wall: float) -> dict[str, object]:
        return {
            "input_tokens": 10, "cached_input_tokens": 0, "output_tokens": 5,
            "reasoning_output_tokens": 1, "calls": 1, "attempts": 1, "retries": 0,
            "rate_limit_events": 0, "interrupted_attempts": 0, "wall_seconds": wall,
        }

    @staticmethod
    def _report(questions: int, *, wall: float = 20.0) -> dict[str, object]:
        return {
            "questions": questions,
            "correct": questions,
            "accuracy": 1.0,
            "categories": {
                "knowledge-update": {"questions": 1, "correct": 1, "accuracy": 1.0},
                "temporal-reasoning": {"questions": 1, "correct": 1, "accuracy": 1.0},
            },
            "retrieval": {"mean_ms": 2.0, "p95_ms": 3.0},
            "cost": {
                "wall_seconds": wall, "semantic_input_tokens": 10, "reader_input_tokens": 5,
                "judge_input_tokens": 3, "ownward_data_bytes": 100,
                "codex": {"calls": questions * 3, "attempts": questions * 3, "retries": 0, "rate_limit_events": 0, "interrupted_attempts": 0, "wall_seconds": wall},
            },
            "diagnostics": {"questions": questions, "post_answer_only": True, "excluded_from_product_execution_and_scoring": True},
            "diagnostic_summary": {"by_first_observed_gap": {"none": questions}},
            "execution": {"complete": True, "protocol_valid": True, "evidence_complete": True},
            "formal": False,
        }

    @staticmethod
    def _execution_result(
        subject: str,
        accuracy: float,
        *,
        role: str,
        passed: bool = True,
        dependencies: dict[str, str] | None = None,
        shared_conditions: dict[str, str] | None = None,
        input_identity: str | None = None,
    ) -> dict[str, object]:
        direct = dependencies or {"development-materials": "3" * 64, "executor": "4" * 64, "observer": "5" * 64}
        shared = shared_conditions or {
            "dataset": "3" * 64,
            "environment": "6" * 64,
            "executor": "4" * 64,
            "model-profile": "7" * 64,
            "observer": "5" * 64,
            "prompt-and-schema": "8" * 64,
            "scorer": "9" * 64,
        }
        content = {
            "schema": validation.EXECUTION_RESULT_SCHEMA,
            "plan_identity": "0" * 64,
            "subject_identity": subject,
            "subject_role": role,
            "subject_name": "synthetic-v2" if role == "v2-candidate" else "v0",
            "comparison_purpose": "candidate-evaluation",
            "evidence_type": "development",
            "status": "passed" if passed else "failed",
            "passed": passed,
            "candidate_decision": passed if role == "v2-candidate" else None,
            "formal": False,
            "formal_state_written": False,
            "observation": {
                "final_answer_accuracy": accuracy,
                "latency": {"retrieval_mean_ms": 2.0 + accuracy, "wall_seconds": 10.0 + accuracy},
            },
            "failure_feedback": [],
            "input_identity": input_identity or iteration.canonical_sha256({"shared_conditions": shared, "direct_dependencies": direct}),
            "shared_conditions": shared,
            "direct_dependencies": direct,
        }
        return {**content, "identity": iteration.canonical_sha256(content)}

    @staticmethod
    def _write_json(path: Path, value: object) -> None:
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(value, ensure_ascii=False, sort_keys=True, indent=2) + "\n", encoding="utf-8")


if __name__ == "__main__":
    unittest.main()
