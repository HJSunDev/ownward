from __future__ import annotations

import hashlib
import inspect
import json
from pathlib import Path
import shutil
from typing import Any, Callable

import kernel_iteration_evidence as evidence
import kernel_iteration_answer_sufficiency as answer_sufficiency
import kernel_iteration_official_evaluator as official_evaluator
import kernel_iteration_validation as validation


CONTRACT_RELATIVE = Path("iteration/v2/stage6-evaluator-environment-contract.json")
QUALIFICATION_RELATIVE = Path("iteration/v2/stage6-evaluator-environment-qualification.json")
DEPENDENCY_MIGRATION_RELATIVE = Path("iteration/v2/stage6-evaluator-environment-dependency-migration.json")
CONTRACT_SCHEMA = "ownward.kernel-iteration-stage6-evaluator-environment-contract/v4"
RESULT_SCHEMA = "ownward.kernel-iteration-stage6-evaluator-environment-qualification/v4"
RECEIPT_SCHEMA = "ownward.kernel-iteration-stage6-evaluator-environment-receipt/v4"
DEPENDENCY_MIGRATION_SCHEMA = "ownward.kernel-iteration-stage6-evaluator-environment-dependency-migration/v1"


class EvaluatorReliabilityError(ValueError):
    pass


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise EvaluatorReliabilityError(message)


def _load_json(path: Path) -> dict[str, Any]:
    value = json.loads(path.read_text(encoding="utf-8"))
    _require(isinstance(value, dict), f"JSON 对象无效: {path}")
    return value


def _mapping(value: dict[str, Any], key: str) -> dict[str, Any]:
    item = value.get(key)
    _require(isinstance(item, dict), f"{key} 必须是对象")
    return item


def load_contract(suite_root: Path) -> dict[str, Any]:
    value = _load_json(suite_root.resolve() / CONTRACT_RELATIVE)
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("schema") == CONTRACT_SCHEMA, "官方评测器环境合同 schema 无效")
    _require(value.get("identity") == evidence.canonical_sha256(content), "官方评测器环境合同身份漂移")
    failure = _mapping(value, "failure_semantics")
    _require(
        failure.get("fail_closed") is True
        and failure.get("candidate_failure") is False
        and failure.get("candidate_execution_allowed_before_qualification") is False
        and failure.get("silent_degradation_forbidden") is True,
        "官方评测器环境没有冻结 fail-closed 语义",
    )
    root_cause = _mapping(value, "root_cause_boundary")
    _require(
        root_cause.get("product_context_failures") == 1
        and root_cause.get("oracle_context_failures") == 2
        and root_cause.get("judge_correct_controls_passed") == 1
        and root_cause.get("judge_wrong_controls_passed") == 1
        and root_cause.get("transport_timeouts") == 0
        and root_cause.get("worker_restarts") == 0
        and root_cause.get("rate_limit_events") == 0
        and root_cause.get("candidate_failure") is False,
        "oracle Reader 根因边界漂移",
    )
    product_reader = _mapping(value, "product_reader_unchanged")
    attribution_reader = _mapping(value, "attribution_reader")
    _require(
        product_reader.get("model") == "gpt-5.6-luna"
        and product_reader.get("reasoning_effort") == "xhigh"
        and attribution_reader.get("model") == "gpt-5.6-terra"
        and attribution_reader.get("reasoning_effort") == "xhigh"
        and attribution_reader.get("low_reasoning_forbidden") is True
        and attribution_reader.get("sol_model_forbidden") is True,
        "产品 Reader 或归因 Reader 边界漂移",
    )
    attribution = _mapping(value, "attribution_qualification")
    _require(
        attribution.get("questions") == 5
        and attribution.get("product_repeats") == [2, 3]
        and attribution.get("oracle_repeats") == [1, 2, 3]
        and attribution.get("reader_calls") == 25
        and attribution.get("judge_calls") == 40
        and attribution.get("product_context_failures_maximum") == 0
        and attribution.get("oracle_context_failures_maximum") == 0,
        "oracle Reader 资格调用合同漂移",
    )
    cost = _mapping(value, "cost_bound")
    _require(
        float(cost["pre_attribution_observed_upper_seconds"]) + float(cost["future_single_case_attribution_maximum_seconds"])
        <= float(cost["level_total_wall_seconds_maximum"]),
        "失败后单题归因没有为 50 题硬上限闭合成本",
    )
    return value


def attribution_reader_settings(suite_root: Path) -> dict[str, Any]:
    frozen = _mapping(load_contract(suite_root.resolve()), "attribution_reader")
    return {
        key: frozen[key]
        for key in ("capability_source", "model", "reasoning_effort", "max_output_tokens", "timeout_seconds", "attempts")
    }


def _load_material(suite_root: Path, contract: dict[str, Any]) -> dict[str, Any]:
    frozen = _mapping(contract, "material")
    relative = Path(str(frozen["path"]))
    _require(not relative.is_absolute() and ".." not in relative.parts, "单题资格材料路径越界")
    path = (suite_root / relative).resolve()
    _require(path.is_relative_to(suite_root) and path.is_file(), "单题资格材料缺失")
    _require(evidence.file_sha256(path) == frozen["sha256"], "单题资格材料摘要漂移")
    material = _load_json(path)
    content = {key: item for key, item in material.items() if key != "identity"}
    _require(material.get("identity") == evidence.canonical_sha256(content), "单题资格材料身份漂移")
    cases = material.get("cases", [])
    _require(
        material.get("identity") == frozen["identity"]
        and isinstance(cases, list)
        and len(cases) == int(frozen["questions"])
        and [item.get("coverage") for item in cases] == frozen["coverages"],
        "oracle Reader 资格材料错绑",
    )
    return material


def _runtime_paths(runtime: dict[str, Any]) -> tuple[Path, Path, Path]:
    layout = _mapping(runtime["environment"], "layout")
    python_root = Path(str(layout["python"]))
    python = python_root / ("Scripts/python.exe" if (python_root / "Scripts/python.exe").is_file() else "bin/python")
    source = Path(str(layout["source"]))
    evaluator = source / "src" / "evaluation" / "evaluate_qa.py"
    lock = Path(str(layout["requirements_lock"]))
    return python.resolve(), evaluator.resolve(), lock.resolve()


def _qualification_controller_identity() -> str:
    return evidence.canonical_sha256({
        "schema": "ownward.kernel-iteration-stage6-evaluator-qualification-controller/v1",
        "material": inspect.getsource(_load_material),
        "runtime_paths": inspect.getsource(_runtime_paths),
        "reader_settings": inspect.getsource(attribution_reader_settings),
        "execution": inspect.getsource(run),
    })


def _longmemeval_qualification_surface_identity(module: Any) -> str:
    return evidence.canonical_sha256({
        "schema": "ownward.kernel-iteration-stage6-longmemeval-qualification-surface/v1",
        "session_content": inspect.getsource(module.session_content),
        "answer_prompt": inspect.getsource(module._answer_prompt),
    })


def current_dependencies(suite_root: Path, execution_config: Path) -> tuple[dict[str, str], dict[str, Any], dict[str, Any]]:
    contract = load_contract(suite_root)
    runtime = validation.validate_execution_config(suite_root, execution_config.resolve(), expected_reader_effort="xhigh")
    module = validation._load_longmemeval_module(suite_root)
    python, evaluator, lock = _runtime_paths(runtime)
    expected = _mapping(contract, "official_evaluator")
    _require(evidence.file_sha256(evaluator) == expected["sha256"], "官方评测器源码漂移")
    _require(evidence.file_sha256(lock) == expected["requirements_lock_sha256"], "官方评测器依赖锁漂移")
    judge = _mapping(runtime["protocol_value"], "judge")
    reader = _mapping(runtime["protocol_value"], "reader")
    frozen_reader = _mapping(contract, "product_reader_unchanged")
    frozen_judge = _mapping(contract, "judge")
    _require(reader.get("model") == frozen_reader["model"] and reader.get("reasoning_effort") == frozen_reader["reasoning_effort"], "产品首答 Reader 身份漂移")
    _require(judge.get("model") == frozen_judge["model"] and judge.get("reasoning_effort") == frozen_judge["reasoning_effort"], "官方 Judge 身份漂移")
    dependencies = dict(sorted({
        "contract": contract["identity"],
        "controller": _qualification_controller_identity(),
        "qualification-entry": evidence.file_sha256(Path(__file__).with_name("kernel_iteration_evaluator_qualification.py")),
        "answer-attribution": evidence.file_sha256(Path(answer_sufficiency.__file__).resolve()),
        "qualification-material": _mapping(contract, "material")["sha256"],
        "longmemeval-executor": _longmemeval_qualification_surface_identity(module),
        "official-evaluator-adapter": evidence.file_sha256(Path(official_evaluator.__file__).resolve()),
        "official-evaluator-worker": evidence.file_sha256(official_evaluator.WORKER),
        "official-evaluator-source": evidence.file_sha256(evaluator),
        "requirements-lock": evidence.file_sha256(lock),
        "python-runtime": evidence.file_sha256(python),
        "environment-manifest": evidence.file_sha256(runtime["environment_manifest"]),
        "protocol": evidence.file_sha256(runtime["protocol"]),
        "codex-executor": evidence.file_sha256(runtime["codex_binary"]),
    }.items()))
    return dependencies, runtime, contract


def run(
    suite_root: Path,
    output_root: Path,
    execution_config: Path,
    formal_state: Path,
    *,
    resume: bool = False,
    diagnose: Callable[..., dict[str, Any]] | None = None,
) -> dict[str, Any]:
    suite_root = suite_root.resolve()
    output_root = output_root.resolve()
    state_path = formal_state.resolve()
    _require(state_path.is_file(), "官方评测器资格缺少正式 state 只读基线")
    state_before = state_path.read_bytes()
    dependencies, runtime, contract = current_dependencies(suite_root, execution_config)
    python, evaluator, _lock = _runtime_paths(runtime)
    material = _load_material(suite_root, contract)
    plan_content = {
        "schema": "ownward.kernel-iteration-stage6-evaluator-environment-plan/v3",
        "purpose": "noncandidate-post-failure-oracle-reader-reliability-qualification",
        "formal": False,
        "direct_dependencies": dependencies,
    }
    plan_identity = evidence.canonical_sha256(plan_content)
    root = output_root / "evaluator-qualification" / plan_identity
    plan_path = root / "plan.json"
    result_path = root / "result.json"
    if result_path.is_file():
        _require(resume and _load_json(plan_path) == {**plan_content, "identity": plan_identity}, "官方评测器资格终态只能由同一身份恢复")
        result = _load_json(result_path)
        _validate_result(result, plan_identity, dependencies)
        _require(state_path.read_bytes() == state_before, "官方评测器资格终态复用改写正式 state")
        return {"passed": True, "reused": True, "model_calls": 0, "product_executions": 0, "plan_identity": plan_identity, "result": str(result_path), "identity": result["identity"]}
    evidence.atomic_json(plan_path, {**plan_content, "identity": plan_identity})
    scratch = output_root / ".runtime" / plan_identity[:16]
    _require(not scratch.exists(), "官方评测器资格发现同身份遗留临时运行目录")
    scratch.mkdir(parents=True, exist_ok=True)
    module = validation._load_longmemeval_module(suite_root)
    run_root = scratch / "synthetic-run"
    for case in material["cases"]:
        session_by_id = {item["session_id"]: item for item in case["sessions"]}
        evidence_items = [
            {"id": f"qualification-{session_id}", "content": module.session_content(
                session_id, session_by_id[session_id]["date"], session_by_id[session_id]["turns"],
            )}
            for session_id in case["answer_session_ids"]
        ]
        product_prompt = module._answer_prompt(case, evidence_items)
        reader_root = run_root / "questions" / case["case_id"] / "reader"
        reader_root.mkdir(parents=True)
        evidence.atomic_json(reader_root / "input.json", {"prompt": product_prompt})
        evidence.atomic_json(reader_root / "output.json", {"answer": case["answer"]})
    attribution = _mapping(contract, "attribution_qualification")
    diagnose_call = diagnose or answer_sufficiency._diagnose_codex_boundaries
    try:
        diagnostic = diagnose_call(
            suite_root,
            scratch / "answer-attribution-codex",
            runtime,
            material,
            run_root,
            reader_settings=attribution_reader_settings(suite_root),
            include_original_product_answer=True,
            product_repeats=tuple(attribution["product_repeats"]),
            oracle_repeats=tuple(attribution["oracle_repeats"]),
            settings_label="stage6-oracle-reader-terra-xhigh-qualification",
            run_judge=True,
            correctness_source="judge",
            prompt_renderer_factory=official_evaluator.PromptRenderer,
        )
    finally:
        if "diagnostic" not in locals():
            shutil.rmtree(scratch, ignore_errors=True)
    reader = _mapping(diagnostic, "reader")
    judge = _mapping(diagnostic, "judge")
    _require(
        int(reader["product_context_failures"]) <= int(attribution["product_context_failures_maximum"])
        and int(reader["oracle_context_failures"]) <= int(attribution["oracle_context_failures_maximum"])
        and judge.get("controls_passed") is True
        and int(_mapping(judge, "correct_controls")["passed"]) == int(attribution["questions"])
        and int(_mapping(judge, "wrong_controls")["passed"]) == int(attribution["questions"]),
        "oracle Reader/Judge 资格未通过",
    )
    transport = _mapping(diagnostic, "transport")
    _require(
        int(transport.get("worker_restarts", 0)) == 0
        and transport.get("rate_limit_observed") is False,
        "oracle Reader 资格出现 worker 重启或限流",
    )
    cost = _mapping(diagnostic, "cost")
    observed_wall = float(cost["observed_wall_seconds"])
    cost_bound = _mapping(contract, "cost_bound")
    _require(observed_wall <= float(cost_bound["qualification_maximum_seconds"]), "oracle Reader 资格墙钟超过冻结余量")
    shutil.rmtree(scratch)
    content = {
        "schema": RESULT_SCHEMA,
        "plan_identity": plan_identity,
        "passed": True,
        "formal": False,
        "candidate_executions": 0,
        "product_executions": 0,
        "direct_dependencies": dependencies,
        "attribution": {
            "questions": int(attribution["questions"]),
            "material_identity": material["identity"],
            "reader_settings": reader["settings"],
            "reader_calls": int(_mapping(reader, "cost")["measured_calls"]),
            "reader_aggregate_wall_seconds": float(_mapping(reader, "cost")["aggregate_wall_seconds"]),
            "reader_mean_wall_seconds": float(_mapping(reader, "cost")["mean_wall_seconds"]),
            "reader_p95_wall_seconds": float(_mapping(reader, "cost")["p95_wall_seconds"]),
            "product_context_failures": int(reader["product_context_failures"]),
            "oracle_context_failures": int(reader["oracle_context_failures"]),
            "judge_calls": int(_mapping(judge, "correct_controls")["total"]) + int(_mapping(judge, "wrong_controls")["total"]) + int(judge["reader_answers_total"]),
            "judge_controls_passed": bool(judge["controls_passed"]),
            "judge_correct_controls": dict(_mapping(judge, "correct_controls")),
            "judge_wrong_controls": dict(_mapping(judge, "wrong_controls")),
            "judge_aggregate_wall_seconds": float(cost["judge_aggregate_wall_seconds"]),
            "observed_wall_seconds": observed_wall,
            "pre_attribution_observed_upper_seconds": cost_bound["pre_attribution_observed_upper_seconds"],
            "projected_level_wall_seconds": float(cost_bound["pre_attribution_observed_upper_seconds"]) + observed_wall,
            "level_total_wall_seconds_maximum": cost_bound["level_total_wall_seconds_maximum"],
            "transport": transport,
            "retry_or_rate_limit_degradation": False,
        },
        "fail_closed": True,
        "formal_state_sha256_before": hashlib.sha256(state_before).hexdigest(),
        "formal_state_sha256_after": hashlib.sha256(state_path.read_bytes()).hexdigest(),
        "raw_scratch_destroyed": True,
    }
    _require(content["formal_state_sha256_before"] == content["formal_state_sha256_after"], "官方评测器资格改写正式 state")
    result = {**content, "identity": evidence.canonical_sha256(content)}
    evidence.atomic_json(result_path, result)
    return {"passed": True, "reused": False, "model_calls": int(attribution["reader_calls"]) + int(attribution["judge_calls"]), "product_executions": 0, "plan_identity": plan_identity, "result": str(result_path), "identity": result["identity"]}


def _validate_result(value: dict[str, Any], plan_identity: str, dependencies: dict[str, str]) -> None:
    _require(value.get("schema") == RESULT_SCHEMA and value.get("plan_identity") == plan_identity, "官方评测器资格终态错绑")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), "官方评测器资格终态摘要漂移")
    _require(value.get("passed") is True and value.get("fail_closed") is True, "官方评测器资格未通过")
    _require(value.get("direct_dependencies") == dependencies, "官方评测器资格直接依赖漂移")
    _require(value.get("candidate_executions") == value.get("product_executions") == 0, "官方评测器资格越权执行候选或产品")
    _require(value.get("formal_state_sha256_before") == value.get("formal_state_sha256_after"), "官方评测器资格改写正式 state")
    attribution = _mapping(value, "attribution")
    _require(
        attribution.get("questions") == 5
        and attribution.get("reader_calls") == 25
        and attribution.get("judge_calls") == 40
        and attribution.get("judge_controls_passed") is True,
        "oracle Reader 资格结果漂移",
    )
    reader_settings = _mapping(attribution, "reader_settings")
    _require(
        reader_settings.get("model") == "gpt-5.6-terra"
        and reader_settings.get("reasoning_effort") == "xhigh",
        "oracle Reader 资格模型或推理强度漂移",
    )
    _require(float(attribution["projected_level_wall_seconds"]) <= float(attribution["level_total_wall_seconds_maximum"]), "oracle Reader 资格没有闭合 50 题墙钟")


def load_current_qualification(suite_root: Path, execution_config: Path) -> dict[str, Any]:
    receipt = _load_json(suite_root.resolve() / QUALIFICATION_RELATIVE)
    content = {key: item for key, item in receipt.items() if key != "identity"}
    _require(receipt.get("schema") == RECEIPT_SCHEMA, "官方评测器资格收据 schema 无效")
    _require(receipt.get("identity") == evidence.canonical_sha256(content), "官方评测器资格收据身份漂移")
    result_path = Path(str(receipt["result"])).resolve()
    _require(result_path.is_file() and evidence.file_sha256(result_path) == receipt.get("result_sha256"), "官方评测器资格结果缺失或漂移")
    result = _load_json(result_path)
    planned_dependencies = _mapping(result, "direct_dependencies")
    _validate_result(result, str(result["plan_identity"]), planned_dependencies)
    dependencies, _runtime, _contract = current_dependencies(suite_root, execution_config)
    if planned_dependencies != dependencies:
        migration = _load_json(suite_root.resolve() / DEPENDENCY_MIGRATION_RELATIVE)
        migration_content = {key: item for key, item in migration.items() if key != "identity"}
        _require(migration.get("schema") == DEPENDENCY_MIGRATION_SCHEMA, "官方评测器资格依赖迁移 schema 无效")
        _require(migration.get("identity") == evidence.canonical_sha256(migration_content), "官方评测器资格依赖迁移身份漂移")
        _require(
            migration.get("qualification_result_identity") == result.get("identity")
            and migration.get("qualification_result_sha256") == receipt.get("result_sha256"),
            "官方评测器资格依赖迁移错绑结果",
        )
        _require(_mapping(migration, "source_dependencies") == planned_dependencies, "官方评测器资格依赖迁移源错绑")
        _require(_mapping(migration, "target_dependencies") == dependencies, "官方评测器资格直接依赖漂移")
        proof = _mapping(migration, "proof")
        module = validation._load_longmemeval_module(suite_root)
        _require(
            proof.get("source_longmemeval_file_sha256") == planned_dependencies.get("longmemeval-executor")
            and proof.get("target_longmemeval_file_sha256") == evidence.file_sha256(Path(module.__file__).resolve()),
            "官方评测器资格 LongMemEval 源或目标执行器错绑",
        )
        _require(
            proof.get("source_qualification_surface_identity") == proof.get("qualification_surface_identity")
            and proof.get("qualification_surface_identity") == dependencies.get("longmemeval-executor")
            and proof.get("qualification_surface_identity") == _longmemeval_qualification_surface_identity(module),
            "官方评测器资格实际消费表面漂移",
        )
        _require(
            migration.get("candidate_executions") == 0
            and migration.get("product_executions") == 0
            and migration.get("qualification_result_rewritten") is False,
            "官方评测器资格依赖迁移越权",
        )
    _require(result.get("identity") == receipt.get("result_identity"), "官方评测器资格结果身份错绑")
    return receipt
