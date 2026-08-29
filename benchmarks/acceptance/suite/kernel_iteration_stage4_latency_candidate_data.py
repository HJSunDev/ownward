from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import kernel_iteration_evidence as evidence
import kernel_iteration_stage4_latency_data as latency_data
import kernel_iteration_validation as validation


RECEIPT_SCHEMA = "ownward.kernel-iteration-stage4-retrieval-latency-candidate-preparation/v2"


def prepare(
    suite_root: Path,
    output_root: Path,
    subject_manifest: Path,
    execution_config: Path,
    baseline_preparation: Path,
    persistent_root: Path,
    formal_state: Path,
    *,
    resume: bool = False,
) -> dict[str, Any]:
    suite_root, output_root = suite_root.resolve(), output_root.resolve()
    repository = suite_root.parents[2]
    _require(output_root.is_relative_to(repository / ".tmp"), "检索时延候选准备收据只能写入非正式 .tmp 边界")
    raw_subject = _load_json(subject_manifest.resolve())
    subject = evidence.validate_v2_subject(evidence.load_contract(suite_root), raw_subject)
    runtime = validation.validate_execution_config(suite_root, execution_config.resolve())
    _require(raw_subject.get("artifacts", {}).get("binary") == evidence.file_sha256(runtime["binary"]), "检索时延候选准备错绑二进制")
    baseline = _load_json(baseline_preparation.resolve())
    _validate_identity(baseline, latency_data.RECEIPT_SCHEMA, "检索时延基线 prepared-data 收据")
    materials = latency_data.load_materials(suite_root)
    state_path = formal_state.resolve()
    state_before = evidence.file_sha256(state_path)
    _require(
        state_before == baseline.get("formal_state_sha256_before") == baseline.get("formal_state_sha256_after"),
        "检索时延候选准备前正式 state 漂移",
    )
    persistent_root = persistent_root.resolve()
    _require("kernel-iteration" in persistent_root.parts and "retrieval-latency" in persistent_root.parts, "检索时延候选数据必须位于隔离非正式边界")
    candidate_root = persistent_root / "prepared-candidates" / subject["identity"]
    receipt_path = output_root / "candidate-preparation.json"
    if receipt_path.is_file():
        _require(resume, "检索时延候选 prepared data 已存在；禁止覆盖")
        receipt = _load_json(receipt_path)
        _validate_receipt(receipt, subject, runtime, candidate_root, baseline, materials, state_path)
        return {**receipt, "path": str(receipt_path), "reused": True}
    _require(not receipt_path.exists(), "检索时延候选数据收据现场不是普通文件")
    reused_data = candidate_root.exists()
    if reused_data:
        _require(candidate_root.is_dir(), "检索时延候选数据现场不是目录")
    else:
        for case in materials["cases"]:
            latency_data._prepare_case(runtime, candidate_root / str(case["case_id"]) / "ownward-data", materials, case)
    prepared = {
        str(case["case_id"]): latency_data._tree_sha256(candidate_root / str(case["case_id"]) / "ownward-data")
        for case in materials["cases"]
    }
    authority = {
        str(case["case_id"]): latency_data._materialized_authority_sha256(candidate_root / str(case["case_id"]) / "ownward-data")
        for case in materials["cases"]
    }
    _require(authority == baseline["materialized_authority_sha256"]["current-v2"], "检索时延候选与冻结材料不同尺")
    state_after = evidence.file_sha256(state_path)
    _require(state_after == state_before, "检索时延候选数据准备改写了正式 state")
    content = {
        "schema": RECEIPT_SCHEMA,
        "formal": False,
        "formal_state_written": False,
        "subject_identity": subject["identity"],
        "kernel_generation_identity": raw_subject["kernel_generation_identity"],
        "kernel_effect_identity": raw_subject["kernel_effect_identity"],
        "binary_sha256": evidence.file_sha256(runtime["binary"]),
        "embedding_manifest_sha256": evidence.file_sha256(runtime["embedding"] / "manifest.json"),
        "protocol_sha256": evidence.file_sha256(runtime["protocol"]),
        "materials_identity": materials["identity"],
        "baseline_preparation_identity": baseline["identity"],
        "candidate_root": str(candidate_root),
        "prepared_data_sha256": prepared,
        "materialized_authority_sha256": authority,
        "preparer_sha256": evidence.file_sha256(Path(__file__).resolve()),
        "formal_state_sha256_before": state_before,
        "formal_state_sha256_after": state_after,
        "codex_calls": 0,
        "answer_generation_calls": 0,
        "data_reused_without_product_execution": reused_data,
    }
    receipt = {**content, "identity": evidence.canonical_sha256(content)}
    evidence.atomic_json(receipt_path, receipt)
    return {**receipt, "path": str(receipt_path), "reused": reused_data}


def _validate_receipt(receipt: dict[str, Any], subject: dict[str, Any], runtime: dict[str, Any], candidate_root: Path, baseline: dict[str, Any], materials: dict[str, Any], state_path: Path) -> None:
    _validate_identity(receipt, RECEIPT_SCHEMA, "检索时延候选 prepared-data 收据")
    _require(receipt.get("subject_identity") == subject["identity"], "检索时延候选 prepared-data 身份错绑")
    _require(receipt.get("binary_sha256") == evidence.file_sha256(runtime["binary"]), "检索时延候选 prepared-data 二进制漂移")
    _require(receipt.get("embedding_manifest_sha256") == evidence.file_sha256(runtime["embedding"] / "manifest.json"), "检索时延候选 prepared-data 向量漂移")
    _require(receipt.get("protocol_sha256") == evidence.file_sha256(runtime["protocol"]), "检索时延候选 prepared-data 协议漂移")
    _require(receipt.get("materials_identity") == materials["identity"], "检索时延候选 prepared-data 材料漂移")
    _require(receipt.get("baseline_preparation_identity") == baseline["identity"], "检索时延候选 prepared-data 基线漂移")
    _require(receipt.get("preparer_sha256") == evidence.file_sha256(Path(__file__).resolve()), "检索时延候选准备器漂移")
    prepared = {str(case["case_id"]): latency_data._tree_sha256(candidate_root / str(case["case_id"]) / "ownward-data") for case in materials["cases"]}
    authority = {str(case["case_id"]): latency_data._materialized_authority_sha256(candidate_root / str(case["case_id"]) / "ownward-data") for case in materials["cases"]}
    _require(prepared == receipt.get("prepared_data_sha256"), "检索时延候选 prepared data 漂移")
    _require(authority == receipt.get("materialized_authority_sha256") == baseline["materialized_authority_sha256"]["current-v2"], "检索时延候选权威事实漂移")
    current_state = evidence.file_sha256(state_path)
    _require(receipt.get("formal_state_sha256_before") == receipt.get("formal_state_sha256_after") == current_state, "检索时延候选准备后正式 state 漂移")


def _validate_identity(value: dict[str, Any], schema: str, name: str) -> None:
    _require(value.get("schema") == schema, f"{name} schema 无效")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), f"{name}身份漂移")


def _load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise validation.KernelIterationValidationError(f"无法读取检索时延候选数据制品 {path}: {error}") from error
    _require(isinstance(value, dict), f"检索时延候选数据制品不是对象: {path}")
    return value


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise validation.KernelIterationValidationError(message)
