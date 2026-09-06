import copy
import unittest
from pathlib import Path

import semantic_representation as s


class SourceOwnershipTests(unittest.TestCase):
    def setUp(self):
        self.contract = s.SemanticInputContract(s.COMPACT_REPRESENTATION, "test", None, "targets-first")

    def test_primary_sources_follow_work_order_without_losing_neighbors(self):
        assets = [{"id": f"asset-{i}", "revision": i+1, "content": f"Only group {i} owns value {i}.", "contexts": []} for i in range(3)]
        work = [{"id": f"work-{i}", "asset": assets[i],
                 "candidates": [{**assets[(i+1)%3], "similarity": .9, "explicit_contexts": [],
                                  "relations": [{"source_id": assets[i]["id"], "target_id": assets[(i+1)%3]["id"], "kind": "related"}]},
                                {"id": "outside", "revision": 4, "content": "reference-only fact", "similarity": .8, "explicit_contexts": []}]}
                for i in [2, 0, 1]]
        original = copy.deepcopy(work)
        encoded = self.contract.encode(work)
        self.assertTrue(self.contract.validate(work, encoded)["equivalent"])
        self.assertEqual([row[1] for row in encoded["work"]], [0, 1, 2])
        self.assertEqual([row[2] for row in encoded["bodies"][:3]], [w["asset"]["content"] for w in work])
        self.assertEqual(encoded["bodies"][-1][2], "reference-only fact")
        self.assertEqual(len(encoded["bodies"]), 4)
        self.assertEqual(work, original)
        legacy = s.SemanticInputContract(s.COMPACT_REPRESENTATION, "test", None)
        self.assertEqual(self.contract.fact_identity(work), legacy.fact_identity(work))
        self.assertEqual(self.contract.instruction(), legacy.instruction())
        self.assertEqual(self.contract.body_chars(encoded), legacy.body_chars(legacy.encode(work)))
        bad = copy.deepcopy(encoded)
        bad["work"][0][1] = 1
        with self.assertRaises(s.SemanticRepresentationError):
            self.contract.validate(work, bad)

    def test_grounded_input_and_output_cannot_import_a_neighbors_fact(self):
        contract = s.load_contract(Path(__file__).resolve().parents[2] / "manifests/kernel-candidates/v2/source-ownership/semantic-representation.json")
        work = [{"id": "w", "asset": {"id": "a", "revision": 1, "content": "User: Keep the violet option.\nDo not use amber."},
                 "candidates": [{"id": "b", "revision": 1, "content": "Use amber instead.", "explicit_contexts": []}]}]
        encoded = contract.encode(work)
        self.assertTrue(contract.validate(work, encoded)["equivalent"])
        self.assertEqual("".join(encoded["work"][0]["target"]["passages"].values()), work[0]["asset"]["content"])
        self.assertEqual(encoded["reference_sources"][0]["content"], "Use amber instead.")
        good = {"analyses": [{"index": 0, "summary": 0, "topics": ["choice"], "cues": [{"passage": 1, "kind": "constraint"}]}]}
        decoded = contract.decode_analyses(work, good)
        self.assertEqual(decoded[0]["work_id"], "w")
        self.assertEqual(decoded[0]["cues"][0]["text"], "Do not use amber.")
        for index in [2, -1, True, "Use amber instead."]:
            bad = copy.deepcopy(good)
            bad["analyses"][0]["cues"][0]["passage"] = index
            with self.assertRaises(s.SemanticRepresentationError):
                contract.decode_analyses(work, bad)

    def test_source_slices_are_lossless_bounded_and_support_long_multilingual_text(self):
        for source in ["", "\n\nUser: 中文条件。\nAssistant: 不采用旧值。\n", "word " * 170, "a" * 600, "🙂" * 401]:
            spans = s.source_passages(source)
            self.assertEqual("".join(spans), source)
            self.assertTrue(all(len(span.strip()) <= 384 for span in spans))

    def test_source_slice_keeps_abbreviation_and_qualifying_fact_together(self):
        statement = "User: I met Dr. Rivera on June 12. The inspection was postponed, not completed."
        source = statement + "\n\nAssistant: Understood."
        spans = s.source_passages(source)
        self.assertEqual(spans[0].strip(), statement)
        self.assertEqual("".join(spans), source)

    def test_previous_representation_keeps_its_sealed_instruction_and_slices(self):
        legacy = s.LEGACY_GROUNDED_REPRESENTATION
        self.assertEqual(s.canonical_sha256(s.grounded_instruction(legacy)),
                         "839a1c09330ef15413164328fb19934434815f45f2cc3c25a6d37786951b12c9")
        source = "User: I met Dr. Rivera on June 12."
        self.assertEqual(s.source_passages(source, legacy)[0], "User: I met Dr.")
        self.assertEqual(s.source_passages(source), [source])
    def test_duplicate_source_and_distinct_revisions_remain_lossless(self):
        a = {"id": "a", "revision": 1, "content": "old"}
        b = {"id": "a", "revision": 2, "content": "current"}
        work = [{"id": str(i), "asset": asset, "candidates": [{**b, "explicit_contexts": []}]} for i, asset in enumerate([a, a, b])]
        encoded = self.contract.encode(work)
        self.assertEqual([row[1] for row in encoded["work"]], [0, 0, 1])
        self.assertTrue(self.contract.validate(work, encoded)["equivalent"])


if __name__ == "__main__":
    unittest.main()
