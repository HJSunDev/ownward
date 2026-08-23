from __future__ import annotations

import json
import hashlib
import os
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
import binding as candidate_binding
import resource_environment


def _write_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(f".{path.name}.{os.getpid()}.{time.time_ns()}.tmp")
    with temporary.open("w", encoding="utf-8", newline="\n") as stream:
        stream.write(json.dumps(value, ensure_ascii=False, indent=2) + "\n")
        stream.flush()
        os.fsync(stream.fileno())
    os.replace(temporary, path)


def _json_sha256(value: Any) -> str:
    data = json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
    return hashlib.sha256(data).hexdigest()


def _activate_workspace_binding(workspace: Path, current: dict[str, Any], *, resume: bool) -> Path:
    binding_path = workspace / "binding.json"
    if not binding_path.is_file():
        _write_json(binding_path, current)
        return binding_path
    previous = load_json(binding_path)
    require(isinstance(previous, dict), "专项工作区旧绑定无效")
    if previous == current:
        return binding_path
    require(resume, "专项工作区绑定已变化；使用 --resume 恢复同一候选")
    for name in ("suite_version", "candidate", "binary_sha256"):
        require(previous.get(name) == current.get(name), "专项工作区绑定另一候选或二进制")
    archive = workspace / "evidence" / "product" / "_audit" / "workspace-bindings" / f"{_json_sha256(previous)}.json"
    if archive.is_file():
        require(load_json(archive) == previous, "专项工作区旧绑定归档发生变化")
    else:
        _write_json(archive, previous)
    _write_json(binding_path, current)
    return binding_path


def _product_command(
    suite_root: Path, state: dict[str, Any], config: dict[str, Any], tasks_path: Path,
    binding_path: Path, resource: Path, evidence: Path, output: Path, maximum: float,
) -> list[str]:
    section = config["product"]
    return [
        sys.executable, str(suite_root / "adapters" / "product" / "verify.py"),
        "--binary", str(product_binary(config, state)),
        "--codex-binary", str(Path(section["codex_binary"]).resolve()),
        "--codex-auth-file", str(Path(section["codex_auth_file"]).resolve()),
        "--codex-model", str(section["codex_model"]), "--codex-reasoning-effort", str(section["codex_reasoning_effort"]),
        "--tasks", str(tasks_path), "--binding", str(binding_path), "--resource-report", str(resource),
        "--evidence-dir", str(evidence), "--output", str(output), "--max-wall-seconds", str(maximum),
    ]


def _ensure_product_preflight(
    suite_root: Path, state: dict[str, Any], config: dict[str, Any], workspace: Path,
    tasks: dict[str, Any], tasks_path: Path, binding_path: Path, resource: Path, deadline: float,
    *,
    resume: bool,
) -> None:
    root = workspace / "evidence" / "product-preflight"
    report_path = root / "report.json"
    expected = {
        "qualification_binding": state["binding"],
        "task_set_sha256": _json_sha256(tasks),
        "resource_report_sha256": lifecycle.file_sha256(resource),
    }
    if report_path.is_file():
        report = load_json(report_path)
        actual = {
            "qualification_binding": report.get("qualification_binding"),
            "task_set_sha256": report.get("task_set_sha256"), "resource_report_sha256": report.get("resource_report_sha256"),
        }
        if actual == expected and report.get("passed") is True:
            _require_product_preflight_budget(report, deadline)
            return
        require(resume, "专项执行预检未通过或绑定已变化；使用 --resume 保留现场后恢复")
        archive = root / "_audit" / "reports" / f"{_json_sha256(report)}.json"
        if archive.is_file():
            require(load_json(archive) == report, "专项执行预检旧报告归档发生变化")
        else:
            _write_json(archive, report)
    elif root.exists():
        require(resume, "专项执行预检未完成；使用 --resume 恢复")
    maximum = min(420.0, deadline - time.perf_counter())
    require(maximum > 0, "专项执行预检没有剩余成本预算")
    command = _product_command(
        suite_root, state, config, tasks_path, binding_path, resource,
        root / "scenarios", root / "unused-results.json", maximum,
    )
    command.extend(["--preflight-only", "--preflight-output", str(report_path)])
    if resume:
        command.append("--resume")
    run(command, cwd=suite_root.parents[2], timeout=maximum)
    report = load_json(report_path)
    require(report.get("passed") is True, "专项执行预检未证明资格集能在三十分钟内完成")
    require(report.get("qualification_binding") == state["binding"], "专项执行预检没有绑定当前完整 product scope")
    _require_product_preflight_budget(report, deadline)


def _require_product_preflight_budget(report: dict[str, Any], deadline: float) -> None:
    projected = float(report.get("projected", {}).get("wall_seconds", 0))
    remaining = deadline - time.perf_counter()
    require(projected > 0 and projected <= max(0.0, remaining - 60.0), "专项资格集预计耗时超出本层剩余预算，已在正式执行前停止")


def _resource_identity(suite_root: Path, state: dict[str, Any], config: dict[str, Any]) -> dict[str, Any]:
    section = config["product"]
    package = Path(section["package"]).resolve()
    production = Path(section["production_storage_report"]).resolve()
    adapter = suite_root / "adapters" / "product_resource" / "verify.py"
    thresholds = adapter.with_name("thresholds.json")
    support = suite_root.parents[2] / "benchmarks" / "support" / "ownward_mcp.py"
    package_files = [
        {"path": path.relative_to(package).as_posix(), "size": path.stat().st_size, "sha256": lifecycle.file_sha256(path)}
        for path in sorted(item for item in package.rglob("*") if item.is_file() and not item.is_symlink())
    ]
    return {
        "schema": "ownward.product-resource-binding/v1",
        "candidate": state["binding"]["candidate"],
        "binary_sha256": state["binding"]["binary_sha256"],
        "package_files": package_files,
        "production_storage_report_sha256": lifecycle.file_sha256(production),
        "tools": {
            path.relative_to(suite_root.parents[2]).as_posix(): lifecycle.file_sha256(path)
            for path in (adapter, thresholds, support, suite_root / "resource_environment.py")
        },
        "machine": resource_environment.machine_identity(),
    }


def _raw_evidence(report: dict[str, Any], report_path: Path) -> dict[str, str]:
    evidence = report.get("evidence")
    require(isinstance(evidence, dict) and set(evidence) == {"package_manifest", "process_samples", "workload_results"}, "候选资源报告缺少完整原始证据")
    result: dict[str, str] = {}
    evidence_root = report_path.resolve().parent
    for name, item in evidence.items():
        require(isinstance(item, dict), f"候选资源证据 {name} 无效")
        path = Path(str(item.get("path", ""))).resolve()
        require(path.is_relative_to(evidence_root) and path.is_file(), f"候选资源证据 {name} 缺失")
        digest = lifecycle.file_sha256(path)
        require(digest == item.get("sha256"), f"候选资源证据 {name} 发生变化")
        result[name] = digest
    return result


def _verify_resource_dependencies(report: dict[str, Any], report_path: Path, identity: dict[str, Any]) -> dict[str, str]:
    evidence_hashes = _raw_evidence(report, report_path)
    package_evidence = load_json(Path(report["evidence"]["package_manifest"]["path"]))
    workload_evidence = load_json(Path(report["evidence"]["workload_results"]["path"]))
    current_package = {
        item["path"]: {"bytes": item["size"], "sha256": item["sha256"]}
        for item in identity["package_files"] if item["path"] != "manifest.json"
    }
    manifest = next((item for item in identity["package_files"] if item["path"] == "manifest.json"), None)
    require(manifest is not None, "当前候选发布包缺少 manifest.json")
    require(package_evidence.get("files") == current_package, "资源报告绑定的发布包已经变化")
    require(package_evidence.get("release_manifest_sha256") == manifest["sha256"], "资源报告绑定的发布清单已经变化")
    require(workload_evidence.get("production_storage_report_sha256") == identity["production_storage_report_sha256"], "资源报告绑定的生产存储证据已经变化")
    require(report.get("environment") == identity["machine"], "资源报告绑定的机器环境已经变化")
    return evidence_hashes


def _bind_interrupted_resource_report(
    suite_root: Path, state: dict[str, Any], config: dict[str, Any], report: dict[str, Any], report_path: Path,
) -> Path:
    identity = _resource_identity(suite_root, state, config)
    require(report.get("candidate") == identity["candidate"] and report.get("release_binary_sha256") == identity["binary_sha256"], "未绑定资源报告不属于当前候选")
    _verify_resource_dependencies(report, report_path, identity)
    bound = dict(report)
    bound["resource_binding"] = identity
    _write_json(report_path, bound)
    return report_path


def _bind_legacy_resource_report(
    suite_root: Path, state: dict[str, Any], config: dict[str, Any], report: dict[str, Any], report_path: Path
) -> Path:
    identity = _resource_identity(suite_root, state, config)
    require(report.get("acceptance_binding", {}).get("candidate") == identity["candidate"], "旧资源报告不属于当前候选")
    evidence_hashes = _verify_resource_dependencies(report, report_path, identity)

    # The old product tool manifest is the only trustworthy source for the
    # resource adapter code used by a legacy report.
    old_tool_sha = report["acceptance_binding"].get("tool_sha256")
    manifests = list(Path(config["binding_dir"]).resolve().rglob("product-tools.json"))
    old_manifest = next((load_json(path) for path in manifests if lifecycle.file_sha256(path) == old_tool_sha), None)
    require(isinstance(old_manifest, dict), "找不到旧资源报告绑定的工具清单")
    old_files = {str(item.get("path")): str(item.get("sha256")) for item in old_manifest.get("files", []) if isinstance(item, dict)}
    for relative, digest in identity["tools"].items():
        require(old_files.get(relative) == digest, f"旧资源报告的真实测量依赖已经变化: {relative}")

    receipt = {
        "schema": "ownward.product-resource-reuse/v1",
        "resource_binding": identity,
        "legacy_report": {"path": str(report_path), "sha256": lifecycle.file_sha256(report_path)},
        "raw_evidence": evidence_hashes,
    }
    receipt_path = report_path.parent / "reuse-receipt.json"
    if receipt_path.is_file():
        require(load_json(receipt_path) == receipt, "资源复用凭据与当前真实依赖不一致")
    else:
        _write_json(receipt_path, receipt)
    effective = dict(report)
    effective.pop("acceptance_binding", None)
    effective["resource_binding"] = identity
    effective["reuse_receipt"] = {"path": str(receipt_path), "sha256": lifecycle.file_sha256(receipt_path)}
    effective_path = report_path.parent / "bound-report.json"
    if effective_path.is_file():
        require(load_json(effective_path) == effective, "已绑定资源报告与不可变复用凭据不一致")
    else:
        _write_json(effective_path, effective)
    return effective_path


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
        report = load_json(report_path)
        identity = _resource_identity(suite_root, state, config)
        if report.get("resource_binding") != identity:
            try:
                if "resource_binding" not in report and "acceptance_binding" not in report:
                    report_path = _bind_interrupted_resource_report(suite_root, state, config, report, report_path)
                elif "acceptance_binding" in report:
                    report_path = _bind_legacy_resource_report(suite_root, state, config, report, report_path)
                else:
                    raise ExecutionError("候选资源报告绑定的真实依赖已经变化")
                report = load_json(report_path)
            except (ExecutionError, KeyError, ValueError) as error:
                require(resume, f"候选资源证据已失效；使用 --resume 只重做资源测量: {error}")
                safe_remove(report_path.parent, workspace / "evidence")
                report_path = workspace / "evidence" / "product-resource" / "report.json"
                report = None
        if report is not None:
            require_resource_admission(report, state, report_path, resource_binding=identity)
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
    report["resource_binding"] = _resource_identity(suite_root, state, config)
    _write_json(report_path, report)
    require_resource_admission(report, state, report_path, resource_binding=report["resource_binding"])
    return report_path


def require_resource_admission(
    report: dict[str, Any], state: dict[str, Any], report_path: Path | None = None,
    *, resource_binding: dict[str, Any] | None = None,
) -> None:
    require(report.get("schema") == "ownward.delivery-resource-report/v1", "候选资源报告 schema 无效")
    require(report.get("candidate") == state["binding"]["candidate"] and report.get("release_binary_sha256") == state["binding"]["binary_sha256"], "候选资源报告绑定另一候选")
    require(resource_binding is not None and report.get("resource_binding") == resource_binding, "候选资源报告没有绑定当前真实资源依赖")
    if report_path is not None:
        _raw_evidence(report, report_path)
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
    require(deadline - time.perf_counter() > 0, f"{mode} 的资源准入已经耗尽该层总成本预算")
    dataset, qualification = product.load_default_materials(suite_root)
    tasks = product.prepare_tasks(dataset, qualification, mode)
    tasks_path = workspace / "tasks" / f"{mode}.json"
    tasks_path.parent.mkdir(parents=True, exist_ok=True)
    if tasks_path.is_file():
        require(load_json(tasks_path) == tasks, "冻结专项任务发生变化")
    else:
        tasks_path.write_text(json.dumps(tasks, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    binding_path = _activate_workspace_binding(workspace, state["binding"], resume=resume)
    if mode == "qualification":
        _ensure_product_preflight(
            suite_root, state, config, workspace, tasks, tasks_path, binding_path, resource, deadline,
            resume=resume,
        )
    results_path = workspace / "evidence" / "product" / "results" / f"{mode}.json"
    evidence = workspace / "evidence" / "product" / "scenarios"
    maximum = deadline - time.perf_counter()
    require(maximum > 0, f"{mode} 的资源准入已经耗尽该层总成本预算")
    command = _product_command(suite_root, state, config, tasks_path, binding_path, resource, evidence, results_path, maximum)
    if resume:
        command.append("--resume")
    run(command, cwd=suite_root.parents[2], timeout=maximum)
    results = load_json(results_path)
    score_binding = {"candidate": state["binding"]["candidate"], "binary_sha256": state["binding"]["binary_sha256"], "environment": {"sha256": state["binding"]["environment_sha256"]}, "inputs": {"sha256": state["binding"]["input_manifest_sha256"]}}
    report = product.score_results(contract, dataset, qualification, tasks, results, score_binding)
    validate_layer_report(contract, "product", report, expected_binding=state["binding"])
    return report
