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
                self.assertEqual(value["execution"]["generation_max_active"], 4)
                self.assertEqual(value["execution"]["generation_worker_active_turns_maximum"], 1)
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
            with mock.patch.object(gate, "_current_dependencies", return_value={**fixture.dependencies, "executor": "f" * 64}):
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

    def test_gate_history_reclassifies_old_wall_failure_and_requires_fresh_five(self) -> None:
        path = self.suite_root / "iteration" / "v2" / "stage6-blind-gate-history.json"
        history = json.loads(path.read_text(encoding="utf-8"))
        content = {key: value for key, value in history.items() if key != "identity"}
        self.assertEqual(history["identity"], gate.evidence.canonical_sha256(content))
        self.assertEqual(history["controller_identity"], gate._implementation_identity()["controller"])
        previous = history["last_passed_gate"]
        self.assertEqual(previous["level"], 5)
        self.assertEqual(previous["plan_identity"], "82cfab1b381cb5405ef503f309604a366b519e631b4ecacb33592f008f16c2b9")
        self.assertEqual(previous["result_identity"], "b13bf7c136fb6963d8ec9f2f33b8659566913e9408e2e6d9fc13dc045b6160bd")
        self.assertTrue(previous["passed"])
        self.assertEqual(previous["next_level"], 15)
        self.assertTrue(previous["raw_materials_destroyed"])
        self.assertFalse(previous["valid_for_current_controller"])
        self.assertIsNone(history["current"])
        self.assertEqual(history["next_action"], "restart-stage6-from-fresh-5-question-gate")
        self.assertEqual(len(history["invalidated_diagnostics"]), 5)
        invalidated = {item["result_identity"]: item for item in history["invalidated_diagnostics"]}
        self.assertIn("851f80be476d5e55718d0cc5d3b1732bb8d163ad3862bd74d80d64e4c7da2bb2", invalidated)
        self.assertIn("b90d36ce3cacbc2756a233c19cb091dc5e8ae406cdf8ef82c7933d9911b8981d", invalidated)
        wall = invalidated["3ce4bb4e6191981ee58a02ed79171fcb8fafea6287abeda7559a24c29dceb1f7"]
        self.assertTrue(wall["candidate_quality_passed"])
        self.assertFalse(wall["candidate_failure"])
        self.assertTrue(wall["evaluation_process_failed"])
        attribution = history["stage3_wall_attribution"]
        self.assertTrue(attribution["passed"])
        self.assertLess(attribution["projected_wall_plus_repeatability_error_seconds"], attribution["level_wall_seconds_maximum"])
        self.assertTrue(history["stage4_stage5_revalidation"]["ready_for_fresh_stage6"])
        qualification = history["stage2_reliability_qualification"]
        self.assertEqual(qualification["admitted_questions"], 30)
        self.assertEqual(qualification["candidate_executions"], 0)
        self.assertEqual(qualification["baseline_executions"], 0)
        invalidated = history["invalidated_incomplete_plans"]
        self.assertEqual(len(invalidated), 1)
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
        self.assertEqual(result["repair"]["new_controller_identity"], gate._implementation_identity()["controller"])
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

    def _fixture(
        self,
        root: Path,
        *,
        candidate_pass: bool = True,
        first_admission_rejected: bool = False,
        all_admissions_rejected: bool = False,
        level: int = 5,
    ) -> "GateFixture":
        return GateFixture(
            self.suite_root,
            root,
            candidate_pass=candidate_pass,
            first_admission_rejected=first_admission_rejected,
            all_admissions_rejected=all_admissions_rejected,
            level=level,
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
        self.level = level
        self.admission_calls = 0
        self.execution_order: list[str] = []
        self.dependencies = {
            "controller": "1" * 64,
            "executor": "2" * 64,
            "gate-contract": gate.load_contract(self.suite_root, level)["identity"],
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
                schema = kwargs["schema"]
                case_id = schema["properties"]["case"]["properties"]["case_id"]["enum"][0]
                coverage = schema["properties"]["case"]["properties"]["coverage"]["enum"][0]
                case = self._case(case_id, coverage)
                return {"case": case}, {"calls": 1, "attempts": 1, "wall_seconds": 0.1}
            self.admission_calls += 1
            materials_path = kwargs["prompt"]
            del materials_path
            case_ids = kwargs["schema"]["properties"]["assessments"]["items"]["properties"]["case_id"]["enum"]
            passed = not self.all_admissions_rejected and not (
                self.first_admission_rejected and self.admission_calls == 1
            )
            checks = {name: passed for name in ("plausible", "difficulty_sufficient", "unique_answer", "evidence_sufficient", "no_surface_shortcut", "scoring_discriminative")}
            return {"assessments": [{"case_id": case_id, "checks": checks} for case_id in case_ids]}, {"calls": 1, "attempts": 1, "wall_seconds": 0.1}

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
            mock.patch.object(gate.validation, "validate_execution_config", return_value=runtime),
            mock.patch.object(gate.evidence, "select_subject", side_effect=select),
            mock.patch.object(gate, "_validate_subjects"),
            mock.patch.object(gate, "_shared_conditions", return_value={"shared": "0" * 64}),
            mock.patch.object(gate.evidence, "calibrate_runtime", return_value=runtime_calibration),
            mock.patch.object(gate, "_direct_dependencies", return_value=self.dependencies),
            mock.patch.object(gate, "_previous_gate_dependency", return_value=None if self.level == 5 else "9" * 64),
            mock.patch.object(gate, "_validate_material_isolation"),
            mock.patch.object(gate.validation, "observe_report", side_effect=observe),
        )
        with patches[0], patches[1], patches[2], patches[3], patches[4], patches[5], patches[6], patches[7], patches[8], patches[9], patches[10], patches[11]:
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
