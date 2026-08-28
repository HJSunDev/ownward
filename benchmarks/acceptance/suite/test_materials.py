import copy
import hashlib
import json
import tempfile
import unittest
from pathlib import Path

import product
import report_relationships as relationships
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
            active = next(item for item in changed["files"] if item["path"].endswith("product/v2/dataset.json"))
            active["sha256"] = "0" * 64
            (root / "manifest.json").write_text(json.dumps(changed), encoding="utf-8")
            with self.assertRaisesRegex(materials.MaterialsError, "材料不存在|摘要不匹配"):
                materials.validate_materials(root)

    def test_product_v1_remains_byte_frozen(self) -> None:
        root = self.root / "product" / "v1"
        actual = {
            "dataset_sha256": hashlib.sha256((root / "dataset.json").read_bytes()).hexdigest(),
            "qualification_sha256": hashlib.sha256((root / "qualification.json").read_bytes()).hexdigest(),
            "review_sha256": hashlib.sha256((root / "review.json").read_bytes()).hexdigest(),
        }
        self.assertEqual(materials.PRODUCT_V1_DIGESTS, actual)

    def test_product_v2_is_the_minimal_reviewed_correction(self) -> None:
        v1 = materials.load_json(self.root / "product" / "v1" / "dataset.json")
        v2 = materials.load_json(self.root / "product" / "v2" / "dataset.json")
        self.assertEqual("ownward-product-dataset/v2", v2["version"])
        v1_comparable = copy.deepcopy(v1)
        v2_comparable = copy.deepcopy(v2)
        v1_comparable["version"] = v2_comparable["version"]
        old = next(item for item in v1_comparable["scenarios"] if item["truth"]["id"] == "s27-68857f46")
        revised = next(item for item in v2_comparable["scenarios"] if item["truth"]["id"] == "s27-68857f46")
        self.assertEqual(materials.S27_V2_QUESTION, revised["expression"]["query"]["question"])
        self.assertIn("realization I formed then", materials.S27_V2_QUESTION)
        self.assertIn("distinct practice did I adopt afterward", materials.S27_V2_QUESTION)
        self.assertIn("what later result supports", materials.S27_V2_QUESTION)
        revised["expression"]["query"]["question"] = old["expression"]["query"]["question"]
        self.assertEqual(v1_comparable, v2_comparable)
        self.assertEqual(
            ["s27-68857f46-n1", "s27-68857f46-n2", "s27-68857f46-n3"],
            revised["truth"]["query"]["expected_ids"],
        )

        q1 = materials.load_json(self.root / "product" / "v1" / "qualification.json")
        q2 = materials.load_json(self.root / "product" / "v2" / "qualification.json")
        self.assertEqual(q1["scenario_ids"], q2["scenario_ids"])
        self.assertEqual(8, len(q2["scenario_ids"]))
        review = materials.load_json(self.root / "product" / "v2" / "review.json")
        self.assertEqual(23, review["source"]["inherited_review"]["scenario_count"])
        self.assertTrue(review["adversarial_review"]["valid"])
        self.assertTrue(all(item["passed"] for item in review["adversarial_review"]["checks"]))

    def test_all_active_product_entries_point_only_to_v2(self) -> None:
        suite_root = self.root.parent
        adapters = materials.load_json(suite_root / "adapters.json")["layers"]["product"]
        contract = materials.load_json(suite_root / "contract.json")["evidence_layers"]["product"]
        dataset, qualification = product.load_default_materials(suite_root)
        self.assertEqual("ownward-product-dataset/v2", adapters["version"])
        self.assertEqual("materials/product/v2/dataset.json", adapters["dataset"])
        self.assertEqual("materials/product/v2/qualification.json", adapters["qualification"])
        self.assertEqual("ownward-product-dataset/v2", contract["version"])
        self.assertEqual("ownward-product-dataset/v2", dataset["version"])
        self.assertEqual("ownward-product-dataset/v2", qualification["dataset_version"])
        self.assertTrue(all("/product/v2/" in f"/{path}" for path in relationships.SCOPE_MATERIALS["product"] if "/materials/product/" in f"/{path}"))


if __name__ == "__main__":
    unittest.main()
