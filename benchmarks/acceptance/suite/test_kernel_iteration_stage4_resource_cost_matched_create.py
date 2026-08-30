from __future__ import annotations

import subprocess
from pathlib import Path
import unittest

import kernel_iteration_stage4_resource_cost_matched_create as matched


SUITE_ROOT = Path(__file__).resolve().parent
REPOSITORY = SUITE_ROOT.parents[2]


class MatchedCreateCostTest(unittest.TestCase):
    def test_contract_is_result_before_and_formal_read_only(self) -> None:
        contract = matched.load_contract(SUITE_ROOT)
        self.assertTrue(contract["frozen_before_results"])
        self.assertFalse(contract["results_seen"])
        self.assertFalse(contract["model_execution_allowed"])
        self.assertEqual(contract["formal_state"]["sha256"], "3c7826e0ef86a82ddfab676886e384e2674a23dc80f6df3612d4443be00ffcdc")
        self.assertEqual(contract["cost_classification"]["shared_required_runtime_floor"], "minimum-paired-subject-embedding.ensure_running-mean")

    def test_v0_instrumentation_only_adds_observation_boundaries(self) -> None:
        def source(path: str) -> str:
            completed = subprocess.run(
                ["git", "show", f"99f519018df99bd5202b0c571b8e43481cd1b80e:{path}"],
                cwd=REPOSITORY, capture_output=True, text=True, encoding="utf-8", check=True,
            )
            return completed.stdout

        service = matched._instrument_v0_service(source("internal/core/service.go"))
        collaboration = matched._instrument_v0_collaboration(source("internal/core/collaboration.go"))
        derived = matched._instrument_v0_derived(source("internal/derived/store.go"))
        self.assertIn('"phase": "create.envelope"', service)
        self.assertIn('"phase": "embedding.documents"', collaboration)
        self.assertIn('"phase": "derived.durability_barrier"', derived)
        self.assertIn("s.embedder.EmbedDocuments(ctx, contents)", collaboration)
        self.assertIn("s.derivedStore.Put(record)", collaboration)

    def test_gate_subtracts_only_common_startup_and_keeps_v0_long_input_out_of_equality(self) -> None:
        contract = matched.load_contract(SUITE_ROOT)
        behavior = "behavior"

        def summary(subject: str) -> dict:
            v0 = subject == "v0"
            return {
                "concurrent_wall_seconds": 8.0 if v0 else 6.0,
                "phase_seconds": {
                    "create.envelope": 12.0 if v0 else 11.0,
                    "embedding.documents": 12.0 if v0 else 11.0,
                    "embedding.ensure_running": 6.0 if v0 else 7.0,
                },
                "phase_calls": {"embedding.documents": 3 if v0 else 8},
                "embedding_input_bytes": [100, 500] if v0 else [100],
                "embedding_vector_identities": ["shared", "v0-long"] if v0 else ["shared"],
                "behavior_identity": behavior,
                "trace_identities": [subject],
            }

        rounds = [
            {"round": 1, "subject_order": ["v0", "v2"], "subjects": {"v0": summary("v0"), "v2": summary("v2")}},
            {"round": 2, "subject_order": ["v2", "v0"], "subjects": {"v0": summary("v0"), "v2": summary("v2")}},
        ]
        observers = {"v0": {"identity": "observer-v0"}, "v2": {"identity": "observer-v2"}}
        equivalence = {
            "v0": {"behavior_identity": behavior, "equivalent": True},
            "v2": {"behavior_identity": behavior, "equivalent": True},
        }
        result = matched.evaluate(contract, observers, equivalence, rounds, contract["formal_state"]["sha256"])
        self.assertEqual(result["shared_cost_classification"]["common_ensure_running_seconds"], 6.0)
        self.assertTrue(result["shared_cost_classification"]["v0_also_embeds_the_long_document"])
        self.assertFalse(result["candidate_controlled_gate"]["passed"])


if __name__ == "__main__":
    unittest.main()
