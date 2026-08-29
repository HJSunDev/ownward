from __future__ import annotations

import json
import os
import subprocess
from pathlib import Path
from typing import Any

import kernel_iteration_evidence as evidence


AUDIT_CONTRACT = Path("benchmarks/acceptance/suite/iteration/v2/stage4-retrieval-latency-comparability-audit-contract.json")
MIGRATION_RECEIPT = Path("benchmarks/acceptance/suite/iteration/v2/retrieval-latency-comparability-migration.json")
AUDIT_SCHEMA = "ownward.kernel-iteration-stage4-retrieval-latency-comparability-audit-contract/v1"
MIGRATION_SCHEMA = "ownward.kernel-iteration-retrieval-latency-comparability-migration/v1"
RESULT_SCHEMA = "ownward.kernel-iteration-retrieval-latency-comparability-result/v1"


class ComparabilityError(ValueError):
    pass


def load_audit_contract(repository: Path) -> dict[str, Any]:
    value = _load_json(repository / AUDIT_CONTRACT)
    _require(value.get("schema") == AUDIT_SCHEMA, "检索时延同尺审计合同 schema 无效")
    _require(value.get("frozen_before_new_candidate_measurement") is True, "同尺审计合同未在新候选测量前冻结")
    _require(value.get("candidate_measurements_are_not_authority") is True, "候选结果不得成为同尺审计权威")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), "同尺审计合同身份漂移")
    criteria = value.get("criteria")
    _require(isinstance(criteria, dict) and set(criteria) == {"request_boundary", "included_work", "delivery_contract", "load_shape", "statistics", "ownership"}, "同尺审计判据不完整")
    authorities = _mapping(value, "authorities")
    for name, authority in authorities.items():
        _require(isinstance(authority, dict), f"同尺审计权威 {name} 无效")
        path = authority.get("path")
        digest = authority.get("sha256")
        if isinstance(path, str) and isinstance(digest, str):
            source = (repository / path).resolve()
            _require(source.is_relative_to(repository.resolve()) and source.is_file(), f"同尺审计权威 {name} 缺失")
            _require(evidence.file_sha256(source) == digest, f"同尺审计权威 {name} 摘要漂移")
    _require(float(_mapping(authorities, "consumer_retrieval_gate")["maximum_ms"]) == 600.0, "完整消费者检索门槛漂移")
    return value


def load_migration_receipt(repository: Path, audit: dict[str, Any]) -> dict[str, Any]:
    value = _load_json(repository / MIGRATION_RECEIPT)
    _require(value.get("schema") == MIGRATION_SCHEMA, "检索时延合同迁移收据 schema 无效")
    _require(value.get("audit_contract_identity") == audit["identity"], "合同迁移收据与同尺审计错绑")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), "合同迁移收据身份漂移")
    decision = _mapping(value, "decision")
    _require(decision.get("same_scale") is False and decision.get("candidate_results_used_to_select_replacement_gate") is False, "同尺裁决或结果隔离无效")
    matrix = _mapping(value, "audit_matrix")
    _require(any(item.get("equal") is False for item in matrix.values() if isinstance(item, dict)), "同尺裁决缺少机械差异")
    replacement = _mapping(value, "replacement_latency_policy")
    _require(float(replacement.get("absolute_maximum_ms", -1)) == 600.0, "迁移后的完整消费者绝对门漂移")
    _require(float(replacement.get("decision_maximum_ms", -1)) + float(replacement.get("frozen_repeatability_error_ms", -1)) == 600.0, "完整消费者门槛没有保留冻结重复误差")
    _require(replacement.get("mean_absolute_gate") is None, "无同尺公开依据时不得发明平均时延绝对门")
    return value


def profiles_same_scale(matrix: dict[str, Any]) -> bool:
    return bool(matrix) and all(isinstance(item, dict) and item.get("equal") is True for item in matrix.values())


def evaluate_existing_measurement(result: dict[str, Any], audit: dict[str, Any], migration: dict[str, Any]) -> dict[str, Any]:
    policy = _mapping(audit, "post_audit_measurement_policy")
    _require(result.get("identity") == policy["eligible_existing_result_identity"], "既有真实规模结果身份错绑")
    _require(result.get("formal") is False and result.get("formal_state_written") is False, "既有真实规模结果越权为正式证据")
    schedule = _mapping(result, "schedule")
    _require(int(schedule.get("workers", 0)) == int(policy["workers"]), "真实规模结果 worker 形态不等价")
    _require(int(schedule.get("warmups_per_round", -1)) == int(policy["warmups_per_round"]), "真实规模结果预热漂移")
    _require(int(schedule.get("measured_repetitions_per_round", -1)) == int(policy["measured_repetitions_per_round"]), "真实规模结果重复次数漂移")
    _require(len(schedule.get("balanced_order", [])) == int(policy["balanced_rounds"]), "真实规模结果平衡顺序不完整")
    metrics = _mapping(result, "metrics")
    candidate = _mapping(metrics, "candidate")
    _require(candidate.get("asset_count_range") == [min(policy["required_asset_counts"]), max(policy["required_asset_counts"])], "真实规模资产范围漂移")
    _require(candidate.get("target_delivery_complete") is True and metrics.get("quality_complete") is True, "真实规模结果没有完成质量交付")
    _require(metrics.get("resource_bounds_complete") is True, "真实规模结果资源边界不完整")
    replacement = _mapping(migration, "replacement_latency_policy")
    measured_p95 = float(candidate["p95_ms"])
    repeat_error = float(replacement["frozen_repeatability_error_ms"])
    absolute = float(replacement["absolute_maximum_ms"])
    return {
        "result_identity": result["identity"],
        "complete_consumer_retrieval_p95_ms": measured_p95,
        "frozen_repeatability_error_ms": repeat_error,
        "p95_with_repeatability_margin_ms": measured_p95 + repeat_error,
        "absolute_maximum_ms": absolute,
        "latency_gate_passed": measured_p95 + repeat_error <= absolute,
        "quality_complete": True,
        "resource_bounds_complete": True,
        "additional_measurement_required_for_this_decision": False,
    }


def run(repository: Path, output: Path, formal_state: Path, v0_formal_run: Path, real_scale_result: Path) -> dict[str, Any]:
    repository = repository.resolve()
    output = output.resolve()
    formal_state = formal_state.resolve()
    v0_formal_run = v0_formal_run.resolve()
    real_scale_result = real_scale_result.resolve()
    audit = load_audit_contract(repository)
    migration = load_migration_receipt(repository, audit)
    comparison = evidence.load_contract(repository / "benchmarks" / "acceptance" / "suite")
    _require(comparison.get("policy_revision_identity") == "b5400416de89df4cc821f9827de72b20a25075f4d6e8b5e70e920743d43b198b", "同尺修正后的比较政策身份漂移")
    _require(_mapping(comparison, "latency_policy_migration").get("receipt_identity") == migration["identity"], "比较政策未绑定同尺迁移收据")
    state_before = evidence.file_sha256(formal_state)

    report_path = v0_formal_run / "report.json"
    diagnostics_path = v0_formal_run / "diagnostic-summary.json"
    historical = _mapping(audit, "historical_v0_evidence")
    _require(evidence.file_sha256(report_path) == historical["report_sha256"], "V0 community 聚合报告摘要漂移")
    _require(evidence.file_sha256(diagnostics_path) == historical["diagnostic_summary_sha256"], "V0 community 诊断摘要漂移")
    report = _load_json(report_path)
    diagnostics = _load_json(diagnostics_path)
    retrieval = _mapping(report, "retrieval")
    gaps = _mapping(diagnostics, "by_first_observed_gap")
    _require(report.get("questions") == historical["questions"] == 500, "V0 community 题量漂移")
    _require(abs(float(retrieval["mean_ms"]) - float(historical["retrieval_mean_ms"])) < 1e-9, "V0 community 检索 mean 漂移")
    _require(abs(float(retrieval["p95_ms"]) - float(historical["retrieval_p95_ms"])) < 1e-9, "V0 community 检索 p95 漂移")
    for name in ("target_evidence_not_search_returned", "target_evidence_not_read", "evidence_read_answer_incorrect"):
        _require(gaps.get(name) == historical[name], f"V0 community {name} 聚合漂移")

    old_adapter = _mapping(_mapping(audit, "authorities"), "v0_retrieval_adapter")
    actual_blob = subprocess.check_output(
        ["git", "rev-parse", f"{old_adapter['git_commit']}:{old_adapter['path']}"],
        cwd=repository,
        text=True,
        encoding="utf-8",
    ).strip()
    _require(actual_blob == old_adapter["git_blob"], "V0 检索适配器 Git 审计字节漂移")
    old_source = subprocess.check_output(
        ["git", "show", f"{old_adapter['git_commit']}:{old_adapter['path']}"],
        cwd=repository,
        text=True,
        encoding="utf-8",
    )
    old_retrieve = _function_source(old_source, "retrieve")
    _require("ownward_search" in old_retrieve and "ownward_read" in old_retrieve, "V0 检索入口还原失败")
    _require("ownward_evidence_search" not in old_retrieve and "ownward_evidence_read" not in old_retrieve, "V0 检索职责还原不符合封存实现")
    _require("if evidence and used_chars + len(content) > settings[\"context_max_chars\"]:\n            break" in old_retrieve, "V0 贪心预算终止语义还原失败")

    current_source = (repository / _mapping(_mapping(audit, "authorities"), "current_retrieval_adapter")["path"]).read_text(encoding="utf-8")
    current_retrieve = _function_source(current_source, "retrieve")
    for token in ("ownward_evidence_search", "ownward_evidence_read", "selection_steps", "context_budget"):
        _require(token in current_retrieve, f"V2 完整消费者职责缺失: {token}")

    _require(profiles_same_scale(_mapping(migration, "audit_matrix")) is False, "V0/V2 被错误裁决为同尺")
    _require(evidence.file_sha256(real_scale_result) == _mapping(audit, "post_audit_measurement_policy")["eligible_existing_result_sha256"], "既有真实规模结果摘要漂移")
    measurement = evaluate_existing_measurement(_load_json(real_scale_result), audit, migration)
    state_after = evidence.file_sha256(formal_state)
    _require(state_before == state_after, "同尺审计修改了正式 state")
    result_content = {
        "schema": RESULT_SCHEMA,
        "audit_contract_identity": audit["identity"],
        "migration_receipt_identity": migration["identity"],
        "comparison_policy_revision_identity": comparison["policy_revision_identity"],
        "evidence_compatibility_identity": comparison["identity"],
        "audit_controller_sha256": evidence.file_sha256(Path(__file__).resolve()),
        "entrypoint_sha256": evidence.file_sha256(Path(__file__).with_name("kernel_iteration_run.py")),
        "formal": False,
        "formal_state_written": False,
        "formal_state_sha256_before": state_before,
        "formal_state_sha256_after": state_after,
        "v0": {
            "report_sha256": historical["report_sha256"],
            "diagnostic_summary_sha256": historical["diagnostic_summary_sha256"],
            "retrieval_mean_ms": historical["retrieval_mean_ms"],
            "retrieval_p95_ms": historical["retrieval_p95_ms"],
            "target_delivery_failures": historical["target_evidence_not_search_returned"] + historical["target_evidence_not_read"],
            "latency_status": "historical-diagnostic-not-complete-consumer-gate",
        },
        "same_scale": False,
        "audit_matrix": _mapping(migration, "audit_matrix"),
        "replacement_latency_policy": _mapping(migration, "replacement_latency_policy"),
        "existing_measurement": measurement,
        "retrieval_latency_closed": measurement["latency_gate_passed"],
        "next_validation": None if measurement["latency_gate_passed"] else "reduce-or-mechanically-bound-complete-consumer-p95-tail-under-600ms-with-frozen-repeatability-margin",
        "evidence_disposition": _mapping(migration, "evidence_disposition"),
        "model_calls": 0,
        "product_executions": 0,
        "new_measurements": 0,
    }
    result = {**result_content, "identity": evidence.canonical_sha256(result_content)}
    _atomic_json(output, result)
    return result


def _function_source(source: str, name: str) -> str:
    marker = f"def {name}("
    start = source.find(marker)
    _require(start >= 0, f"缺少函数: {name}")
    next_function = source.find("\ndef ", start + len(marker))
    return source[start:] if next_function < 0 else source[start:next_function]


def _atomic_json(path: Path, value: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(f".{path.name}.{os.getpid()}.tmp")
    try:
        with temporary.open("xb") as stream:
            stream.write((json.dumps(value, ensure_ascii=False, sort_keys=True, indent=2) + "\n").encode("utf-8"))
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
    finally:
        temporary.unlink(missing_ok=True)


def _load_json(path: Path) -> dict[str, Any]:
    value = json.loads(path.read_text(encoding="utf-8"))
    _require(isinstance(value, dict), f"{path} 必须是 JSON 对象")
    return value


def _mapping(value: dict[str, Any], name: str) -> dict[str, Any]:
    item = value.get(name)
    _require(isinstance(item, dict), f"{name} 必须是对象")
    return item


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise ComparabilityError(message)
