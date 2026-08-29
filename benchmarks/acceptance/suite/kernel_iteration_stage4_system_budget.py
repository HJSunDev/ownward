from __future__ import annotations

import json
import os
from pathlib import Path
import shutil
from typing import Any

import kernel_iteration_candidate_system_budget as system_candidate
import kernel_iteration_evidence as evidence
import kernel_iteration_stage4_latency_real_scale as real_scale
import kernel_iteration_validation as validation


CONTRACT_SCHEMA = "ownward.kernel-iteration-stage4-retrieval-latency-system-budget-contract/v1"
RESULT_SCHEMA = "ownward.kernel-iteration-stage4-retrieval-latency-system-budget-result/v1"


def run(
    suite_root: Path,
    output_root: Path,
    candidate_root: Path,
    execution_config_path: Path,
    preparation_receipt_path: Path,
    persistent_root: Path,
    formal_state_path: Path,
    *,
    resume: bool = False,
) -> dict[str, Any]:
    suite_root, output_root = suite_root.resolve(), output_root.resolve()
    repository = suite_root.parents[2]
    _require(output_root.is_relative_to(repository / ".tmp"), "系统线程预算证据只能写入非正式 .tmp 边界")
    contract = load_contract(suite_root)
    state_path = formal_state_path.resolve()
    state_before = evidence.file_sha256(state_path)
    _require(state_before == contract["formal_state_sha256"], "系统线程预算测量前正式 state 漂移")
    result_path = output_root / "result.json"
    if result_path.is_file():
        _require(resume, "系统线程预算结果已存在；禁止随机重跑")
        value = _load_json(result_path)
        _validate_identity(value, RESULT_SCHEMA, "系统线程预算结果")
        _require(value.get("contract_identity") == contract["identity"], "系统线程预算结果合同漂移")
        _require(evidence.file_sha256(state_path) == value.get("formal_state_sha256_after"), "系统线程预算恢复时正式 state 漂移")
        return {**value, "path": str(result_path), "reused": True, "product_executions": 0, "model_executions": 0}

    candidate = system_candidate.prepare(
        suite_root, candidate_root.resolve(), execution_config_path.resolve(), resume=resume,
    )
    _require(candidate["identity"] == contract["candidate_receipt_identity"], "系统线程预算候选收据错绑")
    _require(candidate["subject_identity"] == contract["candidate_subject_identity"], "系统线程预算候选身份错绑")
    _require(candidate["binary_sha256"] == contract["candidate_binary_sha256"], "系统线程预算候选二进制错绑")
    _require(candidate["embedding_runtime_configuration"] == contract["runtime_configuration"], "系统线程预算候选没有使用冻结 2/1 配置")
    binary = candidate_root.resolve() / ("ownward.exe" if os.name == "nt" else "ownward")
    runtime = validation.validate_execution_config(suite_root, Path(candidate["execution_config"]))
    _require(evidence.file_sha256(runtime["protocol"]) == contract["protocol_sha256"], "系统线程预算协议漂移")
    _require(evidence.file_sha256(runtime["embedding"] / "manifest.json") == contract["embedding_manifest_sha256"], "系统线程预算向量制品漂移")
    _require(evidence.file_sha256(repository / "internal" / "embedding" / "llama.go") == contract["embedding_runtime_source_sha256"], "产品原生 2/1 运行实现漂移")
    calibration_path = repository / str(contract["native_runtime_calibration_path"])
    calibration = _load_json(calibration_path)
    _validate_identity(calibration, "ownward.kernel-iteration-stage4-vector-runtime-calibration/v1", "产品原生 2/1 校准")
    _require(calibration["identity"] == contract["native_runtime_calibration_identity"], "产品原生 2/1 校准错绑")
    native_profiles = [
        item for item in calibration.get("configurations", [])
        if isinstance(item, dict) and item.get("threads") == 2 and item.get("parallel") == 1
    ]
    _require(len(native_profiles) == 1, "产品原生 2/1 校准缺失或重复")
    native_profile = native_profiles[0]
    _require(float(native_profile["maximum_vector_component_drift"]) <= float(contract["gates"]["maximum_vector_component_drift"]), "产品原生 2/1 向量漂移超过冻结边界")
    _require(abs(float(native_profile["single"]["p95_ms"]) - float(contract["gates"]["native_2_1_isolated_query_p95_ms"])) <= 1e-9, "产品原生 2/1 隔离下界漂移")

    preparation = _load_json(preparation_receipt_path.resolve())
    _validate_identity(preparation, real_scale.PREPARATION_SCHEMA, "系统线程预算 prepared data 收据")
    _require(preparation["identity"] == contract["preparation_identity"], "系统线程预算 prepared data 收据错绑")
    materials = real_scale.load_materials(suite_root)
    source_root = Path(preparation["subject_roots"]["candidate"]).resolve()
    target_ids = preparation["target_asset_ids"]["candidate"]
    source_before = real_scale._data_identities(source_root, materials)
    _require(source_before == preparation["prepared_data_sha256"]["candidate"], "系统线程预算源 prepared data 在测量前漂移")
    candidate_preparation = _prepare_candidate_data(
        output_root / "preparation.json", persistent_root.resolve(), source_root, binary,
        runtime, materials, candidate, preparation, state_path, resume=resume,
    )
    root = Path(candidate_preparation["candidate_root"])
    before = real_scale._data_identities(root, materials)
    _require(before == candidate_preparation["prepared_data_sha256"], "系统线程预算候选 prepared data 在测量前漂移")

    samples: list[dict[str, Any]] = []
    for _ in range(int(contract["schedule"]["balanced_rounds"])):
        samples.extend(real_scale._run_round(
            "candidate", binary, runtime, root, materials, target_ids, contract["schedule"],
        ))
    after = real_scale._data_identities(root, materials)
    _require(before == after, "系统线程预算只读测量改写了 prepared data")
    source_after = real_scale._data_identities(source_root, materials)
    _require(source_before == source_after, "系统线程预算测量改写了源 prepared data")
    state_after = evidence.file_sha256(state_path)
    _require(state_after == state_before, "系统线程预算测量改写了正式 state")
    summary = real_scale._summarize_samples(samples)
    metrics = evaluate(summary, contract)
    content = {
        "schema": RESULT_SCHEMA,
        "formal": False,
        "formal_state_written": False,
        "contract_identity": contract["identity"],
        "candidate_receipt_identity": candidate["identity"],
        "candidate_subject_identity": candidate["subject_identity"],
        "candidate_binary_sha256": candidate["binary_sha256"],
        "runtime_configuration": dict(contract["runtime_configuration"]),
        "system_thread_budget": dict(contract["system_thread_budget"]),
        "preparation_identity": preparation["identity"],
        "candidate_preparation_identity": candidate_preparation["identity"],
        "source_prepared_data_sha256_before": source_before,
        "source_prepared_data_sha256_after": source_after,
        "prepared_data_sha256_before": before,
        "prepared_data_sha256_after": after,
        "formal_state_sha256_before": state_before,
        "formal_state_sha256_after": state_after,
        "execution_identity": {
            "controller_sha256": evidence.file_sha256(Path(__file__).resolve()),
            "candidate_builder_sha256": evidence.file_sha256(Path(system_candidate.__file__).resolve()),
            "measurement_controller_sha256": evidence.file_sha256(Path(real_scale.__file__).resolve()),
            "transport_sha256": evidence.file_sha256(repository / "benchmarks" / "support" / "ownward_mcp.py"),
            "protocol_sha256": evidence.file_sha256(runtime["protocol"]),
            "embedding_manifest_sha256": evidence.file_sha256(runtime["embedding"] / "manifest.json"),
            "embedding_runtime_source_sha256": evidence.file_sha256(repository / "internal" / "embedding" / "llama.go"),
        },
        "schedule": dict(contract["schedule"]),
        "native_runtime_calibration": {
            "identity": calibration["identity"],
            "maximum_vector_component_drift": native_profile["maximum_vector_component_drift"],
            "single_query": native_profile["single"],
            "vector_sha256": native_profile["vector_sha256"],
        },
        "samples": samples,
        "metrics": metrics,
        "root_status": metrics["root_status"],
        "next_validation": metrics["next_validation"],
    }
    value = {**content, "identity": evidence.canonical_sha256(content)}
    output_root.mkdir(parents=True, exist_ok=True)
    evidence.atomic_json(result_path, value)
    return {**value, "path": str(result_path), "reused": False, "product_executions": len(samples) + 4 * int(contract["schedule"]["balanced_rounds"]), "model_executions": 0}


def _prepare_candidate_data(
    receipt_path: Path,
    candidate_root: Path,
    source_root: Path,
    binary: Path,
    runtime: dict[str, Any],
    materials: dict[str, Any],
    candidate: dict[str, Any],
    source_preparation: dict[str, Any],
    state_path: Path,
    *,
    resume: bool,
) -> dict[str, Any]:
    _require("kernel-iteration" in candidate_root.parts and any(part.startswith("retrieval-latency") for part in candidate_root.parts), "系统线程预算候选数据越过非正式持久边界")
    if receipt_path.is_file():
        _require(resume, "系统线程预算候选 prepared data 已存在；禁止随机覆盖")
        value = _load_json(receipt_path)
        _validate_identity(value, "ownward.kernel-iteration-stage4-system-budget-preparation/v1", "系统线程预算候选准备")
        _require(value.get("candidate_subject_identity") == candidate["subject_identity"], "系统线程预算候选 prepared data 身份错绑")
        _require(value.get("source_preparation_identity") == source_preparation["identity"], "系统线程预算源 prepared data 身份错绑")
        _require(value.get("prepared_data_sha256") == real_scale._data_identities(candidate_root, materials), "系统线程预算候选 prepared data 漂移")
        _require(value.get("formal_state_sha256_before") == value.get("formal_state_sha256_after") == evidence.file_sha256(state_path), "系统线程预算候选准备恢复时正式 state 漂移")
        return value
    _require(not receipt_path.exists() and not candidate_root.exists(), "系统线程预算候选 prepared data 现场不完整；禁止覆盖")
    source_payload = _payload_identities(source_root, materials)
    candidate_root.parent.mkdir(parents=True, exist_ok=True)
    shutil.copytree(source_root, candidate_root, copy_function=shutil.copy2)
    for case in materials["cases"]:
        data_dir = candidate_root / str(case["case_id"]) / "ownward-data"
        lock_path = data_dir / "assets" / ".ownward.lock"
        control_path = data_dir / "authority" / "control.json"
        if lock_path.exists():
            lock_path.unlink()
        _require(control_path.is_file(), "系统线程预算源控制状态缺失")
        control_path.unlink()
    _require(_payload_identities(candidate_root, materials) == source_payload, "系统线程预算候选克隆改写了权威资产或派生记录")
    state_before = evidence.file_sha256(state_path)
    for case in materials["cases"]:
        data_dir = candidate_root / str(case["case_id"]) / "ownward-data"
        environment = os.environ.copy()
        environment["OWNWARD_EMBEDDING_BUNDLE_DIR"] = str(runtime["embedding"])
        with real_scale.longmemeval.adapter.OwnwardRuntime(binary, data_dir, environment, startup_seconds=90, operation_seconds=60) as service:
            rules = service.client.call_tool("ownward_rules", {})
            _require(isinstance(rules, dict), "系统线程预算候选控制初始化失败")
    _require(_payload_identities(candidate_root, materials) == source_payload, "系统线程预算候选控制初始化改写了权威资产或派生记录")
    state_after = evidence.file_sha256(state_path)
    _require(state_after == state_before, "系统线程预算候选准备改写了正式 state")
    content = {
        "schema": "ownward.kernel-iteration-stage4-system-budget-preparation/v1",
        "formal": False,
        "formal_state_written": False,
        "candidate_subject_identity": candidate["subject_identity"],
        "candidate_binary_sha256": candidate["binary_sha256"],
        "source_preparation_identity": source_preparation["identity"],
        "source_root": str(source_root),
        "candidate_root": str(candidate_root),
        "reuse_method": "byte-exact-authority-and-derived-clone-with-authority-owned-control-reinitialization",
        "source_payload_sha256": source_payload,
        "candidate_payload_sha256": _payload_identities(candidate_root, materials),
        "prepared_data_sha256": real_scale._data_identities(candidate_root, materials),
        "formal_state_sha256_before": state_before,
        "formal_state_sha256_after": state_after,
        "product_initializations": len(materials["cases"]),
        "asset_or_semantic_repreparation": False,
        "preparer_sha256": evidence.file_sha256(Path(__file__).resolve()),
    }
    value = {**content, "identity": evidence.canonical_sha256(content)}
    receipt_path.parent.mkdir(parents=True, exist_ok=True)
    evidence.atomic_json(receipt_path, value)
    return value


def _payload_identities(root: Path, materials: dict[str, Any]) -> dict[str, str]:
    result: dict[str, str] = {}
    for case in materials["cases"]:
        data_dir = root / str(case["case_id"]) / "ownward-data"
        entries = []
        for relative in (Path("assets") / "information.jsonl", Path("assets") / "manifest.json", Path("state") / "organization.binlog"):
            path = data_dir / relative
            _require(path.is_file(), f"系统线程预算 prepared payload 缺失: {relative.as_posix()}")
            entries.append({"path": relative.as_posix(), "size": path.stat().st_size, "sha256": evidence.file_sha256(path)})
        result[str(case["case_id"])] = evidence.canonical_sha256(entries)
    return result


def evaluate(summary: dict[str, Any], contract: dict[str, Any]) -> dict[str, Any]:
    gates = contract["gates"]
    p95 = float(summary["p95_ms"])
    improvement = float(gates["superseded_observed_p95_ms"]) - p95
    quality = bool(summary["target_delivery_complete"] and summary["stable_selection_trace_per_case"])
    resources = int(summary["read_calls_max"]) <= 8 and int(summary["context_chars_max"]) <= 24000
    decision = p95 <= float(gates["decision_p95_ms_maximum"])
    engineering = p95 <= float(gates["route_p95_ms_maximum"]) and improvement >= float(gates["minimum_improvement_ms"])
    closed = quality and resources and decision and engineering
    ideal_three_thread_maximum_gain = float(gates["native_2_1_isolated_query_p95_ms"]) / 3.0
    three_thread_ideal_p95 = p95 - ideal_three_thread_maximum_gain
    three_thread_allowed = (
        not closed
        and p95 > float(gates["route_p95_ms_maximum"])
        and three_thread_ideal_p95 <= float(gates["route_p95_ms_maximum"])
    )
    if closed:
        next_validation = "run-only-the-invalidated-quality-and-resume-protections-for-this-subject"
    elif three_thread_allowed:
        next_validation = "one-and-only-one-3-1-four-worker-probe-is-mathematically-admissible"
    else:
        next_validation = "reject-cross-process-thread-budget-route-without-testing-3-1"
    return {
        **summary,
        "superseded_observed_p95_ms": float(gates["superseded_observed_p95_ms"]),
        "observed_p95_improvement_ms": improvement,
        "decision_p95_complete": decision,
        "engineering_margin_complete": engineering,
        "quality_trace_complete": quality,
        "resource_bounds_complete": resources,
        "ideal_3_1_maximum_p95_gain_ms": ideal_three_thread_maximum_gain,
        "ideal_3_1_projected_p95_ms": three_thread_ideal_p95,
        "three_thread_probe_allowed": three_thread_allowed,
        "root_status": "closed" if closed else "open",
        "next_validation": next_validation,
    }


def load_contract(suite_root: Path) -> dict[str, Any]:
    value = _load_json(suite_root / "iteration" / "v2" / "stage4-retrieval-latency-system-budget-contract.json")
    _validate_identity(value, CONTRACT_SCHEMA, "系统线程预算合同")
    _require(value.get("frozen_before_measurement") is True and value.get("candidate_results_seen") is False, "系统线程预算门槛未在结果前冻结")
    repository = suite_root.parents[2]
    paths = {
        "controller_sha256": Path(__file__).resolve(),
        "candidate_builder_sha256": Path(system_candidate.__file__).resolve(),
        "measurement_controller_sha256": Path(real_scale.__file__).resolve(),
        "transport_sha256": repository / "benchmarks" / "support" / "ownward_mcp.py",
        "embedding_runtime_source_sha256": repository / "internal" / "embedding" / "llama.go",
    }
    for field, path in paths.items():
        _require(value.get(field) == evidence.file_sha256(path), f"系统线程预算合同直接依赖漂移: {field}")
    _require(value.get("materials_identity") == real_scale.load_materials(suite_root)["identity"], "系统线程预算材料错绑")
    _require(value.get("runtime_configuration") == {"threads": 2, "threads_batch": 2, "parallel": 1}, "系统线程预算合同不是产品原生 2/1")
    budget = value.get("system_thread_budget")
    _require(isinstance(budget, dict) and budget.get("logical_processors") == 12 and budget.get("aggregate_inference_threads") == 8 and budget.get("independent_workers") == 4, "系统线程预算合同没有封存 12 核/四 worker/八推理线程")
    return value


def _validate_identity(value: dict[str, Any], schema: str, name: str) -> None:
    _require(value.get("schema") == schema, f"{name} schema 无效")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), f"{name}身份漂移")


def _load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise validation.KernelIterationValidationError(f"无法读取系统线程预算制品 {path}: {error}") from error
    _require(isinstance(value, dict), f"系统线程预算制品不是对象: {path}")
    return value


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise validation.KernelIterationValidationError(message)
