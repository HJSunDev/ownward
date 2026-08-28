import copy
import json
import tempfile
import unittest
from pathlib import Path

import lifecycle
import evidence
import evidence_identity
import binding as candidate_binding
from contract import load_contract


class EvidenceLifecycleTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.root = Path(__file__).resolve().parent
        cls.contract = load_contract(cls.root / "contract.json")
        cls.binding = {
            "schema": "ownward.acceptance-binding/v4",
            "suite_version": "1.0.0",
            "candidate": "a" * 40,
            "scopes": {
                name: {
                    "environment_sha256": values[0] * 64,
                    "input_manifest_sha256": values[1] * 64,
                    "tool_sha256": values[2] * 64,
                    "artifact_sha256": values[3] * 64,
                }
                for name, values in {
                    "frontier": "cdef", "core": "f01b", "product": "234b", "community": "567b",
                }.items()
            },
        }

    def state(self):
        return lifecycle.new_state(self.contract, self.binding)

    def checkpoint(self, state, name, passed=True, report=None, directory=None):
        report_sha256 = name[0] * 64
        if state.get("schema") == evidence_identity.STATE_SCHEMA:
            report_sha256 = evidence_identity.canonical_sha256({"mode": name})
        state["checkpoints"][name] = {
            "binding": candidate_binding.for_mode(state["binding"], name),
            "report_sha256": report_sha256,
            "passed": passed,
            "elapsed_seconds": 1,
        }
        if state.get("schema") == evidence_identity.STATE_SCHEMA:
            scope = candidate_binding.scope_for_mode(name)
            state["checkpoints"][name]["evidence_identity"] = evidence_identity.evidence_identity(
                name,
                state["checkpoints"][name]["report_sha256"],
                evidence_identity.scope_dependencies(state["binding"], scope),
            )
        if report is not None:
            path = Path(directory) / f"{name}.json"
            raw = Path(directory) / "evidence" / f"{name}.txt"
            raw.parent.mkdir(exist_ok=True)
            raw.write_text(f"raw evidence for {name}\n", encoding="utf-8")
            evidence.attach_artifacts(report, path, [raw])
            path.write_text(json.dumps(report), encoding="utf-8")
            state["checkpoints"][name].update({
                "report_path": str(path),
                "report_sha256": lifecycle.file_sha256(path),
                "artifact_manifest_sha256": evidence.validate_report_artifacts(path, report),
            })
            if state.get("schema") == evidence_identity.STATE_SCHEMA:
                scope = candidate_binding.scope_for_mode(name)
                state["checkpoints"][name]["evidence_identity"] = evidence_identity.evidence_identity(
                    name,
                    state["checkpoints"][name]["report_sha256"],
                    evidence_identity.scope_dependencies(state["binding"], scope),
                )

    def test_change_scope_selects_only_required_levels(self):
        self.assertEqual([], lifecycle.plan_for_impacts(["local"]))
        self.assertEqual(["targeted"], lifecycle.plan_for_impacts(["retrieval"]))
        self.assertEqual(["core"], lifecycle.plan_for_impacts(["asset"]))
        self.assertEqual(
            ["targeted"],
            lifecycle.plan_for_impacts(["organization"]),
        )
        self.assertEqual(["core", "frontier", "qualification"], lifecycle.plan_for_stage("kernel-baseline"))
        self.assertEqual(
            ["indexing", "lexical", "vector", "graph", "context", "fusion"],
            lifecycle.stages_for_impacts(["retrieval"]),
        )

    def test_illegal_higher_cost_start_is_rejected(self):
        with self.assertRaisesRegex(lifecycle.LifecycleError, "前置证据"):
            lifecycle.can_start(self.contract, self.state(), "qualification")

    def test_failed_checkpoint_is_kept_but_does_not_unlock_next_layer(self):
        state = self.state()
        self.checkpoint(state, "core", passed=False)
        self.checkpoint(state, "frontier", passed=True)
        with self.assertRaisesRegex(lifecycle.LifecycleError, "未通过"):
            lifecycle.can_start(self.contract, state, "qualification")

    def test_changed_prerequisite_evidence_is_rejected_before_higher_cost_layer(self):
        state = self.state()
        with tempfile.TemporaryDirectory() as directory:
            self.checkpoint(state, "core", report=self.core_report(), directory=directory)
            self.checkpoint(state, "frontier", report=self.frontier_report("full"), directory=directory)
            raw = Path(directory) / "evidence" / "core.txt"
            raw.write_text("changed\n", encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "原始证据"):
                lifecycle.can_start(self.contract, state, "qualification")

    def test_local_invalidation_preserves_unaffected_evidence(self):
        state = self.state()
        for name in ("targeted", "core", "frontier", "qualification", "full", "longmemeval"):
            self.checkpoint(state, name)
        removed = lifecycle.invalidate(self.contract, state, "frontier")
        self.assertEqual(["frontier"], removed)
        self.assertEqual({"targeted", "core", "qualification", "full", "longmemeval"}, set(state["checkpoints"]))
        self.assertEqual({"frontier"}, set(state["invalidated_reports"]))

    def test_explicitly_invalidated_report_cannot_be_recovered_as_current(self):
        state = self.state()
        with tempfile.TemporaryDirectory() as directory:
            self.checkpoint(state, "core", report=self.core_report(), directory=directory)
            report_path = Path(state["checkpoints"]["core"]["report_path"])
            lifecycle.invalidate(self.contract, state, "core")
            self.assertTrue(lifecycle.report_was_invalidated(state, "core", report_path))
            report_path.write_text("new interrupted execution\n", encoding="utf-8")
            self.assertFalse(lifecycle.report_was_invalidated(state, "core", report_path))

    def test_binding_change_invalidates_all_and_identical_binding_reuses(self):
        state = self.state()
        self.checkpoint(state, "core")
        state["baseline"] = {"candidate": "previous"}
        self.assertEqual([], lifecycle.rebind(self.contract, state, copy.deepcopy(self.binding)))
        changed = copy.deepcopy(self.binding)
        changed["scopes"]["core"]["tool_sha256"] = "e" * 64
        self.assertEqual(["core"], lifecycle.rebind(self.contract, state, changed))
        self.assertFalse(state["checkpoints"])
        self.assertIsNone(state["baseline"])

    def test_state_owns_nested_binding_snapshots(self):
        initial = copy.deepcopy(self.binding)
        state = lifecycle.new_state(self.contract, initial)
        initial["scopes"]["core"]["tool_sha256"] = "9" * 64
        self.assertEqual("1" * 64, state["binding"]["scopes"]["core"]["tool_sha256"])

        replacement = copy.deepcopy(self.binding)
        replacement["scopes"]["product"]["tool_sha256"] = "f" * 64
        lifecycle.rebind(self.contract, state, replacement)
        replacement["scopes"]["product"]["tool_sha256"] = "0" * 64
        self.assertEqual("f" * 64, state["binding"]["scopes"]["product"]["tool_sha256"])

    def test_environment_input_or_tool_change_archives_active_baseline(self):
        state = self.state()
        state["baseline"] = {"candidate": "previous"}
        changed = copy.deepcopy(self.binding)
        changed["scopes"]["product"]["input_manifest_sha256"] = "f" * 64
        lifecycle.rebind(self.contract, state, changed)
        self.assertIsNone(state["baseline"])
        self.assertEqual("previous", state["baseline_history"][0]["candidate"])

    def test_adding_deferred_community_binding_preserves_internal_checkpoints(self):
        initial = copy.deepcopy(self.binding)
        del initial["scopes"]["community"]
        state = lifecycle.new_state(self.contract, initial)
        for name in ("core", "frontier", "qualification"):
            self.checkpoint(state, name)
        self.assertEqual([], lifecycle.rebind(self.contract, state, copy.deepcopy(self.binding)))
        self.assertEqual({"core", "frontier", "qualification"}, set(state["checkpoints"]))

    def test_community_binding_change_only_invalidates_community_and_summary(self):
        state = self.state()
        for name in ("core", "frontier", "qualification", "full", "longmemeval", "summarize"):
            self.checkpoint(state, name)
        changed = copy.deepcopy(self.binding)
        changed["scopes"]["community"]["input_manifest_sha256"] = "f" * 64
        self.assertEqual(["longmemeval", "summarize"], lifecycle.rebind(self.contract, state, changed))
        self.assertEqual({"core", "frontier", "qualification", "full"}, set(state["checkpoints"]))

    def test_state_round_trip_is_a_resume_checkpoint(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "state.json"
            state = self.state()
            self.checkpoint(state, "core")
            lifecycle.save_state(path, state)
            self.assertEqual(state, lifecycle.load_state(path))

    def test_cost_overrun_is_rejected_before_checkpoint(self):
        state = self.state()
        report = self.frontier_report("targeted")
        with self.assertRaisesRegex(lifecycle.LifecycleError, "成本上限"):
            lifecycle.record(self.contract, state, "targeted", report, "f" * 64, 181)
        self.assertFalse(state["checkpoints"])

    def test_identical_report_is_reused(self):
        state = self.state()
        with tempfile.TemporaryDirectory() as directory:
            report = self.frontier_report("targeted")
            report_path = Path(directory) / "targeted.json"
            raw = Path(directory) / "evidence" / "targeted.json"
            raw.parent.mkdir()
            raw.write_text("{}\n", encoding="utf-8")
            evidence.attach_artifacts(report, report_path, [raw])
            report_path.write_text(json.dumps(report), encoding="utf-8")
            digest = lifecycle.file_sha256(report_path)
            self.assertEqual("recorded", lifecycle.record(
                self.contract, state, "targeted", report, digest, 1, str(report_path)
            ))
            self.assertEqual("reused", lifecycle.record(
                self.contract, state, "targeted", report, digest, 1, str(report_path)
            ))

    def test_targeted_stage_selection_is_part_of_checkpoint_identity(self):
        state = self.state()
        with tempfile.TemporaryDirectory() as directory:
            report = self.frontier_report("targeted")
            report_path = Path(directory) / "targeted.json"
            raw = Path(directory) / "evidence" / "targeted.json"
            raw.parent.mkdir()
            raw.write_text("{}\n", encoding="utf-8")
            evidence.attach_artifacts(report, report_path, [raw])
            report_path.write_text(json.dumps(report), encoding="utf-8")
            digest = lifecycle.file_sha256(report_path)
            lexical = {"targeted_stages": ["lexical"]}
            vector = {"targeted_stages": ["vector"]}
            lifecycle.record(self.contract, state, "targeted", report, digest, 1, str(report_path), lexical)
            self.assertEqual(report_path, lifecycle.reusable_report(self.contract, state, "targeted", lexical))
            self.assertIsNone(lifecycle.reusable_report(self.contract, state, "targeted", vector))

    def test_formal_checkpoint_cannot_exist_only_in_memory(self):
        state = self.state()
        report = self.frontier_report("targeted")
        with self.assertRaisesRegex(lifecycle.LifecycleError, "落盘报告"):
            lifecycle.record(self.contract, state, "targeted", report, "f" * 64, 1)

    def test_summary_is_blocked_when_community_quality_is_not_determined(self):
        state = self.state()
        with tempfile.TemporaryDirectory() as directory:
            self.checkpoint(state, "core", report=self.core_report(), directory=directory)
            self.checkpoint(state, "full", report=self.product_report("full"), directory=directory)
            self.checkpoint(state, "longmemeval", passed=False, report=self.community_report(), directory=directory)
            with self.assertRaisesRegex(lifecycle.LifecycleError, "未通过"):
                lifecycle.summarize(self.contract, state)

    def test_summary_rejects_missing_or_changed_evidence(self):
        state = self.state()
        for name in ("core", "full", "longmemeval"):
            self.checkpoint(state, name)
        with self.assertRaisesRegex(lifecycle.LifecycleError, "报告缺失"):
            lifecycle.summarize(self.contract, state)

    def test_summary_cannot_be_recorded_from_undetermined_community_quality(self):
        state = self.state()
        with tempfile.TemporaryDirectory() as directory:
            self.checkpoint(state, "core", report=self.core_report(), directory=directory)
            self.checkpoint(state, "full", report=self.product_report("full"), directory=directory)
            self.checkpoint(state, "longmemeval", passed=False, report=self.community_report(), directory=directory)
            with self.assertRaisesRegex(lifecycle.LifecycleError, "未通过"):
                lifecycle.summarize(self.contract, state)

    def test_promotion_requires_core_frontier_and_qualification(self):
        state = self.state()
        with tempfile.TemporaryDirectory() as directory:
            self.checkpoint(state, "frontier", report=self.frontier_report("full"), directory=directory)
            with self.assertRaisesRegex(lifecycle.LifecycleError, "固定内核"):
                lifecycle.promote_baseline(self.contract, state)
            self.checkpoint(state, "core", report=self.core_report(), directory=directory)
            self.checkpoint(state, "qualification", report=self.product_report("qualification"), directory=directory)
            lifecycle.promote_baseline(self.contract, state)
        self.assertEqual(self.binding["candidate"], state["baseline"]["candidate"])
        self.assertEqual(state["checkpoints"]["frontier"]["report_sha256"], state["baseline"]["frontier_report_sha256"])
        self.assertEqual("full", state["baseline"]["reports"]["frontier"]["value"]["mode"])

    def test_raw_evidence_change_invalidates_reuse_and_summary(self):
        state = self.state()
        with tempfile.TemporaryDirectory() as directory:
            self.checkpoint(state, "core", report=self.core_report(), directory=directory)
            self.checkpoint(state, "full", report=self.product_report("full"), directory=directory)
            self.checkpoint(state, "longmemeval", report=self.community_report(), directory=directory)
            raw = Path(directory) / "evidence" / "core.txt"
            raw.write_text("changed\n", encoding="utf-8")
            self.assertIsNone(lifecycle.reusable_report(self.contract, state, "core"))
            with self.assertRaisesRegex(ValueError, "原始证据"):
                lifecycle.summarize(self.contract, state)

    def test_promoted_baseline_embeds_observation_before_workspace_cleanup(self):
        state = self.state()
        with tempfile.TemporaryDirectory() as directory:
            self.checkpoint(state, "core", report=self.core_report(), directory=directory)
            self.checkpoint(state, "frontier", report=self.frontier_report("full"), directory=directory)
            self.checkpoint(state, "qualification", report=self.product_report("qualification"), directory=directory)
            observation_path = Path(directory) / "observation.json"
            observation = {"schema": "fixture", "metrics": [{"name": "recall", "value": 1.0}]}
            observation_path.write_text(json.dumps(observation), encoding="utf-8")
            state["checkpoints"]["frontier"].update({
                "observation_path": str(observation_path),
                "observation_sha256": lifecycle.file_sha256(observation_path),
            })
            lifecycle.promote_baseline(self.contract, state)
        embedded = state["baseline"]["observations"]["full"]
        self.assertEqual(observation, embedded["value"])
        self.assertEqual(lifecycle.canonical_sha256(observation), embedded["canonical_sha256"])

    def test_v3_promotion_and_history_use_one_complete_baseline_contract(self):
        repository = self.root.parents[2]
        components = evidence_identity.build_candidate_components(
            repository,
            "3e712f22f0529b4eef81b8826f8bb201bf9f6bf8",
            "b" * 64,
            "e" * 64,
        )
        manifests = {}
        for scope in self.binding["scopes"]:
            manifests[f"{scope}-tools.json"] = {
                "schema": "ownward.acceptance-tool-manifest/v4",
                "scope": scope,
                "repository_commit": "a" * 40,
                "files": [{"path": f"tool/{scope}.py", "sha256": "c" * 64}],
            }
        direct = evidence_identity.build_binding(
            self.binding, components, manifests, evidence_identity.lifecycle_identities(repository),
            evidence_identity.reporting_identities(repository),
        )
        state = lifecycle.new_state(self.contract, direct)
        original = self.binding
        self.binding = direct
        try:
            with tempfile.TemporaryDirectory() as directory:
                self.checkpoint(state, "core", report=self.core_report(), directory=directory)
                self.checkpoint(state, "frontier", report=self.frontier_report("full"), directory=directory)
                self.checkpoint(state, "qualification", report=self.product_report("qualification"), directory=directory)
                lifecycle.promote_baseline(self.contract, state)
                lifecycle._validate_state(self.contract, state)
        finally:
            self.binding = original
        baseline = state["baseline"]
        self.assertEqual(
            {"product_identity", "direct_dependencies", "evidence_identities", "identity"},
            {name for name in baseline if name in {"product_identity", "direct_dependencies", "evidence_identities", "identity"}},
        )
        tampered = copy.deepcopy(state)
        tampered["baseline_history"].append(tampered.pop("baseline"))
        tampered["baseline_history"][0]["direct_dependencies"]["core"]["kernel"] = "0" * 64
        with self.assertRaisesRegex(lifecycle.LifecycleError, "基线历史 0"):
            lifecycle._validate_state(self.contract, tampered)
        identity_drift = copy.deepcopy(state)
        identity_drift["baseline"]["identity"] = "0" * 64
        with self.assertRaisesRegex(lifecycle.LifecycleError, "identity"):
            lifecycle._validate_state(self.contract, identity_drift)
        report_drift = copy.deepcopy(state)
        report_drift["baseline"]["reports"]["core"]["canonical_sha256"] = "0" * 64
        with self.assertRaisesRegex(lifecycle.LifecycleError, "报告摘要漂移"):
            lifecycle._validate_state(self.contract, report_drift)
        checkpoint_drift = copy.deepcopy(state)
        checkpoint_drift["checkpoints"]["core"]["report_sha256"] = "0" * 64
        checkpoint_drift["checkpoints"]["core"]["evidence_identity"] = evidence_identity.evidence_identity(
            "core", "0" * 64, evidence_identity.scope_dependencies(state["binding"], "core"),
        )
        with self.assertRaisesRegex(lifecycle.LifecycleError, "活动基线 core"):
            lifecycle._validate_state(self.contract, checkpoint_drift)

    def frontier_report(self, mode):
        active = candidate_binding.for_mode(self.binding, "frontier")
        return {
            "schema": "ownward.frontier-report/v1", "suite_version": "1.0.0",
            "benchmark_version": "ownward-core-frontier/v1", "mode": mode,
            "candidate": evidence_identity.source_git(self.binding), "baseline": "baseline",
            "environment": {"sha256": active["environment_sha256"]},
            "inputs": {"sha256": active["input_manifest_sha256"]},
            "quality": [], "latency": [], "resources": [], "diagnostics": {},
            "decision": "eligible_for_qualification", "started_at": "x", "finished_at": "y",
        }

    def core_report(self):
        active = candidate_binding.for_mode(self.binding, "core")
        return {
            "schema": "ownward.core-baseline-report/v1", "suite_version": "1.0.0",
            "candidate": evidence_identity.source_git(self.binding), "binary_sha256": active["binary_sha256"],
            "environment": {"sha256": active["environment_sha256"]},
            "inputs": {"sha256": active["input_manifest_sha256"]},
            "invariants": {name: True for name in self.contract["evidence_layers"]["core"]["required_invariants"]},
            "passed": True, "started_at": "x", "finished_at": "y",
        }

    def product_report(self, mode):
        active = candidate_binding.for_mode(self.binding, mode)
        count = 2 if mode == "qualification" else 6
        return {
            "schema": "ownward.product-report/v1", "suite_version": "1.0.0",
            "dataset_version": "ownward-product-dataset/v2", "mode": mode,
            "candidate": evidence_identity.source_git(self.binding), "binary_sha256": active["binary_sha256"],
            "environment": {"sha256": active["environment_sha256"]},
            "inputs": {"sha256": active["input_manifest_sha256"]},
            "categories": {name: {"scenarios": count, "passed": True} for name in self.contract["evidence_layers"]["product"]["categories"]},
            "organization_gain": {"passed": True}, "quality": {"passed": True},
            "latency": {"passed": True}, "resources": {"passed": True}, "passed": True,
            "started_at": "x", "finished_at": "y",
        }

    def community_report(self):
        active = candidate_binding.for_mode(self.binding, "longmemeval")
        return {
            "schema": "ownward.longmemeval-report/v1", "suite_version": "1.0.0",
            "official_version": "longmemeval-s/9e0b455f4ef0e2ab8f2e582289761153549043fc+d6f21ea9",
            "profile": "Ownward LongMemEval-S Production Profile",
            "candidate": evidence_identity.source_git(self.binding), "binary_sha256": active["binary_sha256"],
            "environment": {"sha256": active["environment_sha256"]},
            "inputs": {"sha256": active["input_manifest_sha256"]},
            "capabilities": self.contract["evidence_layers"]["community"]["capabilities"],
            "benchmark": {"questions": 500, "complete": True, "question_types": list(self.contract["evidence_layers"]["community"]["question_types"])},
            "execution": {"complete": True, "protocol_valid": True, "evidence_complete": True, "passed": True},
            "quality": {
                "accuracy": 0.83, "comparison_policy": "equivalent-profile-only", "hard_accuracy_threshold": None,
                "score_complete": True, "assessment_status": "not_determined",
                "assessment_basis": "no-equivalent-production-profile-reference",
                "first_version_condition_satisfied": False, "passed": None,
            },
            "retrieval": {"mean_ms": 10.0, "p95_ms": 20.0, "max_ms": 30.0},
            "cost": {"wall_seconds": 14400.0, "within_budget": True},
            "diagnostics": {"questions": 500, "complete": True, "post_answer_only": True, "excluded_from_product_execution_and_scoring": True},
            "submission": {
                "package_sha256": "f" * 64, "official_evaluation_sha256": "e" * 64,
                "hypotheses_sha256": "d" * 64, "diagnostics_sha256": "b" * 64,
                "diagnostic_summary_sha256": "a" * 64, "checkpoint_manifest_sha256": "c" * 64,
            },
            "completion": {"status": "not_satisfied", "reason": "community-quality-not-determined"},
            "passed": False,
            "started_at": "x", "finished_at": "y",
        }


if __name__ == "__main__":
    unittest.main()
