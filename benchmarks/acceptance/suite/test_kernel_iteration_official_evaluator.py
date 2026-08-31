from __future__ import annotations

from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock

import kernel_iteration_official_evaluator as evaluator


EVALUATOR_SOURCE = '''
def get_anscheck_prompt(question_type, question, answer, hypothesis, abstention=False):
    return f"{question_type}|{question}|{answer}|{hypothesis}|{abstention}"
'''


class OfficialEvaluatorTests(unittest.TestCase):
    def test_persistent_renderer_uses_isolated_worker(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / "evaluate_qa.py"
            source.write_text(EVALUATOR_SOURCE, encoding="utf-8")
            (Path(directory) / "backoff.py").write_text("__version__ = '2.2.1'\n", encoding="utf-8")
            question = {
                "question_id": "control",
                "question_type": "single-session-user",
                "question": "Which color?",
                "answer": "cobalt",
            }
            with mock.patch.dict("os.environ", {"PYTHONPATH": directory}):
                probe = evaluator.cold_probe(Path(sys.executable), source)
                self.assertTrue(probe["prompts_distinct"])
                with evaluator.PromptRenderer(Path(sys.executable), source) as renderer:
                    first = renderer.render(question, "cobalt")
                    second = renderer.render(question, "amber")
            self.assertIn("cobalt", first)
            self.assertIn("amber", second)
            self.assertNotEqual(first, second)

    def test_worker_render_error_is_fail_closed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            source = Path(directory) / "evaluate_qa.py"
            source.write_text(
                EVALUATOR_SOURCE + "\n# probe controls still render; the request below is deliberately malformed.\n",
                encoding="utf-8",
            )
            (Path(directory) / "backoff.py").write_text("__version__ = '2.2.1'\n", encoding="utf-8")
            with mock.patch.dict("os.environ", {"PYTHONPATH": directory}):
                with evaluator.PromptRenderer(Path(sys.executable), source) as renderer:
                    with self.assertRaises(evaluator.OfficialEvaluatorError):
                        renderer.render({"question_id": "missing-fields"}, "answer")


if __name__ == "__main__":
    unittest.main()
