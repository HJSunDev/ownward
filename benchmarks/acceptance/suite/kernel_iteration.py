from __future__ import annotations

import hashlib
import json
import math
import os
import platform
import re
import subprocess
import time
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
VIEW_RELATIVE = Path("materials/optimization/v1/direction-organization-granularity.json")
CORE_RELATIVE = Path("materials/core/v1/dataset.json")
PRODUCT_SOURCE_PATHS = ("internal", "cmd/ownward", "go.mod", "go.sum")


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
        candidate_identity = f"worktree:{source_identity}"
    organization_equivalence = _v0_organization_path_equivalence(repository)
    _require(organization_equivalence["equivalent"] is True, "候选改动了 V0 组织或重建成本路径，不能复用同输入基线")
    output_root.mkdir(parents=True, exist_ok=True)

    diagnostics_path = formal_run / "diagnostics.jsonl"
    report_path = formal_run / "report.json"
    view_path = suite_root / VIEW_RELATIVE
    core_path = suite_root / CORE_RELATIVE
    for path in (diagnostics_path, report_path, view_path, core_path):
        _require(path.is_file(), f"迭代输入不存在: {path}")
    formal_input_identity = {
        "candidate": V0_CANDIDATE,
        "diagnostics_sha256": file_sha256(diagnostics_path),
        "formal_report_sha256": file_sha256(report_path),
        "view_sha256": file_sha256(view_path),
        "core_protection_sha256": file_sha256(core_path),
        "analyzer_sha256": file_sha256(Path(__file__)),
        "algorithm": "kernel-v1-first-direction/v1",
    }
    input_identity = {
        **formal_input_identity,
        "evaluation_candidate": candidate_identity,
        "evaluation_product_source_sha256": source_identity,
        "v0_organization_path_equivalence": organization_equivalence,
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
    _validate_efficiency_authority(view_path, repository)

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
    specs = {
        "direction": (view_path, ("organization", "indexing", "lexical", "vector", "graph", "context", "fusion", "efficiency")),
        "protection": (core_path, ("identity", "relations", "merge_split", "incremental_consistency", "context", "fusion")),
    }
    for name, (materials, stages) in specs.items():
        path = output_root / f"{name}-observation.json"
        existing = _reuse_observation(path, candidate_identity, file_sha256(materials), observer_input_sha, tool_sha, stages, resume)
        if existing is not None:
            observations[name] = existing
            observation_reused[name] = True
            durations[name] = 0.0
            continue
        started = time.perf_counter()
        command = [
            str(tool),
            "--materials", str(materials),
            "--candidate", candidate_identity,
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
    _require(sum(durations.values()) <= 600, "方向视图与保护检查超过 10 分钟")

    measured_durations = {
        name: durations[name] if not observation_reused[name] else _observation_elapsed(observations[name])
        for name in observations
    }
    result = build_baseline(input_identity, pool, observations, measured_durations) if is_v0_baseline else build_candidate_result(
        input_identity, pool, observations, measured_durations, load_json(view_path), candidate_identity,
    )
    result["identity_sha256"] = canonical_sha256({
        "input": input_identity,
        "problem_pool": file_sha256(pool_path),
        "direction_observation": file_sha256(output_root / "direction-observation.json"),
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
            "cluster": "session_unit_context_overflow",
            "direction": "information_representation_and_organization",
            "reason": "highest-proven-impact-earliest-causal-point",
            "mapped_failures": sum(item["mechanism"] == "session_unit_context_overflow" for item in problems),
            "next_validation": "organization-granularity-v1",
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
    mechanism, direction, status, samples = classify_problem(
        gap,
        semantic_complete,
        organized_expected,
        all_expected_returned,
        expected_chars,
        str(diagnostic.get("capability")),
    )
    diagnostic_path = question_root / "diagnostic.json"
    retrieval_ref = _mapping(artifacts, "retrieval")
    retrieval_path = Path(str(retrieval_ref.get("path", "")))
    _require(diagnostic_path.is_file() and retrieval_path.is_file(), "问题诊断证据不完整")
    _require(file_sha256(retrieval_path) == retrieval_ref.get("sha256"), "检索证据摘要不匹配")
    return {
        "formal_question_identity": str(diagnostic.get("question_identity")),
        "formal_question_id": str(diagnostic.get("question_id")),
        "question_type": str(diagnostic.get("question_type")),
        "capability": str(diagnostic.get("capability")),
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
        },
        "evidence": {
            "diagnostic": _relative_evidence(diagnostic_path, formal_run),
            "retrieval": _relative_evidence(retrieval_path, formal_run),
            "source_asset_log": _relative_evidence(source_log, formal_run),
        },
    }


def classify_problem(
    gap: str,
    semantic_complete: bool,
    organized_expected: bool,
    all_expected_returned: bool,
    expected_chars: int,
    capability: str,
) -> tuple[str, str, str, list[str]]:
    if gap == "target_evidence_not_read" and semantic_complete and organized_expected and all_expected_returned and expected_chars > CONTEXT_BUDGET_CHARS:
        return (
            "session_unit_context_overflow",
            "information_representation_and_organization",
            "proven_general_mechanism",
            ["granularity-multi-source", f"granularity-{capability}"],
        )
    if gap == "target_evidence_not_read":
        return (
            "pending_search_order_or_context_selection",
            "retrieval_architecture_and_algorithm",
            "pending_additional_evidence",
            ["protection-budget-fit-ordering"],
        )
    if gap == "target_evidence_not_search_returned":
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
        _require(counts["pending_search_order_or_context_selection"] == 14, "预算内未读取问题规模漂移")
        _require(counts["pending_semantic_organization_or_retrieval"] == 116, "未检索问题规模漂移")
        _require(counts["reader_failure_after_complete_evidence"] == 9, "Reader 边界问题规模漂移")


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
    return re.sub(r"(\.\d{6})\d+(?=Z|[+-]\d{2}:\d{2}$)", r"\1", value).replace("Z", "+00:00")


def _validate_view_leakage(view_path: Path, formal_run: Path, pool: dict[str, Any]) -> None:
    view = load_json(view_path)
    optimization = _mapping(view, "optimization_view")
    policy = _mapping(optimization, "leakage_policy")
    _require(all(policy.get(name) is False for name in ("formal_question_text", "formal_answer", "formal_gold_identity", "formal_session_content")), "快速视图泄漏策略无效")
    terms = ["远岫观测计划", "潮汐温室", "纸鸢工坊", "苔原邮局", "雾桥灯塔"]
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


def _validate_efficiency_authority(view_path: Path, repository: Path) -> None:
    gate = _mapping(_mapping(load_json(view_path), "optimization_view"), "frozen_gate")
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
    files = _source_files(repository, PRODUCT_SOURCE_PATHS)
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
