from __future__ import annotations

import json
from pathlib import Path
import tempfile
import unittest

import evidence_identity
import final_validation_gate as gate
from execution_support import ExecutionError


class FinalValidationGateTests(unittest.TestCase):
    def _write(self, path: Path, content: dict[str, object]) -> dict[str, object]:
        value = {**content, "identity": evidence_identity.canonical_sha256(content)}
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(value) + "\n", encoding="utf-8")
        return value

    def _fixture(self, root: Path) -> tuple[dict[str, object], list[dict[str, object]]]:
        suite_identity = "1" * 64
        candidate_identity = "2" * 64
        baseline_identity = "3" * 64
        batch_identity = "4" * 64
        manifest_content = {
            "schema": "candidate-components/v1",
            "source_subject_identity": candidate_identity,
        }
        manifest = self._write(root / "candidate.json", manifest_content)
        previous_plan = None
        previous_result = None
        records = []
        for index, level in enumerate(gate.LEVELS):
            dependencies = {}
            if previous_result is not None:
                dependencies["previous-partition-result"] = previous_result["identity"]
            plan_content = {
                "schema": gate.PLAN_SCHEMA,
                "suite_identity": suite_identity,
                "candidate_subject_identity": candidate_identity,
                "baseline_subject_identity": baseline_identity,
                "evaluation_batch_identity": batch_identity,
                "shared_conditions": {"environment": "5" * 64, "reader": "6" * 64},
                "level": level,
                "previous_plan_identity": previous_plan["identity"] if previous_plan else None,
                "previous_partition_continuation_identity": None,
                "direct_dependencies": dependencies,
            }
            plan = {**plan_content, "identity": evidence_identity.canonical_sha256(plan_content)}
            result_content = {
                "schema": gate.RESULT_SCHEMA,
                "plan_identity": plan["identity"],
                "suite_identity": suite_identity,
                "candidate_subject_identity": candidate_identity,
                "level": level,
                "previous_plan_identity": previous_plan["identity"] if previous_plan else None,
                "status": "passed",
                "passed": True,
                "candidate_decision": True,
                "formal": False,
                "formal_state_written": False,
                "contains_reversible_question_answer_evidence_or_case_ids": False,
                "absolute_decision": {"passed": True},
                "relative_baseline_decision": {"passed": True},
                "next_level": gate.LEVELS[index + 1] if index + 1 < len(gate.LEVELS) else None,
                "stage6_complete": level == 50,
            }
            result = {**result_content, "identity": evidence_identity.canonical_sha256(result_content)}
            run_root = root / "evidence" / "blind-suite-runs" / suite_identity / candidate_identity / plan["identity"]
            run_root.mkdir(parents=True)
            (run_root / "plan.json").write_text(json.dumps(plan) + "\n", encoding="utf-8")
            (run_root / "result.json").write_text(json.dumps(result) + "\n", encoding="utf-8")
            records.append({"root": run_root, "plan": plan, "result": result})
            previous_plan, previous_result = plan, result
        config = {
            "candidate": {"component_manifest": str(root / "candidate.json")},
            "final_validation_gate": {
                "blind_suite_output": str(root / "evidence"),
                "suite_identity": suite_identity,
            },
            "manifest": manifest,
        }
        return config, records

    def test_complete_same_candidate_chain_allows_final_validation(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            config, _ = self._fixture(Path(temporary))
            result = gate.require_blind_completion(config)
            self.assertTrue(result["passed"])
            self.assertEqual([5, 15, 25, 50], [item["level"] for item in result["chain"]])

    def test_missing_or_wrong_candidate_chain_fails_closed(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            config, records = self._fixture(root)
            records[-1]["root"].joinpath("result.json").unlink()
            with self.assertRaisesRegex(ExecutionError, "没有唯一完成"):
                gate.require_blind_completion(config)
            config, records = self._fixture(root / "other")
            manifest_path = Path(str(config["candidate"]["component_manifest"]))
            self._write(manifest_path, {"schema": "candidate-components/v1", "source_subject_identity": "9" * 64})
            with self.assertRaisesRegex(ExecutionError, "没有唯一完成"):
                gate.require_blind_completion(config)

    def test_tampered_predecessor_is_rejected_even_with_valid_terminal(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            config, records = self._fixture(Path(temporary))
            path = records[1]["root"] / "result.json"
            value = json.loads(path.read_text(encoding="utf-8"))
            value["passed"] = False
            path.write_text(json.dumps(value) + "\n", encoding="utf-8")
            with self.assertRaisesRegex(ExecutionError, "身份漂移"):
                gate.require_blind_completion(config)

    def test_process_only_continuation_requires_bound_receipt(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            config, records = self._fixture(Path(temporary))
            predecessor = records[2]
            continuation = "a" * 64
            result_content = {
                name: value for name, value in predecessor["result"].items() if name != "identity"
            }
            result_content.update({
                "status": "evaluation-process-rejected",
                "passed": False,
                "next_level": None,
            })
            predecessor["result"] = self._write(predecessor["root"] / "result.json", result_content)

            child = records[3]
            child_plan_content = {name: value for name, value in child["plan"].items() if name != "identity"}
            child_plan_content["previous_partition_continuation_identity"] = continuation
            child_plan_content["direct_dependencies"] = {
                "previous-partition-result": predecessor["result"]["identity"],
                "previous-partition-continuation": "b" * 64,
            }
            child["plan"] = self._write(child["root"] / "plan.json", child_plan_content)
            child_result_content = {name: value for name, value in child["result"].items() if name != "identity"}
            child_result_content["plan_identity"] = child["plan"]["identity"]
            child["result"] = self._write(child["root"] / "result.json", child_result_content)

            with self.assertRaisesRegex(ExecutionError, "没有形成可继续"):
                gate.require_blind_completion(config)

    def test_bound_process_only_continuation_preserves_a_quality_pass(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            config, records = self._fixture(Path(temporary))
            predecessor = records[2]
            continuation = "a" * 64
            result_content = {
                name: value for name, value in predecessor["result"].items() if name != "identity"
            }
            result_content.update({
                "status": "evaluation-process-rejected",
                "passed": False,
                "next_level": None,
            })
            predecessor["result"] = self._write(predecessor["root"] / "result.json", result_content)

            child = records[3]
            child_plan_content = {name: value for name, value in child["plan"].items() if name != "identity"}
            child_plan_content["previous_partition_continuation_identity"] = continuation
            child_plan_content["direct_dependencies"] = {
                "previous-partition-result": predecessor["result"]["identity"],
                "previous-partition-continuation": continuation,
            }
            child["plan"] = self._write(child["root"] / "plan.json", child_plan_content)
            child_result_content = {name: value for name, value in child["result"].items() if name != "identity"}
            child_result_content["plan_identity"] = child["plan"]["identity"]
            self._write(child["root"] / "result.json", child_result_content)

            result = gate.require_blind_completion(config)
            self.assertTrue(result["passed"])
            self.assertEqual([5, 15, 25, 50], [item["level"] for item in result["chain"]])

    def test_terminal_cannot_authorize_another_level(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            config, records = self._fixture(Path(temporary))
            terminal = records[-1]
            result_content = {name: value for name, value in terminal["result"].items() if name != "identity"}
            result_content["next_level"] = 100
            self._write(terminal["root"] / "result.json", result_content)
            with self.assertRaisesRegex(ExecutionError, "50 题盲测没有完成"):
                gate.require_blind_completion(config)


if __name__ == "__main__":
    unittest.main()
