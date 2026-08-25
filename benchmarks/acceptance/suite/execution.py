from __future__ import annotations

import json
from datetime import datetime, timezone
import os
from pathlib import Path
import time
from typing import Any

import binding
import evidence
import lifecycle
import relationships
from contract import validate_report
from evidence import validate_layer_report
from execution_support import ExecutionError, load_json, require


def _archive_invalidated_report(state: dict[str, Any], mode: str, report_path: Path) -> None:
    if not lifecycle.report_was_invalidated(state, mode, report_path):
        return
    digest = lifecycle.file_sha256(report_path)
    archive = report_path.parent / "_audit" / mode / f"{digest}.json"
    if archive.is_file():
        require(lifecycle.file_sha256(archive) == digest, "失效报告归档发生变化")
    else:
        archive.parent.mkdir(parents=True, exist_ok=True)
        temporary = archive.with_name(f".{archive.name}.{os.getpid()}.{time.time_ns()}.tmp")
        with temporary.open("wb") as stream:
            stream.write(report_path.read_bytes())
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, archive)
    report_path.unlink()


def load_config(path: Path) -> dict[str, Any]:
    value = load_json(path)
    binding.validate_config(value)
    return value


def execute(
    suite_root: Path,
    contract: dict[str, Any],
    state_path: Path,
    mode: str,
    config: dict[str, Any],
    *,
    resume: bool,
) -> dict[str, Any]:
    require(mode in {"targeted", "core", "frontier", "qualification", "full", "longmemeval"}, f"模式 {mode} 不能执行")
    state = lifecycle.load_state(state_path)
    repository = Path(config["repository"]).resolve()
    workspace = Path(config["workspace"]).resolve()
    require(repository == suite_root.parents[2].resolve(), "执行配置指向另一仓库")
    if os.name == "nt":
        require(bool(workspace.drive) and workspace.drive.lower() != Path.home().drive.lower(), "正式验收工作区不得位于系统盘")
    else:
        require(workspace.is_absolute(), "正式验收工作区必须使用绝对路径")
    binding.verify_current(suite_root, Path(config["binding_dir"]), config, state["binding"], mode)
    lifecycle.can_start(contract, state, mode)
    try:
        selection = relationships.selection_identity(mode, config)
    except relationships.RelationshipError as error:
        raise ExecutionError(str(error)) from error
    reusable = lifecycle.reusable_report(contract, state, mode, selection)
    if reusable is not None:
        require(resume, f"{mode} 已有有效检查点；使用 --resume 复用")
        return {"outcome": "reused", "mode": mode, "report": str(reusable)}
    active_state = dict(state)
    active_state["binding"] = binding.for_mode(state["binding"], mode)
    workspace.mkdir(parents=True, exist_ok=True)
    report_path = workspace / "reports" / f"{mode}.json"
    _archive_invalidated_report(state, mode, report_path)
    if report_path.exists():
        require(resume, f"{mode} 报告已存在；使用 --resume")
        try:
            recovered = load_json(report_path)
            _validate_completed_report(contract, active_state, mode, recovered, report_path, selection)
        except (OSError, ValueError):
            report_path.unlink()
        else:
            elapsed = _report_elapsed(recovered)
            outcome = lifecycle.record(contract, state, mode, recovered, lifecycle.file_sha256(report_path), elapsed, str(report_path.resolve()), selection)
            lifecycle.save_state(state_path, state)
            return {"outcome": outcome, "mode": mode, "elapsed_seconds": elapsed, "report": str(report_path), "recovered": True}

    started = time.perf_counter()
    started_at = datetime.now(timezone.utc).isoformat()
    observation_path = None
    if mode in {"targeted", "frontier"}:
        from execution_frontier import execute_frontier
        report, observation_path = execute_frontier(suite_root, contract, active_state, mode, config, workspace, resume)
        artifact_paths = [observation_path]
    elif mode == "core":
        from execution_core import execute_core
        report = execute_core(suite_root, contract, active_state, config, workspace, resume)
        artifact_paths = [workspace / "evidence" / "core"]
    elif mode in {"qualification", "full"}:
        from execution_product import execute_product, product_artifact_paths
        budget = float(contract["evidence_layers"]["product"]["modes"][mode]["max_wall_seconds"])
        report = execute_product(suite_root, contract, active_state, mode, config, workspace, resume, deadline=started + budget)
        artifact_paths = product_artifact_paths(workspace, mode, report)
    else:
        from execution_community import execute_community
        report, artifact_paths = execute_community(suite_root, contract, active_state["binding"], config, workspace, resume=resume)
    elapsed = time.perf_counter() - started
    report["started_at"] = started_at
    report["finished_at"] = datetime.now(timezone.utc).isoformat()
    evidence.attach_artifacts(report, report_path, artifact_paths)
    _validate_completed_report(contract, active_state, mode, report, report_path, selection)
    report_path.parent.mkdir(parents=True, exist_ok=True)
    temporary = report_path.with_suffix(report_path.suffix + ".tmp")
    temporary.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    temporary.replace(report_path)
    outcome = lifecycle.record(contract, state, mode, report, lifecycle.file_sha256(report_path), elapsed, str(report_path.resolve()), selection)
    if observation_path is not None:
        checkpoint = state["checkpoints"][mode]
        checkpoint["observation_path"] = str(observation_path.resolve())
        checkpoint["observation_sha256"] = lifecycle.file_sha256(observation_path)
    lifecycle.save_state(state_path, state)
    return {"outcome": outcome, "mode": mode, "elapsed_seconds": elapsed, "report": str(report_path)}


def _validate_completed_report(
    contract: dict[str, Any],
    state: dict[str, Any],
    mode: str,
    report: dict[str, Any],
    report_path: Path,
    selection: dict[str, Any] | None,
) -> None:
    if mode in {"targeted", "frontier"}:
        validate_report(contract, "frontier", report)
    else:
        kind = {"core": "core", "qualification": "product", "full": "product", "longmemeval": "community"}[mode]
        validate_layer_report(contract, kind, report, expected_binding=state["binding"])
    require(report.get("candidate") == state["binding"]["candidate"], "已有报告绑定另一候选")
    require(report.get("environment", {}).get("sha256") == state["binding"]["environment_sha256"], "已有报告绑定另一环境")
    require(report.get("inputs", {}).get("sha256") == state["binding"]["input_manifest_sha256"], "已有报告绑定另一输入")
    if mode in {"targeted", "frontier"}:
        require(report.get("mode") == ("targeted" if mode == "targeted" else "full"), "已有前沿报告模式不一致")
    if mode == "targeted":
        observed_stages = report.get("diagnostics", {}).get("stages")
        require(
            selection is not None
            and isinstance(observed_stages, list)
            and len(observed_stages) == len(set(observed_stages))
            and set(observed_stages) == set(selection["targeted_stages"]),
            "已有定向报告绑定另一阶段选择",
        )
    if mode in {"qualification", "full"}:
        require(report.get("mode") == mode, "已有专项报告模式不一致")
    evidence.validate_report_artifacts(report_path, report)


def _report_elapsed(report: dict[str, Any]) -> float:
    try:
        started = datetime.fromisoformat(str(report["started_at"]).replace("Z", "+00:00"))
        finished = datetime.fromisoformat(str(report["finished_at"]).replace("Z", "+00:00"))
    except (KeyError, ValueError) as error:
        raise ExecutionError("已有报告缺少可恢复的起止时间") from error
    elapsed = (finished - started).total_seconds()
    require(elapsed >= 0, "已有报告起止时间无效")
    return elapsed
