from __future__ import annotations

import json
from pathlib import Path
import tempfile
import unittest

import environment


class EnvironmentTest(unittest.TestCase):
    def test_dataset_requires_500_unique_complete_questions(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "dataset.json"
            values = [
                {
                    "question_id": f"q-{index}",
                    "question_type": "multi-session",
                    "question": "question",
                    "answer": "answer",
                    "haystack_dates": [],
                    "haystack_session_ids": [],
                    "haystack_sessions": [],
                }
                for index in range(environment.EXPECTED_QUESTIONS)
            ]
            path.write_text(json.dumps(values), encoding="utf-8")
            self.assertEqual(environment.EXPECTED_QUESTIONS, environment._validate_dataset(path))
            values[-1]["question_id"] = values[0]["question_id"]
            path.write_text(json.dumps(values), encoding="utf-8")
            with self.assertRaisesRegex(environment.EnvironmentError, "duplicate question_id"):
                environment._validate_dataset(path)

    def test_fixed_and_run_paths_cannot_overlap(self) -> None:
        paths = environment._layout(Path("E:/Ownward/acceptance/longmemeval-s"))
        environment._validate_layout(paths)
        paths["runs"] = paths["source"] / "runs"
        with self.assertRaisesRegex(environment.EnvironmentError, "overlaps"):
            environment._validate_layout(paths)

    def test_check_path_has_no_network_operation(self) -> None:
        names = set(environment.check.__code__.co_names)
        self.assertNotIn("urlopen", names)
        self.assertNotIn("_download", names)


if __name__ == "__main__":
    unittest.main()
