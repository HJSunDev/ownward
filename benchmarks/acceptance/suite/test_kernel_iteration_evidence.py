from __future__ import annotations

import json
import copy
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import kernel_iteration_evidence as iteration
import evidence_identity
import identity_fixtures
import lifecycle
from contract import load_contract as load_formal_contract


class KernelIterationEvidenceTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.suite_root = Path(__file__).resolve().parent
        cls.repository = cls.suite_root.parents[2]
        cls.contract = iteration.load_contract(cls.suite_root)

    def test_contract_freezes_three_dimensions_before_any_v2_result(self) -> None:
        self.assertTrue(self.contract["unchanged_dimensions_frozen_before_v2_results"])
        self.assertTrue(self.contract["latency_correction_frozen_before_new_candidate_measurement"])
        self.assertTrue(self.contract["candidate_results_excluded_from_latency_correction"])
        self.assertEqual(
            {
                "information-organization-quality",
                "retrieval-and-final-answer-quality",
                "end-to-end-efficiency",
            },
            set(self.contract["dimensions"]),
        )
        self.assertNotIn("result", self.contract)
        self.assertNotIn("candidate_result", self.contract)
        self.assertEqual(
            self.contract["latency_policy_migration"]["from_identity"],
            self.contract["identity"],
        )
        self.assertEqual(
            self.contract["policy_revision_identity"],
            iteration.canonical_sha256(iteration._contract_policy_content(self.contract)),
        )
        active_names = {
            metric["name"]
            for dimension in self.contract["dimensions"].values()
            for metric in dimension["metrics"]
        }
        self.assertNotIn("retrieval_mean_ms", active_names)
        self.assertNotIn("retrieval_p95_ms", active_names)
        self.assertIn("complete_consumer_retrieval_p95_ms", active_names)
        self.assertEqual(
            "diagnostic-only-not-a-complete-consumer-non-regression-gate",
            self.contract["historical_latency_diagnostics"]["status"],
        )
        self.assertTrue(self.contract["subjects"]["v0"]["formal_evaluation_baseline"])
        self.assertEqual(
            "ownward.kernel-iteration-baseline-facts/v1",
            json.loads((self.suite_root / "iteration/v2/v0-baseline-facts.json").read_text(encoding="utf-8"))["schema"],
        )
        self.assertFalse(any(".tmp" in Path(item["path"]).parts for item in self.contract["sources"].values()))
        formal = json.loads((self.suite_root / "contract.json").read_text(encoding="utf-8"))
        self.assertNotIn("kernel-iteration", formal["execution"]["modes"])
        self.assertNotIn("iteration", formal["evidence_layers"])

    def test_versioned_contract_loads_without_any_runtime_tmp_input(self) -> None:
        with tempfile.TemporaryDirectory(dir=self.repository) as temporary:
            clean_repository = Path(temporary) / "clean-checkout"
            relative_files = [
                Path("benchmarks/acceptance/suite/contract.json"),
                Path("benchmarks/acceptance/suite/iteration/v2/comparison-contract.json"),
                *(Path(item["path"]) for item in self.contract["sources"].values()),
            ]
            for relative in relative_files:
                target = clean_repository / relative
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copy2(self.repository / relative, target)
            clean_suite = clean_repository / "benchmarks/acceptance/suite"
            original = iteration._load_json

            def tracked_only(path: Path) -> dict[str, object]:
                if ".tmp" in path.parts:
                    raise AssertionError(f"版本化合同读取了运行态路径: {path}")
                return original(path)

            with mock.patch.object(iteration, "_load_json", side_effect=tracked_only):
                loaded = iteration.load_contract(clean_suite)
            self.assertEqual(self.contract["identity"], loaded["identity"])

    def test_v0_current_and_synthetic_v2_are_independent_subjects(self) -> None:
        with tempfile.TemporaryDirectory(dir=self.repository) as temporary:
            root = Path(temporary)
            v0 = iteration.run(self.suite_root, root / "evidence", selector="v0")
            current = iteration.run(self.suite_root, root / "evidence", selector="current-product")
            manifest = self._write_subject(root / "v2.json")
            v2 = iteration.run(self.suite_root, root / "evidence", subject_manifest=manifest)
            self.assertEqual(3, len({v0["subject_identity"], current["subject_identity"], v2["subject_identity"]}))
            self.assertNotEqual(v0["evidence_root"], current["evidence_root"])
            self.assertNotEqual(current["evidence_root"], v2["evidence_root"])

    def test_public_cli_uses_the_same_versioned_nonformal_entry(self) -> None:
        with tempfile.TemporaryDirectory(dir=self.repository) as temporary:
            output = Path(temporary) / "evidence"
            completed = subprocess.run(
                [
                    sys.executable,
                    str(self.suite_root / "kernel_iteration_run.py"),
                    "--output",
                    str(output),
                    "--subject",
                    "v0",
                ],
                cwd=self.repository,
                capture_output=True,
                text=True,
                encoding="utf-8",
                timeout=30,
                check=False,
            )
            self.assertEqual(0, completed.returncode, completed.stderr)
            result = json.loads(completed.stdout)
            self.assertFalse(result["formal"])
            self.assertEqual("identity-calibration", result["evidence_type"])

    def test_v1_specific_cli_is_retired_instead_of_remaining_a_second_path(self) -> None:
        with tempfile.TemporaryDirectory(dir=self.repository) as temporary:
            completed = subprocess.run(
                [
                    sys.executable,
                    str(self.suite_root / "run.py"),
                    "kernel-iteration",
                    "--formal-run",
                    temporary,
                    "--output",
                    str(Path(temporary) / "output"),
                    "--candidate",
                    "worktree",
                ],
                cwd=self.repository,
                capture_output=True,
                text=True,
                encoding="utf-8",
                timeout=30,
                check=False,
            )
            self.assertNotEqual(0, completed.returncode)
            self.assertIn("已退出生产入口", completed.stderr)

    def test_direct_dependency_change_only_creates_new_v2_evidence(self) -> None:
        with tempfile.TemporaryDirectory(dir=self.repository) as temporary:
            root = Path(temporary)
            evidence = root / "evidence"
            v0 = iteration.run(self.suite_root, evidence, selector="v0")
            v0_result = Path(v0["evidence_root"]) / "result.json"
            v0_bytes = v0_result.read_bytes()
            first = iteration.run(self.suite_root, evidence, subject_manifest=self._write_subject(root / "v2-a.json"))
            second = iteration.run(
                self.suite_root,
                evidence,
                subject_manifest=self._write_subject(root / "v2-b.json", semantic="9" * 64),
            )
            self.assertNotEqual(first["subject_identity"], second["subject_identity"])
            self.assertNotEqual(first["evidence_root"], second["evidence_root"])
            self.assertEqual(v0_bytes, v0_result.read_bytes())

        changed_registry = copy.deepcopy(self.contract)
        changed_registry["subjects"]["current-product"]["direct_dependencies"]["access"] = "0" * 64
        self.assertEqual(
            self.contract["policy_revision_identity"],
            iteration.canonical_sha256(iteration._contract_policy_content(changed_registry)),
        )
        self.assertNotEqual(
            self.contract["seal_sha256"],
            iteration.canonical_sha256(iteration._contract_seal_content(changed_registry)),
        )

    def test_pair_comparison_allows_only_the_subject_to_differ(self) -> None:
        with tempfile.TemporaryDirectory(dir=self.repository) as temporary:
            root = Path(temporary)
            left = iteration.run(self.suite_root, root / "evidence", selector="v0")
            right = iteration.run(
                self.suite_root,
                root / "evidence",
                subject_manifest=self._write_subject(root / "v2.json"),
            )
            left_plan = Path(left["evidence_root"]) / "plan.json"
            right_plan = Path(right["evidence_root"]) / "plan.json"
            pair = iteration.compare_plans(left_plan, right_plan)
            self.assertEqual(left["contract_identity"], pair["contract_identity"])

            changed = json.loads(right_plan.read_text(encoding="utf-8"))
            changed["direct_dependencies"]["condition:environment"] = "8" * 64
            content = {key: item for key, item in changed.items() if key != "identity"}
            changed["identity"] = iteration.canonical_sha256(content)
            changed_path = root / "changed-plan.json"
            changed_path.write_text(json.dumps(changed), encoding="utf-8")
            with self.assertRaises(iteration.KernelIterationEvidenceError):
                iteration.compare_plans(left_plan, changed_path)

    def test_git_audit_change_does_not_change_candidate_or_plan_identity(self) -> None:
        with tempfile.TemporaryDirectory(dir=self.repository) as temporary:
            root = Path(temporary)
            evidence = root / "evidence"
            first_manifest = self._write_subject(root / "first.json", audit_git="1" * 40)
            first = iteration.run(self.suite_root, evidence, subject_manifest=first_manifest)
            second_manifest = self._write_subject(root / "second.json", audit_git="2" * 40)
            second = iteration.run(self.suite_root, evidence, subject_manifest=second_manifest, resume=True)
            self.assertEqual(first["subject_identity"], second["subject_identity"])
            self.assertEqual(first["plan_identity"], second["plan_identity"])
            self.assertEqual(["subject-selected", "evidence-prepared"], second["reused_checkpoints"])

        changed_contract = copy.deepcopy(self.contract)
        changed_contract["subjects"]["v0"]["audit_source_git"] = "f" * 40
        iteration.validate_contract(self.suite_root, changed_contract)
        self.assertEqual(self.contract["identity"], changed_contract["identity"])

    def test_interrupted_nonformal_evidence_resumes_from_atomic_checkpoint(self) -> None:
        with tempfile.TemporaryDirectory(dir=self.repository) as temporary:
            root = Path(temporary)
            first = iteration.run(self.suite_root, root / "evidence", selector="v0")
            evidence_root = Path(first["evidence_root"])
            subject_checkpoint = evidence_root / "checkpoints" / "subject-selected.json"
            subject_bytes = subject_checkpoint.read_bytes()
            (evidence_root / "checkpoints" / "evidence-prepared.json").unlink()
            (evidence_root / "result.json").unlink()
            resumed = iteration.run(self.suite_root, root / "evidence", selector="v0", resume=True)
            self.assertEqual(["subject-selected"], resumed["reused_checkpoints"])
            self.assertEqual(subject_bytes, subject_checkpoint.read_bytes())
            self.assertTrue((evidence_root / "checkpoints" / "evidence-prepared.json").is_file())
            self.assertTrue((evidence_root / "result.json").is_file())

    def test_runtime_calibration_is_explicit_read_only_and_resumable(self) -> None:
        with tempfile.TemporaryDirectory(dir=self.repository) as temporary:
            root = Path(temporary)
            state_path = self._write_runtime_fixture(root)
            output = root / "evidence"
            before = state_path.read_bytes()
            first = iteration.calibrate_runtime(self.suite_root, output, state_path)
            second = iteration.calibrate_runtime(self.suite_root, output, state_path, resume=True)
            self.assertEqual(first["runtime_calibration_identity"], second["runtime_calibration_identity"])
            self.assertTrue(second["reused"])
            sealed = json.loads((Path(first["evidence_root"]) / "result.json").read_text(encoding="utf-8"))
            self.assertEqual(sealed["state_sha256_before"], sealed["state_sha256_after"])
            self.assertFalse(sealed["formal_state_written"])
            self.assertEqual(before, state_path.read_bytes())

    def test_runtime_calibration_rejects_invalid_runtime_drift_without_output(self) -> None:
        with tempfile.TemporaryDirectory(dir=self.repository) as temporary:
            root = Path(temporary)
            state_path = self._write_runtime_fixture(root)
            state = json.loads(state_path.read_text(encoding="utf-8"))
            state["schema"] = "ownward.acceptance-state/tampered"
            state_path.write_text(json.dumps(state), encoding="utf-8")
            output = root / "evidence"
            with self.assertRaises(iteration.KernelIterationEvidenceError):
                iteration.calibrate_runtime(self.suite_root, output, state_path)
            self.assertFalse(output.exists())

    def test_stage_five_runtime_rebind_does_not_change_policy_or_existing_evidence(self) -> None:
        with tempfile.TemporaryDirectory(dir=self.repository) as temporary:
            root = Path(temporary)
            evidence = root / "evidence"
            existing = iteration.run(self.suite_root, evidence, selector="v0")
            result_path = Path(existing["evidence_root"]) / "result.json"
            result_bytes = result_path.read_bytes()
            state_path = self._write_runtime_fixture(root / "runtime")
            initial_runtime = iteration.calibrate_runtime(self.suite_root, root / "runtime-evidence", state_path)
            state = json.loads(state_path.read_text(encoding="utf-8"))
            replacement_scopes = self._runtime_scopes(binary="e")
            replacement = identity_fixtures.current_binding(self.repository, "b" * 40, replacement_scopes)
            removed = lifecycle.rebind(load_formal_contract(self.suite_root / "contract.json"), state, replacement)
            self.assertEqual(["core", "frontier", "full", "qualification"], removed)
            state_path.write_text(json.dumps(state), encoding="utf-8")
            rebound_runtime = iteration.calibrate_runtime(self.suite_root, root / "runtime-evidence", state_path)
            reloaded = iteration.load_contract(self.suite_root)
            self.assertEqual(self.contract["identity"], reloaded["identity"])
            self.assertNotEqual(initial_runtime["runtime_calibration_identity"], rebound_runtime["runtime_calibration_identity"])
            self.assertEqual(result_bytes, result_path.read_bytes())

    def test_nonformal_entry_cannot_write_or_contain_formal_state(self) -> None:
        state_path = self.repository / iteration.FORMAL_STATE_RELATIVE
        before = state_path.read_bytes() if state_path.is_file() else None
        with tempfile.TemporaryDirectory(dir=self.repository) as temporary:
            iteration.run(self.suite_root, Path(temporary) / "evidence", selector="v0")
        if before is not None:
            self.assertEqual(before, state_path.read_bytes())
        with self.assertRaises(iteration.KernelIterationEvidenceError):
            iteration.run(self.suite_root, state_path.parent / "iteration", selector="v0")
        with self.assertRaises(iteration.KernelIterationEvidenceError):
            iteration.run(self.suite_root, self.repository / ".tmp", selector="v0")

    def test_future_evidence_types_share_one_versioned_input_contract(self) -> None:
        with tempfile.TemporaryDirectory(dir=self.repository) as temporary:
            root = Path(temporary)
            shared = {
                name: iteration.canonical_sha256({"condition": name})
                for name in self.contract["shared_evaluation_conditions"]["required_roles"]
            }
            direct = {
                name: iteration.canonical_sha256({"input": name})
                for name in self.contract["evidence_types"]["development"]["required_dependencies"]
            }
            content = {
                "schema": iteration.INPUT_SCHEMA,
                "evidence_type": "development",
                "shared_conditions": dict(sorted(shared.items())),
                "direct_dependencies": dict(sorted(direct.items())),
                "runtime_dependencies": {},
                "payloads": [],
            }
            input_path = root / "input.json"
            input_path.write_text(json.dumps({**content, "identity": iteration.canonical_sha256(content)}), encoding="utf-8")
            result = iteration.run(
                self.suite_root,
                root / "evidence",
                selector="v0",
                evidence_type="development",
                input_manifest=input_path,
            )
            sealed = json.loads((Path(result["evidence_root"]) / "result.json").read_text(encoding="utf-8"))
            self.assertFalse(sealed["formal_evidence"])
            self.assertIsNone(sealed["candidate_decision"])
            self.assertEqual("prepared", sealed["status"])

    def test_runtime_calibration_dependency_invalidates_only_declared_consumers(self) -> None:
        with tempfile.TemporaryDirectory(dir=self.repository) as temporary:
            root = Path(temporary)
            v0 = iteration.run(self.suite_root, root / "evidence", selector="v0")
            v0_result = Path(v0["evidence_root"]) / "result.json"
            v0_bytes = v0_result.read_bytes()
            manifest = self._write_subject(root / "v2.json")
            first_input = self._write_input(root / "input-a.json", runtime="1" * 64)
            second_input = self._write_input(root / "input-b.json", runtime="2" * 64)
            first = iteration.run(
                self.suite_root,
                root / "evidence",
                subject_manifest=manifest,
                evidence_type="development",
                input_manifest=first_input,
            )
            second = iteration.run(
                self.suite_root,
                root / "evidence",
                subject_manifest=manifest,
                evidence_type="development",
                input_manifest=second_input,
            )
            self.assertNotEqual(first["plan_identity"], second["plan_identity"])
            self.assertEqual(v0_bytes, v0_result.read_bytes())

    def test_runtime_calibration_rejects_report_content_drift(self) -> None:
        with tempfile.TemporaryDirectory(dir=self.repository) as temporary:
            root = Path(temporary)
            state_path = self._write_runtime_fixture(root)
            (root / "core.json").write_text("tampered\n", encoding="utf-8")
            with self.assertRaisesRegex(iteration.KernelIterationEvidenceError, "core 报告摘要漂移"):
                iteration.calibrate_runtime(self.suite_root, root / "evidence", state_path)
            self.assertFalse((root / "evidence").exists())

    def test_v2_contract_rejects_unknown_or_missing_direct_dependency(self) -> None:
        manifest = self._subject()
        manifest["direct_dependencies"].pop("vector")
        projection = self._projection(manifest)
        manifest["identity"] = iteration.canonical_sha256(projection)
        with self.assertRaises(iteration.KernelIterationEvidenceError):
            iteration.validate_v2_subject(self.contract, manifest)
        unknown = self._subject()
        unknown["direct_dependencies"]["documentation"] = "8" * 64
        unknown["identity"] = iteration.canonical_sha256(self._projection(unknown))
        with self.assertRaises(iteration.KernelIterationEvidenceError):
            iteration.validate_v2_subject(self.contract, unknown)

    def _write_subject(self, path: Path, *, semantic: str = "3" * 64, audit_git: str = "a" * 40) -> Path:
        value = self._subject(semantic=semantic, audit_git=audit_git)
        path.write_text(json.dumps(value), encoding="utf-8")
        return path

    def _write_input(self, path: Path, *, runtime: str) -> Path:
        evidence_type = "development"
        shared = {
            name: iteration.canonical_sha256({"condition": name})
            for name in self.contract["shared_evaluation_conditions"]["required_roles"]
        }
        direct = {
            name: iteration.canonical_sha256({"input": name})
            for name in self.contract["evidence_types"][evidence_type]["required_dependencies"]
        }
        content = {
            "schema": iteration.INPUT_SCHEMA,
            "evidence_type": evidence_type,
            "shared_conditions": dict(sorted(shared.items())),
            "direct_dependencies": dict(sorted(direct.items())),
            "runtime_dependencies": {"runtime-calibration": runtime},
            "payloads": [],
        }
        path.write_text(json.dumps({**content, "identity": iteration.canonical_sha256(content)}), encoding="utf-8")
        return path

    def _write_runtime_fixture(self, root: Path) -> Path:
        root.mkdir(parents=True, exist_ok=True)
        binding = identity_fixtures.current_binding(self.repository, "a" * 40, self._runtime_scopes())
        formal_contract = load_formal_contract(self.suite_root / "contract.json")
        state = lifecycle.new_state(formal_contract, binding)
        for name in ("frontier", "core", "qualification", "full"):
            report_path = root / f"{name}.json"
            report_path.write_text(json.dumps({"mode": name, "passed": True}), encoding="utf-8")
            report_sha256 = iteration.file_sha256(report_path)
            state["checkpoints"][name] = {
                "report_path": str(report_path),
                "report_sha256": report_sha256,
                "passed": True,
                "elapsed_seconds": 1,
                "evidence_identity": evidence_identity.evidence_identity(
                    name,
                    report_sha256,
                    lifecycle._dependencies_for_mode(binding, name),
                ),
            }
        state_path = root / "state.json"
        state_path.write_text(json.dumps(state, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        return state_path

    @staticmethod
    def _runtime_scopes(*, binary: str = "f") -> dict[str, dict[str, str]]:
        return {
            "frontier": {
                "environment_sha256": "1" * 64,
                "input_manifest_sha256": "2" * 64,
                "tool_sha256": "3" * 64,
                "artifact_sha256": ("4" if binary == "f" else binary) * 64,
            },
            "core": {"environment_sha256": "5" * 64, "input_manifest_sha256": "6" * 64, "tool_sha256": "7" * 64, "artifact_sha256": binary * 64},
            "product": {"environment_sha256": "8" * 64, "input_manifest_sha256": "9" * 64, "tool_sha256": "a" * 64, "artifact_sha256": binary * 64},
            "community": {"environment_sha256": "b" * 64, "input_manifest_sha256": "c" * 64, "tool_sha256": "d" * 64, "artifact_sha256": binary * 64},
        }

    def _subject(self, *, semantic: str = "3" * 64, audit_git: str = "a" * 40) -> dict[str, object]:
        value: dict[str, object] = {
            "schema": iteration.SUBJECT_SCHEMA,
            "name": "synthetic-v2",
            "role": "v2-candidate",
            "kernel_generation_identity": "1" * 64,
            "kernel_effect_identity": "2" * 64,
            "direct_dependencies": {
                "authority-substrate": "4" * 64,
                "product-rules": "5" * 64,
                "semantic": semantic,
                "vector": "6" * 64,
            },
            "artifacts": {"binary": "7" * 64},
            "audit": {"source_git": audit_git},
        }
        value["identity"] = iteration.canonical_sha256(self._projection(value))
        return value

    @staticmethod
    def _projection(value: dict[str, object]) -> dict[str, object]:
        return {
            "schema": value["schema"],
            "role": value["role"],
            "kernel_generation_identity": value["kernel_generation_identity"],
            "kernel_effect_identity": value["kernel_effect_identity"],
            "direct_dependencies": dict(sorted(dict(value["direct_dependencies"]).items())),
            "artifacts": dict(sorted(dict(value.get("artifacts", {})).items())),
        }


if __name__ == "__main__":
    unittest.main()
