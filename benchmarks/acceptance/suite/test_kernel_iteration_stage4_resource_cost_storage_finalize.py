from __future__ import annotations

from pathlib import Path
import sys
import unittest


HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

import kernel_iteration_stage4_resource_cost_storage_finalize as storage_finalize  # noqa: E402


class ResourceCostStorageFinalizeTests(unittest.TestCase):
    def test_schema_keeps_storage_independent_from_open_token_and_wall_gates(self) -> None:
        self.assertEqual(storage_finalize.SCHEMA, "ownward.kernel-iteration-stage4-resource-cost-storage-evidence/v1")
        self.assertEqual(storage_finalize.CANDIDATE_RECEIPT_SCHEMA, "ownward.kernel-iteration-v2-candidate/v4")


if __name__ == "__main__":
    unittest.main()
