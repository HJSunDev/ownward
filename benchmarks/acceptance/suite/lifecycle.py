from __future__ import annotations

import hashlib
import json
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from contract import validate_report
from evidence import validate_layer_report, validate_report_artifacts


class LifecycleError(ValueError):
    pass


IMPACT_PLAN = {
    "local": [],
    "asset": ["core"],
    "retrieval": ["targeted", "frontier"],
    "organization": ["targeted", "core", "frontier", "qualification"],
    "candidate": ["targeted", "core", "frontier", "qualification", "full", "longmemeval", "summarize"],
}

IMPACT_STAGES = {
    "local": [],
    "asset": ["identity", "incremental_consistency", "organization", "indexing"],
    "retrieval": ["lexical", "vector", "graph", "context", "fusion", "indexing"],
    "organization": [
        "relations", "merge_split", "incremental_consistency", "organization", "indexing",
        "lexical", "vector", "graph", "context", "fusion",
    ],
    "candidate": [
        "identity", "relations", "merge_split", "incremental_consistency", "organization", "indexing",
        "lexical", "vector", "graph", "context", "fusion",
    ],
}

REPORT_KIND = {
    "targeted": "frontier",
    "frontier": "frontier",
    "core": "core",
    "qualification": "product",
    "full": "product",
    "longmemeval": "community",
    "summarize": "suite",
}


def new_state(contract: dict[str, Any], binding: dict[str, str]) -> dict[str, Any]:
    required = contract["execution"]["binding_fields"] + ["tool_sha256"]
    _require(set(binding) == set(required), "候选绑定字段不完整")
    _require(binding["suite_version"] == contract["suite_version"], "候选绑定的体系版本无效")
    for field in required:
        _require(bool(binding[field]), f"候选绑定缺少 {field}")
    return {
        "schema": "ownward.acceptance-state/v1",
        "suite_version": contract["suite_version"],
        "binding": dict(binding),
        "checkpoints": {},
        "invalidated_reports": {},
        "baseline": None,
        "baseline_history": [],
    }


def plan_for_impacts(impacts: list[str]) -> list[str]:
    _require(impacts, "至少需要一个变更影响范围")
    unknown = set(impacts) - set(IMPACT_PLAN)
    _require(not unknown, f"未知变更影响范围: {', '.join(sorted(unknown))}")
    wanted = {mode for impact in impacts for mode in IMPACT_PLAN[impact]}
    order = ["targeted", "core", "frontier", "qualification", "full", "longmemeval", "summarize"]
    return [mode for mode in order if mode in wanted]


def stages_for_impacts(impacts: list[str]) -> list[str]:
    plan_for_impacts(impacts)
    wanted = {stage for impact in impacts for stage in IMPACT_STAGES[impact]}
    order = [
        "identity", "relations", "merge_split", "incremental_consistency", "organization", "indexing",
        "lexical", "vector", "graph", "context", "fusion",
    ]
    return [stage for stage in order if stage in wanted]


def can_start(contract: dict[str, Any], state: dict[str, Any], mode: str) -> None:
    _validate_state(contract, state)
    _require(mode in REPORT_KIND or mode == "self-check", f"未知执行模式: {mode}")
    prerequisites = contract["execution"].get("prerequisites", {}).get(mode, [])
    for prerequisite in prerequisites:
        checkpoint = state["checkpoints"].get(prerequisite)
        _require(isinstance(checkpoint, dict), f"{mode} 缺少前置证据 {prerequisite}")
        _require(checkpoint.get("passed") is True, f"{mode} 的前置证据 {prerequisite} 未通过")
        _checkpoint_report(contract, state, prerequisite)


def record(
    contract: dict[str, Any],
    state: dict[str, Any],
    mode: str,
    report: dict[str, Any],
    report_sha256: str,
    elapsed_seconds: float,
    report_path: str = "",
) -> str:
    can_start(contract, state, mode)
    _require(mode != "self-check", "体系自检不得写入正式检查点")
    _validate_report_for_mode(contract, state, mode, report)
    maximum = _max_wall_seconds(contract, mode)
    if maximum is not None:
        _require(elapsed_seconds <= maximum, f"{mode} 超出成本上限")
    _require(bool(report_path), f"{mode} 正式检查点缺少落盘报告")
    path = Path(report_path)
    _require(path.is_file() and file_sha256(path) == report_sha256, f"{mode} 落盘报告缺失或摘要不一致")
    try:
        persisted = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise LifecycleError(f"{mode} 落盘报告无法读取") from error
    _require(persisted == report, f"{mode} 内存报告与落盘报告不一致")
    artifact_manifest_sha256 = validate_report_artifacts(path, report)
    existing = state["checkpoints"].get(mode)
    if (
        existing
        and existing.get("report_sha256") == report_sha256
        and existing.get("binding") == state["binding"]
        and existing.get("artifact_manifest_sha256", "") == artifact_manifest_sha256
    ):
        return "reused"
    state["checkpoints"][mode] = {
        "binding": dict(state["binding"]),
        "report_sha256": report_sha256,
        "passed": report.get("passed", report.get("decision") in {"eligible_for_qualification", "bootstrap_reference"}),
        "elapsed_seconds": elapsed_seconds,
        "report_path": report_path,
        "artifact_manifest_sha256": artifact_manifest_sha256,
        "recorded_at": datetime.now(timezone.utc).isoformat(),
    }
    state["invalidated_reports"].pop(mode, None)
    return "recorded"


def invalidate(contract: dict[str, Any], state: dict[str, Any], mode: str) -> list[str]:
    _validate_state(contract, state)
    affected = contract["execution"]["invalidation"].get(mode)
    _require(isinstance(affected, list), f"模式 {mode} 没有失效传播规则")
    removed = [name for name in affected if name in state["checkpoints"]]
    for name in removed:
        checkpoint = state["checkpoints"][name]
        digest = checkpoint.get("report_sha256")
        if isinstance(digest, str) and digest:
            state["invalidated_reports"][name] = digest
        del state["checkpoints"][name]
    return removed


def rebind(contract: dict[str, Any], state: dict[str, Any], binding: dict[str, str]) -> list[str]:
    _validate_state(contract, state)
    required = contract["execution"]["binding_fields"] + ["tool_sha256"]
    _require(set(binding) == set(required), "新候选绑定字段不完整")
    _require(binding["suite_version"] == contract["suite_version"], "新候选绑定的体系版本无效")
    reusable = contract["execution"]["reuse_requires_equal"]
    changed = [field for field in reusable if binding.get(field) != state["binding"].get(field)]
    if not changed:
        return []
    removed = sorted(state["checkpoints"])
    if state.get("baseline") is not None and set(changed) & {"environment_sha256", "input_manifest_sha256", "tool_sha256"}:
        state.setdefault("baseline_history", []).append(state["baseline"])
        state["baseline"] = None
    state["binding"] = dict(binding)
    state["checkpoints"].clear()
    state["invalidated_reports"].clear()
    return removed


def report_was_invalidated(state: dict[str, Any], mode: str, path: Path) -> bool:
    digest = state.get("invalidated_reports", {}).get(mode)
    return isinstance(digest, str) and path.is_file() and file_sha256(path) == digest


def promote_baseline(contract: dict[str, Any], state: dict[str, Any]) -> None:
    frontier = state["checkpoints"].get("frontier")
    qualification = state["checkpoints"].get("qualification")
    _require(frontier is not None and frontier.get("passed") is True, "有效基线晋升要求前沿完整模式通过")
    _require(qualification is not None and qualification.get("passed") is True, "有效基线晋升要求资格验证通过")
    frontier_report = _checkpoint_report(contract, state, "frontier")
    qualification_report = _checkpoint_report(contract, state, "qualification")
    if state.get("baseline") is not None:
        state.setdefault("baseline_history", []).append(state["baseline"])
    state["baseline"] = {
        "candidate": state["binding"]["candidate"],
        "binary_sha256": state["binding"]["binary_sha256"],
        "environment_sha256": state["binding"]["environment_sha256"],
        "input_manifest_sha256": state["binding"]["input_manifest_sha256"],
        "tool_sha256": state["binding"]["tool_sha256"],
        "frontier_report_sha256": frontier["report_sha256"],
        "qualification_report_sha256": qualification["report_sha256"],
        "reports": {
            "frontier": {"canonical_sha256": canonical_sha256(frontier_report), "value": frontier_report},
            "qualification": {"canonical_sha256": canonical_sha256(qualification_report), "value": qualification_report},
        },
        "promoted_at": datetime.now(timezone.utc).isoformat(),
    }
    observations: dict[str, dict[str, Any]] = {}
    if frontier.get("observation_path") and frontier.get("observation_sha256"):
        path = Path(frontier["observation_path"])
        _require(path.is_file() and file_sha256(path) == frontier["observation_sha256"], "基线观察证据缺失或变化")
        value = json.loads(path.read_text(encoding="utf-8"))
        _require(isinstance(value, dict), "基线观察证据必须是对象")
        observations["full"] = {
            "source_sha256": frontier["observation_sha256"],
            "canonical_sha256": canonical_sha256(value),
            "value": value,
        }
    if observations:
        state["baseline"]["observations"] = observations


def reusable_report(contract: dict[str, Any], state: dict[str, Any], mode: str) -> Path | None:
    checkpoint = state.get("checkpoints", {}).get(mode)
    if not isinstance(checkpoint, dict) or checkpoint.get("binding") != state.get("binding"):
        return None
    value = str(checkpoint.get("report_path", "")).strip()
    if not value:
        return None
    path = Path(value)
    if not path.is_file() or file_sha256(path) != checkpoint.get("report_sha256"):
        return None
    try:
        report = json.loads(path.read_text(encoding="utf-8"))
        _require(isinstance(report, dict), f"{mode} 检查点报告必须是对象")
        _validate_report_for_mode(contract, state, mode, report)
        if mode == "summarize":
            for dependency in ("core", "full", "longmemeval"):
                _checkpoint_report(contract, state, dependency)
        manifest_sha256 = validate_report_artifacts(path, report)
        _require(manifest_sha256 == checkpoint.get("artifact_manifest_sha256"), f"{mode} 原始证据清单与检查点不一致")
    except (OSError, json.JSONDecodeError, ValueError):
        return None
    return path


def summarize(contract: dict[str, Any], state: dict[str, Any]) -> dict[str, Any]:
    can_start(contract, state, "summarize")
    checkpoints = state["checkpoints"]
    for mode in ("core", "full", "longmemeval"):
        _checkpoint_report(contract, state, mode)
    report = {
        "schema": contract["reports"]["suite"]["schema"],
        "suite_version": contract["suite_version"],
        "candidate": state["binding"]["candidate"],
        "binary_sha256": state["binding"]["binary_sha256"],
        "environment": {"sha256": state["binding"]["environment_sha256"]},
        "inputs": {"sha256": state["binding"]["input_manifest_sha256"]},
        "tool_sha256": state["binding"]["tool_sha256"],
        "core_report_sha256": checkpoints["core"]["report_sha256"],
        "product_report_sha256": checkpoints["full"]["report_sha256"],
        "community_report_sha256": checkpoints["longmemeval"]["report_sha256"],
        "passed": all(checkpoints[name]["passed"] for name in ("core", "full", "longmemeval")),
        "created_at": datetime.now(timezone.utc).isoformat(),
    }
    validate_report(contract, "suite", report)
    return report


def _checkpoint_report(contract: dict[str, Any], state: dict[str, Any], mode: str) -> dict[str, Any]:
    checkpoint = state.get("checkpoints", {}).get(mode)
    _require(isinstance(checkpoint, dict), f"缺少 {mode} 检查点")
    path = Path(str(checkpoint.get("report_path", "")))
    _require(path.is_file(), f"{mode} 检查点报告缺失")
    _require(file_sha256(path) == checkpoint.get("report_sha256"), f"{mode} 检查点报告发生变化")
    try:
        report = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise LifecycleError(f"{mode} 检查点报告无法读取") from error
    _require(isinstance(report, dict), f"{mode} 检查点报告必须是对象")
    _validate_report_for_mode(contract, state, mode, report)
    manifest_sha256 = validate_report_artifacts(path, report)
    _require(manifest_sha256 == checkpoint.get("artifact_manifest_sha256"), f"{mode} 原始证据清单与检查点不一致")
    return report


def load_state(path: Path) -> dict[str, Any]:
    value = json.loads(path.read_text(encoding="utf-8"))
    _require(isinstance(value, dict), "状态文件必须是对象")
    value.setdefault("invalidated_reports", {})
    return value


def save_state(path: Path, state: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(json.dumps(state, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    temporary.replace(path)


def file_sha256(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def canonical_sha256(value: Any) -> str:
    encoded = json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def _validate_report_for_mode(contract: dict[str, Any], state: dict[str, Any], mode: str, report: dict[str, Any]) -> None:
    kind = REPORT_KIND[mode]
    if kind in {"core", "product", "community"}:
        validate_layer_report(contract, kind, report, expected_binding=state["binding"])
    else:
        validate_report(contract, kind, report)
        _require(report.get("candidate") == state["binding"]["candidate"], f"{mode} 报告候选身份不一致")
    if mode in {"targeted", "frontier"}:
        _require(report.get("mode") == ("targeted" if mode == "targeted" else "full"), f"{mode} 报告模式无效")
    if mode in {"qualification", "full"}:
        _require(report.get("mode") == mode, f"{mode} 报告模式无效")
    environment = report.get("environment", {})
    inputs = report.get("inputs", {})
    _require(environment.get("sha256") == state["binding"]["environment_sha256"], f"{mode} 报告环境绑定不一致")
    _require(inputs.get("sha256") == state["binding"]["input_manifest_sha256"], f"{mode} 报告输入绑定不一致")
    if mode == "summarize":
        _require(report.get("tool_sha256") == state["binding"]["tool_sha256"], "汇总报告工具绑定不一致")
        fields = {
            "core_report_sha256": "core",
            "product_report_sha256": "full",
            "community_report_sha256": "longmemeval",
        }
        for field, checkpoint_name in fields.items():
            checkpoint = state["checkpoints"].get(checkpoint_name)
            _require(isinstance(checkpoint, dict), f"汇总报告缺少 {checkpoint_name} 检查点")
            _require(report.get(field) == checkpoint.get("report_sha256"), f"汇总报告的 {field} 与检查点不一致")
        expected = all(state["checkpoints"][name].get("passed") is True for name in fields.values())
        _require(report.get("passed") is expected, "汇总报告总判定与三层检查点不一致")


def _max_wall_seconds(contract: dict[str, Any], mode: str) -> float | None:
    if mode == "targeted":
        return float(contract["optimization_loop"]["modes"]["targeted"]["max_wall_seconds"])
    if mode == "frontier":
        return float(contract["optimization_loop"]["modes"]["full"]["max_wall_seconds"])
    if mode == "core":
        return float(contract["evidence_layers"]["core"]["max_wall_seconds"])
    if mode in {"qualification", "full"}:
        return float(contract["evidence_layers"]["product"]["modes"][mode]["max_wall_seconds"])
    if mode == "longmemeval":
        return float(contract["evidence_layers"]["community"]["expected_wall_seconds"]["max"])
    return None


def _validate_state(contract: dict[str, Any], state: dict[str, Any]) -> None:
    _require(state.get("schema") == "ownward.acceptance-state/v1", "状态文件 schema 无效")
    _require(state.get("suite_version") == contract["suite_version"], "状态文件体系版本无效")
    _require(isinstance(state.get("binding"), dict), "状态文件缺少候选绑定")
    _require(isinstance(state.get("checkpoints"), dict), "状态文件缺少检查点")
    _require(isinstance(state.get("invalidated_reports"), dict), "状态文件缺少失效报告记录")


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise LifecycleError(message)
