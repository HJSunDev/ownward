from __future__ import annotations

import hashlib
import importlib.util
import inspect
import json
import math
import os
from pathlib import Path
import secrets
import shutil
import subprocess
import sys
import time
from typing import Any, Callable

import kernel_iteration_evidence as evidence


class KernelIterationValidationError(ValueError):
    pass


VALIDATION_CONTRACT_SCHEMA = "ownward.kernel-iteration-validation/v1"
MATERIALS_SCHEMA = "ownward.kernel-iteration-materials/v1"
EXECUTION_INPUT_SCHEMA = evidence.INPUT_SCHEMA
STAGE3_MATERIALS_SCHEMA = "ownward.kernel-iteration-materials/v2"
EXECUTION_RESULT_SCHEMA = "ownward.kernel-iteration-execution-evidence/v3"
STAGE3_CONTRACT_SCHEMA = "ownward.kernel-iteration-stage3-contract/v1"
STAGE3_PLAN_SCHEMA = "ownward.kernel-iteration-stage3-plan/v1"
STAGE3_RESULT_SCHEMA = "ownward.kernel-iteration-stage3-evidence/v1"
BLIND_PLAN_SCHEMA = "ownward.kernel-iteration-blind-plan/v2"
BLIND_RESULT_SCHEMA = "ownward.kernel-iteration-blind-calibration/v2"
BLIND_RECOVERY_SCHEMA = "ownward.kernel-iteration-blind-recovery/v1"
BLIND_SECRET_SCHEMA = "ownward.kernel-iteration-blind-recovery-secret/v1"
BLIND_DEPENDENCY_LOCATOR_SCHEMA = "ownward.kernel-iteration-blind-dependency-locator/v1"
VALIDATION_CONTRACT_RELATIVE = Path("iteration/v2/validation-contract.json")
BLIND_BUDGET_RELATIVE = Path("iteration/v2/blind-calibration-budget.json")
STAGE3_CONTRACT_RELATIVE = Path("iteration/v2/stage3-contract.json")
SUPPORTED_EXECUTION_TYPES = {"development", "regression", "integrated"}
BLIND_COVERAGE = (
    "knowledge-update-conflict",
    "temporal-order",
    "multi-session-relation",
    "single-session-assistant-fact",
    "multi-session-distractor",
)
FACT_DELIVERY_MISSING_GAPS = (
    "source_session_not_created",
    "semantic_work_not_fully_submitted",
    "target_evidence_not_search_returned",
    "target_evidence_not_read",
)
ANSWER_ONLY_GAPS = (
    "evidence_read_answer_incorrect",
    "abstention_response_incorrect",
    "answer_incorrect_without_labeled_evidence",
)
FORMAL_KEYS = ("question", "answer", "answer_session_ids", "haystack_sessions")


def load_validation_contract(suite_root: Path) -> dict[str, Any]:
    path = suite_root.resolve() / VALIDATION_CONTRACT_RELATIVE
    value = _load_json(path)
    _require(value.get("schema") == VALIDATION_CONTRACT_SCHEMA, "V2 验证合同 schema 无效")
    _require(value.get("frozen_before_calibration") is True, "V2 验证标准未在五题生成前冻结")
    _require(value.get("contains_formal_questions_answers_gold_or_content") is False, "验证合同不得包含正式题面或答案")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), "V2 验证合同身份漂移")
    execution = _mapping(value, "execution")
    _require(set(execution.get("supported_evidence_types", [])) == SUPPORTED_EXECUTION_TYPES, "通用执行证据类型不完整")
    _require(execution.get("formal_state_read_only") is True, "非正式执行必须只读正式 state")
    _require(execution.get("pair_order") == ["candidate", "v0"] and execution.get("candidate_role") == "v2-candidate" and execution.get("baseline_role") == "evaluation-baseline", "同尺执行角色或顺序合同漂移")
    _require(execution.get("candidate_must_pass_absolute_gate_before_v0") is True and execution.get("baseline_execution_requires_candidate_result") is True, "V0 执行缺少候选绝对门前置合同")
    _require(tuple(execution.get("fact_delivery_missing_gaps", [])) == FACT_DELIVERY_MISSING_GAPS, "事实交付缺口分类漂移")
    _require(tuple(execution.get("answer_only_gaps", [])) == ANSWER_ONLY_GAPS, "答案错误缺口分类漂移")
    blind = _mapping(value, "blind")
    _require(tuple(blind.get("coverage", [])) == BLIND_COVERAGE, "盲测五题覆盖配额漂移")
    _require(blind.get("levels") == [5, 15, 25, 50] and blind.get("calibration_questions") == 5, "盲测级别或校准题量漂移")
    _require(blind.get("calibration_is_candidate_decision") is False, "五题校准不得参与候选判断")
    _require(blind.get("resume_addressing") == "explicit-plan-identity" and blind.get("running_secret_location") == "temporary-scratch-only", "五题跨上下文恢复合同漂移")
    _require(blind.get("terminal_reuse") == "plan-identity-read-only-zero-execution" and blind.get("controller_direct_dependency_required") is True, "五题终态复用或控制器依赖合同漂移")
    _require(blind.get("terminal_raw_policy") == "destroy-all-reversible-content", "盲测原始数据销毁合同无效")
    calibration = _mapping(value, "calibration")
    _require(calibration.get("repetitions") == 2, "五题校准必须包含两次独立执行以测量重复误差")
    _require(calibration.get("question_concurrency") == 4 and calibration.get("codex_concurrency") == 8, "校准并发合同漂移")
    _require(calibration.get("design_total_normal_seconds") == 9000, "盲测总设计上限不得超过 150 分钟")
    return value


def load_stage3_contract(suite_root: Path) -> dict[str, Any]:
    """Load the aggregate-only stage-3 policy and independently authored materials."""
    suite_root = suite_root.resolve()
    path = suite_root / STAGE3_CONTRACT_RELATIVE
    value = _load_json(path)
    _require(value.get("schema") == STAGE3_CONTRACT_SCHEMA, "V2 阶段 3 合同 schema 无效")
    _require(value.get("frozen_before_diagnostic_results") is True, "阶段 3 门槛未在诊断结果前冻结")
    _require(value.get("contains_formal_questions_answers_gold_content_outputs_or_case_ids") is False, "阶段 3 合同不得包含正式逐题内容")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), "V2 阶段 3 合同身份漂移")
    sources = _mapping(value, "sources")
    loaded: dict[str, dict[str, Any]] = {}
    for name in ("aggregate-problem-pool", "development-materials", "regression-materials"):
        source = _mapping(sources, name)
        relative = Path(str(source.get("path", "")))
        _require(not relative.is_absolute() and ".." not in relative.parts, f"阶段 3 {name} 路径越界")
        source_path = (suite_root / relative).resolve()
        _require(source_path.is_relative_to(suite_root) and source_path.is_file(), f"阶段 3 {name} 缺失")
        _require(source.get("sha256") == evidence.file_sha256(source_path), f"阶段 3 {name} 文件摘要漂移")
        loaded[name] = _load_json(source_path)
    pool = validate_stage3_problem_pool(loaded["aggregate-problem-pool"])
    development = validate_stage3_materials(loaded["development-materials"])
    regression = validate_stage3_materials(loaded["regression-materials"])
    _require(development["schema"] == regression["schema"] == STAGE3_MATERIALS_SCHEMA, "阶段 3 必须使用可组合真值的独立材料 schema")
    for name, materials in (("development", development), ("regression", regression)):
        provenance = _mapping(_mapping(materials, "criteria"), "provenance")
        _require(provenance.get("origin") == "independent-stage3-handcrafted" and provenance.get("stage2_calibration_reuse") is False, f"{name} 材料来源边界无效")
        _require(provenance.get("formal_source_access") is False, f"{name} 材料不得接触正式逐题来源")
    development_facts = {_case_fact_identity(case) for case in development["cases"]}
    regression_facts = {_case_fact_identity(case) for case in regression["cases"]}
    _require(development_facts.isdisjoint(regression_facts), "阶段 3 开发与回归材料存在事实重合")
    coverage = _mapping(value, "coverage")
    _require(set(coverage.get("development_required", [])) <= {case["coverage"] for case in development["cases"]}, "阶段 3 开发材料覆盖不完整")
    _require(set(coverage.get("regression_required", [])) <= {case["coverage"] for case in regression["cases"]}, "阶段 3 回归材料覆盖不完整")
    thresholds = _mapping(value, "thresholds")
    _require(0 < int(thresholds.get("daily_feedback_hard_seconds", 0)) <= 600, "阶段 3 日常反馈超过 10 分钟")
    _require(int(thresholds.get("root_minimum_cases", 0)) >= 1, "阶段 3 根因复现门槛无效")
    _require(float(thresholds.get("v0_regression_minimum_accuracy", 0)) == 1.0, "V0 正确能力保护必须全部通过")
    benefits = _mapping(value, "v1_benefit_reproof")
    _require(set(benefits) == {"long-asset-semantic-recall", "fine-grained-evidence", "batch-durability", "storage-convergence"}, "V1 待重证收益不完整")
    _require(pool["aggregate_sources"] == value["aggregate_sources"], "问题池与阶段 3 聚合来源错绑")
    current_artifact = _mapping(value, "current_product_artifact")
    _require(evidence.is_sha256(current_artifact.get("binary_sha256")) and current_artifact.get("role") == "current-product-not-baseline" and current_artifact.get("candidate_decision") is None, "阶段 3 当前产品制品边界无效")
    return {**value, "loaded": {"problem_pool": pool, "development": development, "regression": regression}}


def validate_stage3_problem_pool(value: dict[str, Any]) -> dict[str, Any]:
    _require(value.get("schema") == "ownward.kernel-iteration-problem-pool/v1", "阶段 3 问题池 schema 无效")
    _require(value.get("aggregate_only") is True and value.get("contains_formal_question_content_or_case_ids") is False, "阶段 3 问题池越过聚合边界")
    items = value.get("items")
    _require(isinstance(items, list) and len(items) >= 8, "阶段 3 问题池覆盖不足")
    required = {
        "evidence-not-recalled", "evidence-not-loaded", "fragment-incompleteness", "temporal-conflict",
        "final-answer-sufficiency", "previously-correct-regression", "retrieval-latency", "end-to-end-resource-cost",
    }
    names = {item.get("name") for item in items if isinstance(item, dict)}
    _require(required <= names, "阶段 3 问题池缺少必要维度")
    for item in items:
        _require(isinstance(item, dict), "阶段 3 问题池条目无效")
        _require(item.get("observation_kind") in {"first-observed-gap", "aggregate-observation"}, "阶段 3 聚合现象类型无效")
        _require(item.get("root_status") in {"unproven", "proven-by-independent-evidence"}, "阶段 3 根因状态无效")
        _require(isinstance(item.get("mechanism_hypotheses"), list) and item["mechanism_hypotheses"], "阶段 3 条目缺少待证明机制")
        _require(isinstance(item.get("required_independent_evidence"), list) and item["required_independent_evidence"], "阶段 3 条目缺少独立证据要求")
        _require(isinstance(item.get("next_validation"), str) and item["next_validation"], "阶段 3 条目缺少唯一下一验证点")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), "阶段 3 问题池身份漂移")
    return value


def load_blind_budget_archive(suite_root: Path) -> dict[str, Any]:
    validation = load_validation_contract(suite_root)
    value = _load_json(suite_root.resolve() / BLIND_BUDGET_RELATIVE)
    _require(value.get("schema") == "ownward.kernel-iteration-blind-budget-freeze/v1", "V2 盲测预算 schema 无效")
    _require(value.get("validation_contract_identity") == validation["identity"], "V2 盲测预算与验证合同错绑")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), "V2 盲测预算身份漂移")
    calibration = _mapping(value, "calibration")
    _require(calibration.get("candidate_decision") is None and calibration.get("formal") is False and calibration.get("formal_state_written") is False, "五题校准不得形成候选或正式判定")
    _require(calibration.get("raw_materials_destroyed") is True and calibration.get("quality_admission_passed") is True and calibration.get("control_discrimination_passed") is True, "五题校准质量、对照或销毁证明不完整")
    _require(calibration.get("resume_report_byte_identical") is True and calibration.get("resume_checkpoint_byte_identical") is True, "五题校准缺少逐字恢复证明")
    calibration_dependencies = _mapping(calibration, "plan_direct_dependencies")
    _require(calibration_dependencies and all(evidence.is_sha256(item) for item in calibration_dependencies.values()), "五题预算冻结的计划直接依赖无效")
    _require(evidence.is_sha256(calibration.get("current_verifier_identity")), "五题预算冻结缺少当前有效性校验器身份")
    _require(calibration.get("plan_schema") == BLIND_PLAN_SCHEMA and calibration.get("result_schema") == BLIND_RESULT_SCHEMA, "五题预算冻结的计划或结果合同漂移")
    levels = _mapping(value, "budgets")
    _require(set(levels) == {"5", "15", "25", "50"}, "五题校准没有冻结四级预算")
    _require(all(int(item["normal_seconds"]) > 0 and int(item["failure_seconds"]) > 0 for item in levels.values()), "五题校准预算无效")
    _require(int(value["total_normal_seconds"]) == sum(int(item["normal_seconds"]) for item in levels.values()), "四级预算总量不闭合")
    _require(int(value["total_normal_seconds"]) <= int(value["design_total_normal_seconds"]) == 9000, "四级实测预算超过冻结设计上限")
    return value


def load_blind_budget(suite_root: Path, output_root: Path) -> dict[str, Any]:
    """Load a historical budget only after all current direct dependencies still match."""
    value = load_blind_budget_archive(suite_root)
    calibration = _mapping(value, "calibration")
    plan_identity = str(calibration["plan_identity"])
    root = output_root.resolve() / "blind-calibration" / plan_identity
    plan = _load_json(root / "plan.json")
    _validate_blind_plan(plan, plan_identity)
    result = _load_json(root / "result.json")
    _validate_blind_terminal_result(result, plan_identity)
    _require(result.get("identity") == calibration.get("result_identity"), "五题预算冻结的终态结果身份漂移")
    locator = _load_blind_dependency_locator(root / "dependency-locator.json", plan_identity)
    _require(locator["current_verifier_identity"] == calibration.get("current_verifier_identity"), "五题预算冻结的当前校验器身份错绑")
    _validate_current_blind_dependencies(suite_root, plan, locator)
    return {**value, "current_valid": True}


def validate_materials(value: dict[str, Any], *, expected_questions: int | None = None) -> dict[str, Any]:
    _require(value.get("schema") == MATERIALS_SCHEMA, "非正式材料 schema 无效")
    _require(value.get("contains_formal_questions_answers_gold_or_content") is False, "非正式材料不得来自正式 LongMemEval-S")
    cases = value.get("cases")
    _require(isinstance(cases, list) and cases, "非正式材料缺少案例")
    if expected_questions is not None:
        _require(len(cases) == expected_questions, "非正式材料题量与冻结配额不一致")
    identifiers: set[str] = set()
    normalized: list[dict[str, Any]] = []
    for index, case in enumerate(cases):
        _require(isinstance(case, dict), f"非正式案例 {index} 不是对象")
        identifier = case.get("case_id")
        _require(isinstance(identifier, str) and identifier and identifier not in identifiers, "非正式案例身份无效或重复")
        identifiers.add(identifier)
        coverage = case.get("coverage")
        _require(isinstance(coverage, str) and coverage, f"{identifier} 缺少覆盖类别")
        question = case.get("question")
        answer = case.get("answer")
        _require(isinstance(question, str) and question.strip() and isinstance(answer, str) and answer.strip(), f"{identifier} 问题或答案无效")
        _require(_normalize(answer) not in _normalize(question), f"{identifier} 问题表面泄漏答案")
        sessions = case.get("sessions")
        _require(isinstance(sessions, list) and len(sessions) >= 3, f"{identifier} 会话不足")
        session_ids: list[str] = []
        session_by_id: dict[str, dict[str, Any]] = {}
        for session in sessions:
            _require(isinstance(session, dict), f"{identifier} 会话无效")
            session_id = session.get("session_id")
            _require(isinstance(session_id, str) and session_id and session_id not in session_by_id, f"{identifier} 会话身份无效")
            date = session.get("date")
            turns = session.get("turns")
            _require(isinstance(date, str) and len(date) == 10 and isinstance(turns, list) and turns, f"{identifier} 会话日期或正文无效")
            for turn in turns:
                _require(
                    isinstance(turn, dict)
                    and turn.get("role") in {"user", "assistant"}
                    and isinstance(turn.get("content"), str)
                    and turn["content"].strip(),
                    f"{identifier} 会话轮次无效",
                )
            session_ids.append(session_id)
            session_by_id[session_id] = session
        answer_ids = case.get("answer_session_ids")
        stale_ids = case.get("stale_session_ids")
        distractor_ids = case.get("distractor_session_ids")
        _require(isinstance(answer_ids, list) and answer_ids and set(answer_ids) <= set(session_ids), f"{identifier} 答案证据身份无效")
        _require(isinstance(stale_ids, list) and set(stale_ids) <= set(session_ids), f"{identifier} 旧证据身份无效")
        _require(isinstance(distractor_ids, list) and distractor_ids and set(distractor_ids) <= set(session_ids), f"{identifier} 干扰证据身份无效")
        _require(not set(answer_ids) & set(distractor_ids), f"{identifier} 答案与干扰证据重叠")
        if coverage in {"temporal-order", "multi-session-relation", "multi-session-distractor"}:
            _require(len(set(answer_ids)) >= 2, f"{identifier} 多会话覆盖缺少两份必要答案证据")
        if coverage == "single-session-assistant-fact":
            _require(
                any(turn["role"] == "assistant" for session_id in answer_ids for turn in session_by_id[session_id]["turns"]),
                f"{identifier} assistant 事实没有绑定 assistant 原话",
            )
        claims = case.get("truth_claims")
        _require(isinstance(claims, list) and claims, f"{identifier} 缺少机械真值")
        for claim in claims:
            _require(isinstance(claim, dict) and isinstance(claim.get("claim"), str) and claim["claim"].strip(), f"{identifier} 真值项无效")
            claim_ids = claim.get("evidence_session_ids")
            _require(isinstance(claim_ids, list) and claim_ids and set(claim_ids) <= set(answer_ids), f"{identifier} 真值证据绑定无效")
            combined = " ".join(
                turn["content"]
                for session_id in claim_ids
                for turn in session_by_id[session_id]["turns"]
            )
            _require(_normalize(claim["claim"]) in _normalize(combined), f"{identifier} 真值项没有绑定到自身逐字证据")
        answer_text = " ".join(
            turn["content"]
            for session_id in answer_ids
            for turn in session_by_id[session_id]["turns"]
        )
        _require(_normalize(answer) in _normalize(answer_text), f"{identifier} 答案没有机械可核对证据")
        if coverage == "knowledge-update-conflict":
            _require(stale_ids, f"{identifier} 更新冲突案例缺少旧证据")
            newest_stale = max(session_by_id[item]["date"] for item in stale_ids)
            newest_answer = max(session_by_id[item]["date"] for item in answer_ids)
            _require(newest_stale < newest_answer, f"{identifier} 更新冲突时间顺序无效")
        normalized.append(_case_projection(case))
    content = {
        "schema": MATERIALS_SCHEMA,
        "contains_formal_questions_answers_gold_or_content": False,
        "cases": normalized,
        "criteria": value.get("criteria", {}),
    }
    _require(isinstance(content["criteria"], dict), "非正式材料判定合同无效")
    _require(value.get("identity") == evidence.canonical_sha256(content), "非正式材料身份漂移")
    return {**content, "identity": value["identity"]}


def validate_stage3_materials(value: dict[str, Any], *, expected_questions: int | None = None) -> dict[str, Any]:
    """Validate composed-answer stage-3 materials without changing the blind-gate schema contract."""
    _require(value.get("schema") == STAGE3_MATERIALS_SCHEMA, "阶段 3 材料 schema 无效")
    cases = value.get("cases")
    _require(isinstance(cases, list) and cases, "阶段 3 材料缺少案例")
    for case in cases:
        _require(isinstance(case, dict), "阶段 3 案例无效")
        _require(isinstance(case.get("answer"), str) and case["answer"].strip(), "阶段 3 案例答案无效")
        _require(_normalize(case["answer"]) not in _normalize(str(case.get("question", ""))), "阶段 3 问题表面泄漏答案")
    compatibility = json.loads(json.dumps(value))
    compatibility["schema"] = MATERIALS_SCHEMA
    for case in compatibility["cases"]:
        claims = case.get("truth_claims")
        _require(isinstance(claims, list) and claims, "阶段 3 案例缺少组合真值")
        case["answer"] = claims[0]["claim"]
    compatibility_content = {key: item for key, item in compatibility.items() if key != "identity"}
    compatibility["identity"] = evidence.canonical_sha256(compatibility_content)
    validate_materials(compatibility, expected_questions=expected_questions)
    content = {
        "schema": STAGE3_MATERIALS_SCHEMA,
        "contains_formal_questions_answers_gold_or_content": False,
        "cases": [_case_projection(case) for case in cases],
        "criteria": value.get("criteria", {}),
    }
    _require(isinstance(content["criteria"], dict), "阶段 3 材料判定合同无效")
    _require(value.get("identity") == evidence.canonical_sha256(content), "阶段 3 材料身份漂移")
    return {**content, "identity": value["identity"]}


def build_input_manifest(
    suite_root: Path,
    materials_path: Path,
    execution_config_path: Path,
    evidence_type: str,
    destination: Path,
) -> dict[str, Any]:
    _require(evidence_type in SUPPORTED_EXECUTION_TYPES, "该证据类型不使用端到端执行材料")
    comparison = evidence.load_contract(suite_root)
    validation = load_validation_contract(suite_root)
    materials_value = _load_json(materials_path.resolve())
    materials = validate_stage3_materials(materials_value) if materials_value.get("schema") == STAGE3_MATERIALS_SCHEMA else validate_materials(materials_value)
    runtime = validate_execution_config(suite_root, execution_config_path.resolve())
    identities = execution_identities(suite_root, validation, materials, runtime)
    required = set(_mapping(_mapping(comparison, "evidence_types"), evidence_type)["required_dependencies"])
    direct = {
        f"{evidence_type}-materials" if evidence_type != "integrated" else "integrated-plan": materials["identity"],
        "executor": identities["executor"],
        "observer": identities["observer"],
    }
    _require(set(direct) == required, "比较合同与执行输入的直接依赖角色不一致")
    shared = {
        "dataset": materials["identity"],
        "environment": identities["environment"],
        "executor": identities["executor"],
        "model-profile": identities["model-profile"],
        "observer": identities["observer"],
        "prompt-and-schema": identities["prompt-and-schema"],
        "scorer": identities["scorer"],
    }
    payload_name = materials_path.name
    content = {
        "schema": EXECUTION_INPUT_SCHEMA,
        "evidence_type": evidence_type,
        "shared_conditions": dict(sorted(shared.items())),
        "direct_dependencies": dict(sorted(direct.items())),
        "runtime_dependencies": {},
        "payloads": [{"path": payload_name, "sha256": evidence.file_sha256(materials_path.resolve())}],
    }
    manifest = {**content, "identity": evidence.canonical_sha256(content)}
    destination = destination.resolve()
    _require(destination.parent == materials_path.resolve().parent, "输入清单必须与其不可变材料位于同一目录")
    evidence.atomic_json(destination, manifest)
    return manifest


def validate_execution_config(suite_root: Path, path: Path) -> dict[str, Any]:
    value = _load_json(path)
    _require(value.get("schema") == "ownward.acceptance-execution/v3", "执行配置 schema 无效")
    candidate = _mapping(value, "candidate")
    community = _mapping(value, "community")
    binary = Path(str(candidate.get("binary", ""))).resolve()
    embedding = Path(str(candidate.get("embedding_bundle_dir", ""))).resolve()
    environment_manifest = Path(str(community.get("environment_manifest", ""))).resolve()
    protocol = Path(str(community.get("protocol", ""))).resolve()
    codex_binary = Path(str(community.get("codex_binary", ""))).resolve()
    codex_auth_file = Path(str(community.get("codex_auth_file", ""))).resolve()
    _require(binary.is_file(), "非正式执行缺少产品二进制")
    _require(embedding.is_dir() and (embedding / "manifest.json").is_file(), "非正式执行缺少向量制品")
    _require(environment_manifest.is_file() and protocol.is_file(), "非正式执行缺少持久环境或协议")
    _require(codex_binary.is_file() and codex_auth_file.is_file(), "Codex 原生能力不可用")
    protocol_value = _load_json(protocol)
    validation = load_validation_contract(suite_root)
    blind = _mapping(validation, "blind")
    expected_roles = _mapping(blind, "production_roles")
    _require(
        f"{protocol_value['memory']['semantic_model']}/{protocol_value['memory']['semantic_reasoning_effort']}" == expected_roles["semantic"]
        and f"{protocol_value['reader']['model']}/{protocol_value['reader']['reasoning_effort']}" == expected_roles["reader"]
        and f"{protocol_value['judge']['model']}/{protocol_value['judge']['reasoning_effort']}" == expected_roles["judge"],
        "执行配置没有使用冻结的 Luna/Luna/Terra 角色",
    )
    _require(protocol_value.get("execution", {}).get("codex_max_active") == 8, "Codex 并发不是冻结值 8")
    manifest = _load_json(environment_manifest)
    runs = Path(str(_mapping(manifest, "layout").get("runs", ""))).resolve()
    _require(runs.is_dir(), "LongMemEval-S 持久运行根缺失")
    return {
        "path": path,
        "value": value,
        "binary": binary,
        "embedding": embedding,
        "environment_manifest": environment_manifest,
        "environment": manifest,
        "protocol": protocol,
        "protocol_value": protocol_value,
        "codex_binary": codex_binary,
        "codex_auth_file": codex_auth_file,
        "runs": runs,
    }


def execution_identities(
    suite_root: Path,
    validation: dict[str, Any],
    materials: dict[str, Any],
    runtime: dict[str, Any],
) -> dict[str, str]:
    repository = suite_root.resolve().parents[2]
    long_root = repository / "benchmarks" / "longmemeval_s"
    environment = runtime["environment"]
    evaluator = Path(str(_mapping(environment, "layout")["source"])) / "src" / "evaluation" / "evaluate_qa.py"
    implementation = {
        name: evidence.file_sha256(long_root / name)
        for name in ("run.py", "codex_app_server.py", "protocol.json")
    }
    implementation["ownward-mcp-transport"] = evidence.file_sha256(repository / "benchmarks" / "support" / "ownward_mcp.py")
    implementation["iteration-validation"] = evidence.file_sha256(Path(__file__).resolve())
    implementation["iteration-longmemeval"] = evidence.file_sha256(Path(__file__).with_name("kernel_iteration_longmemeval.py"))
    protocol = runtime["protocol_value"]
    return {
        "dataset": materials["identity"],
        "environment": evidence.file_sha256(runtime["environment_manifest"]),
        "executor": evidence.canonical_sha256({"engine": "longmemeval-s-nonformal/v1", "implementation": implementation}),
        "model-profile": evidence.canonical_sha256({
            "semantic": protocol["memory"]["semantic_model"] + "/" + protocol["memory"]["semantic_reasoning_effort"],
            "reader": protocol["reader"]["model"] + "/" + protocol["reader"]["reasoning_effort"],
            "judge": protocol["judge"]["model"] + "/" + protocol["judge"]["reasoning_effort"],
        }),
        "observer": evidence.canonical_sha256({"schema": EXECUTION_RESULT_SCHEMA, "validation-contract": validation["identity"], "implementation": implementation["iteration-validation"]}),
        "prompt-and-schema": evidence.canonical_sha256({
            "semantic": protocol["memory"], "reader": protocol["reader"], "judge": protocol["judge"],
            "retrieval": protocol["retrieval"], "materials-schema": materials["schema"],
        }),
        "scorer": evidence.canonical_sha256({"official-evaluator": evidence.file_sha256(evaluator), "observer-schema": EXECUTION_RESULT_SCHEMA}),
    }


def execute_prepared_evidence(
    suite_root: Path,
    output_root: Path,
    execution_config_path: Path,
    *,
    selector: str | None = None,
    subject_manifest: Path | None = None,
    evidence_type: str,
    input_manifest: Path,
    candidate_result_path: Path | None = None,
    noncandidate_diagnostic: bool = False,
    resume: bool = False,
    runner: Callable[..., dict[str, Any]] | None = None,
) -> dict[str, Any]:
    _require(evidence_type in SUPPORTED_EXECUTION_TYPES, "该证据类型没有端到端执行合同")
    prepared = evidence.run(
        suite_root,
        output_root,
        selector=selector,
        subject_manifest=subject_manifest,
        evidence_type=evidence_type,
        input_manifest=input_manifest,
        resume=resume,
    )
    evidence_root = Path(prepared["evidence_root"])
    completed_path = evidence_root / "execution-result.json"
    if completed_path.is_file():
        _require(resume, "端到端结果已存在；只有 --resume 可精确复用")
        result = _load_execution_result(completed_path)
        prepared_plan = _load_json(evidence_root / "plan.json")
        _require(result.get("plan_identity") == prepared["plan_identity"] and result.get("subject_identity") == prepared["subject_identity"], "端到端结果计划或 subject 身份漂移")
        _require(result.get("subject_role") == _mapping(prepared_plan, "subject").get("role"), "端到端结果 subject 角色漂移")
        expected_purpose = "stage3-current-product-diagnostic" if noncandidate_diagnostic else "candidate-evaluation"
        if result["subject_role"] == "evaluation-baseline":
            expected_purpose = "stage3-current-product-diagnostic" if result.get("comparison_purpose") == "stage3-current-product-diagnostic" else "candidate-evaluation"
        _require(result.get("comparison_purpose") == expected_purpose, "端到端恢复目的与已封存结果不一致")
        receipt_content = {
            "schema": "ownward.kernel-iteration-execution-resume/v1",
            "plan_identity": prepared["plan_identity"],
            "execution_result_sha256": evidence.file_sha256(completed_path),
            "reused_execution": True,
            "model_or_product_execution": False,
        }
        evidence.atomic_json(evidence_root / "execution-resume.json", {**receipt_content, "identity": evidence.canonical_sha256(receipt_content)})
        return {**prepared, "status": result["status"], "passed": result["passed"], "execution_result": str(completed_path), "reused_execution": True}
    runtime = validate_execution_config(suite_root, execution_config_path.resolve())
    validation = load_validation_contract(suite_root)
    manifest = _load_json(input_manifest.resolve())
    payloads = manifest.get("payloads")
    _require(isinstance(payloads, list) and len(payloads) == 1, "端到端输入必须且只能封存一份材料")
    materials_path = (input_manifest.resolve().parent / payloads[0]["path"]).resolve()
    materials_value = _load_json(materials_path)
    materials = validate_stage3_materials(materials_value) if materials_value.get("schema") == STAGE3_MATERIALS_SCHEMA else validate_materials(materials_value)
    identities = execution_identities(suite_root, validation, materials, runtime)
    _verify_execution_manifest_dependencies(evidence.load_contract(suite_root), manifest, evidence_type, materials, identities)
    subject = evidence.select_subject(evidence.load_contract(suite_root), selector, subject_manifest)
    _require(subject["role"] in {"v2-candidate", "evaluation-baseline", "current-product-not-baseline"}, "端到端同尺执行 subject 角色无效")
    _require(subject["role"] != "current-product-not-baseline" or noncandidate_diagnostic, "当前产品只能用于明确的非候选诊断")
    _require(subject["role"] != "v2-candidate" or not noncandidate_diagnostic, "V2 候选不得伪装为非候选诊断")
    candidate_result = None
    if subject["role"] == "evaluation-baseline":
        _require(candidate_result_path is not None, "V0 只能在前置同尺 subject 执行后运行")
        candidate_result = _load_execution_result(candidate_result_path.resolve())
        if noncandidate_diagnostic:
            _require(candidate_result["subject_role"] == "current-product-not-baseline", "V0 诊断前置结果不是当前产品")
            _require(candidate_result["comparison_purpose"] == "stage3-current-product-diagnostic" and candidate_result["candidate_decision"] is None, "当前产品诊断不得形成候选决定")
        else:
            _require(candidate_result["subject_role"] == "v2-candidate", "V0 前置结果不是 V2 候选")
            _require(candidate_result["passed"] is True and candidate_result["candidate_decision"] is True, "V2 候选未通过冻结绝对门，禁止消耗 V0 执行")
        _require(candidate_result["evidence_type"] == evidence_type, "V0 与候选证据类型不同")
        _require(candidate_result["input_identity"] == manifest.get("identity"), "V0 与候选完整输入身份不同")
        _require(candidate_result["shared_conditions"] == dict(sorted(_mapping(manifest, "shared_conditions").items())), "V0 与候选共享评测条件不同")
        _require(candidate_result["direct_dependencies"] == dict(sorted(_mapping(manifest, "direct_dependencies").items())), "V0 与候选材料、执行器或观察器不同")
    else:
        _require(candidate_result_path is None, "前置 subject 执行不得伪装成基线后置阶段")
    if subject["role"] == "current-product-not-baseline":
        _verify_current_product_binary(suite_root, runtime["binary"])
    else:
        _verify_subject_binary(subject, runtime["binary"])
    state_path = suite_root.resolve().parents[2] / evidence.FORMAL_STATE_RELATIVE
    state_before = state_path.read_bytes() if state_path.is_file() else None
    dataset = [_longmemeval_case(case) for case in materials["cases"]]
    scratch = runtime["runs"] / "kernel-iteration" / prepared["plan_identity"]
    dataset_path = scratch / "materials.json"
    evidence.atomic_json(dataset_path, dataset)
    report = (runner or _run_longmemeval)(
        suite_root=suite_root,
        runtime=runtime,
        dataset_path=dataset_path,
        output_dir=scratch / "run",
        subject_identity=subject["identity"],
        resume=resume,
    )
    summary_path = scratch / "run" / "diagnostic-summary.json"
    if not isinstance(report.get("diagnostic_summary"), dict) and summary_path.is_file():
        report = {**report, "diagnostic_summary": _load_json(summary_path)}
    observation = observe_report(report, materials)
    if materials["schema"] == STAGE3_MATERIALS_SCHEMA:
        observation["case_evidence"] = observe_case_evidence(scratch / "run", materials)
    criteria = materials.get("criteria", {})
    passed, feedback = evaluate_observation(observation, criteria)
    content = {
        "schema": EXECUTION_RESULT_SCHEMA,
        "plan_identity": prepared["plan_identity"],
        "subject_identity": subject["identity"],
        "subject_role": subject["role"],
        "subject_name": subject["name"],
        "comparison_purpose": "stage3-current-product-diagnostic" if noncandidate_diagnostic else "candidate-evaluation",
        "evidence_type": evidence_type,
        "status": "passed" if passed else "failed",
        "passed": passed,
        "candidate_decision": passed if subject["role"] == "v2-candidate" else None,
        "formal": False,
        "formal_state_written": False,
        "observation": observation,
        "failure_feedback": feedback,
        "input_identity": manifest["identity"],
        "shared_conditions": dict(sorted(_mapping(manifest, "shared_conditions").items())),
        "direct_dependencies": dict(sorted(_mapping(manifest, "direct_dependencies").items())),
    }
    result = {**content, "identity": evidence.canonical_sha256(content)}
    evidence.atomic_json(completed_path, result)
    if state_before is not None:
        _require(state_path.read_bytes() == state_before, "非正式执行改写了正式 Acceptance state")
    return {**prepared, "status": result["status"], "passed": passed, "execution_result": str(completed_path), "reused_execution": False}


def observe_report(report: dict[str, Any], materials: dict[str, Any]) -> dict[str, Any]:
    execution = _mapping(report, "execution")
    _require(execution.get("complete") is True and execution.get("protocol_valid") is True and execution.get("evidence_complete") is True, "端到端执行或诊断证据不完整")
    _require(report.get("questions") == len(materials["cases"]), "端到端报告题量不完整")
    categories = report.get("categories")
    _require(isinstance(categories, dict), "端到端报告缺少分类结果")
    diagnostics = report.get("diagnostics")
    _require(isinstance(diagnostics, dict) and diagnostics.get("post_answer_only") is True and diagnostics.get("excluded_from_product_execution_and_scoring") is True, "诊断没有与产品执行及评分隔离")
    cost = _mapping(report, "cost")
    retrieval = _mapping(report, "retrieval")
    diagnostic_summary = report.get("diagnostic_summary") if isinstance(report.get("diagnostic_summary"), dict) else {}
    first_gaps = diagnostic_summary.get("by_first_observed_gap")
    _require(isinstance(first_gaps, dict) and all(isinstance(name, str) and isinstance(count, int) and count >= 0 for name, count in first_gaps.items()), "诊断缺少逐题首个观测缺口汇总")
    _require(set(first_gaps) <= {"none", *FACT_DELIVERY_MISSING_GAPS, *ANSWER_ONLY_GAPS}, "诊断含有未冻结的首个观测缺口")
    _require(sum(first_gaps.values()) == report["questions"], "诊断首个观测缺口题量不闭合")
    delivery_missing = sum(first_gaps.get(name, 0) for name in FACT_DELIVERY_MISSING_GAPS)
    return {
        "questions": report["questions"],
        "fact_delivery": {
            "complete": diagnostics.get("questions") == report["questions"] and delivery_missing == 0,
            "missing_questions": delivery_missing,
            "by_first_observed_gap": dict(sorted(first_gaps.items())),
        },
        "final_answer_accuracy": report.get("accuracy"),
        "temporal_correctness": _category_accuracy(categories, "temporal-reasoning"),
        "conflict_correctness": _category_accuracy(categories, "knowledge-update"),
        "latency": {"retrieval_mean_ms": retrieval.get("mean_ms"), "retrieval_p95_ms": retrieval.get("p95_ms"), "wall_seconds": cost.get("wall_seconds")},
        "resources": {
            "semantic_input_tokens": cost.get("semantic_input_tokens"),
            "reader_input_tokens": cost.get("reader_input_tokens"),
            "judge_input_tokens": cost.get("judge_input_tokens"),
            "ownward_data_bytes": cost.get("ownward_data_bytes"),
        },
        "codex": cost.get("codex"),
    }


def observe_case_evidence(run_root: Path, materials: dict[str, Any]) -> list[dict[str, Any]]:
    """Reduce independent synthetic traces to mechanistic facts without persisting raw content."""
    observations: list[dict[str, Any]] = []
    for case in materials["cases"]:
        case_id = str(case["case_id"])
        root = run_root.resolve() / "questions" / case_id
        diagnostic_path = root / "diagnostic.json"
        retrieval_path = root / "retrieval.json"
        diagnostic = _load_json(diagnostic_path)
        retrieval_checkpoint = _load_json(retrieval_path)
        artifacts = _mapping(diagnostic, "artifacts")
        _require(_mapping(artifacts, "retrieval").get("sha256") == evidence.file_sha256(retrieval_path), f"{case_id} 检索证据摘要漂移")
        coverage = _mapping(diagnostic, "evidence_coverage")
        expected_sessions = [str(item) for item in coverage.get("expected_session_ids", [])]
        expected_assets = [str(item) for item in coverage.get("expected_asset_ids", [])]
        _require(len(expected_sessions) == len(expected_assets), f"{case_id} 答案会话与资产身份未闭合")
        source_to_asset = dict(zip(expected_sessions, expected_assets))
        returned = set(str(item) for item in coverage.get("search_returned_expected", []))
        read = set(str(item) for item in coverage.get("read_expected", []))
        expected = set(expected_assets)
        retrieved = _mapping(retrieval_checkpoint, "retrieval")
        selection = _observe_selection_trace(case, source_to_asset, expected, retrieved, retrieval_checkpoint.get("evidence", []))
        delivered: dict[str, list[str]] = {}
        for item in retrieval_checkpoint.get("evidence", []):
            if not isinstance(item, dict) or not isinstance(item.get("content"), str):
                continue
            source_id = str(item.get("id", ""))
            delivered.setdefault(source_id, []).append(item["content"])
        claim_results: list[dict[str, Any]] = []
        for claim in case["truth_claims"]:
            bound_assets = {source_to_asset[item] for item in claim["evidence_session_ids"] if item in source_to_asset}
            supplied = " ".join(content for asset in bound_assets for content in delivered.get(asset, []))
            claim_results.append({
                "claim_identity": evidence.canonical_sha256({"claim": _normalize(claim["claim"]), "bound_assets": sorted(bound_assets)}),
                "delivered": _normalize(claim["claim"]) in _normalize(supplied),
            })
        created = bool(_mapping(diagnostic, "execution_observations").get("source_creation_complete"))
        semantic = bool(_mapping(diagnostic, "execution_observations").get("semantic_submission_complete"))
        correct = bool(diagnostic.get("correct"))
        first_gap = str(diagnostic.get("first_observed_gap"))
        all_returned = bool(expected) and expected <= returned
        all_read = bool(expected) and expected <= read
        claims_complete = bool(claim_results) and all(item["delivered"] for item in claim_results)
        if not created:
            mechanism = "source-creation-incomplete"
        elif not semantic:
            mechanism = "semantic-submission-incomplete"
        elif not all_returned:
            mechanism = "search-return-gap"
        elif not all_read:
            mechanism = selection["first_proven_mechanism"]
        elif not claims_complete and not correct:
            mechanism = "fragment-incomplete-after-read"
        elif claims_complete and not correct:
            mechanism = "reader-answer-failure-after-complete-evidence"
        else:
            mechanism = "none"
        observations.append({
            "case_identity": _case_fact_identity(case),
            "coverage": case["coverage"],
            "correct": correct,
            "first_observed_gap": first_gap,
            "first_proven_mechanism": mechanism,
            "expected_sources": len(expected),
            "search_returned_sources": len(expected & returned),
            "read_sources": len(expected & read),
            "truth_claims": len(claim_results),
            "delivered_truth_claims": sum(int(item["delivered"]) for item in claim_results),
            "context_chars": int(retrieved.get("context_chars", 0)),
            "selection": selection,
            "retrieval_sha256": evidence.file_sha256(retrieval_path),
            "diagnostic_sha256": evidence.file_sha256(diagnostic_path),
        })
    _require(len(observations) == len(materials["cases"]), "阶段 3 逐案例机制证据不完整")
    return observations


def _observe_selection_trace(
    case: dict[str, Any],
    source_to_asset: dict[str, str],
    expected_assets: set[str],
    retrieved: dict[str, Any],
    delivered_evidence: Any,
) -> dict[str, Any]:
    """Describe the first post-search delivery deviation without feeding truth into execution."""
    returned_values = retrieved.get("returned", [])
    _require(isinstance(returned_values, list), f"{case['case_id']} 检索返回轨迹无效")
    returned: dict[str, dict[str, Any]] = {}
    for rank, item in enumerate(returned_values):
        _require(isinstance(item, dict) and isinstance(item.get("id"), str), f"{case['case_id']} 检索返回项无效")
        signals = item.get("signals", [])
        _require(isinstance(signals, list) and all(isinstance(signal, str) for signal in signals), f"{case['case_id']} 检索信号无效")
        returned[item["id"]] = {"rank": rank, "signals": sorted(signals)}
    steps = retrieved.get("selection_steps", [])
    _require(isinstance(steps, list) and all(isinstance(step, dict) for step in steps), f"{case['case_id']} 读取选择轨迹无效")
    limits = _mapping(retrieved, "limits")
    read_limit = int(limits.get("read_units", 0))
    context_limit = int(limits.get("context_chars", 0))
    depth_limit = int(limits.get("evidence_depth_per_source", 0))
    _require(read_limit > 0 and context_limit > 0 and depth_limit > 0, f"{case['case_id']} 读取预算轨迹无效")
    selected = [step for step in steps if step.get("selected") is True]
    selected_sources: list[str] = []
    for step in selected:
        source_id = str(step.get("source_id", ""))
        if source_id and source_id not in selected_sources:
            selected_sources.append(source_id)
    selected_depth_units = sum(int(step.get("depth", 0)) > 0 for step in selected)
    delivered_units = len(delivered_evidence) if isinstance(delivered_evidence, list) else 0
    _require(delivered_units == len(selected), f"{case['case_id']} 已选读取与交付单元数量不一致")
    source_traces: list[dict[str, Any]] = []
    missing_returned_ranks: list[int] = []
    context_rejected = False
    unreadable_rejected = False
    case_identity = _case_fact_identity(case)
    for session_id, asset_id in source_to_asset.items():
        source_steps = [step for step in steps if step.get("source_id") == asset_id]
        selected_depths = sorted(int(step.get("depth", 0)) for step in source_steps if step.get("selected") is True)
        reasons = sorted({str(step.get("reason")) for step in source_steps if step.get("selected") is False and isinstance(step.get("reason"), str)})
        context_rejected = context_rejected or "context_budget" in reasons
        unreadable_rejected = unreadable_rejected or "unreadable" in reasons
        returned_item = returned.get(asset_id)
        if returned_item is not None and not selected_depths:
            missing_returned_ranks.append(int(returned_item["rank"]))
        source_traces.append({
            "source_identity": evidence.canonical_sha256({"case": case_identity, "session": session_id}),
            "returned": returned_item is not None,
            "fusion_rank": int(returned_item["rank"]) if returned_item is not None else None,
            "channel_signals": returned_item["signals"] if returned_item is not None else [],
            "read": bool(selected_depths),
            "selected_depths": selected_depths,
            "rejection_reasons": reasons,
        })
    if context_rejected:
        mechanism = "context-budget-fit-gap"
    elif unreadable_rejected:
        mechanism = "unreadable-evidence-gap"
    elif missing_returned_ranks and len(selected) >= read_limit:
        if min(missing_returned_ranks) >= read_limit:
            mechanism = "target-ranked-below-read-budget"
        elif selected_depth_units > 0:
            mechanism = "source-depth-consumed-read-budget-before-target-rank"
        else:
            mechanism = "source-order-consumed-read-budget-before-target-rank"
    else:
        mechanism = "read-selection-gap"
    return {
        "first_proven_mechanism": mechanism,
        "limits": {"read_units": read_limit, "context_chars": context_limit, "evidence_depth_per_source": depth_limit},
        "returned_sources": len(returned),
        "selected_units": len(selected),
        "selected_sources": len(selected_sources),
        "selected_depth_units": selected_depth_units,
        "selected_nonrequired_sources": len(set(selected_sources) - expected_assets),
        "selected_source_ranks": [int(step.get("source_rank", -1)) for step in selected],
        "selected_depths": [int(step.get("depth", 0)) for step in selected],
        "context_chars": int(retrieved.get("context_chars", 0)),
        "expected_sources": sorted(source_traces, key=lambda item: (item["fusion_rank"] is None, item["fusion_rank"] if item["fusion_rank"] is not None else 1 << 30, item["source_identity"])),
    }


def evaluate_observation(observation: dict[str, Any], criteria: dict[str, Any]) -> tuple[bool, list[dict[str, Any]]]:
    failures: list[dict[str, Any]] = []
    minimum_accuracy = criteria.get("minimum_accuracy")
    if isinstance(minimum_accuracy, (int, float)) and float(observation["final_answer_accuracy"]) < float(minimum_accuracy):
        failures.append({"stage": "final-answer", "mechanism": "accuracy-below-frozen-gate"})
    if criteria.get("require_complete_fact_delivery") is True and not observation["fact_delivery"]["complete"]:
        failures.append({"stage": "evidence-delivery", "mechanism": "diagnostic-evidence-incomplete"})
    for category, field in (("temporal-reasoning", "temporal_correctness"), ("knowledge-update", "conflict_correctness")):
        minimum = _mapping(criteria, "category_minimums").get(category) if isinstance(criteria.get("category_minimums"), dict) else None
        if isinstance(minimum, (int, float)) and observation[field] is not None and float(observation[field]) < float(minimum):
            failures.append({"stage": category, "mechanism": "category-correctness-below-frozen-gate"})
    maximum_wall = criteria.get("maximum_wall_seconds")
    if isinstance(maximum_wall, (int, float)) and float(observation["latency"]["wall_seconds"]) > float(maximum_wall):
        failures.append({"stage": "efficiency", "mechanism": "wall-budget-exceeded"})
    return not failures, failures


def compare_execution_results(left_path: Path, right_path: Path) -> dict[str, Any]:
    left = _load_execution_result(left_path.resolve())
    right = _load_execution_result(right_path.resolve())
    diagnostic = left["subject_role"] == "current-product-not-baseline"
    _require(left["subject_role"] in {"v2-candidate", "current-product-not-baseline"}, "同尺比较左侧必须是 V2 候选或明确的当前产品诊断")
    _require(right["subject_role"] == "evaluation-baseline", "同尺比较右侧必须是冻结 V0 基线")
    if diagnostic:
        _require(left["comparison_purpose"] == right["comparison_purpose"] == "stage3-current-product-diagnostic", "当前产品诊断目的错绑")
        _require(left["candidate_decision"] is None, "当前产品诊断不得形成候选决定")
    else:
        _require(left["comparison_purpose"] == right["comparison_purpose"] == "candidate-evaluation", "候选比较目的错绑")
        _require(left["passed"] is True and left["candidate_decision"] is True, "V2 候选未通过冻结绝对门，禁止比较 V0")
    _require(right["candidate_decision"] is None, "V0 结果不得形成候选决定")
    _require(left["evidence_type"] == right["evidence_type"], "同尺比较的证据类型不同")
    _require(left["input_identity"] == right["input_identity"], "同尺比较的完整输入身份不同")
    left_conditions = dict(_mapping(left, "shared_conditions"))
    right_conditions = dict(_mapping(right, "shared_conditions"))
    _require(left_conditions == right_conditions, "同尺比较的共享评测条件不同")
    left_dependencies = dict(_mapping(left, "direct_dependencies"))
    right_dependencies = dict(_mapping(right, "direct_dependencies"))
    _require(left_dependencies == right_dependencies, "同尺比较材料、执行器或观察器身份不同")
    metrics = {
        "final_answer_accuracy": float(right["observation"]["final_answer_accuracy"]) - float(left["observation"]["final_answer_accuracy"]),
        "retrieval_mean_ms": float(right["observation"]["latency"]["retrieval_mean_ms"]) - float(left["observation"]["latency"]["retrieval_mean_ms"]),
        "wall_seconds": float(right["observation"]["latency"]["wall_seconds"]) - float(left["observation"]["latency"]["wall_seconds"]),
    }
    value = {
        "schema": "ownward.kernel-iteration-pair-observation/v2",
        "comparison_purpose": left["comparison_purpose"],
        "candidate_decision": None if diagnostic else True,
        "evidence_type": left["evidence_type"],
        "left_subject": left["subject_identity"],
        "right_subject": right["subject_identity"],
        "input_identity": left["input_identity"],
        "shared_conditions": left_conditions,
        "direct_dependencies": left_dependencies,
        "deltas_right_minus_left": metrics,
    }
    return {**value, "identity": evidence.canonical_sha256(value)}


def _load_execution_result(path: Path) -> dict[str, Any]:
    value = _load_json(path)
    _require(value.get("schema") == EXECUTION_RESULT_SCHEMA, "同尺执行结果 schema 无效")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), "同尺执行结果身份漂移")
    _require(value.get("subject_role") in {"v2-candidate", "evaluation-baseline", "current-product-not-baseline"}, "同尺执行结果 subject 角色无效")
    purpose = value.get("comparison_purpose")
    _require(purpose in {"candidate-evaluation", "stage3-current-product-diagnostic"}, "同尺执行结果比较目的无效")
    if value["subject_role"] == "current-product-not-baseline":
        _require(purpose == "stage3-current-product-diagnostic" and value.get("candidate_decision") is None, "当前产品结果越过非候选诊断边界")
    if value["subject_role"] == "v2-candidate":
        _require(purpose == "candidate-evaluation", "V2 候选结果比较目的无效")
    _require(isinstance(value.get("subject_identity"), str) and len(value["subject_identity"]) == 64, "同尺执行结果 subject 身份无效")
    _require(evidence.is_sha256(value.get("input_identity")), "同尺执行结果完整输入身份无效")
    conditions = _mapping(value, "shared_conditions")
    _require(conditions and all(evidence.is_sha256(item) for item in conditions.values()), "同尺执行结果共享评测条件无效")
    return value


def prepare_stage3(
    suite_root: Path,
    output_root: Path,
    development_input: Path,
    regression_input: Path,
    formal_state_path: Path,
    *,
    resume: bool = False,
) -> dict[str, Any]:
    """Freeze stage-3 policy, subjects and inputs before any diagnostic result exists."""
    suite_root = suite_root.resolve()
    output_root = output_root.resolve()
    repository = suite_root.parents[2]
    evidence._validate_output_boundary(repository, output_root)
    stage3 = load_stage3_contract(suite_root)
    comparison = evidence.load_contract(suite_root)
    inputs = {
        "development": _load_stage3_input(development_input, "development", stage3["loaded"]["development"]),
        "regression": _load_stage3_input(regression_input, "regression", stage3["loaded"]["regression"]),
    }
    state_path = formal_state_path.resolve()
    _require(state_path.is_file(), "阶段 3 缺少正式 state 只读基线")
    state_sha = evidence.file_sha256(state_path)
    current = evidence.select_subject(comparison, "current-product")
    baseline = evidence.select_subject(comparison, "v0")
    content = {
        "schema": STAGE3_PLAN_SCHEMA,
        "contract_identity": stage3["identity"],
        "comparison_contract_identity": comparison["identity"],
        "subjects": {"current-product": current["identity"], "v0": baseline["identity"]},
        "inputs": {name: item["identity"] for name, item in sorted(inputs.items())},
        "formal_state_sha256": state_sha,
        "formal": False,
        "candidate_decision": None,
        "v2_candidate_exists": False,
    }
    plan = {**content, "identity": evidence.canonical_sha256(content)}
    root = output_root / "stage3" / plan["identity"]
    plan_path = root / "plan.json"
    if plan_path.is_file():
        _require(resume and _load_json(plan_path) == plan, "阶段 3 计划已存在或身份漂移")
        reused = True
    else:
        evidence.atomic_json(plan_path, plan)
        reused = False
    _require(evidence.file_sha256(state_path) == state_sha, "阶段 3 准备改写了正式 state")
    return {"passed": True, "status": "prepared", "plan_identity": plan["identity"], "plan_path": str(plan_path), "reused": reused}


def finalize_stage3(
    suite_root: Path,
    output_root: Path,
    plan_identity: str,
    result_paths: dict[str, Path],
    formal_state_path: Path,
    *,
    resume: bool = False,
) -> dict[str, Any]:
    """Close aggregate attribution and material freeze without creating a V2 decision."""
    suite_root = suite_root.resolve()
    output_root = output_root.resolve()
    _require(evidence.is_sha256(plan_identity), "阶段 3 计划身份无效")
    root = output_root / "stage3" / plan_identity
    plan = _load_json(root / "plan.json")
    _require(plan.get("schema") == STAGE3_PLAN_SCHEMA and plan.get("identity") == plan_identity, "阶段 3 计划错绑")
    content = {key: item for key, item in plan.items() if key != "identity"}
    _require(evidence.canonical_sha256(content) == plan_identity, "阶段 3 计划内容漂移")
    stage3 = load_stage3_contract(suite_root)
    _require(plan["contract_identity"] == stage3["identity"], "阶段 3 计划与当前合同错绑")
    state_path = formal_state_path.resolve()
    _require(evidence.file_sha256(state_path) == plan["formal_state_sha256"], "阶段 3 期间正式 state 发生变化")
    expected = {"current-development", "v0-development", "current-regression", "v0-regression"}
    _require(set(result_paths) == expected, "阶段 3 结果角色不完整")
    results = {name: _load_execution_result(path.resolve()) for name, path in result_paths.items()}
    for evidence_type in ("development", "regression"):
        current = results[f"current-{evidence_type}"]
        baseline = results[f"v0-{evidence_type}"]
        _require(current["subject_role"] == "current-product-not-baseline" and baseline["subject_role"] == "evaluation-baseline", f"{evidence_type} subject 顺序错绑")
        _require(current["subject_identity"] == plan["subjects"]["current-product"] and baseline["subject_identity"] == plan["subjects"]["v0"], f"{evidence_type} subject 身份漂移")
        _require(current["input_identity"] == baseline["input_identity"] == plan["inputs"][evidence_type], f"{evidence_type} 输入身份漂移")
        _require(current["comparison_purpose"] == baseline["comparison_purpose"] == "stage3-current-product-diagnostic", f"{evidence_type} 越过非候选诊断边界")
        _require(current["candidate_decision"] is None and baseline["candidate_decision"] is None, f"{evidence_type} 不得形成候选决定")
        _validate_execution_resume_receipt(result_paths[f"current-{evidence_type}"])
        _validate_execution_resume_receipt(result_paths[f"v0-{evidence_type}"])
    development_pair = compare_execution_results(result_paths["current-development"], result_paths["v0-development"])
    regression_pair = compare_execution_results(result_paths["current-regression"], result_paths["v0-regression"])
    current_development = results["current-development"]["observation"]
    v0_development = results["v0-development"]["observation"]
    current_regression = results["current-regression"]["observation"]
    v0_regression = results["v0-regression"]["observation"]
    thresholds = _mapping(stage3, "thresholds")
    root_cases = [item for item in current_development["case_evidence"] if item["first_proven_mechanism"] == "fragment-incomplete-after-read"]
    root_proven = len(root_cases) >= int(thresholds["root_minimum_cases"])
    v0_protection = (
        float(v0_regression["final_answer_accuracy"]) >= float(thresholds["v0_regression_minimum_accuracy"])
        and v0_regression["fact_delivery"]["complete"] is True
    )
    wall_seconds = sum(float(results[name]["observation"]["latency"]["wall_seconds"]) for name in expected)
    benefit_results = _adjudicate_v1_benefits(stage3, results)
    directions = _stage3_direction_decisions(root_proven, benefit_results)
    passed = root_proven and v0_protection and wall_seconds <= float(thresholds["daily_feedback_hard_seconds"]) and len(directions) == 5
    result_content = {
        "schema": STAGE3_RESULT_SCHEMA,
        "plan_identity": plan_identity,
        "contract_identity": stage3["identity"],
        "status": "passed" if passed else "failed",
        "passed": passed,
        "formal": False,
        "formal_state_written": False,
        "candidate_decision": None,
        "v2_candidate_exists": False,
        "root_cause": {
            "status": "proven" if root_proven else "not-proven",
            "mechanism": "source-read-complete-but-required-fragments-incomplete",
            "independent_cases": len(root_cases),
            "minimum_cases": int(thresholds["root_minimum_cases"]),
            "responsible_direction": "information-representation-and-organization",
        },
        "v0_correct_capability_protection": {"passed": v0_protection, "accuracy": v0_regression["final_answer_accuracy"], "fact_delivery_complete": v0_regression["fact_delivery"]["complete"]},
        "v1_benefit_reproof": benefit_results,
        "direction_decisions": directions,
        "comparisons": {"development": development_pair["identity"], "regression": regression_pair["identity"]},
        "execution_results": {name: {"identity": value["identity"], "sha256": evidence.file_sha256(result_paths[name].resolve())} for name, value in sorted(results.items())},
        "cost": {"wall_seconds": wall_seconds, "hard_seconds": int(thresholds["daily_feedback_hard_seconds"])},
        "formal_state_sha256": plan["formal_state_sha256"],
    }
    result = {**result_content, "identity": evidence.canonical_sha256(result_content)}
    result_path = root / "result.json"
    if result_path.is_file():
        _require(resume and _load_json(result_path) == result, "阶段 3 终态已存在或结果漂移")
    else:
        evidence.atomic_json(result_path, result)
    _require(evidence.file_sha256(state_path) == plan["formal_state_sha256"], "阶段 3 终态改写了正式 state")
    return {**result, "result_path": str(result_path)}


def _load_stage3_input(path: Path, evidence_type: str, materials: dict[str, Any]) -> dict[str, Any]:
    value = _load_json(path.resolve())
    _require(value.get("schema") == EXECUTION_INPUT_SCHEMA and value.get("evidence_type") == evidence_type, f"阶段 3 {evidence_type} 输入无效")
    _require(_mapping(value, "shared_conditions").get("dataset") == materials["identity"], f"阶段 3 {evidence_type} 材料身份错绑")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), f"阶段 3 {evidence_type} 输入身份漂移")
    return value


def _validate_execution_resume_receipt(result_path: Path) -> None:
    result_path = result_path.resolve()
    receipt_path = result_path.parent / "execution-resume.json"
    _require(receipt_path.is_file(), "阶段 3 缺少执行恢复收据")
    receipt = _load_json(receipt_path)
    _require(receipt.get("schema") == "ownward.kernel-iteration-execution-resume/v1", "阶段 3 缺少执行恢复收据")
    content = {key: item for key, item in receipt.items() if key != "identity"}
    _require(receipt.get("identity") == evidence.canonical_sha256(content), "执行恢复收据身份漂移")
    _require(receipt.get("execution_result_sha256") == evidence.file_sha256(result_path), "执行恢复收据与结果错绑")
    _require(receipt.get("reused_execution") is True and receipt.get("model_or_product_execution") is False, "恢复证明没有做到零模型与零产品执行")


def _adjudicate_v1_benefits(stage3: dict[str, Any], results: dict[str, dict[str, Any]]) -> dict[str, Any]:
    policies = _mapping(stage3, "v1_benefit_reproof")
    current_cases = results["current-development"]["observation"]["case_evidence"]
    baseline_cases = results["v0-development"]["observation"]["case_evidence"]
    long_coverages = set(_mapping(policies, "long-asset-semantic-recall")["coverages"])
    current_long = [item for item in current_cases if item["coverage"] in long_coverages]
    baseline_long = [item for item in baseline_cases if item["coverage"] in long_coverages]
    def source_recall(items: list[dict[str, Any]]) -> float:
        expected = sum(int(item["expected_sources"]) for item in items)
        return sum(int(item["search_returned_sources"]) for item in items) / expected if expected else 0.0
    def claim_recall(items: list[dict[str, Any]]) -> float:
        total = sum(int(item["truth_claims"]) for item in items)
        return sum(int(item["delivered_truth_claims"]) for item in items) / total if total else 0.0
    current_source = source_recall(current_long)
    baseline_source = source_recall(baseline_long)
    source_policy = _mapping(policies, "long-asset-semantic-recall")
    source_accepted = current_source >= float(source_policy["minimum_recall"]) and current_source >= baseline_source
    source_separable = source_accepted and any(item["first_proven_mechanism"] == "fragment-incomplete-after-read" for item in current_long)
    current_claim = claim_recall(current_cases)
    baseline_claim = claim_recall(baseline_cases)
    evidence_policy = _mapping(policies, "fine-grained-evidence")
    evidence_accepted = current_claim >= float(evidence_policy["minimum_claim_delivery"]) and current_claim >= baseline_claim
    current_bytes = sum(int(results[name]["observation"]["resources"]["ownward_data_bytes"]) for name in ("current-development", "current-regression"))
    baseline_bytes = sum(int(results[name]["observation"]["resources"]["ownward_data_bytes"]) for name in ("v0-development", "v0-regression"))
    storage_ratio = current_bytes / baseline_bytes if baseline_bytes else math.inf
    storage_policy = _mapping(policies, "storage-convergence")
    storage_accepted = storage_ratio <= float(storage_policy["maximum_ratio_to_v0"])
    return {
        "long-asset-semantic-recall": {"decision": "protect" if source_accepted and source_separable else "reject-protection", "current": current_source, "v0": baseline_source, "mechanism_separable": source_separable},
        "fine-grained-evidence": {"decision": "protect" if evidence_accepted else "reject-protection", "current": current_claim, "v0": baseline_claim, "mechanism_separable": evidence_accepted and not any(item["first_proven_mechanism"] == "fragment-incomplete-after-read" for item in current_cases)},
        "batch-durability": {"decision": "protect", "resume_byte_identity": True, "zero_execution_resume": True},
        "storage-convergence": {"decision": "protect" if storage_accepted else "reject-protection", "current_bytes": current_bytes, "v0_bytes": baseline_bytes, "ratio": storage_ratio},
    }


def _stage3_direction_decisions(root_proven: bool, benefits: dict[str, Any]) -> dict[str, Any]:
    return {
        "information-representation-and-organization": {"decision": "optimize" if root_proven else "unresolved", "target": "complete-required-fragment-delivery", "gate": "fragment-gap-at-least-halved", "protect": ["source-identity", "authority-content", "semantic-submission"]},
        "retrieval-architecture": {"decision": "optimize", "target": "evidence-delivery-with-v0-latency-bound", "gate": "retrieval-mean-no-worse-than-v0-plus-repeatability", "protect": ["search-recall", "read-budget", "ranking-quality"]},
        "semantic-capability": {"decision": "protect" if benefits["long-asset-semantic-recall"]["decision"] == "protect" else "optimize", "target": "long-asset-search-return", "gate": "same-scale-reproof", "protect": ["semantic-identity", "organization-completeness"]},
        "data-and-storage": {"decision": "protect" if benefits["storage-convergence"]["decision"] == "protect" else "optimize", "target": "storage-convergence", "gate": "no-regression-versus-v0", "protect": ["durability", "rebuild", "authority-losslessness"]},
        "execution-and-state": {"decision": "protect", "target": "batch-durability-and-resume", "gate": "byte-identical-zero-execution-resume", "protect": ["atomic-checkpoints", "bounded-recovery", "formal-state-isolation"]},
    }


def calibrate_blind(
    suite_root: Path,
    output_root: Path,
    execution_config_path: Path,
    formal_state_path: Path,
    *,
    seed: str | None = None,
    plan_identity: str | None = None,
    resume: bool = False,
    invoker: Callable[..., tuple[dict[str, Any], dict[str, Any]]] | None = None,
    runner: Callable[..., dict[str, Any]] | None = None,
) -> dict[str, Any]:
    suite_root = suite_root.resolve()
    output_root = output_root.resolve()
    repository = suite_root.parents[2]
    evidence._validate_output_boundary(repository, output_root)
    comparison = evidence.load_contract(suite_root)
    validation = load_validation_contract(suite_root)
    runtime = validate_execution_config(suite_root, execution_config_path.resolve())
    subject = evidence.select_subject(comparison, "current-product")
    _verify_subject_binary(subject, runtime["binary"], allow_current_product=True)
    state_path = formal_state_path.resolve()
    _require(state_path.is_file(), "五题校准缺少正式 state 只读基线")
    state_before = state_path.read_bytes()
    runtime_calibration = evidence.calibrate_runtime(
        suite_root,
        output_root / "runtime-calibration",
        state_path,
        resume=resume,
    )
    _verify_runtime_binary_binding(state_path, runtime["binary"])

    _require(not (seed is not None and plan_identity is not None), "五题关卡不得同时按 seed 和 plan identity 恢复")
    if plan_identity is not None:
        _require(resume and evidence.is_sha256(plan_identity), "五题 plan identity 恢复参数无效")
        recovery_root = output_root / "blind-calibration" / plan_identity
        recovery = _load_blind_recovery(recovery_root / "active.json", plan_identity)
        _require(Path(recovery["execution_config"]).resolve() == execution_config_path.resolve(), "五题恢复执行配置路径漂移")
        _require(Path(recovery["formal_state"]).resolve() == state_path, "五题恢复正式 state 路径漂移")
        expected_scratch = runtime["runs"] / "kernel-v2-blind-calibration" / plan_identity
        _require(Path(recovery["scratch"]).resolve() == expected_scratch.resolve(), "五题恢复 scratch 路径漂移")
        secret = _load_blind_secret(expected_scratch / "recovery-secret.json", plan_identity)
        gate_seed = secret["gate_seed"]
    else:
        gate_seed = seed or secrets.token_hex(16)
    _require(len(gate_seed) >= 16 and all(character.isalnum() or character in "-_" for character in gate_seed), "盲测校准 seed 无效")
    dependencies = _blind_dependencies(suite_root, validation, runtime, subject, runtime_calibration)
    plan_content = {
        "schema": BLIND_PLAN_SCHEMA,
        "comparison_contract_identity": comparison["identity"],
        "validation_contract_identity": validation["identity"],
        "subject_identity": subject["identity"],
        "purpose": "non-candidate-five-question-calibration",
        "candidate_decision": None,
        "seed_sha256": hashlib.sha256(gate_seed.encode("utf-8")).hexdigest(),
        "direct_dependencies": dict(sorted(dependencies.items())),
        "formal": False,
    }
    computed_plan_identity = evidence.canonical_sha256(plan_content)
    if plan_identity is not None:
        _require(computed_plan_identity == plan_identity, "五题恢复 plan identity 与当前直接依赖不一致")
    else:
        plan_identity = computed_plan_identity
    plan = {**plan_content, "identity": plan_identity}
    root = output_root / "blind-calibration" / plan_identity
    plan_path = root / "plan.json"
    result_path = root / "result.json"
    if result_path.is_file():
        _require(resume and plan_path.is_file() and _load_json(plan_path) == plan, "五题校准终态只能由同一身份 --resume 复用")
        result = _load_json(result_path)
        _validate_blind_terminal_result(result, plan_identity)
        _require(state_path.read_bytes() == state_before, "复用五题校准时正式 state 发生变化")
        return {"passed": result["passed"], "status": result["status"], "plan_identity": plan_identity, "result": str(result_path), "reused": True}
    if plan_path.is_file():
        _require(resume and _load_json(plan_path) == plan, "既有五题校准计划身份漂移")
    else:
        evidence.atomic_json(plan_path, plan)

    scratch = runtime["runs"] / "kernel-v2-blind-calibration" / plan_identity
    active_path = root / "active.json"
    secret_path = scratch / "recovery-secret.json"
    if plan_identity == computed_plan_identity and active_path.is_file():
        recovery = _load_blind_recovery(active_path, plan_identity)
        _require(Path(recovery["scratch"]).resolve() == scratch.resolve(), "五题恢复 scratch 定位漂移")
        _require(Path(recovery["execution_config"]).resolve() == execution_config_path.resolve(), "五题恢复执行配置漂移")
        _require(Path(recovery["formal_state"]).resolve() == state_path, "五题恢复正式 state 漂移")
        secret = _load_blind_secret(secret_path, plan_identity)
        _require(secret["gate_seed"] == gate_seed, "五题恢复秘密与计划摘要不一致")
    else:
        scratch.mkdir(parents=True, exist_ok=True)
        evidence.atomic_json(secret_path, {
            "schema": BLIND_SECRET_SCHEMA,
            "plan_identity": plan_identity,
            "gate_seed": gate_seed,
            "seed_sha256": plan_content["seed_sha256"],
        })
        evidence.atomic_json(active_path, {
            "schema": BLIND_RECOVERY_SCHEMA,
            "plan_identity": plan_identity,
            "execution_config": str(execution_config_path.resolve()),
            "formal_state": str(state_path),
            "scratch": str(scratch.resolve()),
        })
    invoke = invoker or _native_codex_invoke
    run_adapter = runner or _run_longmemeval
    started = time.perf_counter()
    stage_name = "generator"
    generator_usage: dict[str, Any] = {}
    admission_usage: dict[str, Any] = {}
    admission: dict[str, Any] = {"passed": False, "questions": 0, "passed_counts": {}, "rejected_count": 0}
    control_result: dict[str, Any] = {"passed": False, "outcomes": []}
    executions: list[dict[str, Any]] = []
    coverage_counts: dict[str, int] = {}
    try:
        generated_cases: list[dict[str, Any]] = []
        generator_usages: list[dict[str, Any]] = []
        for index, coverage in enumerate(BLIND_COVERAGE, start=1):
            case_id = f"c{index:02d}"
            generator_output, case_usage = invoke(
                suite_root=suite_root,
                runtime=runtime,
                stage=scratch / "generator" / case_id,
                role="generator",
                prompt=_generator_prompt(validation, gate_seed, case_id, coverage),
                schema=_generator_case_schema(case_id, coverage, validation),
                settings=_mapping(_mapping(validation, "blind"), "generation"),
                validate=lambda value, expected_id=case_id, expected_coverage=coverage: _validate_generated_case(value, expected_id, expected_coverage, validation),
            )
            generated_cases.append(_validate_generated_case(generator_output, case_id, coverage, validation))
            generator_usages.append(case_usage)
        generator_usage = _combine_usages(generator_usages)
        materials = _materials_from_generated({"cases": generated_cases})
        validate_materials(materials, expected_questions=5)
        coverage_counts = {name: sum(case["coverage"] == name for case in materials["cases"]) for name in BLIND_COVERAGE}
        stage_name = "quality-admission"
        admission_output, admission_usage = invoke(
            suite_root=suite_root,
            runtime=runtime,
            stage=scratch / "quality-admission",
            role="quality-admission",
            prompt=_admission_prompt(validation, materials),
            schema=_admission_schema([case["case_id"] for case in materials["cases"]]),
            settings=_mapping(_mapping(validation, "blind"), "quality_admission"),
            validate=lambda value: validate_admission(value, materials, validation),
        )
        admission = validate_admission(admission_output, materials, validation)
        control_result = score_controls(materials)
        _require(control_result["passed"], "盲测评分控制未区分已知正确与关键错误")
        if not admission["passed"]:
            _destroy_blind_scratch(scratch, runtime["runs"])
            active_path.unlink(missing_ok=True)
            terminal = _blind_terminal(
                plan_identity,
                validation,
                status="quality-rejected",
                passed=False,
                generator_usage=generator_usage,
                admission_usage=admission_usage,
                admission=admission,
                controls=control_result,
                executions=[],
                resume_proof=None,
                total_wall_seconds=time.perf_counter() - started,
                coverage_counts=coverage_counts,
            )
            evidence.atomic_json(result_path, terminal)
            _require(state_path.read_bytes() == state_before, "质量拒绝路径改写了正式 state")
            return {"passed": False, "status": "quality-rejected", "plan_identity": plan_identity, "result": str(result_path), "reused": False}

        stage_name = "execution"
        dataset_path = scratch / "dataset.json"
        evidence.atomic_json(dataset_path, [_longmemeval_case(case) for case in materials["cases"]])
        for repetition in range(1, int(_mapping(validation, "calibration")["repetitions"]) + 1):
            run_dir = scratch / f"execution-{repetition}"
            report = run_adapter(
                suite_root=suite_root,
                runtime=runtime,
                dataset_path=dataset_path,
                output_dir=run_dir,
                subject_identity=subject["identity"],
                resume=False,
            )
            summary_path = run_dir / "diagnostic-summary.json"
            summary = _load_json(summary_path)
            observation = observe_report({**report, "diagnostic_summary": summary}, materials)
            executions.append({
                "repetition": repetition,
                "report_sha256": evidence.file_sha256(run_dir / "report.json"),
                "checkpoint_sha256": evidence.file_sha256(run_dir / "checkpoint-manifest.json"),
                "diagnostic_summary_sha256": evidence.file_sha256(summary_path),
                "observation": observation,
            })
        stage_name = "resume-verification"
        resume_dir = scratch / "execution-1"
        report_before = (resume_dir / "report.json").read_bytes()
        checkpoint_before = (resume_dir / "checkpoint-manifest.json").read_bytes()
        run_adapter(
            suite_root=suite_root,
            runtime=runtime,
            dataset_path=dataset_path,
            output_dir=resume_dir,
            subject_identity=subject["identity"],
            resume=True,
        )
        resume_proof = {
            "report_byte_identical": report_before == (resume_dir / "report.json").read_bytes(),
            "checkpoint_byte_identical": checkpoint_before == (resume_dir / "checkpoint-manifest.json").read_bytes(),
            "model_work_reused": True,
        }
        _require(all(resume_proof.values()), "五题恢复没有逐字节复用有效结果")
        stage_name = "budget-derivation"
        budgets = derive_blind_budgets(validation, generator_usage, admission_usage, executions)
        _destroy_blind_scratch(scratch, runtime["runs"])
        active_path.unlink(missing_ok=True)
        terminal = _blind_terminal(
            plan_identity,
            validation,
            status="calibrated",
            passed=True,
            generator_usage=generator_usage,
            admission_usage=admission_usage,
            admission=admission,
            controls=control_result,
            executions=executions,
            resume_proof=resume_proof,
            total_wall_seconds=time.perf_counter() - started,
            budgets=budgets,
            coverage_counts=coverage_counts,
        )
        evidence.atomic_json(result_path, terminal)
        _require(state_path.read_bytes() == state_before, "五题校准改写了正式 Acceptance state")
        _require(not scratch.exists(), "五题终态仍保留可还原原始数据")
        return {"passed": True, "status": "calibrated", "plan_identity": plan_identity, "result": str(result_path), "reused": False, "budgets": budgets}
    except (KeyboardInterrupt, InterruptedError):
        _require(state_path.read_bytes() == state_before, "中断路径改写了正式 Acceptance state")
        raise
    except Exception as error:
        _require(state_path.read_bytes() == state_before, "失败路径改写了正式 Acceptance state")
        _destroy_blind_scratch(scratch, runtime["runs"])
        active_path.unlink(missing_ok=True)
        terminal = _blind_terminal(
            plan_identity,
            validation,
            status="failed",
            passed=False,
            generator_usage=generator_usage,
            admission_usage=admission_usage,
            admission=admission,
            controls=control_result,
            executions=executions,
            resume_proof=None,
            total_wall_seconds=time.perf_counter() - started,
            coverage_counts=coverage_counts,
            failure={"stage": stage_name, "error_type": type(error).__name__},
        )
        evidence.atomic_json(result_path, terminal)
        raise


def resume_blind_by_plan_identity(
    suite_root: Path,
    output_root: Path,
    plan_identity: str,
    *,
    invoker: Callable[..., tuple[dict[str, Any], dict[str, Any]]] | None = None,
    runner: Callable[..., dict[str, Any]] | None = None,
) -> dict[str, Any]:
    suite_root = suite_root.resolve()
    output_root = output_root.resolve()
    evidence._validate_output_boundary(suite_root.parents[2], output_root)
    _require(evidence.is_sha256(plan_identity), "五题 plan identity 无效")
    root = output_root / "blind-calibration" / plan_identity
    plan = _load_json(root / "plan.json")
    _validate_blind_plan(plan, plan_identity)
    result_path = root / "result.json"
    if result_path.is_file():
        _validate_blind_plan_against_current_sources(suite_root, plan)
        result = _load_json(result_path)
        _validate_blind_terminal_result(result, plan_identity)
        _require(not (root / "active.json").exists(), "五题终态仍保留活动恢复定位")
        return {"passed": result["passed"], "status": result["status"], "plan_identity": plan_identity, "result": str(result_path), "reused": True, "model_calls": 0, "product_executions": 0}
    recovery = _load_blind_recovery(root / "active.json", plan_identity)
    return calibrate_blind(
        suite_root,
        output_root,
        Path(recovery["execution_config"]),
        Path(recovery["formal_state"]),
        plan_identity=plan_identity,
        resume=True,
        invoker=invoker,
        runner=runner,
    )


def bind_blind_dependency_locator(
    suite_root: Path,
    output_root: Path,
    plan_identity: str,
    execution_config_path: Path,
    formal_state_path: Path,
) -> dict[str, Any]:
    """Bind a terminal blind plan to stable, non-secret current dependency locations."""
    suite_root = suite_root.resolve()
    output_root = output_root.resolve()
    evidence._validate_output_boundary(suite_root.parents[2], output_root)
    _require(evidence.is_sha256(plan_identity), "五题 plan identity 无效")
    root = output_root / "blind-calibration" / plan_identity
    plan = _load_json(root / "plan.json")
    _validate_blind_plan(plan, plan_identity)
    result = _load_json(root / "result.json")
    _validate_blind_terminal_result(result, plan_identity)
    _require(not (root / "active.json").exists(), "运行中的五题关卡不能封存终态依赖定位")
    execution_config = execution_config_path.resolve()
    formal_state = formal_state_path.resolve()
    dependencies = _current_blind_dependencies(suite_root, execution_config, formal_state)
    _require(dependencies == dict(_mapping(plan, "direct_dependencies")), "五题终态的当前直接依赖已漂移")
    content = {
        "schema": BLIND_DEPENDENCY_LOCATOR_SCHEMA,
        "plan_identity": plan_identity,
        "execution_config": str(execution_config),
        "formal_state": str(formal_state),
        "current_verifier_identity": _blind_current_verifier_identity(),
    }
    locator = {**content, "identity": evidence.canonical_sha256(content)}
    path = root / "dependency-locator.json"
    if path.is_file():
        previous = _load_json(path)
        previous_content = {key: item for key, item in previous.items() if key != "identity"}
        _require(previous.get("schema") == BLIND_DEPENDENCY_LOCATOR_SCHEMA and previous.get("plan_identity") == plan_identity, "五题终态依赖定位不能原子收敛")
        _require(previous.get("identity") == evidence.canonical_sha256(previous_content), "五题终态依赖定位摘要漂移")
    if not path.is_file() or _load_json(path) != locator:
        evidence.atomic_json(path, locator)
    return {"plan_identity": plan_identity, "dependency_locator": str(path), "dependency_locator_identity": locator["identity"], "current_verifier_identity": locator["current_verifier_identity"]}


def resume_current_blind_by_plan_identity(
    suite_root: Path,
    output_root: Path,
    plan_identity: str,
    *,
    invoker: Callable[..., tuple[dict[str, Any], dict[str, Any]]] | None = None,
    runner: Callable[..., dict[str, Any]] | None = None,
) -> dict[str, Any]:
    """Resume an active gate, or read a terminal gate only if every current dependency still matches."""
    suite_root = suite_root.resolve()
    output_root = output_root.resolve()
    evidence._validate_output_boundary(suite_root.parents[2], output_root)
    _require(evidence.is_sha256(plan_identity), "五题 plan identity 无效")
    root = output_root / "blind-calibration" / plan_identity
    plan = _load_json(root / "plan.json")
    _validate_blind_plan(plan, plan_identity)
    result_path = root / "result.json"
    if not result_path.is_file():
        return resume_blind_by_plan_identity(
            suite_root,
            output_root,
            plan_identity,
            invoker=invoker,
            runner=runner,
        )
    locator = _load_blind_dependency_locator(root / "dependency-locator.json", plan_identity)
    _validate_current_blind_dependencies(suite_root, plan, locator)
    result = _load_json(result_path)
    _validate_blind_terminal_result(result, plan_identity)
    _require(not (root / "active.json").exists(), "五题终态仍保留活动恢复定位")
    return {"passed": result["passed"], "status": result["status"], "plan_identity": plan_identity, "result": str(result_path), "reused": True, "model_calls": 0, "product_executions": 0, "current_dependencies_valid": True}


def _load_blind_dependency_locator(path: Path, plan_identity: str) -> dict[str, Any]:
    value = _load_json(path)
    _require(value.get("schema") == BLIND_DEPENDENCY_LOCATOR_SCHEMA and value.get("plan_identity") == plan_identity, "五题终态依赖定位错绑")
    _require(set(value) == {"schema", "plan_identity", "execution_config", "formal_state", "current_verifier_identity", "identity"}, "五题终态依赖定位字段越界")
    for name in ("execution_config", "formal_state"):
        _require(isinstance(value.get(name), str) and Path(value[name]).is_absolute(), f"五题终态依赖定位 {name} 无效")
    _require(value.get("current_verifier_identity") == _blind_current_verifier_identity(), "五题终态当前有效性校验器已漂移")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), "五题终态依赖定位摘要漂移")
    return value


def _validate_current_blind_dependencies(suite_root: Path, plan: dict[str, Any], locator: dict[str, Any]) -> None:
    current = _current_blind_dependencies(
        suite_root,
        Path(locator["execution_config"]),
        Path(locator["formal_state"]),
    )
    _require(current == dict(_mapping(plan, "direct_dependencies")), "五题终态至少一个当前直接依赖已漂移")


def _current_blind_dependencies(
    suite_root: Path,
    execution_config_path: Path,
    formal_state_path: Path,
) -> dict[str, str]:
    comparison = evidence.load_contract(suite_root)
    validation = load_validation_contract(suite_root)
    runtime = validate_execution_config(suite_root, execution_config_path.resolve())
    subject = evidence.select_subject(comparison, "current-product")
    _verify_subject_binary(subject, runtime["binary"], allow_current_product=True)
    runtime_calibration = evidence.inspect_runtime_calibration(suite_root, formal_state_path.resolve())
    _verify_runtime_binary_binding(formal_state_path.resolve(), runtime["binary"])
    return _blind_dependencies(suite_root, validation, runtime, subject, {
        "runtime_calibration_identity": runtime_calibration["identity"],
    })


def _blind_current_verifier_identity() -> str:
    callbacks = (
        evidence.inspect_runtime_calibration,
        load_blind_budget_archive,
        load_blind_budget,
        bind_blind_dependency_locator,
        resume_current_blind_by_plan_identity,
        _load_blind_dependency_locator,
        _validate_current_blind_dependencies,
        _current_blind_dependencies,
    )
    return evidence.canonical_sha256({
        "schema": "ownward.kernel-iteration-blind-current-verifier/v1",
        "sources": [inspect.getsource(callback) for callback in callbacks],
    })


def validate_admission(output: dict[str, Any], materials: dict[str, Any], validation: dict[str, Any]) -> dict[str, Any]:
    assessments = output.get("assessments")
    _require(isinstance(assessments, list) and len(assessments) == len(materials["cases"]), "质量准入没有逐题完成")
    required = _mapping(_mapping(validation, "blind"), "quality_admission")["required_checks"]
    by_id = {item.get("case_id"): item for item in assessments if isinstance(item, dict)}
    _require(set(by_id) == {case["case_id"] for case in materials["cases"]}, "质量准入案例身份缺失或错绑")
    counts = {name: 0 for name in required}
    rejected: list[str] = []
    for case in materials["cases"]:
        item = by_id[case["case_id"]]
        checks = item.get("checks")
        _require(isinstance(checks, dict) and set(checks) == set(required), "质量准入检查项漂移")
        for name in required:
            counts[name] += int(checks[name] is True)
        if not all(checks[name] is True for name in required):
            rejected.append(case["case_id"])
    return {"passed": not rejected, "questions": len(materials["cases"]), "passed_counts": counts, "rejected_count": len(rejected)}


def score_controls(materials: dict[str, Any]) -> dict[str, Any]:
    cases = materials["cases"]
    update = next(case for case in cases if case["coverage"] == "knowledge-update-conflict")
    definitions = [
        ("known-correct", cases[0], list(cases[0]["answer_session_ids"]), cases[0]["answer"], True),
        ("missing-evidence", cases[1], list(cases[1]["answer_session_ids"][:-1]), cases[1]["answer"], False),
        ("wrong-evidence", cases[2], list(cases[2]["distractor_session_ids"]), cases[2]["answer"], False),
        ("stale-evidence", update, list(update["stale_session_ids"]), update["answer"], False),
        ("wrong-answer", cases[4], list(cases[4]["answer_session_ids"]), "known-wrong-control-answer", False),
    ]
    outcomes = []
    for name, case, delivered, answer, expected in definitions:
        actual = _mechanical_answer_score(case, delivered, answer)
        outcomes.append({"control": name, "expected": expected, "actual": actual, "matched": actual is expected})
    return {"passed": all(item["matched"] for item in outcomes), "outcomes": outcomes}


def derive_blind_budgets(
    validation: dict[str, Any],
    generator_usage: dict[str, Any],
    admission_usage: dict[str, Any],
    executions: list[dict[str, Any]],
) -> dict[str, Any]:
    calibration = _mapping(validation, "calibration")
    _require(len(executions) == 2, "预算校准缺少两次独立五题执行")
    walls = [float(item["observation"]["latency"]["wall_seconds"]) for item in executions]
    per_question_upper = max(walls) / 5.0
    generation_admission = float(generator_usage.get("wall_seconds", 0.0)) + float(admission_usage.get("wall_seconds", 0.0))
    reserve = 1.0 + float(calibration["normal_variation_reserve_ratio"]) + float(calibration["bounded_retry_reserve_ratio"])
    recovery = float(calibration["level_recovery_reserve_seconds"])
    levels: dict[str, dict[str, int]] = {}
    for count in _mapping(validation, "blind")["levels"]:
        normal = math.ceil((generation_admission + 2 * int(count) * per_question_upper) * reserve + recovery)
        failure = math.ceil((generation_admission + int(count) * per_question_upper) * reserve + recovery)
        levels[str(count)] = {"normal_seconds": normal, "failure_seconds": failure}
    total = sum(item["normal_seconds"] for item in levels.values())
    design = int(calibration["design_total_normal_seconds"])
    _require(total <= design, "五题实测无法支持 150 分钟内的四级正常路径")
    return {
        "schema": "ownward.kernel-iteration-blind-budgets/v1",
        "levels": levels,
        "total_normal_seconds": total,
        "design_total_normal_seconds": design,
        "per_question_upper_seconds": per_question_upper,
        "generation_admission_seconds": generation_admission,
        "repeatability_error": {
            "wall_seconds": abs(walls[0] - walls[1]),
            "final_answer_accuracy": abs(float(executions[0]["observation"]["final_answer_accuracy"]) - float(executions[1]["observation"]["final_answer_accuracy"])),
            "retrieval_mean_ms": abs(float(executions[0]["observation"]["latency"]["retrieval_mean_ms"]) - float(executions[1]["observation"]["latency"]["retrieval_mean_ms"])),
        },
        "reserve": {"normal_variation_ratio": calibration["normal_variation_reserve_ratio"], "bounded_retry_ratio": calibration["bounded_retry_reserve_ratio"], "per_level_recovery_seconds": recovery},
    }


def _blind_terminal(
    plan_identity: str,
    validation: dict[str, Any],
    *,
    status: str,
    passed: bool,
    generator_usage: dict[str, Any],
    admission_usage: dict[str, Any],
    admission: dict[str, Any],
    controls: dict[str, Any],
    executions: list[dict[str, Any]],
    resume_proof: dict[str, Any] | None,
    total_wall_seconds: float,
    budgets: dict[str, Any] | None = None,
    coverage_counts: dict[str, int] | None = None,
    failure: dict[str, str] | None = None,
) -> dict[str, Any]:
    execution_aggregates = []
    for item in executions:
        observation = item["observation"]
        execution_aggregates.append({
            "repetition": item["repetition"],
            "report_sha256": item["report_sha256"],
            "checkpoint_sha256": item["checkpoint_sha256"],
            "diagnostic_summary_sha256": item["diagnostic_summary_sha256"],
            "questions": observation["questions"],
            "final_answer_accuracy": observation["final_answer_accuracy"],
            "temporal_correctness": observation["temporal_correctness"],
            "conflict_correctness": observation["conflict_correctness"],
            "latency": observation["latency"],
            "resources": observation["resources"],
            "codex": _sanitize_codex(observation.get("codex")),
        })
    content = {
        "schema": BLIND_RESULT_SCHEMA,
        "plan_identity": plan_identity,
        "validation_contract_identity": validation["identity"],
        "status": status,
        "passed": passed,
        "candidate_decision": None,
        "formal": False,
        "formal_state_written": False,
        "raw_materials_destroyed": True,
        "contains_reversible_question_answer_or_evidence": False,
        "coverage_counts": coverage_counts if coverage_counts is not None else {name: 1 for name in BLIND_COVERAGE},
        "quality_admission": admission,
        "control_discrimination": controls,
        "generator_usage": _sanitize_usage(generator_usage),
        "admission_usage": _sanitize_usage(admission_usage),
        "executions": execution_aggregates,
        "resume_proof": resume_proof,
        "budgets": budgets,
        "failure": failure,
        "total_wall_seconds": total_wall_seconds,
    }
    return {**content, "identity": evidence.canonical_sha256(content)}


def _validate_blind_terminal_result(value: dict[str, Any], plan_identity: str) -> None:
    _require(value.get("schema") == BLIND_RESULT_SCHEMA and value.get("plan_identity") == plan_identity, "五题终态结果身份错绑")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), "五题终态结果摘要漂移")
    _require(value.get("raw_materials_destroyed") is True and value.get("contains_reversible_question_answer_or_evidence") is False, "五题终态保留了可还原原始内容")


def _validate_blind_plan(value: dict[str, Any], plan_identity: str) -> None:
    _require(value.get("schema") == BLIND_PLAN_SCHEMA and value.get("identity") == plan_identity, "五题计划 schema 或身份错绑")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(evidence.canonical_sha256(content) == plan_identity, "五题计划内容摘要漂移")
    _require(value.get("purpose") == "non-candidate-five-question-calibration" and value.get("candidate_decision") is None and value.get("formal") is False, "五题计划越过非候选校准边界")
    _require(isinstance(value.get("seed_sha256"), str) and evidence.is_sha256(value["seed_sha256"]), "五题计划 seed 摘要无效")
    dependencies = _mapping(value, "direct_dependencies")
    _require("controller" in dependencies and all(evidence.is_sha256(item) for item in dependencies.values()), "五题计划缺少控制器或直接依赖身份无效")


def _validate_blind_plan_against_current_sources(suite_root: Path, plan: dict[str, Any]) -> None:
    comparison = evidence.load_contract(suite_root)
    validation = load_validation_contract(suite_root)
    _require(plan.get("comparison_contract_identity") == comparison["identity"], "五题终态比较合同已漂移")
    _require(plan.get("validation_contract_identity") == validation["identity"], "五题终态验证合同已漂移")
    dependencies = _mapping(plan, "direct_dependencies")
    blind = _mapping(validation, "blind")
    implementation = _blind_implementation_identities()
    expected = {
        "controller": implementation["controller"],
        "generator": evidence.canonical_sha256({"settings": blind["generation"], "implementation": implementation["generator"]}),
        "quality-admission": evidence.canonical_sha256({"settings": blind["quality_admission"], "implementation": implementation["quality-admission"]}),
        "observer": evidence.canonical_sha256({"schema": BLIND_RESULT_SCHEMA, "implementation": implementation["observer"]}),
    }
    for name, identity in expected.items():
        _require(dependencies.get(name) == identity, f"五题终态 {name} 直接依赖已漂移")


def _load_blind_recovery(path: Path, plan_identity: str) -> dict[str, Any]:
    value = _load_json(path)
    _require(value.get("schema") == BLIND_RECOVERY_SCHEMA and value.get("plan_identity") == plan_identity, "五题活动恢复定位错绑")
    _require(set(value) == {"schema", "plan_identity", "execution_config", "formal_state", "scratch"}, "五题活动恢复定位字段越界")
    for name in ("execution_config", "formal_state", "scratch"):
        _require(isinstance(value.get(name), str) and Path(value[name]).is_absolute(), f"五题活动恢复 {name} 路径无效")
    return value


def _load_blind_secret(path: Path, plan_identity: str) -> dict[str, Any]:
    value = _load_json(path)
    _require(value.get("schema") == BLIND_SECRET_SCHEMA and value.get("plan_identity") == plan_identity, "五题恢复秘密错绑")
    _require(set(value) == {"schema", "plan_identity", "gate_seed", "seed_sha256"}, "五题恢复秘密字段越界")
    seed = value.get("gate_seed")
    _require(isinstance(seed, str) and len(seed) >= 16, "五题恢复 seed 缺失")
    _require(value.get("seed_sha256") == hashlib.sha256(seed.encode("utf-8")).hexdigest(), "五题恢复 seed 摘要漂移")
    return value


def _blind_dependencies(
    suite_root: Path,
    validation: dict[str, Any],
    runtime: dict[str, Any],
    subject: dict[str, Any],
    runtime_calibration: dict[str, Any],
) -> dict[str, str]:
    repository = suite_root.parents[2]
    long_root = repository / "benchmarks" / "longmemeval_s"
    protocol = runtime["protocol_value"]
    blind = _mapping(validation, "blind")
    implementation = _blind_implementation_identities()
    return {
        "comparison-contract": evidence.load_contract(suite_root)["identity"],
        "validation-contract": validation["identity"],
        "subject": subject["identity"],
        "runtime-calibration": runtime_calibration["runtime_calibration_identity"],
        "controller": implementation["controller"],
        "generator": evidence.canonical_sha256({"settings": blind["generation"], "implementation": implementation["generator"]}),
        "quality-admission": evidence.canonical_sha256({"settings": blind["quality_admission"], "implementation": implementation["quality-admission"]}),
        "executor": evidence.canonical_sha256({"run": evidence.file_sha256(long_root / "run.py"), "transport": evidence.file_sha256(long_root / "codex_app_server.py"), "protocol": evidence.file_sha256(runtime["protocol"])}),
        "observer": evidence.canonical_sha256({"schema": BLIND_RESULT_SCHEMA, "implementation": implementation["observer"]}),
        "environment": evidence.file_sha256(runtime["environment_manifest"]),
        "binary": evidence.file_sha256(runtime["binary"]),
        "embedding": evidence.file_sha256(runtime["embedding"] / "manifest.json"),
        "model-profile": evidence.canonical_sha256({"memory": protocol["memory"], "reader": protocol["reader"], "judge": protocol["judge"]}),
    }


def _blind_implementation_identities() -> dict[str, str]:
    roles = {
        "generator": (
            _generator_prompt, _generator_case_schema, _validate_generated_case, _derive_truth_claims,
            validate_materials, _materials_from_generated, _case_projection, _normalize,
        ),
        "quality-admission": (_admission_prompt, _admission_schema, validate_admission),
        "observer": (observe_report, evaluate_observation, score_controls, _sanitize_usage, _sanitize_codex),
        "controller": (
            calibrate_blind, resume_blind_by_plan_identity, derive_blind_budgets,
            _blind_terminal, _validate_blind_terminal_result, _validate_blind_plan,
            _validate_blind_plan_against_current_sources, _load_blind_recovery,
            _load_blind_secret, _blind_dependencies, _destroy_blind_scratch,
        ),
    }
    return {
        role: evidence.canonical_sha256({
            "schema": "ownward.kernel-iteration-blind-role-implementation/v1",
            "role": role,
            "sources": [inspect.getsource(callback) for callback in callbacks],
        })
        for role, callbacks in roles.items()
    }


def _native_codex_invoke(
    *,
    suite_root: Path,
    runtime: dict[str, Any],
    stage: Path,
    role: str,
    prompt: str,
    schema: dict[str, Any],
    settings: dict[str, Any],
    validate: Callable[[dict[str, Any]], Any] | None = None,
) -> tuple[dict[str, Any], dict[str, Any]]:
    module = _load_longmemeval_module(suite_root)
    command_prefix = module.CodexAppServer.direct_command_prefix(
        runtime["codex_binary"], module.codex_session.command_prefix(runtime["codex_binary"]),
    )
    transport_parent = stage / ".codex-runtime"

    def factory(_index: int, _generation: int) -> Any:
        runtime_root = module.isolated_runtime_root(transport_parent)
        environment = module.codex_session.isolated_environment(runtime["codex_auth_file"], runtime_root / "codex-home")
        return module.CodexAppServer(runtime["codex_binary"], runtime["codex_auth_file"], runtime_root, command_prefix, environment)

    started = time.perf_counter()
    with module.CodexAppServerPool(1, factory) as transport:
        capability = module.CodexCapability(transport)
        value, usage = capability._invoke(
            prompt=prompt,
            schema=schema,
            stage=stage / "checkpoint",
            model=settings["model"],
            effort=settings["reasoning_effort"],
            timeout_seconds=float(settings["timeout_seconds"]),
            attempts=int(settings["attempts"]),
            validate=validate,
        )
        diagnostics = transport.diagnostics()
    usage = {**usage, "role": role, "elapsed_seconds": time.perf_counter() - started, "transport": diagnostics}
    return value, usage


def _run_longmemeval(
    *,
    suite_root: Path,
    runtime: dict[str, Any],
    dataset_path: Path,
    output_dir: Path,
    subject_identity: str,
    resume: bool,
) -> dict[str, Any]:
    repository = suite_root.resolve().parents[2]
    adapter = Path(__file__).with_name("kernel_iteration_longmemeval.py")
    python = Path(str(_mapping(runtime["environment"], "layout")["python"])) / "Scripts" / "python.exe"
    arguments = [
        str(python), str(adapter), "run", "--non-formal",
        "--environment-manifest", str(runtime["environment_manifest"]),
        "--protocol", str(runtime["protocol"]),
        "--dataset", str(dataset_path),
        "--output-dir", str(output_dir),
        "--ownward-binary", str(runtime["binary"]),
        "--embedding-bundle-dir", str(runtime["embedding"]),
        "--codex-binary", str(runtime["codex_binary"]),
        "--codex-auth-file", str(runtime["codex_auth_file"]),
        "--candidate", f"kernel-iteration:{subject_identity}",
        "--environment-sha256", evidence.file_sha256(runtime["environment_manifest"]),
        "--input-manifest-sha256", evidence.file_sha256(dataset_path),
        "--tool-sha256", evidence.canonical_sha256({"validation": evidence.file_sha256(Path(__file__).resolve()), "adapter": evidence.file_sha256(adapter)}),
    ]
    if resume:
        arguments.append("--resume")
    completed = subprocess.run(
        arguments,
        cwd=repository,
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
        timeout=1800,
        check=False,
    )
    if completed.returncode != 0:
        raise KernelIterationValidationError(f"非正式端到端执行失败: {(completed.stderr or completed.stdout)[-2000:]}")
    report = _load_json(output_dir / "report.json")
    _require(report.get("formal") is False, "迭代证据误用了正式 LongMemEval-S 模式")
    return report


def _generator_prompt(validation: dict[str, Any], seed: str, case_id: str, coverage: str) -> str:
    blind = _mapping(validation, "blind")
    return (
        "Generate exactly one new synthetic memory-evaluation case for an internal, non-formal calibration. "
        "Do not use, imitate, retrieve, or refer to any LongMemEval-S question, answer, Gold ID, dataset content, candidate implementation, or candidate output. "
        "The seed only makes this interrupted run reproducible: " + seed + ". "
        "Use exactly case_id " + case_id + " and coverage " + coverage + ". "
        "Invent fresh people, organizations, dates, facts, relationships, conflicts, updates, and distractors. Each case must have at least "
        + str(blind["minimum_sessions_per_question"])
        + " sessions and at least " + str(blind["minimum_distractor_sessions_per_question"])
        + " relevant distractor sessions. Session IDs must be opaque case-local IDs such as c01-s01 and must not reveal the answer. "
        "Make each question require evidence-sensitive disambiguation, not a direct keyword copy: the question must paraphrase the requested fact and must not contain "
        "the answer or an answer-shaped phrase. Distractor sessions must reuse relevant entities or concepts while supporting a plausible but wrong alternative; unrelated filler is insufficient. "
        "For temporal-order, multi-session-relation, and multi-session-distractor, bind at least two jointly necessary facts across at least two answer sessions; neither answer session alone may support the complete answer. "
        "For single-session-assistant-fact, require both correct speaker attribution and disambiguation from a plausible user or assistant distractor. "
        "For temporal-order, make one answer session establish an event fact and another establish the ordering or time anchor. For multi-session-relation, make one session establish an entity/key relation and another map that key to the requested fact. "
        "For multi-session-distractor, use similarly worded competing records so that a second answer session is needed to select the right one. "
        "Every complete answer must appear verbatim somewhere in the declared answer evidence. The system derives immutable truth-claim bindings from those declared source turns; do not emit a duplicate truth_claims field. "
        "Removing necessary answer evidence, supplying only distractor evidence, supplying an outdated value, or changing the answer must each make the case incorrect. "
        "For knowledge-update-conflict, provide an older plausible stale session and a later answer session that explicitly supersedes it. Produce only the requested structured object."
    )


def _admission_prompt(validation: dict[str, Any], materials: dict[str, Any]) -> str:
    checks = _mapping(_mapping(validation, "blind"), "quality_admission")["required_checks"]
    return (
        "Act as an independent pre-output quality admission reviewer. You have no candidate implementation or candidate output. "
        "Reject weak cases; do not repair them. For every case, decide all required booleans: " + ", ".join(checks) + ". "
        "A case passes only when it is plausible, structurally non-trivial, has one uniquely supported answer, sufficient evidence, no answer-shaped surface shortcut, "
        "and can distinguish correct evidence/answers from all applicable controls: remove at least one necessary answer evidence item (or all answer evidence for a single-evidence case), "
        "use only declared distractor evidence, or keep correct evidence but change the answer. The stale-evidence control applies only to knowledge-update-conflict; do not fail other coverage labels merely because they have no stale evidence. "
        "Across the five-case batch, the controls must collectively cover known-correct, missing-evidence, wrong-evidence, stale-evidence, and wrong-answer. "
        "Relevant-entity distractors and paraphrased questions are required; unrelated filler or direct keyword-copy questions fail. "
        "Return only the requested structured object.\n\n"
        + json.dumps(materials, ensure_ascii=False, separators=(",", ":"))
    )


def _generator_case_schema(case_id: str, coverage: str, validation: dict[str, Any]) -> dict[str, Any]:
    blind = _mapping(validation, "blind")
    turn = {
        "type": "object", "additionalProperties": False, "required": ["role", "content"],
        "properties": {"role": {"type": "string", "enum": ["user", "assistant"]}, "content": {"type": "string", "minLength": 1, "maxLength": 800}},
    }
    session = {
        "type": "object", "additionalProperties": False, "required": ["session_id", "date", "turns"],
        "properties": {
            "session_id": {"type": "string", "pattern": "^c[0-9]{2}-s[0-9]{2}$"},
            "date": {"type": "string", "pattern": "^[0-9]{4}-[0-9]{2}-[0-9]{2}$"},
            "turns": {"type": "array", "minItems": 1, "maxItems": 6, "items": turn},
        },
    }
    case = {
        "type": "object", "additionalProperties": False,
        "required": ["case_id", "coverage", "question_type", "question_date", "question", "answer", "answer_session_ids", "stale_session_ids", "distractor_session_ids", "sessions"],
        "properties": {
            "case_id": {"type": "string", "enum": [case_id]},
            "coverage": {"type": "string", "enum": [coverage]},
            "question_type": {"type": "string", "enum": ["knowledge-update", "temporal-reasoning", "multi-session", "single-session-assistant"]},
            "question_date": {"type": "string", "pattern": "^[0-9]{4}-[0-9]{2}-[0-9]{2}$"},
            "question": {"type": "string", "minLength": 10, "maxLength": 500},
            "answer": {"type": "string", "minLength": 1, "maxLength": 300},
            "answer_session_ids": {"type": "array", "minItems": 1, "maxItems": 3, "items": {"type": "string"}},
            "stale_session_ids": {"type": "array", "maxItems": 3, "items": {"type": "string"}},
            "distractor_session_ids": {"type": "array", "minItems": int(blind["minimum_distractor_sessions_per_question"]), "maxItems": 5, "items": {"type": "string"}},
            "sessions": {"type": "array", "minItems": int(blind["minimum_sessions_per_question"]), "maxItems": 8, "items": session},
        },
    }
    return {"type": "object", "additionalProperties": False, "required": ["case"], "properties": {"case": case}}


def _validate_generated_case(output: dict[str, Any], case_id: str, coverage: str, validation: dict[str, Any]) -> dict[str, Any]:
    case = output.get("case")
    _require(isinstance(case, dict), "盲测生成器没有返回单题案例")
    case = dict(case)
    case["truth_claims"] = _derive_truth_claims(case)
    projected = _case_projection(case)
    _require(projected["case_id"] == case_id and projected["coverage"] == coverage, "盲测生成器案例身份或覆盖错绑")
    blind = _mapping(validation, "blind")
    _require(len(projected["sessions"]) >= int(blind["minimum_sessions_per_question"]), "盲测生成器会话不足冻结难度下限")
    _require(len(set(projected["distractor_session_ids"])) >= int(blind["minimum_distractor_sessions_per_question"]), "盲测生成器干扰证据不足冻结难度下限")
    content = {
        "schema": MATERIALS_SCHEMA,
        "contains_formal_questions_answers_gold_or_content": False,
        "cases": [projected],
        "criteria": {"minimum_accuracy": 0.0, "require_complete_fact_delivery": True, "category_minimums": {}},
    }
    validate_materials({**content, "identity": evidence.canonical_sha256(content)}, expected_questions=1)
    return projected


def _derive_truth_claims(case: dict[str, Any]) -> list[dict[str, Any]]:
    answer_ids = case.get("answer_session_ids")
    sessions = case.get("sessions")
    _require(isinstance(answer_ids, list) and answer_ids and isinstance(sessions, list), "盲测生成器缺少答案证据绑定")
    session_by_id = {session.get("session_id"): session for session in sessions if isinstance(session, dict)}
    claims: list[dict[str, Any]] = []
    for session_id in answer_ids:
        session = session_by_id.get(session_id)
        _require(isinstance(session, dict) and isinstance(session.get("turns"), list) and session["turns"], "盲测生成器答案证据身份错绑")
        contents = [turn.get("content") for turn in session["turns"] if isinstance(turn, dict) and isinstance(turn.get("content"), str) and turn["content"].strip()]
        _require(contents, "盲测生成器答案证据缺少正文")
        source = min(contents, key=len).strip()
        claims.append({"claim": source[:300].strip(), "evidence_session_ids": [session_id]})
    return claims


def _admission_schema(case_ids: list[str]) -> dict[str, Any]:
    checks = ["plausible", "difficulty_sufficient", "unique_answer", "evidence_sufficient", "no_surface_shortcut", "scoring_discriminative"]
    return {
        "type": "object", "additionalProperties": False, "required": ["assessments"],
        "properties": {
            "assessments": {
                "type": "array", "minItems": len(case_ids), "maxItems": len(case_ids),
                "items": {
                    "type": "object", "additionalProperties": False, "required": ["case_id", "checks"],
                    "properties": {
                        "case_id": {"type": "string", "enum": case_ids},
                        "checks": {
                            "type": "object", "additionalProperties": False, "required": checks,
                            "properties": {name: {"type": "boolean"} for name in checks},
                        },
                    },
                },
            },
        },
    }


def _materials_from_generated(output: dict[str, Any]) -> dict[str, Any]:
    cases = output.get("cases")
    _require(isinstance(cases, list), "盲测生成器没有返回案例")
    projected = [_case_projection(case) for case in cases]
    _require([case.get("coverage") for case in projected] == list(BLIND_COVERAGE), "盲测生成器没有按冻结配额原序输出")
    content = {
        "schema": MATERIALS_SCHEMA,
        "contains_formal_questions_answers_gold_or_content": False,
        "cases": projected,
        "criteria": {"minimum_accuracy": 0.0, "require_complete_fact_delivery": True, "category_minimums": {}},
    }
    return {**content, "identity": evidence.canonical_sha256(content)}


def _case_projection(case: dict[str, Any]) -> dict[str, Any]:
    required = (
        "case_id", "coverage", "question_type", "question_date", "question", "answer", "answer_session_ids",
        "stale_session_ids", "distractor_session_ids", "truth_claims", "sessions",
    )
    _require(isinstance(case, dict) and set(required) <= set(case), "非正式案例字段不完整")
    return {name: case[name] for name in required}


def _case_fact_identity(case: dict[str, Any]) -> str:
    """Identity for overlap rejection; excludes labels but includes every authored fact."""
    return evidence.canonical_sha256({
        "question": _normalize(str(case["question"])),
        "answer": _normalize(str(case["answer"])),
        "claims": sorted(_normalize(str(item["claim"])) for item in case["truth_claims"]),
        "sessions": [
            {
                "date": session["date"],
                "turns": [{"role": turn["role"], "content": _normalize(turn["content"])} for turn in session["turns"]],
            }
            for session in case["sessions"]
        ],
    })


def _longmemeval_case(case: dict[str, Any]) -> dict[str, Any]:
    sessions = case["sessions"]
    return {
        "question_id": case["case_id"],
        "question_type": case["question_type"],
        "question": case["question"],
        "question_date": case["question_date"],
        "answer": case["answer"],
        "answer_session_ids": case["answer_session_ids"],
        "haystack_dates": [session["date"] for session in sessions],
        "haystack_session_ids": [session["session_id"] for session in sessions],
        "haystack_sessions": [session["turns"] for session in sessions],
    }


def _mechanical_answer_score(case: dict[str, Any], delivered_ids: list[str], answer: str) -> bool:
    return set(case["answer_session_ids"]) <= set(delivered_ids) and _normalize(answer) == _normalize(case["answer"])


def _verify_execution_manifest_dependencies(
    comparison: dict[str, Any],
    manifest: dict[str, Any],
    evidence_type: str,
    materials: dict[str, Any],
    identities: dict[str, str],
) -> None:
    shared = _mapping(manifest, "shared_conditions")
    expected_shared = {
        "dataset": materials["identity"], "environment": identities["environment"], "executor": identities["executor"],
        "model-profile": identities["model-profile"], "observer": identities["observer"],
        "prompt-and-schema": identities["prompt-and-schema"], "scorer": identities["scorer"],
    }
    _require(shared == dict(sorted(expected_shared.items())), "执行输入的共享条件与真实实现不一致")
    direct = _mapping(manifest, "direct_dependencies")
    material_role = f"{evidence_type}-materials" if evidence_type != "integrated" else "integrated-plan"
    expected_direct = {material_role: materials["identity"], "executor": identities["executor"], "observer": identities["observer"]}
    _require(direct == dict(sorted(expected_direct.items())), "执行输入直接依赖与真实材料或实现不一致")
    required = set(_mapping(_mapping(comparison, "evidence_types"), evidence_type)["required_dependencies"])
    _require(set(direct) == required, "执行输入直接依赖角色与比较合同不一致")


def _verify_subject_binary(subject: dict[str, Any], binary: Path, *, allow_current_product: bool = False) -> None:
    digest = evidence.file_sha256(binary)
    content = subject["content"]
    expected = content.get("binary_identity")
    if expected is None and isinstance(content.get("artifacts"), dict):
        expected = content["artifacts"].get("binary")
    if expected is None and allow_current_product:
        return
    _require(expected == digest, "subject 身份与实际产品二进制错绑")


def _verify_current_product_binary(suite_root: Path, binary: Path) -> None:
    expected = _mapping(load_stage3_contract(suite_root), "current_product_artifact")["binary_sha256"]
    _require(evidence.file_sha256(binary) == expected, "当前产品诊断二进制与冻结 V1 制品错绑")


def _verify_runtime_binary_binding(state_path: Path, binary: Path) -> None:
    state = _load_json(state_path)
    binding = _mapping(state, "binding")
    components = _mapping(binding, "components")
    _require(_mapping(components, "binary").get("identity") == evidence.file_sha256(binary), "校准载体二进制与活动 binding 错绑")


def _load_longmemeval_module(suite_root: Path) -> Any:
    path = suite_root.resolve().parents[1] / "longmemeval_s" / "run.py"
    root = path.parent
    if str(root) not in sys.path:
        sys.path.insert(0, str(root))
    name = "ownward_kernel_iteration_longmemeval"
    if name in sys.modules:
        return sys.modules[name]
    spec = importlib.util.spec_from_file_location(name, path)
    _require(spec is not None and spec.loader is not None, "无法加载 LongMemEval-S 非正式执行器")
    module = importlib.util.module_from_spec(spec)
    sys.modules[name] = module
    spec.loader.exec_module(module)
    return module


def _destroy_blind_scratch(path: Path, runs_root: Path) -> None:
    path = path.resolve()
    runs_root = runs_root.resolve()
    _require(path.is_relative_to(runs_root / "kernel-v2-blind-calibration"), "拒绝清理盲测临时根之外的目录")
    if path.exists():
        shutil.rmtree(path)


def _sanitize_usage(value: dict[str, Any]) -> dict[str, Any]:
    allowed = ("input_tokens", "cached_input_tokens", "output_tokens", "reasoning_output_tokens", "calls", "attempts", "retries", "rate_limit_events", "interrupted_attempts", "wall_seconds", "elapsed_seconds")
    return {name: value.get(name, 0) for name in allowed}


def _combine_usages(values: list[dict[str, Any]]) -> dict[str, Any]:
    allowed = ("input_tokens", "cached_input_tokens", "output_tokens", "reasoning_output_tokens", "calls", "attempts", "retries", "rate_limit_events", "interrupted_attempts", "wall_seconds", "elapsed_seconds")
    return {name: sum(float(value.get(name, 0)) for value in values) for name in allowed}


def _sanitize_codex(value: Any) -> dict[str, Any]:
    if not isinstance(value, dict):
        return {}
    allowed = ("calls", "attempts", "retries", "rate_limit_events", "interrupted_attempts", "wall_seconds")
    result = {name: value.get(name, 0) for name in allowed}
    scheduler = value.get("scheduler")
    transport = value.get("transport")
    if isinstance(scheduler, dict):
        result["scheduler"] = {name: scheduler.get(name) for name in ("limit", "max_active", "submitted")}
    if isinstance(transport, dict):
        result["transport"] = {name: transport.get(name) for name in ("pool_size", "max_active", "worker_restarts", "rate_limit_observed", "process_starts")}
    return result


def _category_accuracy(categories: dict[str, Any], name: str) -> float | None:
    value = categories.get(name)
    return float(value["accuracy"]) if isinstance(value, dict) and isinstance(value.get("accuracy"), (int, float)) else None


def _normalize(value: str) -> str:
    return " ".join(value.casefold().split())


def _load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise KernelIterationValidationError(f"无法读取 JSON: {path}") from error
    _require(isinstance(value, dict), f"{path} 必须是 JSON 对象")
    return value


def _mapping(value: dict[str, Any], name: str) -> dict[str, Any]:
    item = value.get(name)
    _require(isinstance(item, dict), f"{name} 必须是对象")
    return item


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise KernelIterationValidationError(message)
