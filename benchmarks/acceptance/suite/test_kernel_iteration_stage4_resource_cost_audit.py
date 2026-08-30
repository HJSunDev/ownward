from __future__ import annotations

import json
from pathlib import Path
import struct
import sys
import tempfile
import unittest
import zlib


HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE))

import kernel_iteration_stage4_resource_cost_audit as audit  # noqa: E402


class Stage4ResourceCostAuditTests(unittest.TestCase):
    def test_semantic_token_ownership_fails_closed_while_total_closes(self) -> None:
        semantic = {
            "v0": {"observed_input_tokens": 100, "attribution_status": "not-identifiable-from-existing-aggregate-usage-receipts"},
            "v2": {"observed_input_tokens": 99, "attribution_status": "not-identifiable-from-existing-aggregate-usage-receipts"},
        }
        paired = _paired()
        wall = _wall()
        storage = {"obsolete_record_bytes": 55, "compacted_candidate_to_v0_ratio": 0.45}
        result = audit.evaluate_gates(paired, semantic, wall, storage)
        self.assertEqual(result["semantic_input_tokens"]["decision"], "retain-open-and-fail-closed")
        self.assertFalse(result["semantic_input_tokens"]["migration_receipt_generated"])
        self.assertFalse(result["candidate_implementation_authorized"])

    def test_wall_gate_uses_true_critical_path_not_phase_sum(self) -> None:
        paired = _paired()
        semantic = {"v0": {"observed_input_tokens": 100}, "v2": {"observed_input_tokens": 99}}
        wall = _wall()
        storage = {"obsolete_record_bytes": 55, "compacted_candidate_to_v0_ratio": 0.45}
        result = audit.evaluate_gates(paired, semantic, wall, storage)
        item = result["end_to_end_wall_seconds"]
        self.assertTrue(item["mathematical_headroom_to_half"])
        self.assertEqual(item["decision"], "retain-original-half-gate")

    def test_derived_log_inspection_counts_only_latest_committed_record(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "organization.binlog"
            first = _frame("asset-a", "pending", 16)
            second = _frame("asset-a", "ready", 16)
            third = _frame("asset-b", "ready", 8)
            path.write_bytes(first + second + third)
            result = audit.inspect_derived_log(path)
        self.assertEqual(result["frames"], 3)
        self.assertEqual(result["current_assets"], 2)
        self.assertEqual(result["current_bytes"], len(first) + len(second) + len(third))
        self.assertEqual(result["latest_record_bytes"], len(second) + len(third))
        self.assertEqual(result["obsolete_record_bytes"], len(first))


def _paired() -> dict:
    return {"gates": {"dimensions": {
        "semantic_input_tokens": {"v0": 100.0, "v2": 99.0, "passed": False},
        "end_to_end_wall_seconds": {"v0": 100.0, "v2": 90.0, "passed": False},
        "ownward_data_bytes": {"v0": 100.0, "v2": 65.0, "passed": False},
    }}}


def _wall() -> dict:
    return {
        "v0": {"shared_reader_judge_seconds": 20.0, "runner_and_unclassified_seconds": 10.0, "candidate_controlled_seconds": 70.0},
        "v2": {"shared_reader_judge_seconds": 20.0, "runner_and_unclassified_seconds": 10.0, "candidate_controlled_seconds": 60.0},
    }


def _frame(asset_id: str, status: str, vector_bytes: int) -> bytes:
    metadata = json.dumps({
        "schema": "ownward.derived/v4", "asset_id": asset_id, "asset_revision": 1,
        "generated_at": "2026-08-30T00:00:00Z", "provider": "test", "status": status,
        "analysis": {},
    }, separators=(",", ":")).encode("utf-8")
    payload = metadata + (b"\x00" * vector_bytes)
    header = b"OWD3" + struct.pack("<III", len(metadata), vector_bytes, zlib.crc32(payload) & 0xFFFFFFFF)
    return header + payload + b"DONE"


if __name__ == "__main__":
    unittest.main()
