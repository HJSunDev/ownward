from __future__ import annotations

from concurrent.futures import ThreadPoolExecutor
import json
import math
import os
from pathlib import Path
import threading
import time
from typing import Any

import kernel_iteration_evidence as evidence
import kernel_iteration_longmemeval as longmemeval
import kernel_iteration_stage4_latency_data as latency_data
import kernel_iteration_validation as validation


CONTRACT_SCHEMA = "ownward.kernel-iteration-stage4-retrieval-latency-contract/v1"
RESULT_SCHEMA = "ownward.kernel-iteration-stage4-retrieval-latency-diagnosis/v1"


def run(
    suite_root: Path,
    output_root: Path,
    current_subject_manifest: Path,
    current_execution_config: Path,
    v0_binary: Path,
    v0_embedding: Path,
    preparation_receipt: Path,
    formal_state: Path,
) -> dict[str, Any]:
    suite_root, output_root = suite_root.resolve(), output_root.resolve()
    contract = load_contract(suite_root)
    comparison = evidence.load_contract(suite_root)
    subject = evidence.validate_v2_subject(comparison, _load_json(current_subject_manifest.resolve()))
    _require(subject["identity"] == contract["subjects"]["current-v2"], "检索时延当前 V2 subject 错绑")
    runtime = validation.validate_execution_config(suite_root, current_execution_config.resolve())
    v0_binary, v0_embedding = v0_binary.resolve(), v0_embedding.resolve()
    _require(evidence.file_sha256(runtime["binary"]) == contract["binaries"]["current-v2"], "当前 V2 二进制漂移")
    _require(evidence.file_sha256(v0_binary) == contract["binaries"]["v0"], "V0 二进制漂移")
    _require(evidence.file_sha256(runtime["protocol"]) == contract["protocol_sha256"], "检索协议漂移")
    for name, bundle in (("current-v2", runtime["embedding"]), ("v0", v0_embedding)):
        _require(evidence.file_sha256(bundle / "manifest.json") == contract["embedding_manifest_sha256"][name], f"{name} 向量制品漂移")

    receipt = _load_json(preparation_receipt.resolve())
    _validate_artifact_identity(receipt, latency_data.RECEIPT_SCHEMA, "检索时延 prepared-data 收据")
    _require(receipt["identity"] == contract["preparation_identity"], "检索时延 prepared-data 收据错绑")
    roots = {name: Path(receipt["subject_roots"][name]).resolve() for name in ("v0", "current-v2")}
    materials = latency_data.load_materials(suite_root)
    cases = {str(item["case_id"]): str(item["query"]) for item in materials["cases"]}
    _require(len(cases) == int(contract["schedule"]["workers"]), "检索时延材料与冻结并发不同尺")
    before = {
        name: _data_identities(roots[name], cases, contract["prepared_data_sha256"][name])
        for name in ("v0", "current-v2")
    }
    state_path = formal_state.resolve()
    state_before = evidence.file_sha256(state_path)
    _require(state_before == contract["formal_state_sha256"], "检索时延诊断前正式 state 漂移")

    runtimes = {
        "v0": {"binary": v0_binary, "embedding": v0_embedding, "protocol_value": runtime["protocol_value"]},
        "current-v2": runtime,
    }
    samples: dict[str, list[dict[str, Any]]] = {"v0": [], "current-v2": []}
    for order in contract["schedule"]["balanced_order"]:
        _require(sorted(order) == ["current-v2", "v0"], "检索时延平衡顺序无效")
        for name in order:
            samples[name].extend(_run_round(name, runtimes[name], roots[name], cases, contract["schedule"]))

    state_after = evidence.file_sha256(state_path)
    _require(state_after == state_before, "检索时延诊断改写了正式 state")
    after = {
        name: _data_identities(roots[name], cases, contract["prepared_data_sha256"][name])
        for name in ("v0", "current-v2")
    }
    _require(before == after, "只读检索时延诊断改写了 prepared data")
    metrics = evaluate(samples, contract["gates"])
    content = {
        "schema": RESULT_SCHEMA,
        "formal": False,
        "formal_state_written": False,
        "model_calls": 0,
        "answer_generation_calls": 0,
        "product_mutations": 0,
        "contract_identity": contract["identity"],
        "subjects": dict(contract["subjects"]),
        "binaries": dict(contract["binaries"]),
        "prepared_data_sha256_before": before,
        "prepared_data_sha256_after": after,
        "formal_state_sha256_before": state_before,
        "formal_state_sha256_after": state_after,
        "schedule": dict(contract["schedule"]),
        "metrics": metrics,
        "root_status": metrics["root_status"],
        "first_amplified_stage": metrics["first_amplified_stage"],
        "candidate_route_allowed": metrics["root_status"] == "proven",
    }
    value = {**content, "identity": evidence.canonical_sha256(content)}
    destination = output_root / "diagnosis.json"
    _require(not destination.exists(), "检索时延诊断证据已经存在；禁止随机重跑")
    evidence.atomic_json(destination, value)
    return {**value, "path": str(destination)}


def load_contract(suite_root: Path) -> dict[str, Any]:
    path = suite_root.resolve() / "iteration" / "v2" / "stage4-retrieval-latency-contract.json"
    value = _load_json(path)
    _validate_artifact_identity(value, CONTRACT_SCHEMA, "检索时延冻结合同")
    _require(value.get("frozen_before_paired_measurement") is True, "检索时延合同没有在结果前冻结")
    _require(value.get("model_or_answer_execution") is False and value.get("random_rerun_allowed") is False, "检索时延合同允许模型或随机重跑")
    _require(evidence.file_sha256(Path(__file__).resolve()) == value.get("controller_sha256"), "检索时延控制器漂移")
    materials = latency_data.load_materials(suite_root)
    _require(materials["identity"] == value.get("materials_identity"), "检索时延合同材料漂移")
    return value


def evaluate(samples: dict[str, list[dict[str, Any]]], gates: dict[str, Any]) -> dict[str, Any]:
    summaries: dict[str, dict[str, Any]] = {}
    for name in ("v0", "current-v2"):
        values = samples.get(name)
        _require(isinstance(values, list) and values, f"{name} 缺少检索时延样本")
        traces: dict[str, set[str]] = {}
        for item in values:
            _require(isinstance(item, dict) and float(item.get("wall_ms", -1)) >= 0, f"{name} 检索时延样本无效")
            traces.setdefault(str(item["case_identity"]), set()).add(str(item["trace_sha256"]))
        _require(all(len(identities) == 1 for identities in traces.values()), f"{name} 检索选择轨迹不确定")
        summaries[name] = _summarize(values, len(traces))
    repeat_error = float(gates["paired_repeat_error_ms"])
    stage_deltas = {
        stage: summaries["current-v2"][f"{stage}_p95_ms"] - summaries["v0"][f"{stage}_p95_ms"]
        for stage in ("search", "evidence_search", "read", "protocol_overhead")
    }
    amplified = [stage for stage in ("search", "evidence_search", "read", "protocol_overhead") if stage_deltas[stage] > repeat_error]
    first = amplified[0] if amplified else None
    total_delta = summaries["current-v2"]["p95_ms"] - summaries["v0"]["p95_ms"]
    contribution = stage_deltas[first] / total_delta if first is not None and total_delta > 0 else 0.0
    root_status = "proven" if first is not None and contribution >= float(gates["minimum_first_stage_delta_contribution"]) else "not-proven"
    return {
        **summaries,
        "current_v2_minus_v0_p95_ms": total_delta,
        "stage_p95_deltas_ms": stage_deltas,
        "paired_repeat_error_ms": repeat_error,
        "first_amplified_stage": first,
        "first_stage_delta_contribution": contribution,
        "root_status": root_status,
        "absolute_contract_gates": {
            "retrieval_mean_ms_maximum": float(gates["retrieval_mean_ms_maximum"]),
            "retrieval_p95_ms_maximum": float(gates["retrieval_p95_ms_maximum"]),
        },
        "current_v2_meets_absolute_contract": (
            summaries["current-v2"]["mean_ms"] <= float(gates["retrieval_mean_ms_maximum"])
            and summaries["current-v2"]["p95_ms"] <= float(gates["retrieval_p95_ms_maximum"])
        ),
    }


def _summarize(values: list[dict[str, Any]], cases: int) -> dict[str, Any]:
    result: dict[str, Any] = {"samples": len(values), "cases": cases, "stable_case_traces": True}
    retrieval = sorted(
        float(item["search_ms"]) + float(item["evidence_search_ms"]) + float(item["read_ms"])
        for item in values
    )
    result["mean_ms"] = sum(retrieval) / len(retrieval)
    result["p95_ms"] = _p95(retrieval)
    result["max_ms"] = max(retrieval)
    for output, field in (
        ("wall_", "wall_ms"), ("search_", "search_ms"), ("evidence_search_", "evidence_search_ms"),
        ("read_", "read_ms"), ("protocol_overhead_", "protocol_overhead_ms"),
    ):
        numbers = sorted(float(item[field]) for item in values)
        result[f"{output}mean_ms"] = sum(numbers) / len(numbers)
        result[f"{output}p95_ms"] = _p95(numbers)
        result[f"{output}max_ms"] = max(numbers)
    for field in ("search_calls", "evidence_search_calls", "read_calls", "returned_sources", "evidence_probes", "read_units", "context_chars"):
        numbers = [int(item[field]) for item in values]
        result[f"{field}_mean"] = sum(numbers) / len(numbers)
        result[f"{field}_max"] = max(numbers)
    return result


def _run_round(name: str, runtime: dict[str, Any], root: Path, cases: dict[str, str], schedule: dict[str, Any]) -> list[dict[str, Any]]:
    warmups = int(schedule["warmups_per_round"])
    repetitions = int(schedule["measured_repetitions_per_round"])
    barrier = threading.Barrier(len(cases))

    def worker(case_id: str, question: str) -> list[dict[str, Any]]:
        data_dir = root / case_id / "ownward-data"
        _require(data_dir.is_dir(), f"检索时延缺少 prepared data: {name}/{case_id}")
        environment = os.environ.copy()
        environment["OWNWARD_EMBEDDING_BUNDLE_DIR"] = str(runtime["embedding"])
        result: list[dict[str, Any]] = []
        with longmemeval.adapter.OwnwardRuntime(
            runtime["binary"], data_dir, environment, startup_seconds=60,
            operation_seconds=float(runtime["protocol_value"]["retrieval"]["query_timeout_seconds"]),
        ) as service:
            for index in range(warmups + repetitions):
                barrier.wait(timeout=60)
                calls: list[dict[str, Any]] = []
                original = service.client
                _require(original is not None, "Ownward 检索客户端不可用")
                service.client = _TimingClient(original, calls)
                started = time.perf_counter()
                try:
                    if name == "v0":
                        _, trace = _retrieve_v0(service, question, runtime["protocol_value"])
                    else:
                        _, trace = longmemeval.adapter.retrieve(service, question, runtime["protocol_value"])
                finally:
                    service.client = original
                wall_ms = (time.perf_counter() - started) * 1000
                if index < warmups:
                    continue
                tool_ms = sum(float(item["elapsed_ms"]) for item in calls)
                selection = [
                    {
                        "source_rank": step.get("source_rank"), "mode": step.get("mode"), "depth": step.get("depth"),
                        "selected": step.get("selected"), "reason": step.get("reason"),
                    }
                    for step in trace.get("selection_steps", [])
                ]
                counts = {tool: sum(1 for item in calls if item["tool"] == tool) for tool in {
                    "ownward_search", "ownward_evidence_search", "ownward_read", "ownward_evidence_read",
                }}
                result.append({
                    "case_identity": evidence.canonical_sha256({"case_id": case_id}),
                    "trace_sha256": evidence.canonical_sha256({
                        "returned": [item.get("id") for item in trace.get("returned", [])],
                        "selection": selection,
                        "read_ids": trace.get("read_ids", []),
                        "evidence_read_ids": trace.get("evidence_read_ids", []),
                        "context_chars": trace.get("context_chars"),
                    }),
                    "wall_ms": wall_ms,
                    "search_ms": sum(float(item["elapsed_ms"]) for item in calls if item["tool"] == "ownward_search"),
                    "evidence_search_ms": sum(float(item["elapsed_ms"]) for item in calls if item["tool"] == "ownward_evidence_search"),
                    "read_ms": sum(float(item["elapsed_ms"]) for item in calls if item["tool"] in {"ownward_read", "ownward_evidence_read"}),
                    "protocol_overhead_ms": max(0.0, wall_ms - tool_ms),
                    "search_calls": counts.get("ownward_search", 0),
                    "evidence_search_calls": counts.get("ownward_evidence_search", 0),
                    "read_calls": counts.get("ownward_read", 0) + counts.get("ownward_evidence_read", 0),
                    "returned_sources": len(trace.get("returned", [])),
                    "evidence_probes": counts.get("ownward_evidence_search", 0),
                    "read_units": len(trace.get("evidence_read_ids", [])) + sum(1 for item in trace.get("read_paths", []) if item.get("mode") == "full"),
                    "context_chars": int(trace.get("context_chars", 0)),
                })
        return result

    with ThreadPoolExecutor(max_workers=len(cases)) as pool:
        futures = [pool.submit(worker, case_id, question) for case_id, question in sorted(cases.items())]
        return [item for future in futures for item in future.result()]


class _TimingClient:
    def __init__(self, client: Any, calls: list[dict[str, Any]]) -> None:
        self._client = client
        self._calls = calls

    def call_tool(self, name: str, arguments: dict[str, Any]) -> Any:
        started = time.perf_counter()
        try:
            return self._client.call_tool(name, arguments)
        finally:
            self._calls.append({"tool": name, "elapsed_ms": (time.perf_counter() - started) * 1000})


def _retrieve_v0(runtime: Any, question: str, protocol: dict[str, Any]) -> tuple[list[dict[str, Any]], dict[str, Any]]:
    _require(runtime.client is not None, "V0 Ownward 客户端不可用")
    settings = protocol["retrieval"]
    search = runtime.client.call_tool("ownward_search", {"query": question, "limit": settings["search_limit"]})
    results = search.get("results") if isinstance(search, dict) else None
    _require(isinstance(results, list), "V0 Search 没有返回结果列表")
    observed = [
        {"id": item["id"], "score": item.get("score"), "signals": item.get("signals", [])}
        for item in results if isinstance(item, dict) and isinstance(item.get("id"), str)
    ]
    selected: list[dict[str, Any]] = []
    read_ids: list[str] = []
    read_paths: list[dict[str, Any]] = []
    steps: list[dict[str, Any]] = []
    used_chars = 0
    for rank, item in enumerate(observed):
        if len(selected) >= int(settings["read_limit"]):
            break
        read = runtime.client.call_tool("ownward_read", {"id": item["id"]})
        information = read.get("information") if isinstance(read, dict) else None
        _require(isinstance(information, dict) and isinstance(information.get("content"), str), "V0 Read 返回无效")
        content = str(information["content"])
        step = {"source_id": item["id"], "source_rank": rank, "mode": "full", "depth": 0, "content_runes": len(content)}
        if used_chars + len(content) > int(settings["context_max_chars"]):
            steps.append({**step, "selected": False, "reason": "context_budget"})
            continue
        selected.append({"id": information["id"], "content": content})
        read_ids.append(item["id"])
        read_paths.append({"source_id": item["id"], "mode": "full", "evidence_ids": []})
        steps.append({**step, "selected": True})
        used_chars += len(content)
    _require(selected, "V0 检索没有可读证据")
    return selected, {
        "returned": observed, "read_ids": read_ids, "evidence_read_ids": [], "read_paths": read_paths,
        "context_chars": used_chars, "selection_steps": steps,
    }


def _data_identities(root: Path, cases: dict[str, str], expected: dict[str, str]) -> dict[str, str]:
    actual = {case_id: _tree_sha256(root / case_id / "ownward-data") for case_id in sorted(cases)}
    _require(actual == expected, "检索时延 prepared data 身份漂移")
    return actual


def _tree_sha256(root: Path) -> str:
    _require(root.is_dir(), f"检索时延数据目录不存在: {root}")
    manifest = []
    for path in sorted(root.rglob("*")):
        if path.is_file() and not path.is_symlink() and path.name != ".ownward.lock":
            manifest.append({"path": path.relative_to(root).as_posix(), "size": path.stat().st_size, "sha256": evidence.file_sha256(path)})
    return evidence.canonical_sha256(manifest)


def _p95(values: list[float]) -> float:
    return values[min(len(values) - 1, math.ceil(len(values) * 0.95) - 1)]


def _validate_artifact_identity(value: dict[str, Any], schema: str, name: str) -> None:
    _require(value.get("schema") == schema, f"{name} schema 无效")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), f"{name}身份漂移")


def _load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise validation.KernelIterationValidationError(f"无法读取检索时延制品 {path}: {error}") from error
    _require(isinstance(value, dict), f"检索时延制品不是对象: {path}")
    return value


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise validation.KernelIterationValidationError(message)
