from __future__ import annotations

import json
from pathlib import Path
import sys
import tempfile
import unittest


HERE = Path(__file__).resolve().parent
REPOSITORY = HERE.parents[2]
sys.path.insert(0, str(HERE))

import kernel_iteration_candidate_latency as candidate  # noqa: E402
import kernel_iteration_evidence as evidence  # noqa: E402
import kernel_iteration_stage4_latency_candidate as latency  # noqa: E402
import kernel_iteration_stage4_latency_data as latency_data  # noqa: E402
import kernel_iteration_stage4_latency_finalize as finalizer  # noqa: E402
import kernel_iteration_stage4_latency_performance as performance  # noqa: E402
import kernel_iteration_stage4_latency_real_scale as real_scale  # noqa: E402
import kernel_iteration_stage4_vector_runtime_followup as runtime_followup  # noqa: E402
import kernel_iteration_stage4_semantic_model_screening as semantic_models  # noqa: E402
import kernel_iteration_stage4_hierarchical_feasibility as hierarchical  # noqa: E402
import kernel_iteration_stage4_latency_comparability as comparability  # noqa: E402
import kernel_iteration_stage4_latency_tail as latency_tail  # noqa: E402


class Stage4RetrievalLatencyTests(unittest.TestCase):
    def test_hierarchical_feasibility_contract_freezes_exact_proof_and_cost_gates(self) -> None:
        path = HERE / "iteration" / "v2" / "stage4-hierarchical-retrieval-feasibility-contract.json"
        contract = json.loads(path.read_text(encoding="utf-8"))
        hierarchical.validate_contract(REPOSITORY, contract)
        self.assertTrue(contract["frozen_before_feasibility_results"])
        self.assertFalse(contract["candidate_implementation_allowed_before_pass"])
        self.assertEqual(0.95, contract["gates"]["representative_safe_bypass_fraction_minimum"])
        self.assertEqual(0.05, contract["gates"]["fallback_request_fraction_maximum"])
        self.assertIn("returned-source-order", contract["proof_obligation"]["must_preserve"])
        self.assertIn("context-bytes", contract["proof_obligation"]["must_preserve"])
        self.assertEqual(22, sum(item["distinct_requests"] for item in contract["representative_oracle_traces"]))

    def test_hierarchical_worst_case_semantic_contribution_reverses_direct_order(self) -> None:
        theorem = hierarchical._derive_worst_case_theorem({"rrf_offset": 60})
        self.assertTrue(theorem["direct_order_reversible_with_three_semantic_eligible_documents"])
        self.assertGreater(
            theorem["lexical_rank_2_plus_semantic_rank_1"],
            theorem["lexical_rank_1_plus_semantic_rank_3"],
        )

    def test_hierarchical_trace_drift_fails_open(self) -> None:
        contract = json.loads((HERE / "iteration" / "v2" / "stage4-hierarchical-retrieval-feasibility-contract.json").read_text(encoding="utf-8"))
        theorem = hierarchical._derive_worst_case_theorem(contract["fusion_contract"])
        with tempfile.TemporaryDirectory(dir=REPOSITORY) as temporary:
            path = Path(temporary) / "trace.json"
            content = {
                "identity": "1" * 64, "formal": False, "formal_state_written": False,
                "observation": {"case_evidence": [{"selection": {"returned_sources": 2}}]},
            }
            path.write_text(json.dumps(content), encoding="utf-8")
            spec = {
                "name": "drift", "path": str(path.relative_to(REPOSITORY)),
                "sha256": "0" * 64, "identity": "1" * 64,
                "kind": "execution-result", "distinct_requests": 1,
                "minimum_semantic_eligible_documents": 3,
            }
            with self.assertRaisesRegex(Exception, "摘要漂移"):
                hierarchical._evaluate_trace(REPOSITORY, spec, theorem)

    def test_semantic_model_screening_contract_is_frozen_before_results_and_preserves_margin(self) -> None:
        path = HERE / "iteration" / "v2" / "stage4-semantic-model-screening-contract.json"
        contract = json.loads(path.read_text(encoding="utf-8"))
        semantic_models._validate_contract(REPOSITORY, contract)
        self.assertTrue(contract["frozen_before_candidate_results"])
        self.assertFalse(contract["candidate_results_seen"])
        self.assertEqual(2, len(contract["candidates"]))
        self.assertEqual(
            [item["key"] for item in contract["candidates"]],
            contract["order"],
        )
        self.assertLess(contract["gates"]["isolated_query_mean_ms_maximum"], 41.201)
        self.assertLess(contract["gates"]["isolated_query_p95_ms_maximum"], 78.001)
        self.assertEqual(
            "precise-fail-open-hierarchical-retrieval-with-semantic-fallback",
            contract["next_if_all_rejected"],
        )

    def test_semantic_model_receipt_rejects_revision_and_artifact_drift(self) -> None:
        spec = {
            "repository": "example/model", "revision": "a" * 40,
            "license": "mit", "weight": "onnx/model.onnx",
        }
        with tempfile.TemporaryDirectory(dir=REPOSITORY) as temporary:
            root = Path(temporary)
            (root / "onnx").mkdir()
            weight = root / "onnx" / "model.onnx"
            weight.write_bytes(b"model")
            content = {
                "repository": spec["repository"], "revision": spec["revision"],
                "license": spec["license"], "files": {"onnx/model.onnx": evidence.file_sha256(weight)},
            }
            receipt = {**content, "identity": evidence.canonical_sha256(content)}
            semantic_models._validate_receipt(root, spec, receipt)
            weight.write_bytes(b"tampered")
            with self.assertRaisesRegex(Exception, "摘要漂移"):
                semantic_models._validate_receipt(root, spec, receipt)
            weight.write_bytes(b"model")
            wrong = {**receipt, "revision": "b" * 40}
            with self.assertRaisesRegex(Exception, "修订错绑"):
                semantic_models._validate_receipt(root, spec, wrong)

    def test_performance_gate_uses_formal_retrieval_time_not_controller_wall(self) -> None:
        summary = performance._summarize([
            {
                "wall_ms": 99.0, "search_ms": 2.0, "evidence_search_ms": 3.0, "read_ms": 5.0,
                "protocol_overhead_ms": 89.0, "search_calls": 1, "evidence_search_calls": 1,
                "read_calls": 1, "returned_sources": 1, "evidence_probes": 1, "read_units": 1,
                "context_chars": 10,
            }
        ], 1)
        self.assertEqual(10.0, summary["mean_ms"])
        self.assertEqual(99.0, summary["wall_mean_ms"])

    def test_route_freezes_two_stage_root_and_all_execution_identities(self) -> None:
        route = latency.load_route(HERE)
        self.assertTrue(route["frozen_before_candidate_measurement"])
        self.assertEqual("closed-by-system-level-four-worker-thread-budget", route["root"]["status"])
        self.assertEqual(
            "exact-query-vector-plus-persistent-loopback-and-lazy-bounded-parallel-evidence-read-through/v18",
            route["implementation"]["policy"],
        )
        self.assertEqual(evidence.file_sha256(HERE.parents[1] / "support" / "ownward_mcp.py"), route["implementation"]["mcp_transport_sha256"])
        self.assertEqual(evidence.file_sha256(HERE / "kernel_iteration_stage4_latency_candidate_data.py"), route["candidate_preparer_sha256"])
        self.assertEqual(600.0, route["gates"]["complete_consumer_retrieval_p95_absolute_maximum_ms"])
        self.assertEqual(553.0, route["gates"]["complete_consumer_retrieval_p95_decision_maximum_ms"])
        self.assertEqual(41.1999999999025, route["gates"]["historical_v0_community_retrieval_mean_ms_diagnostic"])
        self.assertEqual(77.9999999977008, route["gates"]["historical_v0_community_retrieval_p95_ms_diagnostic"])
        self.assertFalse(route["comparability_audit"]["same_scale"])
        self.assertFalse(route["comparability_audit"]["retrieval_latency_closed"])
        self.assertEqual("historical-mechanism-diagnostic-only", route["rejected_routes_disposition"]["status"])
        self.assertFalse(route["rejected_routes_disposition"]["may_close_or_reject_under_current_latency_policy"])
        self.assertEqual("6/2", route["runtime_calibration"]["selected"])
        self.assertFalse(route["runtime_calibration"]["absolute_end_to_end_gate_met"])
        self.assertFalse(route["implementation"]["active_product_runtime_changed"])
        self.assertEqual(38, route["real_scale_measurement"]["asset_range"][0])
        self.assertEqual(62, route["real_scale_measurement"]["asset_range"][1])
        self.assertFalse(route["real_scale_measurement"]["eligible_for_closure"])
        self.assertTrue(route["runtime_followup"]["exact_query_lower_bound_exceeds_total_mean_gate"])
        assessment = route["runtime_implementation_assessment"]
        self.assertTrue(assessment["all_realistic_fixed_model_runtime_implementations_rejected"])
        self.assertFalse(assessment["retrieval_latency_closed"])
        self.assertEqual("rejected-latency-and-vector-drift", assessment["vulkan"]["status"])
        self.assertEqual("rejected-query-http-500", assessment["openvino"]["status"])
        self.assertEqual("rejected-no-device", assessment["sycl"]["status"])
        screening = route["semantic_model_screening"]
        self.assertEqual([], screening["first_layer_winners"])
        self.assertFalse(screening["v2_vector_generation_created"])
        self.assertFalse(screening["retrieval_latency_closed"])
        self.assertEqual("rejected-isolated-latency", screening["multilingual_e5_small"]["status"])
        self.assertEqual("rejected-breadth-quality", screening["paraphrase_multilingual_minilm_l12_v2"]["status"])
        feasibility = route["hierarchical_retrieval_feasibility"]
        self.assertEqual(0, feasibility["safe_bypass_requests"])
        self.assertEqual(0.95, feasibility["required_bypass_fraction"])
        self.assertFalse(feasibility["candidate_implemented"])
        self.assertFalse(feasibility["retrieval_latency_closed"])
        tail = route["tail_route_probe"]
        self.assertEqual(63.211, tail["required_isolated_gain_ms"])
        self.assertLess(tail["ideal_evidence_parallel_p95_gain_ms"], tail["required_isolated_gain_ms"])
        self.assertGreater(tail["shared_service_p95_ms"], tail["four_independent_service_p95_ms"])
        self.assertGreater(tail["shared_service_maximum_vector_component_drift"], 0.0000001)
        self.assertIsNone(tail["selected_route"])
        self.assertFalse(tail["candidate_implemented"])
        resolution = route["system_thread_budget_resolution"]
        self.assertEqual({"threads": 2, "threads_batch": 2, "parallel": 1}, resolution["runtime_configuration"])
        self.assertEqual(8, resolution["aggregate_inference_threads"])
        self.assertLessEqual(resolution["complete_consumer_p95_ms"], resolution["route_p95_ms_maximum"])
        self.assertEqual(0.0, resolution["vector_maximum_component_drift"])
        self.assertTrue(resolution["resume_byte_identical_and_zero_execution"])
        self.assertTrue(resolution["retrieval_latency_closed"])

    def test_tail_contract_freezes_same_request_and_two_route_early_exit(self) -> None:
        contract = latency_tail._load_contract(HERE)
        self.assertTrue(contract["frozen_before_measurement"])
        self.assertFalse(contract["results_seen"])
        self.assertEqual(4, contract["schedule"]["workers"])
        self.assertEqual(1, contract["shared_vector_probe"]["service_count"])
        self.assertEqual(4, contract["shared_vector_probe"]["concurrent_requests"])
        self.assertEqual(553.0, contract["gates"]["complete_consumer_p95_ms_maximum"])
        self.assertEqual(63.211, contract["gates"]["observed_gap_ms"] + contract["gates"]["additional_engineering_margin_ms"])

    def test_tail_summary_uses_same_request_not_independent_stage_percentiles(self) -> None:
        contract = {"gates": {"quality_trace_byte_equivalent": True}}
        samples = [
            {
                "case_id": "a", "wall_ms": 111.0, "trace_sha256": "a", "context_chars": 10,
                "returned_sources": 2, "read_units": 2, "search_ms": 100.0,
                "calls": [
                    {"tool": "ownward_search", "elapsed_ms": 100.0},
                    {"tool": "ownward_evidence_search", "elapsed_ms": 6.0},
                    {"tool": "ownward_evidence_search", "elapsed_ms": 4.0},
                    {"tool": "ownward_evidence_read", "elapsed_ms": 1.0},
                ],
            },
            {
                "case_id": "b", "wall_ms": 105.0, "trace_sha256": "b", "context_chars": 10,
                "returned_sources": 2, "read_units": 2, "search_ms": 5.0,
                "calls": [
                    {"tool": "ownward_search", "elapsed_ms": 5.0},
                    {"tool": "ownward_evidence_search", "elapsed_ms": 50.0},
                    {"tool": "ownward_evidence_read", "elapsed_ms": 50.0},
                ],
            },
        ]
        summary = latency_tail._summarize_tail(samples, contract)
        self.assertEqual("a", summary["decisive_p95_request"]["case_id"])
        self.assertEqual(111.0, summary["current_p95_ms"])
        self.assertGreater(summary["ideal_evidence_parallel_p95_ms"], 100.0)

    def test_comparability_contract_replaces_only_the_non_equivalent_latency_gate(self) -> None:
        audit = comparability.load_audit_contract(REPOSITORY)
        migration = comparability.load_migration_receipt(REPOSITORY, audit)
        self.assertFalse(comparability.profiles_same_scale(migration["audit_matrix"]))
        self.assertEqual(249, 116 + 133)
        replacement = migration["replacement_latency_policy"]
        self.assertEqual(600.0, replacement["absolute_maximum_ms"])
        self.assertEqual(47.0, replacement["frozen_repeatability_error_ms"])
        self.assertEqual(553.0, replacement["decision_maximum_ms"])
        self.assertIn("same-source-fragment-delivery", migration["evidence_disposition"]["preserved_unmodified"])
        self.assertIn("v0-community-41.2-78-latency", migration["evidence_disposition"]["diagnostic_only"])

    def test_existing_complete_consumer_result_requires_repeatability_margin(self) -> None:
        audit = comparability.load_audit_contract(REPOSITORY)
        migration = comparability.load_migration_receipt(REPOSITORY, audit)
        policy = audit["post_audit_measurement_policy"]

        def result(p95: float) -> dict[str, object]:
            return {
                "identity": policy["eligible_existing_result_identity"],
                "formal": False,
                "formal_state_written": False,
                "schedule": {
                    "workers": 4,
                    "warmups_per_round": 1,
                    "measured_repetitions_per_round": 3,
                    "balanced_order": [["v0", "previous-v2", "candidate"]] * 3,
                },
                "metrics": {
                    "candidate": {
                        "asset_count_range": [38, 62],
                        "target_delivery_complete": True,
                        "p95_ms": p95,
                    },
                    "quality_complete": True,
                    "resource_bounds_complete": True,
                },
            }

        observed = comparability.evaluate_existing_measurement(result(596.2113), audit, migration)
        self.assertFalse(observed["latency_gate_passed"])
        self.assertAlmostEqual(643.2113, observed["p95_with_repeatability_margin_ms"])
        bounded = comparability.evaluate_existing_measurement(result(553.0), audit, migration)
        self.assertTrue(bounded["latency_gate_passed"])

    def test_real_scale_contract_freezes_formal_shape_and_shared_runtime(self) -> None:
        contract = real_scale.load_contract(HERE)
        materials = real_scale.load_materials(HERE)
        self.assertEqual({"threads": 6, "threads_batch": 6, "parallel": 2}, contract["runtime_configuration"])
        self.assertEqual(4, contract["schedule"]["workers"])
        self.assertEqual([38, 46, 54, 62], [item["asset_count"] for item in materials["cases"]])
        self.assertEqual(24, materials["generation"]["search_limit"])
        self.assertEqual(8, materials["generation"]["read_limit"])
        self.assertEqual(24000, materials["generation"]["context_max_chars"])

    def test_runtime_followup_contract_tests_bounded_slots_and_batch_without_changing_model(self) -> None:
        contract = runtime_followup._load_contract(HERE)
        trials = {item["name"]: item for item in contract["trials"]}
        self.assertEqual((1, 6, 2), (
            trials["isolated-selected-6-2"]["active_requests"],
            trials["isolated-selected-6-2"]["threads"],
            trials["isolated-selected-6-2"]["parallel"],
        ))
        self.assertEqual(3, trials["three-active-2-1"]["active_requests"])
        self.assertEqual(4, trials["formal-four-active-2-1"]["active_requests"])
        self.assertEqual(4, trials["single-runtime-batch-four-6-1"]["batch_size"])
        self.assertEqual(0.0000001, contract["gates"]["maximum_vector_component_drift"])

    def test_runtime_implementation_contracts_freeze_same_model_space_and_hard_gates(self) -> None:
        names = [
            "stage4-retrieval-latency-runtime-implementation-contract.json",
            "stage4-retrieval-latency-runtime-implementation-batch2-contract.json",
        ]
        contracts = [json.loads((HERE / "iteration" / "v2" / name).read_text(encoding="utf-8")) for name in names]
        for contract in contracts:
            self.assertTrue(contract["frozen_before_measurement"])
            self.assertFalse(contract["candidate_results_seen"])
            self.assertEqual(
                "6fa0c02a9c302be6f977521d399b4de3a46310a4f2621ee0063747881b673f67",
                contract["reference"]["model_sha256"],
            )
            self.assertEqual("emb_79f072bf21c0c0f5226fa4fe6f1946a5", contract["reference"]["space_id"])
            self.assertEqual(41.201, contract["gates"]["retrieval_mean_ms_maximum_exclusive"])
            self.assertEqual(62.4, contract["gates"]["exact_query_p95_ms_maximum"])
            self.assertEqual(0.0000001, contract["gates"]["maximum_vector_component_drift"])
            self.assertEqual(
                "3c7826e0ef86a82ddfab676886e384e2674a23dc80f6df3612d4443be00ffcdc",
                contract["formal_state_sha256"],
            )

    def test_performance_materials_are_independent_read_only_and_quality_disjoint(self) -> None:
        materials = latency_data.load_materials(HERE)
        serialized = json.dumps(materials, ensure_ascii=False)
        self.assertTrue(materials["performance_only"])
        self.assertFalse(materials["quality_material_overlap"])
        self.assertFalse(materials["contains_formal_questions_answers_gold_content_outputs_or_case_ids"])
        self.assertEqual(4, len(materials["cases"]))
        self.assertEqual(8, materials["generation"]["sources_per_case"])
        self.assertNotIn("answer_session_ids", serialized)
        self.assertNotIn("question_id", serialized)

    def test_candidate_transform_is_exact_and_keeps_current_source_unchanged(self) -> None:
        source = REPOSITORY / "internal" / "core" / "service.go"
        original = source.read_bytes()
        transform = REPOSITORY / "manifests" / "kernel-candidates" / "v2" / "retrieval-latency" / "service-transform.json"
        with tempfile.TemporaryDirectory(dir=REPOSITORY) as temporary:
            output = Path(temporary) / "service.go"
            candidate._render_transformed_source(REPOSITORY, transform, output)
            rendered = output.read_text(encoding="utf-8")
        self.assertEqual(original, source.read_bytes())
        self.assertNotIn("RankOrganizedSemantics", rendered)
        self.assertIn("EmbedQuery(ctx, input.Query)", rendered)
        self.assertIn("ProbeEvidence", rendered)
        self.assertNotIn("semantic-organized-query", rendered)
        self.assertNotIn("assetCount", rendered)
        self.assertIn("EmbedQuery", rendered)
        self.assertIn("CachedReferences", rendered)
        self.assertIn("ReadCached", rendered)
        self.assertIn("evidencePlans.Reset()", rendered)

        runtime_source = REPOSITORY / "internal" / "embedding" / "llama.go"
        runtime_original = runtime_source.read_bytes()
        runtime_transform = REPOSITORY / "manifests" / "kernel-candidates" / "v2" / "retrieval-latency" / "managed-runtime-transform.json"
        with tempfile.TemporaryDirectory(dir=REPOSITORY) as temporary:
            runtime_output = Path(temporary) / "llama.go"
            candidate._render_transformed_source(REPOSITORY, runtime_transform, runtime_output)
            runtime_rendered = runtime_output.read_text(encoding="utf-8")
        self.assertEqual(runtime_original, runtime_source.read_bytes())
        self.assertIn("OpenManagedBundleWithRuntime", runtime_rendered)
        self.assertIn("case m.slots <- struct{}{}", runtime_rendered)
        self.assertIn("for m.active > 0", runtime_rendered)
        self.assertNotIn("requestMu sync.Mutex", runtime_rendered)

    def test_quality_resume_paths_must_share_one_isolated_root(self) -> None:
        base = REPOSITORY / ".tmp" / "kernel-v2-major-iteration" / "stage4-retrieval-latency" / "quality-fixture"
        paths = {
            "multisource": base / "subjects" / "v2-candidate" / ("1" * 64) / "development" / ("2" * 64) / "execution-result.json",
            "development": base / "subjects" / "v2-candidate" / ("1" * 64) / "development" / ("3" * 64) / "execution-result.json",
            "regression": base / "subjects" / "v2-candidate" / ("1" * 64) / "regression" / ("4" * 64) / "execution-result.json",
        }
        self.assertEqual(base.resolve(), finalizer._common_evidence_output_root({name: path.resolve() for name, path in paths.items()}))
        paths["regression"] = REPOSITORY / ".tmp" / "other" / "quality" / "subjects" / "v2-candidate" / ("1" * 64) / "regression" / ("4" * 64) / "execution-result.json"
        with self.assertRaisesRegex(Exception, "同一隔离执行根"):
            finalizer._common_evidence_output_root({name: path.resolve() for name, path in paths.items()})

    def test_semantic_protection_exercises_complete_corpus_fast_path_above_read_budget(self) -> None:
        materials = json.loads((HERE / "iteration" / "v2" / "stage4-retrieval-semantic-protection-materials-v3.json").read_text(encoding="utf-8"))
        self.assertEqual(2, len(materials["cases"]))
        for case in materials["cases"]:
            self.assertEqual(24, len(case["sessions"]))
            self.assertEqual(23, len(case["distractor_session_ids"]))
            self.assertEqual(1, len(case["answer_session_ids"]))
            self.assertGreater(len(case["sessions"]), 8)
            self.assertLessEqual(len(case["sessions"]), 24)
        self.assertIn("surface-distractors", materials["cases"][0]["coverage"])
        self.assertIn("distinct-operational-intent", materials["cases"][1]["coverage"])

    def test_fail_closed_material_has_zero_target_query_overlap_and_surface_competition(self) -> None:
        materials = json.loads((HERE / "iteration" / "v2" / "stage4-retrieval-semantic-failclosed-materials-v1.json").read_text(encoding="utf-8"))
        self.assertEqual(1, len(materials["cases"]))
        case = materials["cases"][0]
        self.assertEqual(24, len(case["sessions"]))
        self.assertEqual(23, len(case["distractor_session_ids"]))
        self.assertGreater(len(case["sessions"]), 8)
        query_tokens = set(case["question"].lower().replace("?", "").split())
        target = next(item for item in case["sessions"] if item["session_id"] in case["answer_session_ids"])
        target_tokens = set(target["turns"][0]["content"].lower().replace(".", "").split())
        self.assertFalse(query_tokens & target_tokens)
        matching_distractors = 0
        for item in case["sessions"]:
            if item["session_id"] in case["distractor_session_ids"]:
                words = set(item["turns"][0]["content"].lower().replace(".", "").split())
                matching_distractors += bool(query_tokens & words)
        self.assertGreaterEqual(matching_distractors, 7)

    def test_generalization_materials_freeze_two_languages_domains_and_bounded_fast_path(self) -> None:
        materials = json.loads((HERE / "iteration" / "v2" / "stage4-retrieval-semantic-generalization-materials-v1.json").read_text(encoding="utf-8"))
        self.assertEqual(2, len(materials["cases"]))
        coverage = {case["coverage"] for case in materials["cases"]}
        self.assertTrue(any("chinese" in item for item in coverage))
        self.assertTrue(any("spanish" in item for item in coverage))
        for case in materials["cases"]:
            self.assertEqual(10, len(case["sessions"]))
            self.assertGreater(len(case["sessions"]), 8)
            self.assertLessEqual(len(case["sessions"]), 24)

    def test_rejected_organized_query_is_absent_from_candidate_sources(self) -> None:
        self.assertFalse((REPOSITORY / "internal" / "kernelv2candidate" / "organized_query.go").exists())
        route = latency.load_route(HERE)
        rejected = {item["name"] for item in route["rejected_routes"]}
        self.assertIn("complete-corpus-organized-ranking-fast-path", rejected)

    def test_finalizer_rejects_mixed_transport_attribution(self) -> None:
        route = {"shared_evaluation": {"mcp_transport_sha256": "final-transport"}}
        performance_result = {
            "execution_identities": {
                "v0": {"mcp_transport_sha256": "old-transport"},
                "previous-v2": {"mcp_transport_sha256": "final-transport"},
                "candidate": {"mcp_transport_sha256": "final-transport"},
            },
            "superseded_evidence": {"eligible_for_candidate_decision": False},
            "common_transport_benefit_counted_as_kernel_improvement": False,
        }
        with self.assertRaisesRegex(Exception, "同一最终传输"):
            finalizer._validate_performance_attribution(performance_result, route)

    def test_finalizer_rejects_lexical_only_semantic_protection(self) -> None:
        materials = {"cases": [{"sessions": [{}] * 24}, {"sessions": [{}] * 24}]}
        case = {
            "selection": {
                "returned_sources": 24,
                "expected_sources": [{"channel_signals": ["lexical"]}],
            },
            "search_returned_sources": 1,
            "read_sources": 1,
            "expected_sources": 1,
        }
        observation = {
            "questions": 2,
            "final_answer_accuracy": 1.0,
            "fact_delivery": {"complete": True},
            "case_evidence": [case, case],
        }
        with self.assertRaisesRegex(Exception, "弱词法/强语义"):
            finalizer._validate_semantic_protection(observation, materials, require_fast_path=True)

    def test_finalizer_rejects_fast_path_signal_on_fail_closed_material(self) -> None:
        materials = {"cases": [{"sessions": [{}] * 24}]}
        observation = {
            "questions": 1,
            "final_answer_accuracy": 1.0,
            "fact_delivery": {"complete": True},
            "case_evidence": [{
                "selection": {
                    "returned_sources": 24,
                    "expected_sources": [{"channel_signals": ["semantic", "semantic-organized-query"], "read": True}],
                },
                "search_returned_sources": 1,
                "read_sources": 1,
                "expected_sources": 1,
            }],
        }
        with self.assertRaisesRegex(Exception, "精确语义失败关闭"):
            finalizer._validate_fail_closed_protection(observation, materials)

    def test_finalizer_requires_generalization_fast_path_for_both_cases(self) -> None:
        materials = {
            "cases": [
                {"coverage": "chinese-ceramic", "sessions": [{}] * 10},
                {"coverage": "spanish-solar", "sessions": [{}] * 10},
            ]
        }
        case = {
            "selection": {"returned_sources": 10, "expected_sources": [{"channel_signals": ["semantic"]}]},
            "search_returned_sources": 1,
            "read_sources": 1,
            "expected_sources": 1,
        }
        observation = {
            "questions": 2,
            "final_answer_accuracy": 1.0,
            "fact_delivery": {"complete": True},
            "case_evidence": [case, case],
        }
        with self.assertRaisesRegex(Exception, "通用结构排序"):
            finalizer._validate_generalization_protection(observation, materials)


if __name__ == "__main__":
    unittest.main()
