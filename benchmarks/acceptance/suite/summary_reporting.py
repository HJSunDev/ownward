from __future__ import annotations

from datetime import datetime, timezone
from typing import Any

import binding as candidate_binding
import evidence_identity
import report_relationships
from contract import validate_report
from lifecycle_error import LifecycleError


def summarize(contract: dict[str, Any], state: dict[str, Any]) -> dict[str, Any]:
    import report_semantics

    report_semantics.can_start(contract, state, "summarize")
    checkpoints = state["checkpoints"]
    for mode in report_relationships.SUMMARY_AGGREGATES:
        report_semantics.checkpoint_report(contract, state, mode)
    summary_binding = candidate_binding.for_mode(state["binding"], "summarize")
    report = {
        "schema": contract["reports"]["suite"]["schema"],
        "suite_version": contract["suite_version"],
        "candidate": evidence_identity.source_git(state["binding"]),
        "binary_sha256": summary_binding["binary_sha256"],
        "environment": {"sha256": summary_binding["environment_sha256"]},
        "inputs": {"sha256": summary_binding["input_manifest_sha256"]},
        "tool_sha256": summary_binding["tool_sha256"],
        "core_report_sha256": checkpoints["core"]["report_sha256"],
        "product_report_sha256": checkpoints["full"]["report_sha256"],
        "community_report_sha256": checkpoints["longmemeval"]["report_sha256"],
        "passed": all(checkpoints[name]["passed"] for name in report_relationships.SUMMARY_AGGREGATES),
        "created_at": datetime.now(timezone.utc).isoformat(),
    }
    validate_report(contract, "suite", report)
    return report


def validate_summary(state: dict[str, Any], report: dict[str, Any], active_binding: dict[str, str]) -> None:
    _require(report.get("tool_sha256") == active_binding["tool_sha256"], "汇总报告工具绑定不一致")
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


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise LifecycleError(message)
