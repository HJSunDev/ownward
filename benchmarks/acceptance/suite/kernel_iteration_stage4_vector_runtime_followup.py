from __future__ import annotations

from concurrent.futures import ThreadPoolExecutor
from contextlib import ExitStack
import json
import math
from pathlib import Path
import statistics
import threading
import time
from typing import Any

import kernel_iteration_evidence as evidence
import kernel_iteration_stage4_vector_runtime as vector_runtime
import kernel_iteration_validation as validation


CONTRACT_SCHEMA = "ownward.kernel-iteration-stage4-vector-runtime-followup-contract/v1"
RESULT_SCHEMA = "ownward.kernel-iteration-stage4-vector-runtime-followup-result/v1"


def run(suite_root: Path, bundle_root: Path, output_path: Path, formal_state: Path) -> dict[str, Any]:
    suite_root = suite_root.resolve()
    repository = suite_root.parents[2]
    bundle_root, output_path, formal_state = bundle_root.resolve(), output_path.resolve(), formal_state.resolve()
    _require(output_path.is_relative_to(repository / ".tmp"), "向量后续校准只能写入非正式 .tmp 边界")
    _require(not output_path.exists(), "向量后续校准结果已存在；禁止选择性覆盖")
    contract = _load_contract(suite_root)
    calibration = _load_json(repository / contract["prior_calibration_path"])
    _require(calibration.get("identity") == contract["prior_calibration_identity"], "向量后续校准错绑既有校准")
    materials = _load_json(suite_root / "iteration" / "v2" / "stage4-retrieval-latency-real-scale-materials.json")
    _require(materials.get("identity") == contract["materials_identity"], "向量后续校准材料漂移")
    manifest_path = bundle_root / "manifest.json"
    manifest = _load_json(manifest_path)
    runtime = bundle_root / str(manifest["runtime"]["entry"])
    model = bundle_root / str(manifest["model"]["path"])
    _require(evidence.file_sha256(manifest_path) == contract["embedding_manifest_sha256"], "向量后续校准模型清单漂移")
    queries = [str(item["query"]) for item in materials["cases"]]
    _require(len(queries) == 4, "向量后续校准必须使用四个真实规模独立查询")
    state_before = evidence.file_sha256(formal_state)
    _require(state_before == contract["formal_state_sha256"], "向量后续校准前正式 state 漂移")

    reference: dict[str, list[float]]
    with vector_runtime._Server(runtime, model, 6, 2) as server:
        reference = {query: server.vector(query) for query in queries}

    results = []
    for trial in contract["trials"]:
        result = _trial(runtime, model, queries, trial)
        result_vectors = result.pop("vectors")
        result["maximum_vector_component_drift"] = max(
            abs(actual - expected)
            for query in queries
            for actual, expected in zip(result_vectors[query], reference[query])
        )
        results.append(result)

    _require(evidence.file_sha256(formal_state) == state_before, "向量后续校准改写了正式 state")
    gates = contract["gates"]
    best = min(results, key=lambda item: (float(item["p95_ms"]), float(item["mean_ms"])))
    isolated = next(item for item in results if item["name"] == "isolated-selected-6-2")
    exact_lower_bound_exceeds_total_mean_gate = float(isolated["mean_ms"]) > float(gates["retrieval_mean_ms_maximum"])
    content = {
        "schema": RESULT_SCHEMA,
        "formal": False,
        "formal_state_written": False,
        "model_or_vector_space_changed": False,
        "contract_identity": contract["identity"],
        "prior_calibration_identity": calibration["identity"],
        "runtime_sha256": evidence.file_sha256(runtime),
        "model_sha256": evidence.file_sha256(model),
        "embedding_manifest_sha256": evidence.file_sha256(manifest_path),
        "formal_state_sha256_before": state_before,
        "formal_state_sha256_after": evidence.file_sha256(formal_state),
        "trials": results,
        "best_trial": best["name"],
        "isolated_selected_runtime_mean_ms": isolated["mean_ms"],
        "isolated_exact_query_lower_bound_exceeds_total_mean_gate": exact_lower_bound_exceeds_total_mean_gate,
        "all_vectors_exact": all(float(item["maximum_vector_component_drift"]) <= float(gates["maximum_vector_component_drift"]) for item in results),
        "root_status": "open",
        "conclusion": "bounded slot and batch scheduling cannot satisfy the frozen total retrieval gate while exact-query inference alone exceeds its mean boundary",
        "next_validation": "a-future-runtime-must-reduce-the-same-model-exact-inference-lower-bound-before-any-candidate-quality-rerun",
    }
    value = {**content, "identity": evidence.canonical_sha256(content)}
    evidence.atomic_json(output_path, value)
    return {**value, "path": str(output_path)}


def _trial(runtime: Path, model: Path, queries: list[str], trial: dict[str, Any]) -> dict[str, Any]:
    servers = int(trial["servers"])
    active = int(trial["active_requests"])
    threads = int(trial["threads"])
    parallel = int(trial["parallel"])
    batch_size = int(trial["batch_size"])
    repetitions = int(trial["repetitions"])
    _require(1 <= active <= 4 and 1 <= servers <= 4 and active <= servers, "向量后续校准槽位无效")
    values: list[float] = []
    vectors: dict[str, list[float]] = {}
    with ExitStack() as stack:
        opened = [stack.enter_context(vector_runtime._Server(runtime, model, threads, parallel)) for _ in range(servers)]
        for index, query in enumerate(queries):
            vectors[query] = opened[index % servers].vector(query)
        if batch_size > 1:
            _require(servers == 1 and active == 1 and batch_size == len(queries), "向量批处理校准边界无效")
            body = {"input": ["task: search result | query: " + query for query in queries], "model": "embeddinggemma"}
            opened[0]._post(body)
            for _ in range(repetitions):
                started = time.perf_counter()
                payload = opened[0]._post(body)
                values.append((time.perf_counter() - started) * 1000)
                _require(len(payload.get("data", [])) == len(queries), "向量批处理返回数量无效")
        else:
            for _ in range(repetitions):
                barrier = threading.Barrier(active)

                def timed(index: int) -> float:
                    barrier.wait(timeout=30)
                    started = time.perf_counter()
                    opened[index].vector(queries[index])
                    return (time.perf_counter() - started) * 1000

                with ThreadPoolExecutor(max_workers=active) as pool:
                    values.extend(pool.map(timed, range(active)))
        peak = sum(server.peak_working_set_bytes for server in opened)
    summary = _summary(values)
    return {
        "name": str(trial["name"]),
        "servers": servers,
        "active_requests": active,
        "threads": threads,
        "parallel": parallel,
        "batch_size": batch_size,
        "samples": len(values),
        "mean_ms": summary["mean_ms"],
        "p95_ms": summary["p95_ms"],
        "max_ms": summary["max_ms"],
        "per_query_throughput_ms": summary["mean_ms"] / batch_size,
        "peak_working_set_bytes": peak,
        "vectors": vectors,
    }


def _summary(values: list[float]) -> dict[str, float]:
    _require(values, "向量后续校准没有样本")
    ordered = sorted(values)
    return {
        "mean_ms": statistics.fmean(values),
        "p95_ms": ordered[min(len(ordered) - 1, math.ceil(len(ordered) * 0.95) - 1)],
        "max_ms": ordered[-1],
    }


def _load_contract(suite_root: Path) -> dict[str, Any]:
    value = _load_json(suite_root / "iteration" / "v2" / "stage4-retrieval-latency-runtime-followup-contract.json")
    _require(value.get("schema") == CONTRACT_SCHEMA, "向量后续校准合同 schema 无效")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), "向量后续校准合同身份漂移")
    _require(value.get("controller_sha256") == evidence.file_sha256(Path(__file__).resolve()), "向量后续校准控制器漂移")
    _require(value.get("frozen_before_measurement") is True and value.get("candidate_results_seen") is False, "向量后续校准合同未在结果前冻结")
    return value


def _load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise validation.KernelIterationValidationError(f"无法读取向量后续校准输入 {path}: {error}") from error
    _require(isinstance(value, dict), f"向量后续校准输入不是对象: {path}")
    return value


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise validation.KernelIterationValidationError(message)
