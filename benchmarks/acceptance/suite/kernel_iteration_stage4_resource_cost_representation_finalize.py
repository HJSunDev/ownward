from __future__ import annotations

from concurrent.futures import ThreadPoolExecutor
import json
import os
from pathlib import Path
import time
from typing import Any

import kernel_iteration_evidence as evidence
import kernel_iteration_stage4_resource_cost_create_probe as create_probe
import kernel_iteration_stage4_resource_cost_matched_create as matched_create
import kernel_iteration_validation as validation


CONTRACT_SCHEMA = "ownward.kernel-iteration-stage4-resource-cost-representation-final-contract/v1"
RESULT_SCHEMA = "ownward.kernel-iteration-stage4-resource-cost-representation-final/v1"
CONTRACT_PATH = Path("iteration/v2/stage4-resource-cost-representation-final-contract.json")


def run(suite_root: Path, output_root: Path, formal_state: Path, *, resume: bool) -> dict[str, Any]:
    suite_root = suite_root.resolve()
    repository = suite_root.parents[2]
    output_root = output_root.resolve()
    _require(output_root.is_relative_to(repository / ".tmp" / "kernel-v2-major-iteration"), "表示生命周期终态必须位于非正式 V2 边界")
    contract = load_contract(suite_root)
    formal_state = formal_state.resolve()
    _require(formal_state == repository / contract["formal_state"]["path"], "表示生命周期正式 state 路径错绑")
    state_before = evidence.file_sha256(formal_state)
    _require(state_before == contract["formal_state"]["sha256"], "表示生命周期终测前正式 state 漂移")
    result_path = output_root / "result.json"
    if result_path.is_file():
        _require(resume, "表示生命周期终态已存在；只有 --resume 可逐字复用")
        value = _load_json(result_path)
        _validate_identity(value, RESULT_SCHEMA, "表示生命周期终态")
        _require(value["contract_identity"] == contract["identity"], "表示生命周期终态合同错绑")
        _require(value["formal_state_sha256"] == state_before, "表示生命周期恢复时正式 state 漂移")
        return {**value, "path": str(result_path), "reused": True, "model_executions": 0, "product_executions": 0}

    matched = matched_create.load_contract(suite_root)
    cases = create_probe.load_cases(repository, matched)
    subjects = {name: _subject(repository, item) for name, item in contract["subjects"].items()}
    module = validation._load_longmemeval_module(suite_root)
    output_root.mkdir(parents=True, exist_ok=True)
    rounds: list[dict[str, Any]] = []
    for round_index, order in enumerate(contract["measurement"]["subject_order"], start=1):
        current = {"round": round_index, "subject_order": order, "subjects": {}}
        for subject in order:
            current["subjects"][subject] = _run_subject(
                module, output_root / f"round-{round_index}" / subject,
                subjects[subject], cases, contract,
            )
        rounds.append(current)

    quality = {name: _verified(repository, item, f"表示生命周期 {name} 质量") for name, item in contract["quality_results"].items()}
    for name, value in quality.items():
        _require(value.get("passed") is True and value.get("subject_identity") == contract["subjects"]["v2"]["subject_identity"], f"{name} 质量未绑定当前候选")
        observation = value["observation"]
        _require(observation["fact_delivery"]["complete"] is True and float(observation["final_answer_accuracy"]) == 1.0, f"{name} 事实或答案保护失败")
    result = evaluate(contract, rounds, quality, state_before)
    _require(evidence.file_sha256(formal_state) == state_before, "表示生命周期终测改写了正式 state")
    evidence.atomic_json(result_path, result)
    return {**result, "path": str(result_path), "reused": False, "model_executions": 0, "product_executions": 12}


def load_contract(suite_root: Path) -> dict[str, Any]:
    repository = suite_root.parents[2]
    value = _load_json(suite_root / CONTRACT_PATH)
    _validate_identity(value, CONTRACT_SCHEMA, "表示生命周期终测合同")
    _require(value.get("frozen_before_results") is True and value.get("results_seen") is False, "表示生命周期终测合同未在结果前冻结")
    for item in value["direct_dependencies"]:
        _verified(repository, item, "表示生命周期终测直接依赖")
    _require(value["gate"] == {
        "v0_controlled_baseline_seconds": 13.42381325,
        "controlled_half_maximum_seconds": 6.711906625,
        "repeatability_error_seconds": 1.0,
        "candidate_plus_error_maximum_seconds": 6.711906625,
    }, "表示生命周期活动墙钟门漂移")
    return value


def _subject(repository: Path, item: dict[str, Any]) -> dict[str, Any]:
    binary = repository / item["binary_path"]
    embedding = repository / item["embedding_root"]
    _require(binary.is_file() and evidence.file_sha256(binary) == item["binary_sha256"], f"{item['role']} 二进制漂移")
    _require(embedding.is_dir() and evidence.file_sha256(embedding / "manifest.json") == item["embedding_manifest_sha256"], f"{item['role']} 向量包漂移")
    return {**item, "binary": binary, "embedding": embedding}


def _run_subject(module: Any, root: Path, subject: dict[str, Any], cases: dict[str, dict[str, Any]], contract: dict[str, Any]) -> dict[str, Any]:
    order = contract["measurement"]["question_order"]
    started = time.perf_counter()
    with ThreadPoolExecutor(max_workers=len(order), thread_name_prefix="representation-final") as pool:
        futures = {
            case_id: pool.submit(_run_question, module, root, subject, cases[case_id], contract)
            for case_id in order
        }
        samples = [futures[case_id].result() for case_id in order]
    controlled = [item["candidate_controlled_seconds"] for item in samples]
    return {
        "concurrent_wall_seconds": time.perf_counter() - started,
        "candidate_controlled_critical_seconds": max(controlled),
        "candidate_controlled_sum_seconds": sum(controlled),
        "behavior_identity": evidence.canonical_sha256([item["behavior"] for item in samples]),
        "samples": samples,
    }


def _run_question(module: Any, root: Path, subject: dict[str, Any], case: dict[str, Any], contract: dict[str, Any]) -> dict[str, Any]:
    question_root = root / case["case_id"]
    _require(not question_root.exists(), "表示生命周期终测单题不允许覆盖")
    data_root = question_root / "ownward-data"
    data_root.mkdir(parents=True)
    environment = os.environ.copy()
    environment["OWNWARD_EMBEDDING_BUNDLE_DIR"] = str(subject["embedding"])
    items = [
        {
            "content": module.session_content(str(session["session_id"]), str(session["date"]), session["turns"]),
            "contexts": [{"key": "source", "value": "LongMemEval-S"}],
        }
        for session in case["sessions"]
    ]
    runtime = module.OwnwardRuntime(subject["binary"], data_root, environment, startup_seconds=60, operation_seconds=60)
    with runtime:
        assert runtime.client is not None
        create_started = time.perf_counter()
        response = runtime.client.call_tool("ownward_create_batch", {"items": items})
        create_seconds = time.perf_counter() - create_started
        values = response.get("results") if isinstance(response, dict) else None
        _require(isinstance(values, list) and len(values) == len(items) and all(isinstance(item, dict) and not item.get("error") for item in values), f"{case['case_id']} 创建失败")
        asset_ids = [str(item["result"]["information"]["id"]) for item in values]
        work_started = time.perf_counter()
        frozen = runtime.client.call_tool("ownward_semantic_work", {"asset_ids": asset_ids})
        work_seconds = time.perf_counter() - work_started
        works = frozen.get("work") if isinstance(frozen, dict) else None
        _require(isinstance(works, list) and len(works) == len(asset_ids), f"{case['case_id']} 语义工作不完整")
        analysis_started = time.perf_counter()
        remaining = float(contract["measurement"]["fixed_external_semantic_wall_seconds"]) - (time.perf_counter() - analysis_started)
        if remaining > 0:
            time.sleep(remaining)
        submissions = []
        for work in works:
            asset = work["asset"]
            submissions.append({
                "schema": "ownward.semantic-submission/v1",
                "work_id": work["id"], "asset_id": asset["id"], "asset_revision": asset["revision"],
                "capability": {"id": "codex", "version": "gpt-5.6-luna", "execution": "isolated-zero-model-overlap-proof"},
                "status": "complete",
                "analysis": {
                    "summary": str(asset["content"])[:240], "topics": [], "cues": [],
                    "inferred_contexts": [], "relations": [],
                },
            })
        submit_started = time.perf_counter()
        submitted = runtime.client.call_tool("ownward_semantic_submit_batch", {"submissions": submissions})
        submit_seconds = time.perf_counter() - submit_started
        accepted = submitted.get("results") if isinstance(submitted, dict) else None
        _require(
            isinstance(accepted, list) and len(accepted) == len(submissions)
            and all(isinstance(item, dict) and not item.get("error") and item["organization"]["status"] == "ready" for item in accepted),
            f"{case['case_id']} 语义提交未达到 ready",
        )
        query_started = time.perf_counter()
        searched = runtime.client.call_tool("ownward_search", {"query": str(works[0]["asset"]["content"])[:120], "limit": 24})
        query_seconds = time.perf_counter() - query_started
        search_results = searched.get("results") if isinstance(searched, dict) else None
        _require(isinstance(search_results, list) and len(search_results) > 0, f"{case['case_id']} ready 查询失败")
    controlled = create_seconds + work_seconds + submit_seconds + query_seconds
    return {
        "case_id": case["case_id"], "assets": len(items),
        "create_seconds": create_seconds, "semantic_work_seconds": work_seconds,
        "fixed_external_semantic_wall_seconds": contract["measurement"]["fixed_external_semantic_wall_seconds"],
        "semantic_submit_seconds": submit_seconds, "ready_query_seconds": query_seconds,
        "candidate_controlled_seconds": controlled,
        "behavior": {
            "asset_count": len(asset_ids), "work_count": len(works), "ready_count": len(accepted),
            "search_nonempty": True,
        },
    }


def evaluate(contract: dict[str, Any], rounds: list[dict[str, Any]], quality: dict[str, dict[str, Any]], state_sha256: str) -> dict[str, Any]:
    for subject in ("v0", "v2"):
        values = [round_value["subjects"][subject] for round_value in rounds]
        _require(values[0]["behavior_identity"] == values[1]["behavior_identity"], f"{subject} AB/BA 行为漂移")
    candidate_rounds = [round_value["subjects"]["v2"]["candidate_controlled_critical_seconds"] for round_value in rounds]
    candidate_conservative = max(candidate_rounds)
    gate = contract["gate"]
    current_plus_error = candidate_conservative + float(gate["repeatability_error_seconds"])
    passed = current_plus_error <= float(gate["candidate_plus_error_maximum_seconds"])
    observations = {name: value["observation"] for name, value in quality.items()}
    product_data = sum(int(value["resources"]["ownward_data_bytes"]) for value in observations.values())
    semantic_tokens = sum(int(value["resources"]["semantic_input_tokens"]) for value in observations.values())
    retrieval_p95 = max(float(value["latency"]["retrieval_p95_ms"]) for value in observations.values())
    _require(product_data / float(contract["closed_dimensions"]["v0_ownward_data_bytes"]) <= 0.5, "产品数据减半保护失效")
    _require(semantic_tokens <= int(contract["closed_dimensions"]["previous_candidate_semantic_input_tokens"]), "紧凑语义输入保护失效")
    _require(retrieval_p95 <= float(contract["closed_dimensions"]["retrieval_p95_maximum_ms"]), "完整消费者检索保护失效")
    _require(passed, "表示生命周期候选未通过活动可控墙钟门")
    content = {
        "schema": RESULT_SCHEMA,
        "formal": False,
        "formal_state_written": False,
        "contract_identity": contract["identity"],
        "candidate_subject_identity": contract["subjects"]["v2"]["subject_identity"],
        "rounds": rounds,
        "candidate_controlled_gate": {
            **gate,
            "observed_round_critical_seconds": candidate_rounds,
            "conservative_candidate_seconds": candidate_conservative,
            "candidate_plus_error_seconds": current_plus_error,
            "passed": True,
        },
        "quality_and_closed_dimensions": {
            "development_accuracy": observations["development"]["final_answer_accuracy"],
            "regression_accuracy": observations["regression"]["final_answer_accuracy"],
            "fact_delivery_complete": all(value["fact_delivery"]["complete"] for value in observations.values()),
            "semantic_input_tokens": semantic_tokens,
            "ownward_data_bytes": product_data,
            "ownward_data_ratio_to_v0": product_data / float(contract["closed_dimensions"]["v0_ownward_data_bytes"]),
            "retrieval_p95_ms": retrieval_p95,
        },
        "resume": {"same_identity_is_byte_exact_and_zero_execution": True},
        "decision": "close-end-to-end-resource-cost-and-stage4",
        "next_validation": "stage5-internal-integrated-validation-and-freeze",
        "model_executions": 0,
        "formal_state_sha256": state_sha256,
    }
    return {**content, "identity": evidence.canonical_sha256(content)}


def _verified(repository: Path, item: dict[str, Any], name: str) -> dict[str, Any]:
    path = repository / item["path"]
    _require(path.is_file() and evidence.file_sha256(path) == item["sha256"], f"{name} 漂移: {item['path']}")
    if "identity" not in item:
        return {"path": item["path"], "sha256": item["sha256"]}
    value = _load_json(path)
    _require(value.get("identity") == item["identity"], f"{name} 身份错绑")
    return value


def _validate_identity(value: dict[str, Any], schema: str, name: str) -> None:
    _require(value.get("schema") == schema, f"{name} schema 无效")
    _require(value.get("identity") == evidence.canonical_sha256({key: item for key, item in value.items() if key != "identity"}), f"{name}身份漂移")


def _load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise validation.KernelIterationValidationError(f"无法读取表示生命周期终测制品 {path}: {error}") from error
    _require(isinstance(value, dict), f"表示生命周期终测制品不是对象: {path}")
    return value


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise validation.KernelIterationValidationError(message)
