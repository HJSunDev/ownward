from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import kernel_iteration_evidence as evidence
import kernel_iteration_stage4_latency_data as latency_data
import kernel_iteration_stage4_latency_performance as paired
import kernel_iteration_validation as validation


CONTRACT_SCHEMA = "ownward.kernel-iteration-stage4-retrieval-latency-contention-contract/v1"
RESULT_SCHEMA = "ownward.kernel-iteration-stage4-retrieval-latency-contention/v1"


def run(
    suite_root: Path,
    output_root: Path,
    current_execution_config: Path,
    v0_binary: Path,
    v0_embedding: Path,
    preparation_receipt: Path,
    paired_diagnosis: Path,
    formal_state: Path,
) -> dict[str, Any]:
    suite_root, output_root = suite_root.resolve(), output_root.resolve()
    contract = load_contract(suite_root)
    runtime = validation.validate_execution_config(suite_root, current_execution_config.resolve())
    v0_binary, v0_embedding = v0_binary.resolve(), v0_embedding.resolve()
    _require(evidence.file_sha256(runtime["binary"]) == contract["binaries"]["current-v2"], "串行诊断当前 V2 二进制漂移")
    _require(evidence.file_sha256(v0_binary) == contract["binaries"]["v0"], "串行诊断 V0 二进制漂移")
    _require(evidence.file_sha256(runtime["protocol"]) == contract["protocol_sha256"], "串行诊断协议漂移")
    for name, bundle in (("current-v2", runtime["embedding"]), ("v0", v0_embedding)):
        _require(evidence.file_sha256(bundle / "manifest.json") == contract["embedding_manifest_sha256"][name], f"串行诊断 {name} 向量制品漂移")

    receipt = _load_json(preparation_receipt.resolve())
    _validate_identity(receipt, latency_data.RECEIPT_SCHEMA, "串行诊断 prepared-data 收据")
    _require(receipt["identity"] == contract["preparation_identity"], "串行诊断 prepared-data 收据错绑")
    concurrent = _load_json(paired_diagnosis.resolve())
    _validate_identity(concurrent, paired.RESULT_SCHEMA, "并发诊断证据")
    _require(concurrent["identity"] == contract["paired_diagnosis_identity"], "串行诊断没有绑定唯一并发证据")
    materials = latency_data.load_materials(suite_root)
    cases = {str(item["case_id"]): str(item["query"]) for item in materials["cases"]}
    roots = {name: Path(receipt["subject_roots"][name]).resolve() for name in ("v0", "current-v2")}
    before = {name: paired._data_identities(roots[name], cases, contract["prepared_data_sha256"][name]) for name in roots}
    state_path = formal_state.resolve()
    state_before = evidence.file_sha256(state_path)
    _require(state_before == contract["formal_state_sha256"], "串行诊断前正式 state 漂移")
    runtimes = {
        "v0": {"binary": v0_binary, "embedding": v0_embedding, "protocol_value": runtime["protocol_value"]},
        "current-v2": runtime,
    }
    samples: dict[str, list[dict[str, Any]]] = {"v0": [], "current-v2": []}
    for order in contract["schedule"]["balanced_order"]:
        for name in order:
            for case_id, question in sorted(cases.items()):
                samples[name].extend(paired._run_round(name, runtimes[name], roots[name], {case_id: question}, contract["schedule"]))
    state_after = evidence.file_sha256(state_path)
    _require(state_after == state_before, "串行诊断改写了正式 state")
    after = {name: paired._data_identities(roots[name], cases, contract["prepared_data_sha256"][name]) for name in roots}
    _require(before == after, "串行诊断改写了 prepared data")
    serial = paired.evaluate(samples, {
        "paired_repeat_error_ms": contract["gates"]["paired_repeat_error_ms"],
        "minimum_first_stage_delta_contribution": 0.0,
        "retrieval_mean_ms_maximum": contract["gates"]["retrieval_mean_ms_maximum"],
        "retrieval_p95_ms_maximum": contract["gates"]["retrieval_p95_ms_maximum"],
    })
    concurrent_metrics = concurrent["metrics"]
    contention = {
        name: {
            "concurrent_search_p95_ms": float(concurrent_metrics[name]["search_p95_ms"]),
            "serial_search_p95_ms": float(serial[name]["search_p95_ms"]),
            "search_p95_amplification_ms": float(concurrent_metrics[name]["search_p95_ms"]) - float(serial[name]["search_p95_ms"]),
            "search_p95_amplification_ratio": float(concurrent_metrics[name]["search_p95_ms"]) / max(float(serial[name]["search_p95_ms"]), 0.001),
        }
        for name in ("v0", "current-v2")
    }
    repeat_error = float(contract["gates"]["paired_repeat_error_ms"])
    proven = (
        all(item["search_p95_amplification_ms"] > repeat_error for item in contention.values())
        and float(serial["current-v2"]["p95_ms"]) <= float(contract["gates"]["retrieval_p95_ms_maximum"])
        and float(serial["current-v2"]["mean_ms"]) <= float(contract["gates"]["retrieval_mean_ms_maximum"])
    )
    content = {
        "schema": RESULT_SCHEMA,
        "formal": False,
        "formal_state_written": False,
        "model_calls": 0,
        "answer_generation_calls": 0,
        "product_mutations": 0,
        "contract_identity": contract["identity"],
        "paired_diagnosis_identity": concurrent["identity"],
        "serial_metrics": serial,
        "contention": contention,
        "root_status": "proven" if proven else "not-proven",
        "first_proven_mechanism": "independent-process-local-vector-runtimes-contend-during-query-embedding" if proven else None,
        "candidate_route_allowed": proven,
        "prepared_data_sha256_before": before,
        "prepared_data_sha256_after": after,
        "formal_state_sha256_before": state_before,
        "formal_state_sha256_after": state_after,
    }
    value = {**content, "identity": evidence.canonical_sha256(content)}
    destination = output_root / "contention.json"
    _require(not destination.exists(), "串行竞争诊断已经存在；禁止随机重跑")
    evidence.atomic_json(destination, value)
    return {**value, "path": str(destination)}


def load_contract(suite_root: Path) -> dict[str, Any]:
    path = suite_root.resolve() / "iteration" / "v2" / "stage4-retrieval-latency-contention-contract.json"
    value = _load_json(path)
    _validate_identity(value, CONTRACT_SCHEMA, "串行竞争冻结合同")
    _require(value.get("frozen_before_serial_measurement") is True, "串行竞争合同没有在结果前冻结")
    _require(value.get("model_or_answer_execution") is False and value.get("random_rerun_allowed") is False, "串行竞争合同允许模型或随机重跑")
    _require(evidence.file_sha256(Path(__file__).resolve()) == value.get("controller_sha256"), "串行竞争控制器漂移")
    _require(evidence.file_sha256(Path(paired.__file__).resolve()) == value.get("paired_controller_sha256"), "并发控制器漂移")
    return value


def _validate_identity(value: dict[str, Any], schema: str, name: str) -> None:
    _require(value.get("schema") == schema, f"{name} schema 无效")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), f"{name}身份漂移")


def _load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise validation.KernelIterationValidationError(f"无法读取串行竞争制品 {path}: {error}") from error
    _require(isinstance(value, dict), f"串行竞争制品不是对象: {path}")
    return value


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise validation.KernelIterationValidationError(message)
