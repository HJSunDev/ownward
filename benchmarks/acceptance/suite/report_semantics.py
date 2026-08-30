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
from contract import validate_report
from evidence import validate_layer_report, validate_report_artifacts
from lifecycle_error import LifecycleError


REPORT_KIND = {
    "targeted": "frontier", "frontier": "frontier", "core": "core",
    "qualification": "product", "full": "product", "longmemeval": "community",
    "summarize": "suite",
}


def can_start(contract: dict[str, Any], state: dict[str, Any], mode: str) -> None:
    import lifecycle

    lifecycle._validate_state(contract, state)
    _require(mode in REPORT_KIND or mode == "self-check", f"未知执行模式: {mode}")
    if state.get("schema") == evidence_identity.STATE_SCHEMA and mode != "self-check":
        verify_current_reporting(state["binding"], include_summary=mode == "summarize")
    for prerequisite in report_relationships.START_ELIGIBILITY.get(mode, ()):
        checkpoint = state["checkpoints"].get(prerequisite)
        _require(isinstance(checkpoint, dict), f"{mode} 缺少前置证据 {prerequisite}")
        _require(checkpoint.get("passed") is True, f"{mode} 的前置证据 {prerequisite} 未通过")
        checkpoint_report(contract, state, prerequisite)


def record(
    contract: dict[str, Any], state: dict[str, Any], mode: str, report: dict[str, Any],
    report_sha256: str, elapsed_seconds: float, report_path: str = "",
    selection: dict[str, Any] | None = None,
) -> str:
    can_start(contract, state, mode)
    _require(mode != "self-check", "体系自检不得写入正式检查点")
    validate_report_for_mode(contract, state, mode, report)
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
    active_binding = candidate_binding.for_mode(state["binding"], mode)
    migrated = state.get("schema") == evidence_identity.STATE_SCHEMA
    direct_dependencies = dependencies_for_mode(state["binding"], mode) if migrated else {}
    direct_identity = evidence_identity.evidence_identity(mode, report_sha256, direct_dependencies) if migrated else None
    existing = state["checkpoints"].get(mode)
    if (
        existing and existing.get("report_sha256") == report_sha256
        and existing.get("binding") == active_binding
        and existing.get("artifact_manifest_sha256", "") == artifact_manifest_sha256
        and existing.get("selection") == selection
        and (not migrated or existing.get("evidence_identity") == direct_identity)
    ):
        return "reused"
    state["checkpoints"][mode] = {
        "binding": active_binding,
        "report_sha256": report_sha256,
        "passed": report.get("passed", report.get("decision") in {"eligible_for_qualification", "bootstrap_reference"}),
        "elapsed_seconds": elapsed_seconds,
        "report_path": report_path,
        "artifact_manifest_sha256": artifact_manifest_sha256,
        "recorded_at": datetime.now(timezone.utc).isoformat(),
    }
    if migrated:
        state["checkpoints"][mode]["evidence_identity"] = direct_identity
    if selection is not None:
        state["checkpoints"][mode]["selection"] = copy.deepcopy(selection)
    state["invalidated_reports"].pop(mode, None)
    return "recorded"


def reusable_report(
    contract: dict[str, Any], state: dict[str, Any], mode: str,
    selection: dict[str, Any] | None = None,
) -> Path | None:
    if state.get("schema") == evidence_identity.STATE_SCHEMA:
        try:
            verify_current_reporting(state["binding"], include_summary=mode == "summarize")
        except LifecycleError:
            return None
    checkpoint = state.get("checkpoints", {}).get(mode)
    if not isinstance(checkpoint, dict) or checkpoint.get("binding") != candidate_binding.for_mode(state["binding"], mode) or checkpoint.get("selection") != selection:
        return None
    if state.get("schema") == evidence_identity.STATE_SCHEMA:
        try:
            evidence_identity.validate_evidence_identity(
                checkpoint.get("evidence_identity"), kind=mode,
                report_sha256=str(checkpoint.get("report_sha256", "")),
                dependencies=dependencies_for_mode(state["binding"], mode),
            )
        except (evidence_identity.EvidenceIdentityError, TypeError):
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
        validate_report_for_mode(contract, state, mode, report)
        if mode == "summarize":
            for dependency in report_relationships.SUMMARY_AGGREGATES:
                checkpoint_report(contract, state, dependency)
        manifest_sha256 = validate_report_artifacts(path, report)
        _require(manifest_sha256 == checkpoint.get("artifact_manifest_sha256"), f"{mode} 原始证据清单与检查点不一致")
    except (OSError, json.JSONDecodeError, ValueError):
        return None
    return path


def checkpoint_report(contract: dict[str, Any], state: dict[str, Any], mode: str) -> dict[str, Any]:
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
    validate_report_for_mode(contract, state, mode, report)
    manifest_sha256 = validate_report_artifacts(path, report)
    _require(manifest_sha256 == checkpoint.get("artifact_manifest_sha256"), f"{mode} 原始证据清单与检查点不一致")
    if state.get("schema") == evidence_identity.STATE_SCHEMA:
        try:
            evidence_identity.validate_evidence_identity(
                checkpoint.get("evidence_identity"), kind=mode,
                report_sha256=str(checkpoint.get("report_sha256", "")),
                dependencies=dependencies_for_mode(state["binding"], mode),
            )
        except evidence_identity.EvidenceIdentityError as error:
            raise LifecycleError(str(error)) from error
    return report


def validate_report_for_mode(
    contract: dict[str, Any], state: dict[str, Any], mode: str, report: dict[str, Any],
) -> None:
    active_binding = candidate_binding.for_mode(state["binding"], mode)
    kind = REPORT_KIND[mode]
    if kind in {"core", "product", "community"}:
        validate_layer_report(contract, kind, report, expected_binding=active_binding)
    else:
        validate_report(contract, kind, report)
        _require(report.get("candidate") == active_binding["candidate"], f"{mode} 报告候选运行身份不一致")
    if mode in {"targeted", "frontier"}:
        _require(report.get("mode") == ("targeted" if mode == "targeted" else "full"), f"{mode} 报告模式无效")
    if mode in {"qualification", "full"}:
        _require(report.get("mode") == mode, f"{mode} 报告模式无效")
    environment = report.get("environment", {})
    inputs = report.get("inputs", {})
    _require(environment.get("sha256") == active_binding["environment_sha256"], f"{mode} 报告环境绑定不一致")
    _require(inputs.get("sha256") == active_binding["input_manifest_sha256"], f"{mode} 报告输入绑定不一致")
    if mode == "summarize":
        import summary_reporting

        summary_reporting.validate_summary(state, report, active_binding)


def dependencies_for_mode(binding: dict[str, Any], mode: str) -> dict[str, str]:
    if binding.get("schema") != evidence_identity.BINDING_SCHEMA:
        return {}
    if mode == "summarize":
        dependencies: dict[str, str] = {}
        for scope in ("core", "product", "community"):
            for name, identity in evidence_identity.scope_dependencies(binding, scope).items():
                dependencies[f"{scope}.{name}"] = identity
        dependencies["summary-generation"] = evidence_identity.reporting_identity(binding, "summary")
        return dependencies
    return evidence_identity.scope_dependencies(binding, candidate_binding.scope_for_mode(mode))


def verify_current_reporting(binding: dict[str, Any], *, include_summary: bool) -> None:
    current = evidence_identity.reporting_identities(Path(__file__).resolve().parents[3])
    kinds = ["reception", "relationships"]
    if include_summary:
        kinds.append("summary")
    for kind in kinds:
        _require(binding.get("reporting", {}).get(kind) == current[kind], f"{kind} 报告语义已经变化，必须先重新绑定")


def file_sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


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


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise LifecycleError(message)
