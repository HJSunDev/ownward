from __future__ import annotations

import json
from pathlib import Path
import sys
import time
from typing import Any

import process_control
import product
from evidence import validate_layer_report
from execution_core import product_binary
from execution_support import ExecutionError, load_json, require, run, safe_remove
import lifecycle


def resource_report(
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
        safe_remove(resource_workspace, workspace)
        require_resource_admission(load_json(report_path), state, report_path)
        return report_path
    section = config["product"]
    package = Path(section["package"]).resolve()
    production = Path(section["production_storage_report"]).resolve()
    require(package.is_dir() and production.is_file(), "专项验收缺少候选发布包或生产规模存储报告")
    evidence = workspace / "evidence" / "product-resource" / "raw"
    if resource_workspace.exists() or evidence.exists():
        require(resume, "候选资源测量未完成；使用 --resume 仅重做资源测量")
        safe_remove(resource_workspace, workspace)
        safe_remove(evidence, workspace / "evidence" / "product-resource")
    adapter = suite_root / "adapters" / "product_resource" / "verify.py"
    command = [
        sys.executable, str(adapter), "--package", str(package), "--candidate", state["binding"]["candidate"],
        "--production-storage-report", str(production), "--workspace", str(resource_workspace),
        "--evidence-dir", str(evidence), "--output", str(report_path),
    ]
    try:
        completed = process_control.run(command, cwd=suite_root.parents[2], timeout=600)
    except process_control.ProcessTimeout as error:
        safe_remove(resource_workspace, workspace)
        raise ExecutionError("候选资源准入超过 600 秒上限，已停止") from error
    safe_remove(resource_workspace, workspace)
    require(report_path.is_file(), f"候选资源测量没有形成报告: {completed.stderr[-2000:]}")
    require(completed.returncode == 0, f"候选资源测量执行失败: {completed.stderr[-2000:]}")
    report = load_json(report_path)
    report["acceptance_binding"] = dict(state["binding"])
    report_path.write_text(json.dumps(report, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    require_resource_admission(report, state, report_path)
    return report_path


def require_resource_admission(report: dict[str, Any], state: dict[str, Any], report_path: Path | None = None) -> None:
    require(report.get("schema") == "ownward.delivery-resource-report/v1", "候选资源报告 schema 无效")
    require(report.get("candidate") == state["binding"]["candidate"] and report.get("release_binary_sha256") == state["binding"]["binary_sha256"], "候选资源报告绑定另一候选")
    require(report.get("acceptance_binding") == state["binding"], "候选资源报告没有绑定当前 product scope 的输入、环境、工具与发布制品")
    if report_path is not None:
        evidence = report.get("evidence")
        require(isinstance(evidence, dict) and set(evidence) == {"package_manifest", "process_samples", "workload_results"}, "候选资源报告缺少完整原始证据")
        evidence_root = report_path.resolve().parent
        for name, item in evidence.items():
            require(isinstance(item, dict), f"候选资源证据 {name} 无效")
            path = Path(str(item.get("path", ""))).resolve()
            require(path.is_relative_to(evidence_root) and path.is_file(), f"候选资源证据 {name} 缺失")
            require(lifecycle.file_sha256(path) == item.get("sha256"), f"候选资源证据 {name} 发生变化")
    require(report.get("passed") is True, "候选未通过资源准入，停止高成本专项验收")


def execute_product(
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
    binary = product_binary(config, state)
    section = config["product"]
    resource = resource_report(suite_root, state, config, workspace, resume=resume)
    dataset, qualification = product.load_default_materials(suite_root)
    tasks = product.prepare_tasks(dataset, qualification, mode)
    tasks_path = workspace / "tasks" / f"{mode}.json"
    tasks_path.parent.mkdir(parents=True, exist_ok=True)
    if tasks_path.is_file():
        require(load_json(tasks_path) == tasks, "冻结专项任务发生变化")
    else:
        tasks_path.write_text(json.dumps(tasks, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    binding_path = workspace / "binding.json"
    if not binding_path.is_file():
        binding_path.write_text(json.dumps(state["binding"], ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    else:
        require(load_json(binding_path) == state["binding"], "专项工作区绑定另一候选")
    results_path = workspace / "evidence" / "product" / "results" / f"{mode}.json"
    evidence = workspace / "evidence" / "product" / "scenarios"
    adapter = suite_root / "adapters" / "product" / "verify.py"
    maximum = deadline - time.perf_counter()
    require(maximum > 0, f"{mode} 的资源准入已经耗尽该层总成本预算")
    command = [
        sys.executable, str(adapter), "--binary", str(binary),
        "--codex-binary", str(Path(section["codex_binary"]).resolve()),
        "--codex-auth-file", str(Path(section["codex_auth_file"]).resolve()),
        "--codex-model", str(section["codex_model"]), "--codex-reasoning-effort", str(section["codex_reasoning_effort"]),
        "--tasks", str(tasks_path), "--binding", str(binding_path), "--resource-report", str(resource),
        "--evidence-dir", str(evidence), "--output", str(results_path), "--max-wall-seconds", str(maximum),
    ]
    if resume:
        command.append("--resume")
    run(command, cwd=suite_root.parents[2], timeout=maximum)
    results = load_json(results_path)
    score_binding = {"candidate": state["binding"]["candidate"], "binary_sha256": state["binding"]["binary_sha256"], "environment": {"sha256": state["binding"]["environment_sha256"]}, "inputs": {"sha256": state["binding"]["input_manifest_sha256"]}}
    report = product.score_results(contract, dataset, qualification, tasks, results, score_binding)
    validate_layer_report(contract, "product", report, expected_binding=state["binding"])
    return report
