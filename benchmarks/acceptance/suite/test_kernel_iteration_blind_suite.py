from __future__ import annotations

import json
from pathlib import Path
import sys
import tempfile
import threading
import unittest
from unittest import mock

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

import kernel_iteration_blind_suite as suite
import kernel_iteration_validation as validation


class BlindVersionSuiteTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.suite_root = Path(__file__).resolve().parent

    def test_contract_freezes_one_complete_version_suite_before_candidates(self) -> None:
        contract = suite.load_contract(self.suite_root)
        specs = suite._suite_specs(contract)
        self.assertEqual((5, 15, 25, 50), tuple(contract["levels"]))
        self.assertEqual(95, len(specs))
        self.assertEqual(95, len({item["case_id"] for item in specs}))
        self.assertTrue(contract["isolation"]["candidate_inputs_forbidden_during_preparation"])
        self.assertEqual(1, contract["lifecycle"]["active_suites_per_major_version_maximum"])
        self.assertEqual(8, contract["generation"]["max_active"])
        self.assertEqual(15, contract["quality_admission"]["batch_questions_maximum"])

    def test_prepare_replaces_only_rejected_case_seals_once_and_resumes_without_work(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            fixture = _PreparationFixture(self.suite_root, Path(temporary), reject_once=True)
            before = fixture.state_path.read_bytes()
            reference = fixture.prepare()
            self.assertTrue(reference["passed"])
            self.assertEqual(96, fixture.generator_calls)
            self.assertEqual(8, fixture.admission_calls)
            self.assertEqual(before, fixture.state_path.read_bytes())

            receipt_path = fixture.output_root / "blind-suite" / "v2" / "suite-receipt.json"
            receipt = json.loads(receipt_path.read_text(encoding="utf-8"))
            self.assertEqual(95, receipt["questions"])
            self.assertEqual(1, receipt["replacement_rounds"][1]["generated_count"])
            self.assertEqual(94, receipt["replacement_rounds"][1]["preserved_count"])
            self.assertEqual(1, receipt["replacement_rounds"][1]["accepted_count"])
            self.assertEqual(0, receipt["quality_admission"]["rejected_count"])
            self.assertEqual(0, receipt["candidate_executions"])
            self.assertEqual(0, receipt["baseline_executions"])

            sealed_path = fixture.vault_root / "v2" / reference["suite_identity"] / "suite.json"
            sealed = json.loads(sealed_path.read_text(encoding="utf-8"))
            self.assertEqual(95, len(sealed["cases"]))
            self.assertEqual(95, len(sealed["slot_certificates"]))
            self.assertIn("question", sealed["cases"][0])
            self._assert_no_raw_content(fixture.output_root)

            calls = (fixture.generator_calls, fixture.admission_calls)
            with mock.patch.object(suite, "_load_current_admission_qualification", return_value=fixture.qualification), \
                    mock.patch.object(suite, "_preparation_dependencies", return_value=fixture.dependencies), \
                    mock.patch.object(suite, "_validate_suite_case", side_effect=fixture._validate_case):
                reused = suite.resume_by_plan_identity(self.suite_root, fixture.output_root, reference["plan_identity"])
            self.assertTrue(reused["reused"])
            self.assertEqual(0, reused["model_calls"])
            self.assertEqual(0, reused["product_executions"])
            self.assertEqual(calls, (fixture.generator_calls, fixture.admission_calls))
            self.assertEqual(before, fixture.state_path.read_bytes())

            with mock.patch.object(suite, "_validate_suite_case", side_effect=fixture._validate_case):
                opened = suite.open_partition_for_evaluation(
                    self.suite_root, fixture.output_root, fixture.vault_root,
                    major_version="v2", suite_identity=reference["suite_identity"], level=5,
                )
            self.assertEqual(5, len(opened["materials"]["cases"]))
            with self.assertRaises(suite.BlindSuiteError):
                suite.open_partition_for_evaluation(
                    self.suite_root, fixture.output_root, fixture.vault_root,
                    major_version="v3", suite_identity=reference["suite_identity"], level=5,
                )
            with self.assertRaises(suite.BlindSuiteError):
                fixture.prepare(seed="different-version-seed-12345")

            retired = suite.retire(
                self.suite_root, fixture.output_root, fixture.vault_root,
                major_version="v2", suite_identity=reference["suite_identity"],
            )
            self.assertEqual("retired", retired["status"])
            with self.assertRaises(suite.BlindSuiteError):
                suite.open_partition_for_evaluation(
                    self.suite_root, fixture.output_root, fixture.vault_root,
                    major_version="v2", suite_identity=reference["suite_identity"], level=5,
                )

    def test_current_cli_has_only_version_suite_production_path(self) -> None:
        source = (self.suite_root / "kernel_iteration_run.py").read_text(encoding="utf-8")
        self.assertNotIn("--blind-gate-config", source)
        self.assertNotIn("--blind-gate-plan-identity", source)
        self.assertIn("--blind-suite-prepare", source)
        self.assertIn("--blind-suite-evaluation-batch", source)
        self.assertIn("--blind-suite-previous-adjudication", source)
        self.assertIn("--blind-suite-qualify-admission", source)
        self.assertIn("kernel_iteration_blind_suite.prepare", source)
        self.assertIn("kernel_iteration_blind_suite.run_partition", source)

    def test_preparation_identity_has_no_candidate_or_git_dependency(self) -> None:
        contract = suite.load_contract(self.suite_root)
        validation_contract = validation.load_validation_contract(self.suite_root)
        runtime = {"external_intelligence": {
            "driver": "codex-app-server/v1",
            "provider": "openai-codex",
            "binary": Path(__file__),
            "credential_file": Path(__file__),
        }}
        qualification = {"identity": "9" * 64}
        dependencies = suite._preparation_dependencies(self.suite_root, contract, validation_contract, runtime, qualification)
        self.assertFalse(any("candidate" in name or "git" in name for name in dependencies))
        plan = suite._plan_content("v2", contract, dependencies, "stable-version-seed-12345")
        self.assertIsNone(plan["candidate_identity"])
        self.assertIsNone(plan["candidate_output"])

    def test_execution_changes_do_not_invalidate_suite_preparation_and_frozen_contract_opens(self) -> None:
        contract = suite.load_contract(self.suite_root)
        changed = json.loads(json.dumps(contract))
        changed["execution"]["absolute_gate"]["read_limit"] = 7
        self.assertEqual(suite._preparation_contract_identity(contract), suite._preparation_contract_identity(changed))
        self.assertNotEqual(contract["identity"], suite.evidence.canonical_sha256({key: value for key, value in changed.items() if key != "identity"}))

    def test_incomplete_admission_preserves_slots_and_resume_only_regenerates_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            fixture = _PreparationFixture(self.suite_root, Path(temporary), reject_once=False, reject_attempts=3)
            first = fixture.prepare()
            self.assertFalse(first["passed"])
            self.assertEqual("quality-admission-incomplete-resume-rejected-or-missing-slots", first["status"])
            self.assertEqual(97, fixture.generator_calls)
            scratch = fixture.vault_root / "v2" / ".preparing" / first["plan_identity"]
            self.assertTrue((scratch / "preparation-state.json").is_file())
            accepted_certificate = scratch / "slots" / "vs-c002" / "certificate.json"
            certificate_identity = json.loads(accepted_certificate.read_text(encoding="utf-8"))["identity"]
            fixture.reject_attempts = 0
            with fixture.patches():
                second = suite.prepare(
                    self.suite_root, fixture.output_root, fixture.vault_root,
                    fixture.execution_config, fixture.state_path,
                    major_version="v2", plan_identity=first["plan_identity"], resume=True,
                    invokers=fixture.invokers,
                )
            self.assertTrue(second["passed"])
            self.assertEqual(98, fixture.generator_calls)
            sealed = json.loads((fixture.vault_root / "v2" / second["suite_identity"] / "suite.json").read_text(encoding="utf-8"))
            by_slot = {item["slot_id"]: item["identity"] for item in sealed["slot_certificates"]}
            self.assertEqual(certificate_identity, by_slot["vs-c002"])

    def test_sealed_suite_uses_frozen_contract_snapshot(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            fixture = _PreparationFixture(self.suite_root, Path(temporary), reject_once=False)
            reference = fixture.prepare()
            with mock.patch.object(suite, "load_contract", side_effect=suite.BlindSuiteError("current contract changed")), \
                    mock.patch.object(suite, "_validate_suite_case", side_effect=fixture._validate_case):
                opened = suite.open_partition_for_evaluation(
                    self.suite_root, fixture.output_root, fixture.vault_root,
                    major_version="v2", suite_identity=reference["suite_identity"], level=5,
                )
            self.assertEqual(5, len(opened["materials"]["cases"]))

    def test_evaluation_batch_accepts_arbitrary_future_version_and_explicit_baseline(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            candidate = root / "candidate.json"
            baseline = root / "baseline.json"
            candidate.write_text('{"kind":"v3-candidate"}\n', encoding="utf-8")
            baseline.write_text('{"kind":"v2-baseline"}\n', encoding="utf-8")
            candidate_config = root / "candidate-execution.json"
            baseline_config = root / "baseline-execution.json"
            candidate_config.write_text('{"config":"candidate"}\n', encoding="utf-8")
            baseline_config.write_text('{"config":"baseline"}\n', encoding="utf-8")
            candidate_freeze = root / "candidate-freeze.json"
            candidate_freeze.write_text(json.dumps({"identity": "5" * 64}) + "\n", encoding="utf-8")
            baseline_freeze = root / "baseline-freeze.json"
            baseline_freeze.write_text(json.dumps({"identity": "6" * 64}) + "\n", encoding="utf-8")
            freeze_content = {
                "schema": suite.EVALUATION_FREEZE_SCHEMA,
                "major_version": "v3",
                "suite_identity": "8" * 64,
                "candidate_subject_identity": "3" * 64,
                "baseline_subject_identity": "2" * 64,
                "source_freezes": [
                    {"role": "candidate", "path": str(candidate_freeze), "sha256": suite.evidence.file_sha256(candidate_freeze), "identity": "5" * 64},
                    {"role": "baseline", "path": str(baseline_freeze), "sha256": suite.evidence.file_sha256(baseline_freeze), "identity": "6" * 64},
                ],
            }
            freeze = {**freeze_content, "identity": suite.evidence.canonical_sha256(freeze_content)}
            freeze_path = root / "freeze.json"
            freeze_path.write_text(json.dumps(freeze) + "\n", encoding="utf-8")
            content = {
                "schema": suite.EVALUATION_BATCH_SCHEMA,
                "major_version": "v3",
                "suite_identity": "8" * 64,
                "candidate": {
                    "subject_manifest": str(candidate), "subject_manifest_sha256": suite.evidence.file_sha256(candidate),
                    "subject_identity": "3" * 64,
                    "execution_config": str(candidate_config), "execution_config_sha256": suite.evidence.file_sha256(candidate_config),
                },
                "baseline": {
                    "subject_manifest": str(baseline), "subject_manifest_sha256": suite.evidence.file_sha256(baseline),
                    "subject_identity": "2" * 64,
                    "execution_config": str(baseline_config), "execution_config_sha256": suite.evidence.file_sha256(baseline_config),
                },
                "freeze_receipt": {"path": str(freeze_path), "sha256": suite.evidence.file_sha256(freeze_path), "identity": freeze["identity"]},
            }
            batch = {**content, "identity": suite.evidence.canonical_sha256(content)}
            path = root / "batch.json"
            path.write_text(json.dumps(batch) + "\n", encoding="utf-8")
            loaded = suite.load_evaluation_batch(path, "v3", "8" * 64)
            self.assertEqual("2" * 64, loaded["baseline"]["subject_identity"])
            contract = suite.load_contract(self.suite_root)
            level = suite.level_contract(contract, 5)
            self.assertNotIn("v0", json.dumps(contract).lower())
            self.assertNotIn("v0", json.dumps(level).lower())
            self.assertIn("relative_baseline_gate", level)

            freeze["baseline_subject_identity"] = "4" * 64
            frozen_content = {key: item for key, item in freeze.items() if key != "identity"}
            freeze["identity"] = suite.evidence.canonical_sha256(frozen_content)
            freeze_path.write_text(json.dumps(freeze) + "\n", encoding="utf-8")
            content["freeze_receipt"] = {"path": str(freeze_path), "sha256": suite.evidence.file_sha256(freeze_path), "identity": freeze["identity"]}
            changed = {**content, "identity": suite.evidence.canonical_sha256(content)}
            path.write_text(json.dumps(changed) + "\n", encoding="utf-8")
            with self.assertRaisesRegex(suite.BlindSuiteError, "baseline subject"):
                suite.load_evaluation_batch(path, "v3", "8" * 64)

    def test_evaluation_subject_accepts_only_exact_frozen_contract_subject(self) -> None:
        comparison = suite.evidence.load_contract(self.suite_root)
        baseline = suite.evidence.select_subject(comparison, "v0")
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "baseline-subject.json"
            path.write_text(json.dumps(baseline) + "\n", encoding="utf-8")
            loaded = suite._load_evaluation_subject(comparison, path)
            self.assertEqual("evaluation-baseline", loaded["role"])
            self.assertEqual(baseline["identity"], loaded["identity"])

            tampered = json.loads(json.dumps(baseline))
            tampered["content"]["formal_evaluation_baseline"] = False
            path.write_text(json.dumps(tampered) + "\n", encoding="utf-8")
            with self.assertRaisesRegex(suite.BlindSuiteError, "封存内容漂移"):
                suite._load_evaluation_subject(comparison, path)

    def test_bounded_confirmation_continuation_binds_source_batch_and_candidate(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary)
            suite_identity = "1" * 64
            candidate_identity = "2" * 64
            previous_plan_identity = "3" * 64
            evaluation_batch_identity = "4" * 64
            root = output / "blind-suite-runs" / suite_identity / candidate_identity / previous_plan_identity
            root.mkdir(parents=True)
            (root / "plan.json").write_text(json.dumps({
                "schema": suite.EXECUTION_PLAN_SCHEMA,
                "suite_identity": suite_identity,
                "candidate_subject_identity": candidate_identity,
                "level": 15,
            }) + "\n", encoding="utf-8")
            result_content = {
                "schema": suite.EXECUTION_RESULT_SCHEMA,
                "plan_identity": previous_plan_identity,
                "status": "candidate-rejected",
                "passed": False,
                "candidate_decision": False,
                "baseline_execution": None,
                "absolute_decision": {
                    "failures": [{"metric": "retrieval_p95_confirmation_required", "actual": 900.0, "required": 553.0}],
                    "retrieval_distribution": {
                        "status": "bounded-confirmation-required",
                        "candidate_failure": False,
                    },
                },
                "formal_state_written": False,
                "contains_reversible_question_answer_evidence_or_case_ids": False,
            }
            result = {**result_content, "identity": suite.evidence.canonical_sha256(result_content)}
            (root / "result.json").write_text(json.dumps(result) + "\n", encoding="utf-8")
            continuation_content = {
                "schema": suite.PARTITION_CONTINUATION_SCHEMA,
                "suite_identity": suite_identity,
                "evaluation_batch_identity": evaluation_batch_identity,
                "candidate_subject_identity": candidate_identity,
                "source_plan_identity": previous_plan_identity,
                "source_result_identity": result["identity"],
                "source_level": 15,
                "next_level": 25,
                "decision": "continue-same-candidate-after-bounded-confirmation",
                "reason": "same-dependency-independent-sufficient-sample-distribution-below-frozen-ceiling",
                "same_frozen_dependencies": True,
                "quality_trace_complete": True,
                "hard_timeout_or_execution_error_count": 0,
                "formal_state_byte_identical": True,
                "contains_reversible_question_answer_evidence_or_case_ids": False,
            }
            continuation = {**continuation_content, "identity": suite.evidence.canonical_sha256(continuation_content)}
            evidence_content = {"schema": "fixture-terminal-evidence/v1", "partition_continuation": continuation}
            terminal = {**evidence_content, "identity": suite.evidence.canonical_sha256(evidence_content)}
            terminal_path = output / "terminal-evidence.json"
            terminal_path.write_text(json.dumps(terminal) + "\n", encoding="utf-8")
            contract = suite.level_contract(suite.load_contract(self.suite_root), 25)

            with self.assertRaisesRegex(suite.BlindSuiteError, "缺少独立裁决"):
                suite._previous_partition_result(
                    output, suite_identity, candidate_identity, contract, previous_plan_identity,
                    evaluation_batch_identity=evaluation_batch_identity,
                )
            decision = suite._previous_partition_result(
                output, suite_identity, candidate_identity, contract, previous_plan_identity,
                adjudication_path=terminal_path, evaluation_batch_identity=evaluation_batch_identity,
            )
            self.assertEqual(result["identity"], decision["result_identity"])
            self.assertEqual(continuation["identity"], decision["continuation_identity"])
            with self.assertRaisesRegex(suite.BlindSuiteError, "evaluation_batch_identity 错绑"):
                suite._previous_partition_result(
                    output, suite_identity, candidate_identity, contract, previous_plan_identity,
                    adjudication_path=terminal_path, evaluation_batch_identity="5" * 64,
                )

    def test_execution_scratch_is_short_suite_bound_and_exactly_cleaned(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            runs = Path(temporary) / ("persistent-runs-" + "x" * 80)
            suite_identity = "7" * 64
            plan_identity = "8" * 64
            path = suite._execution_scratch_path(runs, suite_identity, plan_identity)
            other = suite._execution_scratch_path(runs, "6" * 64, plan_identity)
            self.assertNotEqual(path, other)
            self.assertEqual("kvs", path.parent.name)
            old = runs / "kernel-version-blind-suite" / suite_identity / plan_identity
            self.assertGreater(len(str(old)) - len(str(path)), 60)
            path.mkdir(parents=True)
            (path / "private.json").write_text("{}\n", encoding="utf-8")
            with self.assertRaisesRegex(suite.BlindSuiteError, "之外"):
                suite._destroy_execution_scratch(path, runs, "6" * 64, plan_identity)
            suite._destroy_execution_scratch(path, runs, suite_identity, plan_identity)
            self.assertFalse(path.exists())

    def test_public_suite_evidence_has_no_v0_baseline_assumption(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            fixture = _PreparationFixture(self.suite_root, Path(temporary), reject_once=False)
            fixture.prepare()
            receipt = json.loads((fixture.output_root / "blind-suite" / "v2" / "suite-receipt.json").read_text(encoding="utf-8"))
            self.assertNotIn("v0", json.dumps(receipt).lower())

    def _assert_no_raw_content(self, root: Path) -> None:
        forbidden = {"question", "answer", "case_id", "sessions", "truth_claims"}

        def visit(value: object) -> None:
            if isinstance(value, dict):
                self.assertTrue(forbidden.isdisjoint(value))
                for item in value.values():
                    visit(item)
            elif isinstance(value, list):
                for item in value:
                    visit(item)

        for path in root.rglob("*.json"):
            visit(json.loads(path.read_text(encoding="utf-8")))


class _PreparationFixture:
    def __init__(self, suite_root: Path, root: Path, *, reject_once: bool, reject_attempts: int | None = None) -> None:
        self.suite_root = suite_root
        self.output_root = root / "public"
        self.vault_root = root / "vault"
        self.state_path = root / "state.json"
        self.state_path.write_bytes(b'{"immutable":true}\n')
        self.codex_binary = root / "codex.exe"
        self.codex_binary.write_bytes(b"fixture-codex")
        self.auth_file = root / "auth.json"
        self.auth_file.write_text("{}\n", encoding="utf-8")
        self.execution_config = root / "execution.json"
        self.execution_config.write_text(json.dumps({
            "schema": "ownward.acceptance-execution/v3",
            "community": {
                "codex_binary": str(self.codex_binary),
                "codex_auth_file": str(self.auth_file),
                "protocol": str(Path(__file__).parents[2] / "longmemeval_s" / "protocol.json"),
            },
        }) + "\n", encoding="utf-8")
        self.contract = suite.load_contract(suite_root)
        self.spec_by_id = {item["case_id"]: item for item in suite._suite_specs(self.contract)}
        self.required_checks = list(validation.load_validation_contract(suite_root)["blind"]["quality_admission"]["required_checks"])
        self.dependencies = {
            "suite-contract": self.contract["identity"],
            "generator": "1" * 64,
            "quality-admission": "2" * 64,
            "controller": "3" * 64,
            "codex-executor": "4" * 64,
        }
        self.qualification = {"identity": "9" * 64, "passed": True}
        self.reject_once = reject_once
        self.reject_attempts = 1 if reject_once else (reject_attempts or 0)
        self.rejected = False
        self.generator_calls = 0
        self.admission_calls = 0
        self.lock = threading.Lock()
        self.invokers = [self._invoke for _ in range(8)]

    def prepare(self, *, seed: str = "version-suite-fixture-seed-12345") -> dict[str, object]:
        with self.patches():
            return suite.prepare(
                self.suite_root, self.output_root, self.vault_root,
                self.execution_config, self.state_path,
                major_version="v2", seed=seed, invokers=self.invokers,
            )

    def patches(self):
        return _PatchStack((
            mock.patch.object(suite, "_load_current_admission_qualification", return_value=self.qualification),
            mock.patch.object(suite, "_preparation_dependencies", return_value=self.dependencies),
            mock.patch.object(suite, "_implementation_identity", return_value={
                "generator": "a" * 64, "quality-admission": "b" * 64,
                "controller": "c" * 64, "execution-controller": "d" * 64,
            }),
            mock.patch.object(suite, "_validate_generated_case", side_effect=self._validate_generated),
            mock.patch.object(suite, "_validate_suite_case", side_effect=self._validate_case),
            mock.patch.object(suite, "_validate_material_isolation"),
            mock.patch.object(suite, "_admission_prompt", return_value="admit-only-current-generated-slots"),
            mock.patch.object(validation, "_admission_review_materials", side_effect=lambda materials, generated: materials),
        ))

    def _invoke(self, **kwargs: object) -> tuple[dict[str, object], dict[str, object]]:
        role = str(kwargs["role"])
        if role == "generator":
            case_id = Path(kwargs["stage"]).name
            with self.lock:
                self.generator_calls += 1
            return {"case": {"case_id": case_id}}, self._usage()
        case_ids = kwargs["schema"]["properties"]["assessments"]["items"]["properties"]["case_id"]["enum"]
        with self.lock:
            self.admission_calls += 1
            reject = self.reject_attempts > 0 and "vs-c001" in case_ids
            if reject:
                self.reject_attempts -= 1
                self.rejected = True
        assessments = []
        for case_id in case_ids:
            checks = {name: True for name in self.required_checks}
            if reject and case_id == "vs-c001":
                checks[self.required_checks[0]] = False
            assessments.append({"case_id": case_id, "checks": checks})
        return {"assessments": assessments}, self._usage()

    def _validate_generated(self, output: dict[str, object], spec: dict[str, object], _contract: dict[str, object]) -> dict[str, object]:
        self.assert_equal(output["case"]["case_id"], spec["case_id"])
        sessions = [
            {
                "session_id": f"{spec['case_id']}-s{index:02d}",
                "date": f"2035-01-{index:02d}",
                "turns": [{"role": "user", "content": f"Independent fact {spec['case_id']} {index}."}],
            }
            for index in range(1, int(spec["session_count"]) + 1)
        ]
        return {
            "case_id": spec["case_id"],
            "coverage": spec["primary"],
            "question_type": "fixture",
            "question_date": "2035-02-01",
            "question": f"Which independent result belongs to {spec['case_id']}?",
            "answer": f"result-{spec['case_id']}",
            "answer_session_ids": [sessions[0]["session_id"]],
            "stale_session_ids": [],
            "distractor_session_ids": [sessions[-2]["session_id"], sessions[-1]["session_id"]],
            "truth_claims": [{"claim": f"result-{spec['case_id']}", "evidence_session_ids": [sessions[0]["session_id"]]}],
            "sessions": sessions,
            "suite_profile": dict(spec),
            "_mechanical_admission_proof": {"schema": "ownward.kernel-iteration-blind-mechanical-admission-proof/v1"},
        }

    @staticmethod
    def _validate_case(case: dict[str, object]) -> None:
        profile = case["suite_profile"]
        if case["case_id"] != profile["case_id"] or len(case["sessions"]) != profile["session_count"]:
            raise suite.BlindSuiteError("fixture case/profile mismatch")

    @staticmethod
    def assert_equal(left: object, right: object) -> None:
        if left != right:
            raise suite.BlindSuiteError("fixture generator identity mismatch")

    @staticmethod
    def _usage() -> dict[str, object]:
        return {
            "input_tokens": 10, "cached_input_tokens": 0, "output_tokens": 5,
            "reasoning_output_tokens": 1, "calls": 1, "attempts": 1,
            "retries": 0, "rate_limit_events": 0, "interrupted_attempts": 0,
            "wall_seconds": 0.01, "elapsed_seconds": 0.01,
        }


class _PatchStack:
    def __init__(self, patches: tuple[object, ...]) -> None:
        self.patches = patches

    def __enter__(self) -> "_PatchStack":
        for patch in self.patches:
            patch.start()
        return self

    def __exit__(self, exc_type: object, exc: object, traceback: object) -> None:
        for patch in reversed(self.patches):
            patch.stop()


if __name__ == "__main__":
    unittest.main()
