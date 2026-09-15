import copy
import unittest
import tempfile
from pathlib import Path

import run
import semantic_representation as s
from external_intelligence import ExternalIntelligenceError, validate_structured_output


class ScopedOrganizationTests(unittest.TestCase):
    def setUp(self):
        self.work = [{"id": "wa", "organization_schema": "ownward.organization/v2",
                      "asset": {"id": "a", "revision": 1, "content": "Luna is our dog.\nKeep her indoors."},
                      "candidates": [{"id": "b", "revision": 2, "content": "My sister calls our dog Moon."}]}]
        self.contract = s.SemanticInputContract(s.GROUNDED_REPRESENTATION, "test", None)
        self.ref = self.contract.encode(self.work)["reference_sources"][0]["source_ref"]
        self.graph = {"schema": "ownward.organization/v2", "units": [],
                      "within_source": [{"type": "related_to", "meaning": "care condition for this dog",
                                         "source": {"asset_id": "self", "selector": 0},
                                         "target": {"asset_id": "self", "selector": 1}}],
                      "cross_source": [{"type": "same_object", "meaning": "names for the same dog",
                                        "source": {"asset_id": "self", "selector": 0, "object_name": "Luna"},
                                        "target": {"asset_id": self.ref, "selector": 0, "object_name": "Moon"}}]}

    def schema(self):
        return self.contract.output_schema(self.work, {})["properties"]["analyses"]["items"]["properties"]["organization"]

    def test_actual_request_and_decode_preserve_both_scopes(self):
        settings = {"semantic_batch_size": 20}
        prompt, schema, _ = run.ExternalIntelligenceCapability(None, self.contract).semantic_request(self.work, settings)
        self.assertIn("organization.within_source", prompt)
        self.assertIn("object_name", prompt)
        row = {"index": 0, "summary": 0, "topics": [], "cues": [], "organization": self.graph}
        validate_structured_output({"analyses": [row]}, schema)
        decoded = self.contract.decode_analyses(self.work, {"analyses": [row]})[0]
        self.assertEqual(len(decoded["organization"]["links"]), 2)
        self.assertEqual(decoded["organization"]["links"][1]["target"]["asset_id"], "b")
        self.assertEqual(decoded["organization"]["links"][0]["target"]["selector"]["exact"], "Keep her indoors.")
        self.assertIn("cross_source", row["organization"])

    def test_scope_budget_and_mixed_address_errors_are_rejected(self):
        for mutation in ("within_foreign", "cross_own_alias", "combined_budget", "two_shapes", "fake_unit", "ordinary_object"):
            with self.subTest(mutation=mutation):
                graph = copy.deepcopy(self.graph)
                if mutation == "within_foreign":
                    graph["within_source"][0]["target"]["asset_id"] = self.ref
                elif mutation == "cross_own_alias":
                    owner_ref = next(b["body_ref"] for b in s.default_semantic_input(self.work)["bodies"] if b["id"] == "a")
                    graph["cross_source"][0]["target"]["asset_id"] = owner_ref
                elif mutation == "combined_budget":
                    graph["within_source"] *= 128
                elif mutation == "two_shapes":
                    graph["links"] = []
                elif mutation == "fake_unit":
                    graph["cross_source"][0]["target"]["unit_id"] = "unpublished"
                else:
                    graph["cross_source"][0]["type"] = "same_as"
                with self.assertRaises((ExternalIntelligenceError, s.SemanticRepresentationError)):
                    validate_structured_output(graph, self.schema())
                    s.decode_organization(self.work, self.work[0], graph, s.GROUNDED_REPRESENTATION)

    def test_no_candidates_keeps_internal_relations(self):
        self.work[0]["candidates"] = []
        graph = copy.deepcopy(self.graph)
        graph["cross_source"] = []
        validate_structured_output(graph, self.schema())
        decoded = s.decode_organization(self.work, self.work[0], graph, s.GROUNDED_REPRESENTATION)
        self.assertEqual(len(decoded["links"]), 1)
        graph["cross_source"] = self.graph["cross_source"]
        with self.assertRaises(ExternalIntelligenceError):
            validate_structured_output(graph, self.schema())

    def test_internal_relation_conditions_can_reference_supplied_context(self):
        self.graph["within_source"][0]["conditions"] = [{"asset_id": self.ref, "selector": 0}]
        validate_structured_output(self.graph, self.schema())
        decoded = s.decode_organization(self.work, self.work[0], self.graph, s.GROUNDED_REPRESENTATION)
        self.assertEqual(decoded["links"][0]["conditions"][0]["asset_id"], "b")

    def test_legacy_work_keeps_original_wire_shape(self):
        self.work[0]["organization_schema"] = "ownward.organization/v1"
        schema = self.schema()
        self.assertIn("links", schema["properties"])
        self.assertNotIn("within_source", schema["properties"])
        prompt, _, _ = run.ExternalIntelligenceCapability(None, self.contract).semantic_request(self.work, {"semantic_batch_size": 20})
        self.assertNotIn("object_name", prompt)

    def test_multiple_decode_failures_repair_separately_and_resume_without_new_calls(self):
        from test_run import FakeTransport
        settings = run.load_json(Path(run.__file__).with_name('protocol.json'))['memory']
        work = self.work + [dict(id='w' + name, organization_schema='ownward.organization/v2',
                               asset=dict(id=name, revision=1, content='Source ' + name), candidates=[])
                            for name in ('c', 'd')]
        good = dict(index=0, summary=0, topics=[], cues=[], organization=self.graph)
        bad = [dict(index=i, summary=99, topics=[], cues=[], organization=dict(
            schema='ownward.organization/v2', units=[], within_source=[], cross_source=[])) for i in (1, 2)]
        transport = FakeTransport([{'analyses': [good, *bad]},
                                   *[{'analyses': [dict(row, index=0, summary=0)]} for row in bad]])
        cap = run.ExternalIntelligenceCapability(transport, self.contract)
        with tempfile.TemporaryDirectory() as directory:
            stage = Path(directory)
            result, usage = cap.semantics(work, settings, stage)
            progress = run.load_json(stage / 'organization-progress.json')
            cached, cached_usage = cap.semantics(work, settings, stage)
            self.assertEqual(progress['attempt'], settings['semantic_attempts'])
        self.assertEqual(transport.calls, 3)
        self.assertEqual(cached, result)
        self.assertEqual(cached_usage, usage)
        self.assertEqual([row['work_id'] for row in result], ['wa', 'wc', 'wd'])
        self.assertEqual(result[0]['organization']['links'][1]['target']['asset_id'], 'b')
        self.assertEqual({ref['id'] for ref in result[0]['input_assets']}, {'a', 'b', 'c', 'd'})
        self.assertEqual({ref['id'] for ref in result[1]['input_assets']}, {'c'})
        self.assertEqual({ref['id'] for ref in result[2]['input_assets']}, {'d'})

    def test_partial_repair_preserves_valid_scoped_result(self):
        from test_run import FakeTransport
        import json
        settings = json.loads(Path(run.__file__).with_name("protocol.json").read_text(encoding="utf-8"))["memory"]
        work = [*self.work, {"id": "wc", "organization_schema": "ownward.organization/v2",
                            "asset": {"id": "c", "revision": 1, "content": "Use the dry entrance."}, "candidates": []}]
        row = {"index": 0, "summary": 0, "topics": [], "cues": [], "organization": self.graph}
        bad = {"index": 1, "summary": 99, "topics": [], "cues": [],
               "organization": {"schema": "ownward.organization/v2", "units": [], "within_source": [], "cross_source": []}}
        fixed = {**bad, "index": 0, "summary": 0}
        transport = FakeTransport([{"analyses": [row, bad]}, {"analyses": [fixed]}])
        capability = run.ExternalIntelligenceCapability(transport, self.contract)
        with tempfile.TemporaryDirectory() as directory:
            decoded, _ = capability.semantics(work, settings, Path(directory))
            cached, _ = capability.semantics(work, settings, Path(directory))
        self.assertEqual(transport.calls, 2)
        self.assertEqual(decoded, cached)
        self.assertEqual(len(decoded[0]["organization"]["links"]), 2)
        self.assertEqual({v["id"] for v in decoded[0]["input_assets"]}, {"a", "b", "c"})
        self.assertEqual({v["id"] for v in decoded[1]["input_assets"]}, {"c"})


if __name__ == "__main__":
    unittest.main()
