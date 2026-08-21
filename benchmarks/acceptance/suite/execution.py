from __future__ import annotations

import json
from datetime import datetime, timezone
from pathlib import Path
import shutil
import sys
import time
from typing import Any

import community
import binding
import evidence
import frontier
import lifecycle
import process_control
import product
from contract import validate_report
from evidence import validate_layer_report
from materials import load_json


class ExecutionError(ValueError):
    pass


def load_config(path: Path) -> dict[str, Any]:
    value = json.loads(path.read_text(encoding="utf-8"))
    _require(isinstance(value, dict), "执行配置必须是对象")
    binding.validate_config(value)
    value["_config_sha256"] = lifecycle.file_sha256(path)
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
    _require(mode in {"targeted", "core", "frontier", "qualification", "full", "longmemeval"}, f"模式 {mode} 不能执行")
    state = lifecycle.load_state(state_path)
    repository = Path(config["repository"]).resolve()
    workspace = Path(config["workspace"]).resolve()
    _require(repository == suite_root.parents[2].resolve(), "执行配置指向另一仓库")
    _require(workspace.drive and workspace.drive.lower() != Path.home().drive.lower(), "正式验收工作区不得位于系统盘")
    binding.verify_current(suite_root, Path(config["binding_dir"]), config, state["binding"], mode)
    lifecycle.can_start(contract, state, mode)
    reusable = lifecycle.reusable_report(contract, state, mode)
    if reusable is not None:
        _require(resume, f"{mode} 已有有效检查点；使用 --resume 复用")
        return {"outcome": "reused", "mode": mode, "report": str(reusable)}
    active_state = dict(state)
    active_state["binding"] = binding.for_mode(state["binding"], mode)
    workspace.mkdir(parents=True, exist_ok=True)
    report_path = workspace / "reports" / f"{mode}.json"
    if lifecycle.report_was_invalidated(state, mode, report_path):
        report_path.unlink()
    if report_path.exists():
        _require(resume, f"{mode} 报告已存在；使用 --resume")
        try:
            recovered = load_json(report_path)
        except (OSError, json.JSONDecodeError):
            report_path.unlink()
        else:
            _validate_completed_report(contract, active_state, mode, recovered, report_path)
            elapsed = _report_elapsed(recovered)
            outcome = lifecycle.record(
                contract, state, mode, recovered, lifecycle.file_sha256(report_path), elapsed, str(report_path.resolve())
            )
            lifecycle.save_state(state_path, state)
            return {"outcome": outcome, "mode": mode, "elapsed_seconds": elapsed, "report": str(report_path), "recovered": True}
    started = time.perf_counter()
    started_at = datetime.now(timezone.utc).isoformat()
    if mode in {"targeted", "frontier"}:
        report, observation_path = _execute_frontier(suite_root, contract, active_state, mode, config, workspace, resume)
        artifact_paths = [observation_path]
    elif mode == "core":
        report = _execute_core(suite_root, contract, active_state, config, workspace, resume)
        observation_path = None
        artifact_paths = [workspace / "evidence" / "core"]
    elif mode in {"qualification", "full"}:
        mode_budget = float(contract["evidence_layers"]["product"]["modes"][mode]["max_wall_seconds"])
        report = _execute_product(
            suite_root, contract, active_state, mode, config, workspace, resume, deadline=started + mode_budget
        )
        observation_path = None
        artifact_paths = [workspace / "evidence" / "product", workspace / "evidence" / "product-resource"]
    else:
        section = dict(_mapping(config, "community"))
        section["_workspace"] = str(workspace)
        report = community.execute(suite_root, contract, active_state["binding"], section, resume=resume)
        validate_layer_report(contract, "community", report, expected_binding=active_state["binding"])
        observation_path = None
        artifact_paths = community.artifact_paths(section)
    elapsed = time.perf_counter() - started
    report["started_at"] = started_at
    report["finished_at"] = datetime.now(timezone.utc).isoformat()
    evidence.attach_artifacts(report, report_path, artifact_paths)
    _validate_completed_report(contract, active_state, mode, report, report_path)
    report_path.parent.mkdir(parents=True, exist_ok=True)
    temporary_report = report_path.with_suffix(report_path.suffix + ".tmp")
    temporary_report.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    temporary_report.replace(report_path)
    outcome = lifecycle.record(
        contract, state, mode, report, lifecycle.file_sha256(report_path), elapsed, str(report_path.resolve())
    )
    if observation_path is not None:
        checkpoint = state["checkpoints"][mode]
        checkpoint["observation_path"] = str(observation_path.resolve())
        checkpoint["observation_sha256"] = lifecycle.file_sha256(observation_path)
    lifecycle.save_state(state_path, state)
    return {"outcome": outcome, "mode": mode, "elapsed_seconds": elapsed, "report": str(report_path)}


def _execute_frontier(
    suite_root: Path,
    contract: dict[str, Any],
    state: dict[str, Any],
    mode: str,
    config: dict[str, Any],
    workspace: Path,
    resume: bool,
) -> tuple[dict[str, Any], Path]:
    section = _mapping(config, "frontier")
    tool = Path(section["tool"]).resolve()
    _require(tool.is_file(), "内核前沿观察器不存在")
    observation_path = workspace / "evidence" / "frontier" / f"{mode}-observation.json"
    observation_path.parent.mkdir(parents=True, exist_ok=True)
    actual_mode = "targeted" if mode == "targeted" else "full"
    expected_stages = set(section.get("targeted_stages", [])) if actual_mode == "targeted" else frontier.STAGES
    candidate = None
    if observation_path.exists():
        _require(resume, "内核观察已存在；使用 --resume 复用")
        try:
            existing = load_json(observation_path)
            frontier.validate_observation(contract, existing)
            if (
                existing.get("candidate") == state["binding"]["candidate"]
                and existing.get("environment", {}).get("sha256") == state["binding"]["environment_sha256"]
                and existing.get("input_manifest_sha256") == state["binding"]["input_manifest_sha256"]
                and set(existing.get("requested_stages", [])) == expected_stages
            ):
                candidate = existing
        except (ValueError, OSError, json.JSONDecodeError):
            candidate = None
        if candidate is None:
            observation_path.unlink()
    dataset = suite_root / "materials" / "core" / "v1" / "dataset.json"
    command = ([sys.executable, str(tool)] if tool.suffix.lower() == ".py" else [str(tool)]) + [
        "--materials", str(dataset), "--candidate", state["binding"]["candidate"],
        "--mode", actual_mode, "--environment-sha256", state["binding"]["environment_sha256"],
        "--input-manifest-sha256", state["binding"]["input_manifest_sha256"], "--repository", str(suite_root.parents[2]),
        "--output", str(observation_path),
    ]
    if actual_mode == "targeted":
        stages = section.get("targeted_stages")
        _require(isinstance(stages, list) and stages, "定向模式必须声明受影响阶段")
        command.extend(["--stages", ",".join(str(value) for value in stages)])
    timeout = float(contract["optimization_loop"]["modes"][actual_mode]["max_wall_seconds"])
    if candidate is None:
        _run(command, cwd=suite_root.parents[2], timeout=timeout)
        candidate = load_json(observation_path)
    frontier.validate_observation(contract, candidate)
    _require(set(candidate.get("requested_stages", [])) == expected_stages, "内核观察报告没有绑定本次执行阶段")
    _require(candidate.get("materials_sha256") == lifecycle.file_sha256(dataset), "内核观察报告没有绑定实际固定材料")
    _require(candidate.get("tool_sha256") == lifecycle.file_sha256(tool), "内核观察报告没有绑定实际执行文件")
    baseline_observation = None
    baseline = state.get("baseline")
    if isinstance(baseline, dict):
        observation = baseline.get("observations", {}).get("full")
        if isinstance(observation, dict):
            if isinstance(observation.get("value"), dict):
                actual = lifecycle.canonical_sha256(observation["value"])
                _require(actual == observation.get("canonical_sha256"), "有效内核基线内联证据发生变化")
                baseline_observation = observation["value"]
            else:
                path = Path(str(observation.get("path", "")))
                _require(path.is_file() and lifecycle.file_sha256(path) == observation.get("sha256"), "有效内核基线观察证据缺失或变化")
                baseline_observation = load_json(path)
    calibration = load_json(suite_root / "materials" / "frontier" / "v1" / "calibration.json")
    report = frontier.compare(contract, baseline_observation, candidate, calibration)
    validate_report(contract, "frontier", report)
    return report, observation_path


def _execute_core(
    suite_root: Path,
    contract: dict[str, Any],
    state: dict[str, Any],
    config: dict[str, Any],
    workspace: Path,
    resume: bool,
) -> dict[str, Any]:
    binary, runtime = _product_paths(config, state)
    evidence = workspace / "evidence" / "core"
    adapter_report = evidence / "adapter-report.json"
    if adapter_report.is_file():
        _require(resume, "固定内核证据已完成；使用 --resume 封装报告")
        report = load_json(adapter_report)
        validate_layer_report(contract, "core", report, expected_binding=state["binding"])
        return report
    if evidence.exists():
        _require(resume, "固定内核证据未完成；使用 --resume 仅重做该层")
        _safe_remove(evidence, workspace / "evidence")
    adapter = suite_root / "adapters" / "core" / "verify.py"
    command = [
        sys.executable, str(adapter), "--repository", str(suite_root.parents[2]), "--binary", str(binary),
        "--candidate", state["binding"]["candidate"], "--runtime-dir", str(runtime),
        "--evidence-dir", str(evidence), "--output", str(adapter_report),
        "--suite-version", contract["suite_version"], "--environment-sha256", state["binding"]["environment_sha256"],
        "--input-manifest-sha256", state["binding"]["input_manifest_sha256"],
    ]
    _run(command, cwd=suite_root.parents[2], timeout=float(contract["evidence_layers"]["core"]["max_wall_seconds"]))
    report = load_json(adapter_report)
    validate_layer_report(contract, "core", report, expected_binding=state["binding"])
    return report


def _resource_report(
    suite_root: Path,
    state: dict[str, Any],
    config: dict[str, Any],
    workspace: Path,
    *,
    resume: bool,
) -> Path:
    report_path = workspace / "evidence" / "product-resource" / "report.json"
    resource_workspace = workspace / "resource-work"
    if report_path.is_file():
        _safe_remove(resource_workspace, workspace)
        _require_resource_admission(load_json(report_path), state, report_path)
        return report_path
    section = _mapping(config, "product")
    package = Path(section["package"]).resolve()
    production = Path(section["production_storage_report"]).resolve()
    _require(package.is_dir() and production.is_file(), "专项验收缺少候选发布包或生产规模存储报告")
    evidence = workspace / "evidence" / "product-resource" / "raw"
    if resource_workspace.exists() or evidence.exists():
        _require(resume, "候选资源测量未完成；使用 --resume 仅重做资源测量")
        _safe_remove(resource_workspace, workspace)
        _safe_remove(evidence, workspace / "evidence" / "product-resource")
    adapter = suite_root / "adapters" / "product_resource" / "verify.py"
    command = [
        sys.executable, str(adapter), "--package", str(package), "--candidate", state["binding"]["candidate"],
        "--production-storage-report", str(production), "--workspace", str(resource_workspace),
        "--evidence-dir", str(evidence), "--output", str(report_path),
    ]
    try:
        completed = process_control.run(command, cwd=suite_root.parents[2], timeout=600)
    except process_control.ProcessTimeout as error:
        _safe_remove(resource_workspace, workspace)
        raise ExecutionError("候选资源准入超过 600 秒上限，已停止") from error
    _safe_remove(resource_workspace, workspace)
    _require(report_path.is_file(), f"候选资源测量没有形成报告: {completed.stderr[-2000:]}")
    report = load_json(report_path)
    _require(completed.returncode == 0, f"候选资源测量执行失败: {completed.stderr[-2000:]}")
    _require_resource_admission(report, state, report_path)
    return report_path


def _require_resource_admission(report: dict[str, Any], state: dict[str, Any], report_path: Path | None = None) -> None:
    _require(report.get("schema") == "ownward.delivery-resource-report/v1", "候选资源报告 schema 无效")
    _require(
        report.get("candidate") == state["binding"]["candidate"]
        and report.get("release_binary_sha256") == state["binding"]["binary_sha256"],
        "候选资源报告绑定另一候选",
    )
    if report_path is not None:
        evidence = report.get("evidence")
        _require(
            isinstance(evidence, dict)
            and set(evidence) == {"package_manifest", "process_samples", "workload_results"},
            "候选资源报告缺少完整原始证据",
        )
        evidence_root = report_path.resolve().parent
        for name, item in evidence.items():
            _require(isinstance(item, dict), f"候选资源证据 {name} 无效")
            path = Path(str(item.get("path", ""))).resolve()
            _require(path.is_relative_to(evidence_root) and path.is_file(), f"候选资源证据 {name} 缺失")
            _require(lifecycle.file_sha256(path) == item.get("sha256"), f"候选资源证据 {name} 发生变化")
    _require(report.get("passed") is True, "候选未通过资源准入，停止高成本专项验收")


def _execute_product(
    suite_root: Path,
    contract: dict[str, Any],
    state: dict[str, Any],
    mode: str,
    config: dict[str, Any],
    workspace: Path,
    resume: bool,
    *,
    deadline: float,
) -> dict[str, Any]:
    binary, runtime = _product_paths(config, state)
    section = _mapping(config, "product")
    resource_report = _resource_report(suite_root, state, config, workspace, resume=resume)
    dataset, qualification = product.load_default_materials(suite_root)
    tasks = product.prepare_tasks(dataset, qualification, mode)
    tasks_path = workspace / "tasks" / f"{mode}.json"
    tasks_path.parent.mkdir(parents=True, exist_ok=True)
    if tasks_path.is_file():
        _require(load_json(tasks_path) == tasks, "冻结专项任务发生变化")
    else:
        tasks_path.write_text(json.dumps(tasks, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    binding_path = workspace / "binding.json"
    if not binding_path.is_file():
        binding_path.write_text(json.dumps(state["binding"], ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    else:
        _require(load_json(binding_path) == state["binding"], "专项工作区绑定另一候选")
    results_path = workspace / "evidence" / "product" / "results" / f"{mode}.json"
    evidence = workspace / "evidence" / "product" / "scenarios"
    adapter = suite_root / "adapters" / "product" / "verify.py"
    maximum = deadline - time.perf_counter()
    _require(maximum > 0, f"{mode} 的资源准入已经耗尽该层总成本预算")
    command = [
        sys.executable, str(adapter), "--binary", str(binary), "--runtime-dir", str(runtime),
        "--codex-binary", str(Path(section["codex_binary"]).resolve()),
        "--codex-auth-file", str(Path(section["codex_auth_file"]).resolve()),
        "--codex-model", str(section["codex_model"]),
        "--codex-reasoning-effort", str(section["codex_reasoning_effort"]),
        "--tasks", str(tasks_path), "--binding", str(binding_path), "--resource-report", str(resource_report),
        "--evidence-dir", str(evidence), "--output", str(results_path), "--max-wall-seconds", str(maximum),
    ]
    if resume:
        command.append("--resume")
    _run(command, cwd=suite_root.parents[2], timeout=maximum)
    results = load_json(results_path)
    score_binding = {
        "candidate": state["binding"]["candidate"], "binary_sha256": state["binding"]["binary_sha256"],
        "environment": {"sha256": state["binding"]["environment_sha256"]},
        "inputs": {"sha256": state["binding"]["input_manifest_sha256"]},
    }
    report = product.score_results(contract, dataset, qualification, tasks, results, score_binding)
    validate_layer_report(contract, "product", report, expected_binding=state["binding"])
    return report


def _product_paths(config: dict[str, Any], state: dict[str, Any]) -> tuple[Path, Path]:
    section = _mapping(config, "product")
    binary = Path(section["binary"]).resolve()
    runtime = Path(section["runtime_dir"]).resolve()
    _require(binary.is_file() and runtime.is_dir(), "候选二进制或本地运行时不存在")
    _require(lifecycle.file_sha256(binary) == state["binding"]["binary_sha256"], "候选二进制摘要变化")
    return binary, runtime


def _run(command: list[str], *, cwd: Path, timeout: float) -> None:
    try:
        completed = process_control.run(command, cwd=cwd, timeout=timeout)
    except process_control.ProcessTimeout as error:
        raise ExecutionError(f"验收执行超过 {timeout:.0f} 秒上限，已停止") from error
    _require(completed.returncode == 0, f"验收执行失败: {completed.stderr[-3000:]}")


def _validate_completed_report(
    contract: dict[str, Any], state: dict[str, Any], mode: str, report: dict[str, Any], report_path: Path
) -> None:
    if mode in {"targeted", "frontier"}:
        validate_report(contract, "frontier", report)
    else:
        kind = {"core": "core", "qualification": "product", "full": "product", "longmemeval": "community"}[mode]
        validate_layer_report(contract, kind, report, expected_binding=state["binding"])
    _require(report.get("candidate") == state["binding"]["candidate"], "已有报告绑定另一候选")
    _require(report.get("environment", {}).get("sha256") == state["binding"]["environment_sha256"], "已有报告绑定另一环境")
    _require(report.get("inputs", {}).get("sha256") == state["binding"]["input_manifest_sha256"], "已有报告绑定另一输入")
    if mode in {"targeted", "frontier"}:
        _require(report.get("mode") == ("targeted" if mode == "targeted" else "full"), "已有前沿报告模式不一致")
    if mode in {"qualification", "full"}:
        _require(report.get("mode") == mode, "已有专项报告模式不一致")
    evidence.validate_report_artifacts(report_path, report)


def _report_elapsed(report: dict[str, Any]) -> float:
    from datetime import datetime
    try:
        started = datetime.fromisoformat(str(report["started_at"]).replace("Z", "+00:00"))
        finished = datetime.fromisoformat(str(report["finished_at"]).replace("Z", "+00:00"))
    except (KeyError, ValueError) as error:
        raise ExecutionError("已有报告缺少可恢复的起止时间") from error
    elapsed = (finished - started).total_seconds()
    _require(elapsed >= 0, "已有报告起止时间无效")
    return elapsed


def _safe_remove(path: Path, parent: Path) -> None:
    if not path.exists():
        return
    resolved = path.resolve()
    _require(resolved.parent == parent.resolve(), f"拒绝清理非预期路径: {resolved}")
    if resolved.is_dir():
        shutil.rmtree(resolved)
    else:
        resolved.unlink()


def _mapping(value: dict[str, Any], name: str) -> dict[str, Any]:
    nested = value.get(name)
    _require(isinstance(nested, dict), f"执行配置缺少 {name}")
    return nested


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise ExecutionError(message)
