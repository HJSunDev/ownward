from __future__ import annotations

import copy
import json
import struct
import tempfile
import unittest
import zlib
from pathlib import Path

import kernel_iteration


class KernelIterationTests(unittest.TestCase):
    def test_formal_storage_audit_reads_authority_and_verifies_binary_records(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            data = root / "questions" / "q1" / "ownward-data"
            (data / "assets").mkdir(parents=True)
            (data / "state").mkdir()
            events = [
                {"operation": "create", "value": {"id": "a", "revision": 1, "content": "first"}},
                {"operation": "update", "value": {"id": "a", "revision": 2, "content": "second revision"}},
            ]
            asset_bytes = "".join(json.dumps(item, separators=(",", ":")) + "\n" for item in events).encode()
            (data / "assets" / "information.jsonl").write_bytes(asset_bytes)
            metadata = json.dumps({
                "schema": "ownward.derived/v3", "asset_id": "a", "asset_revision": 2,
                "status": "ready", "analysis": {}, "semantic_work": {"asset": {"content": "second revision"}},
                "semantic_result": {"status": "complete"}, "embedding_space": "test",
            }, separators=(",", ":")).encode()
            header = b"OWD3" + struct.pack("<III", len(metadata), 0, zlib.crc32(metadata) & 0xFFFFFFFF)
            (data / "state" / "organization.binlog").write_bytes(header + metadata + b"DONE")

            result = kernel_iteration._audit_formal_storage(root, "identity")
            self.assertEqual(1, result["files"])
            self.assertEqual(len(asset_bytes), result["asset_log_bytes"])
            self.assertEqual(1, result["current_asset_count"])
            self.assertEqual(len("second revision"), result["current_content_chars"])
            self.assertEqual(result["asset_log_bytes"] + result["derived_bytes"], result["question_isolated_product_bytes"])

            corrupted = bytearray((data / "state" / "organization.binlog").read_bytes())
            corrupted[-5] ^= 0x01
            (data / "state" / "organization.binlog").write_bytes(corrupted)
            with self.assertRaises(kernel_iteration.KernelIterationError):
                kernel_iteration._audit_formal_storage(root, "identity")

    def test_observation_elapsed_accepts_go_fractional_precision(self) -> None:
        self.assertAlmostEqual(
            0.705696,
            kernel_iteration._observation_elapsed({
                "started_at": "2026-08-26T15:38:23.41786Z",
                "finished_at": "2026-08-26T15:38:24.1235562Z",
            }),
            places=6,
        )

    def test_classifies_only_proven_overflow_as_first_direction(self) -> None:
        mechanism, direction, status, samples = kernel_iteration.classify_problem(
            "target_evidence_not_read", True, True, True, 24001, "multi-session",
        )
        self.assertEqual(mechanism, "session_unit_context_overflow")
        self.assertEqual(direction, "information_representation_and_organization")
        self.assertEqual(status, "proven_general_mechanism")
        self.assertIn("granularity-multi-source", samples)

        pending = kernel_iteration.classify_problem(
            "target_evidence_not_read", True, True, True, 24000, "multi-session",
        )
        self.assertEqual(pending[0], "pending_search_order_or_context_selection")
        proven = kernel_iteration.classify_problem(
            "target_evidence_not_read", True, True, True, 18000, "multi-session", None,
            {
                "unread_expected_count": 1,
                "first_unread_is_expected": True,
                "first_unread_exceeds_remaining_budget": True,
                "all_expected_fit_empty_budget": True,
                "formal_read_is_exact_search_prefix": True,
            },
        )
        self.assertEqual(proven[0], "prefix_greedy_context_budget_starvation")
        self.assertEqual(proven[1], "retrieval_architecture_and_algorithm")

    def test_search_miss_requires_mechanical_representation_evidence(self) -> None:
        result = kernel_iteration.classify_problem(
            "target_evidence_not_search_returned", True, True, False, 12000, "temporal-reasoning",
        )
        self.assertEqual(result[0], "pending_semantic_organization_or_retrieval")
        self.assertEqual(result[1], "pending_cross_direction_evidence")
        proven = kernel_iteration.classify_problem(
            "target_evidence_not_search_returned", True, True, False, 12000, "temporal-reasoning",
            {
                "all_assets_have_oversized_embedding_failure": True,
                "latest_vector_count": 0,
                "expected_asset_vector_count": 0,
                "all_expected_semantic_results_submitted": True,
                "search_signal_kinds": ["lexical"],
            },
        )
        self.assertEqual(proven[0], "semantic_vector_representation_missing_after_oversized_input")
        self.assertEqual(proven[1], "semantic_capability_and_representation_model")

    def test_problem_pool_rejects_leaked_answer_field(self) -> None:
        problems = []
        for index in range(258):
            mechanism = "session_unit_context_overflow" if index < 119 else (
                "prefix_greedy_context_budget_starvation" if index < 133 else (
                    "semantic_vector_representation_missing_after_oversized_input" if index < 249 else "reader_failure_after_complete_evidence"
                )
            )
            problem = {"formal_question_identity": f"q-{index}", "mechanism": mechanism}
            if mechanism == "prefix_greedy_context_budget_starvation":
                offset = index - 119
                rank = 2 if offset < 8 else (3 if offset < 13 else 4)
                problem["observations"] = {"selection": {
                    "first_unread_is_expected": True,
                    "first_unread_rank": rank,
                    "expected_ranks": [rank],
                }}
            if mechanism == "semantic_vector_representation_missing_after_oversized_input":
                problem["observations"] = {"representation": {
                    "latest_asset_count": 46 if index < 248 else 153,
                    "latest_vector_count": 0,
                    "search_signal_kinds": ["lexical"],
                }}
            problems.append(problem)
        pool = {"schema": kernel_iteration.POOL_SCHEMA, "problem_count": 258, "problems": problems}
        kernel_iteration.validate_problem_pool(pool, strict=True)
        leaked = copy.deepcopy(pool)
        leaked["problems"][0]["product_answer"] = "forbidden"
        with self.assertRaises(kernel_iteration.KernelIterationError):
            kernel_iteration.validate_problem_pool(leaked, strict=True)

    def test_builds_semantic_candidate_with_closed_chain_protection(self) -> None:
        direction_values = {
            "semantic_search_recall": 1.0,
            "semantic_search_error_rate": 0.0,
            "long_asset_vector_recovery": 1.0,
            "short_asset_vector_availability": 1.0,
            "semantic_input_marker_coverage": 1.0,
            "restart_semantic_recall": 1.0,
            "rebuild_semantic_recall": 1.0,
            "semantic_signal_rate": 1.0,
            "derived_vectors_per_asset": 1.0,
            "semantic_input_to_source_bytes": 0.1,
            "oversized_raw_embedding_attempts": 0.0,
            "semantic_embedding_calls": 1.0,
            "rebuild_semantic_embedding_calls": 0.0,
            "semantic_representation_p95_ms": 25.0,
            "semantic_rebuild_p95_ms": 20.0,
            "semantic_search_p95_ms": 2.0,
        }
        observations = {
            "direction": {"metrics": [{"name": name, "value": value} for name, value in direction_values.items()]},
            "granularity_protection": {"metrics": [
                {"name": name, "value": 1.0}
                for name in ("required_evidence_budget_recall", "scale_evidence_recall", "budget_fit_protection", "fusion_recall", "fusion_ndcg")
            ]},
            "protection": {"metrics": [{"name": "identity_stability", "value": 1.0}]},
        }
        gate = {
            "semantic_search_recall_min": 1.0,
            "semantic_search_error_rate_max": 0.0,
            "long_asset_vector_recovery_min": 1.0,
            "short_asset_vector_availability_min": 1.0,
            "semantic_input_marker_coverage_min": 1.0,
            "restart_semantic_recall_min": 1.0,
            "rebuild_semantic_recall_min": 1.0,
            "semantic_signal_rate_min": 1.0,
            "derived_vectors_per_asset_max": 1.0,
            "semantic_input_to_source_bytes_max": 1.0,
            "oversized_raw_embedding_attempts_max": 0.0,
            "semantic_embedding_calls_max": 2.0,
            "rebuild_semantic_embedding_calls_max": 0.0,
            "semantic_representation_p95_ms_max": 180000.0,
            "semantic_rebuild_p95_ms_max": 180000.0,
            "semantic_search_p95_ms_max": 78.0,
            "closed_granularity_recall_min": 1.0,
            "closed_granularity_scale_recall_min": 1.0,
        }
        result = kernel_iteration.build_semantic_candidate_result(
            {}, {"selection": {"cluster": "semantic-vector"}}, observations,
            {"direction": 1.0, "granularity_protection": 1.0, "protection": 1.0},
            {"optimization_view": {"frozen_gate": gate}}, "worktree:test",
        )
        self.assertTrue(result["passed"])
        observations["direction"]["metrics"] = [
            {**item, "value": 79.0} if item["name"] == "semantic_search_p95_ms" else item
            for item in observations["direction"]["metrics"]
        ]
        self.assertFalse(kernel_iteration.build_semantic_candidate_result(
            {}, {"selection": {}}, observations,
            {"direction": 1.0, "granularity_protection": 1.0, "protection": 1.0},
            {"optimization_view": {"frozen_gate": gate}}, "worktree:slow",
        )["passed"])

    def test_builds_budget_candidate_with_both_closed_chain_protections(self) -> None:
        observations = {
            "direction": {"metrics": [
                {"name": "required_evidence_item_recall", "value": 1.0},
                {"name": "required_evidence_question_recall", "value": 1.0},
                {"name": "required_evidence_question_error_rate", "value": 0.0},
                {"name": "rank_depth_diagonal_order_rate", "value": 1.0},
                {"name": "top_rank_three_passage_coverage", "value": 1.0},
                {"name": "multi_source_multi_depth_coverage", "value": 1.0},
                {"name": "budget_skip_continuation_rate", "value": 1.0},
                {"name": "lazy_evidence_search_rate", "value": 1.0},
                {"name": "context_budget_compliance", "value": 1.0},
                {"name": "read_limit_compliance", "value": 1.0},
                {"name": "selection_policy_identity_rate", "value": 1.0},
                {"name": "exact_repeatability_rate", "value": 1.0},
                {"name": "consumer_retrieval_p95_ms", "value": 5.0},
            ]},
            "semantic_protection": {"metrics": [
                {"name": "semantic_search_recall", "value": 1.0},
                {"name": "semantic_search_error_rate", "value": 0.0},
                {"name": "long_asset_vector_recovery", "value": 1.0},
                {"name": "restart_semantic_recall", "value": 1.0},
                {"name": "rebuild_semantic_recall", "value": 1.0},
                {"name": "derived_vectors_per_asset", "value": 1.0},
            ]},
            "granularity_protection": {"metrics": [
                {"name": name, "value": 1.0}
                for name in ("required_evidence_budget_recall", "scale_evidence_recall", "budget_fit_protection", "fusion_recall", "fusion_ndcg")
            ]},
            "protection": {"metrics": [{"name": "identity_stability", "value": 1.0}]},
        }
        gate = {
            "required_evidence_item_recall_min": 1.0,
            "required_evidence_question_recall_min": 1.0,
            "required_evidence_question_error_rate_max": 0.0,
            "rank_depth_diagonal_order_rate_min": 1.0,
            "top_rank_three_passage_coverage_min": 1.0,
            "multi_source_multi_depth_coverage_min": 1.0,
            "budget_skip_continuation_rate_min": 1.0,
            "lazy_evidence_search_rate_min": 1.0,
            "context_budget_compliance_min": 1.0,
            "read_limit_compliance_min": 1.0,
            "selection_policy_identity_rate_min": 1.0,
            "consumer_retrieval_p95_ms_max": 600.0,
        }
        durations = {"direction": 1.0, "semantic_protection": 1.0, "granularity_protection": 1.0, "protection": 1.0}
        result = kernel_iteration.build_budget_candidate_result(
            {}, {"selection": {"cluster": "prefix-greedy"}}, observations, durations,
            {"optimization_view": {"frozen_gate": gate}}, "worktree:test",
        )
        self.assertTrue(result["passed"])
        observations["direction"]["metrics"] = [
            {**item, "value": 601.0} if item["name"] == "consumer_retrieval_p95_ms" else item
            for item in observations["direction"]["metrics"]
        ]
        self.assertFalse(kernel_iteration.build_budget_candidate_result(
            {}, {"selection": {}}, observations, durations,
            {"optimization_view": {"frozen_gate": gate}}, "worktree:slow",
        )["passed"])

    def test_builds_baseline_and_candidate_gate_results(self) -> None:
        protected = [
            {"name": name, "value": 1.0}
            for name in ("budget_fit_protection", "identity_stability", "fusion_recall", "fusion_ndcg")
        ]
        efficiency = [
            {"name": name, "value": 0.0, "repeatability_error": 0.0}
            for name in (
                "organization_input_overhead_ratio", "organization_vector_overhead_ratio",
                "derived_record_overhead_ratio", "derived_vector_overhead_ratio",
                "rebuild_input_overhead_ratio", "rebuild_vector_overhead_ratio",
                "rebuilt_record_overhead_ratio", "rebuilt_vector_overhead_ratio",
            )
        ]
        efficiency.extend([
            {"name": "v0_query_workflow_p95_ms", "value": 10.0, "repeatability_error": 1.0},
            {"name": "candidate_query_workflow_p95_ms", "value": 11.0, "repeatability_error": 0.5},
            {"name": "v0_query_content_runes", "value": 1000.0, "repeatability_error": 0.0},
            {"name": "candidate_query_content_runes", "value": 500.0, "repeatability_error": 0.0},
            {"name": "v0_query_serialized_bytes", "value": 3000.0, "repeatability_error": 0.0},
            {"name": "candidate_query_serialized_bytes", "value": 2000.0, "repeatability_error": 0.0},
            {"name": "v0_organization_real_p95_ms", "value": 20.0, "repeatability_error": 1.0},
            {"name": "candidate_organization_real_p95_ms", "value": 21.0, "repeatability_error": 1.0},
            {"name": "v0_rebuild_real_p95_ms", "value": 40.0, "repeatability_error": 1.0},
            {"name": "candidate_rebuild_real_p95_ms", "value": 41.0, "repeatability_error": 1.0},
            {"name": "v0_derived_storage_bytes_per_source_rune", "value": 2.0, "repeatability_error": 0.1},
            {"name": "candidate_derived_storage_bytes_per_source_rune", "value": 2.1, "repeatability_error": 0.1},
        ])
        observations = {
            "direction": {"metrics": [
                {"name": "required_evidence_budget_recall", "value": 1.0},
                {"name": "required_evidence_budget_error_rate", "value": 0.0},
                {"name": "scale_evidence_recall", "value": 1.0},
                *efficiency,
                *protected[:2],
            ]},
            "protection": {"metrics": protected[2:]},
        }
        pool = {"selection": {"cluster": "session_unit_context_overflow"}}
        durations = {"direction": 1.0, "protection": 0.5}
        baseline = kernel_iteration.build_baseline({}, pool, observations, durations)
        self.assertEqual(baseline["schema"], kernel_iteration.BASELINE_SCHEMA)
        self.assertEqual(baseline["baseline"]["required_evidence_budget_recall"], 1.0)

        view = {"optimization_view": {
            "v0_baseline": {
                "budget_fit_protection": 1.0,
                "identity_stability": 1.0,
                "fusion_recall": 1.0,
                "fusion_ndcg": 1.0,
            },
            "v0_efficiency_baseline": {
                name: 0.0 for name in (
                    "organization_input_overhead_ratio", "organization_vector_overhead_ratio",
                    "derived_record_overhead_ratio", "derived_vector_overhead_ratio",
                    "rebuild_input_overhead_ratio", "rebuild_vector_overhead_ratio",
                    "rebuilt_record_overhead_ratio", "rebuilt_vector_overhead_ratio",
                )
            },
            "frozen_gate": {
                "required_evidence_budget_recall_min": 0.7778,
                "required_evidence_budget_error_rate_max": 0.3056,
                "scale_evidence_recall_min": 1.0,
                "query_workflow_p95_ms_max": 600.0,
                "query_workflow_limit_source": "thresholds.json#limits.warm_query_p95_ms_max",
            },
        }}
        candidate = kernel_iteration.build_candidate_result({}, pool, observations, durations, view, "worktree:test")
        self.assertTrue(candidate["passed"])
        self.assertEqual(candidate["gate"]["protected_regressions"], [])
        query_cost = candidate["metrics"]["efficiency_metrics"]["candidate_query_workflow_p95_ms"]
        self.assertTrue(query_cost["relative_passed"])
        self.assertTrue(query_cost["absolute_passed"])

        observations["direction"]["metrics"] = [
            {**item, "value": 30.0}
            if item["name"] == "candidate_query_workflow_p95_ms" else item
            for item in observations["direction"]["metrics"]
        ]
        relatively_slow = kernel_iteration.build_candidate_result({}, pool, observations, durations, view, "worktree:slow")
        self.assertFalse(relatively_slow["passed"])
        slow_cost = relatively_slow["metrics"]["efficiency_metrics"]["candidate_query_workflow_p95_ms"]
        self.assertFalse(slow_cost["relative_passed"])
        self.assertTrue(slow_cost["absolute_passed"])

    def test_upper_bound_ignores_only_floating_point_roundoff(self) -> None:
        self.assertTrue(kernel_iteration._within_upper_bound(42.940033436213994, 42.94003343621399))
        self.assertFalse(kernel_iteration._within_upper_bound(42.9401, 42.94003343621399))


if __name__ == "__main__":
    unittest.main()
