from __future__ import annotations

import copy
import json
from pathlib import Path
import tempfile
import unittest

import kernel_iteration_evidence as evidence
import kernel_iteration_stage4_resource_cost_compact_feasibility as feasibility


class CompactFeasibilityTests(unittest.TestCase):
    def source(self) -> dict:
        context = [{"key": "source", "value": "test"}]
        return {
            "representation": "ownward.semantic-deduplicated-body-table/v1",
            "bodies": [
                {"body_ref": "b0", "id": "a", "revision": 2, "content": "alpha"},
                {"body_ref": "b1", "id": "b", "revision": 3, "content": "beta"},
            ],
            "work": [{
                "work_id": "w",
                "asset": {"body_ref": "b0", "id": "a", "revision": 2, "explicit_contexts": context},
                "candidates": [{"body_ref": "b1", "id": "b", "revision": 3, "explicit_contexts": context, "semantic_similarity": 0.75, "relation": {"kind": "related"}}],
            }],
        }

    def test_compact_representation_round_trips_every_fact_and_relation(self) -> None:
        source = self.source()
        compact = feasibility.compact_semantic_input(source)
        feasibility.validate_equivalence(source, compact)
        self.assertEqual(compact["bodies"][0], ["a", 2, "alpha"])
        self.assertEqual(compact["work"][0][3][0][2]["relation"], {"kind": "related"})

    def test_equivalence_rejects_content_or_relation_drift(self) -> None:
        source = self.source()
        compact = feasibility.compact_semantic_input(source)
        changed = copy.deepcopy(compact)
        changed["bodies"][0][2] = "changed"
        with self.assertRaises(Exception):
            feasibility.validate_equivalence(source, changed)
        changed = copy.deepcopy(compact)
        changed["work"][0][3][0][2]["relation"] = {"kind": "other"}
        with self.assertRaises(Exception):
            feasibility.validate_equivalence(source, changed)

    def test_final_decision_records_balanced_authorization_and_open_create_wall(self) -> None:
        path = Path(__file__).parent / "iteration/v2/stage4-resource-cost-final-decision.json"
        value = json.loads(path.read_text(encoding="utf-8"))
        content = {key: item for key, item in value.items() if key != "identity"}
        self.assertEqual(value["identity"], evidence.canonical_sha256(content))
        token = value["decisions"]["semantic_input_tokens"]
        wall = value["decisions"]["end_to_end_wall_seconds"]
        self.assertEqual(token["candidate_component_maximum"], token["v0_candidate_component_baseline"] * 0.5)
        self.assertEqual(token["selection"], "candidate-composition-declared")
        self.assertEqual(token["frozen_request_prompt_and_schema_matches"], 12)
        self.assertEqual(token["current_status"], "closed")
        self.assertEqual(wall["authorized_routes"], [])
        historical = wall["historical_pre_matched_migrations"]
        self.assertEqual(historical["status"], "superseded-diagnostic-only")
        self.assertEqual(
            historical["candidate_controlled_maximum_seconds"],
            historical["v0_candidate_controlled_baseline_seconds"] * 0.5,
        )
        self.assertTrue(historical["document_batch_exact_vector_identity"])
        self.assertLess(historical["document_batch_conservative_improvement_seconds"], 0)
        matched = wall["matched_create_evidence"]
        self.assertLess(matched["v2_create_envelope_seconds"], matched["v0_create_envelope_seconds"])
        self.assertLess(matched["v2_embedding_seconds"], matched["v0_embedding_seconds"])
        gate = wall["active_matched_gate"]
        self.assertEqual(gate["controlled_half_maximum_seconds"], gate["v0_controlled_baseline_seconds"] * 0.5)
        self.assertGreater(gate["required_improvement_seconds"], 0)
        non_create = wall["same_observation_non_create_decomposition"]
        self.assertLess(non_create["maximum_evidenced_improvement_seconds"], gate["required_improvement_seconds"])
        lifecycle = wall["raw_vector_lifecycle_audit"]
        self.assertGreater(lifecycle["gross_opportunity_margin_seconds"], 0)
        self.assertEqual(lifecycle["proven_net_removable_upper_bound_under_exact_work_identity_seconds"], 0)
        self.assertFalse(lifecycle["implementation_authorized"])
        self.assertEqual(wall["cross_observation_diagnostic"]["status"], "not-an-observed-critical-path-and-not-route-headroom")
        self.assertEqual(wall["matched_create_status"], "v2-faster-than-v0-but-controlled-half-gate-still-fails")
        self.assertEqual(
            value["next_validation"],
            "freeze-and-prove-a-versioned-pending-versus-ready-retrieval-representation-lifecycle-on-independent-materials-before-revisiting-cost; preserve-semantic-work-candidates-immediate-pending-query-ready-ranking-restart-rebuild-and-failure-recovery",
        )
        self.assertFalse(value["stage4_complete"])

    def test_candidate_contract_is_lossless_without_mutating_the_formal_protocol(self) -> None:
        module = feasibility.validation._load_longmemeval_module(Path(__file__).parent)
        manifest = Path(__file__).parent.parents[2] / "manifests/kernel-candidates/v2/resource-cost/semantic-representation.json"
        contract = module.semantic_representation.load_contract(manifest)
        protocol = json.loads((Path(__file__).parents[2] / "longmemeval_s/protocol.json").read_text(encoding="utf-8"))
        module.validate_protocol(protocol)
        self.assertEqual(protocol["memory"]["semantic_input_representation"], module.semantic_representation.DEFAULT_REPRESENTATION)
        work = [{
            "id": "w",
            "asset": {"id": "a", "revision": 2, "content": "alpha", "contexts": [{"key": "source", "value": "test"}]},
            "candidates": [{"id": "b", "revision": 3, "content": "beta", "explicit_contexts": [], "semantic_similarity": 0.75, "relation": {"kind": "related"}}],
        }]
        capability = module.ExternalIntelligenceCapability(object(), contract)
        compact = capability.encoded_semantic_input(work)
        result = capability.validate_encoded_semantic_input(work, compact)
        self.assertEqual(compact["representation"], module.semantic_representation.COMPACT_REPRESENTATION)
        self.assertTrue(result["equivalent"])
        changed = copy.deepcopy(compact)
        changed["work"][0][3][0][2]["semantic_similarity"] = 0.1
        with self.assertRaises(Exception):
            capability.validate_encoded_semantic_input(work, changed)

    def test_existing_submission_preflight_rejects_invalid_analysis(self) -> None:
        module = feasibility.validation._load_longmemeval_module(Path(__file__).parent)
        original = self.source()
        call = {"work_ids": ["w"], "question_id": "q"}
        settings = {"semantic_model": "gpt-5.6-luna", "semantic_reasoning_effort": "low"}
        valid = {"analyses": [{"work_id": "w", "summary": "durable fact", "topics": [], "cues": []}]}
        with tempfile.TemporaryDirectory() as directory:
            identity = feasibility.validate_for_semantic_submission(
                module, original, valid, call, settings, Path(directory),
            )
            self.assertEqual(len(identity), 64)
        invalid = copy.deepcopy(valid)
        invalid["analyses"][0]["summary"] = ""
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaises(Exception):
                feasibility.validate_for_semantic_submission(
                    module, original, invalid, call, settings, Path(directory),
                )

    def test_balanced_decision_uses_repeat_error_and_token_gate(self) -> None:
        contract = {
            "execution": {"balanced_order": [["current", "compact"], ["compact", "current"]], "source_calls": 1},
            "token_gate": {"current_full_input_tokens": 100, "compact_full_input_tokens": 80, "matched_minimum_tokens": 60, "candidate_component_maximum": 20},
            "wall_gate": {"minimum_paired_requests": 2, "non_degradation_rule": "compact-minus-current-mean<=max-within-variant-repeat-range"},
        }

        def request(repeat: int, variant: str, wall: float, tokens: int) -> dict:
            return {
                "repeat": repeat, "variant": variant, "material": "development", "question_id": "q",
                "schema_identity": "s", "work_count": 1, "body_count": 2, "body_chars": 9,
                "source_representation_identity": "source", "cached_input_tokens": 0,
                "rate_limit_events": 0, "input_tokens": tokens, "wall_seconds": wall,
            }

        execution = {
            "requests": [
                request(1, "current", 4.0, 100), request(1, "compact", 4.5, 80),
                request(2, "current", 4.5, 100), request(2, "compact", 4.75, 80),
            ],
            "batch_elapsed_seconds": [
                {"repeat": 1, "variant": "current", "wall_seconds": 10.0},
                {"repeat": 1, "variant": "compact", "wall_seconds": 11.0},
                {"repeat": 2, "variant": "compact", "wall_seconds": 12.5},
                {"repeat": 2, "variant": "current", "wall_seconds": 12.0},
            ],
            "transport": {"discarded_cached_attempts": 0},
        }
        decision = feasibility.evaluate_balanced(execution, contract)
        self.assertTrue(decision["token"]["passed"])
        self.assertTrue(decision["wall"]["nondegraded"])
        self.assertTrue(decision["implementation_authorized"])
        for item in execution["requests"]:
            if item["variant"] == "compact":
                item["wall_seconds"] += 10.0
        decision = feasibility.evaluate_balanced(execution, contract)
        self.assertFalse(decision["wall"]["nondegraded"])
        self.assertFalse(decision["implementation_authorized"])


if __name__ == "__main__":
    unittest.main()
