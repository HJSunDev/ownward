from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import kernel_iteration_evidence as evidence
import kernel_iteration_validation as validation


CONTRACT_SCHEMA = "ownward.kernel-iteration-stage4-resource-cost-non-create-decomposition-contract/v1"
RESULT_SCHEMA = "ownward.kernel-iteration-stage4-resource-cost-non-create-decomposition-result/v1"
CONTRACT_PATH = Path("iteration/v2/stage4-resource-cost-non-create-decomposition-contract.json")


def run(suite_root: Path, output_root: Path, formal_state: Path, *, resume: bool) -> dict[str, Any]:
    suite_root = suite_root.resolve()
    repository = suite_root.parents[2]
    output_root = output_root.resolve()
    _require(
        output_root.is_relative_to(repository / ".tmp" / "kernel-v2-major-iteration"),
        "非 CreateBatch 分解证据必须位于非正式 V2 边界",
    )
    contract = load_contract(suite_root)
    formal_state = formal_state.resolve()
    _require(formal_state == repository / contract["formal_state"]["path"], "正式 state 路径错绑")
    state_before = evidence.file_sha256(formal_state)
    _require(state_before == contract["formal_state"]["sha256"], "分解前正式 state 漂移")
    result_path = output_root / "result.json"
    if result_path.is_file():
        _require(resume, "非 CreateBatch 分解终态已存在；只有 --resume 可逐字复用")
        value = _load_json(result_path)
        _validate_identity(value, RESULT_SCHEMA, "非 CreateBatch 分解终态")
        _require(value["contract_identity"] == contract["identity"], "非 CreateBatch 分解终态合同错绑")
        _require(value["formal_state_sha256"] == state_before, "非 CreateBatch 分解恢复时正式 state 漂移")
        return {**value, "path": str(result_path), "reused": True, "model_executions": 0, "product_executions": 0}

    sources = {
        name: _verified_source(repository, item, name)
        for name, item in contract["sources"].items()
    }
    result = evaluate(contract, sources, state_before)
    _require(evidence.file_sha256(formal_state) == state_before, "非 CreateBatch 分解改写正式 state")
    output_root.mkdir(parents=True, exist_ok=True)
    evidence.atomic_json(result_path, result)
    return {**result, "path": str(result_path), "reused": False, "model_executions": 0, "product_executions": 0}


def evaluate(contract: dict[str, Any], sources: dict[str, dict[str, Any]], state_sha256: str) -> dict[str, Any]:
    wall_audit = sources["real_capability_wall_audit"]["end_to_end_wall_seconds"]
    calibration = sources["matched_calibration"]["subjects"]["v2"]
    matched_create = sources["matched_create"]
    active = contract["active_gate"]

    local_seconds = float(wall_audit["current_candidate_component_seconds"])
    same_run_create = float(wall_audit["current_create_seconds"])
    critical = calibration["wall_critical_path"]
    critical_ids = {
        (material, question_id)
        for material, item in critical["materials"].items()
        for question_id in item["critical_question_ids"]
    }
    request_wall = sum(
        float(item["full_wall_seconds"])
        for item in calibration["request_ledger"]
        if (item["material"], item["question_id"]) in critical_ids
    )
    semantic_submit = float(critical["critical_chain_phase_seconds"]["semantic"]) - request_wall
    retrieval = float(critical["critical_chain_phase_seconds"]["retrieval"])
    same_run_non_create = semantic_submit + retrieval
    closure_error = local_seconds - same_run_create - same_run_non_create
    _require(abs(closure_error) <= float(contract["decomposition"]["maximum_closure_error_seconds"]), "同次关键路径无法闭合")

    matched_create_seconds = float(matched_create["means"]["v2"]["create_envelope_seconds"])
    mixed_residual = local_seconds - matched_create_seconds
    cross_observation_delta = same_run_create - matched_create_seconds
    _require(abs(mixed_residual - same_run_non_create - cross_observation_delta) <= 1e-9, "跨观测残差无法闭合")
    _require(abs(float(active["required_improvement_seconds"]) - float(matched_create["candidate_controlled_gate"]["current_plus_error_seconds"]) + float(matched_create["candidate_controlled_gate"]["controlled_half_maximum_seconds"])) <= 1e-9, "活动门缺口漂移")

    runner = float(critical["runner_and_unclassified_seconds"])
    scheduler = float(critical["critical_chain_phase_seconds"]["scheduler_residual"])
    process_ipc = runner - scheduler
    v2_phases = matched_create["means"]["v2"]
    durability = sum(
        float(v2_phases[name])
        for name in (
            "phase:authority.create_batch",
            "phase:pending-records-and-staged-index",
            "phase:derived.put_batch",
            "phase:derived.durability_barrier",
            "phase:lexical.memory_index",
            "phase:semantic.memory_index",
        )
    )
    categories = [
        {
            "name": "semantic-submission-and-product-execution",
            "observed_seconds": semantic_submit,
            "charged_to_active_non_create_component": True,
            "candidate_control": "direct-but-quality-and-submission-contract-protected",
            "conservative_removable_maximum_seconds": semantic_submit,
            "source": "matched-calibration-v2-critical-semantic-minus-critical-native-request-wall",
        },
        {
            "name": "retrieval-and-evidence-loading",
            "observed_seconds": retrieval,
            "charged_to_active_non_create_component": True,
            "candidate_control": "direct-but-closed-quality-and-latency-chain-protected",
            "conservative_removable_maximum_seconds": retrieval,
            "source": "matched-calibration-v2-critical-retrieval",
        },
        {
            "name": "local-scheduling",
            "observed_seconds": scheduler,
            "charged_to_active_non_create_component": False,
            "candidate_control": "excluded-by-frozen-runner-and-boundary-floor",
            "conservative_removable_maximum_seconds": 0.0,
            "source": "matched-calibration-v2-scheduler-residual",
        },
        {
            "name": "process-and-ipc",
            "observed_seconds": process_ipc,
            "charged_to_active_non_create_component": False,
            "candidate_control": "excluded-by-frozen-runner-and-boundary-floor",
            "conservative_removable_maximum_seconds": 0.0,
            "source": "matched-calibration-v2-runner-and-unclassified-minus-scheduler",
        },
        {
            "name": "durability-and-recovery",
            "observed_seconds": durability,
            "charged_to_active_non_create_component": False,
            "candidate_control": "inside-createbatch-and-already-proven-non-regressed",
            "conservative_removable_maximum_seconds": 0.0,
            "source": "matched-create-v2-authority-derived-and-index-subphases",
        },
    ]
    maximum = sum(float(item["conservative_removable_maximum_seconds"]) for item in categories)
    required = float(active["required_improvement_seconds"])
    authorized = maximum >= required
    _require(not authorized, "现有证据意外授权实现；必须重新冻结路线合同")

    content = {
        "schema": RESULT_SCHEMA,
        "formal": False,
        "formal_state_written": False,
        "contract_identity": contract["identity"],
        "active_gate": active,
        "same_observation_critical_path": {
            "candidate_local_seconds": local_seconds,
            "create_batch_seconds": same_run_create,
            "semantic_submission_and_product_execution_seconds": semantic_submit,
            "retrieval_and_evidence_loading_seconds": retrieval,
            "non_create_seconds": same_run_non_create,
            "closure_error_seconds": closure_error,
        },
        "cross_observation_arithmetic": {
            "matched_create_seconds": matched_create_seconds,
            "mixed_residual_seconds": mixed_residual,
            "same_observation_non_create_seconds": same_run_non_create,
            "cross_observation_create_delta_seconds": cross_observation_delta,
            "classification": "diagnostic-only-not-an-observed-critical-path",
        },
        "categories": categories,
        "route_authorization": {
            "required_improvement_seconds": required,
            "maximum_evidenced_non_create_improvement_seconds": maximum,
            "margin_seconds": maximum - required,
            "authorized_routes": [],
            "implementation_authorized": False,
            "reason": "even deleting every same-observation candidate-controlled non-CreateBatch second cannot reach the active matched gate",
        },
        "execution": {
            "model_executions": 0,
            "reader_executions": 0,
            "judge_executions": 0,
            "product_executions": 0,
            "new_observation_performed": False,
        },
        "decision": "retain-stage4-open-no-mathematical-non-create-headroom",
        "next_validation": "revisit-the-v0-controlled-wall-policy-or-identify-a-new-pre-result-candidate-controlled-architectural-component; do-not-implement-a-non-create-route-from-this-evidence",
        "formal_state_sha256": state_sha256,
    }
    return {**content, "identity": evidence.canonical_sha256(content)}


def load_contract(suite_root: Path) -> dict[str, Any]:
    repository = suite_root.parents[2]
    value = _load_json(suite_root / CONTRACT_PATH)
    _validate_identity(value, CONTRACT_SCHEMA, "非 CreateBatch 分解合同")
    _require(value.get("frozen_before_new_observation") is True, "分解合同未在新观测前冻结")
    _require(value.get("new_results_seen") is False, "分解合同错误声明已看到新结果")
    for item in value["direct_dependencies"]:
        path = repository / item["path"]
        _require(path.is_file() and evidence.file_sha256(path) == item["sha256"], f"分解直接依赖漂移: {item['path']}")
    return value


def _verified_source(repository: Path, item: dict[str, Any], name: str) -> dict[str, Any]:
    path = repository / item["path"]
    _require(path.is_file() and evidence.file_sha256(path) == item["sha256"], f"{name} 文件漂移")
    value = _load_json(path)
    _require(value.get("identity") == item["identity"], f"{name} 身份错绑")
    return value


def _validate_identity(value: dict[str, Any], schema: str, name: str) -> None:
    _require(value.get("schema") == schema, f"{name} schema 无效")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), f"{name}身份漂移")


def _load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise validation.KernelIterationValidationError(f"无法读取非 CreateBatch 分解制品 {path}: {error}") from error
    _require(isinstance(value, dict), f"非 CreateBatch 分解制品不是对象: {path}")
    return value


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise validation.KernelIterationValidationError(message)
