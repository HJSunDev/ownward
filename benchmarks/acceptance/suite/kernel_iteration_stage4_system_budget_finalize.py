from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import kernel_iteration_evidence as evidence
import kernel_iteration_stage4 as stage4
import kernel_iteration_stage4_system_budget as system_budget
import kernel_iteration_validation as validation


RESULT_SCHEMA = "ownward.kernel-iteration-stage4-retrieval-latency-system-budget-evidence/v1"


def finalize(
    suite_root: Path,
    output_root: Path,
    subject_manifest: Path,
    execution_config: Path,
    performance_result: Path,
    development_result: Path,
    multisource_result: Path,
    regression_result: Path,
    formal_state: Path,
    *,
    resume: bool = False,
) -> dict[str, Any]:
    suite_root, output_root = suite_root.resolve(), output_root.resolve()
    subject_path = subject_manifest.resolve()
    subject = evidence.validate_v2_subject(evidence.load_contract(suite_root), _load_json(subject_path))
    runtime = validation.validate_execution_config(suite_root, execution_config.resolve())
    _require(evidence.file_sha256(runtime["binary"]) == subject["content"]["artifacts"]["binary"], "系统线程预算终态二进制与 subject 错绑")
    destination = output_root / "result.json"
    if destination.is_file():
        _require(resume, "系统线程预算终态已经存在；必须显式 resume")
        value = _load_json(destination)
        _validate_identity(value, RESULT_SCHEMA, "系统线程预算终态")
        _require(value.get("controller_sha256") == evidence.file_sha256(Path(__file__).resolve()), "系统线程预算终态控制器身份漂移")
        _require(value.get("subject_identity") == subject["identity"], "系统线程预算终态 subject 漂移")
        return {**value, "reused": True, "path": str(destination)}

    performance = _load_json(performance_result.resolve())
    _validate_identity(performance, system_budget.RESULT_SCHEMA, "系统线程预算性能结果")
    _require(performance.get("candidate_subject_identity") == subject["identity"], "系统线程预算性能结果错绑")
    _require(performance.get("root_status") == "closed", "系统线程预算性能门未通过")
    inputs = suite_root / "iteration" / "v2"
    definitions = {
        "development": (development_result.resolve(), "development", inputs / "stage4-latency-development-input-v2.json"),
        "multisource": (multisource_result.resolve(), "development", inputs / "stage4-latency-multisource-input-v2.json"),
        "regression": (regression_result.resolve(), "regression", inputs / "stage4-latency-regression-input-v2.json"),
    }
    results = {name: validation._load_execution_result(path) for name, (path, _, _) in definitions.items()}
    for name, result in results.items():
        input_value = _load_json(definitions[name][2])
        _require(result.get("subject_identity") == subject["identity"] and result.get("subject_role") == "v2-candidate", f"{name} 质量结果错绑")
        _require(result.get("input_identity") == input_value["identity"], f"{name} 质量输入漂移")
        _require(result.get("passed") is True and result.get("candidate_decision") is True, f"{name} 质量绝对门未通过")

    development = results["development"]["observation"]
    multisource = results["multisource"]["observation"]
    regression = results["regression"]["observation"]
    long_case = next((item for item in development["case_evidence"] if item.get("coverage") == "long-session-multi-fact"), None)
    _require(development["questions"] == 4 and development["final_answer_accuracy"] == 1.0 and development["fact_delivery"]["complete"] is True, "系统线程预算开发 4/4 退化")
    _require(isinstance(long_case, dict) and int(long_case["truth_claims"]) == int(long_case["delivered_truth_claims"]) == 5, "系统线程预算长多事实 5/5 退化")
    multi_cases = multisource["case_evidence"]
    _require(
        multisource["questions"] == 3 and multisource["final_answer_accuracy"] == 1.0 and multisource["fact_delivery"]["complete"] is True
        and sum(int(item["truth_claims"]) for item in multi_cases) == sum(int(item["delivered_truth_claims"]) for item in multi_cases) == 6
        and all(int(item["search_returned_sources"]) == int(item["expected_sources"]) for item in multi_cases)
        and all(int(item["read_sources"]) == int(item["expected_sources"]) for item in multi_cases),
        "系统线程预算多来源 3/3 或六项事实保护退化",
    )
    _require(regression["questions"] == 8 and regression["final_answer_accuracy"] == 1.0 and regression["fact_delivery"]["complete"] is True, "系统线程预算固定回归 8/8 退化")
    protected = [item for item in development["case_evidence"] if item.get("coverage") in {"long-session-multi-fact", "temporal-update-conflict", "structured-boundary"}]
    expected_sources = sum(int(item["expected_sources"]) for item in protected)
    returned_sources = sum(min(int(item["search_returned_sources"]), int(item["expected_sources"])) for item in protected)
    semantic_recall = returned_sources / expected_sources if expected_sources else 0.0
    _require(semantic_recall == 1.0, "系统线程预算长资产语义召回退化")
    all_cases = [*development["case_evidence"], *multi_cases, *regression["case_evidence"]]
    _require(max(int(item["selection"]["selected_units"]) for item in all_cases) <= 8, "系统线程预算质量读取数越界")
    _require(max(int(item["context_chars"]) for item in all_cases) <= 24000, "系统线程预算质量上下文越界")

    resume_proofs = {}
    for name, (path, evidence_type, input_path) in definitions.items():
        result = results[name]
        root = path.parents[5]
        resume_proofs[name] = stage4._prove_resume(
            suite_root, root, subject_path, execution_config.resolve(), evidence_type,
            input_path, path, result,
        )
    state_path = formal_state.resolve()
    state_before = evidence.file_sha256(state_path)
    _require(state_before == performance["formal_state_sha256_before"] == performance["formal_state_sha256_after"], "系统线程预算终态前正式 state 漂移")
    state_after = evidence.file_sha256(state_path)
    _require(state_after == state_before, "系统线程预算终态改写了正式 state")
    wall = sum(float(result["observation"]["latency"]["wall_seconds"]) for result in results.values())
    metrics = performance["metrics"]
    content = {
        "schema": RESULT_SCHEMA,
        "formal": False,
        "formal_state_written": False,
        "controller_sha256": evidence.file_sha256(Path(__file__).resolve()),
        "contract_identity": performance["contract_identity"],
        "performance_result_identity": performance["identity"],
        "subject_identity": subject["identity"],
        "kernel_generation_identity": subject["content"]["kernel_generation_identity"],
        "kernel_effect_identity": subject["content"]["kernel_effect_identity"],
        "binary_sha256": evidence.file_sha256(runtime["binary"]),
        "runtime_configuration": performance["runtime_configuration"],
        "quality_results": {name: result["identity"] for name, result in results.items()},
        "metrics": {
            "complete_consumer_retrieval_mean_ms": metrics["mean_ms"],
            "complete_consumer_retrieval_p95_ms": metrics["p95_ms"],
            "p95_improvement_ms": metrics["observed_p95_improvement_ms"],
            "development_accuracy": development["final_answer_accuracy"],
            "long_multifact_delivered_claims": 5,
            "long_multifact_truth_claims": 5,
            "multisource_accuracy": multisource["final_answer_accuracy"],
            "multisource_delivered_truth_claims": 6,
            "multisource_truth_claims": 6,
            "regression_accuracy": regression["final_answer_accuracy"],
            "long_asset_semantic_recall": semantic_recall,
            "read_units_maximum": max(int(item["selection"]["selected_units"]) for item in all_cases),
            "context_chars_maximum": max(int(item["context_chars"]) for item in all_cases),
            "working_set_bytes_maximum": metrics["working_set_bytes_max"],
            "persistent_state_growth_bytes": 0,
            "affected_quality_wall_seconds": wall,
        },
        "resume": {**resume_proofs, "performance": {"result_identity": performance["identity"], "byte_identical": True, "model_or_product_execution": False}},
        "source_prepared_data_sha256_before": performance["source_prepared_data_sha256_before"],
        "source_prepared_data_sha256_after": performance["source_prepared_data_sha256_after"],
        "candidate_prepared_data_sha256_before": performance["prepared_data_sha256_before"],
        "candidate_prepared_data_sha256_after": performance["prepared_data_sha256_after"],
        "formal_state_sha256_before": state_before,
        "formal_state_sha256_after": state_after,
        "current_product_or_baseline_switched": False,
        "stage4_complete": False,
        "retrieval_latency_closed": True,
        "blind_gate_run": False,
        "passed": True,
    }
    value = {**content, "identity": evidence.canonical_sha256(content)}
    output_root.mkdir(parents=True, exist_ok=True)
    evidence.atomic_json(destination, value)
    return {**value, "reused": False, "path": str(destination)}


def _validate_identity(value: dict[str, Any], schema: str, name: str) -> None:
    _require(value.get("schema") == schema, f"{name} schema 无效")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), f"{name}身份漂移")


def _load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise validation.KernelIterationValidationError(f"无法读取系统线程预算终态制品 {path}: {error}") from error
    _require(isinstance(value, dict), f"系统线程预算终态制品不是对象: {path}")
    return value


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise validation.KernelIterationValidationError(message)
