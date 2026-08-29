from __future__ import annotations

from concurrent.futures import ThreadPoolExecutor
from contextlib import ExitStack
import json
import math
import os
from pathlib import Path
import statistics
import threading
import time
from typing import Any

import psutil

import kernel_iteration_evidence as evidence
import kernel_iteration_longmemeval as longmemeval
import kernel_iteration_stage4_vector_runtime as vector_runtime
import kernel_iteration_validation as validation


MATERIALS_SCHEMA = "ownward.kernel-iteration-stage4-retrieval-latency-real-scale-materials/v1"
CONTRACT_SCHEMA = "ownward.kernel-iteration-stage4-retrieval-latency-real-scale-contract/v1"
PREPARATION_SCHEMA = "ownward.kernel-iteration-stage4-retrieval-latency-real-scale-preparation/v1"
RESULT_SCHEMA = "ownward.kernel-iteration-stage4-retrieval-latency-real-scale-result/v1"


def run(
    suite_root: Path,
    output_root: Path,
    execution_config_path: Path,
    runtime_receipt_path: Path,
    persistent_root: Path,
    formal_state: Path,
    *,
    resume: bool = False,
) -> dict[str, Any]:
    suite_root, output_root = suite_root.resolve(), output_root.resolve()
    repository = suite_root.parents[2]
    _require(output_root.is_relative_to(repository / ".tmp"), "真实规模检索证据只能写入非正式 .tmp 边界")
    contract = load_contract(suite_root)
    materials = load_materials(suite_root)
    runtime = validation.validate_execution_config(suite_root, execution_config_path.resolve())
    receipt = _load_json(runtime_receipt_path.resolve())
    _validate_identity(receipt, "ownward.kernel-iteration-stage4-shared-vector-runtime/v1", "共享运行时收据")
    _require(receipt["identity"] == contract["shared_runtime_receipt_identity"], "共享运行时收据错绑")
    _require(receipt["runtime_configuration"] == contract["runtime_configuration"], "共享运行时配置漂移")
    _require(evidence.file_sha256(runtime["protocol"]) == contract["protocol_sha256"], "真实规模检索协议漂移")
    _require(evidence.file_sha256(runtime["embedding"] / "manifest.json") == contract["embedding_manifest_sha256"], "真实规模向量清单漂移")
    state_path = formal_state.resolve()
    state_before = evidence.file_sha256(state_path)
    _require(state_before == contract["formal_state_sha256"], "真实规模检索前正式 state 漂移")

    result_path = output_root / "result.json"
    if result_path.is_file():
        _require(resume, "真实规模检索结果已存在；禁止随机重跑")
        value = _load_json(result_path)
        _validate_identity(value, RESULT_SCHEMA, "真实规模检索结果")
        _require(value.get("contract_identity") == contract["identity"], "真实规模检索结果合同漂移")
        _require(evidence.file_sha256(state_path) == value.get("formal_state_sha256_after"), "真实规模恢复时正式 state 漂移")
        return {**value, "path": str(result_path), "reused": True, "product_executions": 0, "vector_executions": 0}

    output_root.mkdir(parents=True, exist_ok=True)
    persistent_root = persistent_root.resolve()
    _require(
        "kernel-iteration" in persistent_root.parts
        and any(part.startswith("retrieval-latency-real-scale") for part in persistent_root.parts),
        "真实规模 prepared data 越过非正式持久边界",
    )
    binaries = {name: Path(path).resolve() for name, path in receipt["binary_paths"].items()}
    _require(
        receipt["binary_sha256"] == {name: evidence.file_sha256(path) for name, path in binaries.items()},
        "共享运行时比较二进制漂移",
    )
    _require(receipt["binary_sha256"] == contract["binary_sha256"], "真实规模合同二进制错绑")
    preparation = _prepare_or_resume(
        output_root / "preparation.json", persistent_root, binaries, runtime, materials, contract, state_path,
        resume=resume,
    )

    state_after_preparation = evidence.file_sha256(state_path)
    _require(state_after_preparation == state_before, "真实规模准备改写了正式 state")
    vector_profile = _profile_vectors(runtime["embedding"], materials, contract)
    roots = {name: Path(path) for name, path in preparation["subject_roots"].items()}
    target_ids = preparation["target_asset_ids"]
    before = {name: _data_identities(roots[name], materials) for name in binaries}
    samples: dict[str, list[dict[str, Any]]] = {name: [] for name in binaries}
    for order in contract["schedule"]["balanced_order"]:
        _require(sorted(order) == sorted(binaries), "真实规模三代平衡顺序无效")
        for name in order:
            samples[name].extend(_run_round(
                name, binaries[name], runtime, roots[name], materials, target_ids[name], contract["schedule"],
            ))

    after = {name: _data_identities(roots[name], materials) for name in binaries}
    _require(before == after, "真实规模只读测量改写了 prepared data")
    state_after = evidence.file_sha256(state_path)
    _require(state_after == state_before, "真实规模测量改写了正式 state")
    metrics = _evaluate(samples, vector_profile, contract)
    content = {
        "schema": RESULT_SCHEMA,
        "formal": False,
        "formal_state_written": False,
        "model_or_vector_space_changed": False,
        "codex_calls": 0,
        "answer_generation_calls": 0,
        "product_mutations_during_measurement": 0,
        "contract_identity": contract["identity"],
        "materials_identity": materials["identity"],
        "shared_runtime_receipt_identity": receipt["identity"],
        "preparation_identity": preparation["identity"],
        "subjects": dict(contract["subjects"]),
        "binary_sha256": dict(receipt["binary_sha256"]),
        "runtime_configuration": dict(contract["runtime_configuration"]),
        "execution_identity": {
            "controller_sha256": evidence.file_sha256(Path(__file__).resolve()),
            "transport_sha256": evidence.file_sha256(repository / "benchmarks" / "support" / "ownward_mcp.py"),
            "protocol_sha256": evidence.file_sha256(runtime["protocol"]),
            "embedding_manifest_sha256": evidence.file_sha256(runtime["embedding"] / "manifest.json"),
        },
        "prepared_data_sha256_before": before,
        "prepared_data_sha256_after": after,
        "formal_state_sha256_before": state_before,
        "formal_state_sha256_after": state_after,
        "schedule": dict(contract["schedule"]),
        "vector_profile": vector_profile,
        "metrics": metrics,
        "root_status": metrics["root_status"],
        "next_validation": metrics["next_validation"],
    }
    value = {**content, "identity": evidence.canonical_sha256(content)}
    evidence.atomic_json(result_path, value)
    return {**value, "path": str(result_path), "reused": False, "product_executions": sum(len(items) for items in samples.values()), "vector_executions": vector_profile["requests"]}


def load_materials(suite_root: Path) -> dict[str, Any]:
    value = _load_json(suite_root / "iteration" / "v2" / "stage4-retrieval-latency-real-scale-materials.json")
    _validate_identity(value, MATERIALS_SCHEMA, "真实规模检索材料")
    _require(value.get("contains_formal_questions_answers_gold_content_outputs_or_case_ids") is False, "真实规模材料接触正式内容")
    generation = _mapping(value, "generation")
    _require(generation.get("formal_observed_asset_range") == [38, 62], "真实规模资产范围漂移")
    _require(generation.get("search_limit") == 24 and generation.get("read_limit") == 8 and generation.get("context_max_chars") == 24000, "真实规模协议预算漂移")
    cases = value.get("cases")
    _require(isinstance(cases, list) and [item.get("asset_count") for item in cases] == [38, 46, 54, 62], "真实规模案例没有覆盖 38—62 资产")
    return value


def load_contract(suite_root: Path) -> dict[str, Any]:
    value = _load_json(suite_root / "iteration" / "v2" / "stage4-retrieval-latency-real-scale-contract.json")
    _validate_identity(value, CONTRACT_SCHEMA, "真实规模检索合同")
    _require(value.get("frozen_before_measurement") is True and value.get("candidate_results_seen") is False, "真实规模门槛未在结果前冻结")
    _require(value.get("controller_sha256") == evidence.file_sha256(Path(__file__).resolve()), "真实规模控制器漂移")
    _require(value.get("materials_identity") == load_materials(suite_root)["identity"], "真实规模材料错绑")
    return value


def expanded_sources(materials: dict[str, Any], case: dict[str, Any]) -> list[dict[str, str]]:
    generation = _mapping(materials, "generation")
    result = []
    for index in range(1, int(case["asset_count"]) + 1):
        headline = str(case["target_headline"]) if index == int(case["target_index"]) else str(case["decoy_headline"]).format(index=index)
        repeat = int(case["repeat_base"]) + (index % 5) * 2
        result.append({
            "source_id": f"{case['case_id']}-s{index:02d}",
            "headline": headline,
            "content": headline + "\n" + str(case["filler"]) * repeat,
            "actor": str(generation["source_actor"]),
        })
    return result


def _prepare_or_resume(
    receipt_path: Path,
    persistent_root: Path,
    binaries: dict[str, Path],
    runtime: dict[str, Any],
    materials: dict[str, Any],
    contract: dict[str, Any],
    state_path: Path,
    *,
    resume: bool,
) -> dict[str, Any]:
    roots = {name: persistent_root / name for name in binaries}
    if receipt_path.is_file():
        _require(resume, "真实规模 prepared data 已存在；必须显式恢复")
        value = _load_json(receipt_path)
        _validate_identity(value, PREPARATION_SCHEMA, "真实规模准备收据")
        _require(value.get("materials_identity") == materials["identity"] and value.get("binary_sha256") == contract["binary_sha256"], "真实规模准备收据错绑")
        _require(value.get("prepared_data_sha256") == {name: _data_identities(roots[name], materials) for name in binaries}, "真实规模 prepared data 漂移")
        _require(value.get("formal_state_sha256_before") == value.get("formal_state_sha256_after") == evidence.file_sha256(state_path), "真实规模准备恢复时正式 state 漂移")
        return value
    _require(not receipt_path.exists() and not any(root.exists() for root in roots.values()), "真实规模 prepared data 现场不完整；禁止覆盖")
    state_before = evidence.file_sha256(state_path)
    target_ids: dict[str, dict[str, str]] = {name: {} for name in binaries}
    authority_identities: dict[str, dict[str, str]] = {name: {} for name in binaries}
    for name, binary in binaries.items():
        for case in materials["cases"]:
            data_dir = roots[name] / str(case["case_id"]) / "ownward-data"
            target_ids[name][str(case["case_id"])] = _prepare_case(binary, runtime, data_dir, materials, case)
            authority_identities[name][str(case["case_id"])] = _authority_sha256(data_dir)
    baseline_authority = next(iter(authority_identities.values()))
    _require(all(value == baseline_authority for value in authority_identities.values()), "三代真实规模权威事实不同尺")
    state_after = evidence.file_sha256(state_path)
    _require(state_after == state_before, "真实规模准备改写了正式 state")
    content = {
        "schema": PREPARATION_SCHEMA,
        "formal": False,
        "formal_state_written": False,
        "materials_identity": materials["identity"],
        "binary_sha256": dict(contract["binary_sha256"]),
        "subject_roots": {name: str(root) for name, root in roots.items()},
        "target_asset_ids": target_ids,
        "prepared_data_sha256": {name: _data_identities(root, materials) for name, root in roots.items()},
        "materialized_authority_sha256": authority_identities,
        "formal_state_sha256_before": state_before,
        "formal_state_sha256_after": state_after,
    }
    value = {**content, "identity": evidence.canonical_sha256(content)}
    evidence.atomic_json(receipt_path, value)
    return value


def _prepare_case(binary: Path, runtime: dict[str, Any], data_dir: Path, materials: dict[str, Any], case: dict[str, Any]) -> str:
    environment = os.environ.copy()
    environment["OWNWARD_EMBEDDING_BUNDLE_DIR"] = str(runtime["embedding"])
    sources = expanded_sources(materials, case)
    data_dir.parent.mkdir(parents=True, exist_ok=True)
    asset_ids: list[str] = []
    with longmemeval.adapter.OwnwardRuntime(binary, data_dir, environment, startup_seconds=90, operation_seconds=240) as service:
        for start in range(0, len(sources), 20):
            chunk = sources[start:start + 20]
            created = service.client.call_tool("ownward_create_batch", {"items": [{
                "content": item["content"],
                "contexts": [dict(materials["generation"]["context"])],
                "source": {"actor": item["actor"], "ref": item["source_id"]},
            } for item in chunk]})
            values = created.get("results") if isinstance(created, dict) else None
            _require(isinstance(values, list) and len(values) == len(chunk), f"真实规模资产创建失败: {case['case_id']}")
            for value in values:
                mutation = value.get("result") if isinstance(value, dict) and not value.get("error") else None
                information = mutation.get("information") if isinstance(mutation, dict) else None
                _require(isinstance(information, dict) and isinstance(information.get("id"), str), "真实规模资产创建结果无效")
                asset_ids.append(str(information["id"]))
        source_by_asset = {asset_id: source for asset_id, source in zip(asset_ids, sources)}
        for start in range(0, len(asset_ids), 20):
            chunk_ids = asset_ids[start:start + 20]
            frozen = service.client.call_tool("ownward_semantic_work", {"asset_ids": chunk_ids})
            works = frozen.get("work") if isinstance(frozen, dict) else None
            _require(isinstance(works, list) and len(works) == len(chunk_ids), f"真实规模语义工作不完整: {case['case_id']}")
            submissions = []
            for work in works:
                asset = _mapping(work, "asset")
                source = source_by_asset.get(str(asset.get("id", "")))
                _require(source is not None, "真实规模语义工作资产错绑")
                submissions.append({
                    "schema": "ownward.semantic-submission/v1",
                    "work_id": work["id"], "asset_id": asset["id"], "asset_revision": asset["revision"],
                    "capability": {"id": "codex", "version": "gpt-5.6-luna", "execution": "longmemeval-s"},
                    "status": "complete",
                    "analysis": {"summary": source["headline"], "topics": [str(case["query"])[:120]], "cues": [], "inferred_contexts": [], "relations": []},
                })
            submitted = service.client.call_tool("ownward_semantic_submit_batch", {"submissions": submissions})
            results = submitted.get("results") if isinstance(submitted, dict) else None
            _require(isinstance(results, list) and len(results) == len(submissions) and all(isinstance(item, dict) and not item.get("error") for item in results), "真实规模语义提交失败")
    return asset_ids[int(case["target_index"]) - 1]


def _profile_vectors(bundle_root: Path, materials: dict[str, Any], contract: dict[str, Any]) -> dict[str, Any]:
    manifest = _load_json(bundle_root / "manifest.json")
    runtime = bundle_root / str(manifest["runtime"]["entry"])
    model = bundle_root / str(manifest["model"]["path"])
    queries = [str(case["query"]) for case in materials["cases"]]
    with vector_runtime._Server(runtime, model, 2, 1) as reference_server, vector_runtime._Server(runtime, model, 6, 2) as selected_server:
        reference = {query: reference_server.vector(query) for query in queries}
        actual = {query: selected_server.vector(query) for query in queries}
        drift = max(abs(left - right) for query in queries for left, right in zip(reference[query], actual[query]))
        selected_server.vector(queries[0])
        serial = vector_runtime._measure(lambda index: selected_server.vector(queries[index % len(queries)]), 12, workers=1)
    concurrent: list[float] = []
    with ExitStack() as stack:
        servers = [stack.enter_context(vector_runtime._Server(runtime, model, 6, 2)) for _ in queries]
        for server, query in zip(servers, queries):
            server.vector(query)
        for _ in range(3):
            barrier = threading.Barrier(len(queries))
            def timed(index: int) -> float:
                barrier.wait(timeout=30)
                started = time.perf_counter()
                servers[index].vector(queries[index])
                return (time.perf_counter() - started) * 1000
            with ThreadPoolExecutor(max_workers=len(queries)) as pool:
                concurrent.extend(pool.map(timed, range(len(queries))))
        peak = sum(server.peak_working_set_bytes for server in servers)
    serial_summary = _summary(serial)
    concurrent_summary = _summary(concurrent)
    return {
        "requests": len(reference) + len(actual) + len(serial) + len(concurrent) + len(queries) + 1,
        "exact_vector_maximum_component_drift": drift,
        "vector_sha256": {query: vector_runtime._vector_sha256(actual[query]) for query in queries},
        "single_runtime_exact_embed_query": serial_summary,
        "formal_four_worker_exact_embed_query": concurrent_summary,
        "managed_admission_queue_ms": 0.0,
        "managed_active_requests_per_service": 1,
        "cpu_contention_p95_ms": max(0.0, concurrent_summary["p95_ms"] - serial_summary["p95_ms"]),
        "four_worker_peak_working_set_bytes": peak,
        "runtime_configuration": dict(contract["runtime_configuration"]),
    }


def _run_round(
    name: str,
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
        result = []
        with longmemeval.adapter.OwnwardRuntime(binary, root / case_id / "ownward-data", environment, startup_seconds=90, operation_seconds=60) as service:
            for index in range(int(schedule["warmups_per_round"]) + int(schedule["measured_repetitions_per_round"])):
                barrier.wait(timeout=90)
                calls: list[dict[str, Any]] = []
                original = service.client
                _require(original is not None, "真实规模 Ownward 客户端不可用")
                service.client = _TimingClient(original, calls)
                started = time.perf_counter()
                try:
                    if name == "v0":
                        _, trace = _retrieve_v0(service, str(case["query"]), runtime["protocol_value"])
                    else:
                        _, trace = longmemeval.adapter.retrieve(service, str(case["query"]), runtime["protocol_value"])
                finally:
                    service.client = original
                wall_ms = (time.perf_counter() - started) * 1000
                _validate_target_trace(trace, target_ids[case_id])
                if index < int(schedule["warmups_per_round"]):
                    continue
                tool_ms = sum(float(item["elapsed_ms"]) for item in calls)
                memory = _process_tree_working_set(service.process.pid if service.process is not None else 0)
                result.append({
                    "case_id": case_id,
                    "asset_count": int(case["asset_count"]),
                    "trace_sha256": _trace_sha256(trace),
                    "wall_ms": wall_ms,
                    "search_ms": _tool_ms(calls, {"ownward_search"}),
                    "evidence_search_ms": _tool_ms(calls, {"ownward_evidence_search"}),
                    "read_ms": _tool_ms(calls, {"ownward_read", "ownward_evidence_read"}),
                    "protocol_overhead_ms": max(0.0, wall_ms - tool_ms),
                    "search_calls": _tool_count(calls, {"ownward_search"}),
                    "evidence_search_calls": _tool_count(calls, {"ownward_evidence_search"}),
                    "read_calls": _tool_count(calls, {"ownward_read", "ownward_evidence_read"}),
                    "returned_sources": len(trace.get("returned", [])),
                    "read_units": len(trace.get("evidence_read_ids", [])) + sum(1 for item in trace.get("read_paths", []) if item.get("mode") == "full"),
                    "context_chars": int(trace.get("context_chars", 0)),
                    "working_set_bytes": memory,
                })
        return result

    with ThreadPoolExecutor(max_workers=len(cases)) as pool:
        futures = [pool.submit(worker, case_id, case) for case_id, case in sorted(cases.items())]
        return [item for future in futures for item in future.result()]


def _evaluate(samples: dict[str, list[dict[str, Any]]], vector_profile: dict[str, Any], contract: dict[str, Any]) -> dict[str, Any]:
    summaries = {name: _summarize_samples(values) for name, values in samples.items()}
    gates = contract["gates"]
    candidate = summaries["candidate"]
    previous = summaries["previous-v2"]
    absolute = candidate["mean_ms"] <= float(gates["retrieval_mean_ms_maximum"]) and candidate["p95_ms"] <= float(gates["retrieval_p95_ms_maximum"])
    quality = all(summary["target_delivery_complete"] and summary["stable_selection_trace_per_case"] for summary in summaries.values())
    resources = all(summary["read_calls_max"] <= 8 and summary["context_chars_max"] <= 24000 for summary in summaries.values())
    drift = float(vector_profile["exact_vector_maximum_component_drift"]) <= float(gates["maximum_vector_component_drift"])
    improvement = previous["p95_ms"] - candidate["p95_ms"]
    root_status = "closed" if absolute and quality and resources and drift else "open"
    if not absolute:
        next_validation = "measure-first-dominant-stage-under-shared-runtime-and-test-only-the-minimum-same-model-runtime-or-evidence-change"
    elif improvement <= float(gates["paired_repeat_error_ms"]):
        next_validation = "candidate-specific-latency-amplification-not-proven-closed"
    else:
        next_validation = None
    return {
        **summaries,
        "candidate_meets_absolute_contract": absolute,
        "candidate_p95_improvement_over_previous_v2_ms": improvement,
        "quality_complete": quality,
        "resource_bounds_complete": resources,
        "vector_identity_complete": drift,
        "root_status": root_status,
        "next_validation": next_validation,
    }


def _summarize_samples(values: list[dict[str, Any]]) -> dict[str, Any]:
    _require(values, "真实规模检索样本为空")
    retrieval = [float(item["search_ms"]) + float(item["evidence_search_ms"]) + float(item["read_ms"]) for item in values]
    traces: dict[str, set[str]] = {}
    for item in values:
        traces.setdefault(str(item["case_id"]), set()).add(str(item["trace_sha256"]))
    result: dict[str, Any] = {
        "samples": len(values),
        "asset_count_range": [min(int(item["asset_count"]) for item in values), max(int(item["asset_count"]) for item in values)],
        "mean_ms": statistics.fmean(retrieval),
        "p95_ms": _p95(retrieval),
        "max_ms": max(retrieval),
        "target_delivery_complete": True,
        "stable_selection_trace_per_case": all(len(items) == 1 for items in traces.values()),
    }
    for output, field in (("wall", "wall_ms"), ("search", "search_ms"), ("evidence_search", "evidence_search_ms"), ("read", "read_ms"), ("protocol_overhead", "protocol_overhead_ms")):
        numbers = [float(item[field]) for item in values]
        result[f"{output}_mean_ms"] = statistics.fmean(numbers)
        result[f"{output}_p95_ms"] = _p95(numbers)
    for field in ("search_calls", "evidence_search_calls", "read_calls", "returned_sources", "read_units", "context_chars", "working_set_bytes"):
        numbers = [int(item[field]) for item in values]
        result[f"{field}_max"] = max(numbers)
        result[f"{field}_mean"] = statistics.fmean(numbers)
    return result


class _TimingClient:
    def __init__(self, client: Any, calls: list[dict[str, Any]]) -> None:
        self.client, self.calls = client, calls

    def call_tool(self, name: str, arguments: dict[str, Any]) -> Any:
        started = time.perf_counter()
        try:
            return self.client.call_tool(name, arguments)
        finally:
            self.calls.append({"tool": name, "elapsed_ms": (time.perf_counter() - started) * 1000})


def _retrieve_v0(runtime: Any, question: str, protocol: dict[str, Any]) -> tuple[list[dict[str, Any]], dict[str, Any]]:
    settings = protocol["retrieval"]
    search = runtime.client.call_tool("ownward_search", {"query": question, "limit": settings["search_limit"]})
    values = search.get("results") if isinstance(search, dict) else None
    _require(isinstance(values, list), "V0 Search 没有返回结果")
    observed = [{"id": item["id"], "score": item.get("score"), "signals": item.get("signals", [])} for item in values if isinstance(item, dict) and isinstance(item.get("id"), str)]
    selected, read_ids, read_paths, steps, used = [], [], [], [], 0
    for rank, item in enumerate(observed):
        if len(selected) >= int(settings["read_limit"]):
            break
        read = runtime.client.call_tool("ownward_read", {"id": item["id"]})
        information = read.get("information") if isinstance(read, dict) else None
        _require(isinstance(information, dict) and isinstance(information.get("content"), str), "V0 Read 返回无效")
        content = str(information["content"])
        if used + len(content) > int(settings["context_max_chars"]):
            steps.append({"source_id": item["id"], "source_rank": rank, "mode": "full", "depth": 0, "selected": False, "reason": "context_budget"})
            continue
        selected.append({"id": information["id"], "content": content})
        read_ids.append(item["id"])
        read_paths.append({"source_id": item["id"], "mode": "full", "evidence_ids": []})
        steps.append({"source_id": item["id"], "source_rank": rank, "mode": "full", "depth": 0, "selected": True})
        used += len(content)
    _require(selected, "V0 检索没有可读证据")
    return selected, {"returned": observed, "read_ids": read_ids, "evidence_read_ids": [], "read_paths": read_paths, "context_chars": used, "selection_steps": steps}


def _validate_target_trace(trace: dict[str, Any], target_id: str) -> None:
    returned = {str(item.get("id")) for item in trace.get("returned", []) if isinstance(item, dict)}
    read_sources = {str(item.get("source_id")) for item in trace.get("read_paths", []) if isinstance(item, dict)} | {str(item) for item in trace.get("read_ids", [])}
    _require(target_id in returned, "真实规模目标来源未被搜索返回")
    _require(target_id in read_sources, "真实规模目标来源未被读取")


def _trace_sha256(trace: dict[str, Any]) -> str:
    return evidence.canonical_sha256({
        "returned": [item.get("id") for item in trace.get("returned", [])],
        "selection": [{key: item.get(key) for key in ("source_rank", "mode", "depth", "selected", "reason")} for item in trace.get("selection_steps", [])],
        "read_ids": trace.get("read_ids", []), "evidence_read_ids": trace.get("evidence_read_ids", []),
        "context_chars": trace.get("context_chars"),
    })


def _data_identities(root: Path, materials: dict[str, Any]) -> dict[str, str]:
    return {str(case["case_id"]): _tree_sha256(root / str(case["case_id"]) / "ownward-data") for case in materials["cases"]}


def _tree_sha256(root: Path) -> str:
    _require(root.is_dir(), f"真实规模数据目录不存在: {root}")
    manifest = []
    for path in sorted(root.rglob("*")):
        if path.is_file() and not path.is_symlink() and path.name != ".ownward.lock":
            manifest.append({"path": path.relative_to(root).as_posix(), "size": path.stat().st_size, "sha256": evidence.file_sha256(path)})
    return evidence.canonical_sha256(manifest)


def _authority_sha256(root: Path) -> str:
    path = root / "assets" / "information.jsonl"
    _require(path.is_file(), "真实规模权威日志不存在")
    values = []
    for line in path.read_text(encoding="utf-8").splitlines():
        item = json.loads(line)
        value = item.get("value") if isinstance(item, dict) else None
        source = value.get("source") if isinstance(value, dict) else None
        _require(isinstance(value, dict) and isinstance(source, dict), "真实规模权威日志无效")
        values.append({"kind": value.get("kind"), "content": value.get("content"), "contexts": value.get("contexts"), "source": {"actor": source.get("actor"), "ref": source.get("ref")}, "revision": value.get("revision")})
    return evidence.canonical_sha256(values)


def _process_tree_working_set(pid: int) -> int:
    if pid <= 0:
        return 0
    try:
        process = psutil.Process(pid)
        return sum(int(item.memory_info().rss) for item in [process, *process.children(recursive=True)])
    except (psutil.NoSuchProcess, psutil.AccessDenied):
        return 0


def _tool_ms(calls: list[dict[str, Any]], names: set[str]) -> float:
    return sum(float(item["elapsed_ms"]) for item in calls if item["tool"] in names)


def _tool_count(calls: list[dict[str, Any]], names: set[str]) -> int:
    return sum(1 for item in calls if item["tool"] in names)


def _summary(values: list[float]) -> dict[str, float | int]:
    return {"samples": len(values), "mean_ms": statistics.fmean(values), "p95_ms": _p95(values), "max_ms": max(values)}


def _p95(values: list[float]) -> float:
    ordered = sorted(values)
    return ordered[min(len(ordered) - 1, math.ceil(len(ordered) * 0.95) - 1)]


def _validate_identity(value: dict[str, Any], schema: str, name: str) -> None:
    _require(value.get("schema") == schema, f"{name} schema 无效")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), f"{name}身份漂移")


def _load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise validation.KernelIterationValidationError(f"无法读取真实规模检索制品 {path}: {error}") from error
    _require(isinstance(value, dict), f"真实规模检索制品不是对象: {path}")
    return value


def _mapping(value: dict[str, Any], name: str) -> dict[str, Any]:
    result = value.get(name)
    _require(isinstance(result, dict), f"真实规模检索制品缺少对象: {name}")
    return result


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise validation.KernelIterationValidationError(message)
