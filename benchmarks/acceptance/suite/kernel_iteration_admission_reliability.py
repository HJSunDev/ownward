from __future__ import annotations

import hashlib
import hmac
import inspect
import json
from pathlib import Path
import secrets
import shutil
import time
from typing import Any, Callable

import kernel_iteration_evidence as evidence
import kernel_iteration_validation as validation


class AdmissionReliabilityError(ValueError):
    pass


CONTRACT_SCHEMA = "ownward.kernel-iteration-stage2-admission-reliability-contract/v1"
PLAN_SCHEMA = "ownward.kernel-iteration-stage2-admission-reliability-plan/v1"
RESULT_SCHEMA = "ownward.kernel-iteration-stage2-admission-reliability-result/v1"
LOCATOR_SCHEMA = "ownward.kernel-iteration-stage2-admission-reliability-locator/v1"
SECRET_SCHEMA = "ownward.kernel-iteration-stage2-admission-reliability-secret/v1"
ACTIVE_SCHEMA = "ownward.kernel-iteration-stage2-admission-reliability-active/v1"
PROGRESS_SCHEMA = "ownward.kernel-iteration-stage2-admission-reliability-progress/v1"
CONTRACT_RELATIVE = Path("iteration/v2/stage2-blind-admission-reliability-contract.json")
MODES = ("diagnosis", "qualification")


def load_contract(suite_root: Path) -> dict[str, Any]:
    value = _load_json(suite_root.resolve() / CONTRACT_RELATIVE)
    _require(value.get("schema") == CONTRACT_SCHEMA, "阶段 2 准入可靠性合同 schema 无效")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), "阶段 2 准入可靠性合同身份漂移")
    _require(value.get("frozen_before_diagnosis") is True and value.get("candidate_results_seen") is False, "阶段 2 准入可靠性合同未在诊断前冻结")
    _require(value.get("formal") is False and value.get("questions_per_batch") == 15, "阶段 2 准入可靠性题量或证据边界漂移")
    expected = {name: 3 for name in validation.BLIND_COVERAGE}
    _require(value.get("coverage_counts") == expected, "阶段 2 准入可靠性覆盖配额漂移")
    modes = _mapping(value, "modes")
    _require(modes == {
        "diagnosis": {"batches": 1, "requires_all_pass": False},
        "qualification": {"batches": 2, "requires_all_pass": True},
    }, "阶段 2 准入可靠性批次合同漂移")
    budget = _mapping(value, "budget")
    _require(budget.get("per_batch_wall_seconds_maximum") == 492 and budget.get("qualification_total_wall_seconds_maximum") == 984, "阶段 2 准入可靠性预算漂移")
    _require(_mapping(value, "evidence").get("terminal_raw_policy") == "destroy-all-reversible-content", "阶段 2 准入可靠性原始内容策略无效")
    return value


def run(
    suite_root: Path,
    output_root: Path,
    execution_config: Path,
    formal_state: Path,
    *,
    mode: str,
    seed: str | None = None,
    plan_identity: str | None = None,
    resume: bool = False,
    invoker: Callable[..., tuple[dict[str, Any], dict[str, Any]]] | None = None,
) -> dict[str, Any]:
    suite_root = suite_root.resolve()
    output_root = output_root.resolve()
    repository = suite_root.parents[2]
    evidence._validate_output_boundary(repository, output_root)
    _require(mode in MODES, "阶段 2 准入可靠性模式无效")
    contract = load_contract(suite_root)
    validation_contract = validation.load_validation_contract(suite_root)
    runtime = validation.validate_execution_config(suite_root, execution_config.resolve())
    state_path = formal_state.resolve()
    _require(state_path.is_file(), "阶段 2 准入可靠性缺少正式 state 只读基线")
    state_before = state_path.read_bytes()
    dependencies = _direct_dependencies(suite_root, contract, validation_contract, runtime)

    if plan_identity is not None:
        _require(resume and evidence.is_sha256(plan_identity), "阶段 2 准入可靠性 plan identity 无效")
        locator = _load_locator(output_root / "admission-reliability" / plan_identity / "locator.json", plan_identity)
        _require(locator["mode"] == mode, "阶段 2 准入可靠性恢复模式漂移")
        _require(Path(locator["execution_config"]).resolve() == execution_config.resolve(), "阶段 2 准入可靠性执行配置恢复漂移")
        _require(Path(locator["formal_state"]).resolve() == state_path, "阶段 2 准入可靠性正式 state 恢复漂移")
        scratch = runtime["runs"] / "kernel-v2-admission-reliability" / plan_identity
        secret = _load_secret(scratch / "recovery-secret.json", plan_identity)
        gate_seed = secret["seed"]
    else:
        gate_seed = seed or secrets.token_hex(16)
    _require(len(gate_seed) >= 16 and all(character.isalnum() or character in "-_" for character in gate_seed), "阶段 2 准入可靠性 seed 无效")

    plan_content = {
        "schema": PLAN_SCHEMA,
        "purpose": "non-candidate-15-question-generation-admission-reliability",
        "mode": mode,
        "batches": int(_mapping(_mapping(contract, "modes"), mode)["batches"]),
        "questions_per_batch": 15,
        "contract_identity": contract["identity"],
        "validation_contract_identity": validation_contract["identity"],
        "seed_sha256": hashlib.sha256(gate_seed.encode("utf-8")).hexdigest(),
        "direct_dependencies": dict(sorted(dependencies.items())),
        "candidate_decision": None,
        "product_execution_forbidden": True,
        "formal": False,
    }
    computed_identity = evidence.canonical_sha256(plan_content)
    if plan_identity is not None:
        _require(plan_identity == computed_identity, "阶段 2 准入可靠性计划与当前依赖不一致")
    else:
        plan_identity = computed_identity
    plan = {**plan_content, "identity": plan_identity}
    root = output_root / "admission-reliability" / plan_identity
    result_path = root / "result.json"
    plan_path = root / "plan.json"
    if result_path.is_file():
        _require(resume and plan_path.is_file() and _load_json(plan_path) == plan, "阶段 2 准入可靠性终态只能由同一计划恢复")
        result = _load_json(result_path)
        _validate_result(result, plan_identity)
        _require(_current_dependencies(suite_root, _load_locator(root / "locator.json", plan_identity)) == dependencies, "阶段 2 准入可靠性终态直接依赖漂移")
        _require(state_path.read_bytes() == state_before, "阶段 2 准入可靠性终态复用改写正式 state")
        return _terminal_reference(result_path, result, reused=True)
    if plan_path.is_file():
        _require(resume and _load_json(plan_path) == plan, "阶段 2 准入可靠性既有计划漂移")
    else:
        evidence.atomic_json(plan_path, plan)

    scratch = runtime["runs"] / "kernel-v2-admission-reliability" / plan_identity
    _initialize_recovery(root, scratch, plan, gate_seed, execution_config, state_path, output_root)
    progress_path = root / "progress.json"
    progress = _load_json(progress_path) if progress_path.is_file() else {
        "schema": PROGRESS_SCHEMA,
        "plan_identity": plan_identity,
        "batches": [],
    }
    _validate_progress(progress, plan_identity)
    expected_batches = int(plan["batches"])
    try:
        for batch_index in range(1, expected_batches + 1):
            if any(item["batch"] == batch_index for item in progress["batches"]):
                continue
            batch = _run_batch(
                suite_root, runtime, validation_contract, contract, scratch,
                plan_identity, gate_seed, batch_index, progress["batches"], invoker,
            )
            progress["batches"].append(batch)
            evidence.atomic_json(progress_path, progress)
            _destroy_batch(scratch / f"batch-{batch_index:02d}", scratch)
            if mode == "qualification" and not batch["admission"]["passed"]:
                break
        result = _finish(root, scratch, runtime["runs"], progress_path, state_path, state_before, plan, contract, progress["batches"])
        return _terminal_reference(result_path, result, reused=False)
    except (KeyboardInterrupt, InterruptedError):
        _require(state_path.read_bytes() == state_before, "阶段 2 准入可靠性中断路径改写正式 state")
        raise
    except Exception:
        _require(state_path.read_bytes() == state_before, "阶段 2 准入可靠性失败路径改写正式 state")
        raise


def resume_by_plan_identity(
    suite_root: Path,
    output_root: Path,
    plan_identity: str,
    *,
    invoker: Callable[..., tuple[dict[str, Any], dict[str, Any]]] | None = None,
) -> dict[str, Any]:
    suite_root = suite_root.resolve()
    output_root = output_root.resolve()
    _require(evidence.is_sha256(plan_identity), "阶段 2 准入可靠性 plan identity 无效")
    root = output_root / "admission-reliability" / plan_identity
    plan = _load_json(root / "plan.json")
    _validate_plan(plan, plan_identity)
    locator = _load_locator(root / "locator.json", plan_identity)
    result_path = root / "result.json"
    if result_path.is_file():
        current = _current_dependencies(suite_root, locator)
        _require(current == _mapping(plan, "direct_dependencies"), "阶段 2 准入可靠性终态直接依赖漂移")
        result = _load_json(result_path)
        _validate_result(result, plan_identity)
        _require(not (root / "active.json").exists(), "阶段 2 准入可靠性终态仍有活动指针")
        return _terminal_reference(result_path, result, reused=True)
    return run(
        suite_root, output_root, Path(locator["execution_config"]), Path(locator["formal_state"]),
        mode=str(locator["mode"]), plan_identity=plan_identity, resume=True, invoker=invoker,
    )


def _run_batch(
    suite_root: Path,
    runtime: dict[str, Any],
    validation_contract: dict[str, Any],
    contract: dict[str, Any],
    scratch: Path,
    plan_identity: str,
    gate_seed: str,
    batch_index: int,
    completed: list[dict[str, Any]],
    invoke: Callable[..., tuple[dict[str, Any], dict[str, Any]]] | None,
) -> dict[str, Any]:
    started = time.perf_counter()
    batch_root = scratch / f"batch-{batch_index:02d}"
    if invoke is None:
        with validation._native_codex_batch_invoker(suite_root, runtime, batch_root / ".codex-runtime") as batch_invoke:
            return _run_batch(
                suite_root, runtime, validation_contract, contract, scratch,
                plan_identity, gate_seed, batch_index, completed, batch_invoke,
            )
    batch_seed = hashlib.sha256(f"{gate_seed}:{batch_index}".encode("utf-8")).hexdigest()
    generated: list[dict[str, Any]] = []
    usages: list[dict[str, Any]] = []
    for index, coverage in enumerate(_coverage_schedule(contract), start=1):
        case_id = f"b{batch_index:02d}-c{index:02d}"
        value, usage = invoke(
            suite_root=suite_root,
            runtime=runtime,
            stage=batch_root / "generator" / case_id,
            role="generator",
            prompt=validation._generator_prompt(validation_contract, batch_seed, case_id, coverage),
            schema=validation._generator_case_schema(case_id, coverage, validation_contract),
            settings=_mapping(_mapping(validation_contract, "blind"), "generation"),
            validate=lambda output, expected_id=case_id, expected_coverage=coverage: validation._validate_generated_case(output, expected_id, expected_coverage, validation_contract),
        )
        generated.append(validation._validate_generated_case(value, case_id, coverage, validation_contract))
        usages.append(usage)
    materials = _materials(generated)
    validation.validate_materials(materials, expected_questions=15)
    controls = validation.score_controls(materials)
    _require(controls["passed"], "阶段 2 准入可靠性评分控制无法区分关键错误")
    tags = _case_tags(plan_identity, gate_seed, materials)
    prior_tags = {tag for item in completed for tag in item.get("active_case_tags", [])}
    _require(prior_tags.isdisjoint(tags), "阶段 2 准入可靠性批次事实重合")
    admission_value, admission_usage = invoke(
        suite_root=suite_root,
        runtime=runtime,
        stage=batch_root / "quality-admission",
        role="quality-admission",
        prompt=validation._admission_prompt(validation_contract, materials),
        schema=validation._admission_schema([case["case_id"] for case in materials["cases"]]),
        settings=_mapping(_mapping(validation_contract, "blind"), "quality_admission"),
        validate=lambda output: validation.validate_admission(output, materials, validation_contract),
    )
    admission = validation.validate_admission(admission_value, materials, validation_contract)
    wall = time.perf_counter() - started
    return {
        "batch": batch_index,
        "material_identity": materials["identity"],
        "coverage_counts": {name: sum(case["coverage"] == name for case in materials["cases"]) for name in validation.BLIND_COVERAGE},
        "admission": admission,
        "control_discrimination_passed": True,
        "generator_usage": validation._sanitize_usage(validation._combine_usages(usages)),
        "admission_usage": validation._sanitize_usage(admission_usage),
        "wall_seconds": wall,
        "active_case_tags": sorted(tags),
    }


def _finish(
    root: Path,
    scratch: Path,
    runs_root: Path,
    progress_path: Path,
    state_path: Path,
    state_before: bytes,
    plan: dict[str, Any],
    contract: dict[str, Any],
    active_batches: list[dict[str, Any]],
) -> dict[str, Any]:
    batches = [
        {key: value for key, value in item.items() if key != "active_case_tags"}
        for item in active_batches
    ]
    mode = str(plan["mode"])
    expected = int(plan["batches"])
    admission_passed = len(batches) == expected and all(item["admission"]["passed"] for item in batches)
    budget = _mapping(contract, "budget")
    per_batch_budget = float(budget["per_batch_wall_seconds_maximum"])
    total_wall = sum(float(item["wall_seconds"]) for item in batches)
    budget_passed = all(float(item["wall_seconds"]) <= per_batch_budget for item in batches)
    if mode == "qualification":
        budget_passed = budget_passed and total_wall <= float(budget["qualification_total_wall_seconds_maximum"])
    passed = mode == "diagnosis" or (admission_passed and budget_passed)
    aggregate = _aggregate_diagnostics(batches)
    content = {
        "schema": RESULT_SCHEMA,
        "plan_identity": plan["identity"],
        "contract_identity": contract["identity"],
        "mode": mode,
        "status": "diagnosis-complete" if mode == "diagnosis" else ("qualified" if passed else "qualification-rejected"),
        "passed": passed,
        "admission_reliability_passed": admission_passed,
        "budget_passed": budget_passed,
        "candidate_decision": None,
        "candidate_executions": 0,
        "baseline_executions": 0,
        "formal": False,
        "formal_state_written": False,
        "raw_materials_destroyed": True,
        "contains_reversible_question_answer_evidence_or_case_identifiers": False,
        "batches": batches,
        "aggregate_diagnostics": aggregate,
        "total_wall_seconds": total_wall,
        "next_action": _mapping(contract, "next_actions")[
            "diagnosis" if mode == "diagnosis" else ("qualification_passed" if passed else "qualification_failed")
        ],
    }
    result = {**content, "identity": evidence.canonical_sha256(content)}
    _destroy_scratch(scratch, runs_root)
    progress_path.unlink(missing_ok=True)
    (root / "active.json").unlink(missing_ok=True)
    evidence.atomic_json(root / "result.json", result)
    _require(state_path.read_bytes() == state_before, "阶段 2 准入可靠性终态改写正式 state")
    _validate_result(result, str(plan["identity"]))
    return result


def _aggregate_diagnostics(batches: list[dict[str, Any]]) -> dict[str, Any]:
    checks = list(_mapping(_mapping(validation.load_validation_contract(Path(__file__).resolve().parent), "blind"), "quality_admission")["required_checks"])
    by_coverage = {name: 0 for name in validation.BLIND_COVERAGE}
    by_check = {name: 0 for name in checks}
    matrix = {coverage: {name: 0 for name in checks} for coverage in validation.BLIND_COVERAGE}
    combinations: dict[tuple[str, ...], int] = {}
    for batch in batches:
        aggregate = _mapping(_mapping(batch, "admission"), "failure_aggregate")
        for coverage, count in _mapping(aggregate, "rejected_by_coverage").items():
            by_coverage[coverage] += int(count)
        for name, count in _mapping(aggregate, "failed_by_check").items():
            by_check[name] += int(count)
        for coverage, values in _mapping(aggregate, "failed_by_coverage_and_check").items():
            _require(isinstance(values, dict), "阶段 2 准入可靠性覆盖检查矩阵无效")
            for name, count in values.items():
                matrix[coverage][name] += int(count)
        for item in aggregate.get("failed_check_combinations", []):
            names = tuple(item["checks"])
            combinations[names] = combinations.get(names, 0) + int(item["count"])
    return {
        "rejected_by_coverage": by_coverage,
        "failed_by_check": by_check,
        "failed_by_coverage_and_check": matrix,
        "failed_check_combinations": [
            {"checks": list(names), "count": combinations[names]}
            for names in sorted(combinations)
        ],
    }


def _materials(cases: list[dict[str, Any]]) -> dict[str, Any]:
    content = {
        "schema": validation.MATERIALS_SCHEMA,
        "contains_formal_questions_answers_gold_or_content": False,
        "cases": cases,
        "criteria": {"minimum_accuracy": 0.0, "require_complete_fact_delivery": True, "category_minimums": {}},
    }
    return {**content, "identity": evidence.canonical_sha256(content)}


def _coverage_schedule(contract: dict[str, Any]) -> list[str]:
    counts = _mapping(contract, "coverage_counts")
    schedule = [
        coverage
        for depth in range(max(int(value) for value in counts.values()))
        for coverage in validation.BLIND_COVERAGE
        if depth < int(counts[coverage])
    ]
    _require(len(schedule) == 15, "阶段 2 准入可靠性覆盖计划漂移")
    return schedule


def _case_tags(plan_identity: str, secret: str, materials: dict[str, Any]) -> set[str]:
    key = hashlib.sha256(f"{plan_identity}:{secret}".encode("utf-8")).digest()
    return {
        hmac.new(key, validation._case_fact_identity(case).encode("ascii"), hashlib.sha256).hexdigest()
        for case in materials["cases"]
    }


def _direct_dependencies(suite_root: Path, contract: dict[str, Any], validation_contract: dict[str, Any], runtime: dict[str, Any]) -> dict[str, str]:
    implementation = _implementation_identity()
    blind = _mapping(validation_contract, "blind")
    return {
        "contract": contract["identity"],
        "validation-contract": validation_contract["identity"],
        "controller": implementation["controller"],
        "controller-entry": evidence.file_sha256(suite_root / "kernel_iteration_run.py"),
        "generator": evidence.canonical_sha256({"settings": blind["generation"], "implementation": implementation["generator"]}),
        "quality-admission": evidence.canonical_sha256({"settings": blind["quality_admission"], "implementation": implementation["quality-admission"]}),
        "codex-executor": evidence.file_sha256(runtime["codex_binary"]),
        "codex-auth-location": evidence.canonical_sha256(str(runtime["codex_auth_file"].resolve())),
        "runs-root": evidence.canonical_sha256(str(runtime["runs"].resolve())),
    }


def _current_dependencies(suite_root: Path, locator: dict[str, Any]) -> dict[str, str]:
    contract = load_contract(suite_root)
    validation_contract = validation.load_validation_contract(suite_root)
    runtime = validation.validate_execution_config(suite_root, Path(locator["execution_config"]))
    return _direct_dependencies(suite_root, contract, validation_contract, runtime)


def _implementation_identity() -> dict[str, str]:
    roles = {
        "generator": (validation._generator_prompt, validation._generator_case_schema, validation._validate_generated_case, validation._derive_truth_claims),
        "quality-admission": (validation._admission_prompt, validation._admission_schema, validation.validate_admission),
        "controller": (
            load_contract, run, resume_by_plan_identity, _run_batch, _finish,
            _aggregate_diagnostics, _materials, _coverage_schedule, _case_tags,
            _direct_dependencies, _current_dependencies, _initialize_recovery,
            _validate_plan, _validate_result, _validate_progress,
            _destroy_batch, _destroy_scratch, validation._native_codex_batch_invoker,
        ),
    }
    return {
        role: evidence.canonical_sha256({
            "schema": "ownward.kernel-iteration-stage2-admission-reliability-role/v1",
            "role": role,
            "sources": [inspect.getsource(callback) for callback in callbacks],
        })
        for role, callbacks in roles.items()
    }


def _initialize_recovery(root: Path, scratch: Path, plan: dict[str, Any], seed: str, execution_config: Path, state_path: Path, output_root: Path) -> None:
    root.mkdir(parents=True, exist_ok=True)
    scratch.mkdir(parents=True, exist_ok=True)
    secret_path = scratch / "recovery-secret.json"
    if secret_path.is_file():
        secret = _load_secret(secret_path, str(plan["identity"]))
        _require(secret["seed"] == seed, "阶段 2 准入可靠性恢复秘密漂移")
    else:
        evidence.atomic_json(secret_path, {
            "schema": SECRET_SCHEMA,
            "plan_identity": plan["identity"],
            "seed": seed,
            "seed_sha256": plan["seed_sha256"],
        })
    locator_content = {
        "schema": LOCATOR_SCHEMA,
        "plan_identity": plan["identity"],
        "mode": plan["mode"],
        "execution_config": str(execution_config.resolve()),
        "formal_state": str(state_path),
        "output_root": str(output_root),
    }
    locator = {**locator_content, "identity": evidence.canonical_sha256(locator_content)}
    locator_path = root / "locator.json"
    if locator_path.is_file():
        _require(_load_json(locator_path) == locator, "阶段 2 准入可靠性恢复定位漂移")
    else:
        evidence.atomic_json(locator_path, locator)
    active = {"schema": ACTIVE_SCHEMA, "plan_identity": plan["identity"], "scratch": str(scratch)}
    evidence.atomic_json(root / "active.json", active)


def _load_locator(path: Path, plan_identity: str) -> dict[str, Any]:
    value = _load_json(path)
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("schema") == LOCATOR_SCHEMA and value.get("plan_identity") == plan_identity, "阶段 2 准入可靠性定位错绑")
    _require(value.get("identity") == evidence.canonical_sha256(content), "阶段 2 准入可靠性定位身份漂移")
    _require(set(value) == {"schema", "plan_identity", "mode", "execution_config", "formal_state", "output_root", "identity"}, "阶段 2 准入可靠性定位字段越界")
    return value


def _load_secret(path: Path, plan_identity: str) -> dict[str, Any]:
    value = _load_json(path)
    _require(value.get("schema") == SECRET_SCHEMA and value.get("plan_identity") == plan_identity, "阶段 2 准入可靠性恢复秘密错绑")
    _require(set(value) == {"schema", "plan_identity", "seed", "seed_sha256"}, "阶段 2 准入可靠性恢复秘密字段越界")
    _require(value.get("seed_sha256") == hashlib.sha256(str(value.get("seed", "")).encode("utf-8")).hexdigest(), "阶段 2 准入可靠性恢复秘密摘要漂移")
    return value


def _validate_plan(value: dict[str, Any], plan_identity: str) -> None:
    _require(value.get("schema") == PLAN_SCHEMA and value.get("identity") == plan_identity, "阶段 2 准入可靠性计划错绑")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(evidence.canonical_sha256(content) == plan_identity, "阶段 2 准入可靠性计划摘要漂移")
    _require(value.get("candidate_decision") is None and value.get("product_execution_forbidden") is True and value.get("formal") is False, "阶段 2 准入可靠性越过非候选边界")


def _validate_result(value: dict[str, Any], plan_identity: str) -> None:
    _require(value.get("schema") == RESULT_SCHEMA and value.get("plan_identity") == plan_identity, "阶段 2 准入可靠性终态错绑")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), "阶段 2 准入可靠性终态摘要漂移")
    _require(value.get("raw_materials_destroyed") is True and value.get("contains_reversible_question_answer_evidence_or_case_identifiers") is False, "阶段 2 准入可靠性终态保留可逆内容")
    _require(value.get("candidate_executions") == value.get("baseline_executions") == 0 and value.get("formal_state_written") is False, "阶段 2 准入可靠性越权执行产品或改写正式状态")
    _require(not _contains_forbidden_raw_key(value), "阶段 2 准入可靠性终态包含原始内容字段")


def _validate_progress(value: dict[str, Any], plan_identity: str) -> None:
    _require(value.get("schema") == PROGRESS_SCHEMA and value.get("plan_identity") == plan_identity, "阶段 2 准入可靠性进度错绑")
    _require(isinstance(value.get("batches"), list), "阶段 2 准入可靠性进度批次无效")


def _contains_forbidden_raw_key(value: Any) -> bool:
    forbidden = {"question", "answer", "evidence", "sessions", "truth_claims", "case_id", "per_case_output", "active_case_tags"}
    if isinstance(value, dict):
        return bool(forbidden & set(value)) or any(_contains_forbidden_raw_key(item) for item in value.values())
    if isinstance(value, list):
        return any(_contains_forbidden_raw_key(item) for item in value)
    return False


def _destroy_batch(path: Path, scratch: Path) -> None:
    path = path.resolve()
    scratch = scratch.resolve()
    _require(path.parent == scratch, "拒绝清理阶段 2 准入可靠性批次目录之外的路径")
    if path.exists():
        shutil.rmtree(path)


def _destroy_scratch(path: Path, runs_root: Path) -> None:
    path = path.resolve()
    allowed = (runs_root.resolve() / "kernel-v2-admission-reliability")
    _require(path.parent == allowed, "拒绝清理阶段 2 准入可靠性运行根之外的路径")
    if path.exists():
        shutil.rmtree(path)


def _terminal_reference(path: Path, result: dict[str, Any], *, reused: bool) -> dict[str, Any]:
    return {
        "passed": bool(result["passed"]),
        "status": result["status"],
        "mode": result["mode"],
        "plan_identity": result["plan_identity"],
        "result": str(path),
        "reused": reused,
        "model_calls": 0 if reused else sum(int(batch["generator_usage"].get("calls", 0)) + int(batch["admission_usage"].get("calls", 0)) for batch in result["batches"]),
        "product_executions": 0,
        "next_action": result["next_action"],
    }


def _load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise AdmissionReliabilityError(f"阶段 2 准入可靠性 JSON 无法读取: {path}: {error}") from error
    _require(isinstance(value, dict), f"阶段 2 准入可靠性 JSON 顶层无效: {path}")
    return value


def _mapping(value: Any, name: str) -> dict[str, Any]:
    item = value.get(name) if isinstance(value, dict) else None
    _require(isinstance(item, dict), f"阶段 2 准入可靠性缺少对象字段: {name}")
    return item


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise AdmissionReliabilityError(message)
