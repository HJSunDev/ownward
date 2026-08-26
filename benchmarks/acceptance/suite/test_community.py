import json
import tempfile
import unittest
from unittest import mock
from pathlib import Path

import community


class CommunityExecutionTests(unittest.TestCase):
    def setUp(self) -> None:
        self.binding = {
            "candidate": "a" * 40, "binary_sha256": "a" * 64,
            "environment_sha256": "b" * 64, "input_manifest_sha256": "c" * 64,
            "tool_sha256": "d" * 64,
        }

    def test_complete_run_requires_current_identity_and_all_500_questions(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            package = root / "submission.zip"
            package.write_bytes(b"package")
            (root / "official-evaluation.jsonl").write_text("{}\n", encoding="utf-8")
            (root / "diagnostics.jsonl").write_text("{}\n", encoding="utf-8")
            (root / "diagnostic-summary.json").write_text("{}\n", encoding="utf-8")
            (root / "checkpoint-manifest.json").write_text("{}\n", encoding="utf-8")
            identity = {**self.binding, "formal": True, "profile": "Ownward LongMemEval-S Production Profile"}
            identity["binary_sha256"] = self.binding["binary_sha256"]
            (root / "identity.json").write_text(json.dumps(identity), encoding="utf-8")
            report = {
                **identity, "questions": 500, "submission_sha256": community._sha256(package),
                "execution": {"complete": True, "protocol_valid": True, "evidence_complete": True},
                "quality": {"assessment_status": "not_determined"}, "passed": False,
            }
            (root / "report.json").write_text(json.dumps(report), encoding="utf-8")
            self.assertEqual(500, community._run_complete(root, self.binding)["questions"])
            identity["tool_sha256"] = "e" * 64
            (root / "identity.json").write_text(json.dumps(identity), encoding="utf-8")
            self.assertIsNone(community._run_complete(root, self.binding))

    def test_incomplete_run_is_resumable(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "identity.json").write_text("not-json", encoding="utf-8")
            self.assertIsNone(community._run_complete(root, self.binding))

    def test_resume_uses_only_the_remaining_persistent_wall_budget(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "wall-clock.json").write_text(json.dumps({
                "schema": "ownward.longmemeval-s-wall-clock/v1",
                "elapsed_seconds": 7200.5,
            }), encoding="utf-8")
            self.assertEqual(13199.5, community._remaining_wall_seconds(root, 20400))

    def test_resume_rejects_an_exhausted_or_invalid_persistent_wall_budget(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            clock = root / "wall-clock.json"
            clock.write_text(json.dumps({
                "schema": "ownward.longmemeval-s-wall-clock/v1",
                "elapsed_seconds": 20400,
            }), encoding="utf-8")
            with self.assertRaisesRegex(community.CommunityExecutionError, "exhausted"):
                community._remaining_wall_seconds(root, 20400)
            clock.write_text(json.dumps({
                "schema": "ownward.longmemeval-s-wall-clock/v1",
                "elapsed_seconds": -1,
            }), encoding="utf-8")
            with self.assertRaisesRegex(community.CommunityExecutionError, "invalid"):
                community._remaining_wall_seconds(root, 20400)
            clock.write_text("not-json", encoding="utf-8")
            with self.assertRaisesRegex(community.CommunityExecutionError, "invalid"):
                community._remaining_wall_seconds(root, 20400)

    def test_outer_timeout_seals_the_persistent_budget(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "wall-clock.json").write_text(json.dumps({
                "schema": "ownward.longmemeval-s-wall-clock/v1",
                "elapsed_seconds": 7200,
            }), encoding="utf-8")
            with mock.patch.object(
                community.process_control, "run",
                side_effect=community.process_control.ProcessTimeout("timeout"),
            ) as run:
                with self.assertRaisesRegex(community.CommunityExecutionError, "exceeded"):
                    community._run_with_wall_budget(["adapter"], cwd=root, run_dir=root, maximum=20400)
            self.assertEqual(13200, run.call_args.kwargs["timeout"])
            clock = json.loads((root / "wall-clock.json").read_text(encoding="utf-8"))
            self.assertEqual(20400, clock["elapsed_seconds"])
            with self.assertRaisesRegex(community.CommunityExecutionError, "exhausted"):
                community._remaining_wall_seconds(root, 20400)

    def test_non_timeout_exit_persists_the_exact_invocation_cost(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "wall-clock.json").write_text(json.dumps({
                "schema": "ownward.longmemeval-s-wall-clock/v1",
                "elapsed_seconds": 7200,
            }), encoding="utf-8")
            with mock.patch.object(community.process_control, "run", return_value="completed"), mock.patch.object(
                community.time, "monotonic", side_effect=[100.0, 125.5],
            ):
                result = community._run_with_wall_budget(["adapter"], cwd=root, run_dir=root, maximum=20400)
            self.assertEqual("completed", result)
            clock = json.loads((root / "wall-clock.json").read_text(encoding="utf-8"))
            self.assertEqual(7225.5, clock["elapsed_seconds"])

    def test_suite_artifact_export_stays_in_acceptance_workspace(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            workspace = Path(directory) / "workspace"
            paths = community.artifact_paths({"_workspace": str(workspace)})
            self.assertEqual([workspace / "evidence" / "community"], paths)
            self.assertTrue(paths[0].is_relative_to(workspace))

    def test_persistent_manifest_rejects_old_or_changed_benchmark(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            manifest = root / "manifest.json"
            manifest.write_text(json.dumps({
                "schema": "ownward.longmemeval-s-environment/v1",
                "official": {"code_revision": "old"},
                "integrity": {"data_sha256": community.OFFICIAL_DATA_SHA256},
                "layout": {},
            }), encoding="utf-8")
            with self.assertRaisesRegex(community.CommunityExecutionError, "revision"):
                community._persistent_layout(manifest)


if __name__ == "__main__":
    unittest.main()
