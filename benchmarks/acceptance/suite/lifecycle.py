from __future__ import annotations

import copy
import hashlib
import json
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

import binding as candidate_binding
import evidence_identity
import report_relationships
import state_relationships
from lifecycle_error import LifecycleError
from report_semantics import (
    can_start, checkpoint_report as _checkpoint_report,
    dependencies_for_mode as _dependencies_for_mode, file_sha256,
    record, reusable_report, validate_report_for_mode as _validate_report_for_mode,
)
from summary_reporting import summarize


def new_state(contract: dict[str, Any], binding: dict[str, Any]) -> dict[str, Any]:
    candidate_binding.validate_binding(binding)
    _require(binding["suite_version"] == contract["suite_version"], "候选绑定的体系版本无效")
    return {
        "schema": evidence_identity.STATE_SCHEMA,
        "suite_version": contract["suite_version"],
        "binding": copy.deepcopy(binding),
        "checkpoints": {},
        "invalidated_reports": {},
        "baseline": None,
        "baseline_history": [],
    }


def plan_for_impacts(impacts: list[str]) -> list[str]:
    try:
        return state_relationships.plan_for_impacts(impacts)
    except report_relationships.RelationshipError as error:
        raise LifecycleError(str(error)) from error


def stages_for_impacts(impacts: list[str]) -> list[str]:
    try:
        return state_relationships.stages_for_impacts(impacts)
    except report_relationships.RelationshipError as error:
        raise LifecycleError(str(error)) from error


def plan_for_stage(stage: str) -> list[str]:
    try:
        return state_relationships.plan_for_stage(stage)
    except report_relationships.RelationshipError as error:
        raise LifecycleError(str(error)) from error


def invalidate(contract: dict[str, Any], state: dict[str, Any], mode: str) -> list[str]:
    _validate_state(contract, state)
    affected = state_relationships.MODE_INVALIDATION.get(mode)
    _require(affected is not None, f"模式 {mode} 没有失效传播规则")
    removed = [name for name in affected if name in state["checkpoints"]]
    for name in removed:
        checkpoint = state["checkpoints"][name]
        digest = checkpoint.get("report_sha256")
        if isinstance(digest, str) and digest:
            state["invalidated_reports"][name] = digest
        del state["checkpoints"][name]
    if mode in state_relationships.BASELINE_AGGREGATES and state.get("baseline") is not None:
        state.setdefault("baseline_history", []).append(state["baseline"])
        state["baseline"] = None
    return removed


def rebind(contract: dict[str, Any], state: dict[str, Any], binding: dict[str, Any]) -> list[str]:
    _validate_state(contract, state)
    candidate_binding.validate_binding(binding)
    _require(binding["suite_version"] == contract["suite_version"], "新候选绑定的体系版本无效")
    current = state["binding"]
    changed_scopes = {
        name for name in set(current["scopes"]) | set(binding["scopes"])
        if current["scopes"].get(name, {}).get("identity") != binding["scopes"].get(name, {}).get("identity")
    }
    if not changed_scopes:
        summary_changed = current.get("reporting", {}).get("summary") != binding.get("reporting", {}).get("summary")
        state["binding"] = copy.deepcopy(binding)
        if summary_changed and "summarize" in state["checkpoints"]:
            checkpoint = state["checkpoints"].pop("summarize")
            digest = checkpoint.get("report_sha256")
            if isinstance(digest, str) and digest:
                state["invalidated_reports"]["summarize"] = digest
            return ["summarize"]
        return []
    affected: set[str] = set()
    for name in changed_scopes:
        for mode in state_relationships.SCOPE_RESULTS.get(name, ()):
            affected.update(state_relationships.MODE_INVALIDATION[mode])
    removed = sorted(name for name in affected if name in state["checkpoints"])
    if state.get("baseline") is not None and changed_scopes & {"frontier", "core", "product"}:
        state.setdefault("baseline_history", []).append(state["baseline"])
        state["baseline"] = None
    state["binding"] = copy.deepcopy(binding)
    for name in removed:
        checkpoint = state["checkpoints"].pop(name)
        digest = checkpoint.get("report_sha256")
        if isinstance(digest, str) and digest:
            state["invalidated_reports"][name] = digest
    return removed


def report_was_invalidated(state: dict[str, Any], mode: str, path: Path) -> bool:
    digest = state.get("invalidated_reports", {}).get(mode)
    return isinstance(digest, str) and path.is_file() and file_sha256(path) == digest


def promote_baseline(contract: dict[str, Any], state: dict[str, Any]) -> None:
    core = state["checkpoints"].get("core")
    frontier = state["checkpoints"].get("frontier")
    qualification = state["checkpoints"].get("qualification")
    _require(core is not None and core.get("passed") is True, "有效基线晋升要求固定内核通过")
    _require(frontier is not None and frontier.get("passed") is True, "有效基线晋升要求前沿完整模式通过")
    _require(qualification is not None and qualification.get("passed") is True, "有效基线晋升要求资格验证通过")
    core_report = _checkpoint_report(contract, state, "core")
    frontier_report = _checkpoint_report(contract, state, "frontier")
    qualification_report = _checkpoint_report(contract, state, "qualification")
    if state.get("baseline") is not None:
        state.setdefault("baseline_history", []).append(state["baseline"])
    core_binding = candidate_binding.for_mode(state["binding"], "core")
    frontier_binding = candidate_binding.for_mode(state["binding"], "frontier")
    product_binding = candidate_binding.for_mode(state["binding"], "qualification")
    promoted = {
        "candidate": evidence_identity.source_git(state["binding"]),
        "binary_sha256": core_binding["binary_sha256"],
        "bindings": {"core": core_binding, "frontier": frontier_binding, "product": product_binding},
        "core_report_sha256": core["report_sha256"],
        "frontier_report_sha256": frontier["report_sha256"],
        "qualification_report_sha256": qualification["report_sha256"],
        "reports": {
            "core": {"canonical_sha256": canonical_sha256(core_report), "value": core_report},
            "frontier": {"canonical_sha256": canonical_sha256(frontier_report), "value": frontier_report},
            "qualification": {"canonical_sha256": canonical_sha256(qualification_report), "value": qualification_report},
        },
        "promoted_at": datetime.now(timezone.utc).isoformat(),
    }
    if state.get("schema") == evidence_identity.STATE_SCHEMA:
        dependencies = {
            "core": _dependencies_for_mode(state["binding"], "core"),
            "frontier": _dependencies_for_mode(state["binding"], "frontier"),
            "qualification": _dependencies_for_mode(state["binding"], "qualification"),
        }
        promoted.update(evidence_identity.baseline_identity_fields(
            evidence_identity.product_identity(state["binding"]),
            dependencies,
            {
                "core": core["report_sha256"],
                "frontier": frontier["report_sha256"],
                "qualification": qualification["report_sha256"],
            },
        ))
    state["baseline"] = promoted
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


def canonical_sha256(value: Any) -> str:
    encoded = json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def _validate_state(contract: dict[str, Any], state: dict[str, Any]) -> None:
    _require(state.get("schema") == evidence_identity.STATE_SCHEMA, "状态文件 schema 无效")
    _require(state.get("suite_version") == contract["suite_version"], "状态文件体系版本无效")
    _require(isinstance(state.get("binding"), dict), "状态文件缺少候选绑定")
    candidate_binding.validate_binding(state["binding"])
    _require(isinstance(state.get("checkpoints"), dict), "状态文件缺少检查点")
    _require(isinstance(state.get("invalidated_reports"), dict), "状态文件缺少失效报告记录")
    _require(state["binding"].get("schema") == evidence_identity.BINDING_SCHEMA, "当前状态必须使用直接依赖绑定")
    for mode, checkpoint in state["checkpoints"].items():
        _require(isinstance(checkpoint, dict), f"{mode} 检查点无效")
        try:
            evidence_identity.validate_evidence_identity(
                checkpoint.get("evidence_identity"), kind=mode,
                report_sha256=str(checkpoint.get("report_sha256", "")),
                dependencies=_dependencies_for_mode(state["binding"], mode),
            )
        except evidence_identity.EvidenceIdentityError as error:
            raise LifecycleError(str(error)) from error
    baseline = state.get("baseline")
    if baseline is not None:
        try:
            evidence_identity.validate_baseline_identity(baseline, binding=state["binding"])
        except evidence_identity.EvidenceIdentityError as error:
            raise LifecycleError(str(error)) from error
        for mode, field in (
            ("core", "core_report_sha256"),
            ("frontier", "frontier_report_sha256"),
            ("qualification", "qualification_report_sha256"),
        ):
            checkpoint = state["checkpoints"].get(mode)
            _require(
                isinstance(checkpoint, dict) and checkpoint.get("report_sha256") == baseline.get(field),
                f"活动基线 {mode} 报告不是当前检查点",
            )
    history = state.get("baseline_history")
    _require(isinstance(history, list), "基线历史必须是数组")
    for index, value in enumerate(history):
        try:
            evidence_identity.validate_baseline_identity(value)
        except evidence_identity.EvidenceIdentityError as error:
            raise LifecycleError(f"基线历史 {index}: {error}") from error


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise LifecycleError(message)
