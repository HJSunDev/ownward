import copy
import json
import tempfile
import unittest
from pathlib import Path

import materials


class AcceptanceMaterialsTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.root = Path(__file__).resolve().parent / "materials"

    def test_frozen_materials_are_complete_and_consistent(self) -> None:
        materials.validate_materials(self.root)

    def test_materials_reject_digest_drift(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            manifest = json.loads((self.root / "manifest.json").read_text(encoding="utf-8"))
            changed = copy.deepcopy(manifest)
            changed["files"][0]["sha256"] = "0" * 64
            (root / "manifest.json").write_text(json.dumps(changed), encoding="utf-8")
            with self.assertRaisesRegex(materials.MaterialsError, "材料不存在|摘要不匹配"):
                materials.validate_materials(root)


if __name__ == "__main__":
    unittest.main()
