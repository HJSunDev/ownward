from __future__ import annotations

from concurrent.futures import ThreadPoolExecutor
import json
import math
import os
from pathlib import Path
import threading
from typing import Any

import kernel_iteration_evidence as evidence
import kernel_iteration_longmemeval as longmemeval
import kernel_iteration_validation as validation


CONTRACT_SCHEMA = "ownward.kernel-iteration-stage4-multisource-performance-contract/v1"
RESULT_SCHEMA = "ownward.kernel-iteration-stage4-multisource-performance/v1"


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
    baseline_subject = evidence.validate_v2_subject(comparison, _load_json(baseline_subject_manifest.resolve()))
    candidate_subject = evidence.validate_v2_subject(comparison, _load_json(candidate_subject_manifest.resolve()))
    _require(baseline_subject["identity"] == contract["subjects"]["baseline"], "成对性能基线 subject 错绑")
    _require(candidate_subject["identity"] == contract["subjects"]["candidate"], "成对性能候选 subject 错绑")
    baseline_runtime = validation.validate_execution_config(suite_root, baseline_execution_config.resolve())
    candidate_runtime = validation.validate_execution_config(suite_root, candidate_execution_config.resolve())
    for name, subject, runtime in (
        ("baseline", baseline_subject, baseline_runtime),
        ("candidate", candidate_subject, candidate_runtime),
    ):
        artifacts = subject["content"]["artifacts"]
        _require(evidence.file_sha256(runtime["binary"]) == artifacts["binary"], f"{name} 二进制与 subject 错绑")
        _require(artifacts["binary"] == contract["binaries"][name], f"{name} 二进制漂移")
        _require(evidence.file_sha256(runtime["protocol"]) == contract["protocol_sha256"], f"{name} 协议漂移")
    baseline_result = validation._load_execution_result(baseline_result_path.resolve())
    candidate_result = validation._load_execution_result(candidate_result_path.resolve())
    _require(baseline_result["identity"] == contract["sealed_results"]["baseline"], "成对性能基线结果错绑")
    _require(candidate_result["identity"] == contract["sealed_results"]["candidate"], "成对性能候选结果错绑")
    _require(baseline_result["input_identity"] == candidate_result["input_identity"] == contract["input_identity"], "成对性能输入不同尺")
    _require(baseline_result["passed"] is False and candidate_result["passed"] is True, "成对性能没有绑定保留的失败基线与质量通过候选")

    materials = contract["materials"]
    cases = {item["case_id"]: item["question"] for item in materials["cases"]}
    _require(len(cases) == int(contract["schedule"]["workers"]), "成对性能案例与并发不同尺")
    roots = {"baseline": baseline_run_root.resolve(), "candidate": candidate_run_root.resolve()}
    runtimes = {"baseline": baseline_runtime, "candidate": candidate_runtime}
    before = {
        name: _data_identities(root, cases, contract["prepared_data"][name])
        for name, root in roots.items()
    }
    state_path = formal_state.resolve()
    state_before = evidence.file_sha256(state_path)
    _require(state_before == contract["formal_state_sha256"], "成对性能执行前正式 state 漂移")

    samples: dict[str, list[dict[str, Any]]] = {"baseline": [], "candidate": []}
    for order in contract["schedule"]["balanced_order"]:
        _require(sorted(order) == ["baseline", "candidate"], "成对性能顺序不是平衡配对")
        for name in order:
            samples[name].extend(_run_round(runtimes[name], roots[name], cases, contract["schedule"]))
    state_after = evidence.file_sha256(state_path)
    _require(state_after == state_before, "成对性能执行改写了正式 state")
    after = {
        name: _data_identities(root, cases, contract["prepared_data"][name])
        for name, root in roots.items()
    }
    _require(before == after, "只读成对性能执行改写了候选数据")

    metrics = evaluate(samples, float(contract["gates"]["candidate_p95_delta_max_ms"]))
    _require(metrics["passed"], "候选检索 p95 超出冻结重复误差")
    content = {
        "schema": RESULT_SCHEMA,
        "formal": False,
        "formal_state_written": False,
        "model_calls": 0,
        "product_mutations": 0,
        "contract_identity": contract["identity"],
        "subjects": dict(contract["subjects"]),
        "sealed_results": dict(contract["sealed_results"]),
        "input_identity": contract["input_identity"],
        "schedule": dict(contract["schedule"]),
        "metrics": metrics,
        "prepared_data_sha256_before": before,
        "prepared_data_sha256_after": after,
        "formal_state_sha256_before": state_before,
        "formal_state_sha256_after": state_after,
        "passed": True,
    }
    value = {**content, "identity": evidence.canonical_sha256(content)}
    destination = output_root.resolve() / "stage4-multisource" / "performance" / candidate_subject["identity"] / "result.json"
    _require(not destination.exists(), "成对性能证据已经存在；禁止随机重跑")
    evidence.atomic_json(destination, value)
    return {**value, "path": str(destination)}


def load_contract(suite_root: Path) -> dict[str, Any]:
    path = suite_root.resolve() / "iteration" / "v2" / "stage4-multisource-performance-contract.json"
    value = _load_json(path)
    _validate_identity(value, CONTRACT_SCHEMA, "成对性能合同")
    _require(value.get("frozen_before_performance_replay") is True, "成对性能合同没有在执行前冻结")
    _require(value.get("random_rerun_allowed") is False and value.get("model_or_answer_execution") is False, "成对性能合同允许随机或模型重跑")
    materials = validation.validate_stage3_materials(
        _load_json(suite_root.resolve() / "iteration" / "v2" / "stage4-multisource-materials.json"),
        expected_questions=int(value["schedule"]["workers"]),
    )
    _require(materials["identity"] == value["materials_identity"], "成对性能材料漂移")
    _require(evidence.file_sha256(Path(__file__).resolve()) == value["controller_sha256"], "成对性能控制器漂移")
    return {**value, "materials": materials}


def evaluate(samples: dict[str, list[dict[str, Any]]], maximum_delta: float) -> dict[str, Any]:
    summaries: dict[str, dict[str, Any]] = {}
    for name in ("baseline", "candidate"):
        values = samples.get(name)
        _require(isinstance(values, list) and values, f"{name} 缺少性能样本")
        trace_by_case: dict[str, set[str]] = {}
        for item in values:
            _require(isinstance(item, dict) and float(item["total_ms"]) >= 0, f"{name} 性能样本无效")
            trace_by_case.setdefault(str(item["case_identity"]), set()).add(str(item["trace_sha256"]))
        _require(all(len(identities) == 1 for identities in trace_by_case.values()), f"{name} 重复检索选择不确定")
        totals = sorted(float(item["total_ms"]) for item in values)
        summaries[name] = {
            "samples": len(values),
            "cases": len(trace_by_case),
            "mean_ms": sum(totals) / len(totals),
            "p95_ms": _p95(totals),
            "max_ms": max(totals),
            "search_p95_ms": _p95(sorted(float(item["search_ms"]) for item in values)),
            "evidence_search_p95_ms": _p95(sorted(float(item["evidence_search_ms"]) for item in values)),
            "read_p95_ms": _p95(sorted(float(item["read_ms"]) for item in values)),
            "stable_case_traces": True,
        }
    delta = summaries["candidate"]["p95_ms"] - summaries["baseline"]["p95_ms"]
    return {
        **summaries,
        "candidate_minus_baseline_p95_ms": delta,
        "candidate_p95_delta_max_ms": maximum_delta,
        "passed": delta <= maximum_delta,
    }


def _run_round(runtime: dict[str, Any], root: Path, cases: dict[str, str], schedule: dict[str, Any]) -> list[dict[str, Any]]:
    warmups = int(schedule["warmups_per_round"])
    repetitions = int(schedule["measured_repetitions_per_round"])
    barrier = threading.Barrier(len(cases))

    def worker(case_id: str, question: str) -> list[dict[str, Any]]:
        data_dir = root / "run" / "questions" / case_id / "ownward-data"
        _require(data_dir.is_dir(), f"成对性能缺少已准备数据: {case_id}")
        environment = os.environ.copy()
        environment["OWNWARD_EMBEDDING_BUNDLE_DIR"] = str(runtime["embedding"])
        result: list[dict[str, Any]] = []
        with longmemeval.adapter.OwnwardRuntime(
            runtime["binary"], data_dir, environment, startup_seconds=60,
            operation_seconds=float(runtime["protocol_value"]["retrieval"]["query_timeout_seconds"]),
        ) as service:
            for index in range(warmups + repetitions):
                barrier.wait(timeout=60)
                _, trace = longmemeval.adapter.retrieve(service, question, runtime["protocol_value"])
                if index < warmups:
                    continue
                selection = [
                    {
                        "source_rank": step.get("source_rank"), "mode": step.get("mode"),
                        "depth": step.get("depth"), "selected": step.get("selected"), "reason": step.get("reason"),
                    }
                    for step in trace.get("selection_steps", [])
                ]
                result.append({
                    "case_identity": evidence.canonical_sha256({"case_id": case_id}),
                    "trace_sha256": evidence.canonical_sha256({
                        "returned_ranks": list(range(len(trace.get("returned", [])))),
                        "selection": selection,
                        "read_units": len(trace.get("evidence_read_ids", [])) + sum(1 for path in trace.get("read_paths", []) if path.get("mode") == "full"),
                        "context_chars": trace.get("context_chars"),
                    }),
                    "search_ms": float(trace["search_ms"]),
                    "evidence_search_ms": float(trace["evidence_search_ms"]),
                    "read_ms": float(trace["read_ms"]),
                    "total_ms": float(trace["total_ms"]),
                })
        return result

    with ThreadPoolExecutor(max_workers=len(cases)) as pool:
        futures = [pool.submit(worker, case_id, question) for case_id, question in sorted(cases.items())]
        return [item for future in futures for item in future.result()]


def _data_identities(root: Path, cases: dict[str, str], expected: dict[str, str]) -> dict[str, str]:
    actual = {case_id: _tree_sha256(root / "run" / "questions" / case_id / "ownward-data") for case_id in sorted(cases)}
    _require(actual == expected, "成对性能已准备数据身份漂移")
    return actual


def _tree_sha256(root: Path) -> str:
    _require(root.is_dir(), f"数据目录不存在: {root}")
    manifest = []
    for path in sorted(root.rglob("*")):
        if path.is_file() and not path.is_symlink() and path.name != ".ownward.lock":
            manifest.append({"path": path.relative_to(root).as_posix(), "size": path.stat().st_size, "sha256": evidence.file_sha256(path)})
    return evidence.canonical_sha256(manifest)


def _p95(values: list[float]) -> float:
    return values[min(len(values) - 1, math.ceil(len(values) * 0.95) - 1)]


def _validate_identity(value: dict[str, Any], schema: str, name: str) -> None:
    _require(value.get("schema") == schema, f"{name} schema 无效")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), f"{name}身份漂移")


def _load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise validation.KernelIterationValidationError(f"无法读取成对性能制品 {path}: {error}") from error
    _require(isinstance(value, dict), f"成对性能制品不是对象: {path}")
    return value


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise validation.KernelIterationValidationError(message)
