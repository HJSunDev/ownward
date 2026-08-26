from __future__ import annotations

import hashlib
import importlib.util
import json
import math
import os
import platform
import re
import struct
import subprocess
import sys
import time
import zlib
from collections import Counter
from datetime import datetime
from pathlib import Path
from typing import Any, Iterable


class KernelIterationError(ValueError):
    pass


SCHEMA = "ownward.kernel-iteration/v1"
POOL_SCHEMA = "ownward.kernel-formal-failure-pool/v1"
BASELINE_SCHEMA = "ownward.kernel-fast-view-baseline/v1"
V0_CANDIDATE = "99f519018df99bd5202b0c571b8e43481cd1b80e"
CONTEXT_BUDGET_CHARS = 24_000
VIEW_RELATIVE = Path("materials/optimization/v1/direction-budget-selection.json")
SEMANTIC_VIEW_RELATIVE = Path("materials/optimization/v1/direction-semantic-representation.json")
GRANULARITY_VIEW_RELATIVE = Path("materials/optimization/v1/direction-organization-granularity.json")
STORAGE_VIEW_RELATIVE = Path("materials/optimization/v1/direction-storage-deposition.json")
CORE_RELATIVE = Path("materials/core/v1/dataset.json")
PRODUCT_SOURCE_PATHS = ("internal", "cmd/ownward", "go.mod", "go.sum")
ITERATION_IMPLEMENTATION_PATHS = (
    *PRODUCT_SOURCE_PATHS,
    "benchmarks/longmemeval_s/run.py",
    "benchmarks/longmemeval_s/protocol.json",
)


def run_storage(
    suite_root: Path,
    formal_run: Path,
    output_root: Path,
    candidate: str,
    resume: bool,
) -> dict[str, Any]:
    repository = suite_root.parents[2]
    formal_run = formal_run.resolve()
    output_root = output_root.resolve()
    _require(formal_run.is_dir(), "正式 LongMemEval-S 运行目录不存在")
    _require(candidate == "worktree", "存储方向候选必须绑定当前未提交工作树")
    product_sha = _worktree_product_sha256(repository)
    implementation_sha = _worktree_source_sha256(repository, ITERATION_IMPLEMENTATION_PATHS)
    candidate_identity = f"worktree:{implementation_sha}"
    observer_candidate = f"worktree:{product_sha}"
    paths = {
        "storage": suite_root / STORAGE_VIEW_RELATIVE,
        "semantic": suite_root / SEMANTIC_VIEW_RELATIVE,
        "granularity": suite_root / GRANULARITY_VIEW_RELATIVE,
        "budget": suite_root / VIEW_RELATIVE,
        "core": suite_root / CORE_RELATIVE,
    }
    for path in paths.values():
        _require(path.is_file(), f"迭代输入不存在: {path}")
    report_path = formal_run / "report.json"
    diagnostics_path = formal_run / "diagnostics.jsonl"
    input_identity = {
        "formal_candidate": V0_CANDIDATE,
        "formal_report_sha256": file_sha256(report_path),
        "formal_diagnostics_sha256": file_sha256(diagnostics_path),
        "evaluation_candidate": candidate_identity,
        "evaluation_product_source_sha256": product_sha,
        "evaluation_implementation_source_sha256": implementation_sha,
        **{f"{name}_view_sha256": file_sha256(path) for name, path in paths.items()},
        "analyzer_sha256": file_sha256(Path(__file__)),
        "algorithm": "kernel-v1-storage-deposition/v1",
    }
    input_sha = canonical_sha256(input_identity)
    output_root.mkdir(parents=True, exist_ok=True)
    formal_path = output_root / "formal-storage-observation.json"
    formal = _reuse_json(formal_path, input_sha, resume)
    if formal is None:
        formal = _audit_formal_storage(formal_run, input_sha)
        atomic_json(formal_path, formal)
    _validate_formal_storage_observation(formal, load_json(paths["storage"]))

    tool = _build_observer(repository, output_root, resume)
    tool_sha = file_sha256(tool)
    environment_sha = canonical_sha256({
        "os": platform.system().lower(), "arch": platform.machine().lower(),
        "python": platform.python_version(), "go": _command_output(["go", "env", "GOVERSION"], repository),
    })
    observer_input_sha = canonical_sha256({**input_identity, "observer_sha256": tool_sha})
    specifications = {
        "storage": (paths["storage"], ("storage_architecture",)),
        "semantic": (paths["semantic"], ("semantic_representation",)),
        "granularity": (paths["granularity"], ("organization", "indexing", "lexical", "vector", "graph", "context", "fusion", "efficiency")),
        "core": (paths["core"], ("identity", "relations", "merge_split", "incremental_consistency", "context", "fusion")),
    }
    observations: dict[str, dict[str, Any]] = {}
    reused: dict[str, bool] = {}
    durations: dict[str, float] = {}
    for name, (materials, stages) in specifications.items():
        observation_path = output_root / f"{name}-observation.json"
        existing = _reuse_observation(
            observation_path, observer_candidate, file_sha256(materials), observer_input_sha,
            tool_sha, stages, resume,
        )
        if existing is not None:
            observations[name] = existing
            reused[name] = True
            durations[name] = 0.0
            continue
        started = time.perf_counter()
        command = [
            str(tool), "--materials", str(materials), "--candidate", observer_candidate,
            "--mode", "targeted", "--environment-sha256", environment_sha,
            "--input-manifest-sha256", observer_input_sha, "--repository", str(repository),
            "--output", str(observation_path), "--stages", ",".join(stages), "--self-check",
            "--source-identity-sha256", product_sha,
        ]
        completed = subprocess.run(command, cwd=repository, text=True, encoding="utf-8", errors="replace", capture_output=True, timeout=180, check=False)
        if completed.returncode != 0:
            raise KernelIterationError(f"{name} 快速视图失败: {(completed.stderr or completed.stdout).strip()}")
        observations[name] = load_json(observation_path)
        reused[name] = False
        durations[name] = time.perf_counter() - started

    budget_path = output_root / "budget-observation.json"
    budget_tool_sha = file_sha256(repository / "benchmarks" / "longmemeval_s" / "run.py")
    existing_budget = _reuse_observation(
        budget_path, candidate_identity, file_sha256(paths["budget"]), observer_input_sha,
        budget_tool_sha, ("budget_selection",), resume,
    )
    if existing_budget is None:
        started = time.perf_counter()
        observations["budget"] = _observe_budget_selection(repository, paths["budget"], candidate_identity, observer_input_sha, budget_tool_sha)
        atomic_json(budget_path, observations["budget"])
        reused["budget"] = False
        durations["budget"] = time.perf_counter() - started
    else:
        observations["budget"] = existing_budget
        reused["budget"] = True
        durations["budget"] = 0.0
    _require(max((_observation_elapsed(value) for value in observations.values()), default=0) <= 180, "单个方向视图超过 3 分钟")
    _require(sum(_observation_elapsed(value) for value in observations.values()) <= 600, "方向与全部保护检查超过 10 分钟")

    pool = {"selection": {"mechanism": "rebuildable_semantic_work_content_persisted_twice"}}
    granularity_result = build_candidate_result(
        input_identity, pool, {"direction": observations["granularity"], "protection": observations["core"]},
        {"direction": _observation_elapsed(observations["granularity"]), "protection": _observation_elapsed(observations["core"])},
        load_json(paths["granularity"]), candidate_identity,
    )
    semantic_result = build_semantic_candidate_result(
        input_identity, pool,
        {"direction": observations["semantic"], "granularity_protection": observations["granularity"], "protection": observations["core"]},
        {"direction": _observation_elapsed(observations["semantic"]), "granularity_protection": _observation_elapsed(observations["granularity"]), "protection": _observation_elapsed(observations["core"])},
        load_json(paths["semantic"]), candidate_identity,
    )
    budget_result = build_budget_candidate_result(
        input_identity, pool,
        {"direction": observations["budget"], "semantic_protection": observations["semantic"], "granularity_protection": observations["granularity"], "protection": observations["core"]},
        {"direction": _observation_elapsed(observations["budget"]), "semantic_protection": _observation_elapsed(observations["semantic"]), "granularity_protection": _observation_elapsed(observations["granularity"]), "protection": _observation_elapsed(observations["core"])},
        load_json(paths["budget"]), candidate_identity,
    )
    storage_view = load_json(paths["storage"])
    storage_gate = _mapping(_mapping(storage_view, "optimization_view"), "frozen_gate")
    storage_metrics = {item["name"]: item for item in observations["storage"]["metrics"]}
    checks: dict[str, Any] = {}
    failures: list[str] = []
    for name, gate_name in (
        ("storage_amplification_ratio", "storage_amplification_ratio_max"),
        ("derived_storage_amplification_ratio", "derived_storage_amplification_ratio_max"),
        ("authority_history_reclaim_ratio", "authority_history_reclaim_ratio_max"),
        ("derived_records_per_asset", "derived_records_per_asset_max"),
        ("storage_view_query_p95_ms", "storage_view_query_p95_ms_max"),
    ):
        value = float(_mapping(storage_metrics, name)["value"])
        maximum = float(storage_gate[gate_name])
        passed = _within_upper_bound(value, maximum)
        checks[name] = {"value": value, "maximum": maximum, "passed": passed}
        if not passed:
            failures.append(name)
    for name, gate_name in (
        ("semantic_work_payload_recovery", "semantic_work_payload_recovery_min"),
        ("semantic_receipt_idempotency", "semantic_receipt_idempotency_min"),
        ("maintenance_byte_repeatability", "maintenance_byte_repeatability_min"),
        ("backup_restore_integrity", "backup_restore_integrity_min"),
        ("storage_view_search_recall", "storage_view_search_recall_min"),
    ):
        value = float(_mapping(storage_metrics, name)["value"])
        minimum = float(storage_gate[gate_name])
        passed = value >= minimum
        checks[name] = {"value": value, "minimum": minimum, "passed": passed}
        if not passed:
            failures.append(name)
    protections = {
        "information_representation_and_organization": granularity_result["passed"],
        "semantic_capability_and_representation_model": semantic_result["passed"],
        "retrieval_architecture_and_algorithm": budget_result["passed"],
    }
    failures.extend(name for name, passed in protections.items() if not passed)
    result = {
        "schema": "ownward.kernel-storage-candidate/v1", "candidate": candidate_identity,
        "input_identity": input_identity, "formal_storage_observation": formal,
        "metrics": checks, "protected_directions": protections,
        "gate": {**storage_gate, "failures": sorted(failures)},
        "cost": {"direction_wall_seconds": _observation_elapsed(observations["storage"]), "all_views_wall_seconds": sum(_observation_elapsed(value) for value in observations.values()), "direction_view_max_seconds": 180, "direction_validation_max_seconds": 600},
        "resume": {"identity_exact": True, "reused": reused, "policy": "reuse_exact_identity_and_rerun_only_invalid_parts"},
        "passed": not failures, "formal_evidence": False, "formal_acceptance_state_modified": False, "may_promote_baseline": False,
    }
    result["identity_sha256"] = canonical_sha256({
        "input": input_identity, "formal": file_sha256(formal_path),
        **{name: file_sha256(output_root / f"{name}-observation.json") for name in specifications},
        "budget": file_sha256(budget_path),
    })
    result_path = output_root / "candidate-result.json"
    existing_result = _reuse_json(result_path, result["identity_sha256"], resume)
    if existing_result is None:
        atomic_json(result_path, result)
    else:
        result = existing_result
    _require(result.get("passed") is True, f"存储方向候选未通过: {result.get('gate', {}).get('failures')}")
    return {"passed": True, "candidate": candidate_identity, "result": str(result_path), "identity_sha256": result["identity_sha256"], "reused": reused}


def _audit_formal_storage(formal_run: Path, identity_sha256: str) -> dict[str, Any]:
    totals = Counter()
    question_roots = sorted((formal_run / "questions").glob("*/ownward-data"))
    for question_root in question_roots:
        asset_path = question_root / "assets" / "information.jsonl"
        state_path = question_root / "state" / "organization.binlog"
        _require(asset_path.is_file() and state_path.is_file(), "正式产品存储证据不完整")
        totals["asset_log_bytes"] += asset_path.stat().st_size
        current_assets: dict[str, dict[str, Any]] = {}
        with asset_path.open("r", encoding="utf-8") as stream:
            for line_number, line in enumerate(stream, 1):
                try:
                    event = json.loads(line)
                    value = _mapping(event, "value")
                    asset_id = value.get("id")
                    revision = value.get("revision")
                    content = value.get("content")
                    _require(isinstance(asset_id, str) and asset_id and isinstance(revision, int) and revision > 0 and isinstance(content, str), f"正式权威资产第 {line_number} 行无效")
                    previous = current_assets.get(asset_id)
                    _require(previous is None or revision == int(previous["revision"]) + 1, f"正式权威资产 {asset_id} 版本不连续")
                    current_assets[asset_id] = {"revision": revision, "content_chars": len(content)}
                except (json.JSONDecodeError, KernelIterationError, KeyError, TypeError, ValueError) as exc:
                    raise KernelIterationError(f"正式权威资产日志损坏: {asset_path}:{line_number}: {exc}") from exc
        totals["current_asset_count"] += len(current_assets)
        totals["current_content_chars"] += sum(int(item["content_chars"]) for item in current_assets.values())
        totals["files"] += 1
        with state_path.open("rb") as stream:
            while True:
                header = stream.read(16)
                if not header:
                    break
                _require(len(header) == 16 and header[:4] == b"OWD3", "正式派生日志记录头无效")
                metadata_length, vector_length, expected_crc = struct.unpack("<III", header[4:])
                metadata = stream.read(metadata_length)
                vector = stream.read(vector_length)
                footer = stream.read(4)
                _require(len(metadata) == metadata_length and len(vector) == vector_length and footer == b"DONE", "正式派生日志记录被截断")
                _require(zlib.crc32(metadata + vector) & 0xFFFFFFFF == expected_crc, "正式派生日志记录校验失败")
                work_at = metadata.find(b',"semantic_work":')
                result_at = metadata.find(b',"semantic_result":')
                embedding_at = metadata.find(b',"embedding_space":')
                end = embedding_at if embedding_at >= 0 else len(metadata)
                work_bytes = ((result_at if result_at >= 0 else end) - work_at) if work_at >= 0 else 0
                result_bytes = (end - result_at) if result_at >= 0 else 0
                totals["records"] += 1
                totals["metadata_bytes"] += metadata_length
                totals["vector_bytes"] += vector_length
                totals["semantic_work_bytes"] += work_bytes
                totals["semantic_result_bytes"] += result_bytes
                totals["completed_records"] += int(result_at >= 0)
    derived_bytes = totals["metadata_bytes"] + totals["vector_bytes"] + totals["records"] * 20
    return {
        "schema": "ownward.kernel-formal-storage-observation/v1", "identity_sha256": identity_sha256,
        **dict(totals), "derived_bytes": derived_bytes,
        "question_isolated_product_bytes": totals["asset_log_bytes"] + derived_bytes,
        "semantic_work_share": totals["semantic_work_bytes"] / derived_bytes,
        "classification": {"benchmark_question_isolation_is_product_defect": False, "semantic_trace_audit_is_product_defect": False, "derived_semantic_work_duplication_is_product_defect": True},
    }


def _validate_formal_storage_observation(observation: dict[str, Any], view: dict[str, Any]) -> None:
    expected = _mapping(_mapping(view, "optimization_view"), "formal_scale_observation")
    _require(observation.get("files") == 500, "正式存储观察未覆盖 500 个隔离问题目录")
    _require(observation.get("asset_log_bytes") == expected["authoritative_asset_bytes"], "正式权威资产存储规模漂移")
    _require(observation.get("current_asset_count") == expected["current_asset_count"], "正式当前资产数量漂移")
    length_profile = _mapping(_mapping(view, "optimization_view"), "formal_length_profile")
    _require(observation.get("current_content_chars") == length_profile["total_content_chars"], "正式当前资产正文规模漂移")
    _require(observation.get("records") == expected["derived_record_count"], "正式派生记录规模漂移")
    _require(observation.get("derived_bytes") == expected["derived_log_bytes"], "正式派生日志规模漂移")
    _require(observation.get("question_isolated_product_bytes") == expected["question_isolated_product_bytes"], "正式问题隔离产品存储规模漂移")
    _require(observation.get("semantic_work_bytes") == expected["semantic_work_bytes"], "正式语义工作放大规模漂移")
    _require(math.isclose(float(observation.get("semantic_work_share", 0)), float(expected["semantic_work_share"]), rel_tol=1e-12), "正式语义工作放大比例漂移")


def _within_upper_bound(value: float, maximum: float) -> bool:
    return value <= maximum or math.isclose(value, maximum, rel_tol=1e-12, abs_tol=1e-12)


def run(
    suite_root: Path,
    formal_run: Path,
    output_root: Path,
    candidate: str,
    resume: bool,
) -> dict[str, Any]:
    repository = suite_root.parents[2]
    formal_run = formal_run.resolve()
    output_root = output_root.resolve()
    _require(formal_run.is_dir(), "正式 LongMemEval-S 运行目录不存在")
    is_v0_baseline = candidate == V0_CANDIDATE
    if is_v0_baseline:
        _verify_product_source_equivalence(repository, candidate)
        candidate_identity = candidate
        source_identity = _product_tree_sha256(repository, candidate)
    else:
        _require(candidate == "worktree", "首方向候选只能绑定冻结 V0 或当前工作树")
        source_identity = _worktree_product_sha256(repository)
        implementation_identity = _worktree_source_sha256(repository, ITERATION_IMPLEMENTATION_PATHS)
        candidate_identity = f"worktree:{implementation_identity}"
    closed_granularity_equivalence = _closed_granularity_path_equivalence(repository)
    closed_semantic_equivalence = _closed_semantic_path_equivalence(repository)
    output_root.mkdir(parents=True, exist_ok=True)

    diagnostics_path = formal_run / "diagnostics.jsonl"
    report_path = formal_run / "report.json"
    view_path = suite_root / VIEW_RELATIVE
    semantic_view_path = suite_root / SEMANTIC_VIEW_RELATIVE
    granularity_view_path = suite_root / GRANULARITY_VIEW_RELATIVE
    core_path = suite_root / CORE_RELATIVE
    for path in (diagnostics_path, report_path, view_path, semantic_view_path, granularity_view_path, core_path):
        _require(path.is_file(), f"迭代输入不存在: {path}")
    formal_input_identity = {
        "candidate": V0_CANDIDATE,
        "diagnostics_sha256": file_sha256(diagnostics_path),
        "formal_report_sha256": file_sha256(report_path),
        "view_sha256": file_sha256(view_path),
        "closed_semantic_view_sha256": file_sha256(semantic_view_path),
        "closed_granularity_view_sha256": file_sha256(granularity_view_path),
        "core_protection_sha256": file_sha256(core_path),
        "analyzer_sha256": file_sha256(Path(__file__)),
        "algorithm": "kernel-v1-budget-fit-diagonal-selection/v2",
    }
    input_identity = {
        **formal_input_identity,
        "evaluation_candidate": candidate_identity,
        "evaluation_product_source_sha256": source_identity,
        "evaluation_implementation_source_sha256": implementation_identity if not is_v0_baseline else source_identity,
        "previous_closed_chain": {
            "candidate_commit": "e6bfc82c0750ed0db30ff67b9ec6f7a3ca446570",
            "view_sha256": file_sha256(granularity_view_path),
            "public_path_equivalence": closed_granularity_equivalence,
        },
        "previous_closed_semantic_chain": {
            "candidate_commit": "436e12c3b97ad6c78254fdc8296a5aa6bb8c8665",
            "view_sha256": file_sha256(semantic_view_path),
            "product_path_equivalence": closed_semantic_equivalence,
        },
    }
    pool_identity_sha = canonical_sha256(formal_input_identity)

    pool_path = output_root / "problem-pool.json"
    pool = _reuse_json(pool_path, pool_identity_sha, resume)
    pool_reused = pool is not None
    if pool is None:
        pool = build_problem_pool(formal_run, V0_CANDIDATE, formal_input_identity, strict=True)
        pool["identity_sha256"] = pool_identity_sha
        atomic_json(pool_path, pool)
    validate_problem_pool(pool, strict=True)
    _validate_view_leakage(view_path, formal_run, pool)
    _validate_efficiency_authority(view_path, repository, formal_run)

    tool = _build_observer(repository, output_root, resume)
    tool_sha = file_sha256(tool)
    environment_sha = canonical_sha256({
        "os": platform.system().lower(),
        "arch": platform.machine().lower(),
        "python": platform.python_version(),
        "go": _command_output(["go", "env", "GOVERSION"], repository),
    })
    observer_input_sha = canonical_sha256({**input_identity, "observer_sha256": tool_sha})
    observations: dict[str, dict[str, Any]] = {}
    observation_reused: dict[str, bool] = {}
    durations: dict[str, float] = {}
    adapter_path = repository / "benchmarks" / "longmemeval_s" / "run.py"
    direction_path = output_root / "direction-observation.json"
    direction_tool_sha = file_sha256(adapter_path)
    existing_direction = _reuse_observation(
        direction_path, candidate_identity, file_sha256(view_path), observer_input_sha,
        direction_tool_sha, ("budget_selection",), resume,
    )
    if existing_direction is None:
        started = time.perf_counter()
        observations["direction"] = _observe_budget_selection(
            repository, view_path, candidate_identity, observer_input_sha, direction_tool_sha,
        )
        atomic_json(direction_path, observations["direction"])
        durations["direction"] = time.perf_counter() - started
        observation_reused["direction"] = False
    else:
        observations["direction"] = existing_direction
        durations["direction"] = 0.0
        observation_reused["direction"] = True
    specs = {
        "semantic_protection": (semantic_view_path, ("semantic_representation",)),
        "granularity_protection": (granularity_view_path, ("organization", "indexing", "lexical", "vector", "graph", "context", "fusion")),
        "protection": (core_path, ("identity", "relations", "merge_split", "incremental_consistency", "context", "fusion")),
    }
    product_observer_candidate = candidate_identity if is_v0_baseline else f"worktree:{source_identity}"
    for name, (materials, stages) in specs.items():
        path = output_root / f"{name}-observation.json"
        existing = _reuse_observation(path, product_observer_candidate, file_sha256(materials), observer_input_sha, tool_sha, stages, resume)
        if existing is not None:
            observations[name] = existing
            observation_reused[name] = True
            durations[name] = 0.0
            continue
        started = time.perf_counter()
        command = [
            str(tool),
            "--materials", str(materials),
            "--candidate", product_observer_candidate,
            "--mode", "targeted",
            "--environment-sha256", environment_sha,
            "--input-manifest-sha256", observer_input_sha,
            "--repository", str(repository),
            "--output", str(path),
            "--stages", ",".join(stages),
            "--self-check",
        ]
        if is_v0_baseline:
            command.extend(["--source-equivalent-candidate", V0_CANDIDATE])
        else:
            command.extend(["--source-identity-sha256", source_identity])
        completed = subprocess.run(command, cwd=repository, text=True, encoding="utf-8", errors="replace", capture_output=True, timeout=180, check=False)
        if completed.returncode != 0:
            raise KernelIterationError(f"{name} 快速视图失败: {(completed.stderr or completed.stdout).strip()}")
        durations[name] = time.perf_counter() - started
        observations[name] = load_json(path)
        observation_reused[name] = False
    _require(durations["direction"] <= 180, "单个确定性方向视图超过 3 分钟")
    _require(sum(durations.values()) <= 600, "方向视图与全部保护检查超过 10 分钟")

    measured_durations = {
        name: durations[name] if not observation_reused[name] else _observation_elapsed(observations[name])
        for name in observations
    }
    _require(not is_v0_baseline, "本轮冻结 V0 基线来自正式机械证据，不在改变后的产品源码上重算")
    result = build_budget_candidate_result(
        input_identity, pool, observations, measured_durations, load_json(view_path), candidate_identity,
    )
    result["identity_sha256"] = canonical_sha256({
        "input": input_identity,
        "problem_pool": file_sha256(pool_path),
        "direction_observation": file_sha256(output_root / "direction-observation.json"),
        "semantic_protection_observation": file_sha256(output_root / "semantic_protection-observation.json"),
        "granularity_protection_observation": file_sha256(output_root / "granularity_protection-observation.json"),
        "protection_observation": file_sha256(output_root / "protection-observation.json"),
    })
    if is_v0_baseline:
        _validate_frozen_baseline(view_path, result)
    result_path = output_root / ("v0-baseline.json" if is_v0_baseline else "candidate-result.json")
    existing_result = _reuse_json(result_path, result["identity_sha256"], resume)
    if existing_result is None:
        atomic_json(result_path, result)
    else:
        result = existing_result
    resume_proof = {
        "schema": "ownward.kernel-iteration-resume-proof/v1",
        "identity_sha256": result["identity_sha256"],
        "problem_pool_sha256": file_sha256(pool_path),
        "direction_observation_sha256": file_sha256(output_root / "direction-observation.json"),
        "semantic_protection_observation_sha256": file_sha256(output_root / "semantic_protection-observation.json"),
        "granularity_protection_observation_sha256": file_sha256(output_root / "granularity_protection-observation.json"),
        "protection_observation_sha256": file_sha256(output_root / "protection-observation.json"),
        "result_sha256": file_sha256(result_path),
        "reused": {"problem_pool": pool_reused, **observation_reused},
        "invalid_parts_rerun_only": True,
    }
    atomic_json(output_root / "resume-proof.json", resume_proof)
    return {
        "schema": SCHEMA,
        "candidate": candidate_identity,
        "problem_pool": str(pool_path),
        "problem_pool_count": len(pool["problems"]),
        "selected_cluster": pool["selection"]["cluster"],
        "selected_direction": pool["selection"]["direction"],
        "direction_observation": str(output_root / "direction-observation.json"),
        "semantic_protection_observation": str(output_root / "semantic_protection-observation.json"),
        "granularity_protection_observation": str(output_root / "granularity_protection-observation.json"),
        "protection_observation": str(output_root / "protection-observation.json"),
        "result": str(result_path),
        "reused": {"problem_pool": pool_reused, **observation_reused},
        "wall_seconds": round(sum(durations.values()), 6),
        "passed": bool(result.get("passed", True)),
        "formal_acceptance_state_modified": False,
    }


def build_problem_pool(
    formal_run: Path,
    candidate: str,
    input_identity: dict[str, Any],
    strict: bool,
) -> dict[str, Any]:
    diagnostics = _load_jsonl(formal_run / "diagnostics.jsonl")
    _require(len(diagnostics) == (500 if strict else len(diagnostics)), "正式诊断必须完整覆盖 500 题")
    problems: list[dict[str, Any]] = []
    for diagnostic in diagnostics:
        if diagnostic.get("correct") is True:
            continue
        problems.append(_map_problem(formal_run, diagnostic))
    pool = {
        "schema": POOL_SCHEMA,
        "candidate": candidate,
        "source": input_identity,
        "problem_count": len(problems),
        "problems": sorted(problems, key=lambda item: item["formal_question_identity"]),
        "selection": {
            "cluster": "prefix_greedy_context_budget_starvation",
            "direction": "retrieval_architecture_and_algorithm",
            "reason": "highest-proven-impact-earliest-causal-point",
            "mapped_failures": sum(item["mechanism"] == "prefix_greedy_context_budget_starvation" for item in problems),
            "next_validation": "budget-fit-evidence-selection-v2",
            "other_directions_started": [],
        },
        "privacy": {
            "contains_question_text": False,
            "contains_product_answer": False,
            "contains_gold_or_source_content": False,
            "formal_identity_and_artifact_digest_only": True,
        },
    }
    validate_problem_pool(pool, strict)
    return pool


def _map_problem(formal_run: Path, diagnostic: dict[str, Any]) -> dict[str, Any]:
    coverage = _mapping(diagnostic, "evidence_coverage")
    observations = _mapping(diagnostic, "execution_observations")
    artifacts = _mapping(diagnostic, "artifacts")
    checkpoint_ref = _mapping(artifacts, "question_checkpoint")
    checkpoint_path = Path(str(checkpoint_ref.get("path", "")))
    _require(checkpoint_path.is_file(), "问题检查点不存在")
    _require(file_sha256(checkpoint_path) == checkpoint_ref.get("sha256"), "问题检查点摘要不匹配")
    question_root = checkpoint_path.parent
    source_log = question_root / "ownward-data" / "assets" / "information.jsonl"
    _require(source_log.is_file(), "问题源资产日志不存在")
    content_lengths = _asset_content_lengths(source_log)
    expected_ids = _strings(coverage.get("expected_asset_ids"), "期望证据身份")
    returned_ids = _strings(coverage.get("search_returned_ids"), "搜索结果身份")
    read_ids = _strings(coverage.get("read_ids"), "读取身份")
    expected_chars = sum(content_lengths.get(identifier, 0) for identifier in expected_ids)
    returned_expected = [identifier for identifier in returned_ids if identifier in set(expected_ids)]
    read_expected = [identifier for identifier in read_ids if identifier in set(expected_ids)]
    gap = str(diagnostic.get("first_observed_gap"))
    semantic_complete = observations.get("semantic_submission_complete") is True
    organized_expected = set(_strings(coverage.get("organized_expected"), "组织证据身份")) == set(expected_ids)
    all_expected_returned = set(expected_ids) <= set(returned_ids)
    diagnostic_path = question_root / "diagnostic.json"
    retrieval_ref = _mapping(artifacts, "retrieval")
    retrieval_path = Path(str(retrieval_ref.get("path", "")))
    _require(diagnostic_path.is_file() and retrieval_path.is_file(), "问题诊断证据不完整")
    _require(file_sha256(retrieval_path) == retrieval_ref.get("sha256"), "检索证据摘要不匹配")
    representation_evidence: dict[str, Any] | None = None
    selection_evidence: dict[str, Any] | None = None
    derived_path = question_root / "ownward-data" / "state" / "organization.binlog"
    if gap == "target_evidence_not_search_returned":
        _require(derived_path.is_file(), "未检索问题缺少派生状态日志")
        derived = _derived_state_observation(derived_path, expected_ids)
        retrieval = load_json(retrieval_path)
        returned = _mapping(retrieval, "retrieval").get("returned", [])
        _require(isinstance(returned, list), "检索候选结果无效")
        signals = sorted({
            signal
            for item in returned if isinstance(item, dict)
            for signal in item.get("signals", []) if isinstance(signal, str)
        })
        submission_refs = artifacts.get("semantic_submissions", [])
        _require(isinstance(submission_refs, list) and submission_refs, "未检索问题缺少语义提交证据")
        submission_identity: list[dict[str, Any]] = []
        for ref in submission_refs:
            _require(isinstance(ref, dict), "语义提交证据引用无效")
            path = Path(str(ref.get("path", "")))
            _require(path.is_file() and file_sha256(path) == ref.get("sha256"), "语义提交证据摘要不匹配")
            submission_identity.append({"sha256": ref["sha256"], "asset_count": len(ref.get("asset_ids", [])), "work_count": len(ref.get("work_ids", []))})
        representation_evidence = {
            **derived,
            "search_signal_kinds": signals,
            "semantic_submission_count": len(submission_refs),
            "semantic_submission_identity_sha256": canonical_sha256(submission_identity),
        }
    if gap == "target_evidence_not_read":
        retrieval = load_json(retrieval_path)
        retrieval_value = _mapping(retrieval, "retrieval")
        returned = retrieval_value.get("returned")
        _require(isinstance(returned, list) and all(isinstance(item, dict) and isinstance(item.get("id"), str) for item in returned), "未读取问题的检索顺序无效")
        returned_ids = [str(item["id"]) for item in returned]
        formal_read_ids = _strings(retrieval_value.get("read_ids"), "正式读取顺序")
        _require(formal_read_ids == returned_ids[:len(formal_read_ids)], "正式读取并非搜索结果前缀")
        used_chars = sum(content_lengths.get(identifier, 0) for identifier in formal_read_ids)
        first_unread_rank = len(formal_read_ids) + 1
        first_unread = returned_ids[first_unread_rank - 1] if first_unread_rank <= len(returned_ids) else ""
        first_unread_chars = content_lengths.get(first_unread, 0)
        unread_expected = [identifier for identifier in expected_ids if identifier not in formal_read_ids]
        expected_ranks = sorted(returned_ids.index(identifier) + 1 for identifier in expected_ids)
        selection_evidence = {
            "returned_count": len(returned_ids),
            "read_prefix_count": len(formal_read_ids),
            "expected_ranks": expected_ranks,
            "unread_expected_count": len(unread_expected),
            "prefix_content_chars": used_chars,
            "first_unread_rank": first_unread_rank,
            "first_unread_is_expected": first_unread in set(expected_ids),
            "first_unread_content_chars": first_unread_chars,
            "first_unread_exceeds_remaining_budget": used_chars + first_unread_chars > CONTEXT_BUDGET_CHARS,
            "all_expected_fit_empty_budget": expected_chars <= CONTEXT_BUDGET_CHARS,
            "formal_read_is_exact_search_prefix": True,
            "retrieval_identity_sha256": canonical_sha256({
                "returned": [{"rank": index + 1, "score": item.get("score"), "signals": item.get("signals", [])} for index, item in enumerate(returned)],
                "read_prefix_count": len(formal_read_ids),
                "content_lengths": [content_lengths.get(identifier, 0) for identifier in returned_ids],
            }),
        }
    mechanism, direction, status, samples = classify_problem(
        gap,
        semantic_complete,
        organized_expected,
        all_expected_returned,
        expected_chars,
        str(diagnostic.get("capability")),
        representation_evidence,
        selection_evidence,
    )
    return {
        "formal_question_identity": str(diagnostic.get("question_identity")),
        "first_observed_gap": gap,
        "mechanism": mechanism,
        "mechanism_status": status,
        "responsible_direction": direction,
        "internal_sample_ids": samples,
        "observations": {
            "semantic_submission_complete": semantic_complete,
            "all_expected_assets_organized": organized_expected,
            "all_expected_assets_search_returned": all_expected_returned,
            "expected_asset_count": len(expected_ids),
            "search_returned_expected_count": len(returned_expected),
            "read_expected_count": len(read_expected),
            "expected_content_chars": expected_chars,
            "context_budget_chars": CONTEXT_BUDGET_CHARS,
            **({"representation": representation_evidence} if representation_evidence is not None else {}),
            **({"selection": selection_evidence} if selection_evidence is not None else {}),
        },
        "evidence": {
            "diagnostic": _relative_evidence(diagnostic_path, formal_run),
            "retrieval": _relative_evidence(retrieval_path, formal_run),
            "source_asset_log": _relative_evidence(source_log, formal_run),
            **({"derived_state_log": _relative_evidence(derived_path, formal_run)} if representation_evidence is not None else {}),
        },
    }


def classify_problem(
    gap: str,
    semantic_complete: bool,
    organized_expected: bool,
    all_expected_returned: bool,
    expected_chars: int,
    capability: str,
    representation_evidence: dict[str, Any] | None = None,
    selection_evidence: dict[str, Any] | None = None,
) -> tuple[str, str, str, list[str]]:
    if gap == "target_evidence_not_read" and semantic_complete and organized_expected and all_expected_returned and expected_chars > CONTEXT_BUDGET_CHARS:
        return (
            "session_unit_context_overflow",
            "information_representation_and_organization",
            "proven_general_mechanism",
            ["granularity-multi-source", f"granularity-{capability}"],
        )
    if gap == "target_evidence_not_read":
        evidence = selection_evidence or {}
        if (
            semantic_complete
            and organized_expected
            and all_expected_returned
            and expected_chars <= CONTEXT_BUDGET_CHARS
            and evidence.get("unread_expected_count", 0) > 0
            and evidence.get("first_unread_is_expected") is True
            and evidence.get("first_unread_exceeds_remaining_budget") is True
            and evidence.get("all_expected_fit_empty_budget") is True
            and evidence.get("formal_read_is_exact_search_prefix") is True
        ):
            return (
                "prefix_greedy_context_budget_starvation",
                "retrieval_architecture_and_algorithm",
                "proven_general_mechanism",
                ["budget-fit-source-breadth", "budget-skip-continuation"],
            )
        return (
            "pending_search_order_or_context_selection",
            "retrieval_architecture_and_algorithm",
            "pending_additional_evidence",
            ["protection-budget-fit-ordering"],
        )
    if gap == "target_evidence_not_search_returned":
        evidence = representation_evidence or {}
        if (
            semantic_complete
            and organized_expected
            and evidence.get("all_assets_have_oversized_embedding_failure") is True
            and evidence.get("latest_vector_count") == 0
            and evidence.get("expected_asset_vector_count") == 0
            and evidence.get("all_expected_semantic_results_submitted") is True
            and evidence.get("search_signal_kinds") == ["lexical"]
        ):
            return (
                "semantic_vector_representation_missing_after_oversized_input",
                "semantic_capability_and_representation_model",
                "proven_general_mechanism",
                ["semantic-representation-long-asset", "semantic-representation-short-batch-protection"],
            )
        return (
            "pending_semantic_organization_or_retrieval",
            "pending_cross_direction_evidence",
            "pending_additional_evidence",
            ["pending-search-miss-mechanism"],
        )
    if gap == "evidence_read_answer_incorrect":
        return (
            "reader_failure_after_complete_evidence",
            "outside_kernel_reader_boundary",
            "not_a_proven_kernel_root_cause",
            ["protection-complete-evidence-reader-boundary"],
        )
    return "pending_unclassified", "pending_evidence", "pending_additional_evidence", ["pending-unclassified"]


def validate_problem_pool(pool: dict[str, Any], strict: bool) -> None:
    _require(pool.get("schema") == POOL_SCHEMA, "问题池 schema 无效")
    problems = pool.get("problems")
    _require(isinstance(problems, list) and len(problems) == pool.get("problem_count"), "问题池规模无效")
    identities = [item.get("formal_question_identity") for item in problems if isinstance(item, dict)]
    _require(len(identities) == len(set(identities)) and all(identities), "问题池身份重复或空白")
    forbidden = {"question", "product_answer", "answer", "gold", "expected_asset_ids", "source_content"}
    _require(not (set(_all_keys(pool)) & forbidden), "问题池包含禁止的公开题目、答案或源事实字段")
    counts = Counter(item.get("mechanism") for item in problems)
    if strict:
        _require(len(problems) == 258, "V0 正式错误池必须包含 258 项")
        _require(counts["session_unit_context_overflow"] == 119, "首个通用根因簇规模漂移")
        _require(counts["prefix_greedy_context_budget_starvation"] == 14, "预算内未读取根因簇规模漂移")
        _require(counts["semantic_vector_representation_missing_after_oversized_input"] == 116, "语义表示缺失问题规模漂移")
        _require(counts["reader_failure_after_complete_evidence"] == 9, "Reader 边界问题规模漂移")
        representation = [
            _mapping(_mapping(item, "observations").get("representation", {}), "representation")
            for item in problems if item.get("mechanism") == "semantic_vector_representation_missing_after_oversized_input"
        ]
        _require(sum(int(item.get("latest_asset_count", 0)) for item in representation) == 5443, "语义表示缺失的派生资产规模漂移")
        _require(sum(int(item.get("latest_vector_count", -1)) for item in representation) == 0, "V0 未检索问题不应存在向量")
        _require(all(item.get("search_signal_kinds") == ["lexical"] for item in representation), "V0 未检索问题出现非词法搜索通道")
        selection = [
            _mapping(_mapping(item, "observations").get("selection", {}), "selection")
            for item in problems if item.get("mechanism") == "prefix_greedy_context_budget_starvation"
        ]
        _require(len(selection) == 14 and all(item.get("first_unread_is_expected") is True for item in selection), "预算内未读取根因证据不完整")
        ranks = Counter(rank for item in selection for rank in item.get("expected_ranks", []) if rank == item.get("first_unread_rank"))
        _require(ranks == {2: 8, 3: 5, 4: 1}, "预算内未读取的目标排名分布漂移")


def _observe_budget_selection(
    repository: Path,
    view_path: Path,
    candidate: str,
    input_manifest_sha256: str,
    tool_sha256: str,
) -> dict[str, Any]:
    started_at = datetime.now().astimezone().isoformat()
    adapter_path = repository / "benchmarks" / "longmemeval_s" / "run.py"
    module_name = "_ownward_kernel_budget_selection_adapter"
    spec = importlib.util.spec_from_file_location(module_name, adapter_path)
    _require(spec is not None and spec.loader is not None, "LongMemEval-S 适配器无法加载")
    adapter = importlib.util.module_from_spec(spec)
    sys.modules[module_name] = adapter
    sys.path.insert(0, str(adapter_path.parent))
    try:
        spec.loader.exec_module(adapter)
    finally:
        sys.path.remove(str(adapter_path.parent))
    protocol = load_json(adapter_path.with_name("protocol.json"))
    view = load_json(view_path)
    cases = view.get("cases")
    _require(isinstance(cases, list) and cases, "预算选择视图没有样本")

    class FixtureClient:
        def __init__(self, case: dict[str, Any]) -> None:
            self.sources = list(case["sources"])
            self.contents = {str(item["fixture_id"]): self.source_content(item) for item in self.sources}
            self.evidence: dict[str, tuple[str, str]] = {}

        @staticmethod
        def source_content(item: dict[str, Any]) -> str:
            segments = item.get("segments")
            if isinstance(segments, list):
                return "".join(
                    str(segment["content"]).ljust(int(segment["width"]), str(segment["padding_char"]))
                    for segment in segments
                )
            return str(item["content"]) + str(item.get("padding", "")) * int(item.get("padding_repeat", 0))

        @staticmethod
        def query_terms(query: str) -> set[str]:
            stop = {"what", "where", "which", "does", "from", "that", "this", "with", "have", "were", "after", "follows"}
            return {term for term in re.findall(r"[a-z0-9-]{4,}", query.lower()) if term not in stop}

        def call_tool(self, name: str, arguments: dict[str, Any]) -> dict[str, Any]:
            if name == "ownward_search":
                return {"results": [
                    {
                        "id": str(item["fixture_id"]),
                        "score": float(len(self.sources) - index),
                        "signals": item.get("signals", ["lexical"]),
                    }
                    for index, item in enumerate(self.sources)
                ]}
            if name == "ownward_evidence_search":
                source_id = str(arguments["source_id"])
                content = self.contents[source_id]
                if len(content) <= 384:
                    return {"evidence": []}
                terms = self.query_terms(str(arguments["query"]))
                chunks = [content[start:start + 384] for start in range(0, len(content), 384)]
                scored = [
                    (sum(chunk.lower().count(term) for term in terms), index, chunk)
                    for index, chunk in enumerate(chunks)
                ]
                scored = sorted((item for item in scored if item[0] > 0), key=lambda item: (-item[0], item[1]))[:int(arguments["limit"])]
                references = []
                for _, index, chunk in scored:
                    evidence_id = f"fixture:{source_id}:{index}"
                    self.evidence[evidence_id] = (source_id, chunk)
                    references.append({"id": evidence_id, "source_id": source_id, "content_runes": len(chunk)})
                return {"evidence": references}
            if name == "ownward_evidence_read":
                source_id, content = self.evidence[str(arguments["id"])]
                return {"evidence": {"id": str(arguments["id"]), "source_id": source_id, "content": content}}
            if name == "ownward_read":
                source_id = str(arguments["id"])
                return {"information": {"id": source_id, "content": self.contents[source_id]}}
            raise KernelIterationError(f"预算选择夹具调用未知工具: {name}")

    class FixtureRuntime:
        def __init__(self, client: FixtureClient) -> None:
            self.client = client

    def legacy_full_prefix(case: dict[str, Any]) -> list[dict[str, str]]:
        used = 0
        selected: list[dict[str, str]] = []
        budget = int(case.get("context_budget_chars", 24000))
        limit = int(case.get("read_limit", 8))
        client = FixtureClient(case)
        for item in case["sources"]:
            source_id = str(item["fixture_id"])
            content = client.contents[source_id]
            if selected and used + len(content) > budget:
                break
            selected.append({"id": source_id, "content": content})
            used += len(content)
            if len(selected) >= limit:
                break
        return selected

    def previous_source_major(case: dict[str, Any]) -> list[dict[str, str]]:
        client = FixtureClient(case)
        selected: list[dict[str, str]] = []
        used = 0
        budget = int(case.get("context_budget_chars", 24000))
        limit = int(case.get("read_limit", 8))
        for item in case["sources"]:
            source_id = str(item["fixture_id"])
            references = client.call_tool("ownward_evidence_search", {
                "source_id": source_id, "query": case["query"],
                "limit": int(protocol["retrieval"]["evidence_search_limit_per_source"]),
            })["evidence"]
            if references:
                for reference in references:
                    if used + int(reference["content_runes"]) > budget:
                        return selected
                    used += int(reference["content_runes"])
                    narrowed = client.call_tool("ownward_evidence_read", {"id": reference["id"]})["evidence"]
                    selected.append({"id": source_id, "content": str(narrowed["content"])})
                    if len(selected) >= limit:
                        return selected
                continue
            content = client.contents[source_id]
            if used + len(content) > budget:
                return selected
            used += len(content)
            selected.append({"id": source_id, "content": content})
            if len(selected) >= limit:
                return selected
        return selected

    def evidence_hits(items: list[dict[str, Any]], required: list[dict[str, Any]]) -> int:
        return sum(
            any(
                str(item.get("id")) == str(expectation["source_id"])
                and str(expectation["marker"]) in str(item.get("content", ""))
                for item in items
            )
            for expectation in required
        )

    case_results: list[dict[str, Any]] = []
    elapsed_ms: list[float] = []
    candidate_item_hits = 0
    legacy_complete = 0
    previous_complete = 0
    candidate_complete = 0
    total_required = 0
    diagonal_checks = 0
    skip_checks = 0
    lazy_checks = 0
    context_checks = 0
    read_checks = 0
    policy_checks = 0
    repeat_checks = 0
    for case in cases:
        case_protocol = json.loads(json.dumps(protocol))
        case_protocol["retrieval"]["context_max_chars"] = int(case.get("context_budget_chars", 24000))
        case_protocol["retrieval"]["read_limit"] = int(case.get("read_limit", 8))
        required = list(case["required_evidence"])
        required_sources = {str(item["source_id"]) for item in required}
        total_required += len(required)
        legacy_evidence = legacy_full_prefix(case)
        previous_evidence = previous_source_major(case)
        legacy_hits = evidence_hits(legacy_evidence, required)
        previous_hits = evidence_hits(previous_evidence, required)
        legacy_complete += int(legacy_hits == len(required))
        previous_complete += int(previous_hits == len(required))
        client = FixtureClient(case)
        before = time.perf_counter()
        evidence, trace = adapter.retrieve(FixtureRuntime(client), str(case["query"]), case_protocol)
        elapsed_ms.append((time.perf_counter() - before) * 1000)
        selected = set(str(item) for item in trace["read_ids"])
        item_hits = evidence_hits(evidence, required)
        candidate_item_hits += item_hits
        candidate_complete += int(item_hits == len(required))
        selected_steps = [item for item in trace["selection_steps"] if item.get("selected") is True]
        priority = [
            (int(item["priority_layer"]), int(item["depth"]), int(item["source_rank"]))
            for item in trace["selection_steps"]
        ]
        diagonal = priority == sorted(priority)
        diagonal_checks += int(diagonal)
        skipped = [item for item in trace["selection_steps"] if item.get("reason") == "context_budget"]
        if skipped:
            first_skip = trace["selection_steps"].index(skipped[0])
            skip_checks += int(any(item.get("selected") is True and item.get("source_id") in required_sources for item in trace["selection_steps"][first_skip + 1:]))
        else:
            skip_checks += 1
        evidence_search_calls = len({str(item["source_id"]) for item in trace["selection_steps"]})
        lazy_checks += int(evidence_search_calls <= int(case["max_evidence_search_calls"]))
        context_checks += int(int(trace["context_chars"]) <= int(case_protocol["retrieval"]["context_max_chars"]))
        read_checks += int(len(evidence) <= int(case_protocol["retrieval"]["read_limit"]))
        policy_checks += int(trace.get("selection_policy") == "rank-depth-diagonal-budget-fit/v1")
        second_evidence, second_trace = adapter.retrieve(FixtureRuntime(FixtureClient(case)), str(case["query"]), case_protocol)
        timing_fields = {"search_ms", "evidence_search_ms", "read_ms", "total_ms"}
        stable_trace = {key: value for key, value in trace.items() if key not in timing_fields}
        stable_second = {key: value for key, value in second_trace.items() if key not in timing_fields}
        repeat_checks += int(evidence == second_evidence and stable_trace == stable_second)
        case_results.append({
            "case_id": case["case_id"],
            "required_evidence_count": len(required),
            "legacy_full_prefix_evidence_hits": legacy_hits,
            "previous_source_major_evidence_hits": previous_hits,
            "candidate_evidence_hits": item_hits,
            "selected_source_count": len(selected),
            "selected_evidence_count": len(evidence),
            "evidence_search_calls": evidence_search_calls,
            "evidence_search_calls_max": int(case["max_evidence_search_calls"]),
            "context_chars": trace["context_chars"],
            "budget_skip_count": len(skipped),
            "rank_depth_diagonal_order": diagonal,
            "repeatable": evidence == second_evidence and stable_trace == stable_second,
        })
    count = len(cases)
    values = sorted(elapsed_ms)
    p95_index = max(0, math.ceil(len(values) * 0.95) - 1)

    def metric(name: str, dimension: str, value: float, direction: str) -> dict[str, Any]:
        return {
            "name": name, "dimension": dimension, "stage": "budget_selection", "value": value,
            "direction": direction, "repeatability_error": 0, "materiality": 0.005, "protected": True,
        }

    metrics = [
        metric("required_evidence_question_recall", "quality", candidate_complete / count, "higher"),
        metric("required_evidence_question_error_rate", "quality", 1 - candidate_complete / count, "lower"),
        metric("required_evidence_item_recall", "quality", candidate_item_hits / total_required, "higher"),
        metric("legacy_full_prefix_question_recall", "quality", legacy_complete / count, "higher"),
        metric("previous_source_major_question_recall", "quality", previous_complete / count, "higher"),
        metric("rank_depth_diagonal_order_rate", "quality", diagonal_checks / count, "higher"),
        metric("top_rank_three_passage_coverage", "quality", float(next(item["candidate_evidence_hits"] == item["required_evidence_count"] for item in case_results if item["case_id"] == "B07")), "higher"),
        metric("multi_source_multi_depth_coverage", "quality", float(next(item["candidate_evidence_hits"] == item["required_evidence_count"] for item in case_results if item["case_id"] == "B08")), "higher"),
        metric("budget_skip_continuation_rate", "quality", skip_checks / count, "higher"),
        metric("lazy_evidence_search_rate", "resources", lazy_checks / count, "higher"),
        metric("context_budget_compliance", "resources", context_checks / count, "higher"),
        metric("read_limit_compliance", "resources", read_checks / count, "higher"),
        metric("selection_policy_identity_rate", "quality", policy_checks / count, "higher"),
        metric("exact_repeatability_rate", "quality", repeat_checks / count, "higher"),
        metric("consumer_retrieval_p95_ms", "latency", values[p95_index], "lower"),
    ]
    finished_at = datetime.now().astimezone().isoformat()
    return {
        "schema": "ownward.core-frontier-observation/v1",
        "suite_version": "1.0.0",
        "candidate": candidate,
        "materials_sha256": file_sha256(view_path),
        "input_manifest_sha256": input_manifest_sha256,
        "mode": "targeted",
        "requested_stages": ["budget_selection"],
        "environment": {"os": platform.system().lower(), "arch": platform.machine().lower(), "python": platform.python_version()},
        "tool_sha256": tool_sha256,
        "metrics": sorted(metrics, key=lambda item: item["name"]),
        "cases": case_results,
        "started_at": started_at,
        "finished_at": finished_at,
    }


def build_baseline(
    input_identity: dict[str, Any],
    pool: dict[str, Any],
    observations: dict[str, dict[str, Any]],
    durations: dict[str, float],
) -> dict[str, Any]:
    direction_metrics = {item["name"]: item for item in observations["direction"]["metrics"]}
    protection_metrics = {item["name"]: item for item in observations["protection"]["metrics"]}
    primary = float(_mapping(direction_metrics, "required_evidence_budget_recall")["value"])
    error = float(_mapping(direction_metrics, "required_evidence_budget_error_rate")["value"])
    protected_names = ("budget_fit_protection", "identity_stability", "fusion_recall", "fusion_ndcg")
    protected = {
        name: (direction_metrics if name in direction_metrics else protection_metrics)[name]
        for name in protected_names
    }
    return {
        "schema": BASELINE_SCHEMA,
        "candidate": V0_CANDIDATE,
        "input_identity": input_identity,
        "problem_pool_sha256": canonical_sha256(pool),
        "selected_cluster": pool["selection"],
        "baseline": {
            "required_evidence_budget_recall": primary,
            "required_evidence_budget_error_rate": error,
            "protected_metrics": protected,
        },
        "large_improvement_gate": {
            "required_evidence_budget_recall_min": min(1.0, max(0.5, primary * 2)),
            "required_evidence_budget_error_rate_max": error / 2,
            "protected_regression_allowed": False,
        },
        "cost": {
            "direction_wall_seconds": round(durations["direction"], 6),
            "protection_wall_seconds": round(durations["protection"], 6),
            "direction_view_max_seconds": 180,
            "direction_validation_target_seconds": 420,
            "direction_validation_max_seconds": 600,
        },
        "resume": {
            "identity_exact": True,
            "checkpoints": ["problem_pool", "direction_observation", "protection_observation"],
            "policy": "reuse_exact_identity_and_rerun_only_invalid_parts",
        },
        "formal_evidence": False,
        "may_promote_baseline": False,
    }


def build_candidate_result(
    input_identity: dict[str, Any],
    pool: dict[str, Any],
    observations: dict[str, dict[str, Any]],
    durations: dict[str, float],
    view: dict[str, Any],
    candidate: str,
) -> dict[str, Any]:
    optimization = _mapping(view, "optimization_view")
    frozen_baseline = _mapping(optimization, "v0_baseline")
    gate = _mapping(optimization, "frozen_gate")
    direction_metrics = {item["name"]: item for item in observations["direction"]["metrics"]}
    protection_metrics = {item["name"]: item for item in observations["protection"]["metrics"]}
    primary = float(_mapping(direction_metrics, "required_evidence_budget_recall")["value"])
    error = float(_mapping(direction_metrics, "required_evidence_budget_error_rate")["value"])
    protected_names = ("budget_fit_protection", "identity_stability", "fusion_recall", "fusion_ndcg")
    protected: dict[str, dict[str, Any]] = {}
    regressions: list[str] = []
    for name in protected_names:
        metric = (direction_metrics if name in direction_metrics else protection_metrics)[name]
        protected[name] = metric
        if float(metric["value"]) < float(frozen_baseline[name]):
            regressions.append(name)
    recall_passed = primary >= float(gate["required_evidence_budget_recall_min"])
    error_passed = error <= float(gate["required_evidence_budget_error_rate_max"])
    scale_metric = _mapping(direction_metrics, "scale_evidence_recall")
    scale_passed = float(scale_metric["value"]) >= float(gate["scale_evidence_recall_min"])
    efficiency_baseline = _mapping(optimization, "v0_efficiency_baseline")
    structural_names = (
        "organization_input_overhead_ratio",
        "organization_vector_overhead_ratio",
        "derived_record_overhead_ratio",
        "derived_vector_overhead_ratio",
        "rebuild_input_overhead_ratio",
        "rebuild_vector_overhead_ratio",
        "rebuilt_record_overhead_ratio",
        "rebuilt_vector_overhead_ratio",
    )
    efficiency_checks: dict[str, dict[str, Any]] = {}
    efficiency_regressions: list[str] = []
    for name in structural_names:
        measured = _mapping(direction_metrics, name)
        maximum = float(efficiency_baseline[name]) + float(measured.get("repeatability_error", 0))
        passed = _within_upper_bound(float(measured["value"]), maximum)
        efficiency_checks[name] = {"value": measured["value"], "maximum": maximum, "passed": passed}
        if not passed:
            efficiency_regressions.append(name)
    for candidate_name, v0_name in (
        ("candidate_query_content_runes", "v0_query_content_runes"),
        ("candidate_query_serialized_bytes", "v0_query_serialized_bytes"),
        ("candidate_organization_real_p95_ms", "v0_organization_real_p95_ms"),
        ("candidate_rebuild_real_p95_ms", "v0_rebuild_real_p95_ms"),
        ("candidate_derived_storage_bytes_per_source_rune", "v0_derived_storage_bytes_per_source_rune"),
    ):
        measured = _mapping(direction_metrics, candidate_name)
        baseline_metric = _mapping(direction_metrics, v0_name)
        maximum = float(baseline_metric["value"]) + float(measured.get("repeatability_error", 0)) + float(baseline_metric.get("repeatability_error", 0))
        passed = _within_upper_bound(float(measured["value"]), maximum)
        efficiency_checks[candidate_name] = {
            "value": measured["value"], "paired_v0_value": baseline_metric["value"],
            "maximum": maximum, "passed": passed,
        }
        if not passed:
            efficiency_regressions.append(candidate_name)
    query_metric = _mapping(direction_metrics, "candidate_query_workflow_p95_ms")
    v0_query_metric = _mapping(direction_metrics, "v0_query_workflow_p95_ms")
    paired_repeatability_max = float(v0_query_metric["value"]) + float(query_metric.get("repeatability_error", 0)) + float(v0_query_metric.get("repeatability_error", 0))
    absolute_query_max = float(gate["query_workflow_p95_ms_max"])
    query_value = float(query_metric["value"])
    relative_query_passed = _within_upper_bound(query_value, paired_repeatability_max)
    absolute_query_passed = _within_upper_bound(query_value, absolute_query_max)
    query_passed = relative_query_passed and absolute_query_passed
    efficiency_checks["candidate_query_workflow_p95_ms"] = {
        "value": query_metric["value"], "paired_v0_value": v0_query_metric["value"],
        "relative_maximum": paired_repeatability_max, "absolute_maximum": absolute_query_max,
        "relative_passed": relative_query_passed, "absolute_passed": absolute_query_passed,
        "authority": gate["query_workflow_limit_source"], "passed": query_passed,
    }
    if not query_passed:
        efficiency_regressions.append("candidate_query_workflow_p95_ms")
    return {
        "schema": "ownward.kernel-fast-view-candidate/v1",
        "candidate": candidate,
        "input_identity": input_identity,
        "problem_pool_sha256": canonical_sha256(pool),
        "selected_cluster": pool["selection"],
        "metrics": {
            "required_evidence_budget_recall": primary,
            "required_evidence_budget_error_rate": error,
            "scale_evidence_recall": scale_metric,
            "protected_metrics": protected,
            "efficiency_metrics": efficiency_checks,
        },
        "gate": {
            **gate,
            "recall_passed": recall_passed,
            "error_rate_passed": error_passed,
            "scale_passed": scale_passed,
            "protected_regressions": regressions,
            "efficiency_regressions": efficiency_regressions,
        },
        "cost": {
            "direction_wall_seconds": round(durations["direction"], 6),
            "protection_wall_seconds": round(durations["protection"], 6),
            "direction_view_max_seconds": 180,
            "direction_validation_max_seconds": 600,
        },
        "resume": {
            "identity_exact": True,
            "checkpoints": ["problem_pool", "direction_observation", "protection_observation"],
            "policy": "reuse_exact_identity_and_rerun_only_invalid_parts",
        },
        "passed": recall_passed and error_passed and scale_passed and not regressions and not efficiency_regressions,
        "formal_evidence": False,
        "formal_acceptance_state_modified": False,
        "may_promote_baseline": False,
    }


def build_semantic_candidate_result(
    input_identity: dict[str, Any],
    pool: dict[str, Any],
    observations: dict[str, dict[str, Any]],
    durations: dict[str, float],
    view: dict[str, Any],
    candidate: str,
) -> dict[str, Any]:
    optimization = _mapping(view, "optimization_view")
    gate = _mapping(optimization, "frozen_gate")
    direction = {item["name"]: item for item in observations["direction"]["metrics"]}
    granularity = {item["name"]: item for item in observations["granularity_protection"]["metrics"]}
    core = {item["name"]: item for item in observations["protection"]["metrics"]}

    checks: dict[str, dict[str, Any]] = {}
    failures: list[str] = []

    def minimum(metric_name: str, gate_name: str) -> None:
        metric = _mapping(direction, metric_name)
        required = float(gate[gate_name])
        passed = float(metric["value"]) >= required
        checks[metric_name] = {"value": metric["value"], "minimum": required, "passed": passed}
        if not passed:
            failures.append(metric_name)

    def maximum(metric_name: str, gate_name: str) -> None:
        metric = _mapping(direction, metric_name)
        required = float(gate[gate_name])
        passed = _within_upper_bound(float(metric["value"]), required)
        checks[metric_name] = {"value": metric["value"], "maximum": required, "passed": passed}
        if not passed:
            failures.append(metric_name)

    for metric_name, gate_name in (
        ("semantic_search_recall", "semantic_search_recall_min"),
        ("long_asset_vector_recovery", "long_asset_vector_recovery_min"),
        ("short_asset_vector_availability", "short_asset_vector_availability_min"),
        ("semantic_input_marker_coverage", "semantic_input_marker_coverage_min"),
        ("restart_semantic_recall", "restart_semantic_recall_min"),
        ("rebuild_semantic_recall", "rebuild_semantic_recall_min"),
        ("semantic_signal_rate", "semantic_signal_rate_min"),
    ):
        minimum(metric_name, gate_name)
    for metric_name, gate_name in (
        ("semantic_search_error_rate", "semantic_search_error_rate_max"),
        ("derived_vectors_per_asset", "derived_vectors_per_asset_max"),
        ("semantic_input_to_source_bytes", "semantic_input_to_source_bytes_max"),
        ("oversized_raw_embedding_attempts", "oversized_raw_embedding_attempts_max"),
        ("semantic_embedding_calls", "semantic_embedding_calls_max"),
        ("rebuild_semantic_embedding_calls", "rebuild_semantic_embedding_calls_max"),
        ("semantic_representation_p95_ms", "semantic_representation_p95_ms_max"),
        ("semantic_rebuild_p95_ms", "semantic_rebuild_p95_ms_max"),
        ("semantic_search_p95_ms", "semantic_search_p95_ms_max"),
    ):
        maximum(metric_name, gate_name)

    protected_sources = {
        "required_evidence_budget_recall": (granularity, float(gate["closed_granularity_recall_min"])),
        "scale_evidence_recall": (granularity, float(gate["closed_granularity_scale_recall_min"])),
        "budget_fit_protection": (granularity, 1.0),
        "identity_stability": (core, 1.0),
        "fusion_recall": (granularity, 1.0),
        "fusion_ndcg": (granularity, 1.0),
    }
    protected: dict[str, dict[str, Any]] = {}
    for name, (source, required) in protected_sources.items():
        metric = _mapping(source, name)
        passed = float(metric["value"]) >= required
        protected[name] = {"value": metric["value"], "minimum": required, "passed": passed}
        if not passed:
            failures.append(name)

    return {
        "schema": "ownward.kernel-fast-view-candidate/v1",
        "candidate": candidate,
        "input_identity": input_identity,
        "problem_pool_sha256": canonical_sha256(pool),
        "selected_cluster": pool["selection"],
        "metrics": {"direction": checks, "protected": protected},
        "gate": {**gate, "failures": sorted(failures)},
        "cost": {
            "direction_wall_seconds": round(durations["direction"], 6),
            "granularity_protection_wall_seconds": round(durations["granularity_protection"], 6),
            "common_protection_wall_seconds": round(durations["protection"], 6),
            "direction_view_max_seconds": 180,
            "direction_validation_max_seconds": 600,
        },
        "resume": {
            "identity_exact": True,
            "checkpoints": ["problem_pool", "direction_observation", "granularity_protection_observation", "protection_observation"],
            "policy": "reuse_exact_identity_and_rerun_only_invalid_parts",
        },
        "passed": not failures,
        "formal_evidence": False,
        "formal_acceptance_state_modified": False,
        "may_promote_baseline": False,
    }


def build_budget_candidate_result(
    input_identity: dict[str, Any],
    pool: dict[str, Any],
    observations: dict[str, dict[str, Any]],
    durations: dict[str, float],
    view: dict[str, Any],
    candidate: str,
) -> dict[str, Any]:
    gate = _mapping(_mapping(view, "optimization_view"), "frozen_gate")
    direction = {item["name"]: item for item in observations["direction"]["metrics"]}
    semantic = {item["name"]: item for item in observations["semantic_protection"]["metrics"]}
    granularity = {item["name"]: item for item in observations["granularity_protection"]["metrics"]}
    core = {item["name"]: item for item in observations["protection"]["metrics"]}
    checks: dict[str, dict[str, Any]] = {}
    protected: dict[str, dict[str, Any]] = {}
    failures: list[str] = []

    def minimum(metric_name: str, gate_name: str) -> None:
        value = float(_mapping(direction, metric_name)["value"])
        required = float(gate[gate_name])
        passed = value >= required
        checks[metric_name] = {"value": value, "minimum": required, "passed": passed}
        if not passed:
            failures.append(metric_name)

    def maximum(metric_name: str, gate_name: str) -> None:
        value = float(_mapping(direction, metric_name)["value"])
        required = float(gate[gate_name])
        passed = _within_upper_bound(value, required)
        checks[metric_name] = {"value": value, "maximum": required, "passed": passed}
        if not passed:
            failures.append(metric_name)

    for metric_name, gate_name in (
        ("required_evidence_item_recall", "required_evidence_item_recall_min"),
        ("required_evidence_question_recall", "required_evidence_question_recall_min"),
        ("rank_depth_diagonal_order_rate", "rank_depth_diagonal_order_rate_min"),
        ("top_rank_three_passage_coverage", "top_rank_three_passage_coverage_min"),
        ("multi_source_multi_depth_coverage", "multi_source_multi_depth_coverage_min"),
        ("budget_skip_continuation_rate", "budget_skip_continuation_rate_min"),
        ("lazy_evidence_search_rate", "lazy_evidence_search_rate_min"),
        ("context_budget_compliance", "context_budget_compliance_min"),
        ("read_limit_compliance", "read_limit_compliance_min"),
        ("selection_policy_identity_rate", "selection_policy_identity_rate_min"),
    ):
        minimum(metric_name, gate_name)
    for metric_name, gate_name in (
        ("required_evidence_question_error_rate", "required_evidence_question_error_rate_max"),
        ("consumer_retrieval_p95_ms", "consumer_retrieval_p95_ms_max"),
    ):
        maximum(metric_name, gate_name)
    repeatability = float(_mapping(direction, "exact_repeatability_rate")["value"])
    checks["exact_repeatability_rate"] = {"value": repeatability, "minimum": 1.0, "passed": repeatability >= 1.0}
    if repeatability < 1.0:
        failures.append("exact_repeatability_rate")

    protected_sources = {
        "semantic_search_recall": (semantic, 1.0, "minimum"),
        "semantic_search_error_rate": (semantic, 0.0, "maximum"),
        "long_asset_vector_recovery": (semantic, 1.0, "minimum"),
        "restart_semantic_recall": (semantic, 1.0, "minimum"),
        "rebuild_semantic_recall": (semantic, 1.0, "minimum"),
        "derived_vectors_per_asset": (semantic, 1.0, "maximum"),
        "required_evidence_budget_recall": (granularity, 1.0, "minimum"),
        "scale_evidence_recall": (granularity, 1.0, "minimum"),
        "budget_fit_protection": (granularity, 1.0, "minimum"),
        "fusion_recall": (granularity, 1.0, "minimum"),
        "fusion_ndcg": (granularity, 1.0, "minimum"),
        "identity_stability": (core, 1.0, "minimum"),
    }
    for name, (source, required, comparison) in protected_sources.items():
        value = float(_mapping(source, name)["value"])
        passed = value >= required if comparison == "minimum" else _within_upper_bound(value, required)
        protected[name] = {"value": value, comparison: required, "passed": passed}
        if not passed:
            failures.append(name)

    return {
        "schema": "ownward.kernel-fast-view-candidate/v1",
        "candidate": candidate,
        "input_identity": input_identity,
        "problem_pool_sha256": canonical_sha256(pool),
        "selected_cluster": pool["selection"],
        "metrics": {"direction": checks, "protected": protected},
        "gate": {**gate, "failures": sorted(failures)},
        "cost": {
            "direction_wall_seconds": round(durations["direction"], 6),
            "semantic_protection_wall_seconds": round(durations["semantic_protection"], 6),
            "granularity_protection_wall_seconds": round(durations["granularity_protection"], 6),
            "common_protection_wall_seconds": round(durations["protection"], 6),
            "direction_view_max_seconds": 180,
            "direction_validation_max_seconds": 600,
        },
        "resume": {
            "identity_exact": True,
            "checkpoints": [
                "problem_pool", "direction_observation", "semantic_protection_observation",
                "granularity_protection_observation", "protection_observation",
            ],
            "policy": "reuse_exact_identity_and_rerun_only_invalid_parts",
        },
        "passed": not failures,
        "formal_evidence": False,
        "formal_acceptance_state_modified": False,
        "may_promote_baseline": False,
    }
def _validate_frozen_baseline(view_path: Path, baseline: dict[str, Any]) -> None:
    optimization = _mapping(load_json(view_path), "optimization_view")
    frozen = _mapping(optimization, "v0_baseline")
    measured = _mapping(baseline, "baseline")
    _require(float(frozen.get("required_evidence_budget_recall", -1)) == float(measured["required_evidence_budget_recall"]), "V0 主指标基线漂移")
    _require(float(frozen.get("required_evidence_budget_error_rate", -1)) == float(measured["required_evidence_budget_error_rate"]), "V0 错误率基线漂移")
    protected = _mapping(measured, "protected_metrics")
    for name in ("budget_fit_protection", "identity_stability", "fusion_recall", "fusion_ndcg"):
        _require(float(frozen.get(name, -1)) == float(_mapping(protected, name)["value"]), f"V0 保护指标 {name} 漂移")
    frozen_gate = _mapping(optimization, "frozen_gate")
    for name, value in baseline["large_improvement_gate"].items():
        _require(frozen_gate.get(name) == value, "首方向大幅改善门槛漂移")


def _build_observer(repository: Path, output_root: Path, resume: bool) -> Path:
    tool_root = output_root / "tool"
    tool_root.mkdir(parents=True, exist_ok=True)
    tool = tool_root / ("ownward-frontier.exe" if os.name == "nt" else "ownward-frontier")
    source_identity = canonical_sha256({
        str(path.relative_to(repository)).replace("\\", "/"): file_sha256(path)
        for path in _source_files(repository, ("internal", "cmd/ownward-frontier", "go.mod", "go.sum"))
        if not path.name.endswith("_test.go")
    })
    manifest_path = tool_root / "manifest.json"
    if resume and tool.is_file() and manifest_path.is_file():
        manifest = load_json(manifest_path)
        if manifest.get("source_identity") == source_identity and manifest.get("tool_sha256") == file_sha256(tool):
            return tool
    completed = subprocess.run(
        ["go", "build", "-o", str(tool), "./cmd/ownward-frontier"],
        cwd=repository,
        text=True,
        encoding="utf-8",
        errors="replace",
        capture_output=True,
        timeout=180,
        check=False,
    )
    if completed.returncode != 0:
        raise KernelIterationError(f"构建快速观察器失败: {(completed.stderr or completed.stdout).strip()}")
    atomic_json(manifest_path, {"source_identity": source_identity, "tool_sha256": file_sha256(tool)})
    return tool


def _reuse_json(path: Path, identity_sha: str, resume: bool) -> dict[str, Any] | None:
    if not (resume and path.is_file()):
        return None
    try:
        value = load_json(path)
    except (OSError, ValueError, json.JSONDecodeError):
        return None
    return value if value.get("identity_sha256") == identity_sha else None


def _reuse_observation(
    path: Path,
    candidate: str,
    materials_sha: str,
    input_sha: str,
    tool_sha: str,
    stages: Iterable[str],
    resume: bool,
) -> dict[str, Any] | None:
    if not (resume and path.is_file()):
        return None
    try:
        value = load_json(path)
    except (OSError, ValueError, json.JSONDecodeError):
        return None
    expected = {
        "candidate": candidate,
        "materials_sha256": materials_sha,
        "input_manifest_sha256": input_sha,
        "tool_sha256": tool_sha,
    }
    if any(value.get(key) != expected_value for key, expected_value in expected.items()):
        return None
    return value if set(value.get("requested_stages", [])) == set(stages) else None


def _observation_elapsed(value: dict[str, Any]) -> float:
    try:
        started = datetime.fromisoformat(_python_iso(str(value["started_at"])))
        finished = datetime.fromisoformat(_python_iso(str(value["finished_at"])))
    except (KeyError, ValueError) as error:
        raise KernelIterationError("观察报告缺少可复核墙钟") from error
    elapsed = (finished - started).total_seconds()
    _require(0 <= elapsed <= 180, "观察报告墙钟越界")
    return elapsed


def _python_iso(value: str) -> str:
    normalized = value.replace("Z", "+00:00")
    return re.sub(
        r"\.(\d+)(?=[+-]\d{2}:\d{2}$)",
        lambda match: "." + (match.group(1) + "000000")[:6],
        normalized,
    )


def _validate_view_leakage(view_path: Path, formal_run: Path, pool: dict[str, Any]) -> None:
    view = load_json(view_path)
    optimization = _mapping(view, "optimization_view")
    policy = _mapping(optimization, "leakage_policy")
    _require(all(policy.get(name) is False for name in ("formal_question_text", "formal_answer", "formal_gold_identity", "formal_session_content")), "快速视图泄漏策略无效")
    terms = [
        "Archive AX-41", "Archive BX-72", "Archive CX-15", "Archive DX-90",
        "cobalt-lattice-concept", "moss-relay-concept", "violet-vent-concept", "pearl-lock-concept",
        "极地成像漂移", "港湾音频失真", "温室热偏移", "工坊振动漂移",
        "HELIX-214", "ORBIT-563", "QUARTZ-807", "NOVA-442", "PINE-118",
        "DAWN-31", "NOON-52", "DUSK-74", "EMBER-21", "TIDE-68", "MINT-95",
        "amber relay seal", "cedar transit marker", "lunar workshop latch", "fern observatory transfer",
        "summit transit", "harbor sequence",
    ]
    corpus = "\n".join(
        json.dumps(load_json(Path(item["evidence"]["diagnostic"]["absolute_path"])), ensure_ascii=False)
        for item in pool["problems"]
    )
    _require(all(term not in corpus for term in terms), "内部快速视图与正式诊断表达发生重合")
    for item in pool["problems"]:
        source_path = Path(item["evidence"]["source_asset_log"]["absolute_path"])
        for record in _load_jsonl(source_path):
            content = record.get("value", {}).get("content") if isinstance(record.get("value"), dict) else None
            if isinstance(content, str):
                _require(all(term not in content for term in terms), "内部快速视图与正式源会话事实发生重合")


def _validate_efficiency_authority(view_path: Path, repository: Path, formal_run: Path) -> None:
    gate = _mapping(_mapping(load_json(view_path), "optimization_view"), "frozen_gate")
    if "consumer_retrieval_p95_ms_max" in gate:
        source = str(gate.get("consumer_retrieval_limit_source", "")).split("#", 1)[0]
        path = repository / source
        _require(path.is_file(), "消费检索成本权威阈值来源不存在")
        _require(file_sha256(path) == gate.get("consumer_retrieval_limit_source_sha256"), "消费检索成本权威阈值来源漂移")
        limits = _mapping(load_json(path), "limits")
        _require(float(limits.get("warm_query_p95_ms_max", -1)) == float(gate["consumer_retrieval_p95_ms_max"]), "消费检索成本门槛与权威阈值不一致")
        return
    if "semantic_search_p95_ms_max" in gate:
        optimization = _mapping(load_json(view_path), "optimization_view")
        source = _mapping(optimization, "formal_source")
        report_path = formal_run / "report.json"
        _require(file_sha256(report_path) == source.get("report_sha256"), "语义查询成本的正式报告摘要漂移")
        report = load_json(report_path)
        measured = float(_mapping(report, "retrieval").get("p95_ms", -1))
        _require(math.isclose(measured, float(gate["semantic_search_p95_ms_max"]), abs_tol=1e-6), "语义查询成本门槛与 V0 正式实测不一致")
        return
    source = str(gate.get("query_workflow_limit_source", "")).split("#", 1)[0]
    path = repository / source
    _require(path.is_file(), "查询成本权威阈值来源不存在")
    _require(file_sha256(path) == gate.get("query_workflow_limit_source_sha256"), "查询成本权威阈值来源漂移")
    limits = _mapping(load_json(path), "limits")
    _require(float(limits.get("warm_query_p95_ms_max", -1)) == float(gate.get("query_workflow_p95_ms_max", -2)), "查询成本门槛与权威资源阈值不一致")


def _verify_product_source_equivalence(repository: Path, candidate: str) -> None:
    commands = [
        ["git", "diff", "--quiet", f"{candidate}..HEAD", "--", *PRODUCT_SOURCE_PATHS],
        ["git", "diff", "--quiet", "--", *PRODUCT_SOURCE_PATHS],
        ["git", "diff", "--cached", "--quiet", "--", *PRODUCT_SOURCE_PATHS],
    ]
    for command in commands:
        if subprocess.run(command, cwd=repository, check=False).returncode != 0:
            raise KernelIterationError("当前产品源码与冻结 V0 候选并非逐字等价")


def _closed_granularity_path_equivalence(repository: Path) -> dict[str, Any]:
    paths = (
        "internal/core/evidence.go",
        "internal/derived/evidence.go",
        "internal/domain/evidence.go",
        "internal/retrieval/lexical.go",
        "internal/adapter/mcpserver/server.go",
    )
    digests: dict[str, str] = {}
    for relative in paths:
        current = (repository / relative).read_bytes()
        baseline = subprocess.check_output(["git", "show", f"e6bfc82:{relative}"], cwd=repository)
        _require(current == baseline, f"已关闭的按需证据公共路径发生变化: {relative}")
        digests[relative] = hashlib.sha256(current).hexdigest()
    return {
        "equivalent": True,
        "files": digests,
        "public_consumer_behavior": "protected-by-budget-selection-view-and-adapter-tests",
    }


def _closed_semantic_path_equivalence(repository: Path) -> dict[str, Any]:
    paths = (
        "internal/core/collaboration.go",
        "internal/core/generation.go",
        "internal/core/service.go",
        "internal/derived/store.go",
    )
    digests: dict[str, str] = {}
    for relative in paths:
        current = (repository / relative).read_bytes()
        baseline = subprocess.check_output(["git", "show", f"436e12c:{relative}"], cwd=repository)
        _require(current == baseline, f"已关闭的语义表示路径发生变化: {relative}")
        digests[relative] = hashlib.sha256(current).hexdigest()
    return {"equivalent": True, "files": digests}


def _v0_organization_path_equivalence(repository: Path) -> dict[str, Any]:
    whole_files = (
        "internal/core/collaboration.go",
        "internal/core/generation.go",
        "internal/derived/store.go",
    )
    service_functions = (
        "NewCollaborative", "Create", "CreateBatch", "createAsset", "Maintain",
    )
    file_digests: dict[str, str] = {}
    for relative in whole_files:
        current = (repository / relative).read_bytes()
        baseline = subprocess.check_output(["git", "show", f"{V0_CANDIDATE}:{relative}"], cwd=repository)
        _require(current == baseline, f"V0 组织成本路径发生变化: {relative}")
        file_digests[relative] = hashlib.sha256(current).hexdigest()
    current_service = (repository / "internal/core/service.go").read_text(encoding="utf-8")
    baseline_service = subprocess.check_output(
        ["git", "show", f"{V0_CANDIDATE}:internal/core/service.go"], cwd=repository,
    ).decode("utf-8")
    function_digests: dict[str, str] = {}
    for name in service_functions:
        current = _go_top_level_function(current_service, name)
        baseline = _go_top_level_function(baseline_service, name)
        _require(current == baseline, f"V0 组织成本函数发生变化: Service.{name}")
        function_digests[name] = hashlib.sha256(current.encode("utf-8")).hexdigest()
    return {"equivalent": True, "files": file_digests, "service_functions": function_digests}


def _go_top_level_function(source: str, name: str) -> str:
    pattern = re.compile(rf"(?m)^func (?:\([^\n]+\) )?{re.escape(name)}\(")
    match = pattern.search(source)
    _require(match is not None, f"Go 函数不存在: {name}")
    next_match = re.search(r"(?m)^func ", source[match.end():])
    end = len(source) if next_match is None else match.end() + next_match.start()
    return source[match.start():end].rstrip()


def _product_tree_sha256(repository: Path, candidate: str) -> str:
    encoded = subprocess.check_output(["git", "ls-tree", "-r", candidate, "--", *PRODUCT_SOURCE_PATHS], cwd=repository)
    return hashlib.sha256(encoded).hexdigest()


def _worktree_product_sha256(repository: Path) -> str:
    return _worktree_source_sha256(repository, PRODUCT_SOURCE_PATHS)


def _worktree_source_sha256(repository: Path, roots: Iterable[str]) -> str:
    files = _source_files(repository, roots)
    _require(bool(files), "当前工作树没有可绑定的产品源码")
    digest = hashlib.sha256()
    for path in files:
        relative = path.relative_to(repository).as_posix().encode("utf-8")
        digest.update(len(relative).to_bytes(4, "little"))
        digest.update(relative)
        encoded = path.read_bytes()
        digest.update(len(encoded).to_bytes(8, "little"))
        digest.update(encoded)
    return digest.hexdigest()


def _source_files(repository: Path, roots: Iterable[str]) -> list[Path]:
    result: set[Path] = set()
    for root in roots:
        path = repository / root
        if path.is_file():
            result.add(path)
        elif path.is_dir():
            result.update(candidate for candidate in path.rglob("*") if candidate.is_file())
    return sorted(result, key=lambda path: path.relative_to(repository).as_posix())


def _derived_state_observation(path: Path, expected_ids: list[str]) -> dict[str, Any]:
    latest: dict[str, tuple[dict[str, Any], int]] = {}
    oversized: set[str] = set()
    record_count = 0
    with path.open("rb") as stream:
        while True:
            header = stream.read(16)
            if not header:
                break
            _require(len(header) == 16 and header[:4] == b"OWD3", "派生状态记录头无效")
            metadata_length, vector_length, checksum = struct.unpack("<III", header[4:])
            _require(metadata_length > 0 and vector_length % 4 == 0, "派生状态记录长度无效")
            payload = stream.read(metadata_length + vector_length)
            footer = stream.read(4)
            _require(len(payload) == metadata_length + vector_length and footer == b"DONE", "派生状态记录被截断")
            _require(zlib.crc32(payload) & 0xFFFFFFFF == checksum, "派生状态记录校验失败")
            metadata = json.loads(payload[:metadata_length])
            _require(isinstance(metadata, dict) and isinstance(metadata.get("asset_id"), str), "派生状态元数据无效")
            asset_id = metadata["asset_id"]
            error = str(metadata.get("error", ""))
            if "embedding_input_exceeds_runtime_batch" in error or (
                "is too large to process" in error and "physical batch size" in error
            ):
                oversized.add(asset_id)
            latest[asset_id] = (metadata, vector_length // 4)
            record_count += 1
    expected = set(expected_ids)
    _require(expected <= set(latest), "派生状态缺少期望资产")
    expected_vectors = sum(latest[item][1] > 0 for item in expected)
    return {
        "derived_record_count": record_count,
        "latest_asset_count": len(latest),
        "latest_vector_count": sum(vector_length > 0 for _, vector_length in latest.values()),
        "expected_asset_vector_count": expected_vectors,
        "all_assets_have_oversized_embedding_failure": set(latest) <= oversized,
        "all_expected_have_oversized_embedding_failure": expected <= oversized,
        "all_expected_semantic_results_submitted": all(latest[item][0].get("semantic_result") is not None for item in expected),
        "latest_relation_count": sum(len(_mapping(metadata.get("analysis", {}), "analysis").get("relations", [])) for metadata, _ in latest.values()),
    }


def _asset_content_lengths(path: Path) -> dict[str, int]:
    result: dict[str, int] = {}
    for record in _load_jsonl(path):
        if record.get("operation") not in {"create", "update"}:
            continue
        value = record.get("value")
        if isinstance(value, dict) and isinstance(value.get("id"), str) and isinstance(value.get("content"), str):
            result[value["id"]] = len(value["content"])
    return result


def _relative_evidence(path: Path, root: Path) -> dict[str, str]:
    return {
        "path": path.resolve().relative_to(root.resolve()).as_posix(),
        "absolute_path": path.resolve().as_posix(),
        "sha256": file_sha256(path),
    }


def _load_jsonl(path: Path) -> list[dict[str, Any]]:
    values: list[dict[str, Any]] = []
    with path.open("r", encoding="utf-8") as stream:
        for line_number, line in enumerate(stream, 1):
            if not line.strip():
                continue
            value = json.loads(line)
            _require(isinstance(value, dict), f"{path}:{line_number} 必须是 JSON 对象")
            values.append(value)
    return values


def _all_keys(value: Any) -> Iterable[str]:
    if isinstance(value, dict):
        for key, nested in value.items():
            yield str(key)
            yield from _all_keys(nested)
    elif isinstance(value, list):
        for nested in value:
            yield from _all_keys(nested)


def _strings(value: Any, label: str) -> list[str]:
    _require(isinstance(value, list) and all(isinstance(item, str) for item in value), f"{label}无效")
    return list(value)


def _mapping(value: Any, name: str) -> dict[str, Any]:
    if isinstance(value, dict) and name in value and isinstance(value[name], dict):
        return value[name]
    if isinstance(value, dict) and name not in value:
        return value
    raise KernelIterationError(f"{name} 必须是对象")


def load_json(path: Path) -> dict[str, Any]:
    value = json.loads(path.read_text(encoding="utf-8"))
    _require(isinstance(value, dict), f"{path} 必须是 JSON 对象")
    return value


def file_sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def canonical_sha256(value: Any) -> str:
    encoded = json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def atomic_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(json.dumps(value, ensure_ascii=False, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    temporary.replace(path)


def _command_output(command: list[str], cwd: Path) -> str:
    return subprocess.check_output(command, cwd=cwd, text=True, encoding="utf-8", errors="replace").strip()


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise KernelIterationError(message)
