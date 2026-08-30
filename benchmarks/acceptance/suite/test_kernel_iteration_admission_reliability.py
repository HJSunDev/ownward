from __future__ import annotations

import json
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
        prompt = validation._admission_prompt(self._validation_contract(), self._materials())
        self.assertIn("Assess all 15 cases independently", prompt)
        self.assertNotIn("five-case batch", prompt)

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
        with self.assertRaisesRegex(validation.KernelIterationValidationError, "干扰证据泄露"):
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

    def test_default_controller_reuses_one_single_turn_server_per_batch(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            fixture = ReliabilityFixture(self.suite_root, Path(directory))
            opened: list[str] = []

            @contextmanager
            def batch_invoker(_suite_root: Path, _runtime: dict[str, object], transport_parent: Path):
                opened.append(transport_parent.parent.name)
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
            self.assertEqual(opened, ["batch-01", "batch-02"])
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

    def test_qualification_failure_stops_without_extra_batch_and_preserves_aggregate(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            fixture = ReliabilityFixture(self.suite_root, Path(directory), rejected_batches={1: {"b01-c01": ("unique_answer",)}})
            result = fixture.run("qualification")
            self.assertFalse(result["passed"])
            terminal = json.loads(Path(result["result"]).read_text(encoding="utf-8"))
            self.assertEqual(len(terminal["batches"]), 1)
            self.assertEqual(terminal["aggregate_diagnostics"]["failed_by_check"]["unique_answer"], 1)
            self.assertEqual(terminal["next_action"], "continue-same-stage2-root-cause")

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
            "truth_claims": [{"claim": f"{prefix} independent evidence statement", "evidence_session_ids": [session_id]} for session_id in answer_ids],
            "sessions": sessions,
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
                "generation": {"model": "generator", "reasoning_effort": "medium", "timeout_seconds": 1, "attempts": 1},
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
