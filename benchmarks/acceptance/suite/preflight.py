from __future__ import annotations

import hashlib
import json
import math
import os
import shutil
import sys
from pathlib import Path
from typing import Any

import binding
import report_relationships as relationships
import process_control

SUPPORT_ROOT = Path(__file__).resolve().parents[2] / "support"
LONGMEM_ROOT = Path(__file__).resolve().parents[2] / "longmemeval_s"
for dependency_root in (SUPPORT_ROOT, LONGMEM_ROOT):
    if str(dependency_root) not in sys.path:
        sys.path.insert(0, str(dependency_root))
import external_intelligence  # noqa: E402
import external_intelligence_runtime  # noqa: E402
import semantic_representation  # noqa: E402


class PreflightError(ValueError):
    pass


def _community_cost_projection(
    *, semantic_model_seconds: float, semantic_calls: int, reader_model_seconds: float,
    judge_model_seconds: float, calibration_questions: int, per_question_host_seconds: float,
    projected_semantic_requests: int, question_count: int, question_workers: int,
    external_intelligence_max_active: int, normal_variation_reserve_ratio: float,
    bounded_retry_reserve_ratio: float, checkpoint_recovery_reserve_seconds: float,
) -> dict[str, float]:
    """Project independent semantic work at external-intelligence capacity; question stages stay question-bound."""
    semantic = semantic_model_seconds / semantic_calls * projected_semantic_requests / external_intelligence_max_active
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
    try:
        return _run_created(suite_root, config, isolation_dir, scopes)
    finally:
        if isolation_dir.exists():
            shutil.rmtree(isolation_dir)


def _run_created(
    suite_root: Path, config: dict[str, Any], isolation_dir: Path, scopes: tuple[str, ...],
) -> dict[str, Any]:
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
        package = Path(product["package"]).resolve()
        production = Path(product["production_storage_report"]).resolve()
        _require(package.is_dir() and (package / "manifest.json").is_file(), "候选发布包或清单不存在")
        _require(production.is_file(), "生产规模存储证据不存在")
        try:
            configuration = external_intelligence_runtime.configuration_from_execution(product)
            probe_result = external_intelligence_runtime.probe(configuration)
            roles = external_intelligence_runtime.role_profile_from_execution(product)
            implementation = external_intelligence_runtime.selected_implementation(configuration.driver)
        except external_intelligence.ExternalIntelligenceError as error:
            raise PreflightError(str(error)) from error
        checks["product"] = {
            "external_intelligence": {
                "driver": configuration.driver,
                "provider": implementation["provider"],
                "artifact_sha256": binding.sha256(configuration.binary),
                "probe": probe_result,
                "roles": {name: roles[name] for name in ("semantic", "reader")},
                "credential_content_read": False,
            }
        }
    if "community" in scopes:
        community = config["community"]
        checks["community"] = _community_preflight(suite_root, config, isolation_dir)
        _require(free_bytes >= 20 * 1024**3, "社区验收隔离目录可用磁盘空间不足 20 GiB")

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
            "wall_budget_enforced": checks["community"].get("wall_budget_enforced", True),
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


def _validate_runtime_evidence(
    transport: dict[str, Any], completed_attempts: list[dict[str, Any]],
    selection: dict[str, Any], limit: int, expected_calls: int, maximum_retries: int,
) -> None:
    _require(transport.get("external_intelligence_driver") == selection["driver"], "代表校准使用了另一外部智能实现")
    if selection["worker_isolation"] == "request-local-context":
        _require(
            transport.get("server_processes") == 0 and transport.get("process_starts") == 0
            and 0 < int(transport.get("max_active", 0)) <= limit
            and transport.get("active_turns") == 0,
            "代表校准未使用有界且已收口的进程内请求",
        )
        contexts = [item.get("session_id") for item in completed_attempts]
    else:
        _require(
            0 < int(transport.get("server_processes", 0)) <= limit
            and transport.get("per_worker_max_active") == 1,
            "代表校准未使用单 turn 的有界外部智能 worker 池",
        )
        _require(
            int(transport.get("worker_restarts", maximum_retries + 1)) <= maximum_retries
            and int(transport.get("process_starts", 0)) >= int(transport["server_processes"]),
            "代表预检外部智能 worker 恢复超过有界预算",
        )
        _require(
            all(item.get("thread_ephemeral") is True and item.get("sandbox") == "read-only" for item in completed_attempts),
            "代表校准未为每个请求使用独立、只读的新外部智能会话",
        )
        contexts = [item.get("thread_id") for item in completed_attempts]
    _require(
        len(contexts) == expected_calls and all(isinstance(item, str) and item for item in contexts)
        and len(set(contexts)) == expected_calls,
        "代表校准未为每个请求使用独立上下文",
    )


def _community_preflight(suite_root: Path, config: dict[str, Any], isolation_dir: Path) -> dict[str, Any]:
    repository = suite_root.parents[2].resolve()
    adapters = binding.load_json(suite_root / "adapters.json")
    community = adapters["layers"]["community"]
    adapter = (suite_root / community["adapter"]).resolve()
    protocol_path = (suite_root / community["protocol"]).resolve()
    _require(adapter.is_file() and protocol_path.is_file(), "LongMemEval-S 适配器或协议不存在")
    revision = community["official_revision"]
    source = adapter.read_text(encoding="utf-8")
    _require(f'OFFICIAL_CODE_REVISION = "{revision}"' in source, "LongMemEval-S 校验路径未绑定固定版本")
    section = config["community"]
    try:
        external_configuration = external_intelligence_runtime.configuration_from_execution(section)
        external_intelligence_runtime.probe(external_configuration)
        external_roles = external_intelligence_runtime.role_profile_from_execution(section)
    except external_intelligence.ExternalIntelligenceError as error:
        raise PreflightError(str(error)) from error
    manifest_path = Path(section["environment_manifest"]).resolve()
    manifest = binding.load_json(manifest_path)
    layout = manifest.get("layout")
    _require(isinstance(layout, dict), "LongMemEval-S 持久环境清单不完整")
    python_root = Path(layout["python"]).resolve()
    python = python_root / ("Scripts/python.exe" if os.name == "nt" else "bin/python")
    data = Path(layout["data"]).resolve()
    runs = Path(layout["runs"]).resolve()
    representation_arguments: list[str] = []
    representation = config.get("candidate", {}).get("semantic_representation")
    if representation is not None:
        _require(isinstance(representation, dict) and isinstance(representation.get("manifest"), str), "候选语义表示声明无效")
        representation_path = Path(representation["manifest"]).resolve()
        _require(representation_path.is_file(), "候选语义表示清单不存在")
        representation_arguments = ["--semantic-representation-manifest", str(representation_path)]
    check = process_control.run(
        [str(python), str(adapter), "check", "--environment-manifest", str(manifest_path), "--protocol", str(protocol_path), *representation_arguments],
        cwd=repository, timeout=180,
    )
    _require(check.returncode == 0, f"LongMemEval-S 离线环境检查失败: {check.stderr[-2000:]}")
    protocol = binding.load_json(protocol_path)
    external_selection = external_intelligence_runtime.selected_implementation(external_configuration.driver)
    official_questions = json.loads(data.read_text(encoding="utf-8"))
    _require(isinstance(official_questions, list), "LongMemEval-S 固定数据不是问题数组")
    expected_batches = int(protocol["execution"]["calibration_semantic_batches_per_question"])
    fixture = _community_calibration_fixture(official_questions, protocol)
    fixture_path = isolation_dir / "longmemeval-s-fixture.json"
    fixture_path.write_text(json.dumps(fixture, ensure_ascii=False, indent=2), encoding="utf-8")
    product_binary_sha256 = binding.sha256(Path(config["candidate"]["binary"]).resolve())
    calibration_candidate = f"preflight-{product_binary_sha256}"
    runtime_path = adapter.with_name("external_intelligence_runtime.py")
    contract_path = adapter.parents[1] / "support" / "external_intelligence.py"
    implementation_paths = external_intelligence_runtime.implementation_files(external_configuration.driver)
    _require(runtime_path.is_file() and all(path.is_file() for path in implementation_paths) and contract_path.is_file(), "LongMemEval-S external-intelligence adapter is missing")
    selection_bytes = json.dumps(external_selection, sort_keys=True, separators=(",", ":")).encode("utf-8")
    community_tool_sha256 = hashlib.sha256(
        adapter.read_bytes() + runtime_path.read_bytes() + b"".join(path.read_bytes() for path in implementation_paths)
        + contract_path.read_bytes() + selection_bytes + protocol_path.read_bytes()
    ).hexdigest()
    if representation_arguments:
        representation_runtime = adapter.with_name("semantic_representation.py")
        community_tool_sha256 = hashlib.sha256(bytes.fromhex(community_tool_sha256) + representation_runtime.read_bytes() + Path(representation_arguments[1]).read_bytes()).hexdigest()
    semantic_contract = semantic_representation.load_contract(
        Path(representation_arguments[1]) if representation_arguments else None
    )
    dry_plan_identity = {
        "schema": "ownward.longmemeval-s-dry-plan/v1",
        "candidate": calibration_candidate,
        "binary_sha256": product_binary_sha256,
        "environment_sha256": binding.sha256(manifest_path),
        "input_manifest_sha256": binding.sha256(data),
        "dataset_sha256": binding.sha256(data),
        "semantic_dependency_sha256": binding._canonical_sha256({
            "transport_version": "ownward.longmemeval-s-semantic-transport/v2",
            "memory": protocol["memory"],
            "input_representation": semantic_contract.representation,
            "input_representation_manifest_identity": semantic_contract.manifest_identity,
            "create_context": {"key": "source", "value": "LongMemEval-S"},
        }),
    }
    dry_plan_identity["sha256"] = binding._canonical_sha256(dry_plan_identity)
    dry_plan_token = dry_plan_identity["sha256"][:16]
    semantic_token = hashlib.sha256(json.dumps({
        "dry_plan_token": dry_plan_token,
        "external_intelligence_implementation_sha256": hashlib.sha256(
            b"".join(path.read_bytes() for path in implementation_paths)
        ).hexdigest(),
        "calibration_external_intelligence_max_active": protocol["execution"]["codex_max_active"],
        "external_intelligence_roles": {
            name: external_roles[name] for name in ("semantic", "reader", "judge")
        },
    }, sort_keys=True, separators=(",", ":")).encode("utf-8")).hexdigest()[:16]
    dry_plan_output = runs / "dry-plan" / f"{product_binary_sha256[:8]}-{dry_plan_token}"
    dry_plan_reused = False
    for identity_path in sorted((runs / "dry-plan").glob("*/identity.json")) if (runs / "dry-plan").is_dir() else []:
        identity = binding.load_json(identity_path)
        candidate_output = identity_path.parent
        candidate_report = binding.load_json(candidate_output / "report.json") if (candidate_output / "report.json").is_file() else {}
        if (
            identity == dry_plan_identity
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
        *representation_arguments,
    ]
    if not dry_plan_reused:
        if dry_plan_output.exists():
            dry_plan_command.append("--resume")
        dry_plan_process = process_control.run(dry_plan_command, cwd=repository, timeout=7200)
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
    output = runs / "preflight" / f"{product_binary_sha256[:8]}-{semantic_token}-production-profile"
    command = [
        str(python), str(adapter), "run", "--non-formal", "--environment-manifest", str(manifest_path), "--protocol", str(protocol_path),
        "--dataset", str(fixture_path), "--output-dir", str(output), "--ownward-binary", str(config["candidate"]["binary"]),
        "--embedding-bundle-dir", str(config["candidate"]["embedding_bundle_dir"]), "--candidate", calibration_candidate,
        "--external-intelligence-driver", external_configuration.driver,
        "--external-intelligence-binary", str(external_configuration.binary),
        "--external-intelligence-credential-file", str(external_configuration.credential_file),
        "--external-intelligence-roles-json", json.dumps({
            name: external_roles[name] for name in ("semantic", "reader", "judge")
        }, sort_keys=True, separators=(",", ":")),
        "--environment-sha256", binding.sha256(manifest_path), "--input-manifest-sha256", binding.sha256(data),
        "--tool-sha256", community_tool_sha256,
        *representation_arguments,
    ]
    if output.exists():
        command.append("--resume")
    completed = process_control.run(command, cwd=repository, timeout=1800)
    _require(completed.returncode == 0, f"LongMemEval-S 隔离预检失败: {completed.stderr[-3000:]}")
    report_before_resume = (output / "report.json").read_bytes()
    checkpoint_before_resume = (output / "checkpoint-manifest.json").read_bytes()
    resume_command = list(command)
    if "--resume" not in resume_command:
        resume_command.append("--resume")
    resumed = process_control.run(resume_command, cwd=repository, timeout=180)
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
        and result.get("quality", {}).get("first_version_condition_satisfied") is None
        and result.get("passed") is True,
        "LongMemEval-S 隔离预检未形成有效执行检查点",
    )
    _require(result.get("profile") == protocol["acceptance"]["profile"], "LongMemEval-S 隔离预检生产口径无效")
    capabilities = result.get("capabilities")
    _require(isinstance(capabilities, dict), "LongMemEval-S 隔离预检缺少外部智能角色证据")
    _require(capabilities.get("semantic") == {
        "source": external_selection["provider"],
        "model": external_roles["semantic"]["model"],
        "reasoning_effort": external_roles["semantic"]["reasoning_effort"],
        "input_representation": capabilities.get("semantic", {}).get("input_representation"),
        "input_representation_manifest_identity": capabilities.get("semantic", {}).get("input_representation_manifest_identity"),
    }, "LongMemEval-S 隔离预检未真实调用冻结的语义角色")
    _require(
        isinstance(capabilities["semantic"]["input_representation"], str)
        and capabilities["semantic"]["input_representation"]
        and isinstance(capabilities["semantic"]["input_representation_manifest_identity"], str)
        and len(capabilities["semantic"]["input_representation_manifest_identity"]) == 64,
        "LongMemEval-S 语义输入表示身份缺失",
    )
    _require(capabilities.get("reader") == {
        "source": external_selection["provider"],
        "model": external_roles["reader"]["model"],
        "reasoning_effort": external_roles["reader"]["reasoning_effort"],
    }, "LongMemEval-S 隔离预检未真实调用冻结的 Reader 角色")
    _require(capabilities.get("judge") == {
        "source": external_selection["provider"],
        "model": external_roles["judge"]["model"],
        "reasoning_effort": external_roles["judge"]["reasoning_effort"],
    }, "LongMemEval-S 隔离预检未真实调用冻结的 Judge 角色")
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
    _require(len(analysis_units) == semantic_calls, "LongMemEval-S 分析单元与外部智能调用量不一致")
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
    external_intelligence_limit = int(protocol["execution"]["codex_max_active"])
    question_count = int(protocol["official"]["question_count"])
    projected_semantic_requests = int(dry_plan["semantic_work_batches"])
    projection = _community_cost_projection(
        semantic_model_seconds=semantic_model_seconds, semantic_calls=semantic_calls,
        reader_model_seconds=reader_model_seconds, judge_model_seconds=judge_model_seconds,
        calibration_questions=len(questions), per_question_host_seconds=per_question_host,
        projected_semantic_requests=projected_semantic_requests, question_count=question_count,
        question_workers=workers, external_intelligence_max_active=external_intelligence_limit,
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
    external_intelligence = result["cost"]["codex"]
    expected_external_intelligence_calls = semantic_calls + 2 * len(questions)
    attempt_metadata = [
        binding.load_json(path) for path in output.glob("questions/**/codex/attempt-*/metadata.json")
    ]
    completed_attempts = [item for item in attempt_metadata if item.get("outcome") == "complete"]
    maximum_bounded_retries = max(1, math.floor(expected_external_intelligence_calls * 0.1))
    _require(int(external_intelligence["calls"]) == expected_external_intelligence_calls, "LongMemEval-S 外部智能调用量不完整")
    _require(int(external_intelligence["attempts"]) == expected_external_intelligence_calls + int(external_intelligence["retries"]), "LongMemEval-S 外部智能尝试计数不一致")
    _require(int(external_intelligence["retries"]) <= maximum_bounded_retries, f"并发 {external_intelligence_limit} 的代表预检超过有界重试预算")
    _require(
        int(external_intelligence["scheduler"]["limit"]) == external_intelligence_limit
        and 0 < int(external_intelligence["scheduler"]["max_active"]) <= external_intelligence_limit,
        "代表校准没有遵守全局外部智能并发上限",
    )
    _validate_runtime_evidence(
        external_intelligence.get("transport", {}), completed_attempts, external_selection,
        external_intelligence_limit, expected_external_intelligence_calls, maximum_bounded_retries,
    )
    _require(int(result["cost"]["semantic_submitted_batches"]) == semantic_batches, "代表校准未提交全部语义批次")
    _require(
        semantic_calls * 2 <= legacy_semantic_calls
        and projected_semantic_requests * 2 <= int(dry_plan["legacy_semantic_analysis_calls"]),
        "LongMemEval-S 去重合并后的语义调用量未显著收敛",
    )
    if protocol["execution"].get("full_wall_policy", "enforce") == "enforce":
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
            "legacy_process_starts": legacy_semantic_calls + 2 * len(questions),
            "worker_process_starts": external_intelligence["transport"]["process_starts"],
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
        "calibration_accuracy": result["accuracy"],
        "fixture_wall_seconds": result["cost"]["wall_seconds"], "projected_full_wall_seconds": projected,
        "required_ceiling_wall_seconds": required_ceiling,
        "wall_budget_enforced": protocol["execution"].get("full_wall_policy", "enforce") == "enforce",
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
        "external_intelligence_concurrency": {
            **external_intelligence["scheduler"], "frozen": external_intelligence_limit,
            "selection_candidates": [8, 12],
            "selection_policy": "lowest stable pool whose required ceiling is at most the frozen full-run wall budget",
            "selected": external_intelligence_limit,
            "higher_pool_not_required": external_intelligence_limit == 8 and required_ceiling <= float(protocol["execution"]["full_wall_seconds"]),
        },
        "external_intelligence_calls": {
            **{name: external_intelligence[name] for name in ("calls", "attempts", "retries", "rate_limit_events", "interrupted_attempts")},
            "maximum_bounded_retries": maximum_bounded_retries,
        },
        "external_intelligence_tokens": {
            name: result["cost"][name]
            for name in (
                "semantic_input_tokens", "semantic_output_tokens", "reader_input_tokens", "reader_output_tokens",
                "judge_input_tokens", "judge_output_tokens",
            )
        },
        "semantic_plan_equivalent": True, "semantic_submission_complete": True, "byte_exact_resume": True,
        "external_intelligence": {
            "driver": external_configuration.driver,
            "provider": external_selection["provider"],
            "transport": external_selection["transport"],
            "probe": external_intelligence_runtime.probe(external_configuration),
            "roles": {name: external_roles[name] for name in ("semantic", "reader", "judge")},
        },
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
