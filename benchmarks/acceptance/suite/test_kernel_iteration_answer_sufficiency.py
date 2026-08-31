from __future__ import annotations

import copy
import json
from types import SimpleNamespace
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import kernel_iteration_answer_sufficiency as answer_sufficiency


HERE = Path(__file__).resolve().parent


class AnswerSufficiencyTests(unittest.TestCase):
    def test_attribution_errors_are_stage_specific_and_content_safe(self) -> None:
        for category, stage in (
            ("reader", "reader-execution"),
            ("judge", "judge-execution"),
            ("official-prompt-renderer", "judge-prompt-render"),
        ):
            with self.subTest(category=category):
                with self.assertRaises(answer_sufficiency.AttributionDiagnosticError) as raised:
                    answer_sufficiency._attribution_call(
                        category, stage, lambda: (_ for _ in ()).throw(RuntimeError("secret raw fixture")),
                    )
                receipt = answer_sufficiency.safe_attribution_exception(raised.exception, "fallback")
                self.assertEqual(receipt["category"], category)
                self.assertEqual(receipt["stage"], stage)
                self.assertEqual(receipt["error_type"], "RuntimeError")
                self.assertEqual(len(receipt["message_sha256"]), 64)
                self.assertNotIn("secret raw fixture", json.dumps(receipt))

        class FailedTransport:
            def __enter__(self) -> None:
                raise OSError("private transport detail")

            def __exit__(self, *_args: object) -> None:
                return None

        with self.assertRaises(answer_sufficiency.AttributionDiagnosticError) as raised:
            with answer_sufficiency._attribution_context(FailedTransport(), "transport", "transport-start"):
                pass
        self.assertEqual(raised.exception.category, "transport")

        with self.assertRaises(answer_sufficiency.AttributionDiagnosticError) as raised:
            answer_sufficiency._diagnose_codex_boundaries(
                HERE, HERE, {}, {"cases": []}, HERE,
                correctness_source="invalid",
            )
        self.assertEqual(raised.exception.category, "schema-validation")

    def test_injected_prompt_renderer_owns_judge_path_and_closes(self) -> None:
        renderer_state = {"entered": False, "closed": False, "calls": 0}
        judge_stages: list[Path] = []

        class Renderer:
            def __enter__(self) -> "Renderer":
                renderer_state["entered"] = True
                return self

            def __exit__(self, *_args: object) -> None:
                renderer_state["closed"] = True

            def render(self, _question: dict[str, object], hypothesis: str) -> str:
                self.assert_active()
                renderer_state["calls"] += 1
                return hypothesis

            @staticmethod
            def assert_active() -> None:
                if not renderer_state["entered"] or renderer_state["closed"]:
                    raise AssertionError("renderer used outside its context")

        class Pool:
            def __init__(self, _size: int, _factory: object) -> None:
                pass

            def __enter__(self) -> "Pool":
                return self

            def __exit__(self, *_args: object) -> None:
                return None

            @staticmethod
            def diagnostics() -> dict[str, int]:
                return {"calls": 6}

        class Capability:
            def __init__(self, _transport: Pool) -> None:
                pass

            @staticmethod
            def answer(_prompt: str, _settings: dict[str, object], _stage: Path) -> tuple[str, dict[str, float]]:
                return "cobalt", {"wall_seconds": 0.0}

            @staticmethod
            def judge(prompt: str, _settings: dict[str, object], _stage: Path) -> tuple[bool, dict[str, bool], dict[str, float]]:
                judge_stages.append(_stage)
                label = prompt != "unsupported-control-answer"
                return label, {"label": label}, {"wall_seconds": 0.0}

        formal_prompt_calls: list[object] = []

        def formal_prompt(*args: object) -> str:
            formal_prompt_calls.append(args)
            raise AssertionError("formal run.py prompt entry must not be used")

        module = SimpleNamespace(
            CodexAppServer=SimpleNamespace(direct_command_prefix=lambda *_args: []),
            codex_session=SimpleNamespace(command_prefix=lambda *_args: [], isolated_environment=lambda *_args: {}),
            isolated_runtime_root=lambda path: path / "runtime",
            CodexAppServerPool=Pool,
            CodexCapability=Capability,
            session_content=lambda *_args: "session",
            _answer_prompt=lambda *_args: "oracle prompt",
            official_prompt=formal_prompt,
        )
        case = {
            "case_id": "fixture-case",
            "coverage": "fixture",
            "question_type": "single-session-user",
            "question_date": "2026-01-02",
            "question": "Which color?",
            "answer": "cobalt",
            "answer_session_ids": ["session-1"],
            "stale_session_ids": [],
            "distractor_session_ids": [],
            "truth_claims": [{"claim": "The color is cobalt."}],
            "sessions": [{"session_id": "session-1", "date": "2026-01-01", "turns": [{"role": "user", "content": "The color is cobalt."}]}],
        }
        runtime = {
            "protocol_value": {"reader": {"model": "reader"}, "judge": {"model": "judge"}},
            "environment": {"layout": {"source": ".", "python": "."}},
            "codex_binary": Path("codex"),
            "codex_auth_file": Path("auth"),
        }
        with tempfile.TemporaryDirectory() as directory:
            run_root = Path(directory) / "run"
            question_root = run_root / "questions" / case["case_id"] / "reader"
            question_root.mkdir(parents=True)
            (question_root / "input.json").write_text(json.dumps({"prompt": "product prompt"}), encoding="utf-8")
            (question_root / "output.json").write_text(json.dumps({"answer": "cobalt"}), encoding="utf-8")
            with mock.patch.object(answer_sufficiency.validation, "_load_longmemeval_module", return_value=module), \
                    mock.patch.object(answer_sufficiency, "load_contract", return_value={"mechanical_answer_atoms": {"fixture": [["cobalt"]]}}):
                result = answer_sufficiency._diagnose_codex_boundaries(
                    HERE, Path(directory) / "stage", runtime, {"cases": [case]}, run_root,
                    include_original_product_answer=True,
                    product_repeats=(),
                    oracle_repeats=(1,),
                    run_judge=True,
                    correctness_source="judge",
                    prompt_renderer_factory=lambda _python, _evaluator: Renderer(),
                )
        self.assertTrue(renderer_state["entered"])
        self.assertTrue(renderer_state["closed"])
        self.assertEqual(renderer_state["calls"], 4)
        self.assertEqual(len(judge_stages), len(set(judge_stages)))
        self.assertEqual(formal_prompt_calls, [])
        self.assertTrue(result["judge"]["controls_passed"])
        self.assertEqual(result["reader"]["product_context_failures"], 0)
        self.assertEqual(result["reader"]["oracle_context_failures"], 0)

    def test_contract_is_pre_result_frozen_disjoint_and_aggregate_only(self) -> None:
        contract = answer_sufficiency.load_contract(HERE)
        self.assertTrue(contract["frozen_before_diagnostic_results"])
        self.assertTrue(contract["aggregate_trigger"]["raw_content_destroyed"])
        self.assertEqual(len(contract["loaded"]["diagnosis_materials"]["cases"]), 5)
        self.assertEqual(len(contract["loaded"]["regression_materials"]["cases"]), 4)
        self.assertNotIn("question", contract["aggregate_trigger"])
        self.assertNotIn("answer", contract["aggregate_trigger"])

    def test_classification_stops_at_first_proven_boundary(self) -> None:
        contract = answer_sufficiency.load_contract(HERE)
        execution = {
            "observation": {
                "final_answer_accuracy": 1.0,
                "case_evidence": [{"truth_claims": 3, "delivered_truth_claims": 3}],
            }
        }
        regression = {"observation": {"final_answer_accuracy": 1.0}}
        stable = {
            "reader": {
                "product_context_failures": 0, "oracle_context_failures": 0,
                "product_context_variations": 0, "oracle_context_variations": 0,
            },
            "judge": {"controls_passed": True},
        }
        result = answer_sufficiency.classify_root(contract, execution, regression, stable, {"exact": True})
        self.assertEqual(result["status"], "not-reproduced-with-counterevidence")
        self.assertEqual(result["responsible_component"], "external-answer-boundary")
        self.assertFalse(result["kernel_change_required"])

        missing = copy.deepcopy(execution)
        missing["observation"]["case_evidence"][0]["delivered_truth_claims"] = 2
        result = answer_sufficiency.classify_root(contract, missing, regression, stable, {"exact": True})
        self.assertEqual(result["responsible_component"], "kernel-context")

        reader = copy.deepcopy(stable)
        reader["reader"]["oracle_context_failures"] = 1
        result = answer_sufficiency.classify_root(contract, execution, regression, reader, {"exact": True})
        self.assertEqual(result["responsible_component"], "reader")

        judge = copy.deepcopy(stable)
        judge["judge"]["controls_passed"] = False
        result = answer_sufficiency.classify_root(contract, execution, regression, judge, {"exact": True})
        self.assertEqual(result["responsible_component"], "judge-or-scorer")

        result = answer_sufficiency.classify_root(contract, execution, regression, stable, {"exact": False})
        self.assertEqual(result["responsible_component"], "executor-or-observer")

        wording_only = copy.deepcopy(stable)
        wording_only["reader"]["product_context_variations"] = 5
        wording_only["reader"]["oracle_context_variations"] = 5
        result = answer_sufficiency.classify_root(contract, execution, regression, wording_only, {"exact": True})
        self.assertEqual(result["status"], "not-reproduced-with-counterevidence")
        self.assertNotEqual(result["responsible_component"], "reader")

    def test_mechanical_answer_atoms_require_every_group(self) -> None:
        groups = [["alpha"], ["beta", "bravo"]]
        self.assertTrue(answer_sufficiency._answer_matches("Alpha and Bravo", groups))
        self.assertFalse(answer_sufficiency._answer_matches("Alpha only", groups))


if __name__ == "__main__":
    unittest.main()
