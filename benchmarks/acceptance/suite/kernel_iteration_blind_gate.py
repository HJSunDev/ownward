from __future__ import annotations

import hashlib
import inspect
import json
from pathlib import Path
import secrets
import shutil
import threading
import time
from concurrent.futures import ThreadPoolExecutor
from contextlib import ExitStack, contextmanager
from typing import Any, Callable

import kernel_iteration_evidence as evidence
import kernel_iteration_validation as validation


class BlindGateError(ValueError):
    pass


GATE_LEVELS = (5, 15, 25, 50)
CONTRACT_RELATIVES = {
    level: Path(f"iteration/v2/stage6-blind-gate-{level:02d}-contract.json")
    for level in GATE_LEVELS
}
LEVEL_BUDGETS = {5: 406, 15: 751, 25: 1097, 50: 1961}
LEVEL_SEQUENCE = {5: (None, 15), 15: (5, 25), 25: (15, 50), 50: (25, None)}
CONTRACT_SCHEMA = "ownward.kernel-iteration-stage6-blind-gate-contract/v3"
PLAN_SCHEMA = "ownward.kernel-iteration-stage6-blind-plan/v2"
ADMISSION_SCHEMA = "ownward.kernel-iteration-stage6-blind-admission/v2"
RESULT_SCHEMA = "ownward.kernel-iteration-stage6-blind-result/v3"
RECOVERY_SCHEMA = "ownward.kernel-iteration-stage6-blind-recovery/v2"
SECRET_SCHEMA = "ownward.kernel-iteration-stage6-blind-secret/v2"
LOCATOR_SCHEMA = "ownward.kernel-iteration-stage6-blind-locator/v2"
STAGE5_FREEZE_RELATIVE = Path(".tmp/kernel-v2-major-iteration/stage5/freeze.json")


def load_contract(suite_root: Path, level: int) -> dict[str, Any]:
    suite_root = suite_root.resolve()
    _require(level in GATE_LEVELS, "阶段 6 盲测级别无效")
    value = _load_json(suite_root / CONTRACT_RELATIVES[level])
    _require(value.get("schema") == CONTRACT_SCHEMA, "阶段 6 候选盲测合同 schema 无效")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), "阶段 6 候选盲测合同身份漂移")
    _require(value.get("frozen_before_generation") is True and value.get("formal") is False, "阶段 6 候选盲测合同没有在生成前冻结")
    _require(value.get("level") == level, "阶段 6 候选盲测合同级别错绑")
    data = _mapping(value, "data")
    counts = _mapping(data, "coverage_counts")
    expected_count = level // len(validation.BLIND_COVERAGE)
    _require(data.get("questions") == level, "阶段 6 候选盲测题数漂移")
    _require(counts == {name: expected_count for name in validation.BLIND_COVERAGE}, "阶段 6 候选盲测覆盖漂移")
    _require(int(data.get("maximum_admission_batches", 0)) >= 2, "质量准入失败后没有冻结重新生成边界")
    execution = _mapping(value, "execution")
    _require(execution.get("order") == ["v2-candidate", "v0-baseline"] and execution.get("v0_requires_candidate_absolute_pass") is True, "候选与 V0 执行顺序漂移")
    _require(
        execution.get("generation_max_active") == 4
        and execution.get("generation_worker_active_turns_maximum") == 1
        and execution.get("generation_result_order") == "frozen-coverage-order"
        and execution.get("quality_admission_after_all_generation") is True,
        "阶段 6 生成调度合同漂移",
    )
    gate = _mapping(value, "absolute_gate")
    _require(gate == {
        "questions": level,
        "final_answer_accuracy_minimum": 1.0,
        "fact_delivery_missing_maximum": 0,
        "temporal_correctness_minimum": 1.0,
        "conflict_correctness_minimum": 1.0,
        "complete_consumer_retrieval_p95_ms_maximum": 553.0,
        "level_total_wall_seconds_maximum": LEVEL_BUDGETS[level],
        "read_limit": 8,
        "context_chars_maximum": 24000,
    }, "阶段 6 候选绝对门漂移")
    relative = _mapping(value, "relative_v0_gate")
    _require(relative.get("gate_role") == "sequential-early-rejection-not-standalone-overall-uplift-proof", "单级盲测不得冒充整体跃升证明")
    failure = _mapping(value, "failure")
    _require(
        failure.get("evaluation_process_failure_is_candidate_failure") is False
        and failure.get("whole_level_efficiency_gate_remains_mandatory") is True,
        "阶段 6 评测流程失败归属合同漂移",
    )
    previous, following = LEVEL_SEQUENCE[level]
    sequence = _mapping(value, "sequence")
    _require(sequence == {
        "previous_level": previous,
        "previous_pass_required": previous is not None,
        "next_level": following,
        "terminal": following is None,
    }, "阶段 6 候选盲测顺序漂移")
    return value


def run(
    suite_root: Path,
    output_root: Path,
    candidate_execution_config: Path,
    baseline_execution_config: Path,
    candidate_subject_manifest: Path,
    formal_state_path: Path,
    *,
    level: int,
    previous_plan_identity: str | None = None,
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
    contract = load_contract(suite_root, level)
    comparison = evidence.load_contract(suite_root)
    validation_contract = validation.load_validation_contract(suite_root)
    budget = validation.load_blind_budget_archive(suite_root)
    candidate_runtime = validation.validate_execution_config(suite_root, candidate_execution_config.resolve())
    baseline_runtime = validation.validate_execution_config(suite_root, baseline_execution_config.resolve())
    candidate = evidence.select_subject(comparison, None, candidate_subject_manifest.resolve())
    baseline = evidence.select_subject(comparison, "v0")
    _validate_subjects(contract, candidate, baseline, candidate_runtime, baseline_runtime)
    shared_conditions = _shared_conditions(candidate_runtime, baseline_runtime)
    state_path = formal_state_path.resolve()
    _require(state_path.is_file(), "阶段 6 候选盲测缺少正式 state 只读基线")
    state_before = state_path.read_bytes()
    runtime_calibration = evidence.calibrate_runtime(
        suite_root, output_root / "runtime-calibration", state_path, resume=resume,
    )
    _require(state_path.read_bytes() == state_before, "运行态校准改写了正式 state")

    _require(not (seed is not None and plan_identity is not None), "阶段 6 候选盲测不得同时按 seed 和 plan identity 恢复")
    if plan_identity is not None:
        _require(resume and evidence.is_sha256(plan_identity), "阶段 6 候选盲测 plan identity 无效")
        locator = _load_locator(output_root / "blind-gate" / plan_identity / "locator.json", plan_identity)
        _require(locator["level"] == level, "阶段 6 候选盲测恢复级别漂移")
        _require(locator.get("previous_plan_identity") == previous_plan_identity, "阶段 6 候选盲测前级恢复身份漂移")
        _require(Path(locator["candidate_execution_config"]).resolve() == candidate_execution_config.resolve(), "候选执行配置恢复路径漂移")
        _require(Path(locator["baseline_execution_config"]).resolve() == baseline_execution_config.resolve(), "V0 执行配置恢复路径漂移")
        _require(Path(locator["candidate_subject_manifest"]).resolve() == candidate_subject_manifest.resolve(), "候选 subject 恢复路径漂移")
        _require(Path(locator["formal_state"]).resolve() == state_path, "正式 state 恢复路径漂移")
        scratch = candidate_runtime["runs"] / "kernel-v2-blind-gate" / plan_identity
        secret = _load_secret(scratch / "recovery-secret.json", plan_identity)
        gate_seed = secret["gate_seed"]
    else:
        gate_seed = seed or secrets.token_hex(16)
    _require(len(gate_seed) >= 16 and all(character.isalnum() or character in "-_" for character in gate_seed), "阶段 6 候选盲测 seed 无效")

    previous_result_identity = _previous_gate_dependency(
        suite_root, output_root, contract, previous_plan_identity,
    )

    dependencies = _direct_dependencies(
        suite_root, contract, comparison, validation_contract, budget, candidate, baseline,
        candidate_runtime, baseline_runtime, runtime_calibration, shared_conditions,
    )
    if previous_result_identity is not None:
        dependencies["previous-gate-result"] = previous_result_identity
    plan_content = {
        "schema": PLAN_SCHEMA,
        "purpose": "v2-sequential-one-time-blind-gate",
        "level": level,
        "previous_plan_identity": previous_plan_identity,
        "candidate_decision": "pending",
        "gate_contract_identity": contract["identity"],
        "comparison_contract_identity": comparison["identity"],
        "validation_contract_identity": validation_contract["identity"],
        "candidate_subject_identity": candidate["identity"],
        "candidate_kernel_generation_identity": candidate["content"]["kernel_generation_identity"],
        "candidate_kernel_effect_identity": candidate["content"]["kernel_effect_identity"],
        "baseline_subject_identity": baseline["identity"],
        "seed_sha256": hashlib.sha256(gate_seed.encode("utf-8")).hexdigest(),
        "shared_conditions": shared_conditions,
        "direct_dependencies": dict(sorted(dependencies.items())),
        "formal": False,
    }
    computed_identity = evidence.canonical_sha256(plan_content)
    if plan_identity is not None:
        _require(plan_identity == computed_identity, "阶段 6 候选盲测 plan identity 与当前直接依赖不一致")
    else:
        plan_identity = computed_identity
    plan = {**plan_content, "identity": plan_identity}
    root = output_root / "blind-gate" / plan_identity
    result_path = root / "result.json"
    plan_path = root / "plan.json"
    if result_path.is_file():
        _require(resume and plan_path.is_file() and _load_json(plan_path) == plan, "阶段 6 候选盲测终态只能由同一身份恢复")
        result = _load_json(result_path)
        _validate_result(result, plan_identity)
        _require(state_path.read_bytes() == state_before, "阶段 6 终态复用改写了正式 state")
        return _terminal_reference(result_path, result, reused=True)
    if plan_path.is_file():
        _require(resume and _load_json(plan_path) == plan, "既有阶段 6 候选盲测计划身份漂移")
    else:
        evidence.atomic_json(plan_path, plan)

    scratch = candidate_runtime["runs"] / "kernel-v2-blind-gate" / plan_identity
    _require(candidate_runtime["runs"] == baseline_runtime["runs"], "候选与 V0 没有共享同一持久运行根")
    _initialize_recovery(
        root, scratch, plan_identity, gate_seed, plan["seed_sha256"],
        candidate_execution_config, baseline_execution_config, candidate_subject_manifest, state_path,
        level, previous_plan_identity,
    )
    execute = runner or validation._run_longmemeval
    started = time.perf_counter()
    generation_usages: list[dict[str, Any]] = []
    admission_usages: list[dict[str, Any]] = []
    excluded_rejection_wall_seconds = 0.0
    rejection_path = root / "admission-rejections.json"
    rejection_state = _load_json(rejection_path) if rejection_path.is_file() else {"schema": "ownward.kernel-iteration-stage6-admission-rejections/v1", "plan_identity": plan_identity, "batches": []}
    _require(rejection_state.get("schema") == "ownward.kernel-iteration-stage6-admission-rejections/v1" and rejection_state.get("plan_identity") == plan_identity, "阶段 6 候选盲测准入拒绝恢复状态错绑")
    rejected_batches = list(rejection_state.get("batches", []))
    _require(all(isinstance(item, dict) and isinstance(item.get("attempt"), int) for item in rejected_batches), "阶段 6 候选盲测准入拒绝恢复状态无效")
    generation_usages.extend(dict(item.get("generation_usage", {})) for item in rejected_batches)
    admission_usages.extend(dict(item.get("admission_usage", {})) for item in rejected_batches)
    generation_scheduler = {
        "policy": "bounded-independent-lanes-original-order/v1",
        "max_active_limit": int(_mapping(contract, "execution")["generation_max_active"]),
        "max_active_observed": max((int(_mapping(item, "generation_scheduler").get("max_active_observed", 0)) for item in rejected_batches if isinstance(item.get("generation_scheduler"), dict)), default=0),
        "submitted": sum(int(_mapping(item, "generation_scheduler").get("submitted", 0)) for item in rejected_batches if isinstance(item.get("generation_scheduler"), dict)),
        "per_worker_max_active_turns": 1,
        "result_order": "frozen-coverage-order",
    }
    materials: dict[str, Any] | None = None
    admission: dict[str, Any] | None = None
    admitted_attempt = 0
    try:
        with ExitStack() as invocation_stack:
            lane_invokers = (
                [invoker]
                if invoker is not None
                else invocation_stack.enter_context(_native_generation_invokers(
                    suite_root,
                    candidate_runtime,
                    scratch / "generation-transport",
                    generation_scheduler["max_active_limit"],
                ))
            )
            for attempt in range(1, int(_mapping(contract, "data")["maximum_admission_batches"]) + 1):
                if any(item["attempt"] == attempt for item in rejected_batches):
                    continue
                attempt_started = time.perf_counter()
                attempt_root = scratch / f"batch-{attempt:02d}"
                attempt_seed = hashlib.sha256(f"{gate_seed}:{attempt}".encode("utf-8")).hexdigest()
                generated, usages, attempt_scheduler = _generate_cases(
                    suite_root, candidate_runtime, validation_contract, contract,
                    attempt_root, attempt_seed, lane_invokers,
                )
                generation_scheduler["max_active_observed"] = max(
                    int(generation_scheduler["max_active_observed"]), int(attempt_scheduler["max_active_observed"]),
                )
                generation_scheduler["submitted"] = int(generation_scheduler["submitted"]) + int(attempt_scheduler["submitted"])
                generation_usages.append(validation._combine_usages(usages))
                candidate_materials = _materials_from_generated(generated, contract)
                validation.validate_materials(candidate_materials, expected_questions=level)
                _validate_material_isolation(suite_root, output_root, candidate_materials)
                admission_output, admission_usage = lane_invokers[0](
                    suite_root=suite_root,
                    runtime=candidate_runtime,
                    stage=attempt_root / "quality-admission",
                    role="quality-admission",
                    prompt=validation._admission_prompt(validation_contract, candidate_materials),
                    schema=validation._admission_schema([case["case_id"] for case in candidate_materials["cases"]]),
                    settings=_mapping(_mapping(validation_contract, "blind"), "quality_admission"),
                    validate=lambda value: validation.validate_admission(value, candidate_materials, validation_contract),
                )
                admission_usages.append(admission_usage)
                candidate_admission = validation.validate_admission(admission_output, candidate_materials, validation_contract)
                controls = validation.score_controls(candidate_materials)
                _require(controls["passed"], "阶段 6 候选盲测评分控制不能区分关键错误")
                if not candidate_admission["passed"]:
                    rejection_wall = time.perf_counter() - attempt_started
                    excluded_rejection_wall_seconds += rejection_wall
                    rejected_batches.append({
                        "attempt": attempt,
                        "material_identity": candidate_materials["identity"],
                        "rejected_count": candidate_admission["rejected_count"],
                        "generation_usage": validation._sanitize_usage(generation_usages[-1]),
                        "admission_usage": validation._sanitize_usage(admission_usages[-1]),
                        "generation_scheduler": attempt_scheduler,
                        "wall_seconds": rejection_wall,
                    })
                    evidence.atomic_json(rejection_path, {
                        "schema": "ownward.kernel-iteration-stage6-admission-rejections/v1",
                        "plan_identity": plan_identity,
                        "batches": rejected_batches,
                    })
                    _destroy_attempt(attempt_root, scratch)
                    continue
                materials = candidate_materials
                admission = candidate_admission
                admitted_attempt = attempt
                admitted_content = {
                    "schema": ADMISSION_SCHEMA,
                    "plan_identity": plan_identity,
                    "attempt": attempt,
                    "material_identity": materials["identity"],
                    "case_fact_identities": [validation._case_fact_identity(case) for case in materials["cases"]],
                    "coverage_counts": {name: sum(case["coverage"] == name for case in materials["cases"]) for name in validation.BLIND_COVERAGE},
                    "quality_admission": admission,
                    "control_discrimination": controls,
                    "candidate_has_not_run": True,
                }
                evidence.atomic_json(root / "admission.json", {**admitted_content, "identity": evidence.canonical_sha256(admitted_content)})
                break
        if materials is None or admission is None:
            return _finish(
                root, scratch, candidate_runtime["runs"], state_path, state_before, plan, contract,
                status="quality-admission-exhausted", passed=False, candidate_decision=None,
                admitted_attempt=0, materials=None, admission=None, rejected_batches=rejected_batches,
                generation_usage=validation._combine_usages(generation_usages), admission_usage=validation._combine_usages(admission_usages),
                generation_scheduler=generation_scheduler, evaluation_process_decision=None,
                candidate_execution=None, baseline_execution=None, absolute=None, relative=None,
                resume_proof=None, general_root_cause=None, started=started,
                excluded_rejection_wall_seconds=excluded_rejection_wall_seconds,
            )

        dataset_path = scratch / f"batch-{admitted_attempt:02d}" / "dataset.json"
        evidence.atomic_json(dataset_path, [validation._longmemeval_case(case) for case in materials["cases"]])
        candidate_execution = _execute(
            suite_root, candidate_runtime, dataset_path, scratch / "candidate", candidate["identity"], materials,
            resume=resume, runner=execute,
        )
        absolute = _absolute_decision(candidate_execution["observation"], contract)
        if not absolute["passed"]:
            root_cause = _general_root_cause(candidate_execution["observation"], absolute["failures"])
            return _finish(
                root, scratch, candidate_runtime["runs"], state_path, state_before, plan, contract,
                status="candidate-rejected", passed=False, candidate_decision=False,
                admitted_attempt=admitted_attempt, materials=materials, admission=admission, rejected_batches=rejected_batches,
                generation_usage=validation._combine_usages(generation_usages), admission_usage=validation._combine_usages(admission_usages),
                generation_scheduler=generation_scheduler, evaluation_process_decision=None,
                candidate_execution=candidate_execution, baseline_execution=None, absolute=absolute, relative=None,
                resume_proof=None, general_root_cause=root_cause, started=started,
                excluded_rejection_wall_seconds=excluded_rejection_wall_seconds,
            )

        baseline_execution = _execute(
            suite_root, baseline_runtime, dataset_path, scratch / "baseline", baseline["identity"], materials,
            resume=resume, runner=execute,
        )
        relative = _relative_decision(candidate_execution["observation"], baseline_execution["observation"])
        resume_proof = _resume_proof(
            suite_root, candidate_runtime, baseline_runtime, dataset_path, scratch,
            candidate["identity"], baseline["identity"], execute,
        )
        total = _decision_wall_seconds(started, excluded_rejection_wall_seconds)
        wall_limit = float(_mapping(contract, "absolute_gate")["level_total_wall_seconds_maximum"])
        candidate_decision = bool(relative["passed"])
        evaluation_process_decision = {
            "passed": total <= wall_limit,
            "failures": [] if total <= wall_limit else [{"metric": "level_total_wall_seconds", "actual": total, "maximum": wall_limit}],
            "candidate_failure": False,
        }
        passed = bool(candidate_decision and evaluation_process_decision["passed"])
        if passed:
            status = "passed"
            root_cause = None
        elif not candidate_decision:
            status = "relative-rejected"
            root_cause = _general_root_cause(candidate_execution["observation"], relative["failures"])
        else:
            status = "evaluation-process-rejected"
            root_cause = {
                "first_observed_gap": None,
                "responsible_direction": "stage6-evaluation-controller",
                "mechanism_status": "requires-independent-process-attribution-and-repair",
                "failure_metrics": ["level_total_wall_seconds"],
            }
        return _finish(
            root, scratch, candidate_runtime["runs"], state_path, state_before, plan, contract,
            status=status, passed=passed, candidate_decision=candidate_decision,
            admitted_attempt=admitted_attempt, materials=materials, admission=admission, rejected_batches=rejected_batches,
            generation_usage=validation._combine_usages(generation_usages), admission_usage=validation._combine_usages(admission_usages),
            generation_scheduler=generation_scheduler, evaluation_process_decision=evaluation_process_decision,
            candidate_execution=candidate_execution, baseline_execution=baseline_execution, absolute=absolute, relative=relative,
            resume_proof=resume_proof, general_root_cause=root_cause, started=started,
            excluded_rejection_wall_seconds=excluded_rejection_wall_seconds,
        )
    except (KeyboardInterrupt, InterruptedError):
        _require(state_path.read_bytes() == state_before, "阶段 6 候选盲测中断路径改写了正式 state")
        raise
    except Exception:
        _require(state_path.read_bytes() == state_before, "阶段 6 候选盲测失败路径改写了正式 state")
        raise


def resume_by_plan_identity(
    suite_root: Path,
    output_root: Path,
    plan_identity: str,
    *,
    invoker: Callable[..., tuple[dict[str, Any], dict[str, Any]]] | None = None,
    runner: Callable[..., dict[str, Any]] | None = None,
) -> dict[str, Any]:
    suite_root = suite_root.resolve()
    output_root = output_root.resolve()
    root = output_root / "blind-gate" / plan_identity
    _require(evidence.is_sha256(plan_identity), "阶段 6 候选盲测 plan identity 无效")
    plan = _load_json(root / "plan.json")
    _validate_plan(plan, plan_identity)
    locator = _load_locator(root / "locator.json", plan_identity)
    result_path = root / "result.json"
    if result_path.is_file():
        current = _current_dependencies(suite_root, locator)
        _require(current == dict(_mapping(plan, "direct_dependencies")), "阶段 6 候选盲测终态直接依赖已漂移")
        result = _load_json(result_path)
        _validate_result(result, plan_identity)
        _require(not (root / "active.json").exists(), "阶段 6 候选盲测终态仍有活动恢复文件")
        return {**_terminal_reference(result_path, result, reused=True), "model_calls": 0, "product_executions": 0, "current_dependencies_valid": True}
    return run(
        suite_root, output_root,
        Path(locator["candidate_execution_config"]), Path(locator["baseline_execution_config"]),
        Path(locator["candidate_subject_manifest"]), Path(locator["formal_state"]),
        level=int(locator["level"]), previous_plan_identity=locator.get("previous_plan_identity"),
        plan_identity=plan_identity, resume=True, invoker=invoker, runner=runner,
    )


def _execute(
    suite_root: Path,
    runtime: dict[str, Any],
    dataset_path: Path,
    output_dir: Path,
    subject_identity: str,
    materials: dict[str, Any],
    *,
    resume: bool,
    runner: Callable[..., dict[str, Any]],
) -> dict[str, Any]:
    report = runner(
        suite_root=suite_root, runtime=runtime, dataset_path=dataset_path,
        output_dir=output_dir, subject_identity=subject_identity, resume=resume,
    )
    summary_path = output_dir / "diagnostic-summary.json"
    summary = _load_json(summary_path)
    observation = validation.observe_report({**report, "diagnostic_summary": summary}, materials)
    return {
        "subject_identity": subject_identity,
        "report_sha256": evidence.file_sha256(output_dir / "report.json"),
        "checkpoint_sha256": evidence.file_sha256(output_dir / "checkpoint-manifest.json"),
        "diagnostic_summary_sha256": evidence.file_sha256(summary_path),
        "observation": observation,
    }


def _absolute_decision(observation: dict[str, Any], contract: dict[str, Any]) -> dict[str, Any]:
    gate = _mapping(contract, "absolute_gate")
    checks = [
        ("questions", observation["questions"], gate["questions"], lambda actual, expected: actual == expected),
        ("final_answer_accuracy", observation["final_answer_accuracy"], gate["final_answer_accuracy_minimum"], lambda actual, expected: float(actual) >= float(expected)),
        ("fact_delivery_missing", observation["fact_delivery"]["missing_questions"], gate["fact_delivery_missing_maximum"], lambda actual, expected: int(actual) <= int(expected)),
        ("retrieval_p95_ms", observation["latency"]["retrieval_p95_ms"], gate["complete_consumer_retrieval_p95_ms_maximum"], lambda actual, expected: float(actual) <= float(expected)),
    ]
    for name, field in (("temporal_correctness", "temporal_correctness_minimum"), ("conflict_correctness", "conflict_correctness_minimum")):
        if observation[name] is not None:
            checks.append((name, observation[name], gate[field], lambda actual, expected: float(actual) >= float(expected)))
    failures = [
        {"metric": name, "actual": actual, "required": expected}
        for name, actual, expected, predicate in checks if actual is None or not predicate(actual, expected)
    ]
    return {"passed": not failures, "failures": failures}


def _relative_decision(candidate: dict[str, Any], baseline: dict[str, Any]) -> dict[str, Any]:
    comparisons = [
        ("final_answer_accuracy", candidate["final_answer_accuracy"], baseline["final_answer_accuracy"], lambda left, right: float(left) >= float(right)),
        ("fact_delivery_missing", candidate["fact_delivery"]["missing_questions"], baseline["fact_delivery"]["missing_questions"], lambda left, right: int(left) <= int(right)),
        ("semantic_input_tokens", candidate["resources"]["semantic_input_tokens"], baseline["resources"]["semantic_input_tokens"], lambda left, right: int(left) <= int(right)),
        ("ownward_data_bytes", candidate["resources"]["ownward_data_bytes"], baseline["resources"]["ownward_data_bytes"], lambda left, right: int(left) <= int(right)),
    ]
    for name in ("temporal_correctness", "conflict_correctness"):
        if candidate[name] is not None and baseline[name] is not None:
            comparisons.append((name, candidate[name], baseline[name], lambda left, right: float(left) >= float(right)))
    failures = [
        {"metric": name, "candidate": left, "v0": right}
        for name, left, right, predicate in comparisons if left is None or right is None or not predicate(left, right)
    ]
    return {"passed": not failures, "failures": failures, "retrieval_latency_policy": "candidate-absolute-complete-consumer-gate-only"}


def _coverage_schedule(contract: dict[str, Any]) -> list[str]:
    counts = _mapping(_mapping(contract, "data"), "coverage_counts")
    schedule = [
        coverage
        for depth in range(max(int(value) for value in counts.values()))
        for coverage in validation.BLIND_COVERAGE
        if depth < int(counts[coverage])
    ]
    _require(len(schedule) == int(_mapping(contract, "data")["questions"]), "阶段 6 候选盲测覆盖计划题数漂移")
    return schedule


def _decision_wall_seconds(started: float, excluded_rejection_wall_seconds: float) -> float:
    _require(excluded_rejection_wall_seconds >= 0.0, "准入拒绝墙钟不能为负")
    return max(0.0, time.perf_counter() - started - excluded_rejection_wall_seconds)


@contextmanager
def _native_generation_invokers(
    suite_root: Path,
    runtime: dict[str, Any],
    transport_parent: Path,
    max_active: int,
) -> Any:
    _require(max_active > 0, "阶段 6 生成并发必须为正")
    with ExitStack() as stack:
        invokers = [
            stack.enter_context(validation._native_codex_batch_invoker(
                suite_root, runtime, transport_parent / f"worker-{index + 1:02d}",
            ))
            for index in range(max_active)
        ]
        yield invokers


def _generate_cases(
    suite_root: Path,
    runtime: dict[str, Any],
    validation_contract: dict[str, Any],
    contract: dict[str, Any],
    attempt_root: Path,
    attempt_seed: str,
    invokers: list[Callable[..., tuple[dict[str, Any], dict[str, Any]]]],
) -> tuple[list[dict[str, Any]], list[dict[str, Any]], dict[str, Any]]:
    _require(bool(invokers), "阶段 6 生成调度缺少 worker")
    work = [
        (index, coverage, f"g{attempt_root.name.rsplit('-', 1)[-1]}-c{index:02d}")
        for index, coverage in enumerate(_coverage_schedule(contract), start=1)
    ]
    lanes: list[list[tuple[int, str, str]]] = [[] for _ in invokers]
    for index, item in enumerate(work):
        lanes[index % len(lanes)].append(item)
    activity_lock = threading.Lock()
    active = 0
    maximum = 0

    def run_lane(
        invoke: Callable[..., tuple[dict[str, Any], dict[str, Any]]],
        items: list[tuple[int, str, str]],
    ) -> list[tuple[int, dict[str, Any], dict[str, Any]]]:
        nonlocal active, maximum
        completed: list[tuple[int, dict[str, Any], dict[str, Any]]] = []
        for index, coverage, case_id in items:
            with activity_lock:
                active += 1
                maximum = max(maximum, active)
            try:
                output, usage = invoke(
                    suite_root=suite_root,
                    runtime=runtime,
                    stage=attempt_root / "generator" / case_id,
                    role="generator",
                    prompt=_generator_prompt(validation_contract, attempt_seed, case_id, coverage),
                    schema=validation._generator_case_schema(case_id, coverage, validation_contract),
                    settings=_mapping(_mapping(validation_contract, "blind"), "generation"),
                    validate=lambda value, expected_id=case_id, expected_coverage=coverage: validation._validate_generated_case(value, expected_id, expected_coverage, validation_contract),
                )
            finally:
                with activity_lock:
                    active -= 1
            completed.append((
                index,
                validation._validate_generated_case(output, case_id, coverage, validation_contract),
                usage,
            ))
        return completed

    completed: list[tuple[int, dict[str, Any], dict[str, Any]]] = []
    with ThreadPoolExecutor(max_workers=len(invokers), thread_name_prefix="stage6-generator") as pool:
        futures = [pool.submit(run_lane, invokers[index], lane) for index, lane in enumerate(lanes) if lane]
        for future in futures:
            completed.extend(future.result())
    completed.sort(key=lambda item: item[0])
    _require([item[0] for item in completed] == list(range(1, len(work) + 1)), "阶段 6 生成结果没有按冻结原序重组")
    return (
        [item[1] for item in completed],
        [item[2] for item in completed],
        {
            "policy": "bounded-independent-lanes-original-order/v1",
            "max_active_limit": len(invokers),
            "max_active_observed": maximum,
            "submitted": len(work),
            "per_worker_max_active_turns": 1,
            "result_order": "frozen-coverage-order",
        },
    )


def _materials_from_generated(cases: list[dict[str, Any]], contract: dict[str, Any]) -> dict[str, Any]:
    projected = [validation._case_projection(case) for case in cases]
    _require([case["coverage"] for case in projected] == _coverage_schedule(contract), "阶段 6 候选盲测没有按冻结配额原序输出")
    content = {
        "schema": validation.MATERIALS_SCHEMA,
        "contains_formal_questions_answers_gold_or_content": False,
        "cases": projected,
        "criteria": {"minimum_accuracy": 0.0, "require_complete_fact_delivery": True, "category_minimums": {}},
    }
    return {**content, "identity": evidence.canonical_sha256(content)}


def _previous_gate_dependency(
    suite_root: Path,
    output_root: Path,
    contract: dict[str, Any],
    previous_plan_identity: str | None,
) -> str | None:
    sequence = _mapping(contract, "sequence")
    previous_level = sequence["previous_level"]
    if previous_level is None:
        _require(previous_plan_identity is None, "首级盲测不得声明前级计划")
        return None
    _require(isinstance(previous_plan_identity, str) and evidence.is_sha256(previous_plan_identity), "后续盲测缺少有效前级计划身份")
    root = output_root / "blind-gate" / str(previous_plan_identity)
    plan = _load_json(root / "plan.json")
    _validate_plan(plan, str(previous_plan_identity))
    _require(plan.get("level") == previous_level, "前级盲测计划级别错绑")
    locator = _load_locator(root / "locator.json", str(previous_plan_identity))
    _require(_current_dependencies(suite_root, locator) == dict(_mapping(plan, "direct_dependencies")), "前级盲测直接依赖已漂移")
    result = _load_json(root / "result.json")
    _validate_result(result, str(previous_plan_identity))
    _require(result.get("passed") is True and result.get("candidate_decision") is True, "前级盲测没有通过")
    _require(result.get("next_level") == contract["level"], "前级盲测没有授权当前级别")
    return str(result["identity"])


def _failure_transition(
    contract: dict[str, Any],
    status: str,
    passed: bool,
    candidate_decision: bool | None,
    root_cause: dict[str, Any] | None,
) -> dict[str, Any] | None:
    if passed:
        return None
    if candidate_decision is None:
        return {
            "candidate_failed": False,
            "reason": status,
            "next_action": "generate-a-fresh-plan-after-quality-admission-exhaustion",
            "reuse_rejected_content": False,
        }
    if candidate_decision is True:
        return {
            "candidate_failed": False,
            "reason": status,
            "general_root_cause": root_cause,
            "return_to_stage": 3,
            "independent_nonoverlapping_reproduction_required": True,
            "same_blind_content_rerun_forbidden": True,
            "required_loop": [
                "destroy-current-blind-content",
                "repair-and-requalify-stage6-evaluation-process",
                "restart-stage6-from-a-fresh-five-question-gate",
            ],
        }
    failure = _mapping(contract, "failure")
    return {
        "candidate_failed": True,
        "return_to_stage": failure["return_to_stage"],
        "general_root_cause": root_cause,
        "independent_nonoverlapping_reproduction_required": failure["independent_nonoverlapping_reproduction_required"],
        "same_blind_content_rerun_forbidden": failure["same_blind_content_rerun_forbidden"],
        "required_loop": [
            "destroy-current-blind-content",
            "reproduce-general-root-cause-on-independent-development-and-regression-material",
            "optimize-and-revalidate",
            "refreeze-candidate",
            "restart-stage6-from-a-fresh-five-question-gate",
        ],
    }


def _next_action(contract: dict[str, Any], status: str, passed: bool, candidate_decision: bool | None) -> str:
    if passed:
        following = _mapping(contract, "sequence")["next_level"]
        return "stage7-final-handoff" if following is None else f"run-fresh-{following}-question-gate"
    if candidate_decision is None:
        return "generate-a-fresh-plan-after-quality-admission-exhaustion"
    if candidate_decision is True:
        return "return-to-stage3-evaluation-process-attribution-and-repair"
    return "return-to-stage3-independent-reproduction-and-optimization"


def _resume_proof(
    suite_root: Path,
    candidate_runtime: dict[str, Any],
    baseline_runtime: dict[str, Any],
    dataset_path: Path,
    scratch: Path,
    candidate_identity: str,
    baseline_identity: str,
    runner: Callable[..., dict[str, Any]],
) -> dict[str, Any]:
    proofs = []
    for name, runtime, subject_identity in (
        ("candidate", candidate_runtime, candidate_identity),
        ("baseline", baseline_runtime, baseline_identity),
    ):
        run_root = scratch / name
        before_report = (run_root / "report.json").read_bytes()
        before_checkpoint = (run_root / "checkpoint-manifest.json").read_bytes()
        runner(
            suite_root=suite_root, runtime=runtime, dataset_path=dataset_path,
            output_dir=run_root, subject_identity=subject_identity, resume=True,
        )
        proofs.append({
            "subject": name,
            "report_byte_identical": before_report == (run_root / "report.json").read_bytes(),
            "checkpoint_byte_identical": before_checkpoint == (run_root / "checkpoint-manifest.json").read_bytes(),
            "model_calls": 0,
            "product_executions": 0,
        })
    _require(all(item["report_byte_identical"] and item["checkpoint_byte_identical"] for item in proofs), "阶段 6 候选盲测恢复未逐字复用")
    return {"subjects": proofs, "passed": True}


def _finish(
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
    candidate_decision: bool | None,
    admitted_attempt: int,
    materials: dict[str, Any] | None,
    admission: dict[str, Any] | None,
    rejected_batches: list[dict[str, Any]],
    generation_usage: dict[str, Any],
    admission_usage: dict[str, Any],
    generation_scheduler: dict[str, Any],
    evaluation_process_decision: dict[str, Any] | None,
    candidate_execution: dict[str, Any] | None,
    baseline_execution: dict[str, Any] | None,
    absolute: dict[str, Any] | None,
    relative: dict[str, Any] | None,
    resume_proof: dict[str, Any] | None,
    general_root_cause: dict[str, Any] | None,
    started: float,
    excluded_rejection_wall_seconds: float,
) -> dict[str, Any]:
    decision_wall = _decision_wall_seconds(started, excluded_rejection_wall_seconds)
    rejected_wall = sum(float(item.get("wall_seconds", 0.0)) for item in rejected_batches)
    operational_wall = decision_wall + rejected_wall
    execution_aggregates = {
        "candidate": _execution_aggregate(candidate_execution),
        "v0": _execution_aggregate(baseline_execution),
    }
    material_identity = materials["identity"] if materials is not None else None
    case_identities = [validation._case_fact_identity(case) for case in materials["cases"]] if materials is not None else []
    _destroy_scratch(scratch, runs_root)
    (root / "active.json").unlink(missing_ok=True)
    content = {
        "schema": RESULT_SCHEMA,
        "plan_identity": plan["identity"],
        "level": plan["level"],
        "previous_plan_identity": plan.get("previous_plan_identity"),
        "gate_contract_identity": contract["identity"],
        "status": status,
        "passed": passed,
        "candidate_decision": candidate_decision,
        "formal": False,
        "formal_state_written": False,
        "raw_materials_destroyed": True,
        "contains_reversible_question_answer_or_evidence": False,
        "admitted_attempt": admitted_attempt,
        "material_identity": material_identity,
        "case_fact_identities": case_identities,
        "coverage_counts": ({name: sum(case["coverage"] == name for case in materials["cases"]) for name in validation.BLIND_COVERAGE} if materials is not None else {}),
        "quality_admission": admission,
        "rejected_batches": rejected_batches,
        "generation_usage": validation._sanitize_usage(generation_usage),
        "admission_usage": validation._sanitize_usage(admission_usage),
        "generation_scheduler": generation_scheduler,
        "executions": execution_aggregates,
        "absolute_decision": absolute,
        "relative_v0_decision": relative,
        "evaluation_process_decision": evaluation_process_decision,
        "resume_proof": resume_proof,
        "general_root_cause": general_root_cause,
        "failure_transition": _failure_transition(contract, status, passed, candidate_decision, general_root_cause),
        "cost_and_recovery": {
            "candidate_decision_wall_seconds": decision_wall,
            "admission_rejection_wall_seconds": rejected_wall,
            "operational_total_wall_seconds": operational_wall,
            "level_budget_seconds": _mapping(contract, "absolute_gate")["level_total_wall_seconds_maximum"],
            "bounded_infrastructure_retry_only": True,
            "admission_rejection_not_candidate_failure": True,
        },
        "next_level": _mapping(contract, "sequence")["next_level"] if passed else None,
        "stage6_complete": bool(passed and _mapping(contract, "sequence")["terminal"] is True),
        "next_action": _next_action(contract, status, passed, candidate_decision),
    }
    result = {**content, "identity": evidence.canonical_sha256(content)}
    evidence.atomic_json(root / "result.json", result)
    _require(state_path.read_bytes() == state_before, "阶段 6 候选盲测终态改写了正式 state")
    _require(not scratch.exists(), "阶段 6 候选盲测终态仍保留可还原原始数据")
    return _terminal_reference(root / "result.json", result, reused=False)


def _execution_aggregate(value: dict[str, Any] | None) -> dict[str, Any] | None:
    if value is None:
        return None
    observation = value["observation"]
    return {
        "subject_identity": value["subject_identity"],
        "report_sha256": value["report_sha256"],
        "checkpoint_sha256": value["checkpoint_sha256"],
        "diagnostic_summary_sha256": value["diagnostic_summary_sha256"],
        "questions": observation["questions"],
        "fact_delivery": observation["fact_delivery"],
        "final_answer_accuracy": observation["final_answer_accuracy"],
        "temporal_correctness": observation["temporal_correctness"],
        "conflict_correctness": observation["conflict_correctness"],
        "latency": observation["latency"],
        "resources": observation["resources"],
        "codex": validation._sanitize_codex(observation.get("codex")),
    }


def _general_root_cause(observation: dict[str, Any], failures: list[dict[str, Any]]) -> dict[str, Any]:
    gaps = _mapping(_mapping(observation, "fact_delivery"), "by_first_observed_gap")
    first_gap = next((name for name, count in sorted(gaps.items()) if name != "none" and int(count) > 0), None)
    if first_gap in {"target_evidence_not_search_returned"}:
        direction = "retrieval-architecture"
    elif first_gap in {"target_evidence_not_read"}:
        direction = "information-organization-and-delivery"
    elif first_gap in validation.ANSWER_ONLY_GAPS:
        direction = "final-answer-sufficiency-boundary"
    else:
        direction = "execution-or-resource-boundary" if failures else "unproven"
    return {
        "first_observed_gap": first_gap,
        "responsible_direction": direction,
        "mechanism_status": "requires-independent-nonoverlapping-reproduction",
        "failure_metrics": [item.get("metric") for item in failures],
    }


def _validate_subjects(
    contract: dict[str, Any],
    candidate: dict[str, Any],
    baseline: dict[str, Any],
    candidate_runtime: dict[str, Any],
    baseline_runtime: dict[str, Any],
) -> None:
    expected_candidate = _mapping(contract, "candidate")
    expected_baseline = _mapping(contract, "baseline")
    _require(candidate["role"] == expected_candidate["role"], "阶段 6 候选 subject 角色无效")
    _require(candidate["content"].get("kernel_generation_identity") == expected_candidate["kernel_generation_identity"], "阶段 6 候选内核世代漂移")
    _require(candidate["content"].get("kernel_effect_identity") == expected_candidate["kernel_effect_identity"], "阶段 6 候选内核效果漂移")
    _require(baseline["role"] == expected_baseline["role"], "阶段 6 V0 角色无效")
    _require(baseline["content"].get("kernel_generation_identity") == expected_baseline["kernel_generation_identity"], "阶段 6 V0 世代漂移")
    _require(baseline["content"].get("kernel_effect_identity") == expected_baseline["kernel_effect_identity"], "阶段 6 V0 效果身份漂移")
    validation._verify_subject_binary(candidate, candidate_runtime["binary"])
    validation._verify_subject_binary(baseline, baseline_runtime["binary"])


def _shared_conditions(candidate: dict[str, Any], baseline: dict[str, Any]) -> dict[str, str]:
    _require(evidence.file_sha256(candidate["environment_manifest"]) == evidence.file_sha256(baseline["environment_manifest"]), "候选与 V0 环境不同")
    _require(evidence.file_sha256(candidate["protocol"]) == evidence.file_sha256(baseline["protocol"]), "候选与 V0 协议不同")
    _require(evidence.file_sha256(candidate["codex_binary"]) == evidence.file_sha256(baseline["codex_binary"]), "候选与 V0 Codex 执行器不同")
    _require(candidate["codex_auth_file"].resolve() == baseline["codex_auth_file"].resolve(), "候选与 V0 Codex 身份来源不同")
    candidate_bundle = _load_json(candidate["embedding"] / "manifest.json")
    baseline_bundle = _load_json(baseline["embedding"] / "manifest.json")
    vector_projection = lambda value: {
        "schema": value.get("schema"),
        "capability": value.get("capability"),
        "model_sha256": _mapping(value, "model").get("sha256"),
        "space": _mapping(value, "space"),
    }
    _require(vector_projection(candidate_bundle) == vector_projection(baseline_bundle), "候选与 V0 向量模型或空间条件不同")
    protocol = candidate["protocol_value"]
    return dict(sorted({
        "environment": evidence.file_sha256(candidate["environment_manifest"]),
        "protocol": evidence.file_sha256(candidate["protocol"]),
        "codex-executor": evidence.file_sha256(candidate["codex_binary"]),
        "model-profile": evidence.canonical_sha256({"memory": protocol["memory"], "reader": protocol["reader"], "judge": protocol["judge"]}),
        "vector-semantics": evidence.canonical_sha256(vector_projection(candidate_bundle)),
        "question-concurrency": evidence.canonical_sha256({"question": 4, "codex": 8}),
    }.items()))


def _direct_dependencies(
    suite_root: Path,
    contract: dict[str, Any],
    comparison: dict[str, Any],
    validation_contract: dict[str, Any],
    budget: dict[str, Any],
    candidate: dict[str, Any],
    baseline: dict[str, Any],
    candidate_runtime: dict[str, Any],
    baseline_runtime: dict[str, Any],
    runtime_calibration: dict[str, Any],
    shared_conditions: dict[str, str],
) -> dict[str, str]:
    repository = suite_root.parents[2]
    stage5_freeze = _load_json(repository / STAGE5_FREEZE_RELATIVE)
    _require(stage5_freeze.get("runtime_identity") == _mapping(contract, "candidate")["kernel_generation_identity"], "Stage 5 冻结运行身份漂移")
    _require(stage5_freeze.get("source_subject_identity") == _mapping(contract, "candidate")["source_subject_identity"], "Stage 5 冻结源 subject 漂移")
    _require(stage5_freeze.get("rebuilt_packaging_subject_identity") == candidate["identity"], "阶段 6 盲测候选不是 Stage 5 干净重建制品")
    implementation = _implementation_identity()
    blind = _mapping(validation_contract, "blind")
    long_root = repository / "benchmarks" / "longmemeval_s"
    adapter = suite_root / "kernel_iteration_longmemeval.py"
    mcp_transport = repository / "benchmarks" / "support" / "ownward_mcp.py"
    evaluator = Path(str(_mapping(candidate_runtime["environment"], "layout")["source"])) / "src" / "evaluation" / "evaluate_qa.py"
    executor_identity = evidence.canonical_sha256({
        "longmemeval": evidence.file_sha256(long_root / "run.py"),
        "codex-transport": evidence.file_sha256(long_root / "codex_app_server.py"),
        "nonformal-adapter": evidence.file_sha256(adapter),
        "ownward-mcp-transport": evidence.file_sha256(mcp_transport),
        "semantic-representation-runtime": evidence.file_sha256(long_root / "semantic_representation.py"),
        "protocol": evidence.file_sha256(candidate_runtime["protocol"]),
    })
    return {
        "gate-contract": contract["identity"],
        "comparison-contract": comparison["identity"],
        "validation-contract": validation_contract["identity"],
        "blind-budget": budget["identity"],
        "stage5-freeze": stage5_freeze["identity"],
        "candidate-subject": candidate["identity"],
        "baseline-subject": baseline["identity"],
        "candidate-binary": evidence.file_sha256(candidate_runtime["binary"]),
        "baseline-binary": evidence.file_sha256(baseline_runtime["binary"]),
        "candidate-embedding": evidence.file_sha256(candidate_runtime["embedding"] / "manifest.json"),
        "baseline-embedding": evidence.file_sha256(baseline_runtime["embedding"] / "manifest.json"),
        "candidate-semantic-representation": candidate_runtime["semantic_representation_identity"],
        "baseline-semantic-representation": baseline_runtime["semantic_representation_identity"],
        "runtime-calibration": str(runtime_calibration.get("runtime_calibration_identity", runtime_calibration.get("identity", ""))),
        "shared-conditions": evidence.canonical_sha256(shared_conditions),
        "executor": executor_identity,
        "official-scorer": evidence.file_sha256(evaluator),
        "controller": implementation["controller"],
        "controller-entry": evidence.file_sha256(suite_root / "kernel_iteration_run.py"),
        "generator": evidence.canonical_sha256({"settings": blind["generation"], "implementation": implementation["generator"]}),
        "quality-admission": evidence.canonical_sha256({"settings": blind["quality_admission"], "implementation": implementation["quality-admission"]}),
        "observer-and-scorer": implementation["observer-and-scorer"],
    }


def _current_dependencies(suite_root: Path, locator: dict[str, Any]) -> dict[str, str]:
    contract = load_contract(suite_root, int(locator["level"]))
    comparison = evidence.load_contract(suite_root)
    validation_contract = validation.load_validation_contract(suite_root)
    budget = validation.load_blind_budget_archive(suite_root)
    candidate_runtime = validation.validate_execution_config(suite_root, Path(locator["candidate_execution_config"]))
    baseline_runtime = validation.validate_execution_config(suite_root, Path(locator["baseline_execution_config"]))
    candidate = evidence.select_subject(comparison, None, Path(locator["candidate_subject_manifest"]))
    baseline = evidence.select_subject(comparison, "v0")
    _validate_subjects(contract, candidate, baseline, candidate_runtime, baseline_runtime)
    shared = _shared_conditions(candidate_runtime, baseline_runtime)
    runtime_calibration = evidence.inspect_runtime_calibration(suite_root, Path(locator["formal_state"]))
    dependencies = _direct_dependencies(
        suite_root, contract, comparison, validation_contract, budget, candidate, baseline,
        candidate_runtime, baseline_runtime, runtime_calibration, shared,
    )
    previous = _previous_gate_dependency(
        suite_root, Path(locator["output_root"]), contract, locator.get("previous_plan_identity"),
    )
    if previous is not None:
        dependencies["previous-gate-result"] = previous
    return dependencies


def _implementation_identity() -> dict[str, str]:
    roles = {
        "generator": (_generator_prompt, validation._generator_case_schema, validation._validate_generated_case, validation._derive_truth_claims),
        "quality-admission": (validation._admission_prompt, validation._admission_schema, validation.validate_admission, validation.score_controls),
        "observer-and-scorer": (validation.observe_report, _absolute_decision, _relative_decision, _general_root_cause),
        "controller": (
            load_contract, run, resume_by_plan_identity, _coverage_schedule,
            _decision_wall_seconds, _native_generation_invokers, _generate_cases, _materials_from_generated,
            _previous_gate_dependency, _execute, _resume_proof, _finish,
            _failure_transition, _next_action, _validate_material_isolation,
            _initialize_recovery, _current_dependencies, _validate_plan,
            _validate_result, _destroy_scratch,
        ),
    }
    return {
        role: evidence.canonical_sha256({
            "schema": "ownward.kernel-iteration-stage6-role/v2",
            "role": role,
            "sources": [inspect.getsource(callback) for callback in callbacks],
            "controller_levels": list(GATE_LEVELS) if role == "controller" else None,
            "controller_budgets": LEVEL_BUDGETS if role == "controller" else None,
            "controller_sequence": {str(key): list(value) for key, value in LEVEL_SEQUENCE.items()} if role == "controller" else None,
        })
        for role, callbacks in roles.items()
    }


def _generator_prompt(validation_contract: dict[str, Any], seed: str, case_id: str, coverage: str) -> str:
    return validation._generator_prompt(validation_contract, seed, case_id, coverage).replace(
        "internal, non-formal calibration",
        "one-time V2 candidate blind gate. This is not calibration. Do not reuse any Stage 2 calibration or Stage 3 development/regression material",
    )


def _validate_material_isolation(suite_root: Path, output_root: Path, materials: dict[str, Any]) -> None:
    stage3 = validation.load_stage3_contract(suite_root)
    frozen = {
        validation._case_fact_identity(case)
        for name in ("development", "regression")
        for case in stage3["loaded"][name]["cases"]
    }
    current = {validation._case_fact_identity(case) for case in materials["cases"]}
    _require(current.isdisjoint(frozen), "阶段 6 候选盲测与阶段 3 开发/回归事实重合")
    gate_root = output_root / "blind-gate"
    if gate_root.is_dir():
        for result_path in gate_root.glob("*/result.json"):
            result = _load_json(result_path)
            previous = set(result.get("case_fact_identities", []))
            _require(current.isdisjoint(previous), "阶段 6 候选盲测复用了既有盲测事实")


def _initialize_recovery(
    root: Path,
    scratch: Path,
    plan_identity: str,
    gate_seed: str,
    seed_sha256: str,
    candidate_execution_config: Path,
    baseline_execution_config: Path,
    candidate_subject_manifest: Path,
    formal_state: Path,
    level: int,
    previous_plan_identity: str | None,
) -> None:
    scratch.mkdir(parents=True, exist_ok=True)
    secret_path = scratch / "recovery-secret.json"
    if secret_path.is_file():
        _require(_load_secret(secret_path, plan_identity)["gate_seed"] == gate_seed, "阶段 6 候选盲测恢复秘密漂移")
    else:
        evidence.atomic_json(secret_path, {"schema": SECRET_SCHEMA, "plan_identity": plan_identity, "gate_seed": gate_seed, "seed_sha256": seed_sha256})
    locator_content = {
        "schema": LOCATOR_SCHEMA,
        "plan_identity": plan_identity,
        "candidate_execution_config": str(candidate_execution_config.resolve()),
        "baseline_execution_config": str(baseline_execution_config.resolve()),
        "candidate_subject_manifest": str(candidate_subject_manifest.resolve()),
        "formal_state": str(formal_state.resolve()),
        "output_root": str(root.parent.parent.resolve()),
        "level": level,
        "previous_plan_identity": previous_plan_identity,
    }
    locator = {**locator_content, "identity": evidence.canonical_sha256(locator_content)}
    if (root / "locator.json").is_file():
        _require(_load_json(root / "locator.json") == locator, "阶段 6 候选盲测定位收据漂移")
    else:
        evidence.atomic_json(root / "locator.json", locator)
    active = {**locator_content, "schema": RECOVERY_SCHEMA, "scratch": str(scratch.resolve())}
    evidence.atomic_json(root / "active.json", active)


def _load_locator(path: Path, plan_identity: str) -> dict[str, Any]:
    value = _load_json(path)
    _require(value.get("schema") == LOCATOR_SCHEMA and value.get("plan_identity") == plan_identity, "阶段 6 候选盲测定位收据错绑")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), "阶段 6 候选盲测定位收据摘要漂移")
    for name in ("candidate_execution_config", "baseline_execution_config", "candidate_subject_manifest", "formal_state", "output_root"):
        _require(Path(str(value.get(name, ""))).is_absolute(), f"阶段 6 候选盲测 {name} 定位无效")
    _require(value.get("level") in GATE_LEVELS, "阶段 6 候选盲测定位级别无效")
    previous, _following = LEVEL_SEQUENCE[int(value["level"])]
    _require((previous is None and value.get("previous_plan_identity") is None) or evidence.is_sha256(value.get("previous_plan_identity")), "阶段 6 候选盲测前级定位无效")
    return value


def _load_secret(path: Path, plan_identity: str) -> dict[str, Any]:
    value = _load_json(path)
    _require(value.get("schema") == SECRET_SCHEMA and value.get("plan_identity") == plan_identity, "阶段 6 候选盲测恢复秘密错绑")
    seed = value.get("gate_seed")
    _require(isinstance(seed, str) and value.get("seed_sha256") == hashlib.sha256(seed.encode("utf-8")).hexdigest(), "阶段 6 候选盲测恢复秘密摘要漂移")
    return value


def _validate_plan(value: dict[str, Any], plan_identity: str) -> None:
    _require(value.get("schema") == PLAN_SCHEMA and value.get("identity") == plan_identity, "阶段 6 候选盲测计划错绑")
    _require(value.get("level") in GATE_LEVELS, "阶段 6 候选盲测计划级别无效")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(evidence.canonical_sha256(content) == plan_identity, "阶段 6 候选盲测计划摘要漂移")


def _validate_result(value: dict[str, Any], plan_identity: str) -> None:
    _require(value.get("schema") == RESULT_SCHEMA and value.get("plan_identity") == plan_identity, "阶段 6 候选盲测终态错绑")
    _require(value.get("level") in GATE_LEVELS, "阶段 6 候选盲测终态级别无效")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), "阶段 6 候选盲测终态摘要漂移")
    _require(value.get("raw_materials_destroyed") is True and value.get("contains_reversible_question_answer_or_evidence") is False, "阶段 6 候选盲测终态保留可逆内容")
    cost = _mapping(value, "cost_and_recovery")
    _require(cost.get("admission_rejection_not_candidate_failure") is True, "阶段 6 候选盲测错误地把准入拒绝计为候选失败")
    _require(float(cost.get("candidate_decision_wall_seconds", -1)) >= 0.0, "阶段 6 候选判定墙钟无效")
    scheduler = _mapping(value, "generation_scheduler")
    _require(
        scheduler.get("policy") == "bounded-independent-lanes-original-order/v1"
        and int(scheduler.get("max_active_limit", 0)) == 4
        and 0 <= int(scheduler.get("max_active_observed", -1)) <= 4
        and scheduler.get("per_worker_max_active_turns") == 1
        and scheduler.get("result_order") == "frozen-coverage-order",
        "阶段 6 生成调度终态无效",
    )
    process = value.get("evaluation_process_decision")
    if value.get("status") == "evaluation-process-rejected":
        _require(
            value.get("passed") is False
            and value.get("candidate_decision") is True
            and isinstance(process, dict)
            and process.get("passed") is False
            and process.get("candidate_failure") is False
            and _mapping(value, "failure_transition").get("candidate_failed") is False,
            "阶段 6 评测流程超时被错误归为候选失败",
        )


def _destroy_attempt(path: Path, scratch: Path) -> None:
    path = path.resolve()
    scratch = scratch.resolve()
    _require(path.parent == scratch, "拒绝清理阶段 6 候选盲测 batch 之外的目录")
    if path.exists():
        shutil.rmtree(path)


def _destroy_scratch(path: Path, runs_root: Path) -> None:
    path = path.resolve()
    root = (runs_root.resolve() / "kernel-v2-blind-gate")
    _require(path.parent == root, "拒绝清理阶段 6 候选盲测临时根之外的目录")
    if path.exists():
        shutil.rmtree(path)


def _terminal_reference(path: Path, result: dict[str, Any], *, reused: bool) -> dict[str, Any]:
    return {
        "passed": result["passed"],
        "status": result["status"],
        "candidate_decision": result["candidate_decision"],
        "level": result["level"],
        "plan_identity": result["plan_identity"],
        "result": str(path.resolve()),
        "reused": reused,
        "next_level": result.get("next_level"),
        "stage6_complete": result.get("stage6_complete", False),
        "next_action": result.get("next_action"),
    }


def _load_json(path: Path) -> dict[str, Any]:
    value = json.loads(path.read_text(encoding="utf-8"))
    _require(isinstance(value, dict), f"{path} 必须是 JSON 对象")
    return value


def _mapping(value: dict[str, Any], name: str) -> dict[str, Any]:
    item = value.get(name)
    _require(isinstance(item, dict), f"{name} 必须是对象")
    return item


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise BlindGateError(message)
