from __future__ import annotations

import hashlib
import json
import tempfile
import unittest
from pathlib import Path

import check_frozen_baseline as checker


class FrozenBaselineTest(unittest.TestCase):
    def test_direct_file_rejects_missing_and_tampered_dependencies(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            path = root / "direct.txt"
            path.write_text("stable", encoding="utf-8")
            entry = [{
                "role": "direct", "path": "direct.txt", "bytes": 6,
                "sha256": hashlib.sha256(b"stable").hexdigest(),
            }]
            checker.verify_direct_files(root, entry)
            path.write_text("changed", encoding="utf-8")
            with self.assertRaisesRegex(checker.BaselineError, "大小变化|摘要变化"):
                checker.verify_direct_files(root, entry)
            path.unlink()
            with self.assertRaisesRegex(checker.BaselineError, "缺失"):
                checker.verify_direct_files(root, entry)

    def test_release_bundle_rejects_misbound_and_tampered_artifacts(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            bundle = root / "bundle"
            (bundle / "bin").mkdir(parents=True)
            binary = bundle / "bin" / "ownward.exe"
            binary.write_bytes(b"binary")
            manifest = {
                "schema": "ownward.release-bundle/v2", "candidate": "a" * 40,
                "files": {"bin/ownward.exe": hashlib.sha256(b"binary").hexdigest()},
            }
            manifest_path = bundle / "manifest.json"
            manifest_path.write_text(json.dumps(manifest), encoding="utf-8")
            spec = {"root": "bundle", "candidate": "a" * 40, "manifest_sha256": checker.file_sha256(manifest_path)}
            self.assertEqual(checker.verify_release_bundle(root, spec), 1)
            with self.assertRaisesRegex(checker.BaselineError, "错绑"):
                checker.verify_release_bundle(root, {**spec, "candidate": "b" * 40})
            binary.write_bytes(b"tampered")
            with self.assertRaisesRegex(checker.BaselineError, "摘要变化"):
                checker.verify_release_bundle(root, spec)

    def test_state_projection_rejects_checkpoint_or_baseline_drift(self) -> None:
        state = {
            "schema": "ownward.acceptance-state/v1",
            "binding": {
                "schema": "ownward.acceptance-binding/v4", "suite_version": "1.0.0",
                "candidate": "a" * 40,
                "scopes": {"core": {
                    "environment_sha256": "1" * 64, "input_manifest_sha256": "2" * 64,
                    "tool_sha256": "3" * 64, "artifact_sha256": "4" * 64,
                }},
            },
            "baseline": None,
            "checkpoints": {"core": {"passed": True}},
            "invalidated_reports": {"community": "x"},
            "baseline_history": [{"candidate": "v0"}],
        }
        expected = {
            "bound_candidate": "a" * 40, "active_baseline_is_null": True,
            "binding_sha256": checker.canonical_sha256(state["binding"]),
            "checkpoints_sha256": checker.canonical_sha256(state["checkpoints"]),
            "invalidated_reports": state["invalidated_reports"],
            "baseline_history_record_sha256": [checker.canonical_sha256(state["baseline_history"][0])],
        }
        checker.verify_state_projection(state, expected)
        state["checkpoints"]["core"]["passed"] = False
        with self.assertRaisesRegex(checker.BaselineError, "检查点集合变化"):
            checker.verify_state_projection(state, expected)

    def test_responsibility_map_requires_exact_coverage_and_unique_targets(self) -> None:
        entries = []
        for identity in sorted({
            "long-term-assets", "authoritative-control-state", "active-kernel-control",
            "capability-derived-generation", "runtime-retrieval-state", "core-service-orchestration",
            "semantic-capability", "vector-capability", "explicit-assembly", "mcp-access",
            "shared-mcp-runtime", "collaboration-rules", "candidate-release-artifacts",
            "acceptance-binding", "acceptance-evidence-lifecycle",
        }):
            entries.append({
                "id": identity, "current_owner": "current", "current_paths": ["path"],
                "state_class": "runtime-state", "write_authority": "owner", "direct_dependencies": [],
                "target_owner": "target", "current_issue": "none", "implicit_semantics": "none", "next_stage": 2,
            })
        checker.verify_responsibility_map(entries)
        entries[0]["target_owner"] = "unassigned"
        with self.assertRaisesRegex(checker.BaselineError, "唯一目标归属"):
            checker.verify_responsibility_map(entries)


if __name__ == "__main__":
    unittest.main()
