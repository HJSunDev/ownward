from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import kernel_iteration_evidence as evidence
import kernel_iteration_stage4 as stage4
import kernel_iteration_stage4_performance as performance_validation
import kernel_iteration_stage4_protection_performance as protection_performance_validation
import kernel_iteration_validation as validation


CONTRACT_SCHEMA = "ownward.kernel-iteration-stage4-multisource-contract/v1"
DIAGNOSIS_SCHEMA = "ownward.kernel-iteration-stage4-multisource-diagnosis/v1"
ROUTE_SCHEMA = "ownward.kernel-iteration-stage4-multisource-route/v1"
FINAL_SCHEMA = "ownward.kernel-iteration-stage4-multisource-evidence/v1"


def load_contract(suite_root: Path) -> dict[str, Any]:
    path = suite_root.resolve() / "iteration" / "v2" / "stage4-multisource-contract.json"
    value = _load_json(path)
    _validate_identity(value, CONTRACT_SCHEMA, "多来源问题链合同")
    _require(value.get("frozen_before_current_candidate_multisource_results") is True, "多来源材料与门槛没有在诊断结果前冻结")
    _require(value.get("route_must_be_frozen_after_mechanical_diagnosis_and_before_new_candidate_results") is True, "多来源实现路线冻结顺序无效")
    _require(value.get("stage4_may_complete") is False and value.get("blind_gate_allowed") is False, "多来源合同越过阶段 4 边界")
    materials = validation.validate_stage3_materials(
        _load_json(suite_root.resolve() / "iteration" / "v2" / "stage4-multisource-materials.json"),
        expected_questions=int(value["gates"]["multisource_development"]["questions"]),
    )
    manifest = _load_json(suite_root.resolve() / "iteration" / "v2" / "stage4-multisource-input.json")
    _require(materials["identity"] == value["sources"]["materials"], "多来源材料身份错绑")
    _require(manifest.get("identity") == value["sources"]["input"], "多来源完整输入身份错绑")
    _require(manifest.get("shared_conditions", {}).get("dataset") == materials["identity"], "多来源输入没有绑定材料")
    return {**value, "materials": materials, "input": manifest}


def load_route(suite_root: Path) -> dict[str, Any]:
    contract = load_contract(suite_root)
    path = suite_root.resolve() / "iteration" / "v2" / "stage4-multisource-route.json"
    value = _load_json(path)
    _validate_identity(value, ROUTE_SCHEMA, "多来源实现路线")
    _require(value.get("frozen_before_new_candidate_results") is True, "多来源实现路线没有在新候选结果前冻结")
    _require(value.get("contract_identity") == contract["identity"], "多来源实现路线与冻结合同错绑")
    _require(value.get("root_status") == "proven", "多来源实现路线缺少可证根因")
    _require(
        value.get("first_proven_mechanism") == "source-depth-consumed-read-budget-before-target-rank",
        "多来源实现路线没有绑定首个机械偏离点",
    )
    route = value.get("route")
    _require(isinstance(route, dict) and route.get("policy") == "bounded-source-breadth-before-repeated-depth/v1", "多来源候选策略错绑")
    for field in (
        "uses_formal_question_answer_gold_type_or_score", "increases_search_limit", "increases_read_limit",
        "increases_context_budget", "increases_persistent_state", "changes_model_prompt_schema_or_scoring",
    ):
        _require(route.get(field) is False, f"多来源实现路线越过冻结边界: {field}")
    _require(value.get("stage4_may_complete") is False, "多来源实现路线提前关闭阶段 4")
    return value


def diagnose(
    suite_root: Path,
    output_root: Path,
    subject_manifest: Path,
    execution_result: Path,
    formal_state: Path,
    *,
    resume: bool = False,
) -> dict[str, Any]:
    contract = load_contract(suite_root)
    subject = evidence.validate_v2_subject(evidence.load_contract(suite_root), _load_json(subject_manifest.resolve()))
    _require(subject["identity"] == contract["sources"]["first_candidate_subject"], "诊断 subject 不是首个独立 V2 候选")
    result_path = execution_result.resolve()
    result = validation._load_execution_result(result_path)
    _require(result.get("subject_identity") == subject["identity"] and result.get("subject_role") == "v2-candidate", "多来源诊断结果与首候选错绑")
    _require(result.get("evidence_type") == "development" and result.get("input_identity") == contract["sources"]["input"], "多来源诊断结果输入错绑")
    cases = result.get("observation", {}).get("case_evidence")
    _require(isinstance(cases, list) and len(cases) == contract["gates"]["multisource_development"]["questions"], "多来源诊断缺少逐例证据")
    mechanisms: dict[str, int] = {}
    qualifying = 0
    projections: list[dict[str, Any]] = []
    for item in cases:
        _require(isinstance(item, dict) and isinstance(item.get("selection"), dict), "多来源逐例缺少选择链证据")
        expected = int(item.get("expected_sources", 0))
        returned = int(item.get("search_returned_sources", 0))
        read = int(item.get("read_sources", 0))
        mechanism = str(item.get("first_proven_mechanism", ""))
        if expected > 0 and returned == expected and read < expected:
            qualifying += 1
            mechanisms[mechanism] = mechanisms.get(mechanism, 0) + 1
        selection = item["selection"]
        projections.append({
            "case_identity": item["case_identity"],
            "coverage": item["coverage"],
            "correct": item["correct"],
            "expected_sources": expected,
            "search_returned_sources": returned,
            "read_sources": read,
            "delivered_truth_claims": item["delivered_truth_claims"],
            "truth_claims": item["truth_claims"],
            "first_proven_mechanism": mechanism,
            "selected_units": selection["selected_units"],
            "selected_sources": selection["selected_sources"],
            "selected_depth_units": selection["selected_depth_units"],
            "selected_nonrequired_sources": selection["selected_nonrequired_sources"],
            "expected_source_paths": selection["expected_sources"],
            "context_chars": selection["context_chars"],
        })
    ranked = sorted(mechanisms.items(), key=lambda item: (-item[1], item[0]))
    first_mechanism = ranked[0][0] if ranked else None
    state_path = formal_state.resolve()
    state_sha = evidence.file_sha256(state_path)
    _require(state_sha == contract["sources"]["formal_state_sha256"], "多来源诊断期间正式 state 漂移")
    content = {
        "schema": DIAGNOSIS_SCHEMA,
        "contract_identity": contract["identity"],
        "subject_identity": subject["identity"],
        "execution_result_identity": result["identity"],
        "execution_result_sha256": evidence.file_sha256(result_path),
        "formal": False,
        "formal_state_written": False,
        "formal_state_sha256": state_sha,
        "root_status": "proven" if qualifying > 0 and first_mechanism is not None else "not-proven",
        "first_proven_mechanism": first_mechanism,
        "qualifying_cases": qualifying,
        "mechanism_distribution": dict(ranked),
        "cases": projections,
        "route_frozen": False,
        "candidate_decision": None,
    }
    value = {**content, "identity": evidence.canonical_sha256(content)}
    destination = output_root.resolve() / "stage4-multisource" / "diagnosis" / subject["identity"] / "result.json"
    if destination.is_file():
        _require(resume and _load_json(destination) == value, "多来源诊断终态已存在或身份漂移")
        return {**value, "reused": True, "path": str(destination)}
    evidence.atomic_json(destination, value)
    _require(evidence.file_sha256(state_path) == state_sha, "多来源诊断改写了正式 state")
    return {**value, "reused": False, "path": str(destination)}


def finalize(
    suite_root: Path,
    output_root: Path,
    subject_manifest: Path,
    execution_config: Path,
    multisource_result_path: Path,
    development_result_path: Path,
    regression_result_path: Path,
    multisource_performance_path: Path,
    protection_performance_path: Path,
    formal_state: Path,
    *,
    resume: bool = False,
) -> dict[str, Any]:
    contract = load_contract(suite_root)
    route = load_route(suite_root)
    comparison = evidence.load_contract(suite_root)
    subject_path = subject_manifest.resolve()
    subject = evidence.validate_v2_subject(comparison, _load_json(subject_path))
    runtime = validation.validate_execution_config(suite_root, execution_config.resolve())
    _require(evidence.file_sha256(runtime["binary"]) == subject["content"]["artifacts"]["binary"], "多来源终态二进制与 subject 错绑")
    destination = output_root.resolve() / "stage4-multisource" / subject["identity"] / "result.json"
    if destination.is_file():
        _require(resume, "多来源终态已经存在；必须显式 resume")
        value = _load_json(destination)
        _validate_identity(value, FINAL_SCHEMA, "多来源终态")
        _require(value.get("subject_identity") == subject["identity"] and value.get("route_identity") == route["identity"], "多来源终态身份漂移")
        _require(value.get("controller_sha256") == evidence.file_sha256(Path(__file__).resolve()), "多来源终态控制器漂移")
        return {**value, "reused": True, "path": str(destination)}

    multisource = validation._load_execution_result(multisource_result_path.resolve())
    development = validation._load_execution_result(development_result_path.resolve())
    regression = validation._load_execution_result(regression_result_path.resolve())
    for name, result in (("multisource", multisource), ("development", development), ("regression", regression)):
        _require(result.get("subject_identity") == subject["identity"] and result.get("subject_role") == "v2-candidate", f"{name} 结果与终态候选错绑")
        _require(result.get("passed") is True and result.get("candidate_decision") is True, f"{name} 质量绝对门未通过")
    _require(multisource.get("input_identity") == contract["sources"]["input"], "多来源结果输入错绑")
    _require(development.get("input_identity") == _load_json(suite_root / "iteration" / "v2" / "stage4-multisource-development-input.json")["identity"], "原开发结果输入错绑")
    _require(regression.get("input_identity") == _load_json(suite_root / "iteration" / "v2" / "stage4-multisource-regression-input.json")["identity"], "固定回归结果输入错绑")

    multi_observation = multisource["observation"]
    multi_gate = contract["gates"]["multisource_development"]
    multi_cases = multi_observation["case_evidence"]
    _require(
        multi_observation["questions"] == multi_gate["questions"]
        and multi_observation["final_answer_accuracy"] == multi_gate["accuracy"]
        and multi_observation["fact_delivery"]["complete"] is True
        and all(int(item["search_returned_sources"]) == int(item["expected_sources"]) for item in multi_cases)
        and all(int(item["read_sources"]) == int(item["expected_sources"]) for item in multi_cases)
        and all(int(item["delivered_truth_claims"]) == int(item["truth_claims"]) for item in multi_cases),
        "多来源质量或必要事实交付没有达到冻结门槛",
    )
    cost = contract["gates"]["cost_and_isolation"]
    _require(all(int(item["selection"]["selected_units"]) <= int(cost["read_units_maximum"]) for item in multi_cases), "多来源读取数量越界")
    _require(all(int(item["context_chars"]) <= int(cost["context_chars_maximum"]) for item in multi_cases), "多来源上下文预算越界")

    development_gate = contract["gates"]["first_chain_protection"]
    development_observation = development["observation"]
    long_case = next((item for item in development_observation["case_evidence"] if item.get("coverage") == "long-session-multi-fact"), None)
    _require(
        development_observation["questions"] == development_gate["development_questions"]
        and development_observation["final_answer_accuracy"] == development_gate["development_accuracy"]
        and development_observation["fact_delivery"]["complete"] is True
        and isinstance(long_case, dict)
        and long_case["delivered_truth_claims"] == development_gate["long_multifact_delivered_claims"]
        and long_case["truth_claims"] == development_gate["long_multifact_truth_claims"],
        "首问题链保护没有达到冻结门槛",
    )
    regression_gate = contract["gates"]["fixed_regression"]
    regression_observation = regression["observation"]
    _require(
        regression_observation["questions"] == regression_gate["questions"]
        and regression_observation["final_answer_accuracy"] == regression_gate["accuracy"]
        and regression_observation["fact_delivery"]["complete"] is True,
        "固定回归没有达到冻结门槛",
    )
    _require(float(regression_observation["latency"]["retrieval_p95_ms"]) <= float(regression_gate["retrieval_p95_ms_repeat_ceiling"]), "固定回归 p95 越界")
    protected = [item for item in development_observation["case_evidence"] if item.get("coverage") in {"long-session-multi-fact", "temporal-update-conflict", "structured-boundary"}]
    expected = sum(int(item["expected_sources"]) for item in protected)
    returned = sum(min(int(item["search_returned_sources"]), int(item["expected_sources"])) for item in protected)
    recall = returned / expected if expected else 0.0
    _require(recall == contract["gates"]["v1_protection"]["long_asset_semantic_recall"], "长资产语义召回退化")

    performance = _load_performance(multisource_performance_path.resolve(), "ownward.kernel-iteration-stage4-multisource-performance/v1")
    protection = _load_performance(protection_performance_path.resolve(), "ownward.kernel-iteration-stage4-protection-performance/v1")
    _require(performance.get("contract_identity") == performance_validation.load_contract(suite_root)["identity"], "多来源成对性能合同漂移")
    _require(protection.get("contract_identity") == protection_performance_validation.load_contract(suite_root)["identity"], "保护成对性能合同漂移")
    _require(performance.get("sealed_results", {}).get("candidate") == multisource["identity"], "多来源成对性能没有绑定当前质量结果")
    _require(protection.get("sealed_results", {}).get("candidate") == development["identity"], "保护成对性能没有绑定当前开发结果")
    for name, report in (("multisource", performance), ("protection", protection)):
        _require(report.get("passed") is True and report.get("subjects", {}).get("candidate") == subject["identity"], f"{name} 成对性能复核未通过或错绑")
        _require(report["metrics"]["candidate_minus_baseline_p95_ms"] <= report["metrics"]["candidate_p95_delta_max_ms"], f"{name} 超出冻结重复误差")
        _require(report.get("model_calls") == 0 and report.get("product_mutations") == 0, f"{name} 成对性能复核越过只读边界")

    state_path = formal_state.resolve()
    state_before = evidence.file_sha256(state_path)
    _require(state_before == contract["sources"]["formal_state_sha256"], "多来源终态前正式 state 漂移")
    inputs = suite_root / "iteration" / "v2"
    resume_proofs = {
        "multisource": stage4._prove_resume(suite_root, output_root, subject_path, execution_config.resolve(), "development", inputs / "stage4-multisource-input.json", multisource_result_path.resolve(), multisource),
        "development": stage4._prove_resume(suite_root, output_root, subject_path, execution_config.resolve(), "development", inputs / "stage4-multisource-development-input.json", development_result_path.resolve(), development),
        "regression": stage4._prove_resume(suite_root, output_root, subject_path, execution_config.resolve(), "regression", inputs / "stage4-multisource-regression-input.json", regression_result_path.resolve(), regression),
    }
    state_after = evidence.file_sha256(state_path)
    _require(state_after == state_before, "多来源终态改写了正式 state")
    wall = sum(float(item["observation"]["latency"]["wall_seconds"]) for item in (multisource, development, regression))
    _require(wall <= float(cost["affected_feedback_hard_seconds"]), "受影响反馈超过 600 秒")
    content = {
        "schema": FINAL_SCHEMA,
        "formal": False,
        "formal_state_written": False,
        "contract_identity": contract["identity"],
        "route_identity": route["identity"],
        "controller_sha256": evidence.file_sha256(Path(__file__).resolve()),
        "subject_identity": subject["identity"],
        "kernel_generation_identity": subject["content"]["kernel_generation_identity"],
        "kernel_effect_identity": subject["content"]["kernel_effect_identity"],
        "results": {"multisource": multisource["identity"], "development": development["identity"], "regression": regression["identity"]},
        "performance": {"multisource": performance["identity"], "protection": protection["identity"]},
        "metrics": {
            "multisource_accuracy": multi_observation["final_answer_accuracy"],
            "multisource_fact_delivery_complete": multi_observation["fact_delivery"]["complete"],
            "multisource_truth_claims": sum(int(item["truth_claims"]) for item in multi_cases),
            "multisource_delivered_truth_claims": sum(int(item["delivered_truth_claims"]) for item in multi_cases),
            "development_accuracy": development_observation["final_answer_accuracy"],
            "long_multifact_delivered_claims": long_case["delivered_truth_claims"],
            "long_multifact_truth_claims": long_case["truth_claims"],
            "regression_accuracy": regression_observation["final_answer_accuracy"],
            "long_asset_semantic_recall": recall,
            "multisource_retrieval_p95_ms_raw": multi_observation["latency"]["retrieval_p95_ms"],
            "development_retrieval_p95_ms_raw": development_observation["latency"]["retrieval_p95_ms"],
            "regression_retrieval_p95_ms": regression_observation["latency"]["retrieval_p95_ms"],
            "paired_multisource_p95_delta_ms": performance["metrics"]["candidate_minus_baseline_p95_ms"],
            "paired_protection_p95_delta_ms": protection["metrics"]["candidate_minus_baseline_p95_ms"],
            "affected_execution_wall_seconds": wall,
            "read_units_maximum": max(int(item["selection"]["selected_units"]) for item in multi_cases),
            "context_chars_maximum": max(int(item["context_chars"]) for item in multi_cases),
            "persistent_state_growth_bytes": 0,
        },
        "resume": resume_proofs,
        "formal_state_sha256_before": state_before,
        "formal_state_sha256_after": state_after,
        "current_product_or_baseline_switched": False,
        "stage4_complete": False,
        "blind_gate_run": False,
        "passed": True,
    }
    value = {**content, "identity": evidence.canonical_sha256(content)}
    evidence.atomic_json(destination, value)
    return {**value, "reused": False, "path": str(destination)}


def _load_performance(path: Path, schema: str) -> dict[str, Any]:
    value = _load_json(path)
    _validate_identity(value, schema, "成对性能报告")
    return value


def _validate_identity(value: dict[str, Any], schema: str, name: str) -> None:
    _require(value.get("schema") == schema, f"{name} schema 无效")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), f"{name}身份漂移")


def _load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise validation.KernelIterationValidationError(f"无法读取多来源制品 {path}: {error}") from error
    _require(isinstance(value, dict), f"多来源制品不是对象: {path}")
    return value


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise validation.KernelIterationValidationError(message)
