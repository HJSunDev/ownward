from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
from typing import Any


class KernelIterationEvidenceError(ValueError):
    pass


CONTRACT_SCHEMA = "ownward.kernel-iteration-comparison/v2"
SUBJECT_SCHEMA = "ownward.kernel-iteration-subject/v1"
INPUT_SCHEMA = "ownward.kernel-iteration-input/v1"
PLAN_SCHEMA = "ownward.kernel-iteration-plan/v1"
CHECKPOINT_SCHEMA = "ownward.kernel-iteration-checkpoint/v1"
RESULT_SCHEMA = "ownward.kernel-iteration-evidence/v1"
BASELINE_FACTS_SCHEMA = "ownward.kernel-iteration-baseline-facts/v1"
RUNTIME_CALIBRATION_SCHEMA = "ownward.kernel-iteration-runtime-calibration/v1"
RUNTIME_CALIBRATION_RESULT_SCHEMA = "ownward.kernel-iteration-runtime-calibration-evidence/v1"
CONTRACT_RELATIVE = Path("iteration/v2/comparison-contract.json")
FORMAL_STATE_RELATIVE = Path(".tmp/first-kernel-baseline-v1/acceptance/state.json")


def load_contract(suite_root: Path, path: Path | None = None) -> dict[str, Any]:
    contract_path = (path or suite_root / CONTRACT_RELATIVE).resolve()
    value = _load_json(contract_path)
    validate_contract(suite_root, value, contract_path)
    return value


def validate_contract(suite_root: Path, value: dict[str, Any], contract_path: Path | None = None) -> None:
    repository = suite_root.resolve().parents[2]
    _require(value.get("schema") == CONTRACT_SCHEMA, "V2 迭代比较合同 schema 无效")
    _require(value.get("frozen") is True and value.get("frozen_before_v2_results") is True, "V2 比较合同未在候选结果前冻结")
    isolation = _mapping(value, "formal_isolation")
    _require(
        isolation == {
            "formal_contract_mode": False,
            "formal_evidence_layer": False,
            "may_write_acceptance_state": False,
            "may_promote_or_switch_kernel": False,
        },
        "V2 非正式证据隔离边界无效",
    )
    expected_identity = canonical_sha256(_contract_policy_content(value))
    _require(value.get("identity") == expected_identity, "V2 比较政策身份漂移")
    _require(value.get("seal_sha256") == canonical_sha256(_contract_seal_content(value)), "V2 比较合同封存摘要漂移")
    _validate_sources(repository, value)
    _validate_subjects(repository, value)
    _validate_shared_conditions(value)
    _validate_dimensions(value)
    _validate_evidence_types(value)
    _validate_formal_isolation(suite_root, value)
    if contract_path is not None:
        expected_path = (suite_root / CONTRACT_RELATIVE).resolve()
        _require(contract_path == expected_path, "V2 比较合同必须使用唯一版本化路径")


def select_subject(
    contract: dict[str, Any],
    selector: str | None,
    subject_manifest: Path | None = None,
) -> dict[str, Any]:
    _require(bool(selector) != bool(subject_manifest), "必须且只能选择冻结 subject 或提供 V2 subject 清单")
    if selector:
        subjects = _mapping(contract, "subjects")
        _require(selector in subjects, f"未知冻结 subject: {selector}")
        source = dict(_mapping(subjects, selector))
        projection = {key: item for key, item in source.items() if not key.startswith("audit_")}
        return {
            "schema": SUBJECT_SCHEMA,
            "name": selector,
            "role": source["role"],
            "identity": canonical_sha256({"schema": SUBJECT_SCHEMA, "name": selector, "content": projection}),
            "content": projection,
            "audit": {key: item for key, item in source.items() if key.startswith("audit_")},
        }
    manifest = _load_json(subject_manifest.resolve())
    return validate_v2_subject(contract, manifest)


def validate_v2_subject(contract: dict[str, Any], manifest: dict[str, Any]) -> dict[str, Any]:
    definition = _mapping(contract, "v2_subject_contract")
    allowed = set(definition["allowed_direct_dependency_roles"])
    _require(manifest.get("schema") == SUBJECT_SCHEMA and manifest.get("role") == definition["role"], "V2 subject schema 或角色无效")
    required = set(definition["required_identity_fields"])
    _require(required <= set(manifest), "V2 subject 缺少内核身份字段")
    for name in ("kernel_generation_identity", "kernel_effect_identity"):
        _require(is_sha256(manifest.get(name)), f"V2 subject {name} 无效")
    dependencies = _mapping(manifest, "direct_dependencies")
    _require(set(dependencies) == allowed, "V2 subject 直接依赖角色不完整或越界")
    _require(all(is_sha256(item) for item in dependencies.values()), "V2 subject 直接依赖身份无效")
    artifacts = manifest.get("artifacts", {})
    _require(isinstance(artifacts, dict) and all(is_sha256(item) for item in artifacts.values()), "V2 subject 制品身份无效")
    projection = {
        "schema": SUBJECT_SCHEMA,
        "role": manifest["role"],
        "kernel_generation_identity": manifest["kernel_generation_identity"],
        "kernel_effect_identity": manifest["kernel_effect_identity"],
        "direct_dependencies": dict(sorted(dependencies.items())),
        "artifacts": dict(sorted(artifacts.items())),
    }
    identity = canonical_sha256(projection)
    _require(manifest.get("identity") == identity, "V2 subject 内容身份漂移")
    audit = manifest.get("audit", {})
    _require(isinstance(audit, dict), "V2 subject 审计来源无效")
    return {
        "schema": SUBJECT_SCHEMA,
        "name": str(manifest.get("name", "v2-candidate")),
        "role": manifest["role"],
        "identity": identity,
        "content": projection,
        "audit": audit,
    }


def run(
    suite_root: Path,
    output_root: Path,
    *,
    selector: str | None = None,
    subject_manifest: Path | None = None,
    evidence_type: str = "identity-calibration",
    input_manifest: Path | None = None,
    contract_path: Path | None = None,
    resume: bool = False,
) -> dict[str, Any]:
    suite_root = suite_root.resolve()
    repository = suite_root.parents[2]
    output_root = output_root.resolve()
    _validate_output_boundary(repository, output_root)
    contract = load_contract(suite_root, contract_path)
    subject = select_subject(contract, selector, subject_manifest)
    evidence_types = _mapping(contract, "evidence_types")
    _require(evidence_type in evidence_types, f"未知非正式证据类型: {evidence_type}")
    inputs = _load_iteration_input(contract, evidence_type, input_manifest)
    dependencies = {
        "comparison-contract": contract["identity"],
        "subject": subject["identity"],
        **{f"condition:{name}": identity for name, identity in inputs["shared_conditions"].items()},
        **{f"input:{name}": identity for name, identity in inputs["direct_dependencies"].items()},
        **{f"runtime:{name}": identity for name, identity in inputs["runtime_dependencies"].items()},
    }
    plan_content = {
        "schema": PLAN_SCHEMA,
        "contract_identity": contract["identity"],
        "subject": {"name": subject["name"], "role": subject["role"], "identity": subject["identity"]},
        "evidence_type": evidence_type,
        "direct_dependencies": dict(sorted(dependencies.items())),
        "formal": False,
    }
    plan_identity = canonical_sha256(plan_content)
    plan = {**plan_content, "identity": plan_identity}
    evidence_root = output_root / "subjects" / _safe(subject["role"]) / subject["identity"] / evidence_type / plan_identity
    plan_path = evidence_root / "plan.json"
    if plan_path.exists():
        _require(resume, "非正式证据计划已存在；只有 --resume 可复用相同身份")
        _require(_load_json(plan_path) == plan, "已有非正式计划身份或依赖漂移")
    else:
        atomic_json(plan_path, plan)

    reused: list[str] = []
    subject_checkpoint = _checkpoint(plan_identity, "subject-selected", {
        "subject_identity": subject["identity"],
        "subject_role": subject["role"],
    })
    if _ensure_checkpoint(evidence_root / "checkpoints" / "subject-selected.json", subject_checkpoint, resume):
        reused.append("subject-selected")
    prepared_checkpoint = _checkpoint(plan_identity, "evidence-prepared", {
        "input_identity": inputs["identity"],
        "direct_dependencies": dict(sorted(dependencies.items())),
    })
    if _ensure_checkpoint(evidence_root / "checkpoints" / "evidence-prepared.json", prepared_checkpoint, resume):
        reused.append("evidence-prepared")

    result_content = {
        "schema": RESULT_SCHEMA,
        "plan_identity": plan_identity,
        "contract_identity": contract["identity"],
        "subject_identity": subject["identity"],
        "subject_role": subject["role"],
        "evidence_type": evidence_type,
        "status": "prepared",
        "candidate_decision": None,
        "formal_evidence": False,
        "formal_state_written": False,
        "may_promote_or_switch_kernel": False,
        "checkpoints": {
            "subject-selected": subject_checkpoint["identity"],
            "evidence-prepared": prepared_checkpoint["identity"],
        },
    }
    result = {**result_content, "identity": canonical_sha256(result_content)}
    result_path = evidence_root / "result.json"
    if result_path.exists():
        _require(resume and _load_json(result_path) == result, "已有非正式结果身份漂移")
    else:
        atomic_json(result_path, result)
    return {
        "passed": True,
        "formal": False,
        "status": "prepared",
        "contract_identity": contract["identity"],
        "subject_identity": subject["identity"],
        "evidence_type": evidence_type,
        "plan_identity": plan_identity,
        "evidence_root": str(evidence_root),
        "reused_checkpoints": reused,
    }


def calibrate_runtime(
    suite_root: Path,
    output_root: Path,
    state_path: Path,
    *,
    contract_path: Path | None = None,
    resume: bool = False,
) -> dict[str, Any]:
    """Read and seal current Acceptance runtime facts without making them policy."""
    suite_root = suite_root.resolve()
    repository = suite_root.parents[2]
    output_root = output_root.resolve()
    state_path = state_path.resolve()
    _validate_output_boundary(repository, output_root)
    contract = load_contract(suite_root, contract_path)
    definition = _mapping(contract, "runtime_calibration")
    _require(definition.get("schema") == RUNTIME_CALIBRATION_SCHEMA, "运行态校准合同无效")
    _require(state_path.is_file(), "运行态校准缺少 Acceptance state")

    before = state_path.read_bytes()
    state_sha256 = hashlib.sha256(before).hexdigest()
    try:
        state = json.loads(before.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError) as error:
        raise KernelIterationEvidenceError("运行态 Acceptance state 不是有效 UTF-8 JSON") from error
    _require(isinstance(state, dict), "运行态 Acceptance state 必须是对象")
    formal_contract = _load_json(suite_root / "contract.json")
    try:
        import lifecycle as acceptance_lifecycle

        acceptance_lifecycle._validate_state(formal_contract, state)
    except Exception as error:
        raise KernelIterationEvidenceError(f"运行态 Acceptance state 校验失败: {error}") from error
    _require(state.get("schema") == definition["acceptance_state_schema"], "运行态 state schema 漂移")
    binding = _mapping(state, "binding")
    _require(binding.get("schema") == definition["acceptance_binding_schema"], "运行态 binding schema 漂移")
    checkpoints = _mapping(state, "checkpoints")

    checkpoint_facts: dict[str, dict[str, Any]] = {}
    for name, checkpoint in sorted(checkpoints.items()):
        _require(isinstance(checkpoint, dict), f"运行态 {name} 检查点无效")
        report_path = Path(str(checkpoint.get("report_path", "")))
        if not report_path.is_absolute():
            report_path = (repository / report_path).resolve()
        _require(report_path.is_file(), f"运行态 {name} 报告缺失")
        report_sha256 = str(checkpoint.get("report_sha256", ""))
        _require(is_sha256(report_sha256) and file_sha256(report_path) == report_sha256, f"运行态 {name} 报告摘要漂移")
        evidence = _mapping(checkpoint, "evidence_identity")
        _require(is_sha256(evidence.get("identity")), f"运行态 {name} 证据身份无效")
        checkpoint_facts[name] = {
            "report_sha256": report_sha256,
            "evidence_identity": evidence["identity"],
            "passed": checkpoint.get("passed") is True,
        }

    history = state.get("baseline_history")
    _require(isinstance(history, list), "运行态基线历史无效")
    calibration_content = {
        "schema": RUNTIME_CALIBRATION_RESULT_SCHEMA,
        "comparison_contract_identity": contract["identity"],
        "state_schema": state["schema"],
        "state_sha256_before": state_sha256,
        "state_sha256_after": state_sha256,
        "binding_schema": binding["schema"],
        "binding_identity": canonical_sha256(binding),
        "bound_product_identity": binding.get("product"),
        "bound_kernel_generation_identity": _mapping(_mapping(binding, "components"), "kernel-generation").get("identity"),
        "checkpoints": checkpoint_facts,
        "active_baseline_identity": state.get("baseline", {}).get("identity") if isinstance(state.get("baseline"), dict) else None,
        "baseline_history_identities": [item.get("identity") for item in history],
        "formal_state_written": False,
    }
    calibration_identity = canonical_sha256(calibration_content)
    result = {**calibration_content, "identity": calibration_identity}
    evidence_root = output_root / "runtime-calibration" / calibration_identity
    plan = {
        "schema": PLAN_SCHEMA,
        "contract_identity": contract["identity"],
        "evidence_type": "runtime-calibration",
        "direct_dependencies": {
            "comparison-contract": contract["identity"],
            "runtime-state": state_sha256,
            "runtime-binding": calibration_content["binding_identity"],
            **{f"runtime-report:{name}": value["report_sha256"] for name, value in checkpoint_facts.items()},
        },
        "formal": False,
    }
    plan["identity"] = canonical_sha256(plan)

    _require(state_path.read_bytes() == before, "校准期间正式 state 发生变化")
    plan_path = evidence_root / "plan.json"
    result_path = evidence_root / "result.json"
    if plan_path.exists() or result_path.exists():
        _require(resume and plan_path.is_file() and result_path.is_file(), "运行态校准已存在；只有 --resume 可精确复用")
        _require(_load_json(plan_path) == plan and _load_json(result_path) == result, "既有运行态校准身份漂移")
        reused = True
    else:
        atomic_json(plan_path, plan)
        atomic_json(result_path, result)
        reused = False
    _require(state_path.read_bytes() == before, "运行态校准改写了正式 state")
    return {
        "passed": True,
        "formal": False,
        "status": "calibrated",
        "contract_identity": contract["identity"],
        "runtime_calibration_identity": calibration_identity,
        "state_sha256_before": state_sha256,
        "state_sha256_after": state_sha256,
        "evidence_root": str(evidence_root),
        "reused": reused,
    }


def compare_plans(left_path: Path, right_path: Path) -> dict[str, Any]:
    """Prove two evidence plans differ only by their independently sealed subject."""
    left = _load_json(left_path.resolve())
    right = _load_json(right_path.resolve())
    for name, plan in (("left", left), ("right", right)):
        _require(plan.get("schema") == PLAN_SCHEMA, f"{name} 比较计划 schema 无效")
        content = {key: item for key, item in plan.items() if key != "identity"}
        _require(plan.get("identity") == canonical_sha256(content), f"{name} 比较计划身份漂移")
        _require(plan.get("formal") is False, f"{name} 不是非正式证据计划")
    _require(left["contract_identity"] == right["contract_identity"], "比较政策身份不同")
    _require(left["evidence_type"] == right["evidence_type"], "证据类型不同，不能同尺比较")
    left_dependencies = dict(_mapping(left, "direct_dependencies"))
    right_dependencies = dict(_mapping(right, "direct_dependencies"))
    left_subject = left_dependencies.pop("subject", None)
    right_subject = right_dependencies.pop("subject", None)
    _require(is_sha256(left_subject) and is_sha256(right_subject) and left_subject != right_subject, "比较双方必须是不同 subject")
    _require(left_dependencies == right_dependencies, "除 subject 外存在共享条件或输入身份差异")
    pair = {
        "schema": "ownward.kernel-iteration-comparison-pair/v1",
        "contract_identity": left["contract_identity"],
        "evidence_type": left["evidence_type"],
        "left_subject": left_subject,
        "right_subject": right_subject,
        "shared_direct_dependencies": dict(sorted(left_dependencies.items())),
    }
    return {**pair, "identity": canonical_sha256(pair)}


def _load_iteration_input(contract: dict[str, Any], evidence_type: str, path: Path | None) -> dict[str, Any]:
    required = set(_mapping(_mapping(contract, "evidence_types"), evidence_type)["required_dependencies"])
    if path is None:
        _require(evidence_type == "identity-calibration", f"{evidence_type} 需要版本化 --input-manifest")
        content = {"schema": INPUT_SCHEMA, "evidence_type": evidence_type, "shared_conditions": {}, "direct_dependencies": {}, "runtime_dependencies": {}, "payloads": []}
        return {**content, "identity": canonical_sha256(content)}
    path = path.resolve()
    value = _load_json(path)
    _require(value.get("schema") == INPUT_SCHEMA and value.get("evidence_type") == evidence_type, "非正式证据输入 schema 或类型无效")
    shared = _mapping(value, "shared_conditions")
    expected_shared = set(_mapping(contract, "shared_evaluation_conditions")["required_roles"])
    _require(set(shared) == expected_shared and all(is_sha256(item) for item in shared.values()), "共享评测条件身份不完整")
    direct = _mapping(value, "direct_dependencies")
    _require(set(direct) == required and all(is_sha256(item) for item in direct.values()), "证据类型直接依赖身份不完整")
    runtime = value.get("runtime_dependencies", {})
    _require(isinstance(runtime, dict), "运行态直接依赖必须是对象")
    allowed_runtime = set(_mapping(contract, "runtime_calibration")["consumable_dependency_roles"])
    _require(set(runtime) <= allowed_runtime and all(is_sha256(item) for item in runtime.values()), "运行态直接依赖越界或身份无效")
    payloads = value.get("payloads", [])
    _require(isinstance(payloads, list), "非正式证据输入 payloads 无效")
    normalized_payloads: list[dict[str, Any]] = []
    for item in payloads:
        _require(isinstance(item, dict) and set(item) == {"path", "sha256"}, "非正式输入 payload 无效")
        relative = Path(str(item["path"]))
        _require(not relative.is_absolute() and ".." not in relative.parts, "非正式输入 payload 路径越界")
        payload = (path.parent / relative).resolve()
        _require(payload.is_file() and payload.is_relative_to(path.parent), "非正式输入 payload 缺失")
        _require(file_sha256(payload) == item["sha256"], "非正式输入 payload 摘要漂移")
        normalized_payloads.append({"path": relative.as_posix(), "sha256": item["sha256"]})
    content = {
        "schema": INPUT_SCHEMA,
        "evidence_type": evidence_type,
        "shared_conditions": dict(sorted(shared.items())),
        "direct_dependencies": dict(sorted(direct.items())),
        "runtime_dependencies": dict(sorted(runtime.items())),
        "payloads": normalized_payloads,
    }
    _require(value.get("identity") == canonical_sha256(content), "非正式证据输入身份漂移")
    return {**content, "identity": value["identity"]}


def _validate_sources(repository: Path, contract: dict[str, Any]) -> None:
    sources = _mapping(contract, "sources")
    expected = {"kernel_catalog", "current_composition", "frozen_baseline", "v0_baseline_facts"}
    _require(set(sources) == expected, "V2 比较合同来源集合无效")
    for name, source in sources.items():
        _require(isinstance(source, dict) and isinstance(source.get("path"), str) and is_sha256(source.get("sha256")), f"{name} 来源无效")
        _require(".tmp" not in Path(source["path"]).parts, f"{name} 不得依赖运行态 .tmp")
        path = (repository / source["path"]).resolve()
        _require(path.is_relative_to(repository) and path.is_file(), f"{name} 来源缺失")
        _require(file_sha256(path) == source["sha256"], f"{name} 来源摘要漂移")
    facts = _load_json(repository / sources["v0_baseline_facts"]["path"])
    _validate_baseline_facts(facts)
    v0 = _mapping(facts, "v0")
    community = _mapping(v0, "community")
    gaps = _mapping(v0, "diagnostic_first_observed_gaps")
    frontier = _mapping(v0, "frontier")
    dimensions = _mapping(contract, "dimensions")
    _require(community.get("questions") == 500, "V0 community 不是完整 500 题聚合事实")
    _require(_metric_baseline(dimensions, "final_answer_accuracy") == community["accuracy"], "V0 准确率基线漂移")
    _require(_metric_baseline(dimensions, "incorrect_answers") == community["incorrect"], "V0 错误数基线漂移")
    _require(_metric_baseline(dimensions, "target_evidence_delivery_failures") == gaps["target_evidence_not_read"] + gaps["target_evidence_not_search_returned"], "V0 证据交付错误基线漂移")
    _require(_metric_baseline(dimensions, "semantic_input_tokens") == community["semantic_input_tokens"], "V0 语义输入成本基线漂移")
    _require(_metric_baseline(dimensions, "end_to_end_wall_seconds") == community["wall_seconds"], "V0 端到端墙钟基线漂移")
    _require(abs(_metric_baseline(dimensions, "retrieval_mean_ms") - community["retrieval_mean_ms"]) < 1e-9, "V0 检索平均时延基线漂移")
    _require(abs(_metric_baseline(dimensions, "retrieval_p95_ms") - community["retrieval_p95_ms"]) < 1e-9, "V0 检索 p95 基线漂移")
    for name in ("context_precision", "relation_precision", "relation_recall", "fusion_ndcg", "fusion_recall"):
        _require(_metric_baseline(dimensions, name) == _mapping(frontier, name)["value"], f"V0 {name} 基线漂移")
    query = _mapping(frontier, "query_p95_ms")
    _require(_metric_baseline(dimensions, "frontier_query_p95_ms") == query["value"], "V0 frontier query p95 基线漂移")
    _require(_metric_repeatability(dimensions, "frontier_query_p95_ms") == query["repeatability_error"], "V0 frontier query 重复误差漂移")
    _require(_mapping(contract, "v1_benefit_policy")["known_aggregate_observations"] == {
        key: value for key, value in _mapping(facts, "v1_observation").items() if key != "status" and not key.startswith("audit_")
    }, "V1 聚合观察漂移")


def _validate_baseline_facts(facts: dict[str, Any]) -> None:
    _require(facts.get("schema") == BASELINE_FACTS_SCHEMA, "版本化 V0 聚合事实 schema 无效")
    content = {key: value for key, value in facts.items() if key != "identity"}
    _require(facts.get("identity") == canonical_sha256(content), "版本化 V0 聚合事实身份漂移")
    _require(facts.get("contains_formal_questions_answers_gold_or_content") is False, "版本化基线事实不得包含正式题目、答案、Gold 或正文")
    v0 = _mapping(facts, "v0")
    community = _mapping(v0, "community")
    _require(community.get("correct") + community.get("incorrect") == community.get("questions") == 500, "版本化 V0 题量事实无效")
    _require(abs(community.get("accuracy") - community.get("correct") / community.get("questions")) < 1e-12, "版本化 V0 准确率事实无效")
    gaps = _mapping(v0, "diagnostic_first_observed_gaps")
    _require(sum(gaps.values()) == 500 and gaps.get("none") == community.get("correct"), "版本化 V0 诊断聚合不闭合")
    _require(v0.get("automatic_root_cause_attribution") is False, "首次观测缺口不得冒充根因")


def _validate_subjects(repository: Path, contract: dict[str, Any]) -> None:
    subjects = _mapping(contract, "subjects")
    _require(set(subjects) == {"v0", "current-product"}, "冻结 subject 必须且只能包含 V0 与当前产品")
    catalog = _load_json(repository / _mapping(_mapping(contract, "sources"), "kernel_catalog")["path"])
    generations = {item["name"]: item for item in catalog.get("generations", [])}
    _require(set(generations) == {"v0", "v1"}, "内核世代目录不等于冻结 V0/V1")
    v0 = _mapping(subjects, "v0")
    _require(v0["kernel_generation_identity"] == generations["v0"]["kernel"]["identity"], "V0 世代身份漂移")
    _require(v0["binary_identity"] == generations["v0"]["mapping"]["binary_sha256"], "V0 二进制身份漂移")
    _require(v0["direct_dependencies"] == {item["role"]: item["identity"] for item in generations["v0"]["kernel"]["dependencies"]}, "V0 直接依赖漂移")
    facts = _mapping(_load_json(repository / _mapping(_mapping(contract, "sources"), "v0_baseline_facts")["path"]), "v0")
    _require(v0["kernel_generation_identity"] == facts["kernel_generation_identity"], "V0 世代与版本化聚合事实不一致")
    _require(v0["kernel_effect_identity"] == facts["kernel_effect_identity"], "V0 效果身份与版本化聚合事实不一致")
    _require(v0["binary_identity"] == facts["binary_sha256"], "V0 二进制与版本化聚合事实不一致")
    composition = _load_json(repository / _mapping(_mapping(contract, "sources"), "current_composition")["path"])
    current = _mapping(subjects, "current-product")
    components = {item["role"]: item for item in composition.get("components", [])}
    _require(current["composition_identity"] == composition["identity"], "当前产品组合身份漂移")
    _require(current["kernel_identity"] == components["kernel"]["identity"], "当前产品内核身份漂移")
    _require(current["kernel_generation_identity"] == generations["v1"]["kernel"]["identity"], "当前产品 V1 世代映射漂移")
    _require(current["direct_dependencies"] == {role: components[role]["identity"] for role in current["direct_dependencies"]}, "当前产品直接依赖漂移")
    definition = _mapping(contract, "v2_subject_contract")
    _require(definition.get("schema") == SUBJECT_SCHEMA and definition.get("git_is_audit_only") is True, "V2 subject 合同无效")


def _validate_shared_conditions(contract: dict[str, Any]) -> None:
    shared = _mapping(contract, "shared_evaluation_conditions")
    _require(shared.get("comparison_rule") == "pairwise-identical", "共享评测条件必须逐项同尺")
    required = shared.get("required_roles")
    _require(isinstance(required, list) and len(required) == len(set(required)) and len(required) >= 7, "共享评测条件角色无效")
    _require(shared.get("only_planned_variable") == "kernel-generation-and-declared-direct-dependencies", "V2 比较变量边界无效")


def _validate_dimensions(contract: dict[str, Any]) -> None:
    dimensions = _mapping(contract, "dimensions")
    _require(set(dimensions) == {"information-organization-quality", "retrieval-and-final-answer-quality", "end-to-end-efficiency"}, "V2 比较维度不完整")
    large_improvements = 0
    for name, dimension in dimensions.items():
        _require(dimension.get("cross_dimension_compensation") is False, f"{name} 不得跨维度补偿")
        metrics = dimension.get("metrics")
        _require(isinstance(metrics, list) and metrics, f"{name} 缺少冻结指标")
        seen: set[str] = set()
        for metric in metrics:
            _require(isinstance(metric, dict) and isinstance(metric.get("name"), str) and metric["name"] not in seen, f"{name} 指标无效或重复")
            seen.add(metric["name"])
            _require(metric.get("direction") in {"higher", "lower"}, f"{metric['name']} 方向无效")
            _require(isinstance(metric.get("baseline"), (int, float)) and isinstance(metric.get("repeatability_error"), (int, float)), f"{metric['name']} 基线或重复误差无效")
            gate = _mapping(metric, "gate")
            _require(gate.get("kind") in {"large-improvement", "non-regression"}, f"{metric['name']} 门槛类型无效")
            _require(("minimum" in gate) != ("maximum" in gate), f"{metric['name']} 必须有唯一数值门槛")
            if gate.get("basis") in {"errors-at-least-halved", "cost-at-least-halved"}:
                _require(float(gate["maximum"]) <= float(metric["baseline"]) / 2, f"{metric['name']} 没有达到至少减半门槛")
            if gate.get("basis") == "remaining-errors-at-least-halved" and metric["direction"] == "higher":
                _require(float(gate["minimum"]) >= 1 - (1 - float(metric["baseline"])) / 2, f"{metric['name']} 没有达到剩余错误减半门槛")
            large_improvements += int(gate["kind"] == "large-improvement")
    _require(large_improvements >= 4, "V2 合同没有冻结足够的大幅跃升门槛")


def _validate_evidence_types(contract: dict[str, Any]) -> None:
    evidence_types = _mapping(contract, "evidence_types")
    expected = {"identity-calibration", "problem-pool", "development", "regression", "integrated", "blind-calibration", "blind-gate"}
    _require(set(evidence_types) == expected, "非正式证据类型骨架不完整")
    for name, definition in evidence_types.items():
        required = definition.get("required_dependencies")
        _require(isinstance(required, list) and len(required) == len(set(required)), f"{name} 直接依赖声明无效")
    runtime = _mapping(contract, "runtime_calibration")
    _require(runtime.get("schema") == RUNTIME_CALIBRATION_SCHEMA, "运行态校准 schema 无效")
    _require(runtime.get("checkpoint_policy") == "validate-all-present-without-requiring-stage-completion", "运行态检查点校准政策无效")
    _require(runtime.get("state_and_reports_are_read_only") is True, "运行态校准必须只读")
    _require(runtime.get("binding_and_checkpoint_changes_do_not_change_policy_identity") is True, "运行态事实不得进入比较政策身份")


def _validate_formal_isolation(suite_root: Path, contract: dict[str, Any]) -> None:
    formal = _load_json(suite_root / "contract.json")
    modes = _mapping(formal, "execution").get("modes")
    layers = _mapping(formal, "evidence_layers")
    _require(isinstance(modes, list), "正式执行模式合同无效")
    _require("kernel-iteration" not in modes and "kernel-iteration" not in layers, "非正式迭代入口进入了正式执行模式或证据层")
    _require("iteration" not in layers, "非正式迭代证据进入了正式层")


def _validate_output_boundary(repository: Path, output_root: Path) -> None:
    formal_root = (repository / FORMAL_STATE_RELATIVE).resolve().parent
    _require(output_root != formal_root and not output_root.is_relative_to(formal_root) and not formal_root.is_relative_to(output_root), "非正式证据输出不得覆盖或包含正式 Acceptance 状态")
    _require(output_root.name.lower() != "state.json", "非正式证据输出不得伪装为正式 state")


def _checkpoint(plan_identity: str, step: str, payload: dict[str, Any]) -> dict[str, Any]:
    content = {"schema": CHECKPOINT_SCHEMA, "plan_identity": plan_identity, "step": step, "payload": payload}
    return {**content, "identity": canonical_sha256(content)}


def _ensure_checkpoint(path: Path, expected: dict[str, Any], resume: bool) -> bool:
    if path.exists():
        _require(resume and _load_json(path) == expected, f"检查点 {path.name} 身份漂移或未请求恢复")
        return True
    atomic_json(path, expected)
    return False


def atomic_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(f".{path.name}.{os.getpid()}.tmp")
    encoded = (json.dumps(value, ensure_ascii=False, sort_keys=True, indent=2) + "\n").encode("utf-8")
    try:
        with temporary.open("xb") as stream:
            stream.write(encoded)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
    finally:
        temporary.unlink(missing_ok=True)


def canonical_sha256(value: Any) -> str:
    encoded = json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def _contract_seal_content(value: dict[str, Any]) -> dict[str, Any]:
    """Seal all frozen facts while excluding audit-only provenance."""
    def project(item: Any) -> Any:
        if isinstance(item, dict):
            return {
                key: project(nested)
                for key, nested in item.items()
                if key not in {"identity", "seal_sha256"} and not key.startswith("audit_")
            }
        if isinstance(item, list):
            return [project(nested) for nested in item]
        return item

    projected = project(value)
    _require(isinstance(projected, dict), "比较合同身份投影无效")
    return projected


def _contract_policy_content(value: dict[str, Any]) -> dict[str, Any]:
    """Return only rules shared by a comparison; subjects remain independent."""
    names = {
        "schema",
        "formal_isolation",
        "v2_subject_contract",
        "shared_evaluation_conditions",
        "dimensions",
        "v1_benefit_policy",
        "evidence_types",
        "invalidation",
        "cost_limits",
        "runtime_calibration",
    }
    projected = _contract_seal_content(value)
    return {name: projected[name] for name in sorted(names)}


def file_sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def is_sha256(value: Any) -> bool:
    return isinstance(value, str) and len(value) == 64 and all(character in "0123456789abcdef" for character in value)


def _load_json(path: Path) -> dict[str, Any]:
    value = json.loads(path.read_text(encoding="utf-8"))
    _require(isinstance(value, dict), f"{path} 必须是 JSON 对象")
    return value


def _mapping(value: dict[str, Any], name: str) -> dict[str, Any]:
    item = value.get(name)
    _require(isinstance(item, dict), f"{name} 必须是对象")
    return item


def _metric_baseline(dimensions: dict[str, Any], name: str) -> float | int:
    matches = [
        metric
        for dimension in dimensions.values()
        for metric in dimension.get("metrics", [])
        if metric.get("name") == name
    ]
    _require(len(matches) == 1, f"冻结指标不存在或重复: {name}")
    return matches[0]["baseline"]


def _metric_repeatability(dimensions: dict[str, Any], name: str) -> float | int:
    matches = [
        metric
        for dimension in dimensions.values()
        for metric in dimension.get("metrics", [])
        if metric.get("name") == name
    ]
    _require(len(matches) == 1, f"冻结指标不存在或重复: {name}")
    return matches[0]["repeatability_error"]


def _safe(value: str) -> str:
    normalized = "".join(character if character.isalnum() or character in "-_" else "-" for character in value)
    _require(normalized and normalized not in {".", ".."}, "非正式证据 subject 路径无效")
    return normalized


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise KernelIterationEvidenceError(message)
