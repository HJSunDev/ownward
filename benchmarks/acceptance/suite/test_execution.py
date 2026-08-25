import copy
import json
from pathlib import Path
import tempfile
import textwrap
import time
import unittest
from unittest import mock

import execution
import execution_product
import evidence
import lifecycle
import binding as candidate_binding
from contract import load_contract


class UnifiedExecutionTests(unittest.TestCase):
    def setUp(self) -> None:
        self.root = Path(__file__).resolve().parent
        self.repository = self.root.parents[2]
        (self.repository / ".tmp").mkdir(exist_ok=True)
        self.temporary = tempfile.TemporaryDirectory(dir=self.repository / ".tmp")
        self.workspace = Path(self.temporary.name)
        self.contract = load_contract(self.root / "contract.json")
        self.binding = {
            "schema": "ownward.acceptance-binding/v4",
            "suite_version": self.contract["suite_version"], "candidate": "c" * 40,
            "scopes": {
                name: {
                    "environment_sha256": values[0] * 64,
                    "input_manifest_sha256": values[1] * 64,
                    "tool_sha256": values[2] * 64,
                    "artifact_sha256": values[3] * 64,
                }
                for name, values in {
                    "frontier": "bdea", "core": "f01a", "product": "234a", "community": "567a",
                }.items()
            },
        }
        self.state_path = self.workspace / "state.json"
        self.tool = self.workspace / "frontier.py"
        self.tool.write_text(textwrap.dedent("""
            import argparse, hashlib, json
            from pathlib import Path
            parser=argparse.ArgumentParser()
            for name in ('materials','candidate','mode','environment-sha256','input-manifest-sha256','repository','output','stages'):
                parser.add_argument('--'+name)
            a=parser.parse_args()
            tool=hashlib.sha256(Path(__file__).read_bytes()).hexdigest()
            material=hashlib.sha256(Path(a.materials).read_bytes()).hexdigest()
            report={'schema':'ownward.core-frontier-observation/v1','suite_version':'1.0.0','candidate':a.candidate,
              'materials_sha256':material,'input_manifest_sha256':getattr(a,'input_manifest_sha256'),'mode':a.mode,
              'requested_stages':['lexical'],
              'environment':{'sha256':getattr(a,'environment_sha256')},'tool_sha256':tool,
              'metrics':[{'name':'lexical_recall','dimension':'quality','stage':'lexical','value':1.0,'direction':'higher','repeatability_error':0.0,'materiality':0.005,'protected':True}]}
            Path(a.output).parent.mkdir(parents=True,exist_ok=True)
            Path(a.output).write_text(json.dumps(report),encoding='utf-8')
        """), encoding="utf-8")
        self.binding["scopes"]["frontier"]["artifact_sha256"] = lifecycle.file_sha256(self.tool)
        lifecycle.save_state(self.state_path, lifecycle.new_state(self.contract, self.binding))
        self.config = {
            "schema": "ownward.acceptance-execution/v3", "repository": str(self.repository),
            "workspace": str(self.workspace / "work"), "binding_dir": str(self.workspace / "binding"),
            "enabled_scopes": ["frontier"],
            "frontier": {"tool": str(self.tool), "targeted_stages": ["lexical"]}, "product": {},
        }

    def tearDown(self) -> None:
        self.temporary.cleanup()

    @mock.patch("execution.binding.verify_current")
    def test_execute_records_and_reuses_one_real_entry(self, verify_current) -> None:
        first = execution.execute(self.root, self.contract, self.state_path, "targeted", self.config, resume=False)
        self.assertEqual("recorded", first["outcome"])
        second = execution.execute(self.root, self.contract, self.state_path, "targeted", self.config, resume=True)
        self.assertEqual("reused", second["outcome"])
        self.assertEqual(2, verify_current.call_count)

    @mock.patch("execution.binding.verify_current")
    def test_completed_report_is_recovered_without_rerun(self, _verify_current) -> None:
        first = execution.execute(self.root, self.contract, self.state_path, "targeted", self.config, resume=False)
        state = lifecycle.load_state(self.state_path)
        state["checkpoints"].clear()
        lifecycle.save_state(self.state_path, state)
        self.tool.unlink()
        recovered = execution.execute(self.root, self.contract, self.state_path, "targeted", self.config, resume=True)
        self.assertTrue(recovered["recovered"])
        self.assertEqual(first["report"], recovered["report"])

    @mock.patch("execution.binding.verify_current")
    def test_explicit_invalidation_does_not_recover_the_old_final_report(self, _verify_current) -> None:
        first = execution.execute(self.root, self.contract, self.state_path, "targeted", self.config, resume=False)
        old_report = Path(first["report"])
        old_bytes = old_report.read_bytes()
        old_digest = lifecycle.file_sha256(old_report)
        state = lifecycle.load_state(self.state_path)
        lifecycle.invalidate(self.contract, state, "targeted")
        lifecycle.save_state(self.state_path, state)
        rerun = execution.execute(self.root, self.contract, self.state_path, "targeted", self.config, resume=True)
        self.assertEqual("recorded", rerun["outcome"])
        self.assertNotIn("recovered", rerun)
        archived = old_report.parent / "_audit" / "targeted" / f"{old_digest}.json"
        self.assertEqual(old_bytes, archived.read_bytes())

    @mock.patch("execution.binding.verify_current")
    def test_reuse_still_revalidates_current_candidate_and_environment(self, verify_current) -> None:
        execution.execute(self.root, self.contract, self.state_path, "targeted", self.config, resume=False)
        verify_current.side_effect = ValueError("binding changed")
        with self.assertRaisesRegex(ValueError, "binding changed"):
            execution.execute(self.root, self.contract, self.state_path, "targeted", self.config, resume=True)

    @mock.patch("execution.binding.verify_current")
    def test_partial_report_is_discarded_and_only_current_layer_restarts(self, _verify_current) -> None:
        report = Path(self.config["workspace"]) / "reports" / "targeted.json"
        report.parent.mkdir(parents=True)
        report.write_text('{"schema":', encoding="utf-8")
        result = execution.execute(self.root, self.contract, self.state_path, "targeted", self.config, resume=True)
        self.assertEqual("recorded", result["outcome"])
        self.assertEqual("ownward.frontier-report/v1", json.loads(report.read_text(encoding="utf-8"))["schema"])

    @mock.patch("execution.binding.verify_current")
    def test_timeout_stops_without_checkpoint(self, _verify_current) -> None:
        self.tool.write_text("import time; time.sleep(2)\n", encoding="utf-8")
        state = lifecycle.load_state(self.state_path)
        state["binding"]["scopes"]["frontier"]["artifact_sha256"] = lifecycle.file_sha256(self.tool)
        lifecycle.save_state(self.state_path, state)
        contract = copy.deepcopy(self.contract)
        contract["optimization_loop"]["modes"]["targeted"]["max_wall_seconds"] = 0.05
        with self.assertRaisesRegex(execution.ExecutionError, "已停止"):
            execution.execute(self.root, contract, self.state_path, "targeted", self.config, resume=False)
        self.assertFalse(lifecycle.load_state(self.state_path)["checkpoints"])

    def test_failed_resource_admission_cannot_resume_into_product_run(self) -> None:
        state = lifecycle.load_state(self.state_path)
        state["binding"] = candidate_binding.for_mode(self.binding, "qualification")
        report = {
            "schema": "ownward.delivery-resource-report/v1",
            "candidate": self.binding["candidate"],
            "release_binary_sha256": self.binding["scopes"]["product"]["artifact_sha256"],
            "resource_binding": {"schema": "fixture"},
            "passed": False,
        }
        with self.assertRaisesRegex(execution.ExecutionError, "停止高成本"):
            execution_product.require_resource_admission(report, state, resource_binding={"schema": "fixture"})

    def test_valid_shared_resource_evidence_is_reused_across_product_modes(self) -> None:
        state = lifecycle.load_state(self.state_path)
        workspace = Path(self.config["workspace"])
        report_path = workspace / "evidence" / "product-resource" / "report.json"
        report_path.parent.mkdir(parents=True)
        raw = report_path.parent / "raw"
        raw.mkdir()
        artifacts = {}
        for name in ("package_manifest", "process_samples", "workload_results"):
            path = raw / f"{name}.json"
            path.write_text("{}\n", encoding="utf-8")
            artifacts[name] = {"path": str(path), "sha256": lifecycle.file_sha256(path)}
        report = {
            "schema": "ownward.delivery-resource-report/v1",
            "candidate": self.binding["candidate"],
            "release_binary_sha256": self.binding["scopes"]["product"]["artifact_sha256"],
            "resource_binding": {"schema": "fixture", "candidate": self.binding["candidate"]},
            "evidence": artifacts,
            "passed": True,
        }
        report_path.write_text(json.dumps(report), encoding="utf-8")
        resource_binding = report["resource_binding"]
        state["binding"] = candidate_binding.for_mode(self.binding, "qualification")
        execution_product.require_resource_admission(report, state, report_path, resource_binding=resource_binding)
        state["binding"] = candidate_binding.for_mode(self.binding, "full")
        execution_product.require_resource_admission(report, state, report_path, resource_binding=resource_binding)
        (raw / "process_samples.json").write_text("changed\n", encoding="utf-8")
        with self.assertRaisesRegex(execution.ExecutionError, "发生变化"):
            execution_product.require_resource_admission(report, state, report_path, resource_binding=resource_binding)

    def test_product_resource_admission_consumes_the_same_layer_budget(self) -> None:
        state = lifecycle.load_state(self.state_path)
        with (
            mock.patch("execution_product.product_binary", return_value=self.tool),
            mock.patch("execution_product.resource_report", return_value=self.tool),
        ):
            with self.assertRaisesRegex(execution.ExecutionError, "耗尽该层总成本预算"):
                execution_product.execute_product(
                    self.root,
                    self.contract,
                    state,
                    "qualification",
                    self.config,
                    Path(self.config["workspace"]),
                    False,
                    deadline=time.perf_counter() - 1,
                )

    def test_cached_product_preflight_must_fit_the_current_remaining_budget(self) -> None:
        report = {"projected": {"wall_seconds": 120.0}}
        with self.assertRaisesRegex(execution.ExecutionError, "剩余预算"):
            execution_product._require_product_preflight_budget(report, time.perf_counter() + 100.0)
        execution_product._require_product_preflight_budget(report, time.perf_counter() + 181.0)

    def test_product_report_artifacts_exclude_other_modes_and_scenarios(self) -> None:
        workspace = Path(self.config["workspace"])
        product = workspace / "evidence" / "product"
        selected = product / "scenarios" / "selected"
        unrelated = product / "scenarios" / "unrelated"
        resource = workspace / "evidence" / "product-resource"
        qualification_result = product / "results" / "qualification.json"
        full_result = product / "results" / "full.json"
        for path in (selected / "trace.json", unrelated / "trace.json", qualification_result, full_result, resource / "report.json"):
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("{}\n", encoding="utf-8")
        report = {"quality": {"scenarios": [{"scenario_id": "selected"}]}}
        report_path = workspace / "reports" / "qualification.json"

        evidence.attach_artifacts(
            report,
            report_path,
            execution_product.product_artifact_paths(workspace, "qualification", report),
        )
        paths = {item["path"] for item in report["artifacts"]["files"]}
        self.assertIn("evidence/product/results/qualification.json", paths)
        self.assertIn("evidence/product/scenarios/selected/trace.json", paths)
        self.assertNotIn("evidence/product/results/full.json", paths)
        self.assertNotIn("evidence/product/scenarios/unrelated/trace.json", paths)

        full_result.write_text('{"changed": true}\n', encoding="utf-8")
        (unrelated / "trace.json").write_text('{"changed": true}\n', encoding="utf-8")
        evidence.validate_report_artifacts(report_path, report)
        (selected / "trace.json").write_text('{"changed": true}\n', encoding="utf-8")
        with self.assertRaisesRegex(evidence.EvidenceError, "原始证据.*变化"):
            evidence.validate_report_artifacts(report_path, report)

    def test_incomplete_product_preflight_resumes_its_scenario_recovery(self) -> None:
        workspace = Path(self.config["workspace"])
        root = workspace / "evidence" / "product-preflight"
        root.mkdir(parents=True)
        tasks = {"schema": "tasks", "items": []}
        tasks_path = workspace / "tasks.json"
        binding_path = workspace / "binding.json"
        resource = workspace / "resource.json"
        for path in (tasks_path, binding_path, resource):
            path.write_text("{}\n", encoding="utf-8")
        state = lifecycle.load_state(self.state_path)
        state["binding"] = candidate_binding.for_mode(self.binding, "qualification")

        def complete(command: list[str], **_kwargs: object) -> None:
            self.assertIn("--resume", command)
            execution_product._write_json(root / "report.json", {
                "passed": True,
                "qualification_binding": state["binding"],
                "task_set_sha256": execution_product._json_sha256(tasks),
                "resource_report_sha256": lifecycle.file_sha256(resource),
                "projected": {"wall_seconds": 1.0},
            })

        with (
            mock.patch("execution_product._product_command", return_value=["adapter"]),
            mock.patch("execution_product.run", side_effect=complete),
        ):
            execution_product._ensure_product_preflight(
                self.root, state, self.config, workspace, tasks, tasks_path, binding_path, resource,
                time.perf_counter() + 120.0, resume=True,
            )

    def test_failed_product_preflight_report_is_archived_before_resume(self) -> None:
        workspace = Path(self.config["workspace"])
        root = workspace / "evidence" / "product-preflight"
        tasks = {"schema": "tasks", "items": []}
        tasks_path = workspace / "tasks.json"
        binding_path = workspace / "binding.json"
        resource = workspace / "resource.json"
        for path in (tasks_path, binding_path, resource):
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("{}\n", encoding="utf-8")
        state = lifecycle.load_state(self.state_path)
        state["binding"] = candidate_binding.for_mode(self.binding, "qualification")
        failed = {
            "passed": False,
            "qualification_binding": state["binding"],
            "task_set_sha256": execution_product._json_sha256(tasks),
            "resource_report_sha256": lifecycle.file_sha256(resource),
            "projected": {"wall_seconds": 1600.0},
        }
        execution_product._write_json(root / "report.json", failed)

        def complete(_command: list[str], **_kwargs: object) -> None:
            execution_product._write_json(root / "report.json", {
                **failed,
                "passed": True,
                "projected": {"wall_seconds": 1.0},
            })

        with (
            mock.patch("execution_product._product_command", return_value=["adapter"]),
            mock.patch("execution_product.run", side_effect=complete),
        ):
            execution_product._ensure_product_preflight(
                self.root, state, self.config, workspace, tasks, tasks_path, binding_path, resource,
                time.perf_counter() + 120.0, resume=True,
            )
        archived = root / "_audit" / "reports" / f"{execution_product._json_sha256(failed)}.json"
        self.assertEqual(failed, json.loads(archived.read_text(encoding="utf-8")))

    def test_product_workspace_binding_migrates_only_the_same_candidate_binary(self) -> None:
        workspace = Path(self.config["workspace"])
        previous = candidate_binding.for_mode(self.binding, "qualification")
        current = dict(previous)
        current["tool_sha256"] = "9" * 64
        execution_product._write_json(workspace / "binding.json", previous)

        with self.assertRaisesRegex(execution.ExecutionError, "--resume"):
            execution_product._activate_workspace_binding(workspace, current, resume=False)
        self.assertEqual(previous, json.loads((workspace / "binding.json").read_text(encoding="utf-8")))

        execution_product._activate_workspace_binding(workspace, current, resume=True)
        archives = list((workspace / "evidence" / "product" / "_audit" / "workspace-bindings").glob("*.json"))
        self.assertEqual(1, len(archives))
        self.assertEqual(previous, json.loads(archives[0].read_text(encoding="utf-8")))
        self.assertEqual(current, json.loads((workspace / "binding.json").read_text(encoding="utf-8")))

        execution_product._activate_workspace_binding(workspace, current, resume=True)
        self.assertEqual(archives, list((workspace / "evidence" / "product" / "_audit" / "workspace-bindings").glob("*.json")))

        for name in ("candidate", "binary_sha256"):
            execution_product._write_json(workspace / "binding.json", previous)
            changed = dict(current)
            changed[name] = ("d" if name == "candidate" else "e") * (40 if name == "candidate" else 64)
            with self.assertRaisesRegex(execution.ExecutionError, "另一候选或二进制"):
                execution_product._activate_workspace_binding(workspace, changed, resume=True)
            self.assertEqual(previous, json.loads((workspace / "binding.json").read_text(encoding="utf-8")))

    def test_product_frozen_tasks_migrate_with_resume_and_archive_previous(self) -> None:
        workspace = Path(self.config["workspace"])
        previous = {"schema": "ownward.product-tasks/v1", "dataset_version": "v1", "tasks": [{"scenario_id": "old"}]}
        current = {"schema": "ownward.product-tasks/v1", "dataset_version": "v2", "tasks": [{"scenario_id": "current"}]}
        tasks_path = workspace / "tasks" / "qualification.json"
        execution_product._write_json(tasks_path, previous)

        with self.assertRaisesRegex(execution.ExecutionError, "--resume"):
            execution_product._activate_frozen_tasks(workspace, "qualification", current, resume=False)
        self.assertEqual(previous, json.loads(tasks_path.read_text(encoding="utf-8")))

        self.assertEqual(
            tasks_path,
            execution_product._activate_frozen_tasks(workspace, "qualification", current, resume=True),
        )
        archive = workspace / "tasks" / "_audit" / "qualification" / f"{execution_product._json_sha256(previous)}.json"
        self.assertEqual(previous, json.loads(archive.read_text(encoding="utf-8")))
        self.assertEqual(current, json.loads(tasks_path.read_text(encoding="utf-8")))

    def test_interrupted_resource_report_is_bound_after_exact_dependency_check(self) -> None:
        report_path = self.workspace / "resource" / "report.json"
        report_path.parent.mkdir(parents=True)
        report = {"candidate": "candidate", "release_binary_sha256": "b" * 64}
        report_path.write_text(json.dumps(report), encoding="utf-8")
        identity = {"candidate": "candidate", "binary_sha256": "b" * 64}
        with (
            mock.patch("execution_product._resource_identity", return_value=identity),
            mock.patch("execution_product._verify_resource_dependencies") as verify_dependencies,
        ):
            actual = execution_product._bind_interrupted_resource_report(self.root, {}, {}, report, report_path)
        self.assertEqual(report_path, actual)
        self.assertEqual(identity, json.loads(report_path.read_text(encoding="utf-8"))["resource_binding"])
        verify_dependencies.assert_called_once()

    def test_resource_reuse_rejects_a_different_machine_identity(self) -> None:
        report = {
            "environment": {"processor": "old", "physical_memory_bytes": 1},
            "evidence": {
                "package_manifest": {"path": "package.json"},
                "process_samples": {"path": "samples.json"},
                "workload_results": {"path": "workload.json"},
            },
        }
        identity = {
            "package_files": [
                {"path": "manifest.json", "size": 1, "sha256": "m" * 64},
                {"path": "ownward.exe", "size": 1, "sha256": "b" * 64},
            ],
            "production_storage_report_sha256": "p" * 64,
            "machine": {"processor": "current", "physical_memory_bytes": 2},
        }
        package = {
            "files": {"ownward.exe": {"bytes": 1, "sha256": "b" * 64}},
            "release_manifest_sha256": "m" * 64,
        }
        workload = {"production_storage_report_sha256": "p" * 64}
        with (
            mock.patch("execution_product._raw_evidence", return_value={}),
            mock.patch("execution_product.load_json", side_effect=[package, workload]),
        ):
            with self.assertRaisesRegex(execution.ExecutionError, "机器环境已经变化"):
                execution_product._verify_resource_dependencies(report, self.workspace / "report.json", identity)


if __name__ == "__main__":
    unittest.main()
