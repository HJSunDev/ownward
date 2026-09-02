from __future__ import annotations

import json
import math
import inspect
from pathlib import Path
from typing import Any

import kernel_iteration_answer_sufficiency as answer_sufficiency
import kernel_iteration_evidence as evidence
import kernel_iteration_validation as validation


CONTRACT_SCHEMA = "ownward.kernel-iteration-stage3-reader-reliability-contract/v1"
PLAN_SCHEMA = "ownward.kernel-iteration-stage3-reader-reliability-plan/v1"
RESULT_SCHEMA = "ownward.kernel-iteration-stage3-reader-reliability-result/v1"
CONTRACT_RELATIVE = Path("iteration/v2/stage3-reader-reliability-contract.json")
SELECTION_RELATIVE = Path("iteration/v2/stage3-reader-reliability-selection.json")
FORMAL_COST_MIGRATION_RELATIVE = Path("iteration/v2/stage6-formal-reader-cost-migration.json")
ACTIVE_RETRIEVAL_COST_MIGRATION_RELATIVE = Path("iteration/evaluation-correction/stage6-active-retrieval-cost-migration.json")


class ReaderReliabilityError(ValueError):
    pass


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise ReaderReliabilityError(message)


def _load_json(path: Path) -> dict[str, Any]:
    value = json.loads(path.read_text(encoding="utf-8"))
    _require(isinstance(value, dict), f"JSON 对象无效: {path}")
    return value


def _mapping(value: dict[str, Any], key: str) -> dict[str, Any]:
    item = value.get(key)
    _require(isinstance(item, dict), f"{key} 必须是对象")
    return item


def load_contract(suite_root: Path) -> dict[str, Any]:
    suite_root = suite_root.resolve()
    path = suite_root / CONTRACT_RELATIVE
    value = _load_json(path)
    _require(value.get("schema") == CONTRACT_SCHEMA, "Reader 可靠性合同 schema 无效")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), "Reader 可靠性合同身份漂移")
    _require(value.get("frozen_before_new_reader_results") is True, "Reader 可靠性合同没有在新结果前冻结")
    _require(value.get("formal") is False and value.get("blind_gate_execution") is False, "Reader 可靠性合同越权")
    selection = _mapping(value, "reader_selection")
    _require(selection.get("ordered_efforts") == ["medium", "high", "xhigh", "max"], "Reader effort 顺序漂移")
    _require(selection.get("mechanical_correctness_required") == 1.0, "Reader 机械正确率门槛漂移")
    _require(selection.get("semantic_or_wording_variation_is_not_failure") is True, "Reader 语义措辞变化被错误当成失败")
    attribution = _mapping(value, "stage6_failure_attribution")
    _require(attribution.get("first_answer_is_immutable_gate_decision") is True, "Stage 6 首答决定不可变边界漂移")
    _require(attribution.get("diagnostic_repetitions_can_never_convert_a_failed_first_answer_to_pass") is True, "诊断重复可错误洗白首答")
    return value


def load_selection(suite_root: Path, *, require_result: bool = True) -> dict[str, Any]:
    suite_root = suite_root.resolve()
    value = _load_json(suite_root / SELECTION_RELATIVE)
    _require(value.get("schema") == "ownward.kernel-iteration-stage3-reader-reliability-selection/v1", "Reader 选择 schema 无效")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), "Reader 选择身份漂移")
    contract = load_contract(suite_root)
    _require(value.get("contract_identity") == contract["identity"], "Reader 选择合同错绑")
    _require(value.get("selected_reasoning_effort") == "xhigh", "Reader 最低稳定 effort 漂移")
    _require(value.get("selected_reader_profile_identity") == "401aa7962b5ecd3d283093a2d5eee0fe76da941d20ce4aa317ef21216d55c83c", "Reader profile 身份漂移")
    migration = _mapping(value, "controller_identity_migration")
    _require(
        migration.get("source_plan_controller_identity") == "a05326f0e09817981227007f2b3e9de3aaa234c23b29ea64751214c7f630955b"
        and migration.get("target_controller_identity") == _controller_identity()
        and migration.get("source_answer_diagnostic_identity") == "a0fec1e8525a3c7450ab2b855124a72d3229abe65a89763a59e4240f156cf3c2"
        and migration.get("target_answer_diagnostic_identity") == evidence.file_sha256(Path(answer_sufficiency.__file__).resolve())
        and migration.get("calibration_result_rewritten") is False
        and migration.get("model_or_product_execution") is False,
        "Reader 可靠性控制器身份迁移无效",
    )
    protocol_path = suite_root / str(value["protocol_relative_path"])
    _require(protocol_path.is_file(), "正式与 Stage 6 共用 Reader 协议缺失")
    protocol = _load_json(protocol_path)
    reader = _mapping(protocol, "reader")
    _require(
        protocol.get("acceptance", {}).get("profile") == "Ownward LongMemEval-S Production Profile"
        and reader.get("model") == value.get("model")
        and reader.get("reasoning_effort") == value.get("selected_reasoning_effort")
        and reader.get("selection_profile_identity") == value.get("selected_reader_profile_identity")
        and reader.get("selection_contract_identity") == value.get("contract_identity")
        and reader.get("selection_result_identity") == value.get("result_identity"),
        "正式与 Stage 6 Reader 身份没有统一绑定冻结选择",
    )
    cost = load_formal_cost_migration(suite_root, require_source_artifacts=require_result)
    _require(value.get("formal_cost_migration_identity") == cost["identity"], "正式 Reader 成本迁移证明错绑")
    if evidence.file_sha256(protocol_path) != value["protocol_sha256"]:
        active_cost = load_active_retrieval_cost_migration(suite_root, cost)
        _require(active_cost["reader_selection_identity"] == value["identity"], "主动检索成本迁移错绑 Reader 选择")
    _require(value.get("formal_handoff_status") == "community-binding-and-preflight-pending-rebuild", "community 重绑/预检状态错误")
    if require_result:
        result_path = suite_root.parents[2] / str(value["result_relative_path"])
        _require(result_path.is_file() and evidence.file_sha256(result_path) == value["result_sha256"], "Reader 选择原始结果缺失或漂移")
        result = _load_json(result_path)
        _require(result.get("identity") == value["result_identity"] and result.get("passed") is True, "Reader 选择原始结果错绑")
    return value


def load_formal_cost_migration(
    suite_root: Path,
    *,
    require_source_artifacts: bool = False,
) -> dict[str, Any]:
    suite_root = suite_root.resolve()
    value = _load_json(suite_root / FORMAL_COST_MIGRATION_RELATIVE)
    _require(value.get("schema") == "ownward.kernel-iteration-stage6-formal-reader-cost-migration/v1", "正式 Reader 成本迁移 schema 无效")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), "正式 Reader 成本迁移身份漂移")
    target = _mapping(value, "target_formal_protocol")
    target_path = suite_root.parents[2] / str(target["path"])
    _require(target_path.is_file(), "正式 xhigh 协议缺失")
    source = _mapping(value, "source_formal_projection")
    reader = _mapping(value, "reader_selection")
    local = _mapping(value, "candidate_local_critical_path")
    reserves = _mapping(value, "reserves")
    migrated = _mapping(value, "migrated_projection")
    groups = math.ceil(int(reader["formal_reader_requests"]) / int(reader["formal_question_workers"]))
    _require(groups == reader["parallel_groups"], "正式 Reader 并发组数错误")
    added = float(reader["p95_wall_seconds"]) * groups
    _require(math.isclose(added, float(reader["entire_p95_treated_as_incremental_seconds"]), abs_tol=1e-9), "xhigh Reader 全额新增开销计算错误")
    saving = (
        float(local["v0_controlled_baseline_seconds"])
        - float(local["v2_candidate_plus_repeatability_error_seconds"])
    ) * int(local["formal_question_groups"])
    _require(math.isclose(saving, float(local["conservative_projected_saving_seconds"]), abs_tol=1e-9), "V2 本地关键路径节省计算错误")
    projected = float(source["projected_wall_seconds"]) - saving + added
    normal = projected * float(reserves["normal_variation_ratio"])
    retry = float(source["bounded_retry_reserve_seconds"]) + added * float(reserves["bounded_retry_ratio"])
    required = projected + normal + retry + float(reserves["checkpoint_recovery_seconds"])
    _require(math.isclose(projected, float(migrated["projected_wall_seconds"]), abs_tol=1e-9), "xhigh 正式原始投影错误")
    _require(math.isclose(normal, float(migrated["normal_variation_reserve_seconds"]), abs_tol=1e-9), "xhigh 正式波动余量错误")
    _require(math.isclose(retry, float(migrated["bounded_retry_reserve_seconds"]), abs_tol=1e-9), "xhigh 正式重试余量错误")
    _require(math.isclose(required, float(migrated["required_ceiling_wall_seconds"]), abs_tol=1e-9), "xhigh 正式完整余量上界错误")
    _require(required <= float(migrated["hard_ceiling_wall_seconds"]) and migrated.get("passed") is True, "xhigh Reader 超过正式 20400 秒硬上限")
    policy = _mapping(value, "policy")
    _require(
        policy.get("time_ceiling_relaxed") is False
        and policy.get("reader_quality_relaxed") is False
        and policy.get("model_or_product_execution") is False
        and policy.get("formal_state_written") is False
        and policy.get("community_binding_and_preflight_status") == "pending-rebuild-before-formal-handoff"
        and policy.get("this_receipt_is_not_a_formal_preflight") is True,
        "正式 Reader 成本迁移越过质量、状态或 preflight 边界",
    )
    if evidence.file_sha256(target_path) != target["sha256"]:
        load_active_retrieval_cost_migration(suite_root, value)
    if require_source_artifacts:
        repo_root = suite_root.parents[2]
        reader_result = repo_root / ".tmp/kernel-v2-major-iteration/stage3-reader-reliability/reader-reliability/268576e324bed9a36dd442f9dcda519d96177c285bc99a9a373b7b00d2e8063e/result.json"
        local_contract = suite_root / "iteration/v2/stage4-resource-cost-representation-final-contract.json"
        local_result = repo_root / ".tmp/kernel-v2-major-iteration/stage4-end-to-end-resource-cost/representation-lifecycle-final-v6/result.json"
        _require(evidence.file_sha256(reader_result) == reader["result_sha256"], "xhigh Reader 原始成本证据漂移")
        _require(evidence.file_sha256(local_contract) == local["contract_sha256"], "V2 本地关键路径合同漂移")
        _require(evidence.file_sha256(local_result) == local["result_sha256"], "V2 本地关键路径结果漂移")
    return value


def load_active_retrieval_cost_migration(suite_root: Path, source_cost: dict[str, Any]) -> dict[str, Any]:
    suite_root = suite_root.resolve()
    value = _load_json(suite_root / ACTIVE_RETRIEVAL_COST_MIGRATION_RELATIVE)
    _require(value.get("schema") == "ownward.product-faithful-active-retrieval-cost-migration/v1", "主动检索成本迁移 schema 无效")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), "主动检索成本迁移身份漂移")
    _require(value.get("source_reader_cost_migration_identity") == source_cost.get("identity"), "主动检索成本迁移源错绑")
    target = _mapping(value, "target_protocol")
    target_path = suite_root.parents[2] / str(target["path"])
    _require(target_path.is_file() and evidence.file_sha256(target_path) == target["sha256"], "主动检索正式协议与成本迁移错绑")
    protocol = _load_json(target_path)
    retrieval = _mapping(protocol, "retrieval")
    reader = _mapping(protocol, "reader")
    _require(
        retrieval.get("mode") == target.get("retrieval_mode")
        and retrieval.get("allowed_tools") == target.get("required_tools")
        and f'{reader.get("model")}/{reader.get("reasoning_effort")}' == target.get("reader"),
        "主动检索正式协议表面漂移",
    )
    calibration = _mapping(value, "calibration")
    comparison_path = suite_root.parents[2] / str(calibration["comparison_evidence_path"])
    _require(
        comparison_path.is_file()
        and evidence.file_sha256(comparison_path) == calibration["comparison_evidence_sha256"],
        "主动检索成本校准证据漂移",
    )
    charged = float(calibration["charged_retrieval_seconds_per_question"]) * int(calibration["formal_questions"])
    _require(
        float(calibration["charged_retrieval_seconds_per_question"]) >= 2 * float(calibration["observed_retrieval_p95_seconds"])
        and int(calibration["parallelism_credit"]) == 0
        and math.isclose(charged, float(calibration["conservative_incremental_seconds"]), abs_tol=1e-9),
        "主动检索成本没有按无并发折扣的保守边界计入",
    )
    source = _mapping(source_cost, "migrated_projection")
    reserves = _mapping(value, "reserves")
    migrated = _mapping(value, "migrated_projection")
    projected = float(source["projected_wall_seconds"]) + charged
    normal = float(source["normal_variation_reserve_seconds"]) + charged * float(reserves["normal_variation_ratio"])
    retry = float(source["bounded_retry_reserve_seconds"]) + charged * float(reserves["bounded_retry_ratio"])
    required = projected + normal + retry + float(reserves["checkpoint_recovery_seconds"])
    _require(math.isclose(projected, float(migrated["projected_wall_seconds"]), abs_tol=1e-9), "主动检索正式原始投影错误")
    _require(math.isclose(normal, float(migrated["normal_variation_reserve_seconds"]), abs_tol=1e-9), "主动检索正式波动余量错误")
    _require(math.isclose(retry, float(migrated["bounded_retry_reserve_seconds"]), abs_tol=1e-9), "主动检索正式重试余量错误")
    _require(math.isclose(required, float(migrated["required_ceiling_wall_seconds"]), abs_tol=1e-9), "主动检索正式完整余量错误")
    _require(required <= float(migrated["hard_ceiling_wall_seconds"]) and migrated.get("passed") is True, "主动检索超过正式 20400 秒硬上限")
    policy = _mapping(value, "policy")
    _require(
        policy.get("reader_quality_relaxed") is False
        and policy.get("time_ceiling_relaxed") is False
        and policy.get("active_retrieval_fully_charged_without_parallelism_credit") is True
        and policy.get("model_or_product_execution") is False
        and policy.get("formal_state_written") is False
        and policy.get("formal_preflight_status") == "pending",
        "主动检索成本迁移越过质量、执行或正式状态边界",
    )
    return value


def run(
    suite_root: Path,
    output_root: Path,
    execution_config: Path,
    source_result_path: Path,
    source_run_root: Path,
    formal_state_path: Path,
    *,
    resume: bool = False,
) -> dict[str, Any]:
    suite_root = suite_root.resolve()
    output_root = output_root.resolve()
    evidence._validate_output_boundary(suite_root.parents[2], output_root)
    contract = load_contract(suite_root)
    runtime = validation.validate_execution_config(suite_root, execution_config.resolve())
    source_path = source_result_path.resolve()
    source = _load_json(source_path)
    source_contract = _mapping(_mapping(contract, "sources"), "medium_diagnosis")
    _require(source.get("plan_identity") == source_contract["plan_identity"], "Reader medium 诊断 plan 错绑")
    _require(source.get("identity") == source_contract["result_identity"], "Reader medium 诊断结果错绑")
    _require(evidence.file_sha256(source_path) == source_contract["sha256"], "Reader medium 诊断文件漂移")
    diagnosis = _mapping(source, "codex_boundary_diagnosis")
    _require(_mapping(diagnosis, "reader").get("oracle_context_failures") == 2, "Reader medium 已知机械失败事实漂移")
    _require(_mapping(diagnosis, "judge").get("controls_passed") is True, "既有 Terra Judge 控制未通过")
    run_root = source_run_root.resolve()
    _require((run_root / "report.json").is_file(), "Reader 可靠性校准缺少冻结候选运行轨迹")
    formal_state = formal_state_path.resolve()
    _require(formal_state.is_file(), "Reader 可靠性校准缺少正式 state 只读基线")
    state_before = formal_state.read_bytes()
    materials_ref = _mapping(_mapping(contract, "sources"), "materials")
    materials_path = suite_root / str(materials_ref["path"])
    _require(evidence.file_sha256(materials_path) == materials_ref["sha256"], "Reader 可靠性材料漂移")
    materials = validation.validate_stage3_materials(_load_json(materials_path))
    _require(materials["identity"] == materials_ref["identity"], "Reader 可靠性材料身份错绑")
    if resume:
        selection = load_selection(suite_root)
        if (
            selection["contract_identity"] == contract["identity"]
            and selection["result_identity"] == _load_json(
                suite_root.parents[2] / str(selection["result_relative_path"])
            )["identity"]
        ):
            selected_result_path = suite_root.parents[2] / str(selection["result_relative_path"])
            result = _load_json(selected_result_path)
            _validate_result(result, selection["plan_identity"])
            _require(formal_state.read_bytes() == state_before, "Reader 可靠性迁移终态复用改写正式 state")
            return {
                **result,
                "result_path": str(selected_result_path),
                "reused": True,
                "identity_migrated": True,
                "model_calls": 0,
                "product_executions": 0,
            }

    dependencies = {
        "contract": contract["identity"],
        "controller": _controller_identity(),
        "answer-diagnostic": evidence.file_sha256(Path(answer_sufficiency.__file__).resolve()),
        "execution-config": evidence.file_sha256(execution_config.resolve()),
        "source-result": evidence.file_sha256(source_path),
        "source-report": evidence.file_sha256(run_root / "report.json"),
        "source-checkpoint": evidence.file_sha256(run_root / "checkpoint-manifest.json"),
        "materials": materials["identity"],
        "formal-state": evidence.file_sha256(formal_state),
    }
    plan_content = {
        "schema": PLAN_SCHEMA,
        "purpose": "select-lowest-stable-reader-effort-before-stage6",
        "contract_identity": contract["identity"],
        "source_result_identity": source["identity"],
        "direct_dependencies": dict(sorted(dependencies.items())),
        "formal": False,
        "candidate_decision": None,
        "blind_gate_execution": False,
    }
    plan = {**plan_content, "identity": evidence.canonical_sha256(plan_content)}
    root = output_root / "reader-reliability" / plan["identity"]
    plan_path = root / "plan.json"
    result_path = root / "result.json"
    if result_path.is_file():
        _require(resume and plan_path.is_file() and _load_json(plan_path) == plan, "Reader 可靠性终态只能按同一身份恢复")
        result = _load_json(result_path)
        _validate_result(result, plan["identity"])
        _require(formal_state.read_bytes() == state_before, "Reader 可靠性终态复用改写正式 state")
        return {**result, "result_path": str(result_path), "reused": True, "model_calls": 0, "product_executions": 0}
    if plan_path.is_file():
        _require(resume and _load_json(plan_path) == plan, "Reader 可靠性计划身份漂移")
    else:
        evidence.atomic_json(plan_path, plan)

    source_reader = _mapping(diagnosis, "reader")
    tried: list[dict[str, Any]] = [{
        "effort": "medium",
        "source": "frozen-independent-diagnosis",
        "product_context_mechanical_failures": int(source_reader["product_context_failures"]),
        "oracle_context_mechanical_failures": int(source_reader["oracle_context_failures"]),
        "passed": False,
    }]
    selected: dict[str, Any] | None = None
    base_settings = dict(_mapping(runtime["protocol_value"], "reader"))
    for effort in _mapping(contract, "reader_selection")["ordered_efforts"][1:]:
        settings = {**base_settings, "reasoning_effort": effort}
        result = answer_sufficiency._diagnose_codex_boundaries(
            suite_root,
            root / "codex",
            runtime,
            materials,
            run_root,
            reader_settings=settings,
            include_original_product_answer=False,
            product_repeats=(1, 2, 3),
            oracle_repeats=(1, 2, 3),
            settings_label=effort,
            run_judge=False,
        )
        reader = _mapping(result, "reader")
        item = {
            "effort": effort,
            "source": "fresh-reader-only-calibration",
            "product_context_mechanical_failures": int(reader["product_context_failures"]),
            "oracle_context_mechanical_failures": int(reader["oracle_context_failures"]),
            "product_context_variations": int(reader["product_context_variations"]),
            "oracle_context_variations": int(reader["oracle_context_variations"]),
            "cost": reader["cost"],
            "transport": result["transport"],
            "passed": int(reader["product_context_failures"]) == 0 and int(reader["oracle_context_failures"]) == 0,
        }
        tried.append(item)
        if item["passed"]:
            selected = item
            break
    _require(selected is not None, "没有 Reader effort 达到 100% 机械正确率门槛")
    budget = _budget_proof(contract, float(_mapping(selected, "cost")["p95_wall_seconds"]))
    _require(all(item["passed"] for item in budget["levels"].values()), "选定 Reader effort 无法容纳于原始 Stage 6 预算")
    profile_content = {
        "schema": "ownward.kernel-iteration-stage6-reader-profile/v1",
        "model": "gpt-5.6-luna",
        "reasoning_effort": selected["effort"],
        "reader_settings": {**base_settings, "reasoning_effort": selected["effort"]},
        "selection_contract_identity": contract["identity"],
        "calibration_plan_identity": plan["identity"],
        "judge": {"model": "gpt-5.6-terra", "reasoning_effort": "medium", "controls_source_result_identity": source["identity"]},
        "first_answer_accuracy_required": 1.0,
    }
    profile = {**profile_content, "identity": evidence.canonical_sha256(profile_content)}
    result_content = {
        "schema": RESULT_SCHEMA,
        "plan_identity": plan["identity"],
        "contract_identity": contract["identity"],
        "status": "passed",
        "passed": True,
        "formal": False,
        "formal_state_written": False,
        "blind_gate_executed": False,
        "candidate_decision": None,
        "selected_reader_profile": profile,
        "efforts": tried,
        "judge_controls": {
            "source_result_identity": source["identity"],
            "model": "gpt-5.6-terra",
            "reasoning_effort": "medium",
            "correct": "5/5",
            "wrong": "5/5",
            "rerun": False,
        },
        "budget_proof": budget,
        "semantic_or_wording_variation_is_failure": False,
        "raw_answers_persisted": False,
        "formal_state_sha256": evidence.file_sha256(formal_state),
        "next_action": "bind-reader-profile-and-answer-attribution-before-fresh-stage6-five",
    }
    terminal = {**result_content, "identity": evidence.canonical_sha256(result_content)}
    evidence.atomic_json(result_path, terminal)
    _require(formal_state.read_bytes() == state_before, "Reader 可靠性校准改写正式 state")
    _validate_result(terminal, plan["identity"])
    return {**terminal, "result_path": str(result_path), "reused": False, "model_calls": sum(int(_mapping(item, "cost").get("measured_calls", 0)) for item in tried if "cost" in item), "product_executions": 0}


def _budget_proof(contract: dict[str, Any], selected_reader_p95: float) -> dict[str, Any]:
    budget = _mapping(contract, "budget")
    linear = _mapping(budget, "historical_medium_wall_linear_projection")
    fixed = float(linear["fixed_seconds"])
    per_question = float(linear["per_question_seconds"])
    repeat_error = float(budget["repeatability_error_seconds"])
    calls_per_question = int(budget["reader_calls_per_question"])
    parallelism = int(budget["reader_parallelism"])
    levels: dict[str, Any] = {}
    for level_text, hard in _mapping(budget, "level_hard_seconds").items():
        level = int(level_text)
        historical = fixed + per_question * level
        # Deliberately charge the entire selected Reader p95 as new overhead.
        # This is stricter than subtracting medium and proves the unchanged
        # budgets without depending on an optimistic baseline measurement.
        groups = math.ceil(calls_per_question * level / parallelism)
        projected = historical + selected_reader_p95 * groups + repeat_error
        levels[level_text] = {
            "historical_medium_projection_seconds": historical,
            "reader_parallel_groups": groups,
            "conservative_selected_reader_overhead_seconds": selected_reader_p95 * groups,
            "repeatability_error_seconds": repeat_error,
            "projected_plus_error_seconds": projected,
            "hard_seconds": int(hard),
            "margin_seconds": int(hard) - projected,
            "passed": projected <= int(hard),
        }
    return {
        "method": "historical-medium-linear-plus-entire-selected-reader-p95-as-incremental-overhead",
        "selected_reader_p95_seconds": selected_reader_p95,
        "quality_or_time_gate_relaxed": False,
        "levels": levels,
    }


def _validate_result(result: dict[str, Any], plan_identity: str) -> None:
    _require(result.get("schema") == RESULT_SCHEMA and result.get("plan_identity") == plan_identity, "Reader 可靠性结果错绑")
    content = {key: item for key, item in result.items() if key != "identity"}
    _require(result.get("identity") == evidence.canonical_sha256(content), "Reader 可靠性结果身份漂移")
    _require(result.get("passed") is True and result.get("formal_state_written") is False and result.get("blind_gate_executed") is False, "Reader 可靠性结果边界无效")
    _require(result.get("raw_answers_persisted") is False, "Reader 可靠性结果持久化了原始答案")


def _controller_identity() -> str:
    return evidence.canonical_sha256({
        "run": inspect.getsource(run),
        "budget": inspect.getsource(_budget_proof),
        "result-validation": inspect.getsource(_validate_result),
        "answer-diagnosis": evidence.file_sha256(Path(answer_sufficiency.__file__).resolve()),
    })
