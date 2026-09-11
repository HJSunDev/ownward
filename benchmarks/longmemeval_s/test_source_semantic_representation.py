import copy
import unittest
from pathlib import Path

import semantic_representation as s


class SourceOwnershipTests(unittest.TestCase):
    def test_compact_graph_endpoint_has_one_unambiguous_locator(self):
        import run  # Establish the shared benchmark support import path.
        from external_intelligence import ExternalIntelligenceError, validate_structured_output
        work = [{"id":"w", "organization_schema":"ownward.organization/v1",
                 "asset":{"id":"a", "revision":1, "content":"Beacon.\nOnly indoors."},
                 "candidates":[{"id":"b","revision":1,"content":"Keep dry."}]}]
        contract = s.SemanticInputContract(s.GROUNDED_REPRESENTATION, "test", None)
        schema = contract.output_schema(work, {})['properties']['analyses']['items']['properties']['organization']
        good = {"schema":"ownward.organization/v1", "units":[{"id":"u", "selector":0}],
                "links":[{"type":"applies_in", "meaning":"location condition",
                          "source":{"asset_id":"self", "unit_id":"u"},
                          "target":{"asset_id":"self", "selector":1}}]}
        validate_structured_output(good, schema)
        bad = copy.deepcopy(good)
        bad['links'][0]['source']['selector']=1
        with self.assertRaises(ExternalIntelligenceError):
            validate_structured_output(bad, schema)
        bad = copy.deepcopy(good)
        bad['links'][0]['type']='same_object'
        with self.assertRaises(ExternalIntelligenceError):
            validate_structured_output(bad, schema)
        reference = next(b['body_ref'] for b in s.default_semantic_input(work)['bodies'] if b['id']=='b')
        bad = copy.deepcopy(good)
        bad['links'][0]['target']={'asset_id':reference,'unit_id':'invented'}
        with self.assertRaises(ExternalIntelligenceError):
            validate_structured_output(bad, schema)
        bad['links'][0]['target']={'asset_id':reference,'selector':0}
        validate_structured_output(bad, schema)
        decoded = s.decode_organization(work,work[0],good,s.GROUNDED_REPRESENTATION)
        self.assertEqual(decoded['links'][0]['target']['selector']['exact'],'Only indoors.')

    def test_graph_reference_sources_use_the_same_lossless_locator_language(self):
        work = [{"id": "w", "organization_schema": "ownward.organization/v1",
                 "asset": {"id": "a", "revision": 1, "content": "Uses Beacon.\nKeep dry."},
                 "candidates": [{"id": "b", "revision": 2, "content": "Beacon\nOnly indoors.\nNot waterproof.", "explicit_contexts": []}]}]
        contract = s.SemanticInputContract(s.GROUNDED_REPRESENTATION, "test", None)
        encoded = contract.encode(work)
        self.assertTrue(contract.validate(work, encoded)["equivalent"])
        reference = encoded["reference_sources"][0]
        self.assertEqual("".join(reference["passages"].values()), work[0]["candidates"][0]["content"])
        org = {"units": [], "links": [{"type": "applies_in", "source": {"asset_id": 0},
                "target": {"asset_id": reference["source_ref"], "selector": [1, 2]}}]}
        decoded = s.decode_organization(work, work[0], org, s.GROUNDED_REPRESENTATION)
        self.assertEqual(decoded["links"][0]["target"]["asset_id"], "b")
        self.assertEqual(decoded["links"][0]["target"]["selector"]["exact"], "Only indoors.\nNot waterproof.")
        schema = contract.output_schema(work, {})
        self.assertNotIn('"exact"', __import__('json').dumps(schema))
        mention = schema['properties']['analyses']['items']['properties']['organization']['properties']['units']['items']['properties']['mentions']['items']
        self.assertIn('selector', mention['required'])
        self.assertNotIn({'type': 'null'}, mention['properties']['selector']['anyOf'])

    def test_relative_owner_survives_retry_with_reordered_work(self):
        work = [{"id": "wa", "asset": {"id": "a", "revision": 1, "content": "Use Beacon."}, "candidates": []},
                {"id": "wb", "asset": {"id": "b", "revision": 1, "content": "Keep indoors."}, "candidates": []}]
        for batch in [work, list(reversed(work)), [work[1]]]:
            org = {"units": [{"id": "u", "selector": 0}], "links": [{"type": "related_to", "source": {"asset_id": "self", "unit_id": "u"}, "target": {"asset_id": "self"}}]}
            result = s.decode_organization(batch, work[1], org, s.GROUNDED_REPRESENTATION)
            self.assertEqual(result['links'][0]['source']['asset_id'], 'b')
            self.assertEqual(result['links'][0]['target']['asset_id'], 'b')

    def test_graph_source_aliases_preserve_identity_and_cannot_cross_work(self):
        work = [{"id": "w", "organization_schema": "ownward.organization/v1", "asset": {"id": "a", "revision": 1, "content": "A uses B."},
                 "candidates": [{"id": "b", "revision": 1, "content": "B needs shelter."}, {"id": "c", "revision": 1, "content": "Unrelated."}]}]
        bodies = s.default_semantic_input(work)["bodies"]
        refs = {b["id"]: b["body_ref"] for b in bodies}
        org = {"units": [{"id": "u"}], "links": [{"type": "applies_in", "source": {"asset_id": refs['a']}, "target": {"asset_id": refs['b']}}]}
        result = s.decode_organization(work, work[0], org)
        self.assertEqual(result['links'][0]['source']['asset_id'], 'a')
        self.assertEqual(result['links'][0]['target']['asset_id'], 'b')
        self.assertEqual(org['links'][0]['source']['asset_id'], refs['a'])
        org['links'][0]['source']['asset_id'] = refs['c']
        with self.assertRaises(s.SemanticRepresentationError):
            s.decode_organization(work, work[0], org)

    def test_graph_passage_locators_preserve_context_and_reject_wrong_sources(self):
        raw = "Header\nUse Beacon.\nOnly indoors.\nSeparate history."
        work=[{"id":"w","organization_schema":"ownward.organization/v1","asset":{"id":"a","revision":1,"content":raw},"candidates":[{"id":"b","revision":1,"content":"Unnumbered reference."}]}]
        org={"units":[{"id":"u","selector":[1,2],"context":[0]}],"links":[{"type":"applies_in","source":{"asset_id":"a","unit_id":"u"},"target":{"asset_id":"b"}}]}
        decoded=s.decode_organization(work,work[0],org,s.GROUNDED_REPRESENTATION)
        unit=decoded['units'][0]
        self.assertEqual(unit['selector']['exact'],"Use Beacon.\nOnly indoors.\n")
        self.assertEqual(unit['context'][0]['exact'],"Header\n")
        self.assertIn(unit['selector']['prefix']+unit['selector']['exact']+unit['selector']['suffix'],raw)
        org['links'][0]['target']['selector']=99
        with self.assertRaises(s.SemanticRepresentationError):s.decode_organization(work,work[0],org,s.GROUNDED_REPRESENTATION)
        org['links'][0]['target'].pop('selector');org['units'][0]['selector']=[2,1]
        with self.assertRaises(s.SemanticRepresentationError):s.decode_organization(work,work[0],org,s.GROUNDED_REPRESENTATION)

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
