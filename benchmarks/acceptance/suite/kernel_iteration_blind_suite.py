from __future__ import annotations

import hashlib
import inspect as pyinspect
import json
from pathlib import Path
import secrets
import shutil
import threading
import time
from concurrent.futures import ThreadPoolExecutor
from contextlib import ExitStack
from typing import Any, Callable

import kernel_iteration_evidence as evidence
import kernel_iteration_validation as validation


class BlindSuiteError(ValueError):
    pass


CONTRACT_RELATIVE = Path("iteration/blind-suite-contract.json")
CONTRACT_SCHEMA = "ownward.kernel-iteration-blind-suite-contract/v1"
PLAN_SCHEMA = "ownward.kernel-iteration-blind-suite-plan/v1"
LOCATOR_SCHEMA = "ownward.kernel-iteration-blind-suite-locator/v1"
SECRET_SCHEMA = "ownward.kernel-iteration-blind-suite-secret/v1"
SEALED_SCHEMA = "ownward.kernel-iteration-blind-suite/v1"
PRIVATE_MANIFEST_SCHEMA = "ownward.kernel-iteration-blind-suite-private-manifest/v1"
PUBLIC_RECEIPT_SCHEMA = "ownward.kernel-iteration-blind-suite-receipt/v1"
RESULT_SCHEMA = "ownward.kernel-iteration-blind-suite-preparation-result/v1"
PROGRESS_SCHEMA = "ownward.kernel-iteration-blind-suite-preparation-progress/v1"
ACTIVE_SCHEMA = "ownward.kernel-iteration-blind-suite-active/v1"
RETIREMENT_SCHEMA = "ownward.kernel-iteration-blind-suite-retirement/v1"
SLOT_SCHEMA = "ownward.kernel-iteration-blind-suite-slot/v1"
SLOT_CERTIFICATE_SCHEMA = "ownward.kernel-iteration-blind-suite-slot-certificate/v1"
PREPARATION_STATE_SCHEMA = "ownward.kernel-iteration-blind-suite-preparation-state/v1"
QUALIFICATION_PLAN_SCHEMA = "ownward.kernel-iteration-blind-suite-admission-qualification-plan/v1"
QUALIFICATION_RESULT_SCHEMA = "ownward.kernel-iteration-blind-suite-admission-qualification-result/v1"
EXECUTION_PLAN_SCHEMA = "ownward.kernel-iteration-blind-suite-execution-plan/v1"
EXECUTION_LOCATOR_SCHEMA = "ownward.kernel-iteration-blind-suite-execution-locator/v1"
EXECUTION_RESULT_SCHEMA = "ownward.kernel-iteration-blind-suite-execution-result/v1"
EXECUTION_SCRATCH_SCHEMA = "ownward.kernel-iteration-blind-suite-execution-scratch/v1"
PARTITION_CONTINUATION_SCHEMA = "ownward.kernel-iteration-blind-suite-partition-continuation/v1"
EVALUATION_BATCH_SCHEMA = "ownward.kernel-iteration-blind-suite-evaluation-batch/v1"
EVALUATION_FREEZE_SCHEMA = "ownward.kernel-iteration-blind-suite-evaluation-freeze/v1"
MATERIALS_SCHEMA = validation.MATERIALS_SCHEMA
NO_ANSWER = "I don't have enough information to answer that."
PRIMARY_COVERAGE = tuple(validation.BLIND_COVERAGE)


def load_contract(suite_root: Path) -> dict[str, Any]:
    suite_root = suite_root.resolve()
    value = _load_json(suite_root / CONTRACT_RELATIVE)
    _require(value.get("schema") == CONTRACT_SCHEMA, "版本级盲测套题合同 schema 无效")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), "版本级盲测套题合同身份漂移")
    _require(value.get("formal") is False and value.get("frozen_before_generation") is True, "版本级套题合同没有在生成前冻结")
    levels = tuple(value.get("levels", []))
    _require(levels == (5, 15, 25, 50) and int(value.get("questions_total", 0)) == sum(levels), "版本级套题分区或总题量漂移")
    partitions = _mapping(value, "partitions")
    for index, level in enumerate(levels):
        item = _mapping(partitions, str(level))
        _require(int(item.get("questions", 0)) == level, "版本级套题分区题量漂移")
        _require(item.get("previous") == (levels[index - 1] if index else None), "版本级套题前级关系漂移")
        _require(item.get("next") == (levels[index + 1] if index + 1 < len(levels) else None), "版本级套题后级关系漂移")
    matrix = _mapping(value, "coverage_matrix")
    _require(tuple(_mapping(matrix, "primary")) == PRIMARY_COVERAGE, "版本级套题主覆盖维度漂移")
    for axis in ("primary", "answerability", "session_scale", "fact_load", "relation_depth", "interference"):
        counts = _mapping(matrix, axis)
        for level in levels:
            _require(sum(int(_mapping(counts, name).get(str(level), -1)) for name in counts) == level, f"版本级套题 {axis}/{level} 配额不闭合")
    generation = _mapping(value, "generation")
    admission = _mapping(value, "quality_admission")
    _require(generation.get("model") == "gpt-5.6-terra" and generation.get("reasoning_effort") == "xhigh", "版本级套题生成模型漂移")
    _require(admission.get("model") == "gpt-5.6-terra" and admission.get("reasoning_effort") == "medium", "版本级套题准入模型漂移")
    _require(int(generation.get("max_active", 0)) == 8 and generation.get("worker_active_turns_maximum") == 1, "版本级套题生成并发漂移")
    _require(int(admission.get("batch_questions_maximum", 0)) == 15 and int(admission.get("max_active", 0)) == 8, "版本级套题准入批次或并发漂移")
    _require(int(admission.get("maximum_replacement_rounds_per_invocation", 0)) == 3, "版本级套题单次恢复局部替换边界漂移")
    _require(admission.get("unchanged_accepted_slot_reassessment") == "forbidden", "版本级套题已接受槽位仍会被随机翻转")
    qualification = _mapping(admission, "qualification")
    _require(qualification.get("required_before_generation") is True and qualification.get("repeated_sampling_or_best_result_selection") == "forbidden", "版本级套题准入资格边界漂移")
    lifecycle = _mapping(value, "lifecycle")
    _require(lifecycle.get("active_suites_per_major_version_maximum") == 1, "一个大版本只能存在一套活动盲测")
    isolation = _mapping(value, "isolation")
    _require(isolation.get("candidate_inputs_forbidden_during_preparation") is True and isolation.get("formal_state_written") is False, "版本级套题准备越过候选或正式状态边界")
    return value


def qualify_admission(
    suite_root: Path,
    output_root: Path,
    generation_execution_config: Path,
    formal_state_path: Path,
    *,
    invoker: Callable[..., tuple[dict[str, Any], dict[str, Any]]] | None = None,
) -> dict[str, Any]:
    suite_root = suite_root.resolve()
    repository = suite_root.parents[2]
    output_root = output_root.resolve()
    evidence._validate_output_boundary(repository, output_root)
    contract = load_contract(suite_root)
    validation_contract = validation.load_validation_contract(suite_root)
    runtime = _load_generation_runtime(generation_execution_config.resolve())
    state_path = formal_state_path.resolve()
    _require(state_path.is_file(), "版本级套题准入资格缺少正式 state 只读基线")
    state_before = state_path.read_bytes()
    controls, expected = _qualification_controls()
    implementation = _implementation_identity()["quality-admission"]
    plan_content = {
        "schema": QUALIFICATION_PLAN_SCHEMA,
        "admission_contract_identity": _admission_contract_identity(contract),
        "validation_contract_identity": validation_contract["identity"],
        "settings": _mapping(contract, "quality_admission"),
        "quality_admission_implementation": implementation,
        "control_set_identity": evidence.canonical_sha256({"controls": controls, "expected": expected}),
        "external_intelligence_executor": evidence.file_sha256(runtime["external_intelligence"]["binary"]),
        "formal": False,
    }
    plan_identity = evidence.canonical_sha256(plan_content)
    plan = {**plan_content, "identity": plan_identity}
    root = output_root / "blind-suite-admission-qualification" / plan_identity
    plan_path = root / "plan.json"
    result_path = root / "result.json"
    if result_path.is_file():
        _require(plan_path.is_file() and _load_json(plan_path) == plan, "版本级套题准入资格计划漂移")
        result = _load_json(result_path)
        _validate_qualification_result(result, plan_identity)
        _require(result["passed"] is True, "当前版本级套题准入资格未通过")
        _require(state_path.read_bytes() == state_before, "版本级套题准入资格复用改写正式 state")
        return _qualification_reference(result_path, result, reused=True)
    evidence.atomic_json(plan_path, plan)
    settings = _mapping(contract, "quality_admission")
    materials = _qualification_materials(controls)
    schema = validation._admission_schema([case["case_id"] for case in controls])
    started = time.perf_counter()
    if invoker is None:
        with validation._native_external_intelligence_batch_invoker(suite_root, runtime, root / ".transport") as native:
            output, usage = native(
                suite_root=suite_root, runtime=runtime, stage=root / "assessment", role="quality-admission-qualification",
                prompt=_admission_prompt(validation_contract, materials), schema=schema, settings=settings,
                validate=lambda value: validation.validate_admission(value, materials, validation_contract),
            )
    else:
        output, usage = invoker(
            suite_root=suite_root, runtime=runtime, stage=root / "assessment", role="quality-admission-qualification",
            prompt=_admission_prompt(validation_contract, materials), schema=schema, settings=settings,
            validate=lambda value: validation.validate_admission(value, materials, validation_contract),
        )
    admission = validation.validate_admission(output, materials, validation_contract)
    by_id = {str(item["case_id"]): _mapping(item, "checks") for item in output["assessments"]}
    positive_passed = all(all(by_id[case_id].get(name) is True for name in settings["required_checks"]) for case_id in expected["positive_case_ids"])
    negative_outcomes = {
        case_id: by_id[case_id].get(check) is False
        for case_id, check in expected["negative_target_checks"].items()
    }
    passed = bool(positive_passed and all(negative_outcomes.values()))
    result_content = {
        "schema": QUALIFICATION_RESULT_SCHEMA,
        "plan_identity": plan_identity,
        "passed": passed,
        "status": "qualified" if passed else "qualification-rejected",
        "positive_controls": len(expected["positive_case_ids"]),
        "positive_controls_passed": int(positive_passed) * len(expected["positive_case_ids"]),
        "negative_controls": len(negative_outcomes),
        "negative_controls_passed": sum(int(value) for value in negative_outcomes.values()),
        "required_checks": list(settings["required_checks"]),
        "admission_aggregate": admission,
        "usage": validation._sanitize_usage(usage),
        "wall_seconds": time.perf_counter() - started,
        "candidate_executions": 0,
        "baseline_executions": 0,
        "formal_state_written": False,
        "contains_blind_suite_content": False,
        "best_of_or_repeated_sampling": False,
    }
    result = {**result_content, "identity": evidence.canonical_sha256(result_content)}
    evidence.atomic_json(result_path, result)
    _require(state_path.read_bytes() == state_before, "版本级套题准入资格改写正式 state")
    if not passed:
        raise BlindSuiteError("版本级套题准入模型没有通过固定正误对照")
    return _qualification_reference(result_path, result, reused=False)


def prepare(
    suite_root: Path,
    output_root: Path,
    vault_root: Path,
    generation_execution_config: Path,
    formal_state_path: Path,
    *,
    major_version: str,
    seed: str | None = None,
    plan_identity: str | None = None,
    resume: bool = False,
    invokers: list[Callable[..., tuple[dict[str, Any], dict[str, Any]]]] | None = None,
) -> dict[str, Any]:
    suite_root = suite_root.resolve()
    repository = suite_root.parents[2]
    output_root = output_root.resolve()
    vault_root = vault_root.resolve()
    evidence._validate_output_boundary(repository, output_root)
    _validate_vault_boundary(repository, output_root, vault_root)
    version = _normalize_major_version(major_version)
    contract = load_contract(suite_root)
    validation_contract = validation.load_validation_contract(suite_root)
    runtime = _load_generation_runtime(generation_execution_config.resolve())
    state_path = formal_state_path.resolve()
    _require(state_path.is_file(), "版本级套题准备缺少正式 state 只读基线")
    state_before = state_path.read_bytes()
    qualification = _load_current_admission_qualification(
        suite_root, output_root, contract, validation_contract, runtime,
    )
    dependencies = _preparation_dependencies(suite_root, contract, validation_contract, runtime, qualification)
    _require(not (seed is not None and plan_identity is not None), "版本级套题不得同时按 seed 和 plan identity 恢复")
    public_plan_root: Path
    if plan_identity is not None:
        _require(resume and evidence.is_sha256(plan_identity), "版本级套题 plan identity 无效")
        public_plan_root = output_root / "blind-suite-preparation" / plan_identity
        locator = _load_locator(public_plan_root / "locator.json", plan_identity)
        _require(locator["major_version"] == version, "版本级套题恢复大版本错绑")
        _require(Path(locator["vault_root"]).resolve() == vault_root, "版本级套题恢复封存根漂移")
        _require(Path(locator["generation_execution_config"]).resolve() == generation_execution_config.resolve(), "版本级套题恢复生成配置漂移")
        _require(Path(locator["formal_state"]).resolve() == state_path, "版本级套题恢复正式 state 漂移")
        stored_plan = _load_json(public_plan_root / "plan.json")
        stored_content = {key: value for key, value in stored_plan.items() if key != "identity"}
        _require(stored_plan.get("identity") == plan_identity == evidence.canonical_sha256(stored_content), "版本级套题恢复计划身份漂移")
        _require(stored_plan.get("schema") == PLAN_SCHEMA and stored_plan.get("major_version") == version, "版本级套题恢复计划版本漂移")
        terminal_path = public_plan_root / "result.json"
        if terminal_path.is_file():
            terminal = _load_json(terminal_path)
            _validate_preparation_result(terminal, plan_identity)
            inspect_suite(
                suite_root, output_root, vault_root,
                major_version=version, suite_identity=str(terminal["suite_identity"]),
            )
            _require(state_path.read_bytes() == state_before, "版本级套题终态复用改写了正式 state")
            return _preparation_reference(terminal_path, terminal, reused=True)
        scratch = vault_root / version / ".preparing" / plan_identity
        if stored_plan.get("direct_dependencies") != dict(sorted(dependencies.items())):
            _accept_pre_admission_validator_hardening(
                suite_root, public_plan_root, scratch, stored_plan, dependencies, contract, qualification,
            )
            dependencies = dict(stored_plan["direct_dependencies"])
        secret = _load_secret(scratch / "recovery-secret.json", plan_identity)
        suite_seed = str(secret["suite_seed"])
    else:
        suite_seed = seed or secrets.token_hex(24)
        _require(len(suite_seed) >= 16 and all(char.isalnum() or char in "-_" for char in suite_seed), "版本级套题 seed 无效")
        plan_content = _plan_content(version, contract, dependencies, suite_seed)
        plan_identity = evidence.canonical_sha256(plan_content)
        public_plan_root = output_root / "blind-suite-preparation" / plan_identity
    plan_content = _plan_content(version, contract, dependencies, suite_seed)
    computed_identity = evidence.canonical_sha256(plan_content)
    _require(plan_identity == computed_identity, "版本级套题计划与当前直接依赖不一致")
    plan = {**plan_content, "identity": plan_identity}
    result_path = public_plan_root / "result.json"
    plan_path = public_plan_root / "plan.json"
    scratch = vault_root / version / ".preparing" / plan_identity
    if result_path.is_file():
        _require(resume and plan_path.is_file() and _load_json(plan_path) == plan, "版本级套题终态只能由同一计划恢复")
        result = _load_json(result_path)
        _validate_preparation_result(result, plan_identity)
        _require(state_path.read_bytes() == state_before, "版本级套题终态复用改写了正式 state")
        return _preparation_reference(result_path, result, reused=True)
    version_record = vault_root / version / "version.json"
    if version_record.is_file():
        record = _load_json(version_record)
        _require(record.get("schema") == ACTIVE_SCHEMA and record.get("major_version") == version, "版本级套题封存记录无效")
        receipt_path = output_root / "blind-suite" / version / "suite-receipt.json"
        if receipt_path.is_file():
            receipt = _load_json(receipt_path)
            _validate_public_receipt(receipt, version, str(record.get("suite_identity", "")))
            if receipt.get("plan_identity") == plan_identity:
                recovered_content = {
                    "schema": RESULT_SCHEMA,
                    "plan_identity": plan_identity,
                    "major_version": version,
                    "suite_identity": record["suite_identity"],
                    "status": "frozen",
                    "passed": True,
                    "questions": 95,
                    "candidate_executions": 0,
                    "baseline_executions": 0,
                    "formal_state_written": False,
                    "contains_reversible_content": False,
                    "public_receipt_identity": receipt["identity"],
                    "preparation_model_calls": _usage_calls(receipt["generation_usage"]) + _usage_calls(receipt["admission_usage"]),
                    "wall_seconds": receipt["preparation_wall_seconds"],
                }
                recovered = {**recovered_content, "identity": evidence.canonical_sha256(recovered_content)}
                evidence.atomic_json(result_path, recovered)
                _destroy_preparation_scratch(scratch, vault_root, version)
                (public_plan_root / "active.json").unlink(missing_ok=True)
                _require(state_path.read_bytes() == state_before, "版本级套题提交后恢复改写了正式 state")
                return _preparation_reference(result_path, recovered, reused=True)
        raise BlindSuiteError("该内核大版本已经生成过唯一套题；不得再次生成")
    if plan_path.is_file():
        _require(resume and _load_json(plan_path) == plan, "既有版本级套题计划身份漂移")
    else:
        evidence.atomic_json(plan_path, plan)
    _initialize_recovery(public_plan_root, scratch, output_root, vault_root, plan, suite_seed, generation_execution_config, state_path)
    started = time.perf_counter()
    try:
        with ExitStack() as stack:
            lanes = invokers or [
                stack.enter_context(validation._native_external_intelligence_batch_invoker(
                    suite_root, runtime, scratch / "generation-transport" / f"worker-{index + 1:02d}",
                ))
                for index in range(int(_mapping(contract, "generation")["max_active"]))
            ]
            prepared = _prepare_complete_suite(
                suite_root, contract, validation_contract, scratch, suite_seed, lanes, qualification,
            )
        if not prepared["passed"]:
            progress = _incomplete_preparation_result(plan, contract, prepared, state_path, state_before, started)
            progress_path = public_plan_root / "progress.json"
            evidence.atomic_json(progress_path, progress)
            return _preparation_reference(progress_path, progress, reused=False)
        sealed = _sealed_suite(version, plan, contract, prepared)
        suite_identity = sealed["identity"]
        private_manifest = _install_sealed_suite(vault_root, version, suite_identity, sealed, contract, prepared)
        receipt = _public_receipt(version, plan, contract, sealed, private_manifest, prepared, state_path, state_before, started)
        public_version_root = output_root / "blind-suite" / version
        evidence.atomic_json(public_version_root / "suite-receipt.json", receipt)
        pointer = {
            "schema": ACTIVE_SCHEMA,
            "major_version": version,
            "suite_identity": suite_identity,
            "status": "active",
            "receipt_identity": receipt["identity"],
        }
        pointer = {**pointer, "identity": evidence.canonical_sha256(pointer)}
        evidence.atomic_json(public_version_root / "active.json", pointer)
        version_value = {
            "schema": ACTIVE_SCHEMA,
            "major_version": version,
            "suite_identity": suite_identity,
            "status": "active",
            "contract_identity": contract["identity"],
            "public_receipt_identity": receipt["identity"],
        }
        evidence.atomic_json(version_record, {**version_value, "identity": evidence.canonical_sha256(version_value)})
        result_content = {
            "schema": RESULT_SCHEMA,
            "plan_identity": plan_identity,
            "major_version": version,
            "suite_identity": suite_identity,
            "status": "frozen",
            "passed": True,
            "questions": 95,
            "candidate_executions": 0,
            "baseline_executions": 0,
            "formal_state_written": False,
            "contains_reversible_content": False,
            "public_receipt_identity": receipt["identity"],
            "preparation_model_calls": _usage_calls(prepared["generation_usage"]) + _usage_calls(prepared["admission_usage"]),
            "wall_seconds": time.perf_counter() - started,
        }
        result = {**result_content, "identity": evidence.canonical_sha256(result_content)}
        evidence.atomic_json(result_path, result)
        _destroy_preparation_scratch(scratch, vault_root, version)
        (public_plan_root / "active.json").unlink(missing_ok=True)
        _require(state_path.read_bytes() == state_before, "版本级套题封存改写了正式 state")
        return _preparation_reference(result_path, result, reused=False)
    except (KeyboardInterrupt, InterruptedError):
        _require(state_path.read_bytes() == state_before, "版本级套题中断改写了正式 state")
        raise
    except Exception:
        _require(state_path.read_bytes() == state_before, "版本级套题失败改写了正式 state")
        raise


def resume_by_plan_identity(suite_root: Path, output_root: Path, plan_identity: str) -> dict[str, Any]:
    output_root = output_root.resolve()
    _require(evidence.is_sha256(plan_identity), "版本级套题 plan identity 无效")
    locator = _load_locator(output_root / "blind-suite-preparation" / plan_identity / "locator.json", plan_identity)
    return prepare(
        suite_root,
        output_root,
        Path(locator["vault_root"]),
        Path(locator["generation_execution_config"]),
        Path(locator["formal_state"]),
        major_version=str(locator["major_version"]),
        plan_identity=plan_identity,
        resume=True,
    )


def inspect_suite(suite_root: Path, output_root: Path, vault_root: Path, *, major_version: str, suite_identity: str | None = None) -> dict[str, Any]:
    version = _normalize_major_version(major_version)
    record = _load_version_record(vault_root.resolve(), version)
    expected = suite_identity or str(record["suite_identity"])
    _require(expected == record["suite_identity"], "版本级套题身份错绑")
    manifest = _load_private_manifest(vault_root.resolve(), version, expected)
    contract = _load_frozen_suite_contract(vault_root.resolve(), version, expected, manifest)
    receipt_path = output_root.resolve() / "blind-suite" / version / "suite-receipt.json"
    receipt = _load_json(receipt_path)
    _validate_public_receipt(receipt, version, expected)
    _require(receipt["identity"] == record["public_receipt_identity"], "版本级套题公开收据错绑")
    return {
        "major_version": version,
        "suite_identity": expected,
        "status": record["status"],
        "questions": receipt["questions"],
        "partitions": receipt["partitions"],
        "coverage": receipt["coverage"],
        "candidate_executions": 0,
        "baseline_executions": 0,
        "contains_reversible_content": False,
        "receipt": str(receipt_path),
    }


def retire(suite_root: Path, output_root: Path, vault_root: Path, *, major_version: str, suite_identity: str) -> dict[str, Any]:
    version = _normalize_major_version(major_version)
    _require(evidence.is_sha256(suite_identity), "退役套题身份无效")
    record_path = vault_root.resolve() / version / "version.json"
    record = _load_version_record(vault_root.resolve(), version)
    _require(record["suite_identity"] == suite_identity, "退役套题身份错绑")
    if record["status"] == "retired":
        return {"major_version": version, "suite_identity": suite_identity, "status": "retired", "reused": True}
    _require(record["status"] == "active", "只有活动版本套题可以退役")
    manifest = _load_private_manifest(vault_root.resolve(), version, suite_identity)
    content = {
        "schema": RETIREMENT_SCHEMA,
        "major_version": version,
        "suite_identity": suite_identity,
        "manifest_identity": manifest["identity"],
        "future_major_version_reuse_forbidden": True,
    }
    retirement = {**content, "identity": evidence.canonical_sha256(content)}
    evidence.atomic_json(vault_root.resolve() / version / suite_identity / "retirement.json", retirement)
    updated = {key: value for key, value in record.items() if key != "identity"}
    updated["status"] = "retired"
    updated["retirement_identity"] = retirement["identity"]
    evidence.atomic_json(record_path, {**updated, "identity": evidence.canonical_sha256(updated)})
    public_root = output_root.resolve() / "blind-suite" / version
    pointer = _load_json(public_root / "active.json")
    _require(pointer.get("suite_identity") == suite_identity, "公开活动套题指针错绑")
    pointer_content = {key: value for key, value in pointer.items() if key != "identity"}
    pointer_content["status"] = "retired"
    pointer_content["retirement_identity"] = retirement["identity"]
    evidence.atomic_json(public_root / "active.json", {**pointer_content, "identity": evidence.canonical_sha256(pointer_content)})
    return {"major_version": version, "suite_identity": suite_identity, "status": "retired", "reused": False}


def open_partition_for_evaluation(
    suite_root: Path,
    output_root: Path,
    vault_root: Path,
    *,
    major_version: str,
    suite_identity: str,
    level: int,
) -> dict[str, Any]:
    version = _normalize_major_version(major_version)
    record = _load_version_record(vault_root.resolve(), version)
    _require(record["status"] == "active", "退役或非活动版本套题不得执行")
    _require(record["suite_identity"] == suite_identity, "评测请求套题身份错绑")
    manifest = _load_private_manifest(vault_root.resolve(), version, suite_identity)
    contract = _load_frozen_suite_contract(vault_root.resolve(), version, suite_identity, manifest)
    _require(level in tuple(contract["levels"]), "版本级套题分区无效")
    sealed_path = vault_root.resolve() / version / suite_identity / "suite.json"
    _require(evidence.file_sha256(sealed_path) == manifest["sealed_sha256"], "封存套题内容摘要漂移")
    sealed = _load_json(sealed_path)
    _validate_sealed_suite(sealed, version, contract)
    _require(sealed["identity"] == suite_identity, "封存套题身份漂移")
    partition = _mapping(_mapping(sealed, "partitions"), str(level))
    cases = [sealed["cases"][index] for index in partition["case_indexes"]]
    materials = _materials(cases, expected_profiles=[case["suite_profile"] for case in cases])
    _require(materials["identity"] == partition["material_identity"], "封存套题分区材料身份漂移")
    receipt = _load_json(output_root.resolve() / "blind-suite" / version / "suite-receipt.json")
    _validate_public_receipt(receipt, version, suite_identity)
    return {
        "suite_identity": suite_identity,
        "major_version": version,
        "level": level,
        "partition_identity": partition["identity"],
        "materials": materials,
        "admission": sealed["admission_summary"],
        "contract": contract,
    }


def level_contract(contract: dict[str, Any], level: int) -> dict[str, Any]:
    _require(level in tuple(contract["levels"]), "版本级套题分区无效")
    partitions = _mapping(contract, "partitions")
    sequence = _mapping(partitions, str(level))
    absolute = dict(_mapping(_mapping(contract, "execution"), "absolute_gate"))
    absolute["questions"] = level
    absolute["level_total_wall_seconds_maximum"] = int(_mapping(_mapping(contract, "execution"), "level_total_wall_seconds_maximum")[str(level)])
    content = {
        "schema": "ownward.kernel-iteration-blind-suite-partition-contract/v1",
        "suite_contract_identity": contract["identity"],
        "level": level,
        "formal": False,
        "absolute_gate": absolute,
        "sequence": {
            "previous_level": sequence["previous"],
            "previous_pass_required": sequence["previous"] is not None,
            "next_level": sequence["next"],
            "terminal": sequence["terminal"],
        },
        "relative_baseline_gate": {
            "final_answer_accuracy": "candidate-greater-than-or-equal-to-baseline",
            "fact_delivery_missing": "candidate-less-than-or-equal-to-baseline",
            "temporal_correctness": "candidate-greater-than-or-equal-to-baseline-when-applicable",
            "conflict_correctness": "candidate-greater-than-or-equal-to-baseline-when-applicable",
            "semantic_input_tokens": "candidate-less-than-or-equal-to-baseline",
            "ownward_data_bytes": "candidate-less-than-or-equal-to-baseline",
            "retrieval_latency": "absolute-complete-consumer-gate-only",
            "gate_role": "sequential-early-rejection-not-standalone-overall-uplift-proof",
        },
    }
    return {**content, "identity": evidence.canonical_sha256(content)}


def load_evaluation_batch(path: Path, major_version: str, suite_identity: str) -> dict[str, Any]:
    value = _load_json(path)
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("schema") == EVALUATION_BATCH_SCHEMA, "版本级盲测评测批次 schema 无效")
    _require(value.get("identity") == evidence.canonical_sha256(content), "版本级盲测评测批次身份漂移")
    _require(value.get("major_version") == major_version and value.get("suite_identity") == suite_identity, "版本级盲测评测批次与套题错绑")
    for role in ("candidate", "baseline"):
        item = _mapping(value, role)
        for name in ("subject_manifest", "execution_config"):
            target = Path(str(item.get(name, ""))).resolve()
            _require(target.is_file(), f"版本级盲测评测批次缺少 {role}/{name}")
            _require(item.get(f"{name}_sha256") == evidence.file_sha256(target), f"版本级盲测评测批次 {role}/{name} 漂移")
        _require(evidence.is_sha256(str(item.get("subject_identity", ""))), f"版本级盲测评测批次 {role} subject 身份无效")
    freeze = _mapping(value, "freeze_receipt")
    freeze_path = Path(str(freeze.get("path", ""))).resolve()
    _require(freeze_path.is_file() and freeze.get("sha256") == evidence.file_sha256(freeze_path), "版本级盲测冻结收据漂移")
    frozen = _load_json(freeze_path)
    _require(freeze.get("identity") == frozen.get("identity") and evidence.is_sha256(str(freeze.get("identity", ""))), "版本级盲测冻结收据身份错绑")
    frozen_content = {key: item for key, item in frozen.items() if key != "identity"}
    _require(frozen.get("schema") == EVALUATION_FREEZE_SCHEMA, "版本级盲测冻结收据 schema 无效")
    _require(frozen.get("identity") == evidence.canonical_sha256(frozen_content), "版本级盲测冻结收据内容身份漂移")
    _require(frozen.get("major_version") == major_version and frozen.get("suite_identity") == suite_identity, "版本级盲测冻结收据与套题错绑")
    for role in ("candidate", "baseline"):
        _require(frozen.get(f"{role}_subject_identity") == value[role]["subject_identity"], f"版本级盲测冻结收据与 {role} subject 错绑")
    sources = frozen.get("source_freezes")
    _require(isinstance(sources, list) and len(sources) == 2, "版本级盲测冻结收据缺少双 subject 来源")
    by_role = {str(item.get("role", "")): item for item in sources if isinstance(item, dict)}
    _require(set(by_role) == {"candidate", "baseline"}, "版本级盲测冻结收据 subject 来源角色无效")
    for role, source in by_role.items():
        source_path = Path(str(source.get("path", ""))).resolve()
        _require(source_path.is_file(), f"版本级盲测冻结收据缺少 {role} 来源")
        _require(source.get("sha256") == evidence.file_sha256(source_path), f"版本级盲测冻结收据 {role} 来源漂移")
        source_value = _load_json(source_path)
        _require(source.get("identity") == source_value.get("identity") and evidence.is_sha256(str(source.get("identity", ""))), f"版本级盲测冻结收据 {role} 来源身份错绑")
    _require(value["candidate"]["subject_identity"] != value["baseline"]["subject_identity"], "版本级盲测候选与基线身份相同")
    return value


def _load_evaluation_subject(comparison: dict[str, Any], manifest_path: Path) -> dict[str, Any]:
    """Load either a current candidate manifest or an exact frozen subject projection.

    Frozen baselines are accepted only when the complete envelope is byte-for-value
    equal to a subject selected from the versioned comparison contract.  This keeps
    the evaluator generic without letting a self-addressed, operator-invented
    baseline stand in for a frozen subject.
    """
    manifest = _load_json(manifest_path.resolve())
    if "content" not in manifest:
        return evidence.select_subject(comparison, None, manifest_path.resolve())
    subjects = _mapping(comparison, "subjects")
    for selector in subjects:
        expected = evidence.select_subject(comparison, selector)
        if manifest.get("identity") == expected["identity"]:
            _require(manifest == expected, "版本级盲测冻结 subject 封存内容漂移")
            return expected
    raise BlindSuiteError("版本级盲测冻结 subject 不属于当前版本化比较合同")


def run_partition(
    suite_root: Path,
    output_root: Path,
    vault_root: Path,
    evaluation_batch_path: Path,
    formal_state_path: Path,
    *,
    major_version: str,
    suite_identity: str,
    level: int,
    previous_plan_identity: str | None = None,
    previous_adjudication_path: Path | None = None,
    plan_identity: str | None = None,
    resume: bool = False,
    runner: Callable[..., dict[str, Any]] | None = None,
) -> dict[str, Any]:
    import kernel_iteration_blind_gate as evaluator
    import kernel_iteration_evaluator_reliability as evaluator_reliability
    import kernel_iteration_reader_reliability as reader_reliability

    suite_root = suite_root.resolve()
    repository = suite_root.parents[2]
    output_root = output_root.resolve()
    vault_root = vault_root.resolve()
    evidence._validate_output_boundary(repository, output_root)
    _validate_vault_boundary(repository, output_root, vault_root)
    version = _normalize_major_version(major_version)
    sealed = open_partition_for_evaluation(
        suite_root, output_root, vault_root,
        major_version=version, suite_identity=suite_identity, level=level,
    )
    suite_contract = sealed["contract"]
    contract = level_contract(suite_contract, level)
    batch = load_evaluation_batch(evaluation_batch_path.resolve(), version, suite_identity)
    comparison = evidence.load_contract(suite_root)
    validation_contract = validation.load_validation_contract(suite_root)
    reader_selection = reader_reliability.load_selection(suite_root)
    candidate_runtime = validation.validate_execution_config(
        suite_root, Path(batch["candidate"]["execution_config"]), expected_reader_effort=reader_selection["selected_reasoning_effort"],
    )
    baseline_runtime = validation.validate_execution_config(
        suite_root, Path(batch["baseline"]["execution_config"]), expected_reader_effort=reader_selection["selected_reasoning_effort"],
    )
    candidate = _load_evaluation_subject(comparison, Path(batch["candidate"]["subject_manifest"]))
    baseline = _load_evaluation_subject(comparison, Path(batch["baseline"]["subject_manifest"]))
    _require(candidate["identity"] == batch["candidate"]["subject_identity"], "版本级盲测候选错绑")
    _require(baseline["identity"] == batch["baseline"]["subject_identity"], "版本级盲测基线错绑")
    _require(candidate["identity"] != baseline["identity"], "版本级盲测候选与基线不得相同")
    validation._verify_subject_binary(candidate, candidate_runtime["binary"])
    validation._verify_subject_binary(baseline, baseline_runtime["binary"])
    shared_conditions = evaluator._shared_conditions(candidate_runtime, baseline_runtime)
    state_path = formal_state_path.resolve()
    _require(state_path.is_file(), "版本级盲测执行缺少正式 state 只读基线")
    state_before = state_path.read_bytes()
    runtime_calibration = evidence.calibrate_runtime(suite_root, output_root / "runtime-calibration", state_path, resume=resume)
    _require(state_path.read_bytes() == state_before, "版本级盲测执行校准改写了正式 state")
    evaluator_qualification = evaluator_reliability.load_current_qualification(suite_root, Path(batch["candidate"]["execution_config"]))
    previous_decision = _previous_partition_result(
        output_root, suite_identity, candidate["identity"], contract, previous_plan_identity,
        adjudication_path=previous_adjudication_path,
        evaluation_batch_identity=batch["identity"],
    )
    dependencies = _partition_execution_dependencies(
        suite_root, suite_contract, contract, sealed, candidate, baseline,
        candidate_runtime, baseline_runtime, runtime_calibration, shared_conditions,
        reader_selection, evaluator_qualification, batch,
    )
    if previous_decision is not None:
        dependencies["previous-partition-result"] = previous_decision["result_identity"]
        if previous_decision.get("continuation_identity") is not None:
            dependencies["previous-partition-continuation"] = previous_decision["continuation_identity"]
    plan_content = {
        "schema": EXECUTION_PLAN_SCHEMA,
        "purpose": "run-one-frozen-version-suite-partition",
        "major_version": version,
        "suite_identity": suite_identity,
        "partition_identity": sealed["partition_identity"],
        "level": level,
        "previous_plan_identity": previous_plan_identity,
        "previous_partition_continuation_identity": (
            previous_decision.get("continuation_identity") if previous_decision is not None else None
        ),
        "candidate_subject_identity": candidate["identity"],
        "candidate_kernel_generation_identity": candidate["content"]["kernel_generation_identity"],
        "candidate_kernel_effect_identity": candidate["content"]["kernel_effect_identity"],
        "baseline_subject_identity": baseline["identity"],
        "evaluation_batch_identity": batch["identity"],
        "shared_conditions": shared_conditions,
        "direct_dependencies": dict(sorted(dependencies.items())),
        "formal": False,
    }
    computed_identity = evidence.canonical_sha256(plan_content)
    if plan_identity is not None:
        _require(resume and plan_identity == computed_identity, "版本级盲测执行计划与当前依赖不一致")
    else:
        plan_identity = computed_identity
    plan = {**plan_content, "identity": plan_identity}
    root = output_root / "blind-suite-runs" / suite_identity / candidate["identity"] / plan_identity
    plan_path = root / "plan.json"
    result_path = root / "result.json"
    if result_path.is_file():
        _require(resume and plan_path.is_file() and _load_json(plan_path) == plan, "版本级盲测执行终态只能同身份恢复")
        result = _load_json(result_path)
        _validate_execution_result(result, plan_identity)
        _require(state_path.read_bytes() == state_before, "版本级盲测执行终态复用改写正式 state")
        return _execution_reference(result_path, result, reused=True)
    if plan_path.is_file():
        _require(resume and _load_json(plan_path) == plan, "既有版本级盲测执行计划漂移")
    else:
        evidence.atomic_json(plan_path, plan)
    _require(candidate_runtime["runs"] == baseline_runtime["runs"], "候选与当前基线没有共享持久运行根")
    scratch = _execution_scratch_path(candidate_runtime["runs"], suite_identity, plan_identity)
    scratch.mkdir(parents=True, exist_ok=True)
    locator_content = {
        "schema": EXECUTION_LOCATOR_SCHEMA,
        "plan_identity": plan_identity,
        "major_version": version,
        "suite_identity": suite_identity,
        "vault_root": str(vault_root),
        "evaluation_batch": str(evaluation_batch_path.resolve()),
        "formal_state": str(state_path),
        "output_root": str(output_root),
        "level": level,
        "previous_plan_identity": previous_plan_identity,
        "previous_adjudication": str(previous_adjudication_path.resolve()) if previous_adjudication_path is not None else None,
    }
    locator = {**locator_content, "identity": evidence.canonical_sha256(locator_content)}
    if (root / "locator.json").is_file():
        _require(_load_json(root / "locator.json") == locator, "版本级盲测执行恢复定位漂移")
    else:
        evidence.atomic_json(root / "locator.json", locator)
    evidence.atomic_json(root / "active.json", {"schema": EXECUTION_LOCATOR_SCHEMA, "plan_identity": plan_identity, "scratch": str(scratch)})
    dataset_path = scratch / "accepted" / "dataset.json"
    evidence.atomic_json(dataset_path, [validation._longmemeval_case(case) for case in sealed["materials"]["cases"]])
    execute = runner or validation._run_longmemeval
    started = time.perf_counter()
    candidate_execution = None
    baseline_execution = None
    absolute = None
    relative = None
    answer_attribution = None
    resume_proof = None
    try:
        candidate_execution = evaluator._execute(
            suite_root, candidate_runtime, dataset_path, scratch / "candidate", candidate["identity"], sealed["materials"], resume=resume, runner=execute,
        )
        absolute = evaluator._absolute_decision(candidate_execution["observation"], contract)
        candidate_observation = candidate_execution["observation"]
        if not absolute["passed"]:
            answer_attribution = evaluator._attribute_first_answer_failure(
                suite_root, root, candidate_runtime, sealed["materials"], candidate_execution, absolute,
            )
            if answer_attribution is not None and answer_attribution["classification"] == "external-reader-random-failure":
                absolute = evaluator._adjudicate_external_reader_random_failure(absolute, answer_attribution)
                candidate_observation = {**candidate_observation, "final_answer_accuracy": absolute["adjudicated_final_answer_accuracy"]}
            if not absolute["passed"]:
                result = _finish_partition(
                    root, scratch, candidate_runtime["runs"], state_path, state_before, plan, contract,
                    status="candidate-rejected", passed=False, candidate_decision=False,
                    candidate_execution=candidate_execution, baseline_execution=None, absolute=absolute,
                    relative=None, resume_proof=None,
                    general_root_cause=evaluator._general_root_cause(candidate_execution["observation"], absolute["failures"]),
                    answer_failure_attribution=answer_attribution, started=started,
                )
                return result
        baseline_execution = evaluator._execute(
            suite_root, baseline_runtime, dataset_path, scratch / "baseline", baseline["identity"], sealed["materials"], resume=resume, runner=execute,
        )
        relative = evaluator._relative_decision(candidate_observation, baseline_execution["observation"])
        resume_proof = evaluator._resume_proof(
            suite_root, candidate_runtime, baseline_runtime, dataset_path, scratch,
            candidate["identity"], baseline["identity"], execute,
        )
        total = time.perf_counter() - started
        wall_limit = float(_mapping(contract, "absolute_gate")["level_total_wall_seconds_maximum"])
        process_passed = total <= wall_limit
        passed = bool(relative["passed"] and process_passed)
        status = "passed" if passed else ("relative-rejected" if not relative["passed"] else "evaluation-process-rejected")
        root_cause = None if passed else (
            evaluator._general_root_cause(candidate_execution["observation"], relative["failures"])
            if not relative["passed"]
            else {
                "first_observed_gap": None,
                "responsible_direction": "blind-suite-evaluation-controller",
                "mechanism_status": "requires-independent-process-attribution",
                "failure_metrics": ["level_total_wall_seconds"],
            }
        )
        return _finish_partition(
            root, scratch, candidate_runtime["runs"], state_path, state_before, plan, contract,
            status=status, passed=passed, candidate_decision=bool(relative["passed"]),
            candidate_execution=candidate_execution, baseline_execution=baseline_execution,
            absolute=absolute, relative=relative, resume_proof=resume_proof,
            general_root_cause=root_cause, answer_failure_attribution=answer_attribution, started=started,
        )
    except (KeyboardInterrupt, InterruptedError):
        _require(state_path.read_bytes() == state_before, "版本级盲测执行中断改写正式 state")
        raise
    except Exception:
        _require(state_path.read_bytes() == state_before, "版本级盲测执行失败改写正式 state")
        raise


def resume_partition_by_plan_identity(suite_root: Path, output_root: Path, plan_identity: str, *, runner: Callable[..., dict[str, Any]] | None = None) -> dict[str, Any]:
    output_root = output_root.resolve()
    _require(evidence.is_sha256(plan_identity), "版本级盲测执行 plan identity 无效")
    matches = list((output_root / "blind-suite-runs").glob(f"*/*/{plan_identity}/locator.json"))
    _require(len(matches) == 1, "版本级盲测执行 plan identity 定位不唯一")
    locator = _load_execution_locator(matches[0], plan_identity)
    return run_partition(
        suite_root, output_root, Path(locator["vault_root"]),
        Path(locator["evaluation_batch"]), Path(locator["formal_state"]),
        major_version=locator["major_version"], suite_identity=locator["suite_identity"], level=int(locator["level"]),
        previous_plan_identity=locator.get("previous_plan_identity"),
        previous_adjudication_path=(Path(locator["previous_adjudication"]) if locator.get("previous_adjudication") else None),
        plan_identity=plan_identity, resume=True, runner=runner,
    )


def _previous_partition_result(
    output_root: Path,
    suite_identity: str,
    candidate_identity: str,
    contract: dict[str, Any],
    previous_plan_identity: str | None,
    *,
    adjudication_path: Path | None = None,
    evaluation_batch_identity: str | None = None,
) -> dict[str, str] | None:
    previous_level = _mapping(contract, "sequence")["previous_level"]
    if previous_level is None:
        _require(previous_plan_identity is None, "版本级盲测 5 题分区不得声明前级")
        _require(adjudication_path is None, "版本级盲测 5 题分区不得声明前级裁决")
        return None
    _require(isinstance(previous_plan_identity, str) and evidence.is_sha256(previous_plan_identity), "后续版本级盲测分区缺少前级计划")
    root = output_root / "blind-suite-runs" / suite_identity / candidate_identity / previous_plan_identity
    plan = _load_json(root / "plan.json")
    _require(plan.get("schema") == EXECUTION_PLAN_SCHEMA and plan.get("suite_identity") == suite_identity and plan.get("candidate_subject_identity") == candidate_identity, "版本级盲测前级身份错绑")
    _require(plan.get("level") == previous_level, "版本级盲测前级级别错绑")
    result = _load_json(root / "result.json")
    _validate_execution_result(result, previous_plan_identity)
    if result.get("passed") is True and result.get("candidate_decision") is True:
        _require(adjudication_path is None, "已通过的版本级盲测前级不得叠加外部裁决")
        _require(result.get("next_level") == contract["level"], "版本级盲测前级未授权当前分区")
        return {"result_identity": str(result["identity"])}

    absolute = _mapping(result, "absolute_decision")
    distribution = _mapping(absolute, "retrieval_distribution")
    failures = absolute.get("failures")
    _require(
        result.get("status") == "candidate-rejected"
        and result.get("candidate_decision") is False
        and result.get("baseline_execution") is None
        and distribution.get("status") == "bounded-confirmation-required"
        and distribution.get("candidate_failure") is False
        and isinstance(failures, list)
        and failures
        and {item.get("metric") for item in failures if isinstance(item, dict)} == {"retrieval_p95_confirmation_required"},
        "版本级盲测前级没有通过且不属于可独立确认的非候选失败尾值",
    )
    _require(adjudication_path is not None, "版本级盲测前级有界确认缺少独立裁决")
    _require(isinstance(evaluation_batch_identity, str) and evidence.is_sha256(evaluation_batch_identity), "版本级盲测前级裁决缺少评测批次身份")
    continuation = _load_partition_continuation(
        adjudication_path,
        suite_identity=suite_identity,
        evaluation_batch_identity=evaluation_batch_identity,
        candidate_identity=candidate_identity,
        previous_plan_identity=previous_plan_identity,
        previous_result_identity=str(result["identity"]),
        previous_level=int(previous_level),
        next_level=int(contract["level"]),
    )
    return {
        "result_identity": str(result["identity"]),
        "continuation_identity": str(continuation["identity"]),
    }


def _load_partition_continuation(
    path: Path,
    *,
    suite_identity: str,
    evaluation_batch_identity: str,
    candidate_identity: str,
    previous_plan_identity: str,
    previous_result_identity: str,
    previous_level: int,
    next_level: int,
) -> dict[str, Any]:
    value = _load_json(path.resolve())
    outer_content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(outer_content), "版本级盲测前级裁决外层身份漂移")
    continuation = _mapping(value, "partition_continuation")
    content = {key: item for key, item in continuation.items() if key != "identity"}
    _require(continuation.get("schema") == PARTITION_CONTINUATION_SCHEMA, "版本级盲测前级裁决 schema 无效")
    _require(continuation.get("identity") == evidence.canonical_sha256(content), "版本级盲测前级裁决身份漂移")
    expected = {
        "suite_identity": suite_identity,
        "evaluation_batch_identity": evaluation_batch_identity,
        "candidate_subject_identity": candidate_identity,
        "source_plan_identity": previous_plan_identity,
        "source_result_identity": previous_result_identity,
        "source_level": previous_level,
        "next_level": next_level,
    }
    for name, expected_value in expected.items():
        _require(continuation.get(name) == expected_value, f"版本级盲测前级裁决 {name} 错绑")
    _require(continuation.get("decision") == "continue-same-candidate-after-bounded-confirmation", "版本级盲测前级裁决没有授权继续")
    _require(continuation.get("same_frozen_dependencies") is True, "版本级盲测前级裁决依赖不一致")
    _require(continuation.get("quality_trace_complete") is True, "版本级盲测前级裁决质量证据不完整")
    _require(continuation.get("hard_timeout_or_execution_error_count") == 0, "版本级盲测前级裁决包含硬失败")
    _require(continuation.get("formal_state_byte_identical") is True, "版本级盲测前级裁决改写正式 state")
    _require(continuation.get("contains_reversible_question_answer_evidence_or_case_ids") is False, "版本级盲测前级裁决泄露可逆内容")
    return continuation


def _partition_execution_dependencies(
    suite_root: Path,
    suite_contract: dict[str, Any],
    contract: dict[str, Any],
    sealed: dict[str, Any],
    candidate: dict[str, Any],
    baseline: dict[str, Any],
    candidate_runtime: dict[str, Any],
    baseline_runtime: dict[str, Any],
    runtime_calibration: dict[str, Any],
    shared_conditions: dict[str, str],
    reader_selection: dict[str, Any],
    evaluator_qualification: dict[str, Any],
    evaluation_batch: dict[str, Any],
) -> dict[str, str]:
    import kernel_iteration_blind_gate as evaluator
    repository = suite_root.parents[2]
    long_root = repository / "benchmarks" / "longmemeval_s"
    adapter = suite_root / "kernel_iteration_longmemeval.py"
    executor = evidence.canonical_sha256({
        "external-intelligence-contract": evidence.file_sha256(repository / "benchmarks" / "support" / "external_intelligence.py"),
        "external-intelligence-selection": evidence.file_sha256(repository / "benchmarks" / "support" / "external-intelligence-runtime.json"),
        "longmemeval": evidence.file_sha256(long_root / "run.py"),
        "runtime-adapter": evidence.file_sha256(long_root / "external_intelligence_runtime.py"),
        "transport-adapter": evidence.file_sha256(long_root / "codex_app_server.py"),
        "adapter": evidence.file_sha256(adapter),
        "protocol": evidence.file_sha256(candidate_runtime["protocol"]),
    })
    return {
        "suite-contract": suite_contract["identity"],
        "partition-contract": contract["identity"],
        "suite": sealed["suite_identity"],
        "suite-partition": sealed["partition_identity"],
        "candidate-subject": candidate["identity"],
        "baseline-subject": baseline["identity"],
        "candidate-binary": evidence.file_sha256(candidate_runtime["binary"]),
        "baseline-binary": evidence.file_sha256(baseline_runtime["binary"]),
        "runtime-calibration": str(runtime_calibration.get("runtime_calibration_identity", runtime_calibration.get("identity", ""))),
        "shared-conditions": evidence.canonical_sha256(shared_conditions),
        "evaluation-batch": evaluation_batch["identity"],
        "evaluation-freeze-receipt": evaluation_batch["freeze_receipt"]["identity"],
        "reader-selection": reader_selection["identity"],
        "evaluator-qualification": evaluator_qualification["identity"],
        "executor": executor,
        "observer-and-scorer": evaluator._implementation_identity()["observer-and-scorer"],
        "execution-controller": _implementation_identity()["execution-controller"],
    }


def _finish_partition(
    root: Path,
    scratch: Path,
    runs_root: Path,
    state_path: Path,
    state_before: bytes,
    plan: dict[str, Any],
    contract: dict[str, Any],
    *,
    status: str,
    passed: bool,
    candidate_decision: bool,
    candidate_execution: dict[str, Any],
    baseline_execution: dict[str, Any] | None,
    absolute: dict[str, Any],
    relative: dict[str, Any] | None,
    resume_proof: dict[str, Any] | None,
    general_root_cause: dict[str, Any] | None,
    answer_failure_attribution: dict[str, Any] | None,
    started: float,
) -> dict[str, Any]:
    import kernel_iteration_blind_gate as evaluator
    content = {
        "schema": EXECUTION_RESULT_SCHEMA,
        "plan_identity": plan["identity"],
        "major_version": plan["major_version"],
        "suite_identity": plan["suite_identity"],
        "partition_identity": plan["partition_identity"],
        "level": plan["level"],
        "candidate_subject_identity": plan["candidate_subject_identity"],
        "previous_plan_identity": plan.get("previous_plan_identity"),
        "status": status,
        "passed": passed,
        "candidate_decision": candidate_decision,
        "formal": False,
        "formal_state_written": False,
        "contains_reversible_question_answer_evidence_or_case_ids": False,
        "candidate_execution": evaluator._execution_aggregate(candidate_execution),
        "baseline_execution": evaluator._execution_aggregate(baseline_execution),
        "absolute_decision": absolute,
        "relative_baseline_decision": relative,
        "resume_proof": resume_proof,
        "general_root_cause": general_root_cause,
        "answer_failure_attribution": answer_failure_attribution,
        "wall_seconds": time.perf_counter() - started,
        "next_level": _mapping(contract, "sequence")["next_level"] if passed else None,
        "stage6_complete": bool(passed and _mapping(contract, "sequence")["terminal"] is True),
        "next_action": (
            "run-next-frozen-partition" if passed and not _mapping(contract, "sequence")["terminal"]
            else "final-community-preparation" if passed
            else "return-to-optimization-and-restart-same-suite-from-level-5"
        ),
    }
    result = {**content, "identity": evidence.canonical_sha256(content)}
    evidence.atomic_json(root / "result.json", result)
    _destroy_execution_scratch(scratch, runs_root, plan["suite_identity"], plan["identity"])
    (root / "active.json").unlink(missing_ok=True)
    _require(state_path.read_bytes() == state_before, "版本级盲测执行终态改写正式 state")
    return _execution_reference(root / "result.json", result, reused=False)


def _execution_scratch_path(runs_root: Path, suite_identity: str, plan_identity: str) -> Path:
    _require(evidence.is_sha256(suite_identity) and evidence.is_sha256(plan_identity), "版本级盲测执行 scratch 身份无效")
    identity = evidence.canonical_sha256({
        "schema": EXECUTION_SCRATCH_SCHEMA,
        "suite_identity": suite_identity,
        "plan_identity": plan_identity,
    })
    parent = (runs_root.resolve() / "kvs").resolve()
    path = (parent / identity).resolve()
    _require(path.parent == parent, "版本级盲测执行 scratch 逃逸持久运行根")
    return path


def _destroy_execution_scratch(path: Path, runs_root: Path, suite_identity: str, plan_identity: str) -> None:
    path = path.resolve()
    expected = _execution_scratch_path(runs_root, suite_identity, plan_identity)
    _require(path == expected, "拒绝清理版本级盲测执行区之外的目录")
    if path.exists():
        shutil.rmtree(path)


def _validate_execution_result(value: dict[str, Any], plan_identity: str) -> None:
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("schema") == EXECUTION_RESULT_SCHEMA and value.get("plan_identity") == plan_identity, "版本级盲测执行终态错绑")
    _require(value.get("identity") == evidence.canonical_sha256(content), "版本级盲测执行终态身份漂移")
    _require(value.get("formal_state_written") is False and value.get("contains_reversible_question_answer_evidence_or_case_ids") is False, "版本级盲测执行终态泄露内容或写正式状态")


def _execution_reference(path: Path, result: dict[str, Any], *, reused: bool) -> dict[str, Any]:
    return {
        "passed": result["passed"],
        "status": result["status"],
        "candidate_decision": result["candidate_decision"],
        "major_version": result["major_version"],
        "suite_identity": result["suite_identity"],
        "level": result["level"],
        "plan_identity": result["plan_identity"],
        "result": str(path.resolve()),
        "reused": reused,
        "model_calls": 0 if reused else None,
        "product_executions": 0 if reused else None,
        "next_level": result.get("next_level"),
        "next_action": result.get("next_action"),
    }


def _load_execution_locator(path: Path, plan_identity: str) -> dict[str, Any]:
    value = _load_json(path)
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("schema") == EXECUTION_LOCATOR_SCHEMA and value.get("plan_identity") == plan_identity, "版本级盲测执行定位错绑")
    _require(value.get("identity") == evidence.canonical_sha256(content), "版本级盲测执行定位身份漂移")
    return value


def _prepare_complete_suite(
    suite_root: Path,
    contract: dict[str, Any],
    validation_contract: dict[str, Any],
    scratch: Path,
    suite_seed: str,
    invokers: list[Callable[..., tuple[dict[str, Any], dict[str, Any]]]],
    qualification: dict[str, Any],
) -> dict[str, Any]:
    specs = _suite_specs(contract)
    spec_by_id = {spec["case_id"]: spec for spec in specs}
    state_path = scratch / "preparation-state.json"
    if state_path.is_file():
        state = _load_preparation_state(state_path, specs, qualification)
    else:
        state_content = {
            "schema": PREPARATION_STATE_SCHEMA,
            "qualification_identity": qualification["identity"],
            "next_round": 1,
            "slots": {spec["case_id"]: {"status": "missing", "attempt": 0} for spec in specs},
            "rounds": [],
            "generation_usages": [],
            "admission_usages": [],
        }
        state = {**state_content, "identity": evidence.canonical_sha256(state_content)}
        evidence.atomic_json(state_path, state)

    invocation_limit = int(_mapping(contract, "quality_admission")["maximum_replacement_rounds_per_invocation"])
    scheduler_observed = 0
    for _ in range(invocation_limit):
        selected_ids = [spec["case_id"] for spec in specs if _mapping(state["slots"], spec["case_id"])["status"] != "accepted"]
        if not selected_ids:
            break
        round_index = int(state["next_round"])
        selected_specs = [spec_by_id[case_id] for case_id in selected_ids]
        round_seed = hashlib.sha256(f"{suite_seed}:replacement:{round_index}".encode("utf-8")).hexdigest()
        cases, usages, scheduler = _generate_cases(
            suite_root, contract, validation_contract, scratch / f"material-round-{round_index:03d}", round_seed, invokers, selected_specs,
        )
        scheduler_observed = max(scheduler_observed, int(scheduler["max_active_observed"]))
        for spec, case, usage in zip(selected_specs, cases, usages):
            slot_content = {
                "schema": SLOT_SCHEMA,
                "slot_id": spec["case_id"],
                "profile": spec,
                "attempt": int(_mapping(state["slots"], spec["case_id"])["attempt"]) + 1,
                "content_identity": validation._case_fact_identity(case),
                "case": case,
                "generation_usage": validation._sanitize_usage(usage),
            }
            evidence.atomic_json(_slot_path(scratch, spec["case_id"]), {
                **slot_content, "identity": evidence.canonical_sha256(slot_content),
            })

        generated_materials = _materials(cases, expected_profiles=selected_specs)
        admission, admission_usage = _admit_full_suite(
            suite_root, contract, validation_contract, generated_materials, cases,
            scratch / f"quality-admission-round-{round_index:03d}", invokers,
        )
        assessments = {str(item["case_id"]): item for item in admission.pop("_assessments")}
        rejected = set(_rejected_case_ids(admission, generated_materials, validation_contract))
        required = list(_mapping(contract, "quality_admission")["required_checks"])
        admission_identity = evidence.canonical_sha256({
            "settings": contract["quality_admission"],
            "implementation": _implementation_identity()["quality-admission"],
        })
        for spec in selected_specs:
            case_id = str(spec["case_id"])
            slot = _load_slot(_slot_path(scratch, case_id), spec)
            assessment = assessments[case_id]
            status = "rejected" if case_id in rejected else "accepted"
            slot_state = {"status": status, "attempt": slot["attempt"], "content_identity": slot["content_identity"]}
            certificate_path = _slot_certificate_path(scratch, case_id)
            if status == "accepted":
                certificate_content = {
                    "schema": SLOT_CERTIFICATE_SCHEMA,
                    "slot_id": case_id,
                    "profile_identity": evidence.canonical_sha256(spec),
                    "content_identity": slot["content_identity"],
                    "preparation_contract_identity": _preparation_contract_identity(contract),
                    "quality_admission_identity": admission_identity,
                    "qualification_identity": qualification["identity"],
                    "required_checks": {name: _mapping(assessment, "checks")[name] for name in required},
                    "accepted": True,
                }
                certificate = {**certificate_content, "identity": evidence.canonical_sha256(certificate_content)}
                evidence.atomic_json(certificate_path, certificate)
                slot_state["certificate_identity"] = certificate["identity"]
            else:
                certificate_path.unlink(missing_ok=True)
            state["slots"][case_id] = slot_state

        state["rounds"].append({
            "round": round_index,
            "generated_count": len(selected_ids),
            "preserved_count": len(specs) - len(selected_ids),
            "accepted_count": len(selected_ids) - len(rejected),
            "rejected_count": len(rejected),
            "rejected_slot_identities": [evidence.canonical_sha256(case_id) for case_id in sorted(rejected)],
            "admission_batches": admission["batches"],
        })
        state["generation_usages"].extend(validation._sanitize_usage(item) for item in usages)
        state["admission_usages"].append(validation._sanitize_usage(admission_usage))
        state["next_round"] = round_index + 1
        state = _seal_preparation_state(state)
        evidence.atomic_json(state_path, state)

    accepted_specs = [spec for spec in specs if _mapping(state["slots"], spec["case_id"])["status"] == "accepted"]
    complete = len(accepted_specs) == len(specs)
    cases: list[dict[str, Any]] = []
    certificates: list[dict[str, Any]] = []
    materials = None
    if complete:
        for spec in specs:
            slot = _load_slot(_slot_path(scratch, spec["case_id"]), spec)
            certificates.append(_load_slot_certificate(
                _slot_certificate_path(scratch, spec["case_id"]), slot, spec, contract, qualification,
            ))
            cases.append(slot["case"])
        materials = _materials(cases, expected_profiles=specs)
        _validate_material_isolation(suite_root, materials)
        _validate_complete_suite_deterministically(materials, specs, certificates)
    required = list(_mapping(contract, "quality_admission")["required_checks"])
    accepted_count = len(accepted_specs)
    return {
        "passed": complete,
        "cases": cases,
        "materials": materials,
        "certificates": certificates,
        "rounds": state["rounds"],
        "generation_usage": validation._combine_usages(state["generation_usages"]),
        "admission_usage": validation._combine_usages(state["admission_usages"]),
        "scheduler": {
            "policy": "durable-rejected-or-missing-slots-only/v1",
            "max_active_limit": int(_mapping(contract, "generation")["max_active"]),
            "max_active_observed": scheduler_observed,
            "per_worker_max_active_turns": 1,
            "unchanged_accepted_slots_reassessed": 0,
        },
        "coverage": _coverage_counts(cases) if complete else {},
        "admission": {
            "passed": complete,
            "questions": len(specs),
            "accepted_count": accepted_count,
            "rejected_count": len(specs) - accepted_count,
            "passed_counts": {name: accepted_count for name in required},
            "complete_suite_readmission": False,
            "deterministic_full_suite_validation": complete,
            "batches": sum(int(item["admission_batches"]) for item in state["rounds"]),
            "qualification_identity": qualification["identity"],
        },
    }


def _generate_cases(
    suite_root: Path,
    contract: dict[str, Any],
    validation_contract: dict[str, Any],
    attempt_root: Path,
    attempt_seed: str,
    invokers: list[Callable[..., tuple[dict[str, Any], dict[str, Any]]]],
    specs: list[dict[str, Any]],
) -> tuple[list[dict[str, Any]], list[dict[str, Any]], dict[str, Any]]:
    _require(bool(invokers), "版本级套题生成缺少 worker")
    lanes: list[list[tuple[int, dict[str, Any]]]] = [[] for _ in invokers]
    for index, spec in enumerate(specs):
        lanes[index % len(lanes)].append((index, spec))
    activity_lock = threading.Lock()
    active = 0
    maximum = 0

    def run_lane(invoke: Callable[..., tuple[dict[str, Any], dict[str, Any]]], lane: list[tuple[int, dict[str, Any]]]):
        nonlocal active, maximum
        completed = []
        for index, spec in lane:
            with activity_lock:
                active += 1
                maximum = max(maximum, active)
            try:
                output, usage = invoke(
                    suite_root=suite_root,
                    runtime={},
                    stage=attempt_root / "generator" / spec["case_id"],
                    role="generator",
                    prompt=_generator_prompt(validation_contract, attempt_seed, spec),
                    schema=_generator_schema(validation_contract, spec),
                    settings=_mapping(contract, "generation"),
                    validate=lambda value, current=spec: _validate_generated_case(value, current, validation_contract),
                )
            finally:
                with activity_lock:
                    active -= 1
            completed.append((index, _validate_generated_case(output, spec, validation_contract), usage))
        return completed

    completed: list[tuple[int, dict[str, Any], dict[str, Any]]] = []
    with ThreadPoolExecutor(max_workers=len(invokers), thread_name_prefix="blind-suite-generator") as pool:
        futures = [pool.submit(run_lane, invokers[index], lane) for index, lane in enumerate(lanes) if lane]
        for future in futures:
            completed.extend(future.result())
    completed.sort(key=lambda item: item[0])
    _require([item[0] for item in completed] == list(range(len(specs))), "版本级套题生成没有按冻结原序重组")
    return (
        [item[1] for item in completed],
        [item[2] for item in completed],
        {
            "policy": "bounded-independent-lanes-original-suite-order/v1",
            "max_active_limit": len(invokers),
            "max_active_observed": maximum,
            "submitted": len(specs),
            "per_worker_max_active_turns": 1,
            "result_order": "frozen-coverage-order",
        },
    )


def _admit_full_suite(
    suite_root: Path,
    contract: dict[str, Any],
    validation_contract: dict[str, Any],
    materials: dict[str, Any],
    generated: list[dict[str, Any]],
    stage: Path,
    invokers: list[Callable[..., tuple[dict[str, Any], dict[str, Any]]]],
) -> tuple[dict[str, Any], dict[str, Any]]:
    maximum = int(_mapping(contract, "quality_admission")["batch_questions_maximum"])
    batches = [materials["cases"][index:index + maximum] for index in range(0, len(materials["cases"]), maximum)]
    generated_by_id = {case["case_id"]: case for case in generated}

    def run_batch(index: int, cases: list[dict[str, Any]]):
        batch_materials = _materials(cases, expected_profiles=[case["suite_profile"] for case in cases])
        review = validation._admission_review_materials(batch_materials, [generated_by_id[case["case_id"]] for case in cases])
        output, usage = invokers[index % len(invokers)](
            suite_root=suite_root,
            runtime={},
            stage=stage / f"batch-{index + 1:02d}",
            role="quality-admission",
            prompt=_admission_prompt(validation_contract, review),
            schema=validation._admission_schema([case["case_id"] for case in cases]),
            settings=_mapping(contract, "quality_admission"),
            validate=lambda value, current=batch_materials: validation.validate_admission(value, current, validation_contract),
        )
        validation.validate_admission(output, batch_materials, validation_contract)
        return index, list(output["assessments"]), usage

    completed = []
    with ThreadPoolExecutor(max_workers=min(len(batches), len(invokers)), thread_name_prefix="blind-suite-admission") as pool:
        futures = [pool.submit(run_batch, index, cases) for index, cases in enumerate(batches)]
        for future in futures:
            completed.append(future.result())
    completed.sort(key=lambda item: item[0])
    output = {"assessments": [assessment for _, items, _ in completed for assessment in items]}
    admission = validation.validate_admission(output, materials, validation_contract)
    required = list(_mapping(_mapping(validation_contract, "blind"), "quality_admission")["required_checks"])
    admission["_rejected_case_ids"] = [
        str(item["case_id"])
        for item in output["assessments"]
        if any(_mapping(item, "checks")[name] is not True for name in required)
    ]
    admission["_assessments"] = list(output["assessments"])
    admission["batches"] = len(batches)
    admission["complete_suite_readmission"] = False
    admission["coverage_counts"] = _coverage_counts(materials["cases"])
    usages = validation._combine_usages([usage for _, _, usage in completed])
    return admission, usages


def _slot_path(scratch: Path, slot_id: str) -> Path:
    return scratch / "slots" / slot_id / "slot.json"


def _accept_pre_admission_validator_hardening(
    suite_root: Path,
    public_plan_root: Path,
    scratch: Path,
    stored_plan: dict[str, Any],
    current_dependencies: dict[str, str],
    contract: dict[str, Any],
    qualification: dict[str, Any],
) -> None:
    stored = _mapping(stored_plan, "direct_dependencies")
    changed = {name for name in set(stored) | set(current_dependencies) if stored.get(name) != current_dependencies.get(name)}
    _require(changed == {"generator", "preparation-controller"}, "版本级套题未完成计划直接依赖漂移")
    state = _load_preparation_state(scratch / "preparation-state.json", _suite_specs(contract), qualification)
    _require(state.get("next_round") == 1 and state.get("rounds") == [], "只有准入前的生成校验加固可以复用原始生成检查点")
    _require(all(_mapping(state["slots"], spec["case_id"])["status"] == "missing" for spec in _suite_specs(contract)), "生成校验加固不得复用已裁决槽位")
    for spec in _suite_specs(contract):
        _load_slot(_slot_path(scratch, spec["case_id"]), spec)
    content = {
        "schema": "ownward.kernel-iteration-blind-suite-incomplete-plan-dependency-migration/v1",
        "plan_identity": stored_plan["identity"],
        "classification": "pre-admission-generated-content-validator-hardening-only",
        "old_generator_identity": stored["generator"],
        "current_generator_identity": current_dependencies["generator"],
        "old_preparation_controller_identity": stored["preparation-controller"],
        "current_preparation_controller_identity": current_dependencies["preparation-controller"],
        "revalidated_slots": len(_suite_specs(contract)),
        "accepted_certificates_reused": 0,
        "candidate_executions": 0,
        "baseline_executions": 0,
    }
    receipt = {**content, "identity": evidence.canonical_sha256(content)}
    path = public_plan_root / "dependency-migration.json"
    if path.is_file():
        _require(_load_json(path) == receipt, "版本级套题未完成计划迁移收据漂移")
    else:
        evidence.atomic_json(path, receipt)


def _slot_certificate_path(scratch: Path, slot_id: str) -> Path:
    return scratch / "slots" / slot_id / "certificate.json"


def _seal_preparation_state(value: dict[str, Any]) -> dict[str, Any]:
    content = {key: item for key, item in value.items() if key != "identity"}
    return {**content, "identity": evidence.canonical_sha256(content)}


def _load_preparation_state(path: Path, specs: list[dict[str, Any]], qualification: dict[str, Any]) -> dict[str, Any]:
    value = _load_json(path)
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("schema") == PREPARATION_STATE_SCHEMA, "版本级套题准备检查点 schema 无效")
    _require(value.get("identity") == evidence.canonical_sha256(content), "版本级套题准备检查点身份漂移")
    _require(value.get("qualification_identity") == qualification["identity"], "版本级套题准入资格漂移")
    slots = _mapping(value, "slots")
    _require(set(slots) == {str(spec["case_id"]) for spec in specs}, "版本级套题槽位集合漂移")
    for slot in slots.values():
        _require(_mapping({"slot": slot}, "slot").get("status") in {"missing", "accepted", "rejected"}, "版本级套题槽位状态无效")
    return value


def _load_slot(path: Path, spec: dict[str, Any]) -> dict[str, Any]:
    value = _load_json(path)
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("schema") == SLOT_SCHEMA and value.get("slot_id") == spec["case_id"], "版本级套题槽位身份错绑")
    _require(value.get("identity") == evidence.canonical_sha256(content), "版本级套题槽位内容漂移")
    _require(value.get("profile") == spec, "版本级套题槽位配置漂移")
    case = _mapping(value, "case")
    _validate_suite_case(case)
    _require(value.get("content_identity") == validation._case_fact_identity(case), "版本级套题槽位事实身份漂移")
    return value


def _load_slot_certificate(
    path: Path,
    slot: dict[str, Any],
    spec: dict[str, Any],
    contract: dict[str, Any],
    qualification: dict[str, Any],
) -> dict[str, Any]:
    value = _load_json(path)
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("schema") == SLOT_CERTIFICATE_SCHEMA and value.get("slot_id") == spec["case_id"], "版本级套题槽位证书错绑")
    _require(value.get("identity") == evidence.canonical_sha256(content), "版本级套题槽位证书身份漂移")
    _require(value.get("profile_identity") == evidence.canonical_sha256(spec), "版本级套题槽位证书配置漂移")
    _require(value.get("content_identity") == slot["content_identity"], "版本级套题槽位证书内容漂移")
    _require(value.get("preparation_contract_identity") == _preparation_contract_identity(contract), "版本级套题槽位证书合同漂移")
    expected_admission = evidence.canonical_sha256({
        "settings": contract["quality_admission"],
        "implementation": _implementation_identity()["quality-admission"],
    })
    _require(value.get("quality_admission_identity") == expected_admission, "版本级套题槽位证书准入实现漂移")
    _require(value.get("qualification_identity") == qualification["identity"], "版本级套题槽位证书准入资格漂移")
    _require(value.get("accepted") is True and all(item is True for item in _mapping(value, "required_checks").values()), "版本级套题槽位证书没有完整通过")
    return value


def _validate_complete_suite_deterministically(
    materials: dict[str, Any],
    specs: list[dict[str, Any]],
    certificates: list[dict[str, Any]],
) -> None:
    cases = materials["cases"]
    _require(len(cases) == len(specs) == len(certificates), "版本级套题完整性不闭合")
    _require([case["case_id"] for case in cases] == [spec["case_id"] for spec in specs], "版本级套题槽位原序漂移")
    _require(len({certificate["identity"] for certificate in certificates}) == len(certificates), "版本级套题证书重复")
    case_ids = [str(case["case_id"]) for case in cases]
    session_ids = [str(session["session_id"]) for case in cases for session in case["sessions"]]
    _require(len(case_ids) == len(set(case_ids)), "版本级套题案例身份重复")
    _require(len(session_ids) == len(set(session_ids)), "版本级套题跨案例会话身份冲突")
    fact_ids = [validation._case_fact_identity(case) for case in cases]
    _require(len(fact_ids) == len(set(fact_ids)), "版本级套题事实重复")
    for case, certificate in zip(cases, certificates):
        _require(validation._case_fact_identity(case) == certificate["content_identity"], "版本级套题内容与准入证书错绑")


def _generator_prompt(validation_contract: dict[str, Any], seed: str, spec: dict[str, Any]) -> str:
    profile = json.dumps({key: value for key, value in spec.items() if key != "case_id"}, sort_keys=True)
    if spec["answerability"] == "answerable":
        base = validation._generator_prompt(validation_contract, seed, spec["case_id"], spec["primary"])
        return base.replace("internal, non-formal calibration", "sealed major-version blind suite prepared before any candidate execution") + (
            "\nThe frozen suite profile is " + profile + ". Use exactly " + str(spec["session_count"]) + " sessions. "
            "Honor every profile axis: multiple fact load requires multiple independently necessary facts; multi-hop requires at least two linked steps; "
            "higher interference requires more relevant, plausible competing sessions. Do not mention the profile or suite in the authored case."
        )
    return (
        "Generate one sealed major-version blind-suite case before any candidate execution. The case must be genuinely unanswerable from the supplied sessions. "
        "The correct answer must be exactly: " + NO_ANSWER + " The question must be plausible and specific, while every session is relevant enough to tempt an unsupported answer. "
        "No session may contain or entail the requested missing fact. Emit no answer_session_ids and no evidence_bindings. Emit no_answer_proof with at least one precise missing fact "
        "and at least two related session ids whose contents show why the question is tempting but insufficient. Distractors must support at least two mutually plausible alternatives. "
        "The question must not contain the refusal answer. Use exactly " + str(spec["session_count"]) + " sessions and honor this frozen profile: " + profile + ". "
        "Return only the requested structured object. Seed: " + seed
    )


def _generator_schema(validation_contract: dict[str, Any], spec: dict[str, Any]) -> dict[str, Any]:
    if spec["answerability"] == "answerable":
        schema = json.loads(json.dumps(validation._generator_case_schema(spec["case_id"], spec["primary"], validation_contract)))
        _expand_session_schema(schema, spec)
        return schema
    session_ids = _session_ids(spec)
    turn = {
        "type": "object", "additionalProperties": False, "required": ["role", "content"],
        "properties": {"role": {"type": "string", "enum": ["user", "assistant"]}, "content": {"type": "string", "minLength": 1, "maxLength": 800}},
    }
    session = {
        "type": "object", "additionalProperties": False, "required": ["session_id", "date", "turns"],
        "properties": {
            "session_id": {"type": "string", "enum": session_ids},
            "date": {"type": "string", "pattern": "^[0-9]{4}-[0-9]{2}-[0-9]{2}$"},
            "turns": {"type": "array", "minItems": 1, "maxItems": 6, "items": turn},
        },
    }
    case = {
        "type": "object", "additionalProperties": False,
        "required": ["case_id", "coverage", "question_type", "question_date", "question", "answer", "answer_session_ids", "stale_session_ids", "distractor_session_ids", "sessions", "evidence_bindings", "no_answer_proof"],
        "properties": {
            "case_id": {"type": "string", "enum": [spec["case_id"]]},
            "coverage": {"type": "string", "enum": [spec["primary"]]},
            "question_type": {"type": "string", "enum": ["unanswerable"]},
            "question_date": {"type": "string", "pattern": "^[0-9]{4}-[0-9]{2}-[0-9]{2}$"},
            "question": {"type": "string", "minLength": 10, "maxLength": 500},
            "answer": {"type": "string", "enum": [NO_ANSWER]},
            "answer_session_ids": {"type": "array", "maxItems": 0, "items": {"type": "string", "enum": session_ids}},
            "stale_session_ids": {"type": "array", "maxItems": len(session_ids), "items": {"type": "string", "enum": session_ids}},
            "distractor_session_ids": {"type": "array", "minItems": 2, "maxItems": len(session_ids), "items": {"type": "string", "enum": session_ids}},
            "sessions": {"type": "array", "minItems": len(session_ids), "maxItems": len(session_ids), "items": session},
            "evidence_bindings": {
                "type": "array", "maxItems": 0,
                "items": {"type": "object", "additionalProperties": False, "properties": {}},
            },
            "no_answer_proof": {
                "type": "object", "additionalProperties": False, "required": ["missing_facts", "related_session_ids"],
                "properties": {
                    "missing_facts": {"type": "array", "minItems": 1, "maxItems": 3, "items": {"type": "string", "minLength": 4, "maxLength": 200}},
                    "related_session_ids": {"type": "array", "minItems": 2, "maxItems": len(session_ids), "items": {"type": "string", "enum": session_ids}},
                },
            },
        },
    }
    return {"type": "object", "additionalProperties": False, "required": ["case"], "properties": {"case": case}}


def _expand_session_schema(schema: dict[str, Any], spec: dict[str, Any]) -> None:
    case = schema["properties"]["case"]
    sessions = case["properties"]["sessions"]
    current_ids = list(sessions["items"]["properties"]["session_id"]["enum"])
    desired_ids = _session_ids(spec)
    sessions["minItems"] = sessions["maxItems"] = len(desired_ids)

    def replace(value: Any) -> None:
        if isinstance(value, dict):
            if value.get("enum") == current_ids:
                value["enum"] = desired_ids
            for item in value.values():
                replace(item)
        elif isinstance(value, list):
            for item in value:
                replace(item)

    replace(case)
    for name in ("answer_session_ids", "stale_session_ids", "distractor_session_ids"):
        case["properties"][name]["maxItems"] = len(desired_ids)


def _validate_generated_case(output: dict[str, Any], spec: dict[str, Any], validation_contract: dict[str, Any]) -> dict[str, Any]:
    case = output.get("case")
    _require(isinstance(case, dict), "版本级套题生成器没有返回案例")
    if spec["answerability"] == "answerable":
        projected = validation._validate_generated_case(output, spec["case_id"], spec["primary"], validation_contract)
        _require(len(projected["sessions"]) == spec["session_count"], "版本级套题资产规模漂移")
        return {**projected, "suite_profile": dict(spec)}
    _require(case.get("case_id") == spec["case_id"] and case.get("coverage") == spec["primary"], "无答案案例身份或覆盖错绑")
    _require(case.get("question_type") == "unanswerable" and case.get("answer") == NO_ANSWER, "无答案案例答案合同漂移")
    _require(case.get("answer_session_ids") == [] and case.get("evidence_bindings") == [], "无答案案例不得声明答案证据")
    sessions = case.get("sessions")
    _require(isinstance(sessions, list) and len(sessions) == spec["session_count"], "无答案案例资产规模漂移")
    session_by_id = {item.get("session_id"): item for item in sessions if isinstance(item, dict)}
    _require(set(session_by_id) == set(_session_ids(spec)), "无答案案例会话身份漂移")
    distractors = case.get("distractor_session_ids")
    _require(isinstance(distractors, list) and len(set(distractors)) >= 2 and set(distractors) <= set(session_by_id), "无答案案例干扰证据不足")
    proof = case.get("no_answer_proof")
    _require(isinstance(proof, dict) and isinstance(proof.get("missing_facts"), list) and proof["missing_facts"], "无答案案例缺少不可回答证明")
    related = proof.get("related_session_ids")
    _require(isinstance(related, list) and len(set(related)) >= 2 and set(related) <= set(session_by_id), "无答案案例相关证据证明无效")
    all_text = " ".join(str(turn.get("content", "")) for session in sessions for turn in session.get("turns", []) if isinstance(turn, dict))
    _require(validation._normalize(NO_ANSWER) not in validation._normalize(str(case.get("question", "")) + " " + all_text), "无答案案例泄露拒答文本")
    projected = {
        "case_id": spec["case_id"],
        "coverage": spec["primary"],
        "question_type": "unanswerable",
        "question_date": case["question_date"],
        "question": case["question"],
        "answer": NO_ANSWER,
        "answer_session_ids": [],
        "stale_session_ids": list(case.get("stale_session_ids", [])),
        "distractor_session_ids": list(distractors),
        "truth_claims": [],
        "sessions": sessions,
        "suite_profile": dict(spec),
        "_mechanical_admission_proof": {
            "schema": "ownward.kernel-iteration-blind-mechanical-admission-proof/v1",
            "answerability": "insufficient-evidence",
            "missing_fact_count": len(proof["missing_facts"]),
            "related_session_count": len(set(related)),
            "answer_absent_from_all_sessions": True,
        },
    }
    _validate_suite_case(projected)
    return projected


def _admission_prompt(validation_contract: dict[str, Any], review: dict[str, Any]) -> str:
    return validation._admission_prompt(validation_contract, review).replace(
        "has one uniquely supported answer, sufficient evidence",
        "has either one uniquely supported answer with sufficient evidence, or for an insufficient-evidence profile one uniquely justified abstention with no session supporting the missing fact",
    )


def _materials(cases: list[dict[str, Any]], *, expected_profiles: list[dict[str, Any]]) -> dict[str, Any]:
    _require(len(cases) == len(expected_profiles), "版本级套题案例与冻结配置数量漂移")
    projected = []
    for case, profile in zip(cases, expected_profiles):
        item = {key: value for key, value in case.items() if not key.startswith("_")}
        item["suite_profile"] = dict(profile)
        _validate_suite_case(item)
        projected.append(item)
    content = {
        "schema": MATERIALS_SCHEMA,
        "contains_formal_questions_answers_gold_or_content": False,
        "cases": projected,
        "criteria": {"minimum_accuracy": 0.0, "require_complete_fact_delivery": True, "category_minimums": {}},
    }
    return {**content, "identity": evidence.canonical_sha256(content)}


def _validate_suite_case(case: dict[str, Any]) -> None:
    profile = _mapping(case, "suite_profile")
    _require(case.get("case_id") == profile.get("case_id") and case.get("coverage") == profile.get("primary"), "版本级套题案例配置错绑")
    sessions = case.get("sessions")
    _require(isinstance(sessions, list) and len(sessions) == int(profile.get("session_count", 0)), "版本级套题案例会话规模漂移")
    if profile.get("answerability") == "insufficient-evidence":
        _require(case.get("answer") == NO_ANSWER and case.get("answer_session_ids") == [] and case.get("truth_claims") == [], "无答案案例封存语义漂移")
    else:
        compatibility = {
            key: value
            for key, value in case.items()
            if key != "suite_profile" and not key.startswith("_")
        }
        content = {
            "schema": MATERIALS_SCHEMA,
            "contains_formal_questions_answers_gold_or_content": False,
            "cases": [compatibility],
            "criteria": {"minimum_accuracy": 0.0, "require_complete_fact_delivery": True, "category_minimums": {}},
        }
        validation.validate_materials({**content, "identity": evidence.canonical_sha256(content)}, expected_questions=1)


def _suite_specs(contract: dict[str, Any]) -> list[dict[str, Any]]:
    matrix = _mapping(contract, "coverage_matrix")
    specs: list[dict[str, Any]] = []
    global_index = 1
    offsets = {"answerability": 1, "session_scale": 2, "fact_load": 3, "relation_depth": 4, "interference": 5}
    for level in contract["levels"]:
        sequences = {axis: _balanced_axis_sequence(_mapping(matrix, axis), int(level)) for axis in matrix}
        for axis, offset in offsets.items():
            values = sequences[axis]
            if values:
                shift = offset % len(values)
                sequences[axis] = values[shift:] + values[:shift]
        for local_index in range(int(level)):
            scale = sequences["session_scale"][local_index]
            spec = {
                "case_id": f"vs-c{global_index:03d}",
                "partition": int(level),
                "partition_index": local_index + 1,
                "primary": sequences["primary"][local_index],
                "answerability": sequences["answerability"][local_index],
                "session_scale": scale,
                "session_count": int(scale.rsplit("-", 1)[1]),
                "fact_load": sequences["fact_load"][local_index],
                "relation_depth": sequences["relation_depth"][local_index],
                "interference": sequences["interference"][local_index],
            }
            specs.append(spec)
            global_index += 1
    _require(len(specs) == int(contract["questions_total"]), "版本级套题冻结配置题量漂移")
    return specs


def _balanced_axis_sequence(counts: dict[str, Any], level: int) -> list[str]:
    remaining = {name: int(_mapping(counts, name)[str(level)]) for name in counts}
    result: list[str] = []
    while any(remaining.values()):
        for name in counts:
            if remaining[name] > 0:
                result.append(name)
                remaining[name] -= 1
    _require(len(result) == level, "版本级套题覆盖序列题量漂移")
    return result


def _session_ids(spec: dict[str, Any]) -> list[str]:
    number = int(str(spec["case_id"]).rsplit("c", 1)[1])
    return [f"c{number:03d}-s{index:02d}" for index in range(1, int(spec["session_count"]) + 1)]


def _coverage_counts(cases: list[dict[str, Any]]) -> dict[str, dict[str, int]]:
    axes = ("primary", "answerability", "session_scale", "fact_load", "relation_depth", "interference")
    result: dict[str, dict[str, int]] = {axis: {} for axis in axes}
    for case in cases:
        profile = _mapping(case, "suite_profile")
        for axis in axes:
            name = str(profile[axis])
            result[axis][name] = result[axis].get(name, 0) + 1
    return {axis: dict(sorted(values.items())) for axis, values in result.items()}


def _validate_coverage_against_contract(cases: list[dict[str, Any]], contract: dict[str, Any]) -> None:
    matrix = _mapping(contract, "coverage_matrix")
    for level in contract["levels"]:
        selected = [case for case in cases if _mapping(case, "suite_profile")["partition"] == level]
        _require(len(selected) == level, "版本级套题分区封存题量漂移")
        for axis in matrix:
            actual = {name: 0 for name in _mapping(matrix, axis)}
            for case in selected:
                value = str(_mapping(case, "suite_profile")[axis])
                actual[value] = actual.get(value, 0) + 1
            expected = {name: int(_mapping(_mapping(matrix, axis), name)[str(level)]) for name in _mapping(matrix, axis)}
            _require(actual == expected, f"版本级套题 {level}/{axis} 覆盖漂移")


def _validate_material_isolation(suite_root: Path, materials: dict[str, Any]) -> None:
    stage3 = validation.load_stage3_contract(suite_root)
    frozen = {
        validation._case_fact_identity(case)
        for name in ("development", "regression")
        for case in stage3["loaded"][name]["cases"]
    }
    current = [validation._case_fact_identity(case) for case in materials["cases"]]
    _require(len(current) == len(set(current)), "版本级套题内部存在事实重复")
    _require(set(current).isdisjoint(frozen), "版本级套题与 Stage 3 开发/回归事实重合")


def _rejected_case_ids(admission: dict[str, Any], materials: dict[str, Any], validation_contract: dict[str, Any]) -> list[str]:
    required = list(_mapping(_mapping(validation_contract, "blind"), "quality_admission")["required_checks"])
    # validate_admission deliberately returns only aggregates; recover the rejected set from the
    # batch assessments persisted by the invoker checkpoint is neither necessary nor safe here.
    # The scheduler needs the exact set, so _admit_full_suite attaches it transiently.
    rejected = admission.pop("_rejected_case_ids", None)
    if rejected is not None:
        return list(rejected)
    _require(admission.get("rejected_count") == 0 and all(admission["passed_counts"][name] == len(materials["cases"]) for name in required), "版本级套题准入缺少拒绝项身份")
    return []


def _sealed_suite(version: str, plan: dict[str, Any], contract: dict[str, Any], prepared: dict[str, Any]) -> dict[str, Any]:
    cases = prepared["materials"]["cases"]
    _validate_coverage_against_contract(cases, contract)
    partitions = {}
    for level in contract["levels"]:
        indexes = [index for index, case in enumerate(cases) if _mapping(case, "suite_profile")["partition"] == level]
        materials = _materials([cases[index] for index in indexes], expected_profiles=[cases[index]["suite_profile"] for index in indexes])
        content = {"level": level, "case_indexes": indexes, "material_identity": materials["identity"]}
        partitions[str(level)] = {**content, "identity": evidence.canonical_sha256(content)}
    content = {
        "schema": SEALED_SCHEMA,
        "major_version": version,
        "plan_identity": plan["identity"],
        "contract_identity": contract["identity"],
        "preparation_contract_identity": _preparation_contract_identity(contract),
        "questions": len(cases),
        "partitions": partitions,
        "coverage": prepared["coverage"],
        "admission_summary": {
            "passed": prepared["admission"]["passed"],
            "questions": prepared["admission"]["questions"],
            "passed_counts": prepared["admission"]["passed_counts"],
            "rejected_count": prepared["admission"]["rejected_count"],
            "complete_suite_readmission": prepared["admission"].get("complete_suite_readmission"),
            "batches": prepared["admission"].get("batches"),
            "deterministic_full_suite_validation": prepared["admission"].get("deterministic_full_suite_validation"),
            "qualification_identity": prepared["admission"].get("qualification_identity"),
            "slot_certificate_set_identity": evidence.canonical_sha256([item["identity"] for item in prepared["certificates"]]),
        },
        "slot_certificates": prepared["certificates"],
        "cases": cases,
    }
    return {**content, "identity": evidence.canonical_sha256(content)}


def _install_sealed_suite(vault_root: Path, version: str, suite_identity: str, sealed: dict[str, Any], contract: dict[str, Any], prepared: dict[str, Any]) -> dict[str, Any]:
    version_root = vault_root / version
    version_root.mkdir(parents=True, exist_ok=True)
    target = version_root / suite_identity
    staging = version_root / f".install-{suite_identity}"
    if target.exists():
        manifest = _load_private_manifest(vault_root, version, suite_identity)
        sealed_path = target / "suite.json"
        _require(evidence.file_sha256(sealed_path) == manifest["sealed_sha256"], "已安装版本级套题内容漂移")
        _load_frozen_suite_contract(vault_root, version, suite_identity, manifest)
        installed = _load_json(sealed_path)
        _require(installed == sealed, "已安装版本级套题与恢复内容不一致")
        return manifest
    if staging.exists():
        shutil.rmtree(staging)
    staging.mkdir(parents=True)
    evidence.atomic_json(staging / "suite.json", sealed)
    evidence.atomic_json(staging / "contract.json", contract)
    private_content = {
        "schema": PRIVATE_MANIFEST_SCHEMA,
        "major_version": version,
        "suite_identity": suite_identity,
        "contract_identity": contract["identity"],
        "preparation_contract_identity": _preparation_contract_identity(contract),
        "contract_sha256": evidence.file_sha256(staging / "contract.json"),
        "sealed_sha256": evidence.file_sha256(staging / "suite.json"),
        "questions": sealed["questions"],
        "partition_identities": {level: value["identity"] for level, value in sealed["partitions"].items()},
        "case_fact_set_identity": evidence.canonical_sha256(sorted(validation._case_fact_identity(case) for case in sealed["cases"])),
        "candidate_independent": True,
        "status": "active",
    }
    manifest = {**private_content, "identity": evidence.canonical_sha256(private_content)}
    evidence.atomic_json(staging / "manifest.json", manifest)
    staging.rename(target)
    _require((target / "suite.json").is_file() and (target / "contract.json").is_file() and (target / "manifest.json").is_file(), "版本级套题原子安装不完整")
    return manifest


def _public_receipt(version: str, plan: dict[str, Any], contract: dict[str, Any], sealed: dict[str, Any], manifest: dict[str, Any], prepared: dict[str, Any], state_path: Path, state_before: bytes, started: float) -> dict[str, Any]:
    _require(state_path.read_bytes() == state_before, "版本级套题公开收据生成前正式 state 漂移")
    content = {
        "schema": PUBLIC_RECEIPT_SCHEMA,
        "major_version": version,
        "suite_identity": sealed["identity"],
        "plan_identity": plan["identity"],
        "contract_identity": contract["identity"],
        "private_manifest_identity": manifest["identity"],
        "status": "active",
        "questions": sealed["questions"],
        "partitions": {level: {"questions": len(value["case_indexes"]), "identity": value["identity"], "material_identity": value["material_identity"]} for level, value in sealed["partitions"].items()},
        "coverage": sealed["coverage"],
        "quality_admission": sealed["admission_summary"],
        "replacement_rounds": prepared["rounds"],
        "generation_usage": validation._sanitize_usage(prepared["generation_usage"]),
        "admission_usage": validation._sanitize_usage(prepared["admission_usage"]),
        "generation_scheduler": prepared["scheduler"],
        "candidate_executions": 0,
        "baseline_executions": 0,
        "formal_state_written": False,
        "formal_state_sha256": hashlib.sha256(state_before).hexdigest(),
        "contains_reversible_question_answer_evidence_or_case_ids": False,
        "preparation_wall_seconds": time.perf_counter() - started,
        "terminal_resume_zero_model_zero_product": True,
    }
    receipt = {**content, "identity": evidence.canonical_sha256(content)}
    serialized = json.dumps(receipt, ensure_ascii=False)
    for forbidden in ('"question"', '"answer"', '"case_id"', '"sessions"', '"truth_claims"'):
        _require(forbidden not in serialized, "版本级套题公开收据泄露可还原内容")
    return receipt


def _validate_sealed_suite(value: dict[str, Any], version: str, contract: dict[str, Any]) -> None:
    _require(value.get("schema") == SEALED_SCHEMA and value.get("major_version") == version, "封存套题版本或 schema 无效")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), "封存套题内容身份漂移")
    _require(value.get("contract_identity") == contract["identity"] and value.get("questions") == contract["questions_total"], "封存套题合同或题量漂移")
    cases = value.get("cases")
    _require(isinstance(cases, list) and len(cases) == contract["questions_total"], "封存套题案例缺失")
    for case in cases:
        _validate_suite_case(case)
    _validate_coverage_against_contract(cases, contract)


def _load_private_manifest(vault_root: Path, version: str, suite_identity: str) -> dict[str, Any]:
    value = _load_json(vault_root / version / suite_identity / "manifest.json")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("schema") == PRIVATE_MANIFEST_SCHEMA and value.get("major_version") == version and value.get("suite_identity") == suite_identity, "版本级套题私有清单错绑")
    _require(value.get("identity") == evidence.canonical_sha256(content), "版本级套题私有清单身份漂移")
    return value


def _load_frozen_suite_contract(vault_root: Path, version: str, suite_identity: str, manifest: dict[str, Any]) -> dict[str, Any]:
    path = vault_root / version / suite_identity / "contract.json"
    _require(path.is_file() and evidence.file_sha256(path) == manifest.get("contract_sha256"), "封存套题合同快照摘要漂移")
    value = _load_json(path)
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("schema") == CONTRACT_SCHEMA and value.get("identity") == evidence.canonical_sha256(content), "封存套题合同快照身份漂移")
    _require(value.get("identity") == manifest.get("contract_identity"), "封存套题合同快照与私有清单错绑")
    _require(tuple(value.get("levels", [])) == (5, 15, 25, 50) and value.get("questions_total") == 95, "封存套题合同快照分区漂移")
    return value


def _load_version_record(vault_root: Path, version: str) -> dict[str, Any]:
    value = _load_json(vault_root / version / "version.json")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("schema") == ACTIVE_SCHEMA and value.get("major_version") == version, "版本级套题版本记录无效")
    _require(value.get("identity") == evidence.canonical_sha256(content), "版本级套题版本记录身份漂移")
    _require(value.get("status") in {"active", "retired"}, "版本级套题状态无效")
    return value


def _validate_public_receipt(value: dict[str, Any], version: str, suite_identity: str) -> None:
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("schema") == PUBLIC_RECEIPT_SCHEMA and value.get("major_version") == version and value.get("suite_identity") == suite_identity, "版本级套题公开收据错绑")
    _require(value.get("identity") == evidence.canonical_sha256(content), "版本级套题公开收据身份漂移")
    _require(value.get("contains_reversible_question_answer_evidence_or_case_ids") is False, "版本级套题公开收据泄露可还原内容")


def _plan_content(version: str, contract: dict[str, Any], dependencies: dict[str, str], suite_seed: str) -> dict[str, Any]:
    return {
        "schema": PLAN_SCHEMA,
        "purpose": "prepare-one-sealed-suite-for-one-kernel-major-version",
        "major_version": version,
        "preparation_contract_identity": _preparation_contract_identity(contract),
        "questions": contract["questions_total"],
        "levels": contract["levels"],
        "seed_sha256": hashlib.sha256(suite_seed.encode("utf-8")).hexdigest(),
        "candidate_identity": None,
        "candidate_output": None,
        "direct_dependencies": dict(sorted(dependencies.items())),
        "formal": False,
    }


def _preparation_dependencies(
    suite_root: Path,
    contract: dict[str, Any],
    validation_contract: dict[str, Any],
    runtime: dict[str, Any],
    qualification: dict[str, Any],
) -> dict[str, str]:
    implementation = _implementation_identity()
    return {
        "suite-preparation-contract": _preparation_contract_identity(contract),
        "legacy-validation-primitives": validation_contract["identity"],
        "preparation-controller": implementation["preparation-controller"],
        "suite-storage": implementation["suite-storage"],
        "generator": evidence.canonical_sha256({"settings": contract["generation"], "implementation": implementation["generator"]}),
        "quality-admission": evidence.canonical_sha256({"settings": contract["quality_admission"], "implementation": implementation["quality-admission"]}),
        "quality-admission-qualification": qualification["identity"],
        "external-intelligence-executor": evidence.file_sha256(runtime["external_intelligence"]["binary"]),
        "external-intelligence-credential-location": evidence.canonical_sha256(str(runtime["external_intelligence"]["credential_file"])),
    }


def _preparation_contract_identity(contract: dict[str, Any]) -> str:
    return evidence.canonical_sha256({
        "schema": "ownward.kernel-iteration-blind-suite-preparation-contract-projection/v1",
        "levels": contract["levels"],
        "questions_total": contract["questions_total"],
        "partitions": contract["partitions"],
        "coverage_matrix": contract["coverage_matrix"],
        "generation": contract["generation"],
        "quality_admission": contract["quality_admission"],
        "isolation": contract["isolation"],
        "lifecycle": {
            name: _mapping(contract, "lifecycle")[name]
            for name in ("prepare", "freeze", "retire", "active_suites_per_major_version_maximum")
        },
    })


def _admission_contract_identity(contract: dict[str, Any]) -> str:
    return evidence.canonical_sha256({
        "schema": "ownward.kernel-iteration-blind-suite-admission-contract-projection/v1",
        "quality_admission": contract["quality_admission"],
    })


def _load_current_admission_qualification(
    suite_root: Path,
    output_root: Path,
    contract: dict[str, Any],
    validation_contract: dict[str, Any],
    runtime: dict[str, Any],
) -> dict[str, Any]:
    controls, expected = _qualification_controls()
    content = {
        "schema": QUALIFICATION_PLAN_SCHEMA,
        "admission_contract_identity": _admission_contract_identity(contract),
        "validation_contract_identity": validation_contract["identity"],
        "settings": _mapping(contract, "quality_admission"),
        "quality_admission_implementation": _implementation_identity()["quality-admission"],
        "control_set_identity": evidence.canonical_sha256({"controls": controls, "expected": expected}),
        "external_intelligence_executor": evidence.file_sha256(runtime["external_intelligence"]["binary"]),
        "formal": False,
    }
    identity = evidence.canonical_sha256(content)
    path = output_root / "blind-suite-admission-qualification" / identity / "result.json"
    result = _load_json(path)
    _validate_qualification_result(result, identity)
    _require(result.get("passed") is True, "版本级套题准入模型资格未通过")
    return result


def _qualification_controls() -> tuple[list[dict[str, Any]], dict[str, Any]]:
    checks = (
        "plausible", "difficulty_sufficient", "unique_answer", "evidence_sufficient",
        "no_surface_shortcut", "scoring_discriminative",
    )

    def case(
        case_id: str,
        coverage: str,
        question: str,
        answer: str,
        sessions: list[str],
        *,
        answer_indexes: tuple[int, ...] = (0,),
        stale_indexes: tuple[int, ...] = (),
    ) -> dict[str, Any]:
        values = [
            {"session_id": f"{case_id}-s{index:02d}", "date": f"2038-01-{index:02d}", "turns": [{"role": "user", "content": text}]}
            for index, text in enumerate(sessions, 1)
        ]
        return {
            "case_id": case_id,
            "coverage": coverage,
            "question_type": "qualification-control",
            "question_date": "2038-02-01",
            "question": question,
            "answer": answer,
            "answer_session_ids": [values[index]["session_id"] for index in answer_indexes],
            "stale_session_ids": [values[index]["session_id"] for index in stale_indexes],
            "distractor_session_ids": [values[-2]["session_id"], values[-1]["session_id"]],
            "truth_claims": [{"claim": answer, "evidence_session_ids": [values[index]["session_id"] for index in answer_indexes]}],
            "sessions": values,
            "mechanical_admission_proof": {
                "schema": "ownward.kernel-iteration-blind-mechanical-admission-proof/v1",
                "qualification_control": True,
            },
        }

    controls = [
        case("qa-pos-1", "knowledge-update-conflict", "At the audit cutoff, which dispatch code and locker are jointly authorized for Rowan's archive?", "Juniper-8; Locker North", [
            "A signed correction replaced Rowan archive transfer token C-2 with token J-8; the earlier token is void after 2038-01-09.",
            "The controlled dispatch table maps token J-8 to dispatch code Juniper-8.",
            "The current facilities ledger assigns dispatch code Juniper-8 to Locker North.",
            "Before the signed correction, token C-2 mapped to dispatch code Cedar-2 and Locker East.",
            "An unsigned draft proposed moving Juniper-8 to Locker South, but the facilities ledger never adopted it.",
            "Rowan's field-kit archive uses token J-3, dispatch Juniper-3, and Locker North.",
            "A different archive uses token J-8A and dispatch Juniper-8A in Locker West.",
            "A shipping worksheet repeats Locker East without identifying an archive or current token.",
        ], answer_indexes=(0, 1, 2), stale_indexes=(3,)),
        case("qa-pos-2", "multi-session-relation", "Which route and seal identify Mira's verified handoff after the later authorization?", "Route Lark; Seal S-42", [
            "Mira's verified handoff is bound to badge Q-17 after supervisor verification.",
            "The current badge directory maps Q-17 to Locker North.",
            "The authorized dispatch ledger maps Locker North to Route Lark.",
            "The route registry assigns active Seal S-42 to Route Lark after the 2038-01-14 renewal.",
            "An earlier registry assigned expired Seal S-19 to Route Lark before renewal.",
            "Mira's draft handoff used badge K-4; it was never supervisor-verified.",
            "Badge K-4 maps to Locker South, whose route is Finch and seal is S-42.",
            "Another verified handoff uses badge Q-18, Locker North Annex, Route Larkspur, and Seal S-43.",
        ], answer_indexes=(0, 1, 2, 3), stale_indexes=(4,)),
        case("qa-neg-plausible", "single-session-assistant-fact", "What ordinary decision was recorded?", "violet", [
            "Square thunder drinks seventeen backwards, therefore the ordinary decision is violet.", "A spoon negotiated with a calendar.",
            "A plausible neighboring record chose indigo.", "Another plausible neighboring record chose amber.",
        ]),
        case("qa-neg-difficulty", "single-session-assistant-fact", "What color was selected?", "blue", [
            "The selected color was blue.", "Unrelated weather was mild.", "A distant team selected green.", "Another distant team selected red.",
        ]),
        case("qa-neg-unique", "knowledge-update-conflict", "Which unresolved codename is authoritative?", "alpha", [
            "A signed current note says alpha is authoritative.", "A different signed current note at the same time says beta is authoritative.",
            "A draft mentioned gamma.", "A draft mentioned delta.",
        ]),
        case("qa-neg-evidence", "multi-session-relation", "Which vault did the verified token resolve to?", "Vault Violet", [
            "The verified token was T-9.", "Token T-9 has no recorded vault mapping.",
            "Token T-8 maps to Vault Amber.", "Token T-7 maps to Vault Cobalt.",
        ]),
        case("qa-neg-shortcut", "multi-session-distractor", "The answer is Harbor Quartz; which harbor was selected?", "Harbor Quartz", [
            "The selected harbor was Harbor Quartz.", "The selector belongs to the coastal review.",
            "A similar review selected Harbor Opal.", "Another review selected Harbor Jade.",
        ]),
        case("qa-neg-scoring", "temporal-order", "Which value follows the later marker?", "silver", [
            "The later marker says silver.", "The earlier marker also says silver.",
            "A distractor marker says silver.", "Another distractor marker says silver.",
        ]),
    ]
    expected = {
        "positive_case_ids": ["qa-pos-1", "qa-pos-2"],
        "negative_target_checks": {
            "qa-neg-plausible": checks[0],
            "qa-neg-difficulty": checks[1],
            "qa-neg-unique": checks[2],
            "qa-neg-evidence": checks[3],
            "qa-neg-shortcut": checks[4],
            "qa-neg-scoring": checks[5],
        },
    }
    return controls, expected


def _qualification_materials(cases: list[dict[str, Any]]) -> dict[str, Any]:
    content = {
        "schema": MATERIALS_SCHEMA,
        "contains_formal_questions_answers_gold_or_content": False,
        "cases": cases,
        "criteria": {"minimum_accuracy": 0.0, "require_complete_fact_delivery": True, "category_minimums": {}},
    }
    return {**content, "identity": evidence.canonical_sha256(content)}


def _validate_qualification_result(value: dict[str, Any], plan_identity: str) -> None:
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("schema") == QUALIFICATION_RESULT_SCHEMA and value.get("plan_identity") == plan_identity, "版本级套题准入资格身份错绑")
    _require(value.get("identity") == evidence.canonical_sha256(content), "版本级套题准入资格摘要漂移")
    _require(value.get("best_of_or_repeated_sampling") is False and value.get("formal_state_written") is False, "版本级套题准入资格越过单次固定对照或正式状态边界")


def _qualification_reference(path: Path, result: dict[str, Any], *, reused: bool) -> dict[str, Any]:
    return {
        "passed": result["passed"],
        "status": result["status"],
        "plan_identity": result["plan_identity"],
        "identity": result["identity"],
        "result": str(path.resolve()),
        "reused": reused,
        "model_calls": 0 if reused else int(_mapping(result, "usage").get("calls", 0)),
        "product_executions": 0,
    }


def _implementation_identity() -> dict[str, str]:
    roles = {
        "generator": (_suite_specs, _balanced_axis_sequence, _generator_prompt, _generator_schema, _expand_session_schema, _validate_generated_case, _validate_suite_case),
        "quality-admission": (
            qualify_admission, _admission_prompt, _admit_full_suite, _qualification_controls, _qualification_materials,
            _validate_qualification_result, validation.validate_admission,
        ),
        "preparation-controller": (
            load_contract, prepare, resume_by_plan_identity, _prepare_complete_suite,
            _sealed_suite, _validate_complete_suite_deterministically,
        ),
        "suite-storage": (
            inspect_suite, retire, open_partition_for_evaluation, _install_sealed_suite,
            _load_frozen_suite_contract, _validate_sealed_suite, _load_slot,
            _load_slot_certificate, _load_preparation_state,
        ),
        "execution-controller": (
            run_partition,
            resume_partition_by_plan_identity,
            _previous_partition_result,
            _partition_execution_dependencies,
            _finish_partition,
            _validate_execution_result,
        ),
    }
    return {
        role: evidence.canonical_sha256({
            "schema": "ownward.kernel-iteration-blind-suite-role/v1",
            "role": role,
            "sources": [pyinspect.getsource(callback) for callback in callbacks],
        })
        for role, callbacks in roles.items()
    }


def _load_generation_runtime(path: Path) -> dict[str, Any]:
    value = _load_json(path)
    _require(value.get("schema") == "ownward.acceptance-execution/v3", "版本级套题生成配置 schema 无效")
    community = _mapping(value, "community")
    try:
        configuration = validation.external_intelligence_runtime.configuration_from_execution(community)
        validation.external_intelligence_runtime.validate_configuration(configuration)
    except validation.external_intelligence.ExternalIntelligenceError as error:
        raise BlindSuiteError(str(error)) from error
    external_catalog = validation.external_intelligence.load_runtime_selection(
        Path(__file__).resolve().parents[2] / "support" / "external-intelligence-runtime.json"
    )
    external = validation.external_intelligence.select_runtime_implementation(external_catalog, configuration.driver)
    # Legacy generation-only configurations predate the provider-neutral role
    # block and intentionally carry no semantic/reader/judge settings.  Their
    # generator and admission choices come from the selected implementation's
    # sealed profile; full community executions still validate their complete
    # role declaration through role_profile_from_execution.
    roles = (
        validation.external_intelligence.select_runtime_role_profile(external_catalog, configuration.driver)
        if community.get("external_intelligence") is None
        else validation.external_intelligence_runtime.role_profile_from_execution(community)
    )
    return {"external_intelligence": {
        "driver": external["driver"],
        "provider": external["provider"],
        "transport": external["transport"],
        "worker_isolation": external["worker_isolation"],
        "selection_sha256": external["selection_sha256"],
        "binary": configuration.binary,
        "credential_file": configuration.credential_file,
        "roles": roles,
    }}


def _initialize_recovery(public_root: Path, scratch: Path, output_root: Path, vault_root: Path, plan: dict[str, Any], suite_seed: str, generation_config: Path, state_path: Path) -> None:
    scratch.mkdir(parents=True, exist_ok=True)
    secret_path = scratch / "recovery-secret.json"
    secret = {"schema": SECRET_SCHEMA, "plan_identity": plan["identity"], "suite_seed": suite_seed, "seed_sha256": plan["seed_sha256"]}
    if secret_path.is_file():
        _require(_load_secret(secret_path, plan["identity"]) == secret, "版本级套题恢复秘密漂移")
    else:
        evidence.atomic_json(secret_path, secret)
    locator_content = {
        "schema": LOCATOR_SCHEMA,
        "plan_identity": plan["identity"],
        "major_version": plan["major_version"],
        "output_root": str(output_root),
        "vault_root": str(vault_root),
        "generation_execution_config": str(generation_config.resolve()),
        "formal_state": str(state_path),
    }
    locator = {**locator_content, "identity": evidence.canonical_sha256(locator_content)}
    if (public_root / "locator.json").is_file():
        _require(_load_json(public_root / "locator.json") == locator, "版本级套题恢复定位漂移")
    else:
        evidence.atomic_json(public_root / "locator.json", locator)
    evidence.atomic_json(public_root / "active.json", {"schema": LOCATOR_SCHEMA, "plan_identity": plan["identity"], "status": "preparing"})


def _load_locator(path: Path, plan_identity: str) -> dict[str, Any]:
    value = _load_json(path)
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("schema") == LOCATOR_SCHEMA and value.get("plan_identity") == plan_identity, "版本级套题恢复定位错绑")
    _require(value.get("identity") == evidence.canonical_sha256(content), "版本级套题恢复定位身份漂移")
    for name in ("output_root", "vault_root", "generation_execution_config", "formal_state"):
        _require(Path(str(value.get(name, ""))).is_absolute(), f"版本级套题恢复 {name} 路径无效")
    return value


def _load_secret(path: Path, plan_identity: str) -> dict[str, Any]:
    value = _load_json(path)
    _require(value.get("schema") == SECRET_SCHEMA and value.get("plan_identity") == plan_identity, "版本级套题恢复秘密错绑")
    seed = value.get("suite_seed")
    _require(isinstance(seed, str) and value.get("seed_sha256") == hashlib.sha256(seed.encode("utf-8")).hexdigest(), "版本级套题恢复秘密摘要漂移")
    return value


def _incomplete_preparation_result(plan: dict[str, Any], contract: dict[str, Any], prepared: dict[str, Any], state_path: Path, state_before: bytes, started: float) -> dict[str, Any]:
    _require(state_path.read_bytes() == state_before, "版本级套题未完成准备改写了正式 state")
    content = {
        "schema": PROGRESS_SCHEMA,
        "plan_identity": plan["identity"],
        "major_version": plan["major_version"],
        "suite_identity": None,
        "status": "quality-admission-incomplete-resume-rejected-or-missing-slots",
        "passed": False,
        "questions": contract["questions_total"],
        "accepted_slots": prepared["admission"]["accepted_count"],
        "remaining_slots": prepared["admission"]["rejected_count"],
        "candidate_executions": 0,
        "baseline_executions": 0,
        "formal_state_written": False,
        "contains_reversible_content": False,
        "replacement_rounds": prepared["rounds"],
        "preparation_model_calls": _usage_calls(prepared["generation_usage"]) + _usage_calls(prepared["admission_usage"]),
        "wall_seconds": time.perf_counter() - started,
    }
    return {**content, "identity": evidence.canonical_sha256(content)}


def _validate_preparation_result(value: dict[str, Any], plan_identity: str) -> None:
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("schema") == RESULT_SCHEMA and value.get("plan_identity") == plan_identity, "版本级套题准备终态错绑")
    _require(value.get("identity") == evidence.canonical_sha256(content), "版本级套题准备终态身份漂移")
    _require(value.get("contains_reversible_content") is False and value.get("formal_state_written") is False, "版本级套题准备终态泄露内容或写正式状态")


def _preparation_reference(path: Path, result: dict[str, Any], *, reused: bool) -> dict[str, Any]:
    return {
        "passed": result["passed"],
        "status": result["status"],
        "major_version": result["major_version"],
        "suite_identity": result.get("suite_identity"),
        "plan_identity": result["plan_identity"],
        "questions": result["questions"],
        "result": str(path.resolve()),
        "reused": reused,
        "model_calls": 0 if reused else result.get("preparation_model_calls"),
        "product_executions": 0,
    }


def _destroy_preparation_scratch(path: Path, vault_root: Path, version: str) -> None:
    path = path.resolve()
    expected_parent = (vault_root.resolve() / version / ".preparing").resolve()
    _require(path.parent == expected_parent, "拒绝清理版本级套题准备区之外的目录")
    if path.exists():
        shutil.rmtree(path)
    if expected_parent.is_dir() and not any(expected_parent.iterdir()):
        expected_parent.rmdir()


def _validate_vault_boundary(repository: Path, output_root: Path, vault_root: Path) -> None:
    _require(vault_root.is_absolute(), "版本级套题封存根必须是绝对路径")
    _require(not vault_root.is_relative_to(repository.resolve()), "版本级套题原始内容不得封存在源码仓库")
    _require(not output_root.is_relative_to(vault_root) and not vault_root.is_relative_to(output_root), "普通证据输出与套题封存根不得重叠")


def _normalize_major_version(value: str) -> str:
    normalized = value.strip().lower()
    _require(len(normalized) >= 2 and normalized[0] == "v" and normalized[1:].isdigit(), "内核大版本必须使用 v 加正整数")
    _require(int(normalized[1:]) > 0, "内核大版本必须为正")
    return normalized


def _usage_calls(value: dict[str, Any]) -> int:
    return int(value.get("calls", value.get("attempts", 0)))


def _load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise BlindSuiteError(f"无法读取版本级盲测制品: {path}") from error
    _require(isinstance(value, dict), f"{path} 必须是 JSON 对象")
    return value


def _mapping(value: dict[str, Any], name: str) -> dict[str, Any]:
    item = value.get(name)
    _require(isinstance(item, dict), f"{name} 必须是对象")
    return item


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise BlindSuiteError(message)
