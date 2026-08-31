from __future__ import annotations

import json
from pathlib import Path
import tempfile
import threading
import time
import unittest
from unittest import mock

import kernel_iteration_blind_gate as gate


class BlindGateTests(unittest.TestCase):
    def setUp(self) -> None:
        self.suite_root = Path(__file__).resolve().parent

    def test_contract_is_frozen_before_generation(self) -> None:
        for level in gate.GATE_LEVELS:
            with self.subTest(level=level):
                value = gate.load_contract(self.suite_root, level)
                self.assertTrue(value["frozen_before_generation"])
                self.assertEqual(value["execution"]["order"], ["v2-candidate", "v0-baseline"])
                self.assertEqual(value["execution"]["generation_max_active"], 8 if level == 50 else 4)
                self.assertEqual(value["execution"]["generation_worker_active_turns_maximum"], 1)
                if level == 50:
                    self.assertEqual(value["execution"]["rejection_replacement"], "rejected-cases-only")
                    self.assertTrue(value["execution"]["full_set_readmission_after_replacement"])
                self.assertEqual(value["absolute_gate"]["questions"], level)
                self.assertEqual(value["absolute_gate"]["final_answer_accuracy_minimum"], 1.0)
                self.assertEqual(value["absolute_gate"]["level_total_wall_seconds_maximum"], gate.LEVEL_BUDGETS[level])
                self.assertEqual(len(gate._coverage_schedule(value)), level)

    def test_candidate_absolute_failure_never_runs_v0_and_destroys_raw_data(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            fixture = self._fixture(Path(directory), candidate_pass=False)
            result = fixture.run()
            self.assertEqual(result["status"], "candidate-rejected")
            self.assertFalse(result["candidate_decision"])
            self.assertEqual(fixture.execution_order, ["candidate"])
            terminal = json.loads(Path(result["result"]).read_text(encoding="utf-8"))
            self.assertIsNone(terminal["executions"]["v0"])
            self.assertTrue(terminal["raw_materials_destroyed"])
            self.assertFalse(any(fixture.scratch_root.iterdir()))
            self.assertEqual(fixture.state_path.read_bytes(), fixture.state_bytes)

    def test_post_candidate_evaluator_exception_is_fail_closed_and_durable(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            fixture = self._fixture(Path(directory), candidate_pass=False)
            with mock.patch.object(gate, "_attribute_first_answer_failure", side_effect=ModuleNotFoundError("fixture dependency")):
                reference = fixture.run()
            self.assertEqual(reference["status"], "evaluation-process-error")
            self.assertIsNone(reference["candidate_decision"])
            self.assertEqual(fixture.execution_order, ["candidate"])
            root = fixture.output_root / "blind-gate" / reference["plan_identity"]
            observation = json.loads((root / "candidate-observation.json").read_text(encoding="utf-8"))
            terminal = json.loads((root / "result.json").read_text(encoding="utf-8"))
            self.assertEqual(observation["execution"], terminal["executions"]["candidate"])
            self.assertEqual(observation["absolute_decision"], terminal["absolute_decision"])
            self.assertFalse(terminal["failure_transition"]["candidate_failed"])
            self.assertEqual(terminal["failure_transition"]["return_to_stage"], 2)
            self.assertTrue(terminal["evaluation_process_decision"]["fail_closed"])
            self.assertEqual(terminal["evaluation_process_decision"]["stage"], "answer-failure-attribution")
            failure = terminal["evaluation_process_decision"]["failures"][0]
            self.assertEqual(failure["category"], "evaluation-controller")
            self.assertEqual(failure["error_type"], "ModuleNotFoundError")
            self.assertEqual(len(failure["message_sha256"]), 64)
            self.assertNotIn("fixture dependency", json.dumps(terminal))
            self.assertTrue(terminal["raw_materials_destroyed"])
            self.assertFalse(any(fixture.scratch_root.iterdir()))
            with mock.patch.object(gate, "_current_dependencies", return_value=fixture.dependencies):
                reused = gate.resume_by_plan_identity(self.suite_root, fixture.output_root, reference["plan_identity"])
            self.assertTrue(reused["reused"])
            self.assertEqual(reused["model_calls"], 0)
            self.assertEqual(reused["product_executions"], 0)
            self.assertEqual(fixture.state_path.read_bytes(), fixture.state_bytes)

    def test_post_candidate_attribution_rejection_is_fail_closed_and_durable(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            fixture = self._fixture(Path(directory), candidate_pass=False)
            attribution = {
                "classification": "evaluation-process-failure",
                "reason": "oracle-context-failed-or-mechanically-unstable",
                "first_answer_failure_remains_failure": True,
            }
            with mock.patch.object(gate, "_attribute_first_answer_failure", return_value=attribution):
                reference = fixture.run()
            terminal = json.loads(Path(reference["result"]).read_text(encoding="utf-8"))
            self.assertEqual(reference["status"], "evaluation-process-error")
            self.assertIsNone(reference["candidate_decision"])
            self.assertTrue(terminal["evaluation_process_decision"]["fail_closed"])
            self.assertFalse(terminal["failure_transition"]["candidate_failed"])
            self.assertIsNotNone(terminal["executions"]["candidate"])
            self.assertIsNone(terminal["executions"]["v0"])
            with mock.patch.object(gate, "_current_dependencies", return_value=fixture.dependencies):
                reused = gate.resume_by_plan_identity(self.suite_root, fixture.output_root, reference["plan_identity"])
            self.assertTrue(reused["reused"])
            self.assertEqual(reused["model_calls"], 0)
            self.assertEqual(reused["product_executions"], 0)

    def test_answer_attribution_selects_only_first_actual_failure_in_material_order(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            cases = [self._minimal_case(name) for name in ("first", "second", "third")]
            run_root = root / "run"
            for case, correct in zip(cases, (True, False, False)):
                question_root = run_root / "questions" / case["case_id"]
                question_root.mkdir(parents=True)
                (question_root / "diagnostic.json").write_text(
                    json.dumps({"question_id": case["case_id"], "correct": correct}) + "\n",
                    encoding="utf-8",
                )
            (run_root / "diagnostic-summary.json").write_text(
                json.dumps({"questions": 3, "correct": 1}) + "\n", encoding="utf-8",
            )
            observation = {
                "questions": 3,
                "final_answer_accuracy": 1 / 3,
                "fact_delivery": {"complete": True, "missing_questions": 0},
            }
            selected, receipt = gate._first_failed_answer_material({"cases": cases}, run_root, observation)
            self.assertEqual([item["case_id"] for item in selected["cases"]], ["second"])
            self.assertEqual(receipt["selected_material_order"], 2)
            self.assertEqual(receipt["candidate_failed_questions"], 2)
            self.assertEqual(receipt["diagnosed_questions"], 1)
            self.assertFalse(receipt["selection_uses_expected_answer"])

    def test_answer_attribution_closes_fractional_accuracy_without_truncating_failure(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            cases = [self._minimal_case(f"case-{index:02d}") for index in range(15)]
            run_root = root / "run"
            for index, case in enumerate(cases):
                question_root = run_root / "questions" / case["case_id"]
                question_root.mkdir(parents=True)
                (question_root / "diagnostic.json").write_text(
                    json.dumps({"question_id": case["case_id"], "correct": index != 14}) + "\n",
                    encoding="utf-8",
                )
            (run_root / "diagnostic-summary.json").write_text(
                json.dumps({"questions": 15, "correct": 14}) + "\n", encoding="utf-8",
            )
            selected, receipt = gate._first_failed_answer_material(
                {"cases": cases},
                run_root,
                {"questions": 15, "final_answer_accuracy": 14 / 15},
            )
            self.assertEqual([item["case_id"] for item in selected["cases"]], ["case-14"])
            self.assertEqual(receipt["candidate_failed_questions"], 1)

    def test_answer_attribution_passes_only_selected_case_to_reader_and_judge_chain(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            cases = [self._minimal_case(name) for name in ("first", "second", "third")]
            run_root = root / "run"
            for case, correct in zip(cases, (True, False, True)):
                question_root = run_root / "questions" / case["case_id"]
                question_root.mkdir(parents=True)
                (question_root / "diagnostic.json").write_text(
                    json.dumps({"question_id": case["case_id"], "correct": correct}) + "\n",
                    encoding="utf-8",
                )
            (run_root / "diagnostic-summary.json").write_text(
                json.dumps({"questions": 3, "correct": 2}) + "\n", encoding="utf-8",
            )
            captured: list[list[str]] = []
            diagnostic = {
                "reader": {
                    "settings": {"model": "gpt-5.6-terra", "reasoning_effort": "high"},
                    "records": [
                        {"context": "product", "observations": 3, "correct": 0},
                        {"context": "oracle", "observations": 3, "correct": 3},
                    ],
                    "product_context_failures": 3,
                    "oracle_context_failures": 0,
                    "product_context_variations": 0,
                    "oracle_context_variations": 0,
                },
                "judge": {
                    "controls_passed": True,
                    "correct_controls": {"passed": 1, "total": 1},
                    "wrong_controls": {"passed": 1, "total": 1},
                },
                "cost": {"observed_wall_seconds": 1.0},
                "transport": {"calls": 13},
            }

            def diagnose(_suite: Path, _stage: Path, _runtime: dict[str, object], materials: dict[str, object], _run: Path, **kwargs: object) -> dict[str, object]:
                captured.append([str(item["case_id"]) for item in materials["cases"]])
                self.assertEqual(kwargs["reader_settings"]["model"], "gpt-5.6-terra")
                self.assertEqual(kwargs["reader_settings"]["reasoning_effort"], "high")
                return diagnostic

            execution = {"run_root": str(run_root), "observation": {
                "questions": 3, "final_answer_accuracy": 2 / 3,
                "fact_delivery": {"complete": True, "missing_questions": 0},
            }}
            runtime = {"protocol_value": {"reader": {"model": "reader"}}}
            with mock.patch.object(gate.answer_sufficiency, "_diagnose_codex_boundaries", side_effect=diagnose):
                result = gate._attribute_first_answer_failure(
                    self.suite_root, root, runtime, {"cases": cases}, execution,
                    {"failures": [{"metric": "final_answer_accuracy"}]},
                )
            self.assertEqual(captured, [["second"]])
            self.assertEqual(result["selection"]["diagnosed_questions"], 1)
            self.assertEqual(result["diagnostic_reader"]["model"], "gpt-5.6-terra")
            self.assertEqual(result["diagnostic_reader"]["reasoning_effort"], "high")

    def test_invalid_evaluator_qualification_fails_before_material_or_candidate(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            fixture = self._fixture(Path(directory), qualification_error=True)
            with self.assertRaisesRegex(RuntimeError, "qualification drift"):
                fixture.run()
            self.assertEqual(fixture.generator_calls, 0)
            self.assertEqual(fixture.admission_calls, 0)
            self.assertEqual(fixture.execution_order, [])
            self.assertEqual(fixture.state_path.read_bytes(), fixture.state_bytes)

    def test_admission_rejection_regenerates_then_runs_candidate_before_v0(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            fixture = self._fixture(Path(directory), first_admission_rejected=True)
            result = fixture.run()
            self.assertEqual(result["status"], "passed")
            self.assertTrue(result["candidate_decision"])
            self.assertEqual(fixture.execution_order, ["candidate", "baseline", "candidate", "baseline"])
            terminal = json.loads(Path(result["result"]).read_text(encoding="utf-8"))
            self.assertEqual(terminal["admitted_attempt"], 2)
            self.assertEqual(len(terminal["rejected_batches"]), 1)
            aggregate = terminal["rejected_batches"][0]["failure_aggregate"]
            self.assertEqual(set(aggregate), {
                "rejected_by_coverage", "failed_by_check",
                "failed_by_coverage_and_check", "failed_check_combinations",
            })
            self.assertNotIn("g01-c", json.dumps(aggregate, ensure_ascii=False))
            self.assertEqual(terminal["next_level"], 15)
            self.assertTrue(terminal["cost_and_recovery"]["admission_rejection_not_candidate_failure"])
            self.assertGreater(terminal["cost_and_recovery"]["admission_rejection_wall_seconds"], 0.0)
            self.assertFalse(self._contains_forbidden_raw_field(terminal))
            self.assertFalse(any(fixture.scratch_root.iterdir()))

    def test_three_admission_rejections_exhaust_gate_without_candidate_failure(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            fixture = self._fixture(Path(directory), all_admissions_rejected=True)
            result = fixture.run()
            self.assertEqual(result["status"], "quality-admission-exhausted")
            self.assertIsNone(result["candidate_decision"])
            self.assertEqual(fixture.execution_order, [])
            terminal = json.loads(Path(result["result"]).read_text(encoding="utf-8"))
            self.assertEqual([item["attempt"] for item in terminal["rejected_batches"]], [1, 2, 3])
            self.assertEqual(terminal["admitted_attempt"], 0)
            self.assertIsNone(terminal["executions"]["candidate"])
            self.assertIsNone(terminal["executions"]["v0"])
            self.assertFalse(terminal["failure_transition"]["candidate_failed"])
            self.assertEqual(
                terminal["failure_transition"]["next_action"],
                "generate-a-fresh-plan-after-quality-admission-exhaustion",
            )
            self.assertTrue(terminal["raw_materials_destroyed"])
            self.assertFalse(any(fixture.scratch_root.iterdir()))

    def test_rejected_admission_wall_is_excluded_from_candidate_decision_budget(self) -> None:
        with mock.patch.object(gate.time, "perf_counter", return_value=471.177):
            decision = gate._decision_wall_seconds(0.0, 148.95)
        self.assertAlmostEqual(decision, 322.227)
        self.assertLess(decision, gate.LEVEL_BUDGETS[5])

    def test_generation_lanes_are_bounded_concurrent_and_restore_original_order(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            fixture = self._fixture(Path(directory), level=15)
            active = 0
            maximum = 0
            lock = threading.Lock()

            def make_invoker() -> object:
                def invoke(**kwargs: object) -> tuple[dict[str, object], dict[str, object]]:
                    nonlocal active, maximum
                    with lock:
                        active += 1
                        maximum = max(maximum, active)
                    try:
                        time.sleep(0.01)
                        schema = kwargs["schema"]
                        case_id = schema["properties"]["case"]["properties"]["case_id"]["enum"][0]
                        coverage = schema["properties"]["case"]["properties"]["coverage"]["enum"][0]
                        return {"case": fixture._case(case_id, coverage)}, {"calls": 1, "attempts": 1, "wall_seconds": 0.01}
                    finally:
                        with lock:
                            active -= 1
                return invoke

            contract = gate.load_contract(self.suite_root, 15)
            generated, usages, scheduler = gate._generate_cases(
                self.suite_root,
                {},
                fixture._validation_contract(),
                contract,
                Path(directory) / "batch-01",
                "independent-profile-seed",
                [make_invoker() for _ in range(4)],
            )
            self.assertEqual([case["case_id"] for case in generated], [f"g01-c{index:02d}" for index in range(1, 16)])
            self.assertEqual(len(usages), 15)
            self.assertEqual(scheduler["max_active_limit"], 4)
            self.assertEqual(scheduler["max_active_observed"], 4)
            self.assertEqual(maximum, 4)
            self.assertEqual(scheduler["submitted"], 15)

    def test_fifty_question_gate_replaces_only_rejected_case_and_readmits_full_set(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            fixture = self._fixture(Path(directory), level=50, first_rejected_case_only=True)
            result = fixture.run()
            self.assertTrue(result["passed"])
            terminal = json.loads(Path(result["result"]).read_text(encoding="utf-8"))
            rounds = terminal["replacement_rounds"]
            self.assertEqual([item["generated_count"] for item in rounds], [50, 1])
            self.assertEqual([item["preserved_count"] for item in rounds], [0, 49])
            self.assertEqual([item["full_admission_questions"] for item in rounds], [50, 50])
            self.assertEqual(fixture.generated_case_ids.count("g01-c01"), 2)
            self.assertEqual(fixture.generated_case_ids.count("g01-c02"), 1)
            self.assertEqual(fixture.generator_calls, 51)
            self.assertEqual(fixture.admission_calls, 2)

    def test_level_wall_failure_is_evaluation_process_failure_not_candidate_failure(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            fixture = self._fixture(Path(directory))
            with mock.patch.object(gate, "_decision_wall_seconds", return_value=500.0):
                reference = fixture.run()
            terminal = json.loads(Path(reference["result"]).read_text(encoding="utf-8"))
            self.assertEqual(reference["status"], "evaluation-process-rejected")
            self.assertFalse(reference["passed"])
            self.assertTrue(reference["candidate_decision"])
            self.assertFalse(terminal["evaluation_process_decision"]["passed"])
            self.assertFalse(terminal["evaluation_process_decision"]["candidate_failure"])
            self.assertFalse(terminal["failure_transition"]["candidate_failed"])
            self.assertEqual(terminal["general_root_cause"]["responsible_direction"], "stage6-evaluation-controller")
            self.assertEqual(terminal["next_action"], "return-to-stage3-evaluation-process-attribution-and-repair")

    def test_terminal_plan_identity_reuse_is_zero_execution_and_rejects_dependency_drift(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            fixture = self._fixture(Path(directory))
            result = fixture.run()
            with mock.patch.object(gate, "_current_dependencies", return_value=fixture.dependencies):
                reused = gate.resume_by_plan_identity(self.suite_root, fixture.output_root, result["plan_identity"])
            self.assertTrue(reused["reused"])
            self.assertEqual(reused["model_calls"], 0)
            self.assertEqual(reused["product_executions"], 0)
            with mock.patch.object(gate, "_current_dependencies", return_value={**fixture.dependencies, "executor": "f" * 64}), \
                    mock.patch.object(gate, "_load_scheduling_migration", side_effect=OSError("no applicable migration")), \
                    mock.patch.object(gate, "_load_evaluator_reliability_migration", side_effect=OSError("no applicable migration")):
                with self.assertRaisesRegex(gate.BlindGateError, "直接依赖已漂移"):
                    gate.resume_by_plan_identity(self.suite_root, fixture.output_root, result["plan_identity"])

    def test_relative_gate_rejects_shared_cost_regression(self) -> None:
        candidate = self._observation()
        baseline = self._observation()
        candidate["resources"]["semantic_input_tokens"] = 101
        baseline["resources"]["semantic_input_tokens"] = 100
        result = gate._relative_decision(candidate, baseline)
        self.assertFalse(result["passed"])
        self.assertEqual(result["failures"][0]["metric"], "semantic_input_tokens")

    def test_answer_attribution_never_uses_wording_variation_as_failure(self) -> None:
        def diagnostic(product: tuple[int, int], oracle: tuple[int, int], *, controls: bool = True, variations: int = 5) -> dict[str, object]:
            product_correct, product_total = product
            oracle_correct, oracle_total = oracle
            return {
                "reader": {
                    "product_context_failures": product_total - product_correct,
                    "oracle_context_failures": oracle_total - oracle_correct,
                    "product_context_variations": variations,
                    "oracle_context_variations": variations,
                    "records": [
                        {"context": "product", "correct": product_correct, "observations": product_total},
                        {"context": "oracle", "correct": oracle_correct, "observations": oracle_total},
                    ],
                },
                "judge": {"controls_passed": controls},
            }

        stable_context_failure = gate._classify_answer_diagnostic(diagnostic((0, 3), (3, 3)))
        self.assertEqual(stable_context_failure["classification"], "candidate-context-failure")
        unstable_prompt = gate._classify_answer_diagnostic(diagnostic((2, 3), (3, 3)))
        self.assertEqual(unstable_prompt["classification"], "evaluation-process-failure")
        unstable_oracle = gate._classify_answer_diagnostic(diagnostic((0, 3), (2, 3)))
        self.assertEqual(unstable_oracle["classification"], "evaluation-process-failure")
        judge_failure = gate._classify_answer_diagnostic(diagnostic((0, 3), (3, 3), controls=False))
        self.assertEqual(judge_failure["classification"], "evaluation-process-failure")

    def test_level_contracts_have_local_identity_and_terminal_transition(self) -> None:
        contracts = {level: gate.load_contract(self.suite_root, level) for level in gate.GATE_LEVELS}
        self.assertEqual(len({value["identity"] for value in contracts.values()}), 4)
        self.assertEqual(contracts[5]["sequence"]["next_level"], 15)
        self.assertEqual(contracts[15]["sequence"]["previous_level"], 5)
        self.assertEqual(contracts[25]["sequence"]["next_level"], 50)
        self.assertTrue(contracts[50]["sequence"]["terminal"])
        self.assertIsNone(contracts[50]["sequence"]["next_level"])

    def test_candidate_failure_returns_complete_optimization_loop(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            fixture = self._fixture(Path(directory), candidate_pass=False)
            terminal = json.loads(Path(fixture.run()["result"]).read_text(encoding="utf-8"))
            transition = terminal["failure_transition"]
            self.assertEqual(transition["return_to_stage"], 3)
            self.assertTrue(transition["independent_nonoverlapping_reproduction_required"])
            self.assertTrue(transition["same_blind_content_rerun_forbidden"])
            self.assertIn("refreeze-candidate", transition["required_loop"])

    def test_all_levels_share_one_controller_and_only_bind_the_selected_contract(self) -> None:
        expected_next = {5: 15, 15: 25, 25: 50, 50: None}
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            controller_identity = gate._implementation_identity()["controller"]
            for level in gate.GATE_LEVELS:
                with self.subTest(level=level):
                    fixture = self._fixture(root / str(level), level=level)
                    terminal_ref = fixture.run()
                    terminal = json.loads(Path(terminal_ref["result"]).read_text(encoding="utf-8"))
                    plan = json.loads((fixture.output_root / "blind-gate" / terminal["plan_identity"] / "plan.json").read_text(encoding="utf-8"))
                    self.assertEqual(plan["direct_dependencies"]["gate-contract"], gate.load_contract(self.suite_root, level)["identity"])
                    self.assertEqual(terminal["next_level"], expected_next[level])
                    self.assertEqual(terminal["stage6_complete"], level == 50)
                    self.assertTrue(terminal["raw_materials_destroyed"])
                    self.assertFalse(any(fixture.scratch_root.iterdir()))
                    with mock.patch.object(gate, "_current_dependencies", return_value=plan["direct_dependencies"]):
                        reused = gate.resume_by_plan_identity(
                            self.suite_root, fixture.output_root, terminal["plan_identity"],
                        )
                    self.assertTrue(reused["reused"])
                    self.assertEqual(reused["model_calls"], 0)
                    self.assertEqual(reused["product_executions"], 0)
                    self.assertEqual(gate._implementation_identity()["controller"], controller_identity)
                    for other in set(gate.GATE_LEVELS) - {level}:
                        self.assertNotEqual(plan["direct_dependencies"]["gate-contract"], gate.load_contract(self.suite_root, other)["identity"])

    def test_later_level_requires_a_current_valid_previous_gate(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            fixture = self._fixture(Path(directory), level=5)
            reference = fixture.run()
            plan_root = fixture.output_root / "blind-gate" / reference["plan_identity"]
            plan = json.loads((plan_root / "plan.json").read_text(encoding="utf-8"))
            result = json.loads((plan_root / "result.json").read_text(encoding="utf-8"))
            contract = gate.load_contract(self.suite_root, 15)
            with mock.patch.object(gate, "_current_dependencies", return_value=plan["direct_dependencies"]):
                dependency = gate._previous_gate_dependency(
                    self.suite_root, fixture.output_root, contract, reference["plan_identity"],
                )
            self.assertEqual(dependency, result["identity"])
            with self.assertRaisesRegex(gate.BlindGateError, "缺少有效前级计划身份"):
                gate._previous_gate_dependency(self.suite_root, fixture.output_root, contract, None)

    def test_gate_history_records_fail_closed_current_attempt_and_preserves_prior_failure(self) -> None:
        path = self.suite_root / "iteration" / "v2" / "stage6-blind-gate-history.json"
        history = json.loads(path.read_text(encoding="utf-8"))
        content = {key: value for key, value in history.items() if key != "identity"}
        self.assertEqual(history["identity"], gate.evidence.canonical_sha256(content))
        self.assertEqual(history["controller_identity"], gate._implementation_identity()["controller"])
        previous = history["last_passed_gate"]
        self.assertEqual(previous["level"], 15)
        self.assertEqual(previous["plan_identity"], "bf9d6e6efc896e67b3158ca52ce3bd796b20d68ac057a7682c8943bfef2c5b1e")
        self.assertEqual(previous["result_identity"], "f05272eeb997e22a0aa6b7a05b8aa8642cf03cf6c780ae5184b4a7338396f5b4")
        self.assertTrue(previous["passed"])
        self.assertEqual(previous["next_level"], 25)
        self.assertTrue(previous["raw_materials_destroyed"])
        self.assertTrue(previous["valid_for_current_controller"])
        current = history["current"]
        self.assertEqual(current["level"], 25)
        self.assertEqual(current["plan_identity"], "c9b4b1891e2131ac70128dbe62f3b2a454061b930386066435fe9d3acee0c5a1")
        self.assertEqual(current["result_identity"], "d7071af7190c84310b66d3ebfa72c37ab00209c49aa7e35557dd323cbe5129dc")
        self.assertEqual(current["result_sha256"], "c6d1055ed9725340db4d9a6fc48e18c94ea6b7aed9c9f8b3672beda91afbf294")
        self.assertEqual(current["status"], "evaluation-process-rejected")
        self.assertTrue(current["candidate_decision"])
        self.assertTrue(current["candidate_absolute_passed"])
        self.assertTrue(current["candidate_relative_v0_passed"])
        self.assertFalse(current["candidate_failed"])
        self.assertTrue(current["evaluation_process_failed"])
        self.assertTrue(current["evaluation_process_requalified_without_candidate_rerun"])
        self.assertEqual(current["admitted_attempt"], 3)
        self.assertEqual(current["rejected_batches"], 2)
        self.assertEqual(current["generation_calls"], 75)
        self.assertEqual(current["quality_admission_calls"], 3)
        self.assertEqual(current["retries"], 0)
        self.assertEqual(current["rate_limit_events"], 0)
        self.assertEqual(current["interruptions"], 0)
        self.assertEqual(current["generation_max_active_observed"], 4)
        self.assertGreater(current["candidate_decision_wall_seconds"], current["level_budget_seconds"])
        self.assertFalse(current["level_budget_passed"])
        self.assertTrue(current["candidate_executed"])
        self.assertTrue(current["v0_executed"])
        self.assertTrue(current["raw_materials_destroyed"])
        self.assertFalse(current["contains_reversible_question_answer_or_evidence"])
        self.assertTrue(current["same_blind_content_rerun_forbidden"])
        self.assertTrue(current["valid_for_current_controller"])
        self.assertEqual(current["candidate_final_answer_accuracy"], 1.0)
        self.assertEqual(current["baseline_final_answer_accuracy"], 0.96)
        self.assertTrue(current["candidate_fact_delivery_complete"])
        self.assertTrue(current["baseline_fact_delivery_complete"])
        self.assertEqual(current["resume_model_calls"], 0)
        self.assertEqual(current["resume_product_executions"], 0)
        self.assertEqual(current["qualified_predecessor_for_level"], 50)
        gates = history["historical_controller_gates"]
        self.assertEqual([item["level"] for item in gates], [5, 15, 25])
        self.assertEqual([item["status"] for item in gates], ["passed", "passed", "candidate-rejected"])
        self.assertTrue(all(item["raw_materials_destroyed"] for item in gates))
        self.assertTrue(all(item["resume_model_calls"] == 0 for item in gates))
        self.assertTrue(all(item["resume_product_executions"] == 0 for item in gates))
        self.assertEqual(
            history["next_action"],
            "coordinator-review-and-commit-oracle-reader-repair-before-fresh-50-question-gate",
        )
        repair = history["answer_failure_attribution_repair"]
        self.assertEqual(repair["status"], "repaired-and-oracle-reader-independently-qualified")
        self.assertEqual(repair["selected_questions_per_attribution"], 1)
        self.assertEqual(repair["candidate_first_answer_reader"], "gpt-5.6-luna/xhigh")
        self.assertEqual(repair["post_failure_diagnostic_reader"], "gpt-5.6-terra/high")
        self.assertEqual(repair["qualification_questions"], 3)
        self.assertEqual(repair["reader_calls"], 15)
        self.assertEqual(repair["judge_calls"], 24)
        self.assertEqual(repair["product_context_failures"], 0)
        self.assertEqual(repair["oracle_context_failures"], 0)
        self.assertLessEqual(repair["projected_level_wall_seconds"], repair["level_total_wall_seconds_maximum"])
        self.assertEqual(repair["terminal_resume_model_calls"], 0)
        self.assertEqual(repair["terminal_resume_product_executions"], 0)
        self.assertEqual(len(history["invalidated_diagnostics"]), 6)
        invalidated = {item["result_identity"]: item for item in history["invalidated_diagnostics"]}
        self.assertIn("851f80be476d5e55718d0cc5d3b1732bb8d163ad3862bd74d80d64e4c7da2bb2", invalidated)
        self.assertIn("b90d36ce3cacbc2756a233c19cb091dc5e8ae406cdf8ef82c7933d9911b8981d", invalidated)
        self.assertIn("743c43f53629087355570b8861749afd10588a0309d14692ce442d1563189707", invalidated)
        wall = invalidated["3ce4bb4e6191981ee58a02ed79171fcb8fafea6287abeda7559a24c29dceb1f7"]
        self.assertTrue(wall["candidate_quality_passed"])
        self.assertFalse(wall["candidate_failure"])
        self.assertTrue(wall["evaluation_process_failed"])
        attribution = history["stage3_wall_attribution"]
        self.assertTrue(attribution["passed"])
        self.assertLess(attribution["projected_wall_plus_repeatability_error_seconds"], attribution["level_wall_seconds_maximum"])
        diagnosis = history["stage3_answer_sufficiency"]
        self.assertEqual(diagnosis["responsible_component"], "reader")
        self.assertFalse(diagnosis["kernel_change_required"])
        self.assertTrue(diagnosis["stage3_reclosed"])
        self.assertTrue(history["stage4_stage5_revalidation"]["ready_for_fresh_stage6"])
        qualification = history["stage2_reliability_qualification"]
        self.assertEqual(
            qualification["validation_contract_identity"],
            "059ea0abc8f0f1b9cfec3a780c9493829e05129c8c6f9cf4ade9980eafbaccb6",
        )
        self.assertEqual(qualification["plan_identity"], "953ec78955f4ce4ffcc8dd7878493dfc3b8b485a548e3989436d3003a07ceab6")
        self.assertEqual(qualification["result_identity"], "a3e2ceb071a931e12583671898212a2a48c49f41f6048b6c2f90bba621887990")
        self.assertEqual(qualification["admitted_questions"], 30)
        self.assertEqual(qualification["passed_per_batch"], [15, 15])
        self.assertEqual(qualification["generator"], "gpt-5.6-terra/xhigh")
        self.assertEqual(qualification["candidate_executions"], 0)
        self.assertEqual(qualification["baseline_executions"], 0)
        self.assertEqual(qualification["generation_max_active_observed"], 8)
        self.assertTrue(qualification["local_replacement_qualified"])
        invalidated = history["invalidated_incomplete_plans"]
        self.assertEqual(len(invalidated), 2)
        attempt = invalidated[0]
        self.assertEqual(attempt["plan_identity"], "ac540d1423fe1535ee2adfdc95150f77b9d6a1f84edcab9ef2288ede1c253b6c")
        self.assertEqual(attempt["completed_rejected_attempts"], [1])
        self.assertEqual(attempt["maximum_admission_batches"], 3)
        self.assertEqual(attempt["candidate_executions"], 0)
        self.assertEqual(attempt["baseline_executions"], 0)
        self.assertTrue(attempt["rejected_material_destroyed"])
        self.assertFalse(attempt["classification"]["candidate_failure"])
        self.assertFalse(attempt["classification"]["stage2_reliability_failure"])
        self.assertFalse(attempt["classification"]["terminal"])
        self.assertFalse(attempt["counts_toward_stage6"])
        self.assertFalse(attempt["current_gate"])
        leaked = invalidated[1]
        self.assertEqual(leaked["plan_identity"], "5c5f2bff48669b71449166519393ef94ca86c506f454601211160b3b8d1f2300")
        self.assertEqual(leaked["status"], "invalidated-operator-diagnostic-exposed-recovery-seed")
        self.assertEqual(leaked["candidate_executions"], 0)
        self.assertEqual(leaked["baseline_executions"], 0)
        self.assertTrue(leaked["raw_materials_destroyed"])
        self.assertFalse(leaked["counts_toward_stage6"])
        historical_failures = history["historical_failed_50_attempts"]
        self.assertEqual(len(historical_failures), 2)
        self.assertEqual(
            historical_failures[0]["plan_identity"],
            "0f80dc78d2a210242d792f0ef0dc1dad09208b8e65c24c435aea8998d4df670a",
        )
        self.assertFalse(historical_failures[0]["candidate_failure"])
        self.assertTrue(historical_failures[0]["same_content_rerun_forbidden"])
        self.assertEqual(
            historical_failures[1]["plan_identity"],
            "dd4afa9b487c70a5bef365247dfcc62bfd5122034199ba20bb70ac58b56f0050",
        )
        self.assertFalse(historical_failures[1]["candidate_failure"])
        self.assertTrue(historical_failures[1]["same_content_rerun_forbidden"])
        latest_failure = history["latest_failed_50_attempt"]
        self.assertEqual(
            latest_failure["plan_identity"],
            "c45a62f195552071aff23a136f4c1f244468f967952d1d1f4019c05269215e90",
        )
        self.assertEqual(latest_failure["status"], "evaluation-process-error")
        self.assertEqual(latest_failure["failure_stage"], "answer-failure-attribution")
        self.assertEqual(latest_failure["first_observed_gap"], "evidence_read_answer_incorrect")
        self.assertEqual(latest_failure["attribution_classification"], "evaluation-process-failure")
        self.assertEqual(latest_failure["attribution_reason"], "oracle-context-failed-or-mechanically-unstable")
        self.assertEqual(latest_failure["attribution_selected_material_order"], 37)
        self.assertEqual(latest_failure["attribution_diagnosed_questions"], 1)
        self.assertEqual(latest_failure["attribution_product_failures"], 3)
        self.assertEqual(latest_failure["attribution_oracle_failures"], 3)
        self.assertTrue(latest_failure["attribution_judge_controls_passed"])
        self.assertTrue(latest_failure["candidate_execution_completed"])
        self.assertEqual(latest_failure["candidate_questions"], 50)
        self.assertEqual(latest_failure["candidate_final_answer_accuracy"], 0.98)
        self.assertTrue(latest_failure["candidate_fact_delivery_complete"])
        self.assertEqual(latest_failure["candidate_temporal_correctness"], 0.9)
        self.assertEqual(latest_failure["candidate_conflict_correctness"], 1.0)
        self.assertEqual(latest_failure["candidate_retrieval_p95_ms"], 375.0)
        self.assertFalse(latest_failure["candidate_absolute_passed"])
        self.assertIsNone(latest_failure["candidate_decision"])
        self.assertFalse(latest_failure["candidate_failure"])
        self.assertFalse(latest_failure["baseline_executed"])
        self.assertTrue(latest_failure["raw_materials_destroyed"])
        self.assertFalse(latest_failure["contains_reversible_question_answer_or_evidence"])
        self.assertFalse(latest_failure["formal_state_written"])
        self.assertEqual(latest_failure["resume_model_calls"], 0)
        self.assertEqual(latest_failure["resume_product_executions"], 0)
        self.assertTrue(latest_failure["resume_dependencies_valid"])
        self.assertTrue(latest_failure["terminal_fail_closed"])
        self.assertTrue(latest_failure["same_content_rerun_forbidden"])
        self.assertEqual(latest_failure["return_to_stage"], 2)

    def test_wall_attribution_contract_and_result_are_content_addressed_and_closed(self) -> None:
        root = self.suite_root / "iteration" / "v2"
        contract = json.loads((root / "stage3-stage6-wall-attribution-contract.json").read_text(encoding="utf-8"))
        result = json.loads((root / "stage3-stage6-wall-attribution-result.json").read_text(encoding="utf-8"))
        self.assertEqual(contract["identity"], gate.evidence.canonical_sha256({key: value for key, value in contract.items() if key != "identity"}))
        self.assertEqual(result["identity"], gate.evidence.canonical_sha256({key: value for key, value in result.items() if key != "identity"}))
        self.assertEqual(result["contract_identity"], contract["identity"])
        attribution = result["critical_path_attribution"]
        closed = sum(attribution[name] for name in (
            "generation_wall_seconds",
            "quality_admission_wall_seconds",
            "candidate_complete_consumer_wall_seconds",
            "v0_complete_consumer_wall_seconds",
            "controller_start_resume_and_unattributed_wall_seconds",
        ))
        self.assertAlmostEqual(closed, attribution["closed_total_wall_seconds"])
        self.assertFalse(result["root_cause"]["candidate_kernel_failed"])
        self.assertTrue(result["root_cause"]["whole_level_process_efficiency_failed"])
        history = json.loads((root / "stage6-blind-gate-history.json").read_text(encoding="utf-8"))
        self.assertEqual(result["repair"]["new_controller_identity"], history["historical_controller_identity"])
        self.assertNotEqual(result["repair"]["new_controller_identity"], gate._implementation_identity()["controller"])
        projection = result["conservative_projection"]
        self.assertAlmostEqual(
            projection["projected_wall_plus_error_seconds"],
            projection["projected_level_wall_seconds"] + projection["repeatability_error_seconds"],
        )
        self.assertGreater(projection["remaining_margin_seconds"], projection["repeatability_error_seconds"])
        self.assertLessEqual(projection["projected_wall_plus_error_seconds"], projection["level_wall_seconds_maximum"])
        self.assertTrue(result["stage_impact"]["stage3_reclosed"])
        self.assertFalse(result["stage_impact"]["stage4_candidate_identity_changed"])
        self.assertTrue(result["stage_impact"]["stage5_checkpoints_reused"])

    def test_scheduling_migration_preserves_only_current_gate_controller_dependency(self) -> None:
        migration = gate._load_scheduling_migration(self.suite_root)
        self.assertEqual(migration["future_gate_level"], 50)
        self.assertTrue(migration["effect_scope"]["historical_5_15_25_materials_outputs_scores_and_decisions_unchanged"])
        self.assertEqual(migration["noncandidate_qualification"]["passed_per_batch"], [15, 15])
        self.assertEqual(migration["noncandidate_qualification"]["candidate_executions"], 0)
        planned = {"controller": migration["source_controller_identity"], "executor": "a" * 64}
        current = {"controller": migration["target_controller_identity"], "executor": "a" * 64}
        plan_identity = migration["preserved_current_chain"][1]["plan_identity"]
        self.assertTrue(gate._dependencies_current_or_scheduling_migrated(self.suite_root, plan_identity, planned, current))
        self.assertFalse(gate._dependencies_current_or_scheduling_migrated(
            self.suite_root, plan_identity, planned, {**current, "executor": "b" * 64},
        ))

    def test_oracle_reader_migration_preserves_prior_gate_facts_only_for_declared_dependencies(self) -> None:
        migration = gate._load_evaluator_reliability_migration(self.suite_root)
        self.assertEqual(migration["oracle_reader_repair"]["source_reader"], "gpt-5.6-luna/xhigh")
        self.assertEqual(migration["oracle_reader_repair"]["target_reader"], "gpt-5.6-terra/high")
        self.assertTrue(migration["oracle_reader_repair"]["candidate_first_answer_reader_unchanged"])
        planned = {
            "controller": migration["source_controller_identity"],
            "answer-attribution-controller": migration["dependency_changes"]["answer-attribution-controller"]["target"],
            "reader-reliability-selection": migration["dependency_changes"]["reader-reliability-selection"]["target"],
            "evaluator-environment-qualification": "ce0340f8aa62743990240807c033daba7ac6713a8d70d4f03b829efb7ee9f4a7",
            "candidate-subject": "a" * 64,
        }
        current = {
            **planned,
            "controller": migration["target_controller_identity"],
            "evaluator-environment-qualification": migration["qualification_receipt_identity"],
        }
        plan_identity = "c45a62f195552071aff23a136f4c1f244468f967952d1d1f4019c05269215e90"
        self.assertTrue(gate._dependencies_current_or_scheduling_migrated(self.suite_root, plan_identity, planned, current))
        self.assertFalse(gate._dependencies_current_or_scheduling_migrated(
            self.suite_root, plan_identity, planned, {**current, "candidate-subject": "b" * 64},
        ))

    def _fixture(
        self,
        root: Path,
        *,
        candidate_pass: bool = True,
        first_admission_rejected: bool = False,
        all_admissions_rejected: bool = False,
        level: int = 5,
        first_rejected_case_only: bool = False,
        qualification_error: bool = False,
    ) -> "GateFixture":
        return GateFixture(
            self.suite_root,
            root,
            candidate_pass=candidate_pass,
            first_admission_rejected=first_admission_rejected,
            all_admissions_rejected=all_admissions_rejected,
            level=level,
            first_rejected_case_only=first_rejected_case_only,
            qualification_error=qualification_error,
        )

    @staticmethod
    def _observation(questions: int = 5) -> dict[str, object]:
        return {
            "questions": questions,
            "fact_delivery": {"complete": True, "missing_questions": 0, "by_first_observed_gap": {"none": questions}},
            "final_answer_accuracy": 1.0,
            "temporal_correctness": 1.0,
            "conflict_correctness": 1.0,
            "latency": {"retrieval_mean_ms": 100.0, "retrieval_p95_ms": 150.0, "wall_seconds": 30.0},
            "resources": {"semantic_input_tokens": 100, "reader_input_tokens": 100, "judge_input_tokens": 100, "ownward_data_bytes": 100},
            "codex": {"calls": 10, "attempts": 10, "retries": 0},
        }

    @staticmethod
    def _minimal_case(case_id: str) -> dict[str, object]:
        return {
            "case_id": case_id,
            "coverage": "fixture",
            "question": f"Question {case_id}",
            "answer": f"Answer {case_id}",
            "truth_claims": [{"claim": f"Claim {case_id}"}],
            "sessions": [{"date": "2026-01-01", "turns": [{"role": "user", "content": f"Fact {case_id}"}]}],
        }

    @staticmethod
    def _contains_forbidden_raw_field(value: object) -> bool:
        forbidden = {"question", "answer", "sessions", "truth_claims", "evidence"}
        if isinstance(value, dict):
            return bool(forbidden & set(value)) or any(BlindGateTests._contains_forbidden_raw_field(item) for item in value.values())
        if isinstance(value, list):
            return any(BlindGateTests._contains_forbidden_raw_field(item) for item in value)
        return False


class GateFixture:
    def __init__(
        self,
        suite_root: Path,
        root: Path,
        *,
        candidate_pass: bool,
        first_admission_rejected: bool,
        all_admissions_rejected: bool,
        level: int = 5,
        first_rejected_case_only: bool = False,
        qualification_error: bool = False,
    ) -> None:
        self.suite_root = suite_root
        self.output_root = root / "evidence"
        self.runs_root = root / "runs"
        self.runs_root.mkdir(parents=True)
        self.state_path = root / "state.json"
        self.state_bytes = b'{"immutable":true}\n'
        self.state_path.write_bytes(self.state_bytes)
        self.candidate_config = root / "candidate.json"
        self.baseline_config = root / "baseline.json"
        self.subject_manifest = root / "subject.json"
        for path in (self.candidate_config, self.baseline_config, self.subject_manifest):
            path.write_text("{}\n", encoding="utf-8")
        self.candidate_pass = candidate_pass
        self.first_admission_rejected = first_admission_rejected
        self.all_admissions_rejected = all_admissions_rejected
        self.first_rejected_case_only = first_rejected_case_only
        self.qualification_error = qualification_error
        self.level = level
        self.admission_calls = 0
        self.generator_calls = 0
        self.generated_case_ids: list[str] = []
        self.execution_order: list[str] = []
        self.dependencies = {
            "controller": "1" * 64,
            "executor": "2" * 64,
            "gate-contract": gate.load_contract(self.suite_root, level)["identity"],
            "evaluator-environment-qualification": "3" * 64,
        }
        self.scratch_root = self.runs_root / "kernel-v2-blind-gate"

    def run(self) -> dict[str, object]:
        candidate_subject = {
            "identity": "a" * 64, "role": "v2-candidate", "name": "candidate",
            "content": {"kernel_generation_identity": "66a6e827841c09279dec99d53cc7e5db04a879c94da14cbd3355419029bfa2db", "kernel_effect_identity": "a2ad626f00e3806599f625c07d4df4a6430c23c27b3e996c50bd75a9e5fef922"},
        }
        baseline_subject = {
            "identity": "b" * 64, "role": "evaluation-baseline", "name": "v0",
            "content": {"kernel_generation_identity": "1952f4b869e49c4aaf2fbac8139fd0a0c25e4331e18ef559a14faf1db7ed7b77", "kernel_effect_identity": "61729fb8bfdb2fb2895017c0cf00713adf95414978b161f76593509af5a0017a"},
        }
        runtime = {
            "runs": self.runs_root,
            "binary": self.subject_manifest,
            "embedding": self.runs_root,
            "environment_manifest": self.subject_manifest,
            "protocol": self.subject_manifest,
            "codex_binary": self.subject_manifest,
            "codex_auth_file": self.subject_manifest,
            "protocol_value": {},
            "semantic_representation_identity": "c" * 64,
        }

        def select(_comparison: object, selector: str | None, subject_manifest: Path | None = None) -> dict[str, object]:
            return baseline_subject if selector == "v0" else candidate_subject

        def invoke(**kwargs: object) -> tuple[dict[str, object], dict[str, object]]:
            role = kwargs["role"]
            if role == "generator":
                self.generator_calls += 1
                schema = kwargs["schema"]
                case_id = schema["properties"]["case"]["properties"]["case_id"]["enum"][0]
                coverage = schema["properties"]["case"]["properties"]["coverage"]["enum"][0]
                self.generated_case_ids.append(case_id)
                case = self._case(case_id, coverage)
                return {"case": case}, {"calls": 1, "attempts": 1, "wall_seconds": 0.1}
            self.admission_calls += 1
            materials_path = kwargs["prompt"]
            del materials_path
            case_ids = kwargs["schema"]["properties"]["assessments"]["items"]["properties"]["case_id"]["enum"]
            assessments = []
            for case_id in case_ids:
                rejected = self.all_admissions_rejected or (
                    self.first_admission_rejected and self.admission_calls == 1
                ) or (
                    self.first_rejected_case_only and self.admission_calls == 1 and case_id == case_ids[0]
                )
                checks = {name: not rejected for name in ("plausible", "difficulty_sufficient", "unique_answer", "evidence_sufficient", "no_surface_shortcut", "scoring_discriminative")}
                assessments.append({"case_id": case_id, "checks": checks})
            return {"assessments": assessments}, {"calls": 1, "attempts": 1, "wall_seconds": 0.1}

        def runner(**kwargs: object) -> dict[str, object]:
            output = Path(kwargs["output_dir"])
            output.mkdir(parents=True, exist_ok=True)
            subject_identity = str(kwargs["subject_identity"])
            name = "candidate" if subject_identity == candidate_subject["identity"] else "baseline"
            self.execution_order.append(name)
            report = {"subject": name}
            (output / "report.json").write_text(json.dumps(report) + "\n", encoding="utf-8")
            (output / "checkpoint-manifest.json").write_text('{"complete":true}\n', encoding="utf-8")
            (output / "diagnostic-summary.json").write_text(json.dumps({"by_first_observed_gap": {"none": self.level}}) + "\n", encoding="utf-8")
            return report

        def observe(report: dict[str, object], _materials: dict[str, object]) -> dict[str, object]:
            value = BlindGateTests._observation(self.level)
            if report["subject"] == "candidate" and not self.candidate_pass:
                value["final_answer_accuracy"] = 0.8
                value["fact_delivery"] = {"complete": False, "missing_questions": 1, "by_first_observed_gap": {"none": 4, "target_evidence_not_read": 1}}
            if report["subject"] == "baseline":
                value["resources"] = {"semantic_input_tokens": 120, "reader_input_tokens": 100, "judge_input_tokens": 100, "ownward_data_bytes": 120}
            return value

        runtime_calibration = {"runtime_calibration_identity": "d" * 64}
        patches = (
            mock.patch.object(gate.evidence, "load_contract", return_value={"identity": "e" * 64}),
            mock.patch.object(gate.validation, "load_validation_contract", return_value=self._validation_contract()),
            mock.patch.object(gate.validation, "load_blind_budget_archive", return_value={"identity": "f" * 64}),
            mock.patch.object(gate.reader_reliability, "load_selection", return_value={"identity": "7" * 64, "selected_reasoning_effort": "xhigh"}),
            mock.patch.object(gate.validation, "validate_execution_config", return_value=runtime),
            mock.patch.object(
                gate.evaluator_reliability,
                "load_current_qualification",
                return_value={"identity": "3" * 64},
                side_effect=RuntimeError("qualification drift") if self.qualification_error else None,
            ),
            mock.patch.object(gate.evidence, "select_subject", side_effect=select),
            mock.patch.object(gate, "_validate_subjects"),
            mock.patch.object(gate, "_shared_conditions", return_value={"shared": "0" * 64}),
            mock.patch.object(gate.evidence, "calibrate_runtime", return_value=runtime_calibration),
            mock.patch.object(gate, "_direct_dependencies", return_value=self.dependencies),
            mock.patch.object(gate, "_previous_gate_dependency", return_value=None if self.level == 5 else "9" * 64),
            mock.patch.object(gate, "_validate_material_isolation"),
            mock.patch.object(gate.validation, "observe_report", side_effect=observe),
        )
        with patches[0], patches[1], patches[2], patches[3], patches[4], patches[5], patches[6], patches[7], patches[8], patches[9], patches[10], patches[11], patches[12], patches[13]:
            return gate.run(
                self.suite_root, self.output_root, self.candidate_config, self.baseline_config,
                self.subject_manifest, self.state_path, seed="fixture-seed-0001",
                level=self.level, previous_plan_identity=None if self.level == 5 else "8" * 64,
                invoker=invoke, runner=runner,
            )

    @staticmethod
    def _case(case_id: str, coverage: str) -> dict[str, object]:
        case_number = int(case_id.rsplit("c", 1)[1])
        session_prefix = f"c{case_number:02d}"
        answer_ids = [f"{session_prefix}-s05"] if coverage == "knowledge-update-conflict" else ([f"{session_prefix}-s01", f"{session_prefix}-s02"] if coverage in {"temporal-order", "multi-session-relation", "multi-session-distractor"} else [f"{session_prefix}-s01"])
        sessions = [
            {
                "session_id": f"{session_prefix}-s{index:02d}",
                "date": f"2025-01-0{index}",
                "turns": [{
                    "role": "assistant" if coverage == "single-session-assistant-fact" and index == 1 else "user",
                    "content": f"Evidence statement {index} with value {'alpha' if f'{session_prefix}-s{index:02d}' in answer_ids else 'beta'}."
                }],
            }
            for index in range(1, 7)
        ]
        question = "Which independently recorded value is requested after resolving the evidence?"
        temporal_binding = None
        distractor_binding = None
        if coverage == "temporal-order":
            link_key = f"KEY-{case_number:02d}-ZX"
            anchor_one = f"Anchor Alder {case_number:02d}"
            anchor_two = f"Anchor Cedar {case_number:02d}"
            sessions[0]["turns"][0]["content"] = f"Opaque record {link_key} stores the value alpha."
            sessions[1]["turns"][0]["content"] = f"Opaque record {link_key} occurred between {anchor_one} and {anchor_two}."
            question = f"Which value belongs to the opaque record that occurred between {anchor_one} and {anchor_two}?"
            temporal_binding = {
                "link_key": link_key,
                "value_session_id": answer_ids[0],
                "temporal_session_id": answer_ids[1],
                "question_anchor_terms": [anchor_one, anchor_two],
            }
            sessions[4]["turns"][0]["content"] += f" It also mentions {anchor_one}."
        if coverage == "multi-session-distractor":
            link_key = f"KEY-{case_number:02d}-QD"
            qualifier_one = f"Unit Indigo {case_number:02d}"
            qualifier_two = f"Window Quartz {case_number:02d}"
            sessions[0]["turns"][0]["content"] = f"Opaque record {link_key} stores the value alpha."
            sessions[1]["turns"][0]["content"] = f"Opaque record {link_key} belongs to {qualifier_one} during {qualifier_two}."
            question = f"Which value belongs to the record for {qualifier_one} during {qualifier_two}?"
            distractor_binding = {
                "link_key": link_key,
                "value_session_id": answer_ids[0],
                "selector_session_id": answer_ids[1],
                "question_qualifier_terms": [qualifier_one, qualifier_two],
            }
            sessions[4]["turns"][0]["content"] += f" It also mentions {qualifier_one}."
        evidence_bindings = [
            {"session_id": session_id, "quote": sessions[int(session_id.rsplit("s", 1)[1]) - 1]["turns"][0]["content"]}
            for session_id in answer_ids
        ]
        if coverage == "temporal-order":
            question_clues = [
                {"clue": "value", "support_session_id": answer_ids[0], "distractor_session_id": f"{session_prefix}-s04"},
                {"clue": anchor_one, "support_session_id": answer_ids[1], "distractor_session_id": f"{session_prefix}-s05"},
            ]
        elif coverage == "multi-session-distractor":
            question_clues = [
                {"clue": "value", "support_session_id": answer_ids[0], "distractor_session_id": f"{session_prefix}-s04"},
                {"clue": qualifier_one, "support_session_id": answer_ids[1], "distractor_session_id": f"{session_prefix}-s05"},
            ]
        else:
            question_clues = [
                {"clue": "evidence", "support_session_id": answer_ids[0], "distractor_session_id": f"{session_prefix}-s04"},
                {"clue": "value", "support_session_id": answer_ids[-1], "distractor_session_id": f"{session_prefix}-s04"},
            ]
        result = {
            "case_id": case_id,
            "coverage": coverage,
            "question_type": "knowledge-update" if coverage == "knowledge-update-conflict" else ("temporal-reasoning" if coverage == "temporal-order" else ("single-session-assistant" if coverage == "single-session-assistant-fact" else "multi-session")),
            "question_date": "2025-02-01",
            "question": question,
            "answer": "alpha",
            "answer_session_ids": answer_ids,
            "stale_session_ids": [f"{session_prefix}-s01"] if coverage == "knowledge-update-conflict" else [],
            "distractor_session_ids": [f"{session_prefix}-s03", f"{session_prefix}-s04"] if coverage == "knowledge-update-conflict" else [f"{session_prefix}-s04", f"{session_prefix}-s05"],
            "sessions": sessions,
            "evidence_bindings": evidence_bindings,
            "control_binding": {
                "plausible_wrong_answer": "beta",
                "wrong_answer_session_id": f"{session_prefix}-s04",
                "missing_evidence_session_id": answer_ids[0],
            },
            "surface_shortcut_proof": {"question_clues": question_clues},
        }
        if temporal_binding is not None:
            result["temporal_binding"] = temporal_binding
        if distractor_binding is not None:
            result["distractor_binding"] = distractor_binding
        return result

    @staticmethod
    def _validation_contract() -> dict[str, object]:
        return {
            "identity": "1" * 64,
            "blind": {
                "generation": {"model": "generator", "reasoning_effort": "medium", "timeout_seconds": 1, "attempts": 1},
                "quality_admission": {
                    "model": "admission", "reasoning_effort": "medium", "timeout_seconds": 1, "attempts": 1,
                    "required_checks": ["plausible", "difficulty_sufficient", "unique_answer", "evidence_sufficient", "no_surface_shortcut", "scoring_discriminative"],
                },
                "minimum_sessions_per_question": 5,
                "minimum_distractor_sessions_per_question": 2,
            },
        }


if __name__ == "__main__":
    unittest.main()
