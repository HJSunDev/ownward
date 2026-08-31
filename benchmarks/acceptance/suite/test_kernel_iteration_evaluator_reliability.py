from __future__ import annotations

import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock

import kernel_iteration_evaluator_reliability as reliability


class _Renderer:
    def __init__(self, _python: Path, _evaluator: Path) -> None:
        pass

    def __enter__(self) -> "_Renderer":
        return self

    def __exit__(self, *_args: object) -> None:
        return None

    def render(self, _question: dict[str, object], hypothesis: str) -> str:
        return hypothesis


class EvaluatorReliabilityTests(unittest.TestCase):
    def setUp(self) -> None:
        self.suite_root = Path(__file__).resolve().parent

    def test_contract_freezes_fail_closed_noncandidate_boundary(self) -> None:
        contract = reliability.load_contract(self.suite_root)
        self.assertTrue(contract["failure_semantics"]["fail_closed"])
        self.assertFalse(contract["failure_semantics"]["candidate_failure"])
        self.assertFalse(contract["failure_semantics"]["candidate_execution_allowed_before_qualification"])
        self.assertEqual(contract["product_reader_unchanged"]["model"], "gpt-5.6-luna")
        self.assertEqual(contract["product_reader_unchanged"]["reasoning_effort"], "xhigh")
        self.assertEqual(contract["attribution_reader"]["model"], "gpt-5.6-terra")
        self.assertEqual(contract["attribution_reader"]["reasoning_effort"], "high")
        self.assertEqual(contract["attribution_qualification"]["reader_calls"], 15)
        self.assertEqual(contract["attribution_qualification"]["judge_calls"], 24)
        self.assertLessEqual(
            contract["cost_bound"]["pre_attribution_observed_upper_seconds"]
            + contract["cost_bound"]["future_single_case_attribution_maximum_seconds"],
            contract["cost_bound"]["level_total_wall_seconds_maximum"],
        )

    def test_qualification_material_is_independent_and_covers_three_hard_boundaries(self) -> None:
        contract = reliability.load_contract(self.suite_root)
        material = reliability._load_material(self.suite_root, contract)
        self.assertFalse(material["formal"])
        self.assertFalse(material["blind_gate_material"])
        self.assertFalse(material["candidate_decision"])
        self.assertFalse(material["contains_formal_or_blind_content"])
        self.assertEqual(
            [case["coverage"] for case in material["cases"]],
            ["temporal-order", "knowledge-update-conflict", "multi-session-relation"],
        )
        self.assertTrue(all(case["answer_session_ids"] for case in material["cases"]))
        self.assertTrue(all(case["truth_claims"] for case in material["cases"]))

    def test_correct_wrong_controls_persist_and_resume_without_execution(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            python_root = root / "python"
            (python_root / "Scripts").mkdir(parents=True)
            python = python_root / "Scripts" / "python.exe"
            python.write_bytes(b"python")
            source = root / "source"
            (source / "src" / "evaluation").mkdir(parents=True)
            evaluator = source / "src" / "evaluation" / "evaluate_qa.py"
            evaluator.write_bytes(b"evaluator")
            lock = root / "requirements.lock"
            lock.write_bytes(b"lock")
            environment = {"layout": {"python": str(python_root), "source": str(source), "requirements_lock": str(lock)}}
            runtime = {
                "environment": environment,
                "protocol_value": {
                    "reader": {"model": "gpt-5.6-luna", "reasoning_effort": "xhigh"},
                    "judge": {"model": "gpt-5.6-terra", "reasoning_effort": "medium"},
                },
            }
            contract = reliability.load_contract(self.suite_root)
            dependencies = {"fixture": "a" * 64}
            calls: list[list[str]] = []

            def diagnose(_suite: Path, _stage: Path, _runtime: dict[str, object], materials: dict[str, object], _run: Path, **kwargs: object) -> dict[str, object]:
                calls.append([str(item["case_id"]) for item in materials["cases"]])
                self.assertEqual(kwargs["product_repeats"], (2, 3))
                self.assertEqual(kwargs["oracle_repeats"], (1, 2, 3))
                self.assertEqual(kwargs["reader_settings"]["model"], "gpt-5.6-terra")
                self.assertEqual(kwargs["reader_settings"]["reasoning_effort"], "high")
                return {
                    "reader": {
                        "settings": kwargs["reader_settings"],
                        "product_context_failures": 0,
                        "oracle_context_failures": 0,
                        "cost": {
                            "measured_calls": 15,
                            "aggregate_wall_seconds": 20.0,
                            "mean_wall_seconds": 4.0 / 3.0,
                            "p95_wall_seconds": 2.0,
                        },
                    },
                    "judge": {
                        "controls_passed": True,
                        "correct_controls": {"passed": 3, "total": 3},
                        "wrong_controls": {"passed": 3, "total": 3},
                        "reader_answers_total": 18,
                    },
                    "transport": {
                        "process_starts": 4,
                        "worker_restarts": 0,
                        "rate_limit_observed": False,
                    },
                    "cost": {"observed_wall_seconds": 12.5, "judge_aggregate_wall_seconds": 18.0},
                }

            state = root / "state.json"
            state.write_bytes(b'{"immutable":true}\n')
            config = root / "execution.json"
            config.write_text("{}\n", encoding="utf-8")
            output = root / "evidence"
            with mock.patch.object(reliability, "current_dependencies", return_value=(dependencies, runtime, contract)):
                first = reliability.run(self.suite_root, output, config, state, diagnose=diagnose)
                resumed = reliability.run(
                    self.suite_root, output, config, state, resume=True,
                    diagnose=lambda *_args, **_kwargs: self.fail("resume executed attribution"),
                )
            self.assertTrue(first["passed"])
            self.assertEqual(calls, [[
                "stage6-oracle-reader-temporal",
                "stage6-oracle-reader-conflict",
                "stage6-oracle-reader-cross-evidence",
            ]])
            self.assertEqual(first["model_calls"], 39)
            self.assertTrue(resumed["reused"])
            self.assertEqual(resumed["model_calls"], 0)
            self.assertEqual(resumed["product_executions"], 0)
            result = json.loads(Path(first["result"]).read_text(encoding="utf-8"))
            self.assertEqual(result["attribution"]["reader_calls"], 15)
            self.assertEqual(result["attribution"]["judge_calls"], 24)
            self.assertEqual(result["attribution"]["reader_settings"]["model"], "gpt-5.6-terra")
            self.assertEqual(result["attribution"]["reader_settings"]["reasoning_effort"], "high")
            self.assertLessEqual(result["attribution"]["projected_level_wall_seconds"], 1961)
            self.assertTrue(result["raw_scratch_destroyed"])
            self.assertEqual(state.read_bytes(), b'{"immutable":true}\n')
            self.assertFalse((output / ".runtime" / first["plan_identity"][:16]).exists())


if __name__ == "__main__":
    unittest.main()
