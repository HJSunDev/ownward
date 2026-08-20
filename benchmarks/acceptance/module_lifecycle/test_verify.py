from __future__ import annotations

from pathlib import Path
import tempfile
import unittest

import verify


class VerifyTests(unittest.TestCase):
    def test_generation_snapshot_binds_pointer_manifest_and_tree(self) -> None:
        with tempfile.TemporaryDirectory() as root_value:
            state = Path(root_value)
            generation = state / "generations" / "gen-one"
            generation.mkdir(parents=True)
            verify.write_json(state / "current.json", {"generation": "gen-one"})
            verify.write_json(generation / "manifest.json", {"embedding_space": "space-one"})
            (generation / "organization.binlog").write_bytes(b"state")
            snapshot = verify.generation_snapshot(state)
        self.assertEqual(snapshot["pointer"]["generation"], "gen-one")
        self.assertEqual(snapshot["generation_directories"], ["gen-one"])
        self.assertEqual(len(snapshot["state_tree_sha256"]), 64)

    def test_submissions_bind_work_asset_and_capability(self) -> None:
        work = {"id": "W1", "asset": {"id": "I1", "revision": 2}}
        complete = verify.complete_submission(work, summary="summary", execution="test", inferred_context=True)
        uncertain = verify.uncertain_submission(work)
        self.assertEqual(complete["asset_revision"], 2)
        self.assertEqual(complete["capability"]["id"], "module-lifecycle-controller")
        self.assertEqual(uncertain["status"], "uncertain")
        self.assertTrue(uncertain["uncertainty"])


if __name__ == "__main__":
    unittest.main()
