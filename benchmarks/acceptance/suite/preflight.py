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
import report_relationships as relationships


class PreflightError(ValueError):
    pass


def _community_cost_projection(
    *, semantic_model_seconds: float, semantic_calls: int, reader_model_seconds: float,
    judge_model_seconds: float, calibration_questions: int, per_question_host_seconds: float,
    projected_semantic_requests: int, question_count: int, question_workers: int,
    codex_max_active: int, normal_variation_reserve_ratio: float,
    bounded_retry_reserve_ratio: float, checkpoint_recovery_reserve_seconds: float,
) -> dict[str, float]:
    """Project independent semantic work at Codex capacity; question stages stay question-bound."""
    semantic = semantic_model_seconds / semantic_calls * projected_semantic_requests / codex_max_active
    reader = reader_model_seconds / calibration_questions * question_count / question_workers
    judge = judge_model_seconds / calibration_questions * question_count / question_workers
    host = per_question_host_seconds * question_count / question_workers
    projected = semantic + reader + judge + host
    normal_variation = projected * normal_variation_reserve_ratio
    bounded_retry = (semantic + reader + judge) * bounded_retry_reserve_ratio
    required_ceiling = projected + normal_variation + bounded_retry + checkpoint_recovery_reserve_seconds
    return {
        "semantic": semantic, "reader": reader, "judge": judge, "host": host,
        "projected": projected, "normal_variation": normal_variation,
        "bounded_retry": bounded_retry, "checkpoint_recovery": checkpoint_recovery_reserve_seconds,
        "required_ceiling": required_ceiling,
    }


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
            "only_multi_hour_step": "LongMemEval-S", "max_wall_seconds": checks["community"]["frozen_full_wall_seconds_max"], "formal_questions": 500,
            "source": "official 500-question deterministic dry-plan plus four complete representative questions",
            "calibrated_projected_wall_seconds": checks["community"]["projected_full_wall_seconds"],
            "required_ceiling_wall_seconds": checks["community"]["required_ceiling_wall_seconds"],
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
    product_binary_sha256 = binding.sha256(Path(config["candidate"]["binary"]).resolve())
    calibration_candidate = f"preflight-{product_binary_sha256}"
    transport_path = adapter.with_name("codex_app_server.py")
    _require(transport_path.is_file(), "LongMemEval-S Codex App Server transport is missing")
    community_tool_sha256 = hashlib.sha256(adapter.read_bytes() + transport_path.read_bytes() + protocol_path.read_bytes()).hexdigest()
    dry_plan_token = hashlib.sha256(json.dumps({
        "transport": "ownward.longmemeval-s-semantic-transport/v2",
        "memory": protocol["memory"],
        "binary_sha256": product_binary_sha256,
        "dataset_sha256": binding.sha256(data),
    }, sort_keys=True, separators=(",", ":")).encode("utf-8")).hexdigest()[:16]
    semantic_token = hashlib.sha256(json.dumps({
        "dry_plan_token": dry_plan_token,
        "app_server_sha256": binding.sha256(transport_path),
        "calibration_codex_max_active": protocol["execution"]["codex_max_active"],
    }, sort_keys=True, separators=(",", ":")).encode("utf-8")).hexdigest()[:16]
    dry_plan_output = runs / "dry-plan" / f"{product_binary_sha256[:8]}-{dry_plan_token}"
    expected_dry_plan_sources = {
        "binary_sha256": product_binary_sha256,
        "environment_sha256": binding.sha256(manifest_path),
        "input_manifest_sha256": binding.sha256(data),
        "dataset_sha256": binding.sha256(data),
    }
    dry_plan_reused = False
    for identity_path in sorted((runs / "dry-plan").glob("*/identity.json")) if (runs / "dry-plan").is_dir() else []:
        identity = binding.load_json(identity_path)
        candidate_output = identity_path.parent
        candidate_report = binding.load_json(candidate_output / "report.json") if (candidate_output / "report.json").is_file() else {}
        if (
            all(identity.get(name) == value for name, value in expected_dry_plan_sources.items())
            and candidate_report.get("complete") is True
            and candidate_report.get("model_invoked") is False
            and candidate_report.get("questions") == int(protocol["official"]["question_count"])
            and candidate_report.get("semantic_work_batches") == int(protocol["execution"]["semantic_work_requests"])
        ):
            dry_plan_output = candidate_output
            dry_plan_reused = True
            break
    dry_plan_command = [
        str(python), str(adapter), "dry-plan", "--environment-manifest", str(manifest_path), "--protocol", str(protocol_path),
        "--dataset", str(data), "--output-dir", str(dry_plan_output), "--ownward-binary", str(config["candidate"]["binary"]),
        "--embedding-bundle-dir", str(config["candidate"]["embedding_bundle_dir"]), "--candidate", calibration_candidate,
        "--environment-sha256", binding.sha256(manifest_path), "--input-manifest-sha256", binding.sha256(data),
    ]
    if not dry_plan_reused:
        if dry_plan_output.exists():
            dry_plan_command.append("--resume")
        dry_plan_process = subprocess.run(
            dry_plan_command, capture_output=True, text=True, encoding="utf-8", errors="replace", timeout=7200, check=False,
        )
        _require(dry_plan_process.returncode == 0, f"LongMemEval-S 全量确定性 dry-plan 失败: {dry_plan_process.stderr[-3000:]}")
    dry_plan = binding.load_json(dry_plan_output / "report.json")
    _require(
        dry_plan.get("complete") is True
        and dry_plan.get("questions") == int(protocol["official"]["question_count"])
        and dry_plan.get("sessions") == int(protocol["execution"]["total_sessions"])
        and dry_plan.get("model_invoked") is False
        and dry_plan.get("all_work_preserved") is True
        and dry_plan.get("all_bodies_deduplicated_per_analysis_scope") is True,
        "LongMemEval-S 全量 dry-plan 不完整",
    )
    pool_calibration_path = runs / "calibration" / "appserver-pool-natural-batch-v1" / "report.json"
    _require(pool_calibration_path.is_file(), "LongMemEval-S App Server 池校准证据不存在")
    historical_pool_calibration = binding.load_json(pool_calibration_path)
    _require(
        historical_pool_calibration.get("facts_equivalent_across_pool_sizes") is True,
        "LongMemEval-S 历史 App Server 池校准缺少事实等价证明",
    )
    output = runs / "preflight" / f"{product_binary_sha256[:8]}-{semantic_token}-production-profile"
    command = [
        str(python), str(adapter), "run", "--non-formal", "--environment-manifest", str(manifest_path), "--protocol", str(protocol_path),
        "--dataset", str(fixture_path), "--output-dir", str(output), "--ownward-binary", str(config["candidate"]["binary"]),
        "--embedding-bundle-dir", str(config["candidate"]["embedding_bundle_dir"]), "--candidate", calibration_candidate,
        "--codex-binary", str(codex), "--codex-auth-file", str(auth),
        "--environment-sha256", binding.sha256(manifest_path), "--input-manifest-sha256", binding.sha256(data),
        "--tool-sha256", community_tool_sha256,
    ]
    if output.exists():
        command.append("--resume")
    completed = subprocess.run(command, capture_output=True, text=True, encoding="utf-8", errors="replace", timeout=1800, check=False)
    _require(completed.returncode == 0, f"LongMemEval-S 隔离预检失败: {completed.stderr[-3000:]}")
    report_before_resume = (output / "report.json").read_bytes()
    checkpoint_before_resume = (output / "checkpoint-manifest.json").read_bytes()
    resume_command = list(command)
    if "--resume" not in resume_command:
        resume_command.append("--resume")
    resumed = subprocess.run(resume_command, capture_output=True, text=True, encoding="utf-8", errors="replace", timeout=180, check=False)
    _require(resumed.returncode == 0, f"LongMemEval-S 精确恢复复核失败: {resumed.stderr[-2000:]}")
    _require(
        report_before_resume == (output / "report.json").read_bytes()
        and checkpoint_before_resume == (output / "checkpoint-manifest.json").read_bytes(),
        "LongMemEval-S 完整检查点恢复改变了正式结果或证据清单",
    )
    result = binding.load_json(output / "report.json")
    _require(
        result.get("formal") is False
        and result.get("questions") == 4
        and result.get("execution", {}).get("complete") is True
        and result.get("execution", {}).get("protocol_valid") is True
        and result.get("execution", {}).get("evidence_complete") is True,
        "LongMemEval-S 隔离预检未打通完整执行与证据生命周期",
    )
    _require(
        result.get("quality", {}).get("assessment_status") == "not_determined"
        and result.get("quality", {}).get("first_version_condition_satisfied") is False
        and result.get("passed") is False,
        "LongMemEval-S 隔离预检不得伪造质量通过",
    )
    _require(result.get("profile") == protocol["acceptance"]["profile"], "LongMemEval-S 隔离预检生产口径无效")
    _require(result.get("capabilities") == {
        "semantic": {"source": "codex", "model": protocol["memory"]["semantic_model"], "reasoning_effort": protocol["memory"]["semantic_reasoning_effort"]},
        "reader": {"source": "codex", "model": protocol["reader"]["model"], "reasoning_effort": protocol["reader"]["reasoning_effort"]},
        "judge": {"source": "codex", "model": protocol["judge"]["model"], "reasoning_effort": protocol["judge"]["reasoning_effort"]},
    }, "LongMemEval-S 隔离预检未真实调用冻结的三个 Codex 模型")
    _require(result.get("diagnostics", {}).get("questions") == 4, "LongMemEval-S 隔离预检诊断链路不完整")
    questions = [binding.load_json(output / "questions" / item["question_id"] / "result.json") for item in fixture]
    plans = [binding.load_json(output / "questions" / item["question_id"] / "semantic-plan.json") for item in fixture]
    analysis_units_by_identity = {
        unit["identity"]: unit for plan in plans for batch in plan["batches"] for unit in batch["analysis_units"]
    }
    analysis_units = list(analysis_units_by_identity.values())
    semantic_batches = sum(int(item["semantic_batches"]) for item in questions)
    semantic_calls = sum(int(item["usage"]["semantic"]["calls"]) for item in questions)
    legacy_semantic_calls = sum(int(plan["transport"]["legacy_analysis_calls"]) for plan in plans)
    new_input_utf8_bytes = sum(int(plan["transport"]["new_input_utf8_bytes"]) for plan in plans)
    legacy_input_utf8_bytes = sum(int(plan["transport"]["legacy_input_utf8_bytes"]) for plan in plans)
    _require(len(analysis_units) == semantic_calls, "LongMemEval-S 分析单元与 Codex 调用量不一致")
    _require(
        semantic_calls == semantic_batches
        and all(len(unit.get("batch_indexes", [])) == 1 for unit in analysis_units),
        "LongMemEval-S 代表预检必须按原始自然工作批逐批分析，不得跨批合并或发生非必要拆分",
    )
    _require(max(int(unit["input_utf8_bytes"]) for unit in analysis_units) <= int(protocol["memory"]["semantic_analysis_input_token_upper_bound"]), "LongMemEval-S 分析单元超过冻结输入边界")
    _require(max(int(unit["output_token_upper_bound"]) for unit in analysis_units) <= int(protocol["memory"]["semantic_analysis_output_token_upper_bound"]), "LongMemEval-S 分析单元超过冻结输出边界")
    _require(
        all(unit.get("equivalence_sha256") and unit.get("fact_equivalence_sha256") for unit in analysis_units),
        "LongMemEval-S 分析单元缺少无损事实等价证明",
    )
    _require(all(int(item["semantic_batches"]) == expected_batches for item in questions), "LongMemEval-S 代表样本不是完整三批次问题")
    _require(all(item["semantic_execution"]["serial_concurrent_equivalent"] for item in questions), "并发语义载荷与串行计划不等价")
    _require(all(item["semantic_execution"]["submission_order"] == list(range(expected_batches)) for item in questions), "语义批次未按原序完整提交")
    semantic_model_seconds = sum(float(item["usage"]["semantic"]["wall_seconds"]) for item in questions)
    reader_model_seconds = sum(float(item["usage"]["reader"]["wall_seconds"]) for item in questions)
    judge_model_seconds = sum(float(item["usage"]["judge"]["wall_seconds"]) for item in questions)
    per_question_host = sum(
        sum(float(item["phase_seconds"][name]) for name in ("create", "retrieval", "other"))
        for item in questions
    ) / len(questions)
    workers = int(protocol["execution"]["max_workers"])
    codex_limit = int(protocol["execution"]["codex_max_active"])
    question_count = int(protocol["official"]["question_count"])
    projected_semantic_requests = int(dry_plan["semantic_work_batches"])
    projection = _community_cost_projection(
        semantic_model_seconds=semantic_model_seconds, semantic_calls=semantic_calls,
        reader_model_seconds=reader_model_seconds, judge_model_seconds=judge_model_seconds,
        calibration_questions=len(questions), per_question_host_seconds=per_question_host,
        projected_semantic_requests=projected_semantic_requests, question_count=question_count,
        question_workers=workers, codex_max_active=codex_limit,
        normal_variation_reserve_ratio=float(protocol["execution"]["normal_variation_reserve_ratio"]),
        bounded_retry_reserve_ratio=float(protocol["execution"]["bounded_retry_reserve_ratio"]),
        checkpoint_recovery_reserve_seconds=float(protocol["execution"]["checkpoint_recovery_reserve_seconds"]),
    )
    projected_semantic = projection["semantic"]
    projected_reader = projection["reader"]
    projected_judge = projection["judge"]
    projected_host = projection["host"]
    projected = projection["projected"]
    normal_variation_reserve = projection["normal_variation"]
    bounded_retry_reserve = projection["bounded_retry"]
    checkpoint_recovery_reserve = projection["checkpoint_recovery"]
    required_ceiling = projection["required_ceiling"]
    codex = result["cost"]["codex"]
    expected_codex_calls = semantic_calls + 2 * len(questions)
    attempt_metadata = [
        binding.load_json(path) for path in output.glob("questions/**/codex/attempt-*/metadata.json")
    ]
    completed_attempts = [item for item in attempt_metadata if item.get("outcome") == "complete"]
    completed_threads = [str(item.get("thread_id")) for item in completed_attempts]
    maximum_bounded_retries = max(1, math.floor(expected_codex_calls * 0.1))
    _require(int(codex["calls"]) == expected_codex_calls, "LongMemEval-S Codex 调用量不完整")
    _require(int(codex["attempts"]) == expected_codex_calls + int(codex["retries"]), "LongMemEval-S Codex 尝试计数不一致")
    _require(int(codex["retries"]) == 0, f"并发 {codex_limit} 的代表预检未全部首次完成")
    _require(int(codex["rate_limit_events"]) == 0 and int(codex["interrupted_attempts"]) == 0, f"并发 {codex_limit} 的代表校准存在限流或中断")
    _require(int(codex["scheduler"]["limit"]) == codex_limit and int(codex["scheduler"]["max_active"]) == codex_limit, "代表校准未实际达到全局 Codex 并发上限")
    _require(
        codex.get("transport", {}).get("transport") == "codex-app-server-pool-stdio"
        and int(codex["transport"]["server_processes"]) == codex_limit
        and int(codex["transport"]["per_worker_max_active"]) == 1,
        "代表校准未使用单 turn 的有界 Codex App Server 池",
    )
    _require(
        int(codex["transport"]["worker_restarts"]) == 0
        and int(codex["transport"]["process_starts"]) == codex_limit,
        "代表预检出现 App Server transport 失败或 worker 重启",
    )
    _require(
        len(completed_attempts) == expected_codex_calls
        and len(set(completed_threads)) == expected_codex_calls
        and all(item.get("thread_ephemeral") is True and item.get("sandbox") == "read-only" for item in completed_attempts),
        "代表校准未为每个请求使用独立、只读的新 Codex thread",
    )
    _require(int(result["cost"]["semantic_submitted_batches"]) == semantic_batches, "代表校准未提交全部语义批次")
    _require(
        semantic_calls * 2 <= legacy_semantic_calls
        and projected_semantic_requests * 2 <= int(dry_plan["legacy_semantic_analysis_calls"]),
        "LongMemEval-S 去重合并后的语义调用量未显著收敛",
    )
    _require(required_ceiling <= float(protocol["execution"]["full_wall_seconds"]), "LongMemEval-S measured path plus variation, retry, and recovery reserves exceeds the frozen full-run wall budget")
    distribution = lambda values: {
        "minimum": min(values),
        "mean": sum(values) / len(values),
        "maximum": max(values),
        "spread_ratio": (max(values) - min(values)) / (sum(values) / len(values)),
    }
    question_wall_values = [float(item["wall_seconds"]) for item in questions]
    phase_distributions = {
        name: distribution([float(item["phase_seconds"][name]) for item in questions])
        for name in ("create", "semantic", "retrieval", "reader", "judge", "other")
    }
    return {
        "official_revision": revision, "data_sha256": community["data_sha256"], "persistent_environment": str(manifest_path),
        "offline_check": "passed", "fixture_questions": len(fixture),
        "fixture_question_ids": [item["question_id"] for item in fixture],
        "fixture_question_types": [item["question_type"] for item in fixture],
        "fixture_sessions": sum(len(item["haystack_sessions"]) for item in fixture),
        "fixture_semantic_batches": semantic_batches,
        "fixture_semantic_analysis_calls": semantic_calls,
        "fixture_semantic_ab": {
            "legacy_analysis_calls": legacy_semantic_calls,
            "analysis_calls": semantic_calls,
            "legacy_input_utf8_bytes": legacy_input_utf8_bytes,
            "input_utf8_bytes": new_input_utf8_bytes,
            "legacy_codex_process_starts": legacy_semantic_calls + 2 * len(questions),
            "app_server_process_starts": codex["transport"]["process_starts"],
        },
        "fixture_analysis_input_chars": {
            "maximum": max(int(unit["input_chars"]) for unit in analysis_units),
            "mean": sum(int(unit["input_chars"]) for unit in analysis_units) / len(analysis_units),
            "maximum_utf8_bytes": max(int(unit["input_utf8_bytes"]) for unit in analysis_units),
            "frozen_input_token_upper_bound": protocol["memory"]["semantic_analysis_input_token_upper_bound"],
        },
        "dry_plan": {
            "report": str((dry_plan_output / "report.json").resolve()),
            "questions": dry_plan["questions"], "sessions": dry_plan["sessions"],
            "active_natural_batch_analysis_calls": dry_plan["semantic_work_batches"],
            "prior_merged_boundary_plan_calls": dry_plan["semantic_analysis_calls"],
            "legacy_semantic_analysis_calls": dry_plan["legacy_semantic_analysis_calls"],
            "input_utf8_bytes": dry_plan["input_utf8_bytes"],
            "legacy_input_utf8_bytes": dry_plan["legacy_input_utf8_bytes"],
            "maximum_input_utf8_bytes": dry_plan["maximum_input_utf8_bytes"],
            "maximum_output_token_upper_bound": dry_plan["maximum_output_token_upper_bound"],
        },
        "historical_pool_calibration": {
            "report": str(pool_calibration_path),
            "report_sha256": binding.sha256(pool_calibration_path),
            "evaluated_pool_sizes": [item["pool_size"] for item in historical_pool_calibration["pools"]],
            "facts_equivalent_across_pool_sizes": historical_pool_calibration["facts_equivalent_across_pool_sizes"],
        },
        "calibration_accuracy": result["accuracy"],
        "fixture_wall_seconds": result["cost"]["wall_seconds"], "projected_full_wall_seconds": projected,
        "required_ceiling_wall_seconds": required_ceiling,
        "calibration_distributions": {"question_wall": distribution(question_wall_values), "phases": phase_distributions},
        "reserves": {
            "normal_variation": normal_variation_reserve,
            "bounded_retry": bounded_retry_reserve,
            "checkpoint_recovery": checkpoint_recovery_reserve,
        },
        "projection_components": {
            "semantic": projected_semantic, "reader": projected_reader,
            "host": projected_host, "judge": projected_judge,
        },
        "projected_request_counts": {
            "semantic_work": protocol["execution"]["semantic_work_requests"],
            "semantic_analysis": projected_semantic_requests,
            "reader": protocol["execution"]["reader_requests"],
            "judge": protocol["execution"]["judge_requests"],
        },
        "codex_concurrency": {
            **codex["scheduler"], "frozen": codex_limit,
            "selection_candidates": [8, 12],
            "selection_policy": "lowest stable pool whose required ceiling is at most the frozen full-run wall budget",
            "selected": codex_limit,
            "higher_pool_not_required": codex_limit == 8 and required_ceiling <= float(protocol["execution"]["full_wall_seconds"]),
        },
        "codex_calls": {
            **{name: codex[name] for name in ("calls", "attempts", "retries", "rate_limit_events", "interrupted_attempts")},
            "maximum_bounded_retries": maximum_bounded_retries,
        },
        "codex_tokens": {
            name: result["cost"][name]
            for name in (
                "semantic_input_tokens", "semantic_output_tokens", "reader_input_tokens", "reader_output_tokens",
                "judge_input_tokens", "judge_output_tokens",
            )
        },
        "semantic_plan_equivalent": True, "semantic_submission_complete": True, "byte_exact_resume": True,
        "semantic_transport": "codex-app-server-pool-stdio", "reader_transport": "codex-app-server-pool-stdio", "judge_transport": "codex-app-server-pool-stdio",
        "codex_version": codex_version.stdout.strip(),
        "production_profile": protocol["acceptance"]["profile"],
        "judge_model": protocol["judge"]["model"],
        "judge_reasoning_effort": protocol["judge"]["reasoning_effort"],
        "frozen_full_wall_seconds_max": protocol["execution"]["full_wall_seconds"],
        "frozen_request_counts": {name: protocol["execution"][name] for name in ("semantic_work_requests", "reader_requests", "judge_requests")},
        "preflight_report": str((output / "report.json").resolve()),
    }


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise PreflightError(message)
