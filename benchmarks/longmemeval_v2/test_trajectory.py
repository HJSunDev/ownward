import unittest

from ownward_trajectory import normalize_trajectory, trajectory_documents


class TrajectoryTest(unittest.TestCase):
    def test_normalizes_public_trajectory_without_benchmark_labels(self) -> None:
        trajectory = {
            "id": "trajectory-1",
            "goal": "Find the account setting",
            "outcome": "Found it",
            "start_url": "https://example.test/start",
            "states": [
                {
                    "url": "https://example.test/start",
                    "action": "click settings",
                    "thought": "The menu should contain it",
                    "accessibility_tree": "Settings button\nProfile button",
                }
            ],
        }

        normalized = normalize_trajectory(trajectory)
        documents = trajectory_documents(trajectory, 1000)

        self.assertEqual(normalized["actions"], ["click settings"])
        self.assertEqual(len(documents), 2)
        combined = "\n".join(documents)
        self.assertIn("Settings button", combined)
        self.assertNotIn("question_type", combined)
        self.assertNotIn("answer", combined.lower())

    def test_normalizes_source_trajectory_and_splits_large_evidence(self) -> None:
        trajectory = {
            "id": "trajectory-2",
            "metadata": {"original_goal": ["Inspect", "the record"]},
            "content": [
                {
                    "step": 3,
                    "url": "https://example.test/record",
                    "action": None,
                    "thoughts": None,
                    "observation": {"text": "evidence " * 500},
                }
            ],
        }

        documents = trajectory_documents(trajectory, 1000)

        self.assertGreater(len(documents), 2)
        self.assertTrue(all(len(document) <= 1000 for document in documents))
        self.assertTrue(all("trajectory-2" in document for document in documents))


if __name__ == "__main__":
    unittest.main()
