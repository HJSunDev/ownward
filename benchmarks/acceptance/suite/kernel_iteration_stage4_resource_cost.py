from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import kernel_iteration_evidence as evidence
import kernel_iteration_validation as validation


CONTRACT_SCHEMA = "ownward.kernel-iteration-stage4-resource-cost-contract/v1"
RESULT_SCHEMA = "ownward.kernel-iteration-stage4-resource-cost-evidence/v1"
CONTRACT_NAME = "stage4-end-to-end-resource-cost-contract.json"


def run(
    suite_root: Path,
    output_root: Path,
    subject_manifest: Path,
    execution_config: Path,
    baseline_binary: Path,
    baseline_embedding: Path,
    formal_state: Path,
    *,
    resume: bool,
) -> dict[str, Any]:
    suite_root = suite_root.resolve()
    repository = suite_root.parents[2]
    output_root = output_root.resolve()
    state_path = formal_state.resolve()
    contract = load_contract(suite_root)
    _require(output_root.is_relative_to(repository / ".tmp" / "kernel-v2-major-iteration"), "资源成本证据必须位于非正式 V2 边界")
    _require(state_path == repository / contract["formal_state"]["path"], "资源成本正式 state 路径错绑")
    state_before = evidence.file_sha256(state_path)
    _require(state_before == contract["formal_state"]["sha256"], "资源成本测量前正式 state 漂移")
    result_path = output_root / "result.json"
    if result_path.is_file():
        _require(resume, "资源成本终态已存在；只有 --resume 可逐字复用")
        value = _load_json(result_path)
        _validate_identity(value, RESULT_SCHEMA, "资源成本终态")
        _require(value["contract_identity"] == contract["identity"], "资源成本终态合同错绑")
        _require(value["formal_state_sha256_before"] == value["formal_state_sha256_after"] == state_before, "资源成本恢复时正式 state 漂移")
        return {**value, "path": str(result_path), "reused": True, "model_executions": 0, "product_executions": 0}

    subject_manifest = subject_manifest.resolve()
    execution_config = execution_config.resolve()
    baseline_binary = baseline_binary.resolve()
    baseline_embedding = baseline_embedding.resolve()
    _verify_file(subject_manifest, contract["candidate"]["subject_manifest"])
    _verify_file(execution_config, contract["candidate"]["execution_config"])
    _require(evidence.file_sha256(baseline_binary) == contract["v0"]["binary_sha256"], "V0 二进制漂移")
    _require(evidence.file_sha256(baseline_embedding / "manifest.json") == contract["v0"]["embedding_manifest_sha256"], "V0 向量制品漂移")

    candidate_output = repository / contract["candidate"]["evidence_output"]
    candidate_results: dict[str, dict[str, Any]] = {}
    candidate_resume: dict[str, Any] = {}
    for evidence_type in ("development", "regression"):
        item = contract["materials"][evidence_type]
        result_path_for_type = repository / item["candidate_result_path"]
        _verify_file(result_path_for_type, {"path": item["candidate_result_path"], "sha256": item["candidate_result_sha256"]})
        candidate_results[evidence_type] = _load_json(result_path_for_type)
        candidate_resume[evidence_type] = validation.execute_prepared_evidence(
            suite_root,
            candidate_output,
            execution_config,
            subject_manifest=subject_manifest,
            evidence_type=evidence_type,
            input_manifest=repository / item["input_manifest_path"],
            resume=True,
        )
        _require(candidate_resume[evidence_type]["reused_execution"] is True, "候选同身份恢复执行了产品或模型")

    v0_config = _baseline_execution_config(execution_config, baseline_binary, baseline_embedding)
    v0_config_path = output_root / "v0-execution.json"
    output_root.mkdir(parents=True, exist_ok=True)
    evidence.atomic_json(v0_config_path, v0_config)
    v0_output = output_root / "v0-evidence"
    v0_results: dict[str, dict[str, Any]] = {}
    resume_receipts: dict[str, Any] = {}
    for evidence_type in ("development", "regression"):
        item = contract["materials"][evidence_type]
        input_path = repository / item["input_manifest_path"]
        _verify_file(input_path, {"path": item["input_manifest_path"], "sha256": item["input_manifest_sha256"]})
        execution = validation.execute_prepared_evidence(
            suite_root,
            v0_output,
            v0_config_path,
            selector="v0",
            evidence_type=evidence_type,
            input_manifest=input_path,
            candidate_result_path=repository / item["candidate_result_path"],
            resume=False,
        )
        execution_result = Path(execution["execution_result"])
        before = execution_result.read_bytes()
        resumed = validation.execute_prepared_evidence(
            suite_root,
            v0_output,
            v0_config_path,
            selector="v0",
            evidence_type=evidence_type,
            input_manifest=input_path,
            candidate_result_path=repository / item["candidate_result_path"],
            resume=True,
        )
        _require(resumed["reused_execution"] is True and execution_result.read_bytes() == before, "V0 同身份恢复没有逐字复用")
        v0_results[evidence_type] = _load_json(execution_result)
        resume_receipts[evidence_type] = {
            "result_sha256": evidence.file_sha256(execution_result),
            "reused_execution": True,
            "model_or_product_execution": False,
        }

    runtime = validation.validate_execution_config(suite_root, execution_config)
    v0_runtime = validation.validate_execution_config(suite_root, v0_config_path)
    _require(runtime["runs"] == v0_runtime["runs"], "V0/V2 没有使用同一持久运行根")
    subjects = {
        "v0": _aggregate_subject(v0_results, runtime["runs"], v0_output),
        "v2": _aggregate_subject(candidate_results, runtime["runs"], candidate_output),
    }
    _verify_same_scale(v0_results, candidate_results)
    gates = evaluate(subjects, contract)
    state_after = evidence.file_sha256(state_path)
    _require(state_after == state_before, "资源成本测量改写了正式 state")
    content = {
        "schema": RESULT_SCHEMA,
        "formal": False,
        "formal_state_written": False,
        "contract_identity": contract["identity"],
        "candidate_subject_identity": contract["candidate"]["subject_identity"],
        "shared_conditions": candidate_results["development"]["shared_conditions"],
        "materials": {
            name: {
                "input_identity": candidate_results[name]["input_identity"],
                "candidate_result_identity": candidate_results[name]["identity"],
                "v0_result_identity": v0_results[name]["identity"],
            }
            for name in ("development", "regression")
        },
        "classification": contract["classification"],
        "subjects": subjects,
        "gates": gates,
        "resume": {
            "candidate": {name: {"reused_execution": True, "model_or_product_execution": False} for name in candidate_resume},
            "v0": resume_receipts,
        },
        "formal_state_sha256_before": state_before,
        "formal_state_sha256_after": state_after,
        "root_status": gates["root_status"],
        "next_validation": gates["next_validation"],
    }
    value = {**content, "identity": evidence.canonical_sha256(content)}
    evidence.atomic_json(result_path, value)
    return {**value, "path": str(result_path), "reused": False, "model_executions": 6, "product_executions": 12}


def evaluate(subjects: dict[str, Any], contract: dict[str, Any]) -> dict[str, Any]:
    ratios = {}
    for name, field in (
        ("semantic_input_tokens", "semantic_input_tokens"),
        ("end_to_end_wall_seconds", "end_to_end_wall_seconds"),
        ("ownward_data_bytes", "ownward_data_bytes"),
    ):
        baseline = float(subjects["v0"][field])
        candidate = float(subjects["v2"][field])
        _require(baseline > 0, f"V0 {name} 不是正数")
        ratio = candidate / baseline
        maximum = float(contract["gates"][name]["maximum_ratio"])
        ratios[name] = {"v0": baseline, "v2": candidate, "ratio": ratio, "maximum_ratio": maximum, "passed": ratio <= maximum}
    closed = all(item["passed"] for item in ratios.values())
    if closed:
        next_validation = "stage5-internal-integrated-validation-and-freeze"
    else:
        first = max((name for name in ratios if not ratios[name]["passed"]), key=lambda name: ratios[name]["ratio"] - ratios[name]["maximum_ratio"])
        next_validation = f"decompose-first-dominant-product-root:{first}"
    return {"dimensions": ratios, "quality_protections_passed": True, "root_status": "closed" if closed else "open", "next_validation": next_validation}


def _aggregate_subject(results: dict[str, dict[str, Any]], runs: Path, evidence_output: Path) -> dict[str, Any]:
    totals = {"semantic_input_tokens": 0, "end_to_end_wall_seconds": 0.0, "ownward_data_bytes": 0}
    storage = {"authority_asset_bytes": 0, "control_state_bytes": 0, "derived_index_state_bytes": 0, "runtime_state_bytes": 0, "test_and_evidence_bytes": 0}
    phases = {name: 0.0 for name in ("create", "semantic", "retrieval", "reader", "judge", "other")}
    calls = {"semantic": 0, "reader": 0, "judge": 0}
    plans = {}
    for name, result in results.items():
        _require(result.get("passed") is True and result.get("formal") is False, f"{name} 结果没有通过非正式门")
        observation = result["observation"]
        totals["semantic_input_tokens"] += int(observation["resources"]["semantic_input_tokens"])
        totals["end_to_end_wall_seconds"] += float(observation["latency"]["wall_seconds"])
        totals["ownward_data_bytes"] += int(observation["resources"]["ownward_data_bytes"])
        plan = str(result["plan_identity"])
        run_root = runs / "kernel-iteration" / plan / "run"
        measured = _measure_run(run_root)
        _require(measured["ownward_data_bytes"] == int(observation["resources"]["ownward_data_bytes"]), f"{name} 产品数据字节与运行报告不一致")
        for key in storage:
            storage[key] += measured[key]
        for key in phases:
            phases[key] += measured["phase_seconds"][key]
        for key in calls:
            calls[key] += measured["calls"][key]
        plans[name] = {"plan_identity": plan, "run_root_sha256": measured["run_root_sha256"], "questions": measured["questions"]}
    storage["test_and_evidence_bytes"] += _tree_bytes(evidence_output)
    return {**totals, "storage_breakdown": storage, "phase_seconds_sum": phases, "calls": calls, "plans": plans}


def _measure_run(run_root: Path) -> dict[str, Any]:
    _require(run_root.is_dir(), f"端到端运行目录缺失: {run_root}")
    storage = {"authority_asset_bytes": 0, "control_state_bytes": 0, "derived_index_state_bytes": 0, "runtime_state_bytes": 0, "test_and_evidence_bytes": 0}
    phases = {name: 0.0 for name in ("create", "semantic", "retrieval", "reader", "judge", "other")}
    calls = {"semantic": 0, "reader": 0, "judge": 0}
    question_roots = sorted((run_root / "questions").glob("*"))
    for question in question_roots:
        if not question.is_dir():
            continue
        data_root = question / "ownward-data"
        for path in data_root.rglob("*"):
            if not path.is_file():
                continue
            relative = path.relative_to(data_root)
            size = path.stat().st_size
            if relative.parts[0] == "assets" and path.name != ".ownward.lock":
                storage["authority_asset_bytes"] += size
            elif relative.parts[0] == "authority":
                storage["control_state_bytes"] += size
            elif relative.parts[0] == "state":
                storage["derived_index_state_bytes"] += size
            else:
                storage["runtime_state_bytes"] += size
        result = _load_json(question / "result.json")
        for name in phases:
            phases[name] += float(result["phase_seconds"].get(name, 0.0))
        for name in calls:
            calls[name] += int(result["usage"][name]["calls"])
    product = storage["authority_asset_bytes"] + storage["control_state_bytes"] + storage["derived_index_state_bytes"]
    storage["test_and_evidence_bytes"] = _tree_bytes(run_root) - product - storage["runtime_state_bytes"]
    return {
        **storage,
        "ownward_data_bytes": product,
        "phase_seconds": phases,
        "calls": calls,
        "questions": len(question_roots),
        "run_root_sha256": _tree_identity(run_root),
    }


def _verify_same_scale(v0: dict[str, dict[str, Any]], v2: dict[str, dict[str, Any]]) -> None:
    for name in ("development", "regression"):
        left, right = v0[name], v2[name]
        _require(left["input_identity"] == right["input_identity"], f"{name} V0/V2 输入不同尺")
        _require(left["shared_conditions"] == right["shared_conditions"], f"{name} V0/V2 共享条件不同尺")
        _require(left["direct_dependencies"] == right["direct_dependencies"], f"{name} V0/V2 执行或观察依赖不同尺")


def _baseline_execution_config(source: Path, binary: Path, embedding: Path) -> dict[str, Any]:
    value = _load_json(source)
    candidate = dict(value["candidate"])
    candidate["binary"] = str(binary)
    candidate["embedding_bundle_dir"] = str(embedding)
    candidate["commit"] = "99f519018df99bd5202b0c571b8e43481cd1b80e"
    return {**value, "candidate": candidate}


def load_contract(suite_root: Path) -> dict[str, Any]:
    value = _load_json(suite_root / "iteration" / "v2" / CONTRACT_NAME)
    _validate_identity(value, CONTRACT_SCHEMA, "资源成本合同")
    _require(value.get("frozen_before_measurement") is True and value.get("resource_results_seen") is False, "资源成本门槛没有在结果前冻结")
    repository = suite_root.parents[2]
    for item in value["direct_dependencies"]:
        _verify_file(repository / item["path"], item)
    _require(value["gates"] == {
        "semantic_input_tokens": {"maximum_ratio": 0.5},
        "end_to_end_wall_seconds": {"maximum_ratio": 0.5},
        "ownward_data_bytes": {"maximum_ratio": 0.5},
    }, "资源成本三维减半门发生漂移")
    return value


def _verify_file(path: Path, item: dict[str, Any]) -> None:
    _require(path.is_file() and evidence.file_sha256(path) == item["sha256"], f"资源成本直接依赖漂移: {item['path']}")


def _tree_bytes(root: Path) -> int:
    return sum(path.stat().st_size for path in root.rglob("*") if path.is_file()) if root.exists() else 0


def _tree_identity(root: Path) -> str:
    entries = [{"path": path.relative_to(root).as_posix(), "size": path.stat().st_size, "sha256": evidence.file_sha256(path)} for path in sorted(root.rglob("*")) if path.is_file()]
    return evidence.canonical_sha256(entries)


def _validate_identity(value: dict[str, Any], schema: str, name: str) -> None:
    _require(value.get("schema") == schema, f"{name} schema 无效")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), f"{name}身份漂移")


def _load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise validation.KernelIterationValidationError(f"无法读取资源成本制品 {path}: {error}") from error
    _require(isinstance(value, dict), f"资源成本制品不是对象: {path}")
    return value


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise validation.KernelIterationValidationError(message)
