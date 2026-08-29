from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import kernel_iteration_evidence as evidence
import kernel_iteration_stage4_performance as paired
import kernel_iteration_validation as validation


CONTRACT_SCHEMA = "ownward.kernel-iteration-stage4-protection-performance-contract/v1"
RESULT_SCHEMA = "ownward.kernel-iteration-stage4-protection-performance/v1"


def run(
    suite_root: Path,
    output_root: Path,
    baseline_subject_manifest: Path,
    baseline_execution_config: Path,
    baseline_run_root: Path,
    baseline_result_path: Path,
    candidate_subject_manifest: Path,
    candidate_execution_config: Path,
    candidate_run_root: Path,
    candidate_result_path: Path,
    formal_state: Path,
) -> dict[str, Any]:
    contract = load_contract(suite_root)
    comparison = evidence.load_contract(suite_root)
    subjects = {
        "baseline": evidence.validate_v2_subject(comparison, _load_json(baseline_subject_manifest.resolve())),
        "candidate": evidence.validate_v2_subject(comparison, _load_json(candidate_subject_manifest.resolve())),
    }
    runtimes = {
        "baseline": validation.validate_execution_config(suite_root, baseline_execution_config.resolve()),
        "candidate": validation.validate_execution_config(suite_root, candidate_execution_config.resolve()),
    }
    results = {
        "baseline": validation._load_execution_result(baseline_result_path.resolve()),
        "candidate": validation._load_execution_result(candidate_result_path.resolve()),
    }
    for name in ("baseline", "candidate"):
        _require(subjects[name]["identity"] == contract["subjects"][name], f"{name} subject 错绑")
        binary = subjects[name]["content"]["artifacts"]["binary"]
        _require(evidence.file_sha256(runtimes[name]["binary"]) == binary == contract["binaries"][name], f"{name} 二进制漂移")
        _require(evidence.file_sha256(runtimes[name]["protocol"]) == contract["protocol_sha256"], f"{name} 协议漂移")
        _require(results[name]["identity"] == contract["sealed_results"][name], f"{name} 原始结果错绑")
        _require(results[name]["observation"]["final_answer_accuracy"] == 1.0, f"{name} 不是质量通过的保护结果")
        _require(results[name]["shared_conditions"]["dataset"] == contract["materials_identity"], f"{name} 材料不同尺")

    cases = {item["case_id"]: item["question"] for item in contract["materials"]["cases"]}
    _require(len(cases) == int(contract["schedule"]["workers"]), "保护性能案例与并发不同尺")
    roots = {"baseline": baseline_run_root.resolve(), "candidate": candidate_run_root.resolve()}
    before = {name: paired._data_identities(roots[name], cases, contract["prepared_data"][name]) for name in roots}
    state_path = formal_state.resolve()
    state_before = evidence.file_sha256(state_path)
    _require(state_before == contract["formal_state_sha256"], "保护性能执行前正式 state 漂移")
    samples: dict[str, list[dict[str, Any]]] = {"baseline": [], "candidate": []}
    for order in contract["schedule"]["balanced_order"]:
        _require(sorted(order) == ["baseline", "candidate"], "保护性能顺序不是平衡配对")
        for name in order:
            samples[name].extend(paired._run_round(runtimes[name], roots[name], cases, contract["schedule"]))
    after = {name: paired._data_identities(roots[name], cases, contract["prepared_data"][name]) for name in roots}
    _require(before == after, "保护性能复核改写了候选数据")
    state_after = evidence.file_sha256(state_path)
    _require(state_after == state_before, "保护性能复核改写了正式 state")
    metrics = paired.evaluate(samples, float(contract["gates"]["candidate_p95_delta_max_ms"]))
    content = {
        "schema": RESULT_SCHEMA,
        "formal": False,
        "formal_state_written": False,
        "model_calls": 0,
        "product_mutations": 0,
        "contract_identity": contract["identity"],
        "subjects": dict(contract["subjects"]),
        "sealed_results": dict(contract["sealed_results"]),
        "materials_identity": contract["materials_identity"],
        "schedule": dict(contract["schedule"]),
        "metrics": metrics,
        "prepared_data_sha256_before": before,
        "prepared_data_sha256_after": after,
        "formal_state_sha256_before": state_before,
        "formal_state_sha256_after": state_after,
        "passed": metrics["passed"],
    }
    value = {**content, "identity": evidence.canonical_sha256(content)}
    destination = output_root.resolve() / "stage4-multisource" / "protection-performance" / subjects["candidate"]["identity"] / "result.json"
    _require(not destination.exists(), "保护性能证据已经存在；禁止随机重跑")
    evidence.atomic_json(destination, value)
    return {**value, "path": str(destination)}


def load_contract(suite_root: Path) -> dict[str, Any]:
    path = suite_root.resolve() / "iteration" / "v2" / "stage4-protection-performance-contract.json"
    value = _load_json(path)
    _validate_identity(value, CONTRACT_SCHEMA, "保护性能合同")
    _require(value.get("frozen_before_performance_replay") is True, "保护性能合同没有在执行前冻结")
    _require(value.get("random_rerun_allowed") is False and value.get("model_or_answer_execution") is False, "保护性能合同允许随机或模型重跑")
    materials = validation.validate_stage3_materials(
        _load_json(suite_root.resolve() / "iteration" / "v2" / "stage3-development-materials.json"),
        expected_questions=int(value["schedule"]["workers"]),
    )
    _require(materials["identity"] == value["materials_identity"], "保护性能材料漂移")
    _require(evidence.file_sha256(Path(__file__).resolve()) == value["controller_sha256"], "保护性能控制器漂移")
    _require(evidence.file_sha256(Path(paired.__file__).resolve()) == value["paired_controller_sha256"], "成对性能底层控制器漂移")
    return {**value, "materials": materials}


def _validate_identity(value: dict[str, Any], schema: str, name: str) -> None:
    _require(value.get("schema") == schema, f"{name} schema 无效")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), f"{name}身份漂移")


def _load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise validation.KernelIterationValidationError(f"无法读取保护性能制品 {path}: {error}") from error
    _require(isinstance(value, dict), f"保护性能制品不是对象: {path}")
    return value


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise validation.KernelIterationValidationError(message)
