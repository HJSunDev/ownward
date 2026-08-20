from __future__ import annotations

import json
import os
from pathlib import Path
import tempfile
import unittest

import verify


class DeliveryResourceVerifierTests(unittest.TestCase):
    def test_frozen_thresholds_remain_bound_to_component_evidence(self) -> None:
        path = Path(verify.__file__).with_name("thresholds.json")
        thresholds = verify.load(path)
        self.assertEqual(thresholds["schema"], verify.THRESHOLDS_SCHEMA)
        for path_key, digest_key in (
            ("embedding_resource_report", "embedding_resource_report_sha256"),
            ("product_thresholds", "product_thresholds_sha256"),
        ):
            evidence = verify.REPOSITORY / thresholds["basis"][path_key]
            self.assertTrue(evidence.is_file())
            self.assertEqual(verify.canonical_json_sha256(evidence), thresholds["basis"][digest_key])

    def test_canonical_json_hash_is_independent_of_formatting_and_line_endings(self) -> None:
        with tempfile.TemporaryDirectory() as root_value:
            root = Path(root_value)
            first = root / "first.json"
            second = root / "second.json"
            first.write_bytes(b'{\r\n  "value": 1,\r\n  "items": [2, 3]\r\n}\r\n')
            second.write_bytes(b'{"items":[2,3],"value":1}\n')
            self.assertEqual(verify.canonical_json_sha256(first), verify.canonical_json_sha256(second))

    def test_percentile_uses_observed_tail_without_interpolation(self) -> None:
        self.assertEqual(verify.percentile([1, 2, 3], 0.95), 3)
        self.assertEqual(verify.percentile([4, 1, 3, 2], 0.5), 2)

    @unittest.skipUnless(os.name == "nt", "Windows process-tree contract")
    def test_process_tree_includes_sampling_root(self) -> None:
        sample = verify.sample_tree(os.getpid())
        self.assertGreater(sample["rss_bytes"], 0)
        self.assertTrue(any(item["pid"] == os.getpid() for item in sample["processes"]))

    def test_file_manifest_is_content_bound(self) -> None:
        with tempfile.TemporaryDirectory() as root_value:
            root = Path(root_value)
            (root / "one.txt").write_text("one", encoding="utf-8")
            first = verify.file_manifest(root)
            (root / "one.txt").write_text("two", encoding="utf-8")
            second = verify.file_manifest(root)
        self.assertNotEqual(first["one.txt"]["sha256"], second["one.txt"]["sha256"])


if __name__ == "__main__":
    unittest.main()
