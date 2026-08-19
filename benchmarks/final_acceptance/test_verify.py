from __future__ import annotations

import hashlib
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest import mock

import verify


class FinalVerifierTests(unittest.TestCase):
    def test_dynamic_outcomes_are_recomputed_instead_of_trusting_report_flags(self) -> None:
        protocol = {
            "generation": {"task_classes": ["cross_time"]},
            "statistics": {
                "confidence_level": 0.95,
                "dynamic_task_success_wilson_lower_min": 0.6,
                "relation_precision_wilson_lower_min": 0.8,
                "relation_recall_wilson_lower_min": 0.75,
                "ablation_equivalence_margin": 0.05,
                "minimum_quality_gain": 0.1,
                "minimum_latency_or_cost_reduction": 0.15,
            },
            "budgets": {"agent_seconds_per_condition": 60},
        }
        full = {
            "cross_time": {
                "session_id": "full-session",
                "successes": 5,
                "total": 6,
                "success_rate": 5 / 6,
                "tool_calls": 2,
                "search_calls": 1,
                "elapsed_seconds": 10,
            }
        }
        baseline = {
            "cross_time": {
                "session_id": "baseline-session",
                "successes": 4,
                "total": 6,
                "success_rate": 4 / 6,
                "tool_calls": 3,
                "search_calls": 2,
                "elapsed_seconds": 20,
            }
        }
        organization = {"precision_wilson_lower": 0.81, "recall_wilson_lower": 0.76}
        with self.assertRaisesRegex(RuntimeError, "success threshold"):
            verify._require_dynamic_outcomes(
                full_runs=full,
                baseline_runs=baseline,
                organization=organization,
                protocol=protocol,
            )

    def test_dynamic_trace_binds_content_to_successful_ownward_information(self) -> None:
        with tempfile.TemporaryDirectory() as root_value:
            path = Path(root_value) / "events.jsonl"
            events = [
                {"type": "thread.started", "thread_id": "thread-1"},
                {
                    "type": "item.completed",
                    "item": {
                        "type": "mcp_tool_call",
                        "server": "ownward",
                        "tool": "ownward_read",
                        "status": "completed",
                        "result": {"structured_content": {"information": {"id": "one", "content": "grounded fact"}}},
                    },
                },
                {
                    "type": "item.completed",
                    "item": {
                        "type": "mcp_tool_call",
                        "server": "ownward",
                        "tool": "ownward_read",
                        "status": "failed",
                        "result": {"structured_content": {"information": {"id": "two", "content": "failed fact"}}},
                    },
                },
            ]
            path.write_text("\n".join(json.dumps(value) for value in events) + "\n", encoding="utf-8")
            session_id, calls, evidence = verify._dynamic_trace(path)
        self.assertEqual(session_id, "thread-1")
        self.assertEqual(calls, ["ownward_read", "ownward_read"])
        self.assertIn("grounded fact", evidence["one"])
        self.assertNotIn("two", evidence)

    def test_resource_frontier_is_rechecked_against_comparator_evidence(self) -> None:
        with tempfile.TemporaryDirectory() as root_value:
            root = Path(root_value)
            performance_path = root / "performance.json"
            performance_path.write_text("{}\n", encoding="utf-8")
            comparator_path = root / "comparator.json"
            comparator_path.write_text(
                json.dumps(
                    {
                        "schema": "ownward.resource-comparator-report/v1",
                        "passed": True,
                        "comparator": "total-agent-memory",
                        "version": "12.4.0",
                        "scale": 100000,
                        "dimensions": 384,
                        "counts": {"information": 100000, "embeddings": 100000, "relations": 99999, "fts": 100000},
                    }
                ),
                encoding="utf-8",
            )
            report_path = root / "resource.json"
            names = {
                "程序运行闭包",
                "空载常驻内存",
                "十万条 384 维常驻内存",
                "空闲 CPU",
                "十万条 384 维存储占用",
                "持久写入 P95",
                "基础可检索 P95",
                "语义检索内核 P95",
            }
            report = {
                "schema": "ownward.resource-frontier-report/v1",
                "passed": True,
                "candidate": "a" * 40,
                "release_binary_version": "a" * 40,
                "release_binary_sha256": "b" * 64,
                "performance_report_sha256": verify._sha256(performance_path),
                "comparator_report_sha256": verify._sha256(comparator_path),
                "comparator": {"name": "total-agent-memory", "version": "12.4.0"},
                "checks": [{"name": name, "passed": True} for name in names],
            }
            report_path.write_text(json.dumps(report), encoding="utf-8")
            verify._validate_resource_frontier(
                report_path,
                report,
                comparator_path=comparator_path,
                performance_path=performance_path,
                candidate="a" * 40,
                binary_sha256="b" * 64,
            )

    def test_agent_report_is_rechecked_against_tool_evidence(self) -> None:
        with tempfile.TemporaryDirectory() as root_value:
            root = Path(root_value)
            report_path = root / "agent-report.json"
            before = {"id": "I1", "revision": 1, "content": verify.AGENT_INITIAL_CONTENT}
            after = {"id": "I1", "revision": 2, "content": verify.AGENT_FINAL_CONTENT}
            mutation_calls = [
                ("ownward_rules", {"rules": "只保存长期复用的信息，不保存临时工作状态。"}),
                ("ownward_search", {"results": []}),
                ("ownward_create", {"result": {"information": before}}),
                ("ownward_search", {"results": [{"id": "I1"}]}),
                ("ownward_read", {"information": before}),
                ("ownward_update", {"result": {"information": after}}),
                ("ownward_search", {"results": [{"id": "I1"}]}),
                ("ownward_read", {"information": after}),
            ]
            independent_calls = [
                ("ownward_search", {"results": [{"id": "I1"}]}),
                ("ownward_read", {"information": after}),
            ]

            def write_trace(path: Path, session_id: str, calls: list[tuple[str, dict[str, object]]], prompt: str = "") -> None:
                events = [{
                    "type": "session",
                    "session_id": session_id,
                    "agent": "Codex/openai",
                    "model": verify.AGENT_MODEL,
                    "reasoning_effort": verify.AGENT_REASONING_EFFORT,
                    "prompt_sha256": hashlib.sha256(prompt.encode("utf-8")).hexdigest() if prompt else "",
                    "tool_call_count": len(calls),
                    "bypassed": False,
                }]
                events.extend(
                    {"type": "ownward_tool_call", "name": name, "arguments": {}, "result": value}
                    for name, value in calls
                )
                path.write_text("\n".join(json.dumps(event) for event in events) + "\n", encoding="utf-8")

            mutation_path = report_path.with_suffix(".mutation.jsonl")
            independent_path = report_path.with_suffix(".independent.jsonl")
            write_trace(mutation_path, "session-one", mutation_calls, verify.AGENT_MUTATION_PROMPT)
            write_trace(independent_path, "session-two", independent_calls)
            asset_path = report_path.with_suffix(".assets.jsonl")
            asset_path.write_text(
                "\n".join(
                    json.dumps({"operation": operation, "value": value})
                    for operation, value in (("create", before), ("update", after))
                )
                + "\n",
                encoding="utf-8",
            )
            report = {
                "model": verify.AGENT_MODEL,
                "reasoning_effort": verify.AGENT_REASONING_EFFORT,
                "asset_log_sha256": verify._sha256(asset_path),
                "mutation_session": {"id": "session-one", "trace_sha256": verify._sha256(mutation_path)},
                "independent_session": {"id": "session-two", "trace_sha256": verify._sha256(independent_path)},
                "independent_result": {
                    "stable_id": "I1",
                    "revision": 2,
                    "content": verify.AGENT_FINAL_CONTENT,
                    "required_actions": verify.AGENT_APPLIED_ACTIONS,
                },
                "information": {
                    "id": "I1",
                    "revision": 2,
                    "content_sha256": hashlib.sha256(verify.AGENT_FINAL_CONTENT.encode("utf-8")).hexdigest(),
                    "excluded_content_sha256": hashlib.sha256(verify.AGENT_EXCLUDED_CONTENT.encode("utf-8")).hexdigest(),
                    "applied_actions": verify.AGENT_APPLIED_ACTIONS,
                },
            }
            traces = verify._validate_agent_traces(report_path, report)
            report["independent_result"]["required_actions"] = ["verify backups"]
            with self.assertRaisesRegex(RuntimeError, "applied result"):
                verify._validate_agent_traces(report_path, report)
        self.assertEqual(set(traces), {"mutation_session", "independent_session", "asset_log"})

    def test_validates_active_official_submission_and_candidate_evidence(self) -> None:
        with tempfile.TemporaryDirectory() as root_value:
            root = Path(root_value)
            adapter = root / "adapter"
            adapter.mkdir()
            for name in verify.LONGMEM_ADAPTER_FILES:
                (adapter / name).write_text(name, encoding="utf-8")
            digests = verify._adapter_digests(adapter)
            point = root / "operating_points" / "active"
            for domain in ("web", "enterprise"):
                directory = point / domain
                directory.mkdir(parents=True)
                (directory / "run_args.json").write_text(
                    json.dumps(
                        {
                            "ownward_evidence": {
                                "candidate": "a" * 40,
                                "release_binary_sha256": "b" * 64,
                                "query_mode": "codex",
                                "codex_model": verify.LONGMEM_CODEX_MODEL,
                                "codex_reasoning_effort": verify.LONGMEM_CODEX_REASONING_EFFORT,
                                "codex_cli_version": verify.LONGMEM_CODEX_CLI_VERSION,
                                "codex_binary_sha256": "e" * 64,
                                "official_revision": verify.OFFICIAL_REVISION,
                                "adapter_sha256": digests,
                            }
                        }
                    ),
                    encoding="utf-8",
                )
            (point / "metric_overview.json").write_text("{}", encoding="utf-8")
            overview = root / "submission_overview.json"
            overview.write_text(
                json.dumps(
                    {
                        "method": "ownward",
                        "tier": "small",
                        "lafs": {"lafs_gain": 0},
                        "operating_points": [
                            {
                                "name": "active",
                                "metric_overview_file": "operating_points/active/metric_overview.json",
                                "overall_full_set": 0.8,
                                "memory_query_avg_seconds": 100,
                            }
                        ],
                    }
                ),
                encoding="utf-8",
            )
            with mock.patch.object(
                verify,
                "_validate_official_package",
                return_value={"archive_path": root / "submission.tar.gz", "archive_sha256": "c" * 64, "package_tree_sha256": "d" * 64},
            ):
                metrics, package = verify._validate_longmemeval(
                    overview,
                    candidate="a" * 40,
                    binary_sha256="b" * 64,
                    accuracy_min=0.7561,
                    latency_max=130.54,
                    adapter_dir=adapter,
                    official_repo=root,
                    official_python=Path("python"),
                )
        self.assertEqual(metrics["accuracy"], 0.8)
        self.assertEqual(metrics["lafs_gain"], 0)
        self.assertEqual(package["package_tree_sha256"], "d" * 64)

    def test_dynamic_reports_are_bound_to_frozen_protocol_and_complete_evidence(self) -> None:
        with tempfile.TemporaryDirectory() as root_value:
            root = Path(root_value)
            protocol_dir = root / "benchmarks" / "acceptance" / "dynamic"
            protocol_dir.mkdir(parents=True)
            task_classes = {"cross_time", "multi_hop", "context_applicability", "information_update"}
            statistics = {
                "dynamic_task_success_wilson_lower_min": 0.6,
                "relation_precision_wilson_lower_min": 0.8,
                "relation_recall_wilson_lower_min": 0.75,
                "ablation_equivalence_margin": 0.05,
                "minimum_quality_gain": 0.1,
                "minimum_latency_or_cost_reduction": 0.15,
            }
            models = {
                "generator": {"model": "generator"},
                "validator": {"model": "validator"},
                "external_agent": {"model": "agent"},
            }
            budgets = {"agent_tool_calls_per_query": 8}
            protocol_path = protocol_dir / "protocol.json"
            protocol_path.write_text(
                json.dumps(
                    {
                        "generation": {"task_classes": sorted(task_classes), "generated_scenarios": 0},
                        "runtime": {"codex_cli_version": "codex-cli 0.117.0"},
                        "statistics": statistics,
                        "models": models,
                        "budgets": budgets,
                    }
                ),
                encoding="utf-8",
            )
            evidence: dict[str, dict[str, str]] = {}
            required = {
                "random",
                "hidden",
                "expression",
                "validation",
                "dataset",
                "full_mapping",
                "baseline_mapping",
                "asset_integrity",
                "organization",
                "codex_binary",
            }
            for condition in ("full", "baseline"):
                for task_class in task_classes:
                    required.update(
                        {
                            f"{condition}_{task_class}_answers",
                            f"{condition}_{task_class}_events",
                            f"{condition}_{task_class}_run",
                        }
                    )
            evidence_paths: dict[str, Path] = {}
            for name in required:
                path = root / f"{name}.json"
                value = {"method": "secrets.token_hex(32)", "seed": "c" * 64} if name == "random" else {}
                path.write_text(json.dumps(value) + "\n", encoding="utf-8")
                evidence_paths[name] = path
                evidence[name] = {"path": str(path), "sha256": verify._sha256(path)}
            dynamic_checks = {
                "post-freeze-random-generation",
                "independent-expression-validation",
                "required-scope-and-task-coverage",
                "asset-identity-content-integrity",
                "semantic-relation-quality",
                "external-agent-no-bypass-and-budget",
                *(f"dynamic-{value}" for value in task_classes),
            }
            dynamic = {
                "schema": "ownward.dynamic-acceptance-report/v1",
                "passed": True,
                "candidate": "a" * 40,
                "release_binary_version": "a" * 40,
                "release_binary_sha256": "b" * 64,
                "protocol_sha256": verify._sha256(protocol_path),
                "dataset_sha256": verify._sha256(evidence_paths["dataset"]),
                "random_source_sha256": verify._sha256(evidence_paths["random"]),
                "hidden_truth_sha256": verify._sha256(evidence_paths["hidden"]),
                "statistics": statistics,
                "budgets": budgets,
                "generator": models["generator"],
                "validator": models["validator"],
                "external_agent": models["external_agent"],
                "codex_cli": {"version": "codex-cli 0.117.0", "sha256": evidence["codex_binary"]["sha256"]},
                "generated_scenarios": 0,
                "valid_scenarios": 0,
                "rejected_scenarios": 0,
                "tasks": {value: {"wilson_lower": 0.61} for value in task_classes},
                "organization": {"precision_wilson_lower": 0.81, "recall_wilson_lower": 0.76},
                "evidence": evidence,
                "checks": [{"name": name, "passed": True} for name in dynamic_checks],
            }
            dynamic_path = root / "dynamic-report.json"
            dynamic_path.write_text(json.dumps(dynamic), encoding="utf-8")
            ablation = {
                "schema": "ownward.organization-ablation-report/v1",
                "passed": True,
                "candidate": "a" * 40,
                "release_binary_version": "a" * 40,
                "release_binary_sha256": "b" * 64,
                "protocol_sha256": verify._sha256(protocol_path),
                "dataset_sha256": verify._sha256(evidence_paths["dataset"]),
                "dynamic_report_sha256": verify._sha256(dynamic_path),
                "only_difference": ["relation organization state", "relation signals", "relation navigation"],
                "full_disable_relations": False,
                "baseline_disable_relations": True,
                "task_classes": {
                    value: {"equivalent": True, "meaningful_advantage": value == "multi_hop"}
                    for value in task_classes
                },
                "full_total_cost": {
                    "ingestion_operations": 1,
                    "semantic_model_calls": 1,
                    "agent_tool_calls": 1,
                    "organization_seconds": 1,
                    "agent_seconds": 1,
                },
                "baseline_total_cost": {
                    "ingestion_operations": 1,
                    "semantic_model_calls": 1,
                    "agent_tool_calls": 1,
                    "organization_seconds": 1,
                    "agent_seconds": 1,
                },
                "statistics": {
                    "equivalence_margin": statistics["ablation_equivalence_margin"],
                    "minimum_quality_gain": statistics["minimum_quality_gain"],
                    "minimum_latency_or_cost_reduction": statistics["minimum_latency_or_cost_reduction"],
                },
                "checks": [
                    {"name": name, "passed": True}
                    for name in {
                        "same-candidate-binary-and-dataset",
                        "only-relation-organization-disabled",
                        "per-class-no-regression-beyond-equivalence",
                        "meaningful-organization-advantage",
                        "complete-quality-latency-and-cost-accounting",
                    }
                ],
            }
            ablation_path = root / "ablation-report.json"
            ablation_path.write_text(json.dumps(ablation), encoding="utf-8")
            dynamic["statistics"] = {**statistics, "dynamic_task_success_wilson_lower_min": 0.5}
            with mock.patch.object(verify._dynamic_common, "validate_protocol"), self.assertRaisesRegex(
                RuntimeError, "frozen statistics"
            ):
                verify._validate_dynamic_reports(
                    dynamic_path,
                    dynamic,
                    ablation_path,
                    ablation,
                    candidate="a" * 40,
                    binary_sha256="b" * 64,
                    repository=root,
                )

    def test_archive_must_exactly_match_package_tree(self) -> None:
        with tempfile.TemporaryDirectory() as root_value:
            root = Path(root_value)
            package = root / "submission"
            package.mkdir()
            (package / "submission_overview.json").write_text("{}\n", encoding="utf-8")
            nested = package / "operating_points" / "active"
            nested.mkdir(parents=True)
            (nested / "metric_overview.json").write_text('{"overall_full_set":0.8}\n', encoding="utf-8")
            archive = root / "submission.tar.gz"
            with tarfile.open(archive, "w:gz") as output:
                output.add(package, arcname=package.name)
            expected = verify._manifest_sha256(verify._package_manifest(package))
            digest = verify._verify_package_archive(package, archive)
        self.assertEqual(digest, expected)


if __name__ == "__main__":
    unittest.main()
