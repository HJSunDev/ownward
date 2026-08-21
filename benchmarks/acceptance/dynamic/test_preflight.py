from __future__ import annotations

from pathlib import Path
import sys
import unittest


HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))
import common
import preflight


class DynamicPreflightTests(unittest.TestCase):
    def test_preflight_uses_one_required_scenario_per_formal_task_class(self) -> None:
        formal = common.load_json(HERE / "protocol.json")
        derived = preflight.preflight_protocol(formal)
        class_count = len(formal["generation"]["task_classes"])
        batch_size = formal["generation"]["validation_scenarios_per_batch"]
        self.assertEqual(derived["generation"]["generated_scenarios"], class_count * batch_size)
        self.assertEqual(derived["generation"]["minimum_valid_scenarios"], class_count)
        self.assertEqual(derived["generation"]["minimum_scenarios_per_task_class"], 1)
        self.assertEqual(derived["generation"]["validation_scenarios_per_batch"], batch_size)
        self.assertEqual(formal["generation"]["minimum_scenarios_per_task_class"], 6)


if __name__ == "__main__":
    unittest.main()
