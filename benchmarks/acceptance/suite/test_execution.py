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
        execution.execute(self.root, self.contract, self.state_path, "targeted", self.config, resume=False)
        state = lifecycle.load_state(self.state_path)
        lifecycle.invalidate(self.contract, state, "targeted")
        lifecycle.save_state(self.state_path, state)
        rerun = execution.execute(self.root, self.contract, self.state_path, "targeted", self.config, resume=True)
        self.assertEqual("recorded", rerun["outcome"])
        self.assertNotIn("recovered", rerun)

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
            "acceptance_binding": state["binding"],
            "passed": False,
        }
        with self.assertRaisesRegex(execution.ExecutionError, "停止高成本"):
            execution_product.require_resource_admission(report, state)

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
            "acceptance_binding": candidate_binding.for_mode(self.binding, "qualification"),
            "evidence": artifacts,
            "passed": True,
        }
        report_path.write_text(json.dumps(report), encoding="utf-8")
        state["binding"] = candidate_binding.for_mode(self.binding, "qualification")
        actual = execution_product.resource_report(self.root, state, self.config, workspace, resume=False)
        self.assertEqual(report_path, actual)
        (raw / "process_samples.json").write_text("changed\n", encoding="utf-8")
        with self.assertRaisesRegex(execution.ExecutionError, "发生变化"):
            execution_product.resource_report(self.root, state, self.config, workspace, resume=False)

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


if __name__ == "__main__":
    unittest.main()
