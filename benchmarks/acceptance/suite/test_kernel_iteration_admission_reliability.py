from __future__ import annotations

import json
import inspect
from pathlib import Path
import tempfile
import unittest
from unittest import mock
from contextlib import contextmanager

import kernel_iteration_admission_reliability as reliability
import kernel_iteration_validation as validation


CHECKS = (
    "plausible",
    "difficulty_sufficient",
    "unique_answer",
    "evidence_sufficient",
    "no_surface_shortcut",
    "scoring_discriminative",
)


class AdmissionReliabilityTests(unittest.TestCase):
    def setUp(self) -> None:
        self.suite_root = Path(__file__).resolve().parent

    def test_contract_freezes_non_candidate_fifteen_question_boundary(self) -> None:
        contract = reliability.load_contract(self.suite_root)
        self.assertEqual(contract["questions_per_batch"], 15)
        self.assertEqual(contract["modes"]["qualification"]["batches"], 2)
        self.assertTrue(contract["quality"]["candidate_or_baseline_execution_forbidden"])
        self.assertEqual(contract["budget"]["per_batch_wall_seconds_maximum"], 492)
        self.assertEqual(contract["execution"]["generation_max_active"], 8)
        self.assertEqual(contract["execution"]["rejection_replacement"], "rejected-cases-only")
        self.assertTrue(contract["execution"]["full_set_readmission_after_replacement"])

    def test_cli_entry_identity_is_role_owned_and_scopes_real_dispatch(self) -> None:
        identity = reliability.cli_entry_identity()
        self.assertEqual(len(identity), 64)
        sources = "\n".join(inspect.getsource(callback) for callback in (
            reliability.add_cli_arguments, reliability.cli_selected, reliability.dispatch_cli,
        ))
        self.assertIn("--blind-admission-reliability-config", sources)
        self.assertIn("resume_by_plan_identity", sources)
        self.assertIn("return run(", sources)
        original = reliability.inspect.getsource

        def changed_dispatch(callback):
            source = original(callback)
            return source + "\n# changed\n" if callback is reliability.dispatch_cli else source

        with mock.patch.object(reliability.inspect, "getsource", side_effect=changed_dispatch):
            self.assertNotEqual(identity, reliability.cli_entry_identity())

    def test_admission_aggregate_has_coverage_check_and_combination_without_case_ids(self) -> None:
        materials = self._materials()
        output = self._admission_output(materials, {
            "b01-c01": ("difficulty_sufficient", "no_surface_shortcut"),
            "b01-c07": ("evidence_sufficient",),
        })
        result = validation.validate_admission(output, materials, self._validation_contract())
        aggregate = result["failure_aggregate"]
        self.assertEqual(result["rejected_count"], 2)
        self.assertEqual(aggregate["failed_by_check"]["difficulty_sufficient"], 1)
        self.assertEqual(aggregate["failed_by_check"]["evidence_sufficient"], 1)
        self.assertEqual(aggregate["rejected_by_coverage"]["knowledge-update-conflict"], 1)
        self.assertEqual(aggregate["rejected_by_coverage"]["temporal-order"], 1)
        self.assertNotIn("b01-c01", json.dumps(aggregate, ensure_ascii=False))

    def test_fifteen_case_admission_prompt_uses_actual_batch_size(self) -> None:
        generated = [
            validation._validate_generated_case({"case": ReliabilityFixture.case(f"b01-c{index:02d}", validation.BLIND_COVERAGE[(index - 1) % 5])}, f"b01-c{index:02d}", validation.BLIND_COVERAGE[(index - 1) % 5], self._validation_contract())
            for index in range(1, 16)
        ]
        materials = reliability._materials(generated)
        prompt = validation._admission_prompt(
            self._validation_contract(), validation._admission_review_materials(materials, generated),
        )
        self.assertIn("Assess all 15 cases independently", prompt)
        self.assertNotIn("five-case batch", prompt)

    def test_mechanical_admission_proof_rejects_unbound_quotes_controls_and_shortcuts(self) -> None:
        contract = self._validation_contract()
        case = ReliabilityFixture.case("b01-c05", "multi-session-distractor")
        case["evidence_bindings"][0]["quote"] = "not present in the declared source"
        with self.assertRaisesRegex(validation.KernelIterationValidationError, "引文不在声明会话"):
            validation._validate_generated_case({"case": case}, "b01-c05", "multi-session-distractor", contract)
        case = ReliabilityFixture.case("b01-c05", "multi-session-distractor")
        case["control_binding"]["wrong_answer_session_id"] = case["answer_session_ids"][0]
        with self.assertRaisesRegex(validation.KernelIterationValidationError, "错误答案没有绑定声明干扰证据"):
            validation._validate_generated_case({"case": case}, "b01-c05", "multi-session-distractor", contract)
        case = ReliabilityFixture.case("b01-c05", "multi-session-distractor")
        case["surface_shortcut_proof"]["question_clues"][1]["distractor_session_id"] = f"c05-s04"
        with self.assertRaisesRegex(validation.KernelIterationValidationError, "问题线索没有干扰项镜像"):
            validation._validate_generated_case({"case": case}, "b01-c05", "multi-session-distractor", contract)

    def test_generated_case_mechanical_guards_reject_surface_and_temporal_shortcuts(self) -> None:
        contract = self._validation_contract()
        case = ReliabilityFixture.case("b01-c01", "temporal-order")
        case["question"] = "Which record has the answer alpha?"
        with self.assertRaisesRegex(validation.KernelIterationValidationError, "表面捷径"):
            validation._validate_generated_case({"case": case}, "b01-c01", "temporal-order", contract)
        case = ReliabilityFixture.case("b01-c01", "temporal-order")
        case["answer_session_ids"] = ["c01-s01"]
        with self.assertRaisesRegex(validation.KernelIterationValidationError, "两个独立答案会话"):
            validation._validate_generated_case({"case": case}, "b01-c01", "temporal-order", contract)
        case = ReliabilityFixture.case("b01-c01", "temporal-order")
        case["sessions"][3]["turns"][0]["content"] += " alpha"
        with self.assertRaisesRegex(validation.KernelIterationValidationError, "非答案会话泄露"):
            validation._validate_generated_case({"case": case}, "b01-c01", "temporal-order", contract)

    def test_generator_schema_binds_every_reference_to_the_case_local_session_set(self) -> None:
        schema = validation._generator_case_schema("b02-c07", "temporal-order", self._validation_contract())
        properties = schema["properties"]["case"]["properties"]
        expected = [f"c07-s{number:02d}" for number in range(1, 7)]
        self.assertEqual(properties["sessions"]["minItems"], 6)
        self.assertEqual(properties["sessions"]["maxItems"], 6)
        self.assertEqual(properties["sessions"]["items"]["properties"]["session_id"]["enum"], expected)
        self.assertEqual(properties["answer_session_ids"]["items"]["enum"], expected)
        self.assertEqual(properties["stale_session_ids"]["items"]["enum"], expected)
        self.assertEqual(properties["distractor_session_ids"]["items"]["enum"], expected)

    def test_temporal_binding_requires_a_hidden_link_and_separate_value_and_order_evidence(self) -> None:
        contract = self._validation_contract()
        case = ReliabilityFixture.case("b01-c02", "temporal-order")
        validation._validate_generated_case({"case": case}, "b01-c02", "temporal-order", contract)
        case = ReliabilityFixture.case("b01-c02", "temporal-order")
        case["question"] += " KEY-02-ZX"
        with self.assertRaisesRegex(validation.KernelIterationValidationError, "隐藏连接键"):
            validation._validate_generated_case({"case": case}, "b01-c02", "temporal-order", contract)
        case = ReliabilityFixture.case("b01-c02", "temporal-order")
        case["sessions"][1]["turns"][0]["content"] += " alpha"
        with self.assertRaisesRegex(validation.KernelIterationValidationError, "值与顺序证据未分离"):
            validation._validate_generated_case({"case": case}, "b01-c02", "temporal-order", contract)
        case = ReliabilityFixture.case("b01-c02", "temporal-order")
        case["sessions"][3]["turns"][0]["content"] += " KEY-02-ZX"
        with self.assertRaisesRegex(validation.KernelIterationValidationError, "连接键存在第二条路径"):
            validation._validate_generated_case({"case": case}, "b01-c02", "temporal-order", contract)
        case = ReliabilityFixture.case("b01-c02", "temporal-order")
        case["sessions"][3]["turns"][0]["content"] += " Anchor Alder 02 and Anchor Cedar 02"
        with self.assertRaisesRegex(validation.KernelIterationValidationError, "双锚点存在第二条路径"):
            validation._validate_generated_case({"case": case}, "b01-c02", "temporal-order", contract)

    def test_default_controller_uses_bounded_independent_single_turn_workers_per_batch(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            fixture = ReliabilityFixture(self.suite_root, Path(directory))
            opened: list[str] = []

            @contextmanager
            def batch_invoker(_suite_root: Path, _runtime: dict[str, object], transport_parent: Path):
                opened.append(transport_parent.name)
                yield fixture.invoke

            with fixture.patches(), mock.patch.object(validation, "_native_codex_batch_invoker", side_effect=batch_invoker):
                result = reliability.run(
                    self.suite_root,
                    fixture.output_root,
                    fixture.config_path,
                    fixture.state_path,
                    mode="qualification",
                    seed="reliability-fixture-seed-0002",
                )
            self.assertTrue(result["passed"])
            self.assertEqual(opened, [f"worker-{index:02d}" for index in range(1, 9)] * 2)
            self.assertEqual(fixture.generator_calls, 30)
            self.assertEqual(fixture.admission_calls, 2)

    def test_multi_distractor_binding_rejects_any_second_complete_selection_path(self) -> None:
        contract = self._validation_contract()
        case = ReliabilityFixture.case("b01-c05", "multi-session-distractor")
        validation._validate_generated_case({"case": case}, "b01-c05", "multi-session-distractor", contract)
        case = ReliabilityFixture.case("b01-c05", "multi-session-distractor")
        case["sessions"][3]["turns"][0]["content"] += " KEY-05-QD"
        with self.assertRaisesRegex(validation.KernelIterationValidationError, "连接键存在第二条路径"):
            validation._validate_generated_case({"case": case}, "b01-c05", "multi-session-distractor", contract)
        case = ReliabilityFixture.case("b01-c05", "multi-session-distractor")
        case["sessions"][3]["turns"][0]["content"] += " Unit Indigo 05 and Window Quartz 05"
        with self.assertRaisesRegex(validation.KernelIterationValidationError, "双限定存在第二条路径"):
            validation._validate_generated_case({"case": case}, "b01-c05", "multi-session-distractor", contract)

    def test_qualification_uses_two_independent_batches_and_terminal_contains_no_raw_content(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            fixture = ReliabilityFixture(self.suite_root, Path(directory))
            result = fixture.run("qualification")
            self.assertTrue(result["passed"])
            terminal = json.loads(Path(result["result"]).read_text(encoding="utf-8"))
            self.assertEqual(len(terminal["batches"]), 2)
            self.assertTrue(terminal["admission_reliability_passed"])
            self.assertEqual(terminal["candidate_executions"], 0)
            self.assertEqual(terminal["baseline_executions"], 0)
            self.assertFalse(reliability._contains_forbidden_raw_key(terminal))
            self.assertFalse(fixture.scratch_root.exists())
            self.assertEqual(fixture.state_path.read_bytes(), fixture.state_bytes)

    def test_rejected_case_only_is_replaced_then_full_set_is_readmitted_in_original_order(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            fixture = ReliabilityFixture(self.suite_root, Path(directory), rejected_batches={1: {"b01-c01": ("unique_answer",)}})
            result = fixture.run("qualification")
            self.assertTrue(result["passed"])
            terminal = json.loads(Path(result["result"]).read_text(encoding="utf-8"))
            first = terminal["batches"][0]
            self.assertEqual([item["generated_count"] for item in first["replacement_rounds"]], [15, 1])
            self.assertEqual([item["preserved_count"] for item in first["replacement_rounds"]], [0, 14])
            self.assertEqual([item["full_admission_questions"] for item in first["replacement_rounds"]], [15, 15])
            self.assertEqual(fixture.generated_case_ids.count("b01-c01"), 2)
            self.assertEqual(fixture.generated_case_ids.count("b01-c02"), 1)
            self.assertEqual(fixture.generator_calls, 31)
            self.assertEqual(fixture.admission_calls, 3)

    def test_replacement_exhaustion_fails_open_without_starting_second_batch(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            failures = {index: {"b01-c01": ("unique_answer",)} for index in (1, 2, 3)}
            fixture = ReliabilityFixture(self.suite_root, Path(directory), rejected_batches=failures)
            result = fixture.run("qualification")
            self.assertFalse(result["passed"])
            terminal = json.loads(Path(result["result"]).read_text(encoding="utf-8"))
            self.assertEqual(len(terminal["batches"]), 1)
            self.assertEqual(len(terminal["batches"][0]["replacement_rounds"]), 3)
            self.assertEqual(terminal["aggregate_diagnostics"]["failed_by_check"]["unique_answer"], 1)
            self.assertEqual(terminal["next_action"], "continue-same-stage2-root-cause")
            self.assertEqual(fixture.generator_calls, 17)
            self.assertEqual(fixture.admission_calls, 3)

    def test_interrupted_second_batch_resumes_from_aggregate_progress(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            fixture = ReliabilityFixture(self.suite_root, Path(directory), interrupt_on_second_batch=True)
            with self.assertRaises(InterruptedError):
                fixture.run("qualification")
            self.assertTrue(fixture.scratch_root.exists())
            plan_identity = fixture.plan_identity
            self.assertIsNotNone(plan_identity)
            fixture.interrupt_on_second_batch = False
            with fixture.patches():
                resumed = reliability.resume_by_plan_identity(
                    self.suite_root, fixture.output_root, str(plan_identity), invoker=fixture.invoke,
                )
            self.assertTrue(resumed["passed"])
            self.assertEqual(fixture.generator_calls, 31)
            self.assertEqual(fixture.admission_calls, 2)
            self.assertFalse(fixture.scratch_root.exists())

    def test_interrupted_replacement_resumes_from_atomic_round_checkpoint(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            fixture = ReliabilityFixture(
                self.suite_root,
                Path(directory),
                rejected_batches={1: {"b01-c01": ("unique_answer",)}},
                interrupt_on_second_batch=True,
            )
            with self.assertRaises(InterruptedError):
                fixture.run("qualification")
            checkpoint = fixture.scratch_root / "batch-01" / "replacement-checkpoint.json"
            self.assertTrue(checkpoint.is_file())
            fixture.interrupt_on_second_batch = False
            with fixture.patches():
                resumed = reliability.resume_by_plan_identity(
                    self.suite_root, fixture.output_root, str(fixture.plan_identity), invoker=fixture.invoke,
                )
            self.assertTrue(resumed["passed"])
            self.assertEqual(fixture.generated_case_ids.count("b01-c01"), 2)
            self.assertEqual(fixture.generated_case_ids.count("b01-c02"), 1)
            self.assertFalse(fixture.scratch_root.exists())

    def test_terminal_resume_is_zero_execution_and_dependency_drift_fails(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            fixture = ReliabilityFixture(self.suite_root, Path(directory))
            result = fixture.run("diagnosis")
            with fixture.patches():
                reused = reliability.resume_by_plan_identity(self.suite_root, fixture.output_root, result["plan_identity"])
            self.assertTrue(reused["reused"])
            self.assertEqual(reused["model_calls"], 0)
            self.assertEqual(reused["product_executions"], 0)
            with fixture.patches(), mock.patch.object(reliability, "_current_dependencies", return_value={"controller": "f" * 64}):
                with self.assertRaisesRegex(reliability.AdmissionReliabilityError, "直接依赖漂移"):
                    reliability.resume_by_plan_identity(self.suite_root, fixture.output_root, result["plan_identity"])

    @staticmethod
    def _validation_contract() -> dict[str, object]:
        return ReliabilityFixture.validation_contract()

    @staticmethod
    def _materials() -> dict[str, object]:
        cases = [
            ReliabilityFixture.case(f"b01-c{index:02d}", validation.BLIND_COVERAGE[(index - 1) % 5])
            for index in range(1, 16)
        ]
        return reliability._materials(cases)

    @staticmethod
    def _admission_output(materials: dict[str, object], failures: dict[str, tuple[str, ...]]) -> dict[str, object]:
        return ReliabilityFixture.admission_output(materials, failures)


class ReliabilityFixture:
    def __init__(
        self,
        suite_root: Path,
        root: Path,
        *,
        rejected_batches: dict[int, dict[str, tuple[str, ...]]] | None = None,
        interrupt_on_second_batch: bool = False,
    ) -> None:
        self.suite_root = suite_root
        self.output_root = root / "evidence"
        self.runs_root = root / "runs"
        self.runs_root.mkdir(parents=True)
        self.state_path = root / "state.json"
        self.state_bytes = b'{"immutable":true}\n'
        self.state_path.write_bytes(self.state_bytes)
        self.config_path = root / "execution.json"
        self.config_path.write_text("{}\n", encoding="utf-8")
        self.codex_path = root / "codex.exe"
        self.auth_path = root / "auth.json"
        self.codex_path.write_bytes(b"codex")
        self.auth_path.write_text("{}\n", encoding="utf-8")
        self.runtime = {
            "codex_binary": self.codex_path,
            "codex_auth_file": self.auth_path,
            "runs": self.runs_root,
        }
        self.dependencies = {
            "contract": reliability.load_contract(suite_root)["identity"],
            "controller": "1" * 64,
        }
        self.rejected_batches = rejected_batches or {}
        self.interrupt_on_second_batch = interrupt_on_second_batch
        self.generator_calls = 0
        self.generated_case_ids: list[str] = []
        self.admission_calls = 0
        self.plan_identity: str | None = None

    @property
    def scratch_root(self) -> Path:
        return self.runs_root / "kernel-v2-admission-reliability" / str(self.plan_identity)

    def patches(self):
        stack = _PatchStack()
        stack.add(mock.patch.object(validation, "load_validation_contract", return_value=self.validation_contract()))
        stack.add(mock.patch.object(validation, "validate_execution_config", return_value=self.runtime))
        stack.add(mock.patch.object(reliability, "_direct_dependencies", return_value=self.dependencies))
        stack.add(mock.patch.object(reliability, "_current_dependencies", return_value=self.dependencies))
        return stack

    def run(self, mode: str) -> dict[str, object]:
        try:
            with self.patches():
                result = reliability.run(
                    self.suite_root,
                    self.output_root,
                    self.config_path,
                    self.state_path,
                    mode=mode,
                    seed="reliability-fixture-seed-0001",
                    invoker=self.invoke,
                )
            self.plan_identity = str(result["plan_identity"])
            return result
        finally:
            plans = list((self.output_root / "admission-reliability").glob("*/plan.json"))
            if plans:
                self.plan_identity = plans[0].parent.name

    def invoke(self, **kwargs: object) -> tuple[dict[str, object], dict[str, object]]:
        role = str(kwargs["role"])
        if role == "generator":
            self.generator_calls += 1
            if self.interrupt_on_second_batch and self.generator_calls == 16:
                raise InterruptedError("fixture interruption")
            schema = kwargs["schema"]
            properties = schema["properties"]["case"]["properties"]
            case_id = properties["case_id"]["enum"][0]
            coverage = properties["coverage"]["enum"][0]
            self.generated_case_ids.append(case_id)
            return {"case": self.case(case_id, coverage)}, {"calls": 1, "attempts": 1, "wall_seconds": 0.1, "retries": 0, "rate_limit_events": 0}
        self.admission_calls += 1
        batch = self.admission_calls
        prompt = str(kwargs["prompt"])
        materials = json.loads(prompt.split("\n\n", 1)[1])
        return self.admission_output(materials, self.rejected_batches.get(batch, {})), {"calls": 1, "attempts": 1, "wall_seconds": 0.1, "retries": 0, "rate_limit_events": 0}

    @staticmethod
    def admission_output(materials: dict[str, object], failures: dict[str, tuple[str, ...]]) -> dict[str, object]:
        assessments = []
        for case in materials["cases"]:
            failed = set(failures.get(case["case_id"], ()))
            assessments.append({"case_id": case["case_id"], "checks": {name: name not in failed for name in CHECKS}})
        return {"assessments": assessments}

    @staticmethod
    def case(case_id: str, coverage: str) -> dict[str, object]:
        prefix = case_id
        case_number = int(case_id.rsplit("-c", 1)[1])
        session_prefix = f"c{case_number:02d}"
        answer_ids = [f"{session_prefix}-s05"] if coverage == "knowledge-update-conflict" else ([f"{session_prefix}-s01", f"{session_prefix}-s02"] if coverage in {"temporal-order", "multi-session-relation", "multi-session-distractor"} else [f"{session_prefix}-s01"])
        sessions = [
            {
                "session_id": f"{session_prefix}-s{index:02d}",
                "date": f"2025-01-{index:02d}",
                "turns": [{
                    "role": "assistant" if coverage == "single-session-assistant-fact" and index == 1 else "user",
                    "content": f"{prefix} independent evidence statement {index} records value {'alpha' if f'{session_prefix}-s{index:02d}' in answer_ids else 'beta'}."
                }],
            }
            for index in range(1, 7)
        ]
        question = f"Which independently recorded {prefix} value is requested after resolving all evidence?"
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
                {"clue": prefix, "support_session_id": answer_ids[0], "distractor_session_id": f"{session_prefix}-s04"},
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
            "truth_claims": [{"claim": f"{prefix} independent evidence statement", "evidence_session_ids": [session_id]} for session_id in answer_ids],
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
    def validation_contract() -> dict[str, object]:
        return {
            "identity": "a" * 64,
            "blind": {
                "generation": {
                    "model": "generator", "reasoning_effort": "xhigh", "timeout_seconds": 1, "attempts": 1,
                    "max_active": 4, "worker_active_turns_maximum": 1, "result_order": "frozen-coverage-order",
                },
                "quality_admission": {
                    "model": "admission", "reasoning_effort": "medium", "timeout_seconds": 1, "attempts": 1,
                    "required_checks": list(CHECKS),
                },
                "minimum_sessions_per_question": 5,
                "minimum_distractor_sessions_per_question": 2,
            },
        }


class _PatchStack:
    def __init__(self) -> None:
        self._patches: list[object] = []

    def add(self, patcher: object) -> None:
        self._patches.append(patcher)

    def __enter__(self) -> "_PatchStack":
        for patcher in self._patches:
            patcher.start()
        return self

    def __exit__(self, exc_type: object, exc: object, traceback: object) -> None:
        for patcher in reversed(self._patches):
            patcher.stop()


if __name__ == "__main__":
    unittest.main()
