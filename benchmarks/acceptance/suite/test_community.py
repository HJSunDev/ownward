import json
import tempfile
import unittest
from pathlib import Path

import community


class CommunitySubmissionTests(unittest.TestCase):
    def setUp(self) -> None:
        self.binding = {
            "candidate": "candidate", "binary_sha256": "a" * 64,
            "environment_sha256": "b" * 64, "input_manifest_sha256": "c" * 64,
            "tool_sha256": "d" * 64,
        }

    def test_submission_code_contains_trajectory_logic_without_local_import(self) -> None:
        root = Path(__file__).resolve().parents[2] / "longmemeval_v2"
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "ownward_memory.py"
            community.build_submission_code(root / "ownward_memory.py", root / "ownward_trajectory.py", output)
            source = output.read_text(encoding="utf-8")
        self.assertNotIn("from ownward_trajectory import", source)
        self.assertIn("def trajectory_documents", source)
        compile(source, str(output), "exec")

    def test_incomplete_or_stale_domain_is_resumable(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "aggregated_metrics.json").write_text("{}\n", encoding="utf-8")
            (root / "run_args.json").write_text("not-json", encoding="utf-8")
            self.assertIsNone(community._domain_complete(root, "web", self.binding))
            stale = {"ownward_evidence": {
                "candidate": "another", "release_binary_sha256": "a" * 64,
                "environment_sha256": "b" * 64, "input_manifest_sha256": "c" * 64,
                "tool_sha256": "d" * 64, "query_mode": "codex",
                "official_revision": community.OFFICIAL_REVISION,
            }}
            (root / "run_args.json").write_text(json.dumps(stale), encoding="utf-8")
            self.assertIsNone(community._domain_complete(root, "web", self.binding))

    def test_public_frontier_requires_gain_or_exact_reference_point(self) -> None:
        lafs = {
            "lafs_gain": 0,
            "reference_frontier": [{"accuracy": 74.9, "latency_seconds": 108.3}],
        }
        self.assertTrue(community._frontier_eligible(
            lafs, {"lafs_accuracy_percentage_points": 74.9, "lafs_latency_seconds": 108.3}
        ))
        self.assertFalse(community._frontier_eligible(
            lafs, {"lafs_accuracy_percentage_points": 60.0, "lafs_latency_seconds": 180.0}
        ))
        self.assertTrue(community._frontier_eligible(
            {"lafs_gain": 0.01, "reference_frontier": []},
            {"lafs_accuracy_percentage_points": 60.0, "lafs_latency_seconds": 10.0},
        ))

    def test_all_community_evidence_must_stay_in_one_workspace(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            workspace = Path(directory) / "workspace"
            config = {
                "_workspace": str(workspace),
                "web_arguments": ["--output-dir", str(workspace / "community" / "web")],
                "enterprise_arguments": ["--output-dir", str(workspace / "community" / "enterprise")],
                "submission_root": str(workspace / "submission"),
                "submission_name": "ownward",
            }
            self.assertEqual(5, len(community.artifact_paths(config)))
            config["web_arguments"] = ["--output-dir", str(Path(directory) / "outside")]
            with self.assertRaisesRegex(community.CommunityExecutionError, "workspace"):
                community.artifact_paths(config)


if __name__ == "__main__":
    unittest.main()
