from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import kernel_iteration_evidence as evidence
import kernel_iteration_stage4_resource_cost as resource_cost
import kernel_iteration_validation as validation


SCHEMA = "ownward.kernel-iteration-stage4-resource-cost-diagnosis/v1"


def diagnose(
    suite_root: Path,
    output_root: Path,
    paired_result_path: Path,
    execution_config_path: Path,
    formal_state_path: Path,
    *,
    resume: bool,
) -> dict[str, Any]:
    suite_root = suite_root.resolve()
    repository = suite_root.parents[2]
    output_root = output_root.resolve()
    result_path = output_root / "diagnosis.json"
    contract = resource_cost.load_contract(suite_root)
    paired = _load_json(paired_result_path.resolve())
    _validate_identity(paired, resource_cost.RESULT_SCHEMA, "同尺资源结果")
    _require(paired["contract_identity"] == contract["identity"], "资源诊断与成对合同错绑")
    state_path = formal_state_path.resolve()
    state_sha256 = evidence.file_sha256(state_path)
    _require(state_sha256 == contract["formal_state"]["sha256"], "资源诊断前正式 state 漂移")
    if result_path.is_file():
        _require(resume, "资源成本诊断已存在；只有 --resume 可复用")
        value = _load_json(result_path)
        _validate_identity(value, SCHEMA, "资源成本诊断")
        _require(value["paired_result_identity"] == paired["identity"] and value["formal_state_sha256"] == state_sha256, "资源成本诊断恢复身份漂移")
        return {**value, "path": str(result_path), "reused": True}

    runtime = validation.validate_execution_config(suite_root, execution_config_path.resolve())
    semantic = {
        subject: _semantic_plan_summary(runtime["runs"], paired["subjects"][subject]["plans"])
        for subject in ("v0", "v2")
    }
    decision = evaluate(paired, semantic)
    _require(evidence.file_sha256(state_path) == state_sha256, "资源成本诊断改写了正式 state")
    content = {
        "schema": SCHEMA,
        "formal": False,
        "formal_state_written": False,
        "paired_result_identity": paired["identity"],
        "contract_identity": contract["identity"],
        "candidate_subject_identity": paired["candidate_subject_identity"],
        "semantic_input_decomposition": semantic,
        "model_duty": {
            subject: paired["subjects"][subject]["calls"]
            for subject in ("v0", "v2")
        },
        "storage_decomposition": {
            subject: paired["subjects"][subject]["storage_breakdown"]
            for subject in ("v0", "v2")
        },
        "decision": decision,
        "formal_state_sha256": state_sha256,
        "root_status": "open",
        "next_validation": decision["next_validation"],
    }
    value = {**content, "identity": evidence.canonical_sha256(content)}
    output_root.mkdir(parents=True, exist_ok=True)
    evidence.atomic_json(result_path, value)
    return {**value, "path": str(result_path), "reused": False}


def evaluate(paired: dict[str, Any], semantic: dict[str, dict[str, Any]]) -> dict[str, Any]:
    dimensions = paired["gates"]["dimensions"]
    v0 = semantic["v0"]
    v2 = semantic["v2"]
    unique_complete = (
        v0["body_count"] == v0["work_item_count"]
        and v2["body_count"] == v2["work_item_count"]
        and v0["analysis_calls"] == v2["analysis_calls"] == 12
        and v0["work_item_count"] == v2["work_item_count"]
    )
    token_deficit = int(dimensions["semantic_input_tokens"]["v2"] - dimensions["semantic_input_tokens"]["v0"] * 0.5)
    _require(token_deficit > 0, "语义输入已经通过减半门，不应进入根因诊断")
    first_root = {
        "dimension": "semantic_input_tokens",
        "mechanism": "one-lossless-semantic-organization-input-per-independent-case",
        "proven_facts": {
            "same_analysis_calls": v0["analysis_calls"] == v2["analysis_calls"],
            "same_work_item_count": v0["work_item_count"] == v2["work_item_count"],
            "each_work_body_transmitted_once": unique_complete,
            "deduplicated_body_table_active": v0["deduplicated_representation"] and v2["deduplicated_representation"],
            "semantic_input_token_deficit_to_half_gate": token_deficit,
        },
        "removable_duplicate_body_transmission_proven": False,
        "in_package_implementation_allowed": False,
        "reason": "The frozen path already transmits each unique authority work body once. Halving now requires omitting unique work, changing semantic/model responsibility, or deferring uncharged work; all are outside this package and its quality boundary.",
    }
    return {
        "paired_gate_status": paired["gates"]["root_status"],
        "failed_dimensions": [name for name, item in dimensions.items() if not item["passed"]],
        "first_dominant_root": first_root,
        "candidate_code_changed": False,
        "stage4_complete": False,
        "next_validation": "freeze-an-architecture-authorized-lossless-semantic-organization-cost-chain-before-changing-the-candidate",
    }


def _semantic_plan_summary(runs: Path, plans: dict[str, Any]) -> dict[str, Any]:
    totals = {
        "questions": 0,
        "analysis_calls": 0,
        "work_item_count": 0,
        "body_count": 0,
        "body_chars": 0,
        "input_utf8_bytes": 0,
        "legacy_input_utf8_bytes": 0,
    }
    representations: set[str] = set()
    for plan in plans.values():
        root = runs / "kernel-iteration" / str(plan["plan_identity"]) / "run" / "questions"
        _require(root.is_dir(), f"资源诊断缺少运行问题目录: {root}")
        for question in sorted(root.iterdir()):
            if not question.is_dir():
                continue
            semantic_plan = _load_json(question / "semantic-plan.json")
            transport = semantic_plan["transport"]
            representations.add(str(transport["representation"]))
            totals["questions"] += 1
            totals["analysis_calls"] += int(transport["analysis_calls"])
            totals["input_utf8_bytes"] += int(transport["new_input_utf8_bytes"])
            totals["legacy_input_utf8_bytes"] += int(transport["legacy_input_utf8_bytes"])
            for batch in semantic_plan["batches"]:
                totals["work_item_count"] += len(batch["work_ids"])
                for unit in batch["analysis_units"]:
                    totals["body_count"] += int(unit["body_count"])
                    totals["body_chars"] += int(unit["body_chars"])
    return {
        **totals,
        "representations": sorted(representations),
        "deduplicated_representation": representations == {"ownward.semantic-deduplicated-body-table/v1"},
        "body_to_work_ratio": totals["body_count"] / totals["work_item_count"],
    }


def _validate_identity(value: dict[str, Any], schema: str, name: str) -> None:
    _require(value.get("schema") == schema, f"{name} schema 无效")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), f"{name}身份漂移")


def _load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise validation.KernelIterationValidationError(f"无法读取资源诊断制品 {path}: {error}") from error
    _require(isinstance(value, dict), f"资源诊断制品不是对象: {path}")
    return value


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise validation.KernelIterationValidationError(message)
