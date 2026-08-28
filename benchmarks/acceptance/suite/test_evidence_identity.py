from __future__ import annotations

import copy
import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import binding
import evidence_identity
import lifecycle
import report_semantics
from contract import load_contract


class EvidenceIdentityTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.suite = Path(__file__).resolve().parent
        cls.repository = cls.suite.parents[2]
        cls.candidate = "3e712f22f0529b4eef81b8826f8bb201bf9f6bf8"
        cls.components = evidence_identity.build_candidate_components(
            cls.repository,
            cls.candidate,
            "f" * 64,
            "e" * 64,
        )
        cls.lifecycle = evidence_identity.lifecycle_identities(cls.repository)
        cls.reporting = evidence_identity.reporting_identities(cls.repository)

    def test_tool_identity_excludes_git_and_lifecycle_facades_but_not_report_execution(self) -> None:
        base = {
            "schema": "ownward.acceptance-tool-manifest/v4",
            "scope": "core",
            "files": [
                {"path": "benchmarks/acceptance/suite/binding.py", "sha256": "1" * 64},
                {"path": "benchmarks/acceptance/suite/execution_core.py", "sha256": "2" * 64},
                {"path": "benchmarks/acceptance/suite/state_relationships.py", "sha256": "5" * 64},
            ],
        }
        changed_audit = copy.deepcopy(base)
        changed_audit["audit_source_git"] = "b" * 40
        changed_audit["files"][0]["sha256"] = "3" * 64
        self.assertEqual(evidence_identity.tool_identity(base), evidence_identity.tool_identity(changed_audit))
        changed_execution = copy.deepcopy(base)
        changed_execution["files"][1]["sha256"] = "4" * 64
        self.assertNotEqual(evidence_identity.tool_identity(base), evidence_identity.tool_identity(changed_execution))
        changed_state_relationships = copy.deepcopy(base)
        changed_state_relationships["files"][2]["sha256"] = "6" * 64
        self.assertEqual(evidence_identity.tool_identity(base), evidence_identity.tool_identity(changed_state_relationships))

    def test_reporting_identities_are_responsibility_scoped(self) -> None:
        first = copy.deepcopy(self.reporting)
        state_only = copy.deepcopy(first)
        self.assertEqual(first, state_only)
        changed_reception = copy.deepcopy(first)
        self._change_reporting(changed_reception, "reception", "1" * 64)
        changed_relationships = copy.deepcopy(first)
        self._change_reporting(changed_relationships, "relationships", "2" * 64)
        changed_summary = copy.deepcopy(first)
        self._change_reporting(changed_summary, "summary", "3" * 64)
        manifests, scopes = self._current_fixture()
        base = self._binding(scopes, manifests, reporting=first)
        reception = self._binding(scopes, manifests, reporting=changed_reception)
        relationships = self._binding(scopes, manifests, reporting=changed_relationships)
        summary = self._binding(scopes, manifests, reporting=changed_summary)
        self.assertNotEqual(base["scopes"], reception["scopes"])
        self.assertNotEqual(base["scopes"], relationships["scopes"])
        self.assertEqual(base["scopes"], summary["scopes"])
        self.assertNotEqual(binding.aggregate(base)["tool_sha256"], binding.aggregate(summary)["tool_sha256"])
        with patch("evidence_identity.reporting_identities", return_value=changed_summary):
            with self.assertRaisesRegex(lifecycle.LifecycleError, "summary"):
                report_semantics.verify_current_reporting(base, include_summary=True)

    def test_direct_dependencies_encode_only_real_consumers(self) -> None:
        manifests, scopes = self._current_fixture()
        value = self._binding(scopes, manifests)
        scopes = {name: set(item["direct_dependencies"]) for name, item in value["scopes"].items()}
        self.assertEqual({"product", "community"}, {name for name, deps in scopes.items() if "access" in deps})
        self.assertEqual({"product", "community"}, {name for name, deps in scopes.items() if "composition" in deps})
        self.assertEqual({"product", "community"}, {name for name, deps in scopes.items() if "release" in deps})
        self.assertEqual({"core", "product", "community"}, {name for name, deps in scopes.items() if "authority-substrate" in deps})
        self.assertEqual({"frontier"}, {name for name, deps in scopes.items() if "observer" in deps})
        self.assertEqual(set(scopes), {name for name, deps in scopes.items() if "kernel" in deps})
        for role in ("semantic", "vector", "vector-space"):
            self.assertEqual({"core", "product", "community"}, {name for name, deps in scopes.items() if role in deps})
        for name, deps in scopes.items():
            self.assertIn("environment", deps)
            self.assertIn("input", deps)
            self.assertIn("acceptance-tool", deps)
            self.assertNotIn("acceptance-lifecycle", deps)
            self.assertTrue(
                {self.lifecycle[kind]["identity"] for kind in ("stateless", "derived", "authority")}.isdisjoint(
                    value["scopes"][name]["direct_dependencies"].values()
                )
            )
        self.assertNotIn("authority-substrate", self.components["kernel"]["direct_dependencies"])
        self.assertNotIn("semantic", self.components["kernel"]["direct_dependencies"])
        self.assertNotIn("vector", self.components["kernel"]["direct_dependencies"])
        self.assertNotIn("vector-space", self.components["kernel"]["direct_dependencies"])
        self.assertIn("authority-substrate", self.components["kernel-generation"]["direct_dependencies"])

    def test_lifecycle_maintenance_changes_do_not_change_report_scopes(self) -> None:
        manifests, scopes = self._current_fixture()
        first = self._binding(scopes, manifests)
        changed = copy.deepcopy(self.lifecycle)
        changed["evidence"]["identity"] = "9" * 64
        second = self._binding(scopes, manifests, lifecycle=changed)
        self.assertNotEqual(first["lifecycle"]["evidence"]["identity"], second["lifecycle"]["evidence"]["identity"])
        self.assertEqual(
            {scope: value["identity"] for scope, value in first["scopes"].items()},
            {scope: value["identity"] for scope, value in second["scopes"].items()},
        )

    def test_summary_checkpoint_has_own_generation_dependency(self) -> None:
        manifests, scopes = self._current_fixture()
        value = self._binding(scopes, manifests)
        dependencies = report_semantics.dependencies_for_mode(value, "summarize")
        self.assertEqual(self.reporting["summary"]["identity"], dependencies["summary-generation"])
        changed_reporting = copy.deepcopy(self.reporting)
        self._change_reporting(changed_reporting, "summary", "7" * 64)
        changed = self._binding(scopes, manifests, reporting=changed_reporting)
        self.assertEqual(value["scopes"], changed["scopes"])
        state = lifecycle.new_state(load_contract(self.suite / "contract.json"), value)
        for mode in ("core", "full", "longmemeval", "summarize"):
            digest = evidence_identity.canonical_sha256({"mode": mode})
            state["checkpoints"][mode] = {
                "binding": binding.for_mode(value, mode), "report_sha256": digest,
                "evidence_identity": evidence_identity.evidence_identity(
                    mode, digest, report_semantics.dependencies_for_mode(value, mode),
                ),
            }
        removed = lifecycle.rebind(load_contract(self.suite / "contract.json"), state, changed)
        self.assertEqual(["summarize"], removed)
        self.assertEqual({"core", "full", "longmemeval"}, set(state["checkpoints"]))

    def test_observer_change_invalidates_only_observer_evidence(self) -> None:
        manifests, scopes = self._current_fixture()
        value = self._binding(scopes, manifests)
        state = lifecycle.new_state(load_contract(self.suite / "contract.json"), value)
        for mode in ("frontier", "core", "qualification", "full", "longmemeval"):
            report_sha = evidence_identity.canonical_sha256({"mode": mode})
            scope = binding.scope_for_mode(mode)
            state["checkpoints"][mode] = {
                "binding": binding.for_mode(value, mode),
                "report_sha256": report_sha,
                "evidence_identity": evidence_identity.evidence_identity(
                    mode, report_sha, evidence_identity.scope_dependencies(value, scope),
                ),
            }
        changed = copy.deepcopy(value)
        frontier = changed["scopes"]["frontier"]
        frontier["report_binding"]["artifact_sha256"] = "9" * 64
        frontier["direct_dependencies"]["observer"] = "9" * 64
        frontier["identity"] = evidence_identity.dependency_identity("frontier", frontier["direct_dependencies"])
        removed = lifecycle.rebind(load_contract(self.suite / "contract.json"), state, changed)
        self.assertEqual(["frontier"], removed)
        self.assertEqual({"core", "qualification", "full", "longmemeval"}, set(state["checkpoints"]))

    def test_environment_change_is_local_and_identity_graph_rejects_cycles(self) -> None:
        manifests, scopes = self._current_fixture()
        value = self._binding(scopes, manifests)
        before = {name: item["identity"] for name, item in value["scopes"].items()}
        product = value["scopes"]["product"]
        product["report_binding"]["environment_sha256"] = "8" * 64
        product["direct_dependencies"]["environment"] = "8" * 64
        product["identity"] = evidence_identity.dependency_identity("product", product["direct_dependencies"])
        self.assertNotEqual(before["product"], product["identity"])
        self.assertEqual(before["core"], value["scopes"]["core"]["identity"])
        self.assertEqual(before["frontier"], value["scopes"]["frontier"]["identity"])
        invalid = copy.deepcopy(self.components)
        invalid["authority-substrate"]["direct_dependencies"] = {"product": invalid["product"]["identity"]}
        with self.assertRaisesRegex(evidence_identity.EvidenceIdentityError, "循环"):
            evidence_identity.validate_component_graph(invalid)

    def test_candidate_identity_is_stable_and_git_is_only_audit_source(self) -> None:
        repeated = evidence_identity.build_candidate_components(
            self.repository,
            "3e712f22f0529b4eef81b8826f8bb201bf9f6bf8",
            "f" * 64,
            "e" * 64,
        )
        self.assertEqual(self.components, repeated)
        self.assertNotIn("git", json.dumps(self.components, sort_keys=True))
        self.assertEqual(
            "5bf5e617f766d6756c412ac504ae77f5395b37fe2673101f5701230350ef6b99",
            self.components["kernel-generation"]["identity"],
        )

    def test_model_and_space_changes_do_not_leak_into_kernel_effect_or_frontier(self) -> None:
        catalog_path = self.repository / "manifests" / "kernel-generations" / "v1" / "catalog.json"
        catalog = json.loads(catalog_path.read_text(encoding="utf-8"))
        generation = next(
            item for item in catalog["generations"]
            if item["audit"]["source_git"] == "3e712f22f0529b4eef81b8826f8bb201bf9f6bf8"
        )
        semantic = next(item for item in generation["dependencies"] if item["role"] == "semantic")
        vector = next(item for item in generation["dependencies"] if item["role"] == "vector")
        semantic["identity"] = "7" * 64
        vector["identity"] = "8" * 64
        vector["config"]["space"] = "changed-space"
        with tempfile.TemporaryDirectory() as directory:
            changed_catalog = Path(directory) / "catalog.json"
            changed_catalog.write_text(json.dumps(catalog), encoding="utf-8")
            changed = evidence_identity.build_candidate_components(
                self.repository,
                "3e712f22f0529b4eef81b8826f8bb201bf9f6bf8",
                "f" * 64,
                "e" * 64,
                catalog_path=changed_catalog,
            )
        self.assertEqual(self.components["kernel"]["identity"], changed["kernel"]["identity"])
        manifests, scopes = self._current_fixture()
        before = self._binding(scopes, manifests)
        after = self._binding(scopes, manifests, components=changed)
        self.assertEqual(before["scopes"]["frontier"]["identity"], after["scopes"]["frontier"]["identity"])
        self.assertNotEqual(before["scopes"]["core"]["identity"], after["scopes"]["core"]["identity"])
        self.assertNotEqual(before["scopes"]["product"]["identity"], after["scopes"]["product"]["identity"])
        self.assertNotEqual(before["scopes"]["community"]["identity"], after["scopes"]["community"]["identity"])

    def _current_fixture(self) -> tuple[dict[str, dict], dict[str, dict[str, str]]]:
        scopes = {}
        manifests = {}
        for index, scope in enumerate(("frontier", "core", "product", "community"), start=1):
            environment = {"schema": "fixture-environment", "scope": scope}
            inputs = {"schema": "fixture-input", "scope": scope}
            tools = {
                "schema": "ownward.acceptance-tool-manifest/v4", "scope": scope,
                "repository_commit": "a" * 40,
                "files": [{"path": f"tool/{scope}.py", "sha256": str(index) * 64}],
            }
            manifests[f"{scope}-environment.json"] = environment
            manifests[f"{scope}-inputs.json"] = inputs
            manifests[f"{scope}-tools.json"] = tools
            scopes[scope] = {
                "environment_sha256": evidence_identity.canonical_sha256(environment),
                "input_manifest_sha256": evidence_identity.canonical_sha256(inputs),
                "tool_sha256": evidence_identity.canonical_sha256(tools),
                "artifact_sha256": "0" * 64 if scope == "frontier" else "f" * 64,
            }
        return manifests, scopes

    def _binding(
        self,
        scopes: dict[str, dict[str, str]],
        manifests: dict[str, dict],
        *,
        components: dict | None = None,
        lifecycle: dict | None = None,
        reporting: dict | None = None,
    ) -> dict:
        return evidence_identity.build_current_binding(
            candidate=self.candidate,
            suite_version="1.0.0",
            scopes=scopes,
            components=self.components if components is None else components,
            manifests=manifests,
            lifecycle=self.lifecycle if lifecycle is None else lifecycle,
            reporting=self.reporting if reporting is None else reporting,
            audit={"source_git": self.candidate},
        )

    @staticmethod
    def _change_reporting(value: dict, name: str, digest: str) -> None:
        value[name]["content"]["sha256"] = digest
        value[name]["identity"] = evidence_identity.canonical_sha256({
            "schema": value[name]["schema"], "content": value[name]["content"],
        })


if __name__ == "__main__":
    unittest.main()
