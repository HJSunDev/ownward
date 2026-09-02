from __future__ import annotations

import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock

import preflight


class PreflightTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.suite_root = Path(__file__).resolve().parent
        cls.repository = cls.suite_root.parents[2]
        (cls.repository / ".tmp").mkdir(exist_ok=True)

    def test_core_preflight_uses_only_candidate_dependencies(self) -> None:
        with tempfile.TemporaryDirectory(dir=self.repository / ".tmp") as directory:
            root = Path(directory)
            binary, runtime = self._candidate(root)
            config = self._config(root, ["core"], binary, runtime)
            with mock.patch.object(preflight.subprocess, "run", side_effect=AssertionError("core preflight must not execute external tools")):
                report = preflight.run(self.suite_root, config, root / "new-isolation")
            self.assertTrue(report["passed"])
            self.assertEqual(["core"], report["enabled_scopes"])
            self.assertNotIn("product", report["checks"])
            self.assertNotIn("community", report["checks"])
            self.assertNotIn("cost_bound", report)
            self.assertFalse((root / "new-isolation").exists())

    def test_frontier_preflight_needs_no_candidate_model_or_community(self) -> None:
        with tempfile.TemporaryDirectory(dir=self.repository / ".tmp") as directory:
            root = Path(directory)
            observer = root / "frontier.exe"
            observer.write_bytes(b"observer")
            config = {
                "schema": "ownward.acceptance-execution/v3",
                "repository": str(self.repository), "workspace": str(root / "work"), "binding_dir": str(root / "binding"),
                "enabled_scopes": ["frontier"], "frontier": {"tool": str(observer), "targeted_stages": ["lexical"]},
            }
            with (
                mock.patch.object(preflight, "_community_preflight", side_effect=AssertionError("community must stay deferred")),
                mock.patch.object(preflight.subprocess, "run", side_effect=AssertionError("frontier preflight must not execute external tools")),
            ):
                report = preflight.run(self.suite_root, config, root / "new-isolation")
            self.assertEqual(["frontier"], report["enabled_scopes"])
            self.assertEqual({"frontier"}, set(report["checks"]))

    def test_product_preflight_checks_codex_and_release_inputs(self) -> None:
        with tempfile.TemporaryDirectory(dir=self.repository / ".tmp") as directory:
            root = Path(directory)
            binary, runtime = self._candidate(root)
            codex = root / "codex.exe"
            auth = root / "auth.json"
            codex.write_bytes(b"codex")
            auth.write_text("{}\n", encoding="utf-8")
            package = root / "package"
            package.mkdir()
            (package / "manifest.json").write_text("{}\n", encoding="utf-8")
            production = root / "production.json"
            production.write_text("{}\n", encoding="utf-8")
            config = self._config(root, ["product"], binary, runtime)
            config["product"] = {
                "package": str(package), "production_storage_report": str(production),
                "codex_binary": str(codex), "codex_auth_file": str(auth),
                "codex_model": "gpt-5.4-mini", "codex_reasoning_effort": "xhigh",
            }
            completed = mock.Mock(returncode=0, stdout="codex-cli 1.0\n")
            with mock.patch.object(preflight.subprocess, "run", return_value=completed):
                report = preflight.run(self.suite_root, config, root / "new-isolation")
            self.assertIn("product", report["checks"])

    def test_preflight_rejects_changed_runtime_artifact_before_execution(self) -> None:
        with tempfile.TemporaryDirectory(dir=self.repository / ".tmp") as directory:
            root = Path(directory)
            binary, runtime = self._candidate(root)
            (runtime / "model.gguf").write_bytes(b"changed")
            config = self._config(root, ["core"], binary, runtime)
            with self.assertRaisesRegex(preflight.PreflightError, "摘要不一致"):
                preflight.run(self.suite_root, config, root / "isolation")

    def test_community_calibration_uses_complete_three_batch_official_questions(self) -> None:
        protocol = {
            "memory": {"semantic_batch_size": 20},
            "execution": {"calibration_questions": 4, "calibration_semantic_batches_per_question": 3},
        }
        questions = [
            {"question_id": f"{question_type}-short", "question_type": question_type, "haystack_sessions": [None] * 20}
            for question_type in preflight.COMMUNITY_CALIBRATION_TYPES
        ] + [
            {"question_id": question_type, "question_type": question_type, "haystack_sessions": [None] * (41 + index)}
            for index, question_type in enumerate(preflight.COMMUNITY_CALIBRATION_TYPES)
        ]
        selected = preflight._community_calibration_fixture(questions, protocol)
        self.assertEqual(list(preflight.COMMUNITY_CALIBRATION_TYPES), [item["question_id"] for item in selected])
        self.assertTrue(all((len(item["haystack_sessions"]) + 19) // 20 == 3 for item in selected))

    def test_community_cost_projection_uses_external_intelligence_capacity_only_for_semantics(self) -> None:
        projected = preflight._community_cost_projection(
            semantic_model_seconds=800.0, semantic_calls=8,
            reader_model_seconds=40.0, judge_model_seconds=20.0,
            calibration_questions=4, per_question_host_seconds=8.0,
            projected_semantic_requests=80, question_count=40,
            question_workers=4, external_intelligence_max_active=8,
            normal_variation_reserve_ratio=0.2, bounded_retry_reserve_ratio=0.1,
            checkpoint_recovery_reserve_seconds=3600.0,
        )
        self.assertEqual(1000.0, projected["semantic"])
        self.assertEqual(100.0, projected["reader"])
        self.assertEqual(50.0, projected["judge"])
        self.assertEqual(80.0, projected["host"])
        self.assertEqual(5191.0, projected["required_ceiling"])

    def _candidate(self, root: Path) -> tuple[Path, Path]:
        binary = root / "ownward.exe"
        binary.write_bytes(b"binary")
        runtime = root / "embedding"
        runtime.mkdir()
        model = runtime / "model.gguf"
        library = runtime / "runtime.dll"
        model.write_bytes(b"model")
        library.write_bytes(b"runtime")
        manifest = {
            "schema": "ownward.embedding-bundle/v3", "capability": "embeddinggemma-q8",
            "model": {"path": model.name, "sha256": self._sha(model)},
            "runtime": {"files": {library.name: self._sha(library)}},
        }
        (runtime / "manifest.json").write_text(json.dumps(manifest), encoding="utf-8")
        return binary, runtime

    def _config(self, root: Path, scopes: list[str], binary: Path, runtime: Path) -> dict:
        return {
            "schema": "ownward.acceptance-execution/v3",
            "repository": str(self.repository), "workspace": str(root / "work"), "binding_dir": str(root / "binding"),
            "enabled_scopes": scopes, "candidate": {"binary": str(binary), "embedding_bundle_dir": str(runtime)},
        }

    @staticmethod
    def _sha(path: Path) -> str:
        return hashlib.sha256(path.read_bytes()).hexdigest()


if __name__ == "__main__":
    unittest.main()
