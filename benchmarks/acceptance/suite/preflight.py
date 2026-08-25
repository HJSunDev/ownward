from __future__ import annotations

import hashlib
import json
import math
import os
import shutil
import subprocess
from pathlib import Path
from typing import Any

import binding
import relationships


class PreflightError(ValueError):
    pass


def run(suite_root: Path, config: dict[str, Any], isolation_dir: Path) -> dict[str, Any]:
    try:
        binding.validate_config(config)
        scopes = relationships.enabled_scopes(config)
    except (binding.BindingError, relationships.RelationshipError) as error:
        raise PreflightError(str(error)) from error
    isolation_dir = isolation_dir.resolve()
    _require(isolation_dir.drive.upper() != "C:", "验收隔离目录不得位于系统盘")
    _require(not isolation_dir.exists(), "验收隔离目录必须为空白且尚未存在")
    isolation_dir.mkdir(parents=True)
    probe = isolation_dir / ".write-probe"
    probe.write_text("ok", encoding="utf-8")
    probe.unlink()
    free_bytes = shutil.disk_usage(isolation_dir.parent).free

    checks: dict[str, Any] = {}
    if "frontier" in scopes:
        observer = Path(config["frontier"]["tool"]).resolve()
        _require(observer.is_file(), "内核观察器不存在")
        checks["frontier"] = {"observer_sha256": binding.sha256(observer)}
    if set(scopes) & {"core", "product", "community"}:
        binary = Path(config["candidate"]["binary"]).resolve()
        bundle = Path(config["candidate"]["embedding_bundle_dir"]).resolve()
        _require(binary.is_file(), "候选二进制不存在")
        _require(bundle.is_dir(), "本地模型能力目录不存在")
        try:
            embedding = binding._embedding_identity(bundle)
        except binding.BindingError as error:
            raise PreflightError(str(error)) from error
        checks["candidate"] = {
            "binary_sha256": binding.sha256(binary),
            "embedding_capability": embedding.get("capability"),
            "embedding_files": len(embedding["runtime_files"]),
        }
    if "product" in scopes:
        product = config["product"]
        codex = Path(product["codex_binary"]).resolve()
        auth = Path(product["codex_auth_file"]).resolve()
        package = Path(product["package"]).resolve()
        production = Path(product["production_storage_report"]).resolve()
        _require(codex.is_file(), "外部智能体执行程序不存在")
        _require(auth.is_file(), "外部智能体认证文件不存在")
        _require(package.is_dir() and (package / "manifest.json").is_file(), "候选发布包或清单不存在")
        _require(production.is_file(), "生产规模存储证据不存在")
        completed = subprocess.run([*binding._executable_command(codex), "--version"], capture_output=True, text=True, encoding="utf-8", timeout=30, check=False)
        _require(completed.returncode == 0 and completed.stdout.strip(), "外部智能体执行程序不可运行")
        checks["product"] = {"codex_binary_sha256": binding.sha256(codex), "codex_version": completed.stdout.strip(), "codex_auth_available": True}
    if "community" in scopes:
        community = config["community"]
        try:
            checks["community"] = _community_preflight(suite_root, config, isolation_dir)
        except BaseException:
            if isolation_dir.exists():
                shutil.rmtree(isolation_dir)
            raise
        _require(free_bytes >= 20 * 1024**3, "社区验收隔离目录可用磁盘空间不足 20 GiB")

    if isolation_dir.exists():
        shutil.rmtree(isolation_dir)

    report: dict[str, Any] = {
        "schema": "ownward.acceptance-preflight/v2",
        "formal_evidence": False,
        "passed": True,
        "enabled_scopes": list(scopes),
        "checks": checks,
        "isolation_root": str(isolation_dir.parent),
        "free_bytes": free_bytes,
    }
    if "community" in scopes:
        report["cost_bound"] = {
            "only_multi_hour_step": "LongMemEval-S", "max_wall_seconds": 28800, "formal_questions": 500,
            "source": "four complete three-semantic-batch representative questions",
            "calibrated_projected_wall_seconds": checks["community"]["projected_full_wall_seconds"],
        }
    return report


COMMUNITY_CALIBRATION_TYPES = ("knowledge-update", "multi-session", "single-session-assistant", "temporal-reasoning")


def _community_calibration_fixture(official_questions: list[Any], protocol: dict[str, Any]) -> list[dict[str, Any]]:
    expected_batches = int(protocol["execution"]["calibration_semantic_batches_per_question"])
    semantic_batch_size = int(protocol["memory"]["semantic_batch_size"])
    fixture = []
    for question_type in COMMUNITY_CALIBRATION_TYPES:
        selected = next((
            item for item in official_questions
            if isinstance(item, dict)
            and item.get("question_type") == question_type
            and (len(item.get("haystack_sessions", [])) + semantic_batch_size - 1) // semantic_batch_size == expected_batches
        ), None)
        _require(selected is not None, f"LongMemEval-S 缺少三批次代表题型: {question_type}")
        fixture.append(selected)
    _require(len(fixture) == int(protocol["execution"]["calibration_questions"]), "LongMemEval-S 代表校准题数改变")
    return fixture


def _community_preflight(suite_root: Path, config: dict[str, Any], isolation_dir: Path) -> dict[str, Any]:
    adapters = binding.load_json(suite_root / "adapters.json")
    community = adapters["layers"]["community"]
    adapter = (suite_root / community["adapter"]).resolve()
    protocol_path = (suite_root / community["protocol"]).resolve()
    _require(adapter.is_file() and protocol_path.is_file(), "LongMemEval-S 适配器或协议不存在")
    revision = community["official_revision"]
    source = adapter.read_text(encoding="utf-8")
    _require(f'OFFICIAL_CODE_REVISION = "{revision}"' in source, "LongMemEval-S 校验路径未绑定固定版本")
    section = config["community"]
    codex = Path(section["codex_binary"]).resolve()
    auth = Path(section["codex_auth_file"]).resolve()
    _require(codex.is_file() and auth.is_file(), "LongMemEval-S Codex capability is unavailable")
    codex_version = subprocess.run(
        [*binding._executable_command(codex), "--version"], capture_output=True, text=True,
        encoding="utf-8", errors="replace", timeout=30, check=False,
    )
    _require(codex_version.returncode == 0 and codex_version.stdout.strip(), "LongMemEval-S Codex capability cannot start")
    manifest_path = Path(section["environment_manifest"]).resolve()
    manifest = binding.load_json(manifest_path)
    layout = manifest.get("layout")
    _require(isinstance(layout, dict), "LongMemEval-S 持久环境清单不完整")
    python_root = Path(layout["python"]).resolve()
    python = python_root / ("Scripts/python.exe" if os.name == "nt" else "bin/python")
    data = Path(layout["data"]).resolve()
    runs = Path(layout["runs"]).resolve()
    check = subprocess.run(
        [str(python), str(adapter), "check", "--environment-manifest", str(manifest_path), "--protocol", str(protocol_path)],
        capture_output=True, text=True, encoding="utf-8", errors="replace", timeout=180, check=False,
    )
    _require(check.returncode == 0, f"LongMemEval-S 离线环境检查失败: {check.stderr[-2000:]}")
    protocol = binding.load_json(protocol_path)
    official_questions = json.loads(data.read_text(encoding="utf-8"))
    _require(isinstance(official_questions, list), "LongMemEval-S 固定数据不是问题数组")
    expected_batches = int(protocol["execution"]["calibration_semantic_batches_per_question"])
    fixture = _community_calibration_fixture(official_questions, protocol)
    fixture_path = isolation_dir / "longmemeval-s-fixture.json"
    fixture_path.write_text(json.dumps(fixture, ensure_ascii=False, indent=2), encoding="utf-8")
    candidate = subprocess.run(["git", "rev-parse", "HEAD"], cwd=suite_root.parents[2], capture_output=True, text=True, encoding="utf-8", check=False)
    _require(candidate.returncode == 0 and len(candidate.stdout.strip()) == 40, "无法读取候选提交")
    identity_token = hashlib.sha256(adapter.read_bytes() + protocol_path.read_bytes()).hexdigest()[:16]
    output = runs / "preflight" / f"{candidate.stdout.strip()[:8]}-{identity_token}"
    command = [
        str(python), str(adapter), "run", "--non-formal", "--environment-manifest", str(manifest_path), "--protocol", str(protocol_path),
        "--dataset", str(fixture_path), "--output-dir", str(output), "--ownward-binary", str(config["candidate"]["binary"]),
        "--embedding-bundle-dir", str(config["candidate"]["embedding_bundle_dir"]), "--candidate", candidate.stdout.strip(),
        "--codex-binary", str(codex), "--codex-auth-file", str(auth),
        "--environment-sha256", binding.sha256(manifest_path), "--input-manifest-sha256", binding.sha256(data),
        "--tool-sha256", hashlib.sha256(adapter.read_bytes() + protocol_path.read_bytes()).hexdigest(),
        "--judge-api-key-env", section["judge_api_key_env"], "--judge-fixture", "yes",
    ]
    if output.exists():
        command.append("--resume")
    completed = subprocess.run(command, capture_output=True, text=True, encoding="utf-8", errors="replace", timeout=1800, check=False)
    _require(completed.returncode == 0, f"LongMemEval-S 隔离预检失败: {completed.stderr[-3000:]}")
    result = binding.load_json(output / "report.json")
    _require(result.get("formal") is False and result.get("questions") == 4 and result.get("passed") is True, "LongMemEval-S 隔离预检未打通完整生命周期")
    questions = [binding.load_json(output / "questions" / item["question_id"] / "result.json") for item in fixture]
    plans = [binding.load_json(output / "questions" / item["question_id"] / "semantic-plan.json") for item in fixture]
    analysis_units = [unit for plan in plans for batch in plan["batches"] for unit in batch["analysis_units"]]
    semantic_batches = sum(int(item["semantic_batches"]) for item in questions)
    semantic_calls = sum(int(item["usage"]["semantic"]["calls"]) for item in questions)
    _require(len(analysis_units) == semantic_calls, "LongMemEval-S 分析单元与 Codex 调用量不一致")
    _require(max(int(unit["input_chars"]) for unit in analysis_units) <= int(protocol["memory"]["semantic_analysis_max_input_chars"]), "LongMemEval-S 分析单元超过冻结输入边界")
    _require(all(int(item["semantic_batches"]) == expected_batches for item in questions), "LongMemEval-S 代表样本不是完整三批次问题")
    _require(all(item["semantic_execution"]["serial_concurrent_equivalent"] for item in questions), "并发语义载荷与串行计划不等价")
    _require(all(item["semantic_execution"]["submission_order"] == list(range(expected_batches)) for item in questions), "语义批次未按原序完整提交")
    semantic_model_seconds = sum(float(item["usage"]["semantic"]["wall_seconds"]) for item in questions)
    reader_model_seconds = sum(float(item["usage"]["reader"]["wall_seconds"]) for item in questions)
    per_question_host = sum(
        sum(float(item["phase_seconds"][name]) for name in ("create", "retrieval", "other"))
        for item in questions
    ) / len(questions)
    workers = int(protocol["execution"]["max_workers"])
    codex_limit = int(protocol["execution"]["codex_max_active"])
    semantic_unit = semantic_model_seconds / semantic_calls
    reader_unit = reader_model_seconds / len(questions)
    question_count = int(protocol["official"]["question_count"])
    projected_semantic_requests = math.ceil(
        semantic_calls / semantic_batches * int(protocol["execution"]["semantic_batches"])
    )
    projected_semantic = semantic_unit * projected_semantic_requests / codex_limit
    projected_reader = reader_unit * question_count / min(workers, codex_limit)
    projected_host = per_question_host * question_count / workers
    projected_judge = float(protocol["judge"]["budget_seconds_per_question"]) * question_count / workers
    projected = projected_semantic + projected_reader + projected_host + projected_judge
    projected_serial = semantic_unit * projected_semantic_requests / workers + projected_reader + projected_host + projected_judge
    codex = result["cost"]["codex"]
    expected_codex_calls = semantic_calls + len(questions)
    maximum_bounded_retries = max(1, math.floor(expected_codex_calls * 0.1))
    _require(int(codex["calls"]) == expected_codex_calls, "LongMemEval-S Codex 调用量不完整")
    _require(int(codex["attempts"]) == expected_codex_calls + int(codex["retries"]), "LongMemEval-S Codex 尝试计数不一致")
    _require(int(codex["retries"]) <= maximum_bounded_retries, f"并发 {codex_limit} 的代表校准出现异常重试密度")
    _require(int(codex["rate_limit_events"]) == 0 and int(codex["interrupted_attempts"]) == 0, f"并发 {codex_limit} 的代表校准存在限流或中断")
    _require(int(codex["scheduler"]["limit"]) == codex_limit and int(codex["scheduler"]["max_active"]) == codex_limit, "代表校准未实际达到全局 Codex 并发上限")
    _require(int(result["cost"]["semantic_submitted_batches"]) == semantic_batches, "代表校准未提交全部语义批次")
    _require(projected < projected_serial * 0.8, "全局并发 8 未形成显著墙钟收益")
    _require(projected <= float(protocol["execution"]["full_wall_seconds"]), "LongMemEval-S measured Codex path exceeds the frozen full-run wall budget")
    return {
        "official_revision": revision, "data_sha256": community["data_sha256"], "persistent_environment": str(manifest_path),
        "offline_check": "passed", "fixture_questions": len(fixture),
        "fixture_question_ids": [item["question_id"] for item in fixture],
        "fixture_question_types": [item["question_type"] for item in fixture],
        "fixture_sessions": sum(len(item["haystack_sessions"]) for item in fixture),
        "fixture_semantic_batches": semantic_batches,
        "fixture_semantic_analysis_calls": semantic_calls,
        "fixture_analysis_input_chars": {
            "maximum": max(int(unit["input_chars"]) for unit in analysis_units),
            "mean": sum(int(unit["input_chars"]) for unit in analysis_units) / len(analysis_units),
            "frozen_maximum": protocol["memory"]["semantic_analysis_max_input_chars"],
        },
        "fixture_accuracy": result["accuracy"],
        "fixture_wall_seconds": result["cost"]["wall_seconds"], "projected_full_wall_seconds": projected,
        "projected_serial_full_wall_seconds": projected_serial,
        "projection_components": {
            "semantic": projected_semantic, "reader": projected_reader,
            "host": projected_host, "official_judge_reserve": projected_judge,
        },
        "projected_request_counts": {
            "semantic_work": protocol["execution"]["semantic_work_requests"],
            "semantic_analysis": projected_semantic_requests,
            "reader": protocol["execution"]["reader_requests"],
            "judge": protocol["execution"]["judge_requests"],
        },
        "codex_concurrency": {**codex["scheduler"], "frozen": codex_limit, "higher_limit_evaluated": False},
        "codex_calls": {
            **{name: codex[name] for name in ("calls", "attempts", "retries", "rate_limit_events", "interrupted_attempts")},
            "maximum_bounded_retries": maximum_bounded_retries,
        },
        "codex_tokens": {
            name: result["cost"][name]
            for name in ("semantic_input_tokens", "semantic_output_tokens", "reader_input_tokens", "reader_output_tokens")
        },
        "semantic_plan_equivalent": True, "semantic_submission_complete": True,
        "semantic_transport": "codex-cli", "reader_transport": "codex-cli",
        "codex_version": codex_version.stdout.strip(),
        "judge_transport": "deterministic-isolated-fixture", "formal_judge_transport_checked": False,
        "reserved_official_judge_seconds_per_question": protocol["judge"]["budget_seconds_per_question"],
        "frozen_full_wall_seconds_max": protocol["execution"]["full_wall_seconds"],
        "frozen_request_counts": {name: protocol["execution"][name] for name in ("semantic_work_requests", "reader_requests", "judge_requests")},
        "preflight_report": str((output / "report.json").resolve()),
    }


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise PreflightError(message)
