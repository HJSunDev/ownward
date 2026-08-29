from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import kernel_iteration_evidence as evidence
import kernel_iteration_stage4_latency_candidate_data as candidate_data
import kernel_iteration_stage4_latency_data as latency_data
import kernel_iteration_stage4_latency_performance as paired
import kernel_iteration_validation as validation


ROUTE_SCHEMA = "ownward.kernel-iteration-stage4-retrieval-latency-route/v4"
RESULT_SCHEMA = "ownward.kernel-iteration-stage4-retrieval-latency-candidate-performance/v2"
SUPERSEDED_RESULT_SCHEMA = "ownward.kernel-iteration-stage4-retrieval-latency-candidate-performance/v1"


def run(
    suite_root: Path,
    output_root: Path,
    candidate_subject_manifest: Path,
    candidate_execution_config: Path,
    previous_subject_manifest: Path,
    previous_execution_config: Path,
    v0_binary: Path,
    v0_embedding: Path,
    baseline_preparation_receipt: Path,
    candidate_preparation_receipt: Path,
    superseded_performance_result: Path,
    formal_state: Path,
) -> dict[str, Any]:
    suite_root, output_root = suite_root.resolve(), output_root.resolve()
    route = load_route(suite_root)
    _require(
        route["root"]["status"] != "comparison-contract-corrected-complete-consumer-tail-margin-open",
        "旧 41.2/78 ms 候选性能入口已降为诊断；新时延决定必须绑定完整消费者检索政策修订",
    )
    comparison = evidence.load_contract(suite_root)
    raw_candidate = _load_json(candidate_subject_manifest.resolve())
    candidate = evidence.validate_v2_subject(comparison, raw_candidate)
    raw_previous = _load_json(previous_subject_manifest.resolve())
    previous = evidence.validate_v2_subject(comparison, raw_previous)
    _require(previous["identity"] == route["subjects"]["previous-v2"], "同尺性能前序 V2 subject 错绑")

    candidate_runtime = validation.validate_execution_config(suite_root, candidate_execution_config.resolve())
    previous_runtime = validation.validate_execution_config(suite_root, previous_execution_config.resolve())
    v0_binary, v0_embedding = v0_binary.resolve(), v0_embedding.resolve()
    _verify_subject_runtime(raw_candidate, candidate_runtime, "最终候选")
    _verify_subject_runtime(raw_previous, previous_runtime, "前序 V2")
    _require(evidence.file_sha256(v0_binary) == route["subjects"]["v0_binary_sha256"], "同尺性能 V0 二进制漂移")
    _require(candidate_runtime["protocol_value"] == previous_runtime["protocol_value"], "候选与前序 V2 检索协议语义不同尺")
    manifest_sha256 = evidence.file_sha256(v0_embedding / "manifest.json")
    _require(manifest_sha256 == evidence.file_sha256(previous_runtime["embedding"] / "manifest.json") == evidence.file_sha256(candidate_runtime["embedding"] / "manifest.json"), "三代向量清单不同尺")

    transport = suite_root.parents[1] / "support" / "ownward_mcp.py"
    transport_sha256 = evidence.file_sha256(transport)
    _require(transport_sha256 == route["shared_evaluation"]["mcp_transport_sha256"], "最终共享 MCP 传输漂移")
    timing_controller_sha256 = evidence.file_sha256(Path(paired.__file__).resolve())
    _require(timing_controller_sha256 == route["shared_evaluation"]["stage_timing_controller_sha256"], "同尺逐阶段计时控制器漂移")

    baseline = _load_json(baseline_preparation_receipt.resolve())
    _validate_identity(baseline, latency_data.RECEIPT_SCHEMA, "检索时延基线 prepared-data 收据")
    _require(baseline["identity"] == route["shared_evaluation"]["baseline_preparation_identity"], "同尺性能基线准备错绑")
    candidate_receipt = _load_json(candidate_preparation_receipt.resolve())
    _validate_identity(candidate_receipt, candidate_data.RECEIPT_SCHEMA, "检索时延候选 prepared-data 收据")
    _require(candidate_receipt["subject_identity"] == candidate["identity"], "同尺性能候选 prepared data 错绑")
    _require(candidate_receipt["baseline_preparation_identity"] == baseline["identity"], "同尺性能候选准备未绑定共同基线")

    materials = latency_data.load_materials(suite_root)
    _require(materials["identity"] == route["shared_evaluation"]["materials_identity"], "同尺性能材料漂移")
    cases = {str(item["case_id"]): str(item["query"]) for item in materials["cases"]}
    roots = {
        "v0": Path(baseline["subject_roots"]["v0"]).resolve(),
        "previous-v2": Path(baseline["subject_roots"]["current-v2"]).resolve(),
        "candidate": Path(candidate_receipt["candidate_root"]).resolve(),
    }
    expected_data = {
        "v0": baseline["prepared_data_sha256"]["v0"],
        "previous-v2": baseline["prepared_data_sha256"]["current-v2"],
        "candidate": candidate_receipt["prepared_data_sha256"],
    }
    before = {name: paired._data_identities(root, cases, expected_data[name]) for name, root in roots.items()}

    state_path = formal_state.resolve()
    state_before = evidence.file_sha256(state_path)
    _require(state_before == route["formal_state_sha256"], "同尺性能前正式 state 漂移")
    runtimes = {
        "v0": {"binary": v0_binary, "embedding": v0_embedding, "protocol_value": previous_runtime["protocol_value"]},
        "previous-v2": previous_runtime,
        "candidate": candidate_runtime,
    }
    samples: dict[str, list[dict[str, Any]]] = {name: [] for name in runtimes}
    for order in route["schedule"]["balanced_order"]:
        _require(sorted(order) == ["candidate", "previous-v2", "v0"], "三代同尺平衡顺序无效")
        for name in order:
            samples[name].extend(paired._run_round(name, runtimes[name], roots[name], cases, route["schedule"]))

    state_after = evidence.file_sha256(state_path)
    _require(state_after == state_before, "三代同尺性能改写了正式 state")
    after = {name: paired._data_identities(root, cases, expected_data[name]) for name, root in roots.items()}
    _require(before == after, "三代同尺性能改写了 prepared data")
    summaries = {name: paired._summarize(values, len(cases)) for name, values in samples.items()}
    repeat_error = float(route["gates"]["paired_repeat_error_ms"])
    candidate_metrics, previous_metrics, v0_metrics = summaries["candidate"], summaries["previous-v2"], summaries["v0"]
    p95_improvement = float(previous_metrics["p95_ms"]) - float(candidate_metrics["p95_ms"])
    mean_improvement = float(previous_metrics["mean_ms"]) - float(candidate_metrics["mean_ms"])
    gates = route["gates"]
    checks = {
        "absolute_mean": float(candidate_metrics["mean_ms"]) <= float(gates["retrieval_mean_ms_maximum"]),
        "absolute_p95": float(candidate_metrics["p95_ms"]) <= float(gates["retrieval_p95_ms_maximum"]),
        "same_final_transport_v0_mean": float(candidate_metrics["mean_ms"]) <= float(v0_metrics["mean_ms"]) + repeat_error,
        "same_final_transport_v0_p95": float(candidate_metrics["p95_ms"]) <= float(v0_metrics["p95_ms"]) + repeat_error,
        "material_previous_v2_mean_improvement": mean_improvement > repeat_error,
        "material_previous_v2_p95_improvement": p95_improvement > repeat_error,
        "read_limit": int(candidate_metrics["read_units_max"]) <= int(gates["read_units_maximum"]),
        "evidence_depth": int(candidate_metrics["evidence_probes_max"]) == int(gates["evidence_probes_required"]),
        "context_limit": int(candidate_metrics["context_chars_max"]) <= int(gates["context_chars_maximum"]),
        "stable_traces": all(summary["stable_case_traces"] is True for summary in summaries.values()),
    }

    shared_execution = {
        "materials_identity": materials["identity"],
        "protocol_sha256": evidence.file_sha256(previous_runtime["protocol"]),
        "embedding_manifest_sha256": manifest_sha256,
        "mcp_transport_sha256": transport_sha256,
        "performance_controller_sha256": evidence.file_sha256(Path(__file__).resolve()),
        "stage_timing_controller_sha256": timing_controller_sha256,
        "preparation_controller_sha256": baseline["preparer_sha256"],
        "schedule": route["schedule"],
    }
    shared_execution_identity = evidence.canonical_sha256(shared_execution)
    subject_identities = {"v0": route["subjects"]["v0"], "previous-v2": previous["identity"], "candidate": candidate["identity"]}
    execution_identities = {
        name: _execution_identity(name, subject_identities[name], runtimes[name], before[name], shared_execution_identity, transport_sha256)
        for name in ("v0", "previous-v2", "candidate")
    }
    _require(all(item["mcp_transport_sha256"] == transport_sha256 for item in execution_identities.values()), "三代没有封存同一最终传输")

    superseded = _load_json(superseded_performance_result.resolve())
    _validate_identity(superseded, SUPERSEDED_RESULT_SCHEMA, "旧候选性能诊断")
    _require(superseded["identity"] == route["superseded_evidence"]["identity"], "旧候选性能诊断身份漂移")
    content = {
        "schema": RESULT_SCHEMA,
        "formal": False,
        "formal_state_written": False,
        "model_calls": 0,
        "answer_generation_calls": 0,
        "product_mutations": 0,
        "route_identity": route["identity"],
        "subjects": subject_identities,
        "kernel_generation_identity": raw_candidate["kernel_generation_identity"],
        "kernel_effect_identity": raw_candidate["kernel_effect_identity"],
        "shared_execution_conditions": shared_execution,
        "shared_execution_identity": shared_execution_identity,
        "execution_identities": execution_identities,
        "metrics": summaries,
        "candidate_minus_previous_v2_mean_ms": -mean_improvement,
        "candidate_minus_previous_v2_p95_ms": -p95_improvement,
        "common_transport_benefit_counted_as_kernel_improvement": False,
        "superseded_evidence": {
            "identity": superseded["identity"],
            "eligible_for_candidate_decision": False,
            "reason": "baselines-were-measured-before-the-final-mcp-transport-identity",
        },
        "checks": checks,
        "passed": all(checks.values()),
        "prepared_data_sha256_before": before,
        "prepared_data_sha256_after": after,
        "formal_state_sha256_before": state_before,
        "formal_state_sha256_after": state_after,
    }
    value = {**content, "identity": evidence.canonical_sha256(content)}
    destination = output_root / "candidate-performance.json"
    _require(not destination.exists(), "纠正后三代同尺性能证据已经存在；禁止随机重跑")
    evidence.atomic_json(destination, value)
    return {**value, "path": str(destination)}


def load_route(suite_root: Path) -> dict[str, Any]:
    path = suite_root.resolve() / "iteration" / "v2" / "stage4-retrieval-latency-route.json"
    value = _load_json(path)
    _validate_identity(value, ROUTE_SCHEMA, "检索时延候选路线")
    _require(value.get("frozen_before_candidate_measurement") is True, "检索时延候选路线没有在候选结果前冻结")
    _require(value.get("candidate_result_seen") is False, "检索时延候选路线接触了候选结果")
    _require(evidence.file_sha256(Path(__file__).resolve()) == value.get("controller_sha256"), "检索时延候选控制器漂移")
    if value.get("root", {}).get("status") == "comparison-contract-corrected-complete-consumer-tail-margin-open":
        gates = value.get("gates", {})
        _require("retrieval_mean_ms_maximum" not in gates and "retrieval_p95_ms_maximum" not in gates, "非同尺 V0 community 时延门仍在活动路线")
        _require(gates.get("complete_consumer_retrieval_p95_absolute_maximum_ms") == 600.0, "完整消费者检索绝对门漂移")
        _require(gates.get("complete_consumer_retrieval_p95_decision_maximum_ms") == 553.0, "完整消费者检索余量门漂移")
    return value


def _verify_subject_runtime(raw_subject: dict[str, Any], runtime: dict[str, Any], name: str) -> None:
    artifacts = raw_subject.get("artifacts")
    _require(isinstance(artifacts, dict), f"{name}缺少制品身份")
    _require(artifacts.get("binary") == evidence.file_sha256(runtime["binary"]), f"{name} subject 与二进制错绑")


def _execution_identity(name: str, subject_identity: str, runtime: dict[str, Any], prepared: dict[str, str], shared_identity: str, transport_sha256: str) -> dict[str, Any]:
    content = {
        "subject": name,
        "subject_identity": subject_identity,
        "binary_sha256": evidence.file_sha256(runtime["binary"]),
        "embedding_manifest_sha256": evidence.file_sha256(runtime["embedding"] / "manifest.json"),
        "protocol_sha256": evidence.canonical_sha256(runtime["protocol_value"]),
        "prepared_data_sha256": prepared,
        "shared_execution_identity": shared_identity,
        "mcp_transport_sha256": transport_sha256,
    }
    return {**content, "identity": evidence.canonical_sha256(content)}


def _validate_identity(value: dict[str, Any], schema: str, name: str) -> None:
    _require(value.get("schema") == schema, f"{name} schema 无效")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), f"{name}身份漂移")


def _load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise validation.KernelIterationValidationError(f"无法读取检索时延制品 {path}: {error}") from error
    _require(isinstance(value, dict), f"检索时延制品不是对象: {path}")
    return value


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise validation.KernelIterationValidationError(message)
