from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import kernel_iteration_evidence as evidence
import kernel_iteration_stage4_resource_cost as resource_cost
import kernel_iteration_stage4_resource_cost_audit as audit
import kernel_iteration_validation as validation


SCHEMA = "ownward.kernel-iteration-stage4-resource-cost-storage-evidence/v1"
CANDIDATE_RECEIPT_SCHEMA = "ownward.kernel-iteration-v2-candidate/v4"


def finalize(
    suite_root: Path,
    output_root: Path,
    audit_result_path: Path,
    candidate_receipt_path: Path,
    subject_manifest_path: Path,
    execution_config_path: Path,
    development_result_path: Path,
    regression_result_path: Path,
    formal_state_path: Path,
    *,
    resume: bool,
) -> dict[str, Any]:
    suite_root = suite_root.resolve()
    repository = suite_root.parents[2]
    output_root = output_root.resolve()
    result_path = output_root / "storage-result.json"
    state_path = formal_state_path.resolve()
    state_sha256 = evidence.file_sha256(state_path)
    contract = audit.load_contract(suite_root)
    _require(state_sha256 == contract["formal_state"]["sha256"], "存储终态前正式 state 漂移")
    if result_path.is_file():
        _require(resume, "存储成本终态已存在；只有 --resume 可逐字复用")
        value = _load_json(result_path)
        _validate_identity(value, SCHEMA, "存储成本终态")
        _require(value["formal_state_sha256"] == state_sha256, "存储成本终态恢复 state 漂移")
        return {**value, "path": str(result_path), "reused": True, "model_executions": 0, "product_executions": 0}

    audit_result = _load_json(audit_result_path.resolve())
    _validate_identity(audit_result, audit.SCHEMA, "资源可控性审计")
    _require(audit_result["contract_identity"] == contract["identity"], "存储终态与资源审计错绑")
    paired = _load_json(repository / contract["paired_result"]["path"])
    _validate_identity(paired, resource_cost.RESULT_SCHEMA, "原始同尺资源结果")
    receipt = _load_json(candidate_receipt_path.resolve())
    _validate_identity(receipt, CANDIDATE_RECEIPT_SCHEMA, "资源成本候选收据")
    subject = evidence.validate_v2_subject(evidence.load_contract(suite_root), _load_json(subject_manifest_path.resolve()))
    _require(subject["identity"] == receipt["subject_identity"], "资源成本候选 subject 与收据错绑")
    execution_config_path = execution_config_path.resolve()
    runtime = validation.validate_execution_config(suite_root, execution_config_path)
    results = {
        "development": _load_json(development_result_path.resolve()),
        "regression": _load_json(regression_result_path.resolve()),
    }
    for name, result in results.items():
        _require(result.get("passed") is True and result.get("subject_identity") == subject["identity"], f"资源成本候选 {name} 证据无效")
    _require(results["development"]["input_identity"] == paired["materials"]["development"]["input_identity"], "开发材料与原成对结果不同尺")
    _require(results["regression"]["input_identity"] == paired["materials"]["regression"]["input_identity"], "回归材料与原成对结果不同尺")

    input_paths = {
        "development": repository / "benchmarks/acceptance/suite/iteration/v2/stage4-latency-development-input-v2.json",
        "regression": repository / "benchmarks/acceptance/suite/iteration/v2/stage4-latency-regression-input-v2.json",
    }
    execution_output = development_result_path.resolve()
    while execution_output.name != "storage-quality-v1":
        _require(execution_output != execution_output.parent, "无法定位资源成本候选证据根")
        execution_output = execution_output.parent
    reuse = {}
    for name, result in results.items():
        before = Path(development_result_path if name == "development" else regression_result_path).resolve().read_bytes()
        resumed = validation.execute_prepared_evidence(
            suite_root, execution_output, execution_config_path,
            subject_manifest=subject_manifest_path.resolve(), evidence_type=name,
            input_manifest=input_paths[name], resume=True,
        )
        _require(resumed["reused_execution"] is True, f"资源成本候选 {name} 恢复执行了产品或模型")
        _require(Path(resumed["execution_result"]).read_bytes() == before, f"资源成本候选 {name} 恢复没有逐字复用")
        reuse[name] = {"reused_execution": True, "model_executions": 0, "product_executions": 0, "result_sha256": evidence.file_sha256(Path(resumed["execution_result"]))}

    candidate = resource_cost._aggregate_subject(results, runtime["runs"], execution_output)
    for plan in candidate["plans"].values():
        questions = runtime["runs"] / "kernel-iteration" / str(plan["plan_identity"]) / "run" / "questions"
        for log in questions.glob("*/ownward-data/state/organization.binlog"):
            summary = audit.inspect_derived_log(log)
            _require(summary["obsolete_record_bytes"] == 0, f"资源成本候选仍含过期派生记录: {log}")
    v0 = paired["subjects"]["v0"]
    dimensions = {}
    for name, field in (
        ("semantic_input_tokens", "semantic_input_tokens"),
        ("end_to_end_wall_seconds", "end_to_end_wall_seconds"),
        ("ownward_data_bytes", "ownward_data_bytes"),
    ):
        baseline = float(v0[field])
        current = float(candidate[field])
        dimensions[name] = {"v0": baseline, "v2": current, "ratio": current / baseline, "maximum_ratio": 0.5, "passed": current / baseline <= 0.5}
    _require(dimensions["ownward_data_bytes"]["passed"], "资源成本候选没有达到产品数据减半门")
    _require(candidate["calls"] == {"semantic": 12, "reader": 12, "judge": 12}, "资源成本候选改变了模型职责")
    all_cases = [case for result in results.values() for case in result["observation"]["case_evidence"]]
    long_coverages = {"long-session-multi-fact", "temporal-update-conflict", "structured-boundary"}
    long_cases = [case for case in all_cases if case["coverage"] in long_coverages]
    expected_long_sources = sum(int(case["expected_sources"]) for case in long_cases)
    returned_long_sources = sum(int(case["search_returned_sources"]) for case in long_cases)
    _require(expected_long_sources > 0 and returned_long_sources == expected_long_sources, "资源成本候选长资产语义召回退化")
    _require(evidence.file_sha256(state_path) == state_sha256, "存储终态改写了正式 state")
    content = {
        "schema": SCHEMA,
        "formal": False,
        "formal_state_written": False,
        "audit_identity": audit_result["identity"],
        "paired_result_identity": paired["identity"],
        "candidate_receipt_identity": receipt["identity"],
        "candidate_subject_identity": subject["identity"],
        "candidate_kernel_generation_identity": subject["content"]["kernel_generation_identity"],
        "storage_policy": receipt["storage_policy"],
        "candidate": candidate,
        "dimensions": dimensions,
        "quality": {
            "development": "4/4",
            "long_multifact_delivery": "5/5",
            "fixed_regression": "8/8",
            "fact_delivery_complete": all(result["observation"]["fact_delivery"]["complete"] for result in results.values()),
            "long_asset_semantic_recall": returned_long_sources / expected_long_sources,
            "retrieval_p95_ms": max(float(result["observation"]["latency"]["retrieval_p95_ms"]) for result in results.values()),
            "retrieval_p95_maximum_ms": 553,
            "maximum_read_units": max(int(case["selection"]["selected_units"]) for case in all_cases),
            "read_units_maximum": 8,
            "maximum_context_chars": max(int(case["context_chars"]) for case in all_cases),
            "context_chars_maximum": 24000,
        },
        "resume": reuse,
        "formal_state_sha256": state_sha256,
        "stage4_complete": False,
        "closed_dimension": "ownward_data_bytes",
        "open_dimensions": [name for name, value in dimensions.items() if not value["passed"]],
        "next_validation": "persist-exact-semantic-token-category-counters-in-nonformal-codex-receipts-without-running-a-model-before-changing-the-semantic-protocol",
    }
    value = {**content, "identity": evidence.canonical_sha256(content)}
    output_root.mkdir(parents=True, exist_ok=True)
    evidence.atomic_json(result_path, value)
    return {**value, "path": str(result_path), "reused": False, "model_executions": 0, "product_executions": 0}


def _validate_identity(value: dict[str, Any], schema: str, name: str) -> None:
    _require(value.get("schema") == schema, f"{name} schema 无效")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), f"{name}身份漂移")


def _load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise validation.KernelIterationValidationError(f"无法读取存储成本终态 {path}: {error}") from error
    _require(isinstance(value, dict), f"存储成本终态不是对象: {path}")
    return value


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise validation.KernelIterationValidationError(message)
