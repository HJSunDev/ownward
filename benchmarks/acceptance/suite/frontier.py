from __future__ import annotations

import json
import time
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from contract import load_contract, validate_report
from materials import load_json, validate_materials


class FrontierError(ValueError):
    pass


DIMENSIONS = {"quality", "latency", "resources"}
STAGES = {
    "identity",
    "relations",
    "merge_split",
    "incremental_consistency",
    "organization",
    "indexing",
    "lexical",
    "vector",
    "graph",
    "context",
    "fusion",
}


def compare(
    contract: dict[str, Any],
    baseline: dict[str, Any] | None,
    candidate: dict[str, Any],
    calibration: dict[str, Any],
) -> dict[str, Any]:
    validate_observation(contract, candidate)
    if baseline is None:
        return _report(contract, candidate, None, "bootstrap_reference", [], [], calibration)
    validate_observation(contract, baseline)
    for field in ("materials_sha256", "input_manifest_sha256", "environment"):
        _require(candidate.get(field) == baseline.get(field), f"候选与基线的 {field} 不一致")
    if candidate["mode"] == "full":
        _require(baseline.get("mode") == "full", "完整模式必须与完整基线比较")
    baseline_metrics = {item["name"]: item for item in baseline["metrics"]}
    candidate_metrics = {item["name"]: item for item in candidate["metrics"]}
    if candidate["mode"] == "full":
        _require(set(candidate_metrics) == set(baseline_metrics), "完整模式的候选与基线指标集合不一致")
    else:
        _require(set(candidate_metrics) <= set(baseline_metrics), "定向模式包含基线不存在的指标")
    regressions: list[dict[str, Any]] = []
    improvements: list[dict[str, Any]] = []
    for name, current in candidate_metrics.items():
        previous = baseline_metrics[name]
        for field in ("dimension", "stage", "direction", "protected"):
            _require(current.get(field) == previous.get(field), f"指标 {name} 的 {field} 发生变化")
        delta = _oriented_delta(previous, current)
        error = max(float(previous.get("repeatability_error", 0)), float(current.get("repeatability_error", 0)))
        materiality = float(current.get("materiality", 0))
        evidence = {
            "name": name,
            "dimension": current["dimension"],
            "stage": current["stage"],
            "baseline": previous["value"],
            "candidate": current["value"],
            "oriented_delta": delta,
        }
        if current.get("protected") and delta < -error:
            regressions.append(evidence)
        if delta >= materiality > 0:
            improvements.append(evidence)
    if regressions:
        decision = "rejected_regression"
    elif not improvements:
        decision = "rejected_no_material_improvement"
    else:
        decision = "eligible_for_qualification"
    return _report(contract, candidate, str(baseline["candidate"]), decision, regressions, improvements, calibration)


def validate_observation(contract: dict[str, Any], value: dict[str, Any]) -> None:
    _require(value.get("schema") == "ownward.core-frontier-observation/v1", "观察报告 schema 无效")
    _require(value.get("suite_version") == contract.get("suite_version"), "观察报告体系版本无效")
    _require(value.get("mode") in {"targeted", "full"}, "观察报告模式无效")
    _require(bool(value.get("candidate")) and bool(value.get("materials_sha256")), "观察报告缺少身份绑定")
    _require(bool(value.get("input_manifest_sha256")), "观察报告缺少验收输入绑定")
    _require(isinstance(value.get("tool_sha256"), str) and len(value["tool_sha256"]) == 64, "观察报告缺少执行文件绑定")
    _require(isinstance(value.get("environment"), dict) and value["environment"], "观察报告缺少环境身份")
    metrics = value.get("metrics")
    requested_stages = value.get("requested_stages")
    _require(
        isinstance(requested_stages, list)
        and requested_stages
        and len(requested_stages) == len(set(requested_stages))
        and set(requested_stages) <= STAGES,
        "观察报告执行阶段无效",
    )
    _require(isinstance(metrics, list) and metrics, "观察报告没有指标")
    names: set[str] = set()
    dimensions: set[str] = set()
    stages: set[str] = set()
    for item in metrics:
        _require(isinstance(item, dict), "指标必须是对象")
        name = item.get("name")
        _require(isinstance(name, str) and name and name not in names, "指标名称无效或重复")
        _require(item.get("dimension") in DIMENSIONS, f"指标 {name} 的维度无效")
        _require(item.get("stage") in STAGES, f"指标 {name} 的阶段无效")
        _require(item.get("direction") in {"higher", "lower"}, f"指标 {name} 的方向无效")
        _require(isinstance(item.get("value"), (int, float)), f"指标 {name} 缐少数值")
        _require(float(item.get("repeatability_error", 0)) >= 0, f"指标 {name} 重复误差无效")
        _require(float(item.get("materiality", 0)) >= 0, f"指标 {name} 有效改善阈值无效")
        names.add(name)
        dimensions.add(item["dimension"])
        stages.add(item["stage"])
    if value.get("mode") == "full":
        _require(dimensions == DIMENSIONS, "完整观察报告必须分别提供质量、时延和资源证据")
        _require(stages == STAGES, "完整观察报告没有覆盖全部受保护阶段")
    _require(stages == set(requested_stages), "观察指标与声明的执行阶段不一致")


def run_self_check(suite_root: Path, output: Path | None = None) -> dict[str, Any]:
    started = time.perf_counter()
    contract = load_contract(suite_root / "contract.json")
    materials_root = suite_root / "materials"
    manifest = validate_materials(materials_root)
    material_sha = next(
        item["sha256"] for item in manifest["files"] if item["path"].endswith("core/v1/dataset.json")
    )
    dataset = load_json(materials_root / "core" / "v1" / "dataset.json")
    metrics = _fixture_metrics(dataset)
    calibration = load_json(materials_root / "frontier" / "v1" / "calibration.json")
    mode_elapsed: dict[str, float] = {}
    report: dict[str, Any] | None = None
    for mode in ("targeted", "full"):
        mode_started = time.perf_counter()
        baseline = _observation(contract, "self-check-baseline", material_sha, metrics, mode)
        candidate_metrics = [dict(item) for item in metrics]
        next(item for item in candidate_metrics if item["name"] == "fusion_recall")["value"] += 0.02
        candidate = _observation(contract, "self-check-candidate", material_sha, candidate_metrics, mode)
        report = compare(contract, baseline, candidate, calibration)
        validate_report(contract, "frontier", report)
        _require(report["decision"] == "eligible_for_qualification", f"{mode} 体系自检未识别整体改善")
        _require(report.get("baseline_promoted") is False, "体系自检不得晋升正式基线")
        mode_elapsed[mode] = time.perf_counter() - mode_started
    assert report is not None
    report["self_check"] = True
    report["formal_evidence"] = False
    report["mode_elapsed_seconds"] = mode_elapsed
    report["elapsed_seconds"] = time.perf_counter() - started
    if output is not None:
        output.parent.mkdir(parents=True, exist_ok=True)
        output.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    return report


def _fixture_metrics(dataset: dict[str, Any]) -> list[dict[str, Any]]:
    assets = dataset["assets"]
    relations = dataset["truth"]["relations"]
    queries = dataset["queries"]
    embeddings = dataset["frozen_embeddings"]
    return [
        _metric("identity_stability", "quality", "identity", 1.0, "higher", 0.0, 0.001),
        _metric("relation_precision", "quality", "relations", 1.0 if relations else 0.0, "higher", 0.0, 0.001),
        _metric("merge_split_integrity", "quality", "merge_split", 1.0, "higher", 0.0, 0.001),
        _metric("incremental_consistency", "quality", "incremental_consistency", 1.0, "higher", 0.0, 0.001),
        _metric("lexical_recall", "quality", "lexical", 0.90, "higher", 0.001, 0.005),
        _metric("vector_recall", "quality", "vector", 0.90 if embeddings else 0.0, "higher", 0.001, 0.005),
        _metric("graph_recall", "quality", "graph", 0.90 if relations else 0.0, "higher", 0.001, 0.005),
        _metric("context_precision", "quality", "context", 0.90, "higher", 0.001, 0.005),
        _metric("fusion_recall", "quality", "fusion", 0.95 if queries else 0.0, "higher", 0.001, 0.005),
        _metric("fusion_ndcg", "quality", "fusion", 0.95 if queries else 0.0, "higher", 0.001, 0.005),
        _metric("organization_p95_ms", "latency", "organization", float(len(assets)), "lower", 0.5, 2.0),
        _metric("index_build_ms", "latency", "indexing", float(len(assets)) / 2, "lower", 0.5, 2.0),
        _metric("query_p95_ms", "latency", "fusion", float(len(queries)), "lower", 0.5, 2.0),
        _metric("derived_bytes", "resources", "organization", float(len(json.dumps(dataset))), "lower", 32.0, 128.0),
        _metric("index_bytes", "resources", "indexing", float(len(embeddings) * 16 * 8), "lower", 16.0, 64.0),
    ]


def _metric(name: str, dimension: str, stage: str, value: float, direction: str, error: float, materiality: float) -> dict[str, Any]:
    return {
        "name": name,
        "dimension": dimension,
        "stage": stage,
        "value": value,
        "direction": direction,
        "repeatability_error": error,
        "materiality": materiality,
        "protected": True,
    }


def _observation(contract: dict[str, Any], candidate: str, materials_sha: str, metrics: list[dict[str, Any]], mode: str = "full") -> dict[str, Any]:
    return {
        "schema": "ownward.core-frontier-observation/v1",
        "suite_version": contract["suite_version"],
        "candidate": candidate,
        "materials_sha256": materials_sha,
        "input_manifest_sha256": "0" * 64,
        "mode": mode,
        "requested_stages": sorted({item["stage"] for item in metrics}),
        "environment": {"kind": "isolated-self-check", "version": "1"},
        "tool_sha256": "0" * 64,
        "metrics": metrics,
    }


def _oriented_delta(previous: dict[str, Any], current: dict[str, Any]) -> float:
    raw = float(current["value"]) - float(previous["value"])
    return raw if current["direction"] == "higher" else -raw


def _report(contract: dict[str, Any], candidate: dict[str, Any], baseline: str | None, decision: str, regressions: list[dict[str, Any]], improvements: list[dict[str, Any]], calibration: dict[str, Any]) -> dict[str, Any]:
    now = datetime.now(timezone.utc).isoformat()
    by_dimension = {
        dimension: [item for item in candidate["metrics"] if item["dimension"] == dimension]
        for dimension in DIMENSIONS
    }
    return {
        "schema": contract["optimization_loop"]["output_schema"],
        "suite_version": contract["suite_version"],
        "benchmark_version": contract["optimization_loop"]["benchmark_version"],
        "mode": candidate["mode"],
        "candidate": candidate["candidate"],
        "baseline": baseline,
        "environment": candidate["environment"],
        "inputs": {"sha256": candidate["input_manifest_sha256"], "materials_sha256": candidate["materials_sha256"]},
        "quality": by_dimension["quality"],
        "latency": by_dimension["latency"],
        "resources": by_dimension["resources"],
        "diagnostics": {
            "stages": sorted({item["stage"] for item in candidate["metrics"]}),
            "regressions": regressions,
            "improvements": improvements,
            "external_frontier": calibration,
        },
        "decision": decision,
        "qualification_required": decision == "eligible_for_qualification",
        "baseline_promoted": False,
        "started_at": now,
        "finished_at": now,
    }


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise FrontierError(message)
