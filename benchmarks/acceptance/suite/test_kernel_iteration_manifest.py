from __future__ import annotations

import json
from pathlib import Path
import tempfile
import unittest

import kernel_iteration_manifest as manifest


class KernelIterationManifestTests(unittest.TestCase):
    def test_current_pointer_resolves_one_version_manifest(self) -> None:
        suite_root = Path(__file__).resolve().parent
        value = manifest.load(suite_root)
        self.assertEqual("v2", value["major_version"])
        self.assertEqual(
            suite_root / "iteration" / "v2" / "validation-contract.json",
            manifest.path(suite_root, "validation_contract", value),
        )

    def test_manifest_cannot_escape_iteration_root(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            suite_root = Path(temporary)
            iteration = suite_root / "iteration"
            iteration.mkdir()
            path = iteration / "invalid.json"
            path.write_text(json.dumps({
                "schema": manifest.SCHEMA,
                "major_version": "invalid",
                "paths": {
                    "validation_contract": "../outside.json",
                    "blind_budget": "iteration/a.json",
                    "comparison_contract": "iteration/b.json",
                },
            }), encoding="utf-8")
            with self.assertRaisesRegex(ValueError, "path is invalid"):
                manifest.load(suite_root, path)


if __name__ == "__main__":
    unittest.main()
