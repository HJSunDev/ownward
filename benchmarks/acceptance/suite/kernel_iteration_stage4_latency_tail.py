from __future__ import annotations

from concurrent.futures import ThreadPoolExecutor
import json
import math
import os
from pathlib import Path
import statistics
import threading
import time
from typing import Any

import kernel_iteration_evidence as evidence
import kernel_iteration_longmemeval as longmemeval
import kernel_iteration_stage4_latency_real_scale as real_scale
import kernel_iteration_stage4_vector_runtime as vector_runtime
import kernel_iteration_validation as validation


CONTRACT_SCHEMA = "ownward.kernel-iteration-stage4-retrieval-latency-tail-contract/v1"
RESULT_SCHEMA = "ownward.kernel-iteration-stage4-retrieval-latency-tail-result/v1"


def run(
    suite_root: Path,
    output_path: Path,
    execution_config_path: Path,
    persistent_root: Path,
    formal_state: Path,
) -> dict[str, Any]:
    suite_root, output_path = suite_root.resolve(), output_path.resolve()
    repository = suite_root.parents[2]
    _require(output_path.is_relative_to(repository / ".tmp"), "尾部探针只能写入非正式 .tmp 边界")
    _require(not output_path.exists(), "尾部探针结果已存在；禁止选择性覆盖")
    contract = _load_contract(suite_root)
    runtime = validation.validate_execution_config(suite_root, execution_config_path.resolve())
    state_path = formal_state.resolve()
    state_before = evidence.file_sha256(state_path)
    _require(state_before == contract["formal_state_sha256"], "尾部探针前正式 state 漂移")

    prior = _load_json(repository / contract["prior_real_scale_result_path"])
    preparation = _load_json(repository / contract["preparation_path"])
    receipt = _load_json(repository / contract["runtime_receipt_path"])
    _require(prior.get("identity") == contract["prior_real_scale_result_identity"], "尾部探针错绑既有同尺结果")
    _require(preparation.get("identity") == contract["preparation_identity"], "尾部探针错绑 prepared data")
    _require(receipt.get("identity") == contract["runtime_receipt_identity"], "尾部探针错绑运行制品")
    _require(evidence.file_sha256(runtime["protocol"]) == contract["protocol_sha256"], "尾部探针协议漂移")
    _require(evidence.file_sha256(runtime["embedding"] / "manifest.json") == contract["embedding_manifest_sha256"], "尾部探针向量包漂移")

    materials = real_scale.load_materials(suite_root)
    _require(materials["identity"] == contract["materials_identity"], "尾部探针材料漂移")
    binary = Path(receipt["binary_paths"]["candidate"]).resolve()
    _require(evidence.file_sha256(binary) == receipt["binary_sha256"]["candidate"] == contract["candidate_binary_sha256"], "尾部探针候选二进制漂移")
    prepared_root = Path(preparation["subject_roots"]["candidate"]).resolve()
    before = real_scale._data_identities(prepared_root, materials)
    _require(before == preparation["prepared_data_sha256"]["candidate"], "尾部探针 prepared data 漂移")

    samples = _measure_tail(
        binary,
        runtime,
        prepared_root,
        materials,
        preparation["target_asset_ids"]["candidate"],
        contract["schedule"],
    )
    after = real_scale._data_identities(prepared_root, materials)
    _require(after == before, "尾部探针改写了 prepared data")
    shared = _probe_shared_exact_vector(runtime["embedding"], materials, prior, contract)
    tail = _summarize_tail(samples, contract)

    required_gain = float(contract["gates"]["observed_gap_ms"]) + float(contract["gates"]["additional_engineering_margin_ms"])
    shared_gain = float(prior["vector_profile"]["formal_four_worker_exact_embed_query"]["p95_ms"]) - float(shared["p95_ms"])
    evidence_gain = float(tail["current_p95_ms"]) - float(tail["ideal_evidence_parallel_p95_ms"])
    shared["p95_gain_over_four_independent_processes_ms"] = shared_gain
    shared["vector_drift_within_contract"] = float(shared["maximum_vector_component_drift"]) <= float(contract["gates"]["maximum_vector_component_drift"])
    shared["meets_required_isolated_gain"] = shared_gain >= required_gain and shared["vector_drift_within_contract"]
    tail["ideal_evidence_parallel_p95_gain_ms"] = evidence_gain
    tail["meets_required_isolated_gain"] = evidence_gain >= required_gain
    winner = None
    if shared["meets_required_isolated_gain"] or tail["meets_required_isolated_gain"]:
        winner = "shared-exact-vector" if shared_gain >= evidence_gain else "bounded-evidence-parallelism"

    _require(evidence.file_sha256(state_path) == state_before, "尾部探针改写了正式 state")
    content = {
        "schema": RESULT_SCHEMA,
        "formal": False,
        "formal_state_written": False,
        "codex_calls": 0,
        "answer_generation_calls": 0,
        "contract_identity": contract["identity"],
        "prior_real_scale_result_identity": prior["identity"],
        "preparation_identity": preparation["identity"],
        "runtime_receipt_identity": receipt["identity"],
        "candidate_binary_sha256": evidence.file_sha256(binary),
        "execution_identity": {
            "controller_sha256": evidence.file_sha256(Path(__file__).resolve()),
            "transport_sha256": evidence.file_sha256(repository / "benchmarks" / "support" / "ownward_mcp.py"),
            "protocol_sha256": evidence.file_sha256(runtime["protocol"]),
            "embedding_manifest_sha256": evidence.file_sha256(runtime["embedding"] / "manifest.json"),
        },
        "prepared_data_sha256_before": before,
        "prepared_data_sha256_after": after,
        "formal_state_sha256_before": state_before,
        "formal_state_sha256_after": evidence.file_sha256(state_path),
        "required_isolated_gain_ms": required_gain,
        "same_request_tail": tail,
        "shared_exact_vector_probe": shared,
        "selected_route": winner,
        "root_status": "feasible" if winner is not None else "rejected",
        "next_validation": "implement-only-the-selected-product-capability-route" if winner is not None else "preserve-rejection-evidence-without-a-third-route",
    }
    value = {**content, "identity": evidence.canonical_sha256(content)}
    evidence.atomic_json(output_path, value)
    return {**value, "path": str(output_path)}


def _measure_tail(
    binary: Path,
    runtime: dict[str, Any],
    root: Path,
    materials: dict[str, Any],
    target_ids: dict[str, str],
    schedule: dict[str, Any],
) -> list[dict[str, Any]]:
    cases = {str(item["case_id"]): item for item in materials["cases"]}
    barrier = threading.Barrier(len(cases))

    def worker(case_id: str, case: dict[str, Any]) -> list[dict[str, Any]]:
        environment = os.environ.copy()
        environment["OWNWARD_EMBEDDING_BUNDLE_DIR"] = str(runtime["embedding"])
        values: list[dict[str, Any]] = []
        data_dir = root / case_id / "ownward-data"
        with longmemeval.adapter.OwnwardRuntime(binary, data_dir, environment, startup_seconds=90, operation_seconds=60) as service:
            repetitions = int(schedule["warmups"]) + int(schedule["measured_repetitions"])
            for index in range(repetitions):
                barrier.wait(timeout=90)
                calls: list[dict[str, Any]] = []
                original = service.client
                _require(original is not None, "尾部探针 Ownward 客户端不可用")
                service.client = _TimingClient(original, calls)
                started = time.perf_counter()
                try:
                    _, trace = longmemeval.adapter.retrieve(service, str(case["query"]), runtime["protocol_value"])
                finally:
                    service.client = original
                wall_ms = (time.perf_counter() - started) * 1000
                real_scale._validate_target_trace(trace, target_ids[case_id])
                if index < int(schedule["warmups"]):
                    continue
                values.append({
                    "case_id": case_id,
                    "wall_ms": wall_ms,
                    "trace_sha256": real_scale._trace_sha256(trace),
                    "calls": calls,
                    "search_ms": _sum_calls(calls, {"ownward_search"}),
                    "evidence_search_ms": _sum_calls(calls, {"ownward_evidence_search"}),
                    "read_ms": _sum_calls(calls, {"ownward_read", "ownward_evidence_read"}),
                    "context_chars": int(trace.get("context_chars", 0)),
                    "returned_sources": len(trace.get("returned", [])),
                    "read_units": len(trace.get("evidence_read_ids", [])) + sum(1 for item in trace.get("read_paths", []) if item.get("mode") == "full"),
                })
        return values

    with ThreadPoolExecutor(max_workers=len(cases)) as pool:
        futures = [pool.submit(worker, case_id, case) for case_id, case in sorted(cases.items())]
        return [item for future in futures for item in future.result()]


class _TimingClient:
    def __init__(self, client: Any, calls: list[dict[str, Any]]) -> None:
        self.client, self.calls = client, calls

    def call_tool(self, name: str, arguments: dict[str, Any]) -> Any:
        started = time.perf_counter()
        try:
            return self.client.call_tool(name, arguments)
        finally:
            self.calls.append({"tool": name, "elapsed_ms": (time.perf_counter() - started) * 1000})


def _probe_shared_exact_vector(bundle_root: Path, materials: dict[str, Any], prior: dict[str, Any], contract: dict[str, Any]) -> dict[str, Any]:
    manifest = _load_json(bundle_root / "manifest.json")
    runtime = bundle_root / str(manifest["runtime"]["entry"])
    model = bundle_root / str(manifest["model"]["path"])
    queries = [str(item["query"]) for item in materials["cases"]]
    values: list[float] = []
    references: dict[str, list[float]] = {}
    hashes: dict[str, str] = {}
    maximum_drift = 0.0
    round_completion: list[float] = []
    with vector_runtime._Server(runtime, model, 6, 2) as server:
        for query in queries:
            vector = server.vector(query)
            references[query] = vector
            hashes[query] = vector_runtime._vector_sha256(vector)
        for _ in range(int(contract["shared_vector_probe"]["repetitions"])):
            barrier = threading.Barrier(len(queries))

            def timed(index: int) -> tuple[float, float]:
                barrier.wait(timeout=30)
                started = time.perf_counter()
                vector = server.vector(queries[index])
                drift = max(abs(actual - expected) for actual, expected in zip(vector, references[queries[index]]))
                return (time.perf_counter() - started) * 1000, drift

            with ThreadPoolExecutor(max_workers=len(queries)) as pool:
                measured = list(pool.map(timed, range(len(queries))))
            round_values = [item[0] for item in measured]
            maximum_drift = max(maximum_drift, *(item[1] for item in measured))
            values.extend(round_values)
            round_completion.append(max(round_values))
        peak = server.peak_working_set_bytes
    expected = prior["vector_profile"]["vector_sha256"]
    _require(hashes == expected, "共享精确向量与既有同尺向量不逐字一致")
    return {
        "service_count": 1,
        "threads": 6,
        "threads_batch": 6,
        "parallel": 2,
        "concurrent_requests": 4,
        "samples": len(values),
        "mean_ms": statistics.fmean(values),
        "p95_ms": _p95(values),
        "max_ms": max(values),
        "round_completion_p95_ms": _p95(round_completion),
        "peak_working_set_bytes": peak,
        "maximum_vector_component_drift": maximum_drift,
        "vector_sha256": hashes,
    }


def _summarize_tail(samples: list[dict[str, Any]], contract: dict[str, Any]) -> dict[str, Any]:
    _require(samples, "尾部探针没有请求样本")
    rows: list[dict[str, Any]] = []
    for sample in samples:
        search = float(sample["search_ms"])
        evidence_calls = [float(item["elapsed_ms"]) for item in sample["calls"] if item["tool"] == "ownward_evidence_search"]
        read_calls = [float(item["elapsed_ms"]) for item in sample["calls"] if item["tool"] in {"ownward_read", "ownward_evidence_read"}]
        sequential = search + sum(evidence_calls) + sum(read_calls)
        reorder_started = time.perf_counter_ns()
        ordered = list(enumerate(evidence_calls + read_calls))
        ordered.sort(key=lambda item: item[0])
        reorder_ms = (time.perf_counter_ns() - reorder_started) / 1_000_000
        ideal_evidence = (max(evidence_calls) if evidence_calls else 0.0) + (max(read_calls) if read_calls else 0.0) + reorder_ms
        rows.append({
            "case_id": sample["case_id"],
            "retrieval_ms": sequential,
            "search_ms": search,
            "evidence_search_ms": sum(evidence_calls),
            "read_ms": sum(read_calls),
            "protocol_overhead_ms": max(0.0, float(sample["wall_ms"]) - sequential),
            "ideal_evidence_parallel_ms": ideal_evidence,
            "ideal_total_ms": search + ideal_evidence,
            "evidence_search_call_ms": evidence_calls,
            "read_call_ms": read_calls,
            "reorder_ms": reorder_ms,
            "trace_sha256": sample["trace_sha256"],
            "context_chars": sample["context_chars"],
            "returned_sources": sample["returned_sources"],
            "read_units": sample["read_units"],
        })
    decisive = sorted(rows, key=lambda item: float(item["retrieval_ms"]))[max(0, math.ceil(len(rows) * 0.95) - 1)]
    traces: dict[str, set[str]] = {}
    for row in rows:
        traces.setdefault(str(row["case_id"]), set()).add(str(row["trace_sha256"]))
    return {
        "samples": len(rows),
        "current_mean_ms": statistics.fmean(float(item["retrieval_ms"]) for item in rows),
        "current_p95_ms": _p95([float(item["retrieval_ms"]) for item in rows]),
        "ideal_evidence_parallel_p95_ms": _p95([float(item["ideal_total_ms"]) for item in rows]),
        "decisive_p95_request": decisive,
        "stable_trace_per_case": all(len(values) == 1 for values in traces.values()),
        "quality_byte_equivalence_required": contract["gates"]["quality_trace_byte_equivalent"],
        "read_units_max": max(int(item["read_units"]) for item in rows),
        "context_chars_max": max(int(item["context_chars"]) for item in rows),
    }


def _load_contract(suite_root: Path) -> dict[str, Any]:
    path = suite_root / "iteration" / "v2" / "stage4-retrieval-latency-tail-contract.json"
    value = _load_json(path)
    _require(value.get("schema") == CONTRACT_SCHEMA, "尾部合同 schema 无效")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), "尾部合同身份漂移")
    _require(value.get("controller_sha256") == evidence.file_sha256(Path(__file__).resolve()), "尾部探针控制器漂移")
    _require(value.get("frozen_before_measurement") is True and value.get("results_seen") is False, "尾部合同未在结果前冻结")
    return value


def _sum_calls(calls: list[dict[str, Any]], names: set[str]) -> float:
    return sum(float(item["elapsed_ms"]) for item in calls if item["tool"] in names)


def _p95(values: list[float]) -> float:
    ordered = sorted(values)
    return ordered[min(len(ordered) - 1, math.ceil(len(ordered) * 0.95) - 1)]


def _load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise validation.KernelIterationValidationError(f"无法读取尾部探针输入 {path}: {error}") from error
    _require(isinstance(value, dict), f"尾部探针输入不是对象: {path}")
    return value


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise validation.KernelIterationValidationError(message)
