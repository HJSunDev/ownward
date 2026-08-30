from __future__ import annotations

from pathlib import Path
import sys
import unittest


HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

import kernel_iteration_stage4_resource_cost_diagnosis as diagnosis  # noqa: E402


class Stage4ResourceCostDiagnosisTests(unittest.TestCase):
    def test_unique_lossless_semantic_floor_does_not_invent_removable_duplication(self) -> None:
        paired = {
            "gates": {"root_status": "open", "dimensions": {
                "semantic_input_tokens": {"v0": 100.0, "v2": 99.0, "passed": False},
                "end_to_end_wall_seconds": {"v0": 100.0, "v2": 90.0, "passed": False},
                "ownward_data_bytes": {"v0": 100.0, "v2": 65.0, "passed": False},
            }}
        }
        semantic = {
            "v0": {"analysis_calls": 12, "work_item_count": 46, "body_count": 46, "deduplicated_representation": True},
            "v2": {"analysis_calls": 12, "work_item_count": 46, "body_count": 46, "deduplicated_representation": True},
        }
        result = diagnosis.evaluate(paired, semantic)
        root = result["first_dominant_root"]
        self.assertTrue(root["proven_facts"]["each_work_body_transmitted_once"])
        self.assertFalse(root["removable_duplicate_body_transmission_proven"])
        self.assertFalse(root["in_package_implementation_allowed"])
        self.assertFalse(result["stage4_complete"])


if __name__ == "__main__":
    unittest.main()
