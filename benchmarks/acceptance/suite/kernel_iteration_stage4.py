from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import kernel_iteration_candidate as candidate
import kernel_iteration_evidence as evidence
import kernel_iteration_validation as validation


STAGE4_CONTRACT_SCHEMA = "ownward.kernel-iteration-stage4-contract/v1"
STAGE4_EVIDENCE_SCHEMA = "ownward.kernel-iteration-stage4-evidence/v1"


def finalize(
    suite_root: Path,
    output_root: Path,
    subject_manifest: Path,
    execution_config: Path,
    development_input: Path,
    regression_input: Path,
    development_result: Path,
    regression_result: Path,
    formal_state: Path,
    *,
    resume: bool = False,
) -> dict[str, Any]:
    repository = suite_root.resolve().parents[2]
    output_root = output_root.resolve()
    evidence._validate_output_boundary(repository, output_root)
    contract = load_contract(suite_root)
    subject_path = subject_manifest.resolve()
    subject = evidence.validate_v2_subject(evidence.load_contract(suite_root), _load_json(subject_path))
    receipt = candidate.prepare(suite_root, subject_path.parent, execution_config.resolve(), resume=True)
    _require(receipt["subject_identity"] == subject["identity"], "V2 候选制品与 subject 错绑")

    destination = output_root / "stage4" / subject["identity"] / "result.json"
    if destination.exists():
        _require(resume, "阶段 4 候选证据已经存在；必须显式 resume")
        value = _load_json(destination)
        _validate_identity(value, STAGE4_EVIDENCE_SCHEMA, "阶段 4 候选证据")
        _require(value.get("contract_identity") == contract["identity"] and value.get("subject_identity") == subject["identity"], "阶段 4 已有证据错绑")
        return {**value, "reused": True, "path": str(destination)}

    development = validation._load_execution_result(development_result.resolve())
    regression = validation._load_execution_result(regression_result.resolve())
    metrics = validate_results(contract, subject, development, regression)
    state_path = formal_state.resolve()
    _require(state_path.is_file(), "阶段 4 缺少正式 state 只读基线")
    state_before = evidence.file_sha256(state_path)
    resume_proofs = {
        "development": _prove_resume(
            suite_root, output_root, subject_path, execution_config.resolve(), "development",
            development_input.resolve(), development_result.resolve(), development,
        ),
        "regression": _prove_resume(
            suite_root, output_root, subject_path, execution_config.resolve(), "regression",
            regression_input.resolve(), regression_result.resolve(), regression,
        ),
    }
    state_after = evidence.file_sha256(state_path)
    _require(state_before == state_after, "阶段 4 非正式证据修改了正式 state")
    content = {
        "schema": STAGE4_EVIDENCE_SCHEMA,
        "formal": False,
        "formal_state_written": False,
        "contract_identity": contract["identity"],
        "comparison_contract_identity": evidence.load_contract(suite_root)["identity"],
        "subject_identity": subject["identity"],
        "kernel_generation_identity": subject["content"]["kernel_generation_identity"],
        "kernel_effect_identity": subject["content"]["kernel_effect_identity"],
        "candidate_receipt_identity": receipt["identity"],
        "results": {
            "development": development["identity"],
            "regression": regression["identity"],
        },
        "metrics": metrics,
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


def load_contract(suite_root: Path) -> dict[str, Any]:
    path = suite_root.resolve() / "iteration" / "v2" / "stage4-contract.json"
    value = _load_json(path)
    _validate_identity(value, STAGE4_CONTRACT_SCHEMA, "阶段 4 合同")
    _require(value.get("gates_frozen_before_any_candidate_quality_results") is True, "阶段 4 门槛未在任何候选质量结果前冻结")
    _require(value.get("current_route_frozen_before_current_route_quality_results") is True, "阶段 4 当前路线未在本路线质量结果前冻结")
    _require(value.get("stage4_may_complete") is False and value.get("blind_gate_allowed") is False, "阶段 4 合同越过当前工作包边界")
    return value


def validate_results(
    contract: dict[str, Any],
    subject: dict[str, Any],
    development: dict[str, Any],
    regression: dict[str, Any],
) -> dict[str, Any]:
    for name, result in (("development", development), ("regression", regression)):
        _require(result.get("subject_identity") == subject["identity"] and result.get("subject_role") == "v2-candidate", f"{name} 结果没有绑定 V2 候选")
        _require(result.get("evidence_type") == name and result.get("passed") is True and result.get("candidate_decision") is True, f"{name} 结果未通过绝对门")
    development_gate = contract["gates"]["development"]
    regression_gate = contract["gates"]["regression"]
    development_observation = development["observation"]
    regression_observation = regression["observation"]
    long_case = next((item for item in development_observation["case_evidence"] if item.get("coverage") == "long-session-multi-fact"), None)
    _require(isinstance(long_case, dict), "开发结果缺少长多事实案例")
    _require(
        development_observation.get("questions") == development_gate["questions"]
        and development_observation.get("final_answer_accuracy") == development_gate["accuracy"]
        and development_observation["fact_delivery"].get("complete") is development_gate["complete_fact_delivery"]
        and long_case.get("truth_claims") == development_gate["long_multifact_truth_claims"]
        and long_case.get("delivered_truth_claims") == development_gate["long_multifact_delivered_claims"],
        "开发集没有达到冻结的完整片段交付门槛",
    )
    _require(
        regression_observation.get("questions") == regression_gate["questions"]
        and regression_observation.get("final_answer_accuracy") == regression_gate["accuracy"]
        and regression_observation["fact_delivery"].get("complete") is regression_gate["complete_fact_delivery"],
        "固定回归没有达到冻结保护门槛",
    )
    protected_coverages = {"long-session-multi-fact", "temporal-update-conflict", "structured-boundary"}
    protected = [item for item in development_observation["case_evidence"] if item.get("coverage") in protected_coverages]
    _require({item["coverage"] for item in protected} == protected_coverages, "长资产语义召回保护案例不完整")
    expected = sum(int(item["expected_sources"]) for item in protected)
    returned = sum(min(int(item["search_returned_sources"]), int(item["expected_sources"])) for item in protected)
    long_recall = returned / expected if expected else 0.0
    cost = contract["gates"]["cost_and_isolation"]
    _require(long_recall >= contract["gates"]["v1_protection"]["long_asset_semantic_recall"], "长资产语义召回保护退化")
    _require(float(development_observation["latency"]["retrieval_p95_ms"]) <= float(cost["development_retrieval_p95_same_scale_max_ms"]), "开发检索 p95 越过冻结边界")
    _require(float(regression_observation["latency"]["retrieval_p95_ms"]) <= float(cost["regression_retrieval_p95_same_scale_max_ms"]), "回归检索 p95 越过冻结边界")
    wall = float(development_observation["latency"]["wall_seconds"]) + float(regression_observation["latency"]["wall_seconds"])
    _require(wall <= float(cost["affected_feedback_hard_seconds"]), "受影响开发与回归反馈超过 600 秒")
    return {
        "development_accuracy": development_observation["final_answer_accuracy"],
        "development_fact_delivery_complete": development_observation["fact_delivery"]["complete"],
        "long_multifact_delivered_claims": long_case["delivered_truth_claims"],
        "long_multifact_truth_claims": long_case["truth_claims"],
        "regression_accuracy": regression_observation["final_answer_accuracy"],
        "regression_fact_delivery_complete": regression_observation["fact_delivery"]["complete"],
        "long_asset_semantic_recall": long_recall,
        "development_retrieval_p95_ms": development_observation["latency"]["retrieval_p95_ms"],
        "regression_retrieval_p95_ms": regression_observation["latency"]["retrieval_p95_ms"],
        "affected_feedback_wall_seconds": wall,
        "persistent_state_growth_bytes": cost["persistent_state_growth_bytes"],
    }


def _prove_resume(
    suite_root: Path,
    output_root: Path,
    subject_manifest: Path,
    execution_config: Path,
    evidence_type: str,
    input_manifest: Path,
    expected_result_path: Path,
    expected_result: dict[str, Any],
) -> dict[str, Any]:
    plan_root = expected_result_path.parent
    _require(plan_root.is_dir(), f"{evidence_type} 缺少已完成检查点")
    before_files = _tree_files(plan_root)
    immutable_before = _immutable_tree_identity(before_files)
    resumed = validation.execute_prepared_evidence(
        suite_root,
        output_root,
        execution_config,
        subject_manifest=subject_manifest,
        evidence_type=evidence_type,
        input_manifest=input_manifest,
        resume=True,
    )
    _require(resumed.get("reused_execution") is True and Path(resumed["execution_result"]).resolve() == expected_result_path, f"{evidence_type} resume 重做了模型或产品执行")
    after_files = _tree_files(plan_root)
    _require(all(after_files.get(path) == digest for path, digest in before_files.items()), f"{evidence_type} resume 改写了已封存检查点")
    _require(set(after_files) - set(before_files) <= {"execution-resume.json"}, f"{evidence_type} resume 产生了未声明制品")
    receipt = _load_json(plan_root / "execution-resume.json")
    _validate_identity(receipt, "ownward.kernel-iteration-execution-resume/v1", f"{evidence_type} resume 收据")
    _require(receipt.get("reused_execution") is True and receipt.get("model_or_product_execution") is False, f"{evidence_type} resume 收据没有证明零执行")
    stable_before = _tree_identity(plan_root)
    resumed_again = validation.execute_prepared_evidence(
        suite_root,
        output_root,
        execution_config,
        subject_manifest=subject_manifest,
        evidence_type=evidence_type,
        input_manifest=input_manifest,
        resume=True,
    )
    _require(resumed_again.get("reused_execution") is True, f"{evidence_type} 重复 resume 没有复用")
    stable_after = _tree_identity(plan_root)
    _require(stable_before == stable_after, f"{evidence_type} 重复 resume 不是逐字幂等")
    immutable_after = _immutable_tree_identity(_tree_files(plan_root))
    _require(immutable_before == immutable_after, f"{evidence_type} resume 改写了不可变结果或检查点")
    _require(validation._load_execution_result(expected_result_path)["identity"] == expected_result["identity"], f"{evidence_type} resume 结果身份漂移")
    return {
        "plan_identity": expected_result["plan_identity"],
        "result_identity": expected_result["identity"],
        "immutable_checkpoint_tree_sha256_before": immutable_before,
        "immutable_checkpoint_tree_sha256_after": immutable_after,
        "stable_resume_tree_sha256_before": stable_before,
        "stable_resume_tree_sha256_after": stable_after,
        "byte_identical": True,
        "model_or_product_execution": False,
    }


def _tree_identity(root: Path) -> str:
    return evidence.canonical_sha256(_tree_files(root))


def _tree_files(root: Path) -> dict[str, str]:
    return {
        path.relative_to(root).as_posix(): evidence.file_sha256(path)
        for path in sorted(root.rglob("*"))
        if path.is_file()
    }


def _immutable_tree_identity(files: dict[str, str]) -> str:
    return evidence.canonical_sha256({path: digest for path, digest in files.items() if path != "execution-resume.json"})


def _validate_identity(value: dict[str, Any], schema: str, name: str) -> None:
    _require(value.get("schema") == schema, f"{name} schema 无效")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), f"{name}身份漂移")


def _load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise validation.KernelIterationValidationError(f"无法读取阶段 4 制品 {path}: {error}") from error
    _require(isinstance(value, dict), f"阶段 4 制品不是对象: {path}")
    return value


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise validation.KernelIterationValidationError(message)
