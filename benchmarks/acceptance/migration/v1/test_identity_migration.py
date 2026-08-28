from __future__ import annotations

import json
import shutil
import sys
import tempfile
import unittest
from unittest import mock
from pathlib import Path


HERE = Path(__file__).resolve().parent
REPOSITORY = HERE.parents[3]
SUITE_ROOT = REPOSITORY / "benchmarks" / "acceptance" / "suite"
sys.path.insert(0, str(SUITE_ROOT))

import evidence_identity  # noqa: E402
import identity_migration  # noqa: E402
import binding as candidate_binding  # noqa: E402


class IdentityMigrationTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)
        self.state = self.root / "state.json"
        self.binding = self.root / "binding"
        shutil.copy2(
            REPOSITORY / ".tmp" / "first-kernel-baseline-v1" / "acceptance" / "state.json",
            self.state,
        )
        shutil.copytree(
            REPOSITORY / ".tmp" / "first-kernel-baseline-v1" / "acceptance-3e712f2" / "binding",
            self.binding,
        )
        self.frozen = HERE / "frozen-baseline.json"
        if json.loads(self.state.read_text(encoding="utf-8")).get("schema") in {
            "ownward.acceptance-state/v2", evidence_identity.STATE_SCHEMA,
        }:
            self._restore_frozen_source()

    def tearDown(self) -> None:
        self.temporary.cleanup()

    def test_write_is_lossless_and_idempotent(self) -> None:
        before = json.loads(self.state.read_text(encoding="utf-8"))
        reports = {
            mode: (Path(checkpoint["report_path"]), checkpoint["report_sha256"])
            for mode, checkpoint in before["checkpoints"].items()
        }
        first = identity_migration.migrate(
            REPOSITORY, self.state, self.binding, self.frozen, write=True,
        )
        encoded = self.state.read_bytes()
        second = identity_migration.migrate(
            REPOSITORY, self.state, self.binding, self.frozen, write=True,
        )
        self.assertTrue(first["changed"])
        self.assertTrue(first["written"])
        self.assertFalse(second["changed"])
        self.assertFalse(second["written"])
        self.assertEqual(self.state.read_bytes(), encoded)
        migrated = json.loads(encoded.decode("utf-8"))
        self.assertEqual(set(migrated["checkpoints"]), set(before["checkpoints"]))
        self.assertEqual(len(migrated["baseline_history"]), len(before["baseline_history"]))
        for path, digest in reports.values():
            self.assertEqual(evidence_identity.file_sha256(path), digest)
        self.assertFalse(migrated["identity_migration"]["policy"]["reports_rewritten"])
        self.assertFalse(migrated["identity_migration"]["policy"]["parallel_state_created"])

    def test_recovers_pointer_after_atomic_state_commit(self) -> None:
        old_pointer = (self.binding / "active.json").read_bytes()
        identity_migration.migrate(REPOSITORY, self.state, self.binding, self.frozen, write=True)
        migrated = self.state.read_bytes()
        (self.binding / "active.json").write_bytes(old_pointer)
        with self.assertRaisesRegex(identity_migration.IdentityMigrationError, "--write"):
            identity_migration.migrate(REPOSITORY, self.state, self.binding, self.frozen, write=False)
        identity_migration.migrate(REPOSITORY, self.state, self.binding, self.frozen, write=True)
        self.assertEqual(self.state.read_bytes(), migrated)

    def test_tampered_source_fails_without_side_effect(self) -> None:
        value = json.loads(self.state.read_text(encoding="utf-8"))
        value["checkpoints"]["core"]["passed"] = False
        self.state.write_text(json.dumps(value), encoding="utf-8")
        state_before = self.state.read_bytes()
        pointer_before = (self.binding / "active.json").read_bytes()
        generations_before = sorted(path.name for path in (self.binding / "generations").iterdir())
        with self.assertRaisesRegex(identity_migration.IdentityMigrationError, "冻结起点"):
            identity_migration.migrate(REPOSITORY, self.state, self.binding, self.frozen, write=True)
        self.assertEqual(self.state.read_bytes(), state_before)
        self.assertEqual((self.binding / "active.json").read_bytes(), pointer_before)
        self.assertEqual(sorted(path.name for path in (self.binding / "generations").iterdir()), generations_before)

    def test_existing_v5_v2_graph_converges_atomically_and_then_is_byte_idempotent(self) -> None:
        identity_migration.migrate(REPOSITORY, self.state, self.binding, self.frozen, write=True)
        state = json.loads(self.state.read_text(encoding="utf-8"))
        bad_binding = state["binding"]
        bad_binding["schema"] = "ownward.acceptance-binding/v5"
        lifecycle_identity = bad_binding["lifecycle"]["evidence"]["identity"]
        for scope in bad_binding["scopes"].values():
            scope["direct_dependencies"]["acceptance-lifecycle"] = lifecycle_identity
            scope["identity"] = evidence_identity.dependency_identity(
                next(name for name, value in bad_binding["scopes"].items() if value is scope),
                scope["direct_dependencies"],
            )
        state["schema"] = "ownward.acceptance-state/v2"
        for mode, checkpoint in state["checkpoints"].items():
            checkpoint["evidence_identity"] = evidence_identity.evidence_identity(
                mode, checkpoint["report_sha256"],
                bad_binding["scopes"][candidate_binding.scope_for_mode(mode)]["direct_dependencies"],
            )
        for baseline in state["baseline_history"]:
            dependencies = baseline["direct_dependencies"]
            for value in dependencies.values():
                value["acceptance-lifecycle"] = lifecycle_identity
            baseline.update(evidence_identity.baseline_identity_fields(
                baseline["product_identity"], dependencies, {
                    "core": baseline["core_report_sha256"],
                    "frontier": baseline["frontier_report_sha256"],
                    "qualification": baseline["qualification_report_sha256"],
                },
            ))
        state["identity_migration"]["schema"] = "ownward.acceptance-identity-migration/v1"
        state["identity_migration"]["identity"] = evidence_identity.canonical_sha256({
            name: value for name, value in state["identity_migration"].items() if name != "identity"
        })
        active = json.loads((self.binding / "active.json").read_text(encoding="utf-8"))
        generation = self.binding / "generations" / active["generation"]
        erroneous = self.binding / "generations" / "erroneous-v5"
        generation.rename(erroneous)
        generation = erroneous
        active["generation"] = erroneous.name
        encoded_binding = (json.dumps(bad_binding, ensure_ascii=False, indent=2) + "\n").encode("utf-8")
        (generation / "binding.json").write_bytes(encoded_binding)
        active["binding_sha256"] = evidence_identity.file_sha256(generation / "binding.json")
        (self.binding / "active.json").write_text(json.dumps(active, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
        self.state.write_text(json.dumps(state, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")

        reports = {mode: checkpoint["report_sha256"] for mode, checkpoint in state["checkpoints"].items()}
        result = identity_migration.migrate(REPOSITORY, self.state, self.binding, self.frozen, write=True)
        first = self.state.read_bytes()
        repeated = identity_migration.migrate(REPOSITORY, self.state, self.binding, self.frozen, write=True)
        corrected = json.loads(first.decode("utf-8"))
        self.assertTrue(result["changed"] and result["written"])
        self.assertFalse(repeated["changed"] or repeated["written"])
        self.assertEqual(first, self.state.read_bytes())
        self.assertEqual(reports, {mode: checkpoint["report_sha256"] for mode, checkpoint in corrected["checkpoints"].items()})
        self.assertTrue(all(
            "acceptance-lifecycle" not in scope["direct_dependencies"]
            for scope in corrected["binding"]["scopes"].values()
        ))
        self.assertEqual("ownward.acceptance-identity-migration/v2", corrected["identity_migration"]["schema"])

    def test_lifecycle_maintenance_refreshes_only_top_level_artifact_identity(self) -> None:
        identity_migration.migrate(REPOSITORY, self.state, self.binding, self.frozen, write=True)
        before = json.loads(self.state.read_text(encoding="utf-8"))
        lifecycle_only = json.loads(json.dumps(before["binding"]["lifecycle"]))
        lifecycle_only["evidence"]["identity"] = "9" * 64
        with mock.patch.object(evidence_identity, "lifecycle_identities", return_value=lifecycle_only):
            first = identity_migration.migrate(REPOSITORY, self.state, self.binding, self.frozen, write=True)
            encoded = self.state.read_bytes()
            second = identity_migration.migrate(REPOSITORY, self.state, self.binding, self.frozen, write=True)
        after = json.loads(encoded.decode("utf-8"))
        self.assertTrue(first["changed"] and first["written"])
        self.assertFalse(second["changed"] or second["written"])
        self.assertEqual(encoded, self.state.read_bytes())
        self.assertEqual(before["binding"]["scopes"], after["binding"]["scopes"])
        self.assertEqual(before["checkpoints"], after["checkpoints"])
        self.assertEqual(before["baseline_history"], after["baseline_history"])

    def test_reporting_semantics_drift_cannot_be_relabelled_by_identity_migration(self) -> None:
        identity_migration.migrate(REPOSITORY, self.state, self.binding, self.frozen, write=True)
        state_before = self.state.read_bytes()
        pointer_before = (self.binding / "active.json").read_bytes()
        generations_before = sorted(path.name for path in (self.binding / "generations").iterdir())
        current = evidence_identity.reporting_identities(REPOSITORY)
        for kind in ("reception", "relationships", "summary"):
            with self.subTest(kind=kind):
                changed = json.loads(json.dumps(current))
                changed[kind]["content"]["sha256"] = "9" * 64
                changed[kind]["identity"] = evidence_identity.canonical_sha256({
                    "schema": changed[kind]["schema"], "content": changed[kind]["content"],
                })
                with mock.patch.object(evidence_identity, "reporting_identities", return_value=changed):
                    with self.assertRaisesRegex(identity_migration.IdentityMigrationError, "正式 rebind"):
                        identity_migration.migrate(
                            REPOSITORY, self.state, self.binding, self.frozen, write=True,
                        )
                self.assertEqual(state_before, self.state.read_bytes())
                self.assertEqual(pointer_before, (self.binding / "active.json").read_bytes())
                self.assertEqual(
                    generations_before,
                    sorted(path.name for path in (self.binding / "generations").iterdir()),
                )

    def _restore_frozen_source(self) -> None:
        state = json.loads(self.state.read_text(encoding="utf-8"))
        source = state["identity_migration"]["source"]
        state["schema"] = "ownward.acceptance-state/v1"
        legacy_directory = next(
            directory
            for directory in (self.binding / "generations").iterdir()
            if (directory / "binding.json").is_file()
            and evidence_identity.canonical_sha256(
                json.loads((directory / "binding.json").read_text(encoding="utf-8"))
            ) == source["binding_sha256"]
        )
        legacy = json.loads((legacy_directory / "binding.json").read_text(encoding="utf-8"))
        state["binding"] = legacy
        for checkpoint in state["checkpoints"].values():
            checkpoint.pop("evidence_identity", None)
        for baseline in state["baseline_history"]:
            for name in ("product_identity", "direct_dependencies", "evidence_identities", "identity"):
                baseline.pop(name, None)
        state.pop("identity_migration", None)
        encoded = (json.dumps(state, ensure_ascii=False, indent=2) + "\n").replace("\n", "\r\n").encode("utf-8")
        self.assertEqual(evidence_identity.file_sha256_bytes(encoded), source["state_file_sha256"])
        self.state.write_bytes(encoded)
        pointer = {
            "schema": "ownward.acceptance-binding-active/v1",
            "generation": legacy_directory.name,
            "binding_sha256": evidence_identity.file_sha256(legacy_directory / "binding.json"),
        }
        (self.binding / "active.json").write_bytes(
            (json.dumps(pointer, ensure_ascii=False, indent=2) + "\n").encode("utf-8"),
        )


if __name__ == "__main__":
    unittest.main()
