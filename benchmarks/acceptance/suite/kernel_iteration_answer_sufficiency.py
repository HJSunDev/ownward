from __future__ import annotations

import hashlib
import json
import re
import sys
import time
from concurrent.futures import ThreadPoolExecutor, as_completed
from contextlib import contextmanager, nullcontext
from pathlib import Path
from typing import Any, Callable

import kernel_iteration_evidence as evidence
import kernel_iteration_official_evaluator as official_evaluator
import kernel_iteration_validation as validation


CONTRACT_SCHEMA = "ownward.kernel-iteration-stage3-answer-sufficiency-contract/v2"
PLAN_SCHEMA = "ownward.kernel-iteration-stage3-answer-sufficiency-plan/v2"
LEGACY_RESULT_SCHEMA = "ownward.kernel-iteration-stage3-answer-sufficiency-result/v2"
RESULT_SCHEMA = "ownward.kernel-iteration-stage3-answer-sufficiency-result/v3"
CONTRACT_RELATIVE = Path("iteration/v2/stage3-final-answer-sufficiency-contract.json")


class AnswerSufficiencyError(ValueError):
    pass


class AttributionDiagnosticError(RuntimeError):
    def __init__(self, category: str, stage: str, error: BaseException) -> None:
        super().__init__(f"answer attribution failed at {category}:{stage}")
        self.category = category
        self.stage = stage
        self.original_error_type = type(error).__name__
        self.original_message_sha256 = hashlib.sha256(str(error).encode("utf-8")).hexdigest()


_SAFE_ATTRIBUTION_SUMMARIES = {
    "reader": "Reader execution failed before a validated attribution result was available.",
    "judge": "Judge execution failed before a validated attribution result was available.",
    "official-prompt-renderer": "Official prompt rendering failed before a validated attribution result was available.",
    "schema-validation": "Attribution schema or validation failed before a validated result was available.",
    "transport": "Attribution transport failed before a validated result was available.",
    "evaluation-controller": "Attribution controller failed before a validated result was available.",
}


def safe_attribution_exception(error: BaseException, default_stage: str) -> dict[str, str]:
    if isinstance(error, AttributionDiagnosticError):
        category = error.category
        stage = error.stage
        error_type = error.original_error_type
        message_sha256 = error.original_message_sha256
    else:
        category = "evaluation-controller"
        stage = default_stage
        error_type = type(error).__name__
        message_sha256 = hashlib.sha256(str(error).encode("utf-8")).hexdigest()
    content = {
        "category": category,
        "stage": stage,
        "error_type": error_type,
        "message_sha256": message_sha256,
        "safe_summary": _SAFE_ATTRIBUTION_SUMMARIES[category],
    }
    return {**content, "identity": evidence.canonical_sha256(content)}


def _attribution_failure(category: str, stage: str, error: BaseException) -> AttributionDiagnosticError:
    if isinstance(error, AttributionDiagnosticError):
        return error
    return AttributionDiagnosticError(category, stage, error)


def _attribution_call(category: str, stage: str, function: Callable[[], Any]) -> Any:
    try:
        return function()
    except AttributionDiagnosticError:
        raise
    except Exception as error:
        raise _attribution_failure(category, stage, error) from error


@contextmanager
def _attribution_context(context: Any, category: str, stage: str) -> Any:
    try:
        with context as value:
            yield value
    except AttributionDiagnosticError:
        raise
    except Exception as error:
        raise _attribution_failure(category, stage, error) from error


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise AnswerSufficiencyError(message)


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
    _require(value.get("schema") == CONTRACT_SCHEMA, "最终回答充分性合同 schema 无效")
    _require(value.get("frozen_before_diagnostic_results") is True, "最终回答充分性合同没有在结果前冻结")
    _require(value.get("contains_blind_or_formal_question_answer_gold_content_outputs_or_case_ids") is False, "最终回答充分性合同越过盲测或正式内容边界")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), "最终回答充分性合同身份漂移")
    trigger = _mapping(value, "aggregate_trigger")
    _require(trigger.get("raw_content_destroyed") is True and trigger.get("same_content_rerun_forbidden") is True, "25 题盲测原始内容边界漂移")
    _require(trigger.get("first_observed_gaps") == {"evidence_read_answer_incorrect": 2, "none": 23}, "25 题聚合首缺口漂移")
    _require(trigger.get("only_general_boundary_reused") == "final-answer-sufficiency-boundary", "盲测只允许复用通用边界")
    sources = _mapping(value, "sources")
    loaded: dict[str, dict[str, Any]] = {}
    for name in ("diagnosis_materials", "confirmation_materials", "regression_materials"):
        source = _mapping(sources, name)
        relative = Path(str(source.get("path", "")))
        _require(not relative.is_absolute() and ".." not in relative.parts, f"{name} 路径越界")
        source_path = (suite_root / relative).resolve()
        _require(source_path.is_relative_to(suite_root) and source_path.is_file(), f"{name} 缺失")
        _require(source.get("sha256") == evidence.file_sha256(source_path), f"{name} 文件摘要漂移")
        materials = validation.validate_stage3_materials(_load_json(source_path))
        _require(source.get("identity") == materials["identity"], f"{name} 身份错绑")
        provenance = _mapping(_mapping(materials, "criteria"), "provenance")
        _require(
            provenance.get("formal_source_access") is False
            and provenance.get("blind_gate_reuse") is False
            and provenance.get("stage2_calibration_reuse") is False,
            f"{name} 来源边界无效",
        )
        loaded[name] = materials
    diagnosis = loaded["diagnosis_materials"]
    confirmation = loaded["confirmation_materials"]
    regression = loaded["regression_materials"]
    diagnosis_facts = {validation._case_fact_identity(case) for case in diagnosis["cases"]}
    confirmation_facts = {validation._case_fact_identity(case) for case in confirmation["cases"]}
    regression_facts = {validation._case_fact_identity(case) for case in regression["cases"]}
    _require(
        diagnosis_facts.isdisjoint(confirmation_facts)
        and diagnosis_facts.isdisjoint(regression_facts)
        and confirmation_facts.isdisjoint(regression_facts),
        "诊断、确认与正确能力回归事实重合",
    )
    coverage = _mapping(value, "coverage")
    _require(set(coverage.get("diagnosis_required", [])) == {case["coverage"] for case in diagnosis["cases"]}, "诊断覆盖漂移")
    _require(set(coverage.get("confirmation_required", [])) == {case["coverage"] for case in confirmation["cases"]}, "确认覆盖漂移")
    _require(set(coverage.get("regression_required", [])) == {case["coverage"] for case in regression["cases"]}, "回归覆盖漂移")
    repetitions = _mapping(value, "repetitions")
    _require(repetitions == {
        "product_context_total_per_case_including_original": 3,
        "oracle_context_per_case": 3,
        "identical_prompt_required": True,
        "model_and_effort_unchanged": True,
    }, "Reader 重复合同漂移")
    gates = _mapping(value, "gates")
    _require(gates.get("maximum_affected_feedback_seconds") == 600 and gates.get("formal_state_writes") == 0, "诊断成本或正式状态边界漂移")
    return {**value, "loaded": loaded}


def run(
    suite_root: Path,
    output_root: Path,
    candidate_execution_config: Path,
    baseline_execution_config: Path | None,
    candidate_subject_manifest: Path,
    formal_state: Path,
    *,
    phase: str = "final",
    reproduction_result_path: Path | None = None,
    resume: bool = False,
    execute: Callable[..., dict[str, Any]] | None = None,
    codex_diagnose: Callable[..., dict[str, Any]] | None = None,
) -> dict[str, Any]:
    suite_root = suite_root.resolve()
    output_root = output_root.resolve()
    repository = suite_root.parents[2]
    evidence._validate_output_boundary(repository, output_root)
    _require(phase in {"reproduction", "final"}, "最终回答充分性阶段无效")
    contract = load_contract(suite_root)
    state_path = formal_state.resolve()
    _require(state_path.is_file(), "最终回答充分性诊断缺少正式 state 只读基线")
    state_before = state_path.read_bytes()
    candidate_runtime = validation.validate_execution_config(suite_root, candidate_execution_config.resolve())
    subject = _load_json(candidate_subject_manifest.resolve())
    phase_subject = _mapping(_mapping(contract, "subject"), phase)
    _require(subject.get("identity") == phase_subject.get("identity"), "最终回答充分性诊断候选错绑")
    dependencies = {
        "contract": contract["identity"],
        "controller": evidence.file_sha256(Path(__file__).resolve()),
        "controller-entry": evidence.file_sha256(suite_root / "kernel_iteration_run.py"),
        "candidate-config": evidence.file_sha256(candidate_execution_config.resolve()),
        "candidate-subject": subject["identity"],
        "formal-state": evidence.file_sha256(state_path),
        "validation-contract": validation.load_validation_contract(suite_root)["identity"],
    }
    reproduction_result: dict[str, Any] | None = None
    if baseline_execution_config is not None:
        validation.validate_execution_config(suite_root, baseline_execution_config.resolve())
        dependencies["baseline-config"] = evidence.file_sha256(baseline_execution_config.resolve())
    if phase == "final":
        _require(reproduction_result_path is not None and reproduction_result_path.resolve().is_file(), "终态验证缺少已封存根因复现结果")
        reproduction_result = _load_json(reproduction_result_path.resolve())
        _validate_result(reproduction_result, str(reproduction_result.get("plan_identity", "")))
        _require(
            reproduction_result.get("phase") == "reproduction"
            and _mapping(reproduction_result, "root_cause").get("responsible_component") == "kernel-context"
            and reproduction_result.get("passed") is True,
            "终态验证没有绑定已证明的内核上下文根因",
        )
        dependencies["reproduction-result"] = evidence.file_sha256(reproduction_result_path.resolve())
    content = {
        "schema": PLAN_SCHEMA,
        "purpose": "independent-answer-sufficiency-root-diagnosis",
        "contract_identity": contract["identity"],
        "subject_identity": subject["identity"],
        "phase": phase,
        "direct_dependencies": dict(sorted(dependencies.items())),
        "formal": False,
        "candidate_decision": None,
        "blind_gate_execution": False,
    }
    plan = {**content, "identity": evidence.canonical_sha256(content)}
    root = output_root / "answer-sufficiency" / plan["identity"]
    result_path = root / "result.json"
    plan_path = root / "plan.json"
    if result_path.is_file():
        _require(resume and plan_path.is_file() and _load_json(plan_path) == plan, "最终回答充分性终态只能由同一身份恢复")
        result = _load_json(result_path)
        _validate_result(result, plan["identity"], reproduction_result)
        _require(state_path.read_bytes() == state_before, "最终回答充分性终态复用改写正式 state")
        return {**result, "result_path": str(result_path), "reused": True, "model_or_product_execution": False}
    if plan_path.is_file():
        _require(resume and _load_json(plan_path) == plan, "最终回答充分性计划身份漂移")
    else:
        evidence.atomic_json(plan_path, plan)

    started = time.perf_counter()
    input_root = root / "inputs"
    executions: dict[str, dict[str, Any]] = {}
    product_execution_performed = False
    execution_callback = execute or validation.execute_prepared_evidence
    material_runs = [("diagnosis_materials", "diagnosis", "development")]
    if phase == "final":
        material_runs.extend([
            ("confirmation_materials", "confirmation", "development"),
            ("regression_materials", "regression", "regression"),
        ])
    for material_name, label, evidence_type in material_runs:
        materials = contract["loaded"][material_name]
        material_path = input_root / f"{label}.json"
        input_path = input_root / f"{label}-input.json"
        evidence.atomic_json(material_path, materials)
        validation.build_input_manifest(suite_root, material_path, candidate_execution_config.resolve(), evidence_type, input_path)
        candidate = execution_callback(
            suite_root, output_root / "answer-sufficiency-executions", candidate_execution_config.resolve(),
            subject_manifest=candidate_subject_manifest.resolve(), evidence_type=evidence_type,
            input_manifest=input_path, resume=resume,
        )
        product_execution_performed = product_execution_performed or candidate.get("reused_execution") is not True
        candidate_result = Path(str(candidate["execution_result"])).resolve()
        executions[f"candidate-{label}"] = _load_json(candidate_result)

    diagnosis_execution = executions["candidate-diagnosis"]
    diagnosis_run_root = candidate_runtime["runs"] / "kernel-iteration" / diagnosis_execution["plan_identity"] / "run"
    _require(diagnosis_run_root.is_dir(), "最终回答充分性诊断缺少候选原始执行轨迹")
    codex_result = (codex_diagnose or _diagnose_codex_boundaries)(
        suite_root, output_root / "answer-sufficiency-attribution" / diagnosis_execution["plan_identity"], candidate_runtime,
        contract["loaded"]["diagnosis_materials"], diagnosis_run_root,
        oracle_repeats=(1, 2, 3),
        prompt_renderer_factory=official_evaluator.PromptRenderer,
    )
    observer_replay = _replay_observer(diagnosis_run_root, contract["loaded"]["diagnosis_materials"], diagnosis_execution)
    confirmation_execution = executions.get("candidate-confirmation")
    regression_execution = executions.get("candidate-regression")
    confirmation_codex: dict[str, Any] | None = None
    confirmation_replay: dict[str, Any] | None = None
    regression_replay: dict[str, Any] | None = None
    if phase == "final":
        _require(confirmation_execution is not None and regression_execution is not None, "终态确认或回归执行缺失")
        confirmation_run_root = candidate_runtime["runs"] / "kernel-iteration" / confirmation_execution["plan_identity"] / "run"
        confirmation_codex = (codex_diagnose or _diagnose_codex_boundaries)(
            suite_root, output_root / "answer-sufficiency-attribution" / confirmation_execution["plan_identity"], candidate_runtime,
            contract["loaded"]["confirmation_materials"], confirmation_run_root,
            oracle_repeats=(1, 2, 3),
            prompt_renderer_factory=official_evaluator.PromptRenderer,
        )
        confirmation_replay = _replay_observer(
            confirmation_run_root, contract["loaded"]["confirmation_materials"], confirmation_execution,
        )
        regression_run_root = candidate_runtime["runs"] / "kernel-iteration" / regression_execution["plan_identity"] / "run"
        regression_replay = _replay_observer(
            regression_run_root, contract["loaded"]["regression_materials"], regression_execution,
        )
    classification = classify_root(contract, diagnosis_execution, regression_execution, codex_result, observer_replay)
    root_cause, root_cause_evidence, repair_validation = _bind_root_semantics(
        phase, reproduction_result, classification,
    )
    elapsed = time.perf_counter() - started
    gates = _mapping(contract, "gates")
    reproduction_passed = phase == "reproduction" and classification["responsible_component"] == "kernel-context"
    final_passed = phase == "final" and all(
        item is not None and bool(item.get("passed"))
        for item in (diagnosis_execution, confirmation_execution, regression_execution)
    ) and classification["status"] == "not-reproduced-with-counterevidence" and all((
        confirmation_codex is not None,
        int(_mapping(confirmation_codex, "reader")["product_context_failures"]) == 0 if confirmation_codex else False,
        int(_mapping(confirmation_codex, "reader")["oracle_context_failures"]) == 0 if confirmation_codex else False,
        bool(_mapping(confirmation_codex, "judge")["controls_passed"]) if confirmation_codex else False,
        bool(confirmation_replay and confirmation_replay["exact"]),
        bool(regression_replay and regression_replay["exact"]),
    ))
    passed = (reproduction_passed or final_passed) and elapsed <= float(gates["maximum_affected_feedback_seconds"])
    stage3_closed = phase == "final" and passed
    execution_bindings = {
        name: {"identity": item["identity"], "passed": item["passed"], "subject_identity": item["subject_identity"]}
        for name, item in sorted(executions.items())
    }
    if repair_validation is not None:
        _require(confirmation_codex is not None, "终态修复验证缺少独立确认边界")
        repair_validation = {
            **repair_validation,
            "candidate_subject_identity": subject["identity"],
            "evidence": {
                "diagnosis_execution_identity": execution_bindings["candidate-diagnosis"]["identity"],
                "confirmation_execution_identity": execution_bindings["candidate-confirmation"]["identity"],
                "regression_execution_identity": execution_bindings["candidate-regression"]["identity"],
                "diagnosis_passed": execution_bindings["candidate-diagnosis"]["passed"],
                "confirmation_passed": execution_bindings["candidate-confirmation"]["passed"],
                "regression_passed": execution_bindings["candidate-regression"]["passed"],
                "diagnosis_reader_product_failures": int(_mapping(codex_result, "reader")["product_context_failures"]),
                "diagnosis_reader_oracle_failures": int(_mapping(codex_result, "reader")["oracle_context_failures"]),
                "diagnosis_judge_controls_passed": bool(_mapping(codex_result, "judge")["controls_passed"]),
                "confirmation_reader_product_failures": int(_mapping(confirmation_codex, "reader")["product_context_failures"]),
                "confirmation_reader_oracle_failures": int(_mapping(confirmation_codex, "reader")["oracle_context_failures"]),
                "confirmation_judge_controls_passed": bool(_mapping(confirmation_codex, "judge")["controls_passed"]),
                "diagnosis_replay_exact": bool(observer_replay["exact"]),
                "confirmation_replay_exact": bool(confirmation_replay and confirmation_replay["exact"]),
                "regression_replay_exact": bool(regression_replay and regression_replay["exact"]),
            },
        }
    model_turn_performed = any(
        int(_mapping(item, "transport").get("max_active", 0)) > 0
        for item in (codex_result, confirmation_codex)
        if item is not None
    )
    result_content = {
        "schema": RESULT_SCHEMA,
        "plan_identity": plan["identity"],
        "contract_identity": contract["identity"],
        "phase": phase,
        "status": "root-reproduced" if reproduction_passed and passed else ("passed" if final_passed and passed else "failed"),
        "passed": passed,
        "formal": False,
        "formal_state_written": False,
        "blind_gate_executed": False,
        "candidate_decision": None,
        "root_cause": root_cause,
        "root_cause_evidence": root_cause_evidence,
        "repair_validation": repair_validation,
        "reproduction_result_identity": reproduction_result.get("identity") if reproduction_result else None,
        "executions": execution_bindings,
        "codex_boundary_diagnosis": codex_result,
        "confirmation_codex_boundary": confirmation_codex,
        "observer_replay": observer_replay,
        "confirmation_observer_replay": confirmation_replay,
        "regression_observer_replay": regression_replay,
        "stage_reclosure": {
            "stage3": "closed" if stage3_closed else "open",
            "stage4": "requires-current-candidate-revalidation" if stage3_closed else "unchanged-until-candidate-change",
            "stage5": "requires-current-candidate-revalidation" if stage3_closed else "unchanged-until-candidate-change",
            "candidate_subject_identity": subject["identity"],
            "kernel_generation_identity": subject["kernel_generation_identity"],
            "kernel_effect_identity": subject["kernel_effect_identity"],
        },
        "cost": {"wall_seconds": elapsed, "maximum_wall_seconds": float(gates["maximum_affected_feedback_seconds"])},
        "regeneration": {
            "product_execution_performed": product_execution_performed,
            "model_turn_performed": model_turn_performed,
            "source": "existing-checkpoints" if not product_execution_performed and not model_turn_performed else "new-execution",
        },
        "formal_state_sha256": evidence.file_sha256(state_path),
        "next_action": "proceed-to-stage4-current-candidate-validation" if stage3_closed else "repair-proven-source-local-context-mechanism",
    }
    result = {**result_content, "identity": evidence.canonical_sha256(result_content)}
    evidence.atomic_json(result_path, result)
    _require(state_path.read_bytes() == state_before, "最终回答充分性诊断改写正式 state")
    _validate_result(result, plan["identity"], reproduction_result)
    return {
        **result,
        "result_path": str(result_path),
        "reused": False,
        "model_or_product_execution": product_execution_performed or model_turn_performed,
    }


def _bind_root_semantics(
    phase: str,
    reproduction_result: dict[str, Any] | None,
    candidate_classification: dict[str, Any],
) -> tuple[dict[str, Any], dict[str, Any] | None, dict[str, Any] | None]:
    if phase == "reproduction":
        return dict(candidate_classification), None, None
    _require(reproduction_result is not None, "终态缺少根因复现结果")
    proven_root = dict(_mapping(reproduction_result, "root_cause"))
    _require(
        proven_root.get("status") == "proven"
        and proven_root.get("responsible_component") == "kernel-context",
        "终态根因不是复现阶段已经证明的内核上下文根因",
    )
    _require(
        candidate_classification.get("status") == "not-reproduced-with-counterevidence"
        and candidate_classification.get("responsible_component") == "external-answer-boundary",
        "当前候选没有形成独立的修复反证",
    )
    root_evidence = {
        "schema": "ownward.kernel-iteration-stage3-root-cause-evidence/v1",
        "reproduction_result_identity": reproduction_result.get("identity"),
        "reproduction_plan_identity": reproduction_result.get("plan_identity"),
        "root_cause_identity": evidence.canonical_sha256(proven_root),
    }
    repair = {
        "schema": "ownward.kernel-iteration-stage3-repair-validation/v1",
        "status": "repaired-not-reproduced",
        "candidate_classification": dict(candidate_classification),
    }
    return proven_root, root_evidence, repair


def classify_root(
    contract: dict[str, Any],
    diagnosis_execution: dict[str, Any],
    regression_execution: dict[str, Any] | None,
    codex_result: dict[str, Any],
    observer_replay: dict[str, Any],
) -> dict[str, Any]:
    missing_questions = int(_mapping(diagnosis_execution["observation"], "fact_delivery")["missing_questions"])
    diagnosis_accuracy = float(diagnosis_execution["observation"]["final_answer_accuracy"])
    regression_accuracy = float(regression_execution["observation"]["final_answer_accuracy"]) if regression_execution else 0.0
    product_failures = int(codex_result["reader"]["product_context_failures"])
    oracle_failures = int(codex_result["reader"]["oracle_context_failures"])
    product_variations = int(codex_result["reader"]["product_context_variations"])
    oracle_variations = int(codex_result["reader"]["oracle_context_variations"])
    judge_failed = not bool(codex_result["judge"]["controls_passed"])
    if product_failures > 0 and oracle_failures == 0 and missing_questions == 0:
        component = "kernel-context"
        status = "proven"
        mechanism = "required-truth-not-stably-usable-in-product-context"
    elif missing_questions > 0:
        component = "evidence-delivery"
        status = "proven"
        mechanism = "required-truth-not-delivered"
    elif product_failures > 0 or oracle_failures > 0:
        component = "reader"
        status = "proven"
        mechanism = "reader-mechanical-failure-under-frozen-evidence"
    elif judge_failed:
        component = "judge-or-scorer"
        status = "proven"
        mechanism = "official-judge-control-discrimination-failure"
    elif not bool(observer_replay["exact"]):
        component = "executor-or-observer"
        status = "proven"
        mechanism = "derived-observation-replay-drift"
    else:
        gates = _mapping(contract, "gates")
        counterevidence = (
            missing_questions == 0
            and diagnosis_accuracy >= float(gates["final_candidate_diagnosis_accuracy"])
            and regression_accuracy >= float(gates["regression_accuracy"])
            and product_failures == 0 and oracle_failures == 0
            and bool(codex_result["judge"]["controls_passed"])
            and bool(observer_replay["exact"])
        )
        component = "external-answer-boundary" if counterevidence else "unresolved"
        status = "not-reproduced-with-counterevidence" if counterevidence else "unresolved"
        mechanism = "independent-complete-evidence-reader-judge-chain-stable" if counterevidence else "insufficient-independent-evidence"
    return {
        "status": status,
        "responsible_component": component,
        "mechanism": mechanism,
        "blind_aggregate_preserved": True,
        "kernel_change_required": component == "kernel-context",
        "responsible_component_changed": False,
        "repair_boundary": (
            "candidate-kernel-context" if component == "kernel-context"
            else "frozen-external-evaluation-boundary-no-change-authorized"
        ),
        "blind_failure_reclassified_as_random_without_evidence": False,
    }


def _diagnose_codex_boundaries(
    suite_root: Path,
    stage_root: Path,
    runtime: dict[str, Any],
    materials: dict[str, Any],
    run_root: Path,
    **kwargs: Any,
) -> dict[str, Any]:
    try:
        return _diagnose_codex_boundaries_impl(
            suite_root, stage_root, runtime, materials, run_root, **kwargs,
        )
    except AttributionDiagnosticError:
        raise
    except Exception as error:
        raise _attribution_failure("schema-validation", "attribution-validation", error) from error


def _diagnose_codex_boundaries_impl(
    suite_root: Path,
    stage_root: Path,
    runtime: dict[str, Any],
    materials: dict[str, Any],
    run_root: Path,
    *,
    reader_settings: dict[str, Any] | None = None,
    include_original_product_answer: bool = True,
    product_repeats: tuple[int, ...] = (2, 3),
    oracle_repeats: tuple[int, ...] = (1, 2),
    settings_label: str = "frozen",
    run_judge: bool = True,
    correctness_source: str = "atoms",
    prompt_renderer_factory: Callable[[Path, Path], Any] | None = None,
) -> dict[str, Any]:
    _require(correctness_source in {"atoms", "judge"}, "Reader 正确性来源无效")
    _require(correctness_source != "judge" or run_judge, "Judge 正确性来源必须实际运行 Judge")
    started = time.perf_counter()
    module = validation._load_longmemeval_module(suite_root)
    protocol = runtime["protocol_value"]
    evaluator = Path(str(_mapping(runtime["environment"], "layout")["source"])) / "src" / "evaluation" / "evaluate_qa.py"
    renderer_context: Any = nullcontext(None)
    if prompt_renderer_factory is not None:
        python_root = Path(str(_mapping(runtime["environment"], "layout")["python"]))
        python = python_root / ("Scripts/python.exe" if (python_root / "Scripts/python.exe").is_file() else "bin/python")
        renderer_context = _attribution_call(
            "official-prompt-renderer", "renderer-construction",
            lambda: prompt_renderer_factory(python, evaluator),
        )
    runtime_parent = (
        suite_root.parents[2] / ".tmp" / "kernel-v2-answer-runtime"
        / evidence.canonical_sha256({"stage": str(stage_root.resolve())})[:16]
    )

    reader_jobs: list[dict[str, Any]] = []
    original_answers: dict[str, str] = {}
    product_prompt_hashes: dict[str, str] = {}
    for case in materials["cases"]:
        case_id = str(case["case_id"])
        question_root = run_root / "questions" / case_id
        reader_input = _load_json(question_root / "reader" / "input.json")
        reader_output = _load_json(question_root / "reader" / "output.json")
        prompt = str(reader_input["prompt"])
        if include_original_product_answer:
            original_answers[case_id] = str(reader_output["answer"])
        product_prompt_hashes[case_id] = hashlib.sha256(prompt.encode("utf-8")).hexdigest()
        for repeat in product_repeats:
            reader_jobs.append({"case": case, "context": "product", "repeat": repeat, "prompt": prompt})
        session_by_id = {session["session_id"]: session for session in case["sessions"]}
        oracle_evidence = [
            {"id": f"oracle-{session_id}", "content": module.session_content(session_id, session_by_id[session_id]["date"], session_by_id[session_id]["turns"])}
            for session_id in case["answer_session_ids"]
        ]
        oracle_prompt = module._answer_prompt(case, oracle_evidence)
        for repeat in oracle_repeats:
            reader_jobs.append({"case": case, "context": "oracle", "repeat": repeat, "prompt": oracle_prompt})

    answers: list[dict[str, Any]] = []
    external = _mapping(runtime, "external_intelligence")
    transport_context = module.open_external_intelligence_runtime(
        driver=external["driver"], binary=external["binary"], credential_file=external["credential_file"],
        max_active=4, worker_processes=4, runtime_parent=runtime_parent,
    )
    with _attribution_context(renderer_context, "official-prompt-renderer", "renderer-lifecycle") as evaluator_renderer, \
            _attribution_context(transport_context, "transport", "transport-lifecycle") as transport:
        capability = module.ExternalIntelligenceCapability(transport)

        def run_reader(job: dict[str, Any]) -> dict[str, Any]:
            case = job["case"]
            stage = stage_root / "reader" / settings_label / str(case["case_id"]) / str(job["context"]) / f"repeat-{job['repeat']}"
            answer, usage = _attribution_call(
                "reader", "reader-execution",
                lambda: capability.answer(str(job["prompt"]), reader_settings or protocol["reader"], stage),
            )
            return {
                "case_id": case["case_id"], "coverage": case["coverage"], "context": job["context"],
                "repeat": job["repeat"], "prompt_sha256": hashlib.sha256(str(job["prompt"]).encode("utf-8")).hexdigest(),
                "answer": answer, "answer_sha256": hashlib.sha256(answer.encode("utf-8")).hexdigest(), "usage": usage,
            }

        with ThreadPoolExecutor(max_workers=4) as executor:
            futures = [executor.submit(run_reader, job) for job in reader_jobs]
            for future in as_completed(futures):
                answers.append(future.result())

        all_answers: list[dict[str, Any]] = []
        if include_original_product_answer:
            for case in materials["cases"]:
                case_id = str(case["case_id"])
                original = original_answers[case_id]
                all_answers.append({
                    "case_id": case_id, "coverage": case["coverage"], "context": "product", "repeat": 1,
                    "prompt_sha256": product_prompt_hashes[case_id], "answer": original,
                    "answer_sha256": hashlib.sha256(original.encode("utf-8")).hexdigest(), "usage": {},
                })
        all_answers.extend(answers)
        judge_jobs = []
        if run_judge:
            judge_jobs = [{"kind": "reader", "case": next(case for case in materials["cases"] if case["case_id"] == item["case_id"]), "answer": item["answer"], "key": item} for item in all_answers]
            for case in materials["cases"]:
                judge_jobs.append({"kind": "correct-control", "case": case, "answer": case["answer"], "key": None})
                judge_jobs.append({"kind": "wrong-control", "case": case, "answer": "unsupported-control-answer", "key": None})

        judged: list[dict[str, Any]] = []

        def execute_judge(job: dict[str, Any]) -> dict[str, Any]:
            case = job["case"]
            evaluation_case = {**case, "question_id": case["case_id"]}
            prompt = _attribution_call(
                "official-prompt-renderer", "judge-prompt-render",
                lambda: (
                    evaluator_renderer.render(evaluation_case, str(job["answer"]))
                    if evaluator_renderer is not None
                    else module.official_prompt(evaluator, evaluation_case, str(job["answer"]))
                ),
            )
            reader_key = job.get("key") if isinstance(job.get("key"), dict) else {}
            suffix = evidence.canonical_sha256({
                "kind": str(job["kind"]),
                "context": reader_key.get("context"),
                "repeat": reader_key.get("repeat"),
                "answer_sha256": hashlib.sha256(str(job["answer"]).encode("utf-8")).hexdigest(),
            })[:12]
            stage = stage_root / "judge" / str(case["case_id"]) / str(job["kind"]) / suffix
            label, output, usage = _attribution_call(
                "judge", "judge-execution",
                lambda: capability.judge(prompt, protocol["judge"], stage),
            )
            return {
                "kind": job["kind"], "case_id": case["case_id"], "label": label, "output": output,
                "answer_sha256": hashlib.sha256(str(job["answer"]).encode("utf-8")).hexdigest(), "reader_key": job["key"], "usage": usage,
            }

        if judge_jobs:
            with ThreadPoolExecutor(max_workers=4) as executor:
                futures = [executor.submit(execute_judge, job) for job in judge_jobs]
                for future in as_completed(futures):
                    judged.append(future.result())
        transport_diagnostics = transport.diagnostics()

    atoms = _mapping(load_contract(suite_root), "mechanical_answer_atoms")
    reader_judgments = [item for item in judged if item["kind"] == "reader"]
    reader_records: list[dict[str, Any]] = []
    product_failures = oracle_failures = 0
    product_variations = oracle_variations = 0
    for case in materials["cases"]:
        case_id = str(case["case_id"])
        for context in ("product", "oracle"):
            items = sorted((item for item in all_answers if item["case_id"] == case_id and item["context"] == context), key=lambda item: int(item["repeat"]))
            expected = len(product_repeats) + (1 if include_original_product_answer else 0) if context == "product" else len(oracle_repeats)
            _require(len(items) == expected, f"{case_id} Reader 重复数量不完整")
            prompt_hashes = {item["prompt_sha256"] for item in items}
            _require(len(prompt_hashes) == 1, f"{case_id} {context} Reader 提示并非逐字相同")
            if correctness_source == "atoms":
                mechanical = [_answer_matches(item["answer"], atoms[case["coverage"]]) for item in items]
            else:
                mechanical = []
                for item in items:
                    match = next((judgment for judgment in reader_judgments if judgment.get("reader_key") == item), None)
                    _require(match is not None and isinstance(match.get("label"), bool), f"{case_id} {context} 缺少冻结 Judge 标签")
                    mechanical.append(bool(match["label"]))
            failures = sum(not value for value in mechanical)
            variations = int(len({item["answer_sha256"] for item in items}) > 1)
            if context == "product":
                product_failures += failures
                product_variations += variations
            else:
                oracle_failures += failures
                oracle_variations += variations
            reader_records.append({
                "case_identity": validation._case_fact_identity(case), "context": context,
                "observations": len(items), "correct": sum(mechanical), "variations": variations,
                "prompt_sha256": next(iter(prompt_hashes)), "answer_sha256s": [item["answer_sha256"] for item in items],
            })
    correct_controls = [item for item in judged if item["kind"] == "correct-control"]
    wrong_controls = [item for item in judged if item["kind"] == "wrong-control"]
    controls_passed = (
        all(item["label"] is True for item in correct_controls)
        and all(item["label"] is False for item in wrong_controls)
        if run_judge else None
    )
    reader_walls = [float(_mapping(item, "usage").get("wall_seconds", 0.0)) for item in answers]
    judge_walls = [float(_mapping(item, "usage").get("wall_seconds", 0.0)) for item in judged]
    return {
        "reader": {
            "settings": dict(reader_settings or protocol["reader"]),
            "correctness_source": correctness_source,
            "product_context_failures": product_failures,
            "oracle_context_failures": oracle_failures,
            "product_context_variations": product_variations,
            "oracle_context_variations": oracle_variations,
            "records": reader_records,
            "cost": {
                "measured_calls": len(reader_walls),
                "aggregate_wall_seconds": sum(reader_walls),
                "mean_wall_seconds": sum(reader_walls) / len(reader_walls) if reader_walls else 0.0,
                "p95_wall_seconds": _percentile(reader_walls, 0.95),
            },
        },
        "judge": {
            "controls_passed": controls_passed,
            "executed": run_judge,
            "correct_controls": {"passed": sum(item["label"] is True for item in correct_controls), "total": len(correct_controls)},
            "wrong_controls": {"passed": sum(item["label"] is False for item in wrong_controls), "total": len(wrong_controls)},
            "reader_answers_accepted": sum(item["label"] is True for item in reader_judgments),
            "reader_answers_total": len(reader_judgments),
        },
        "transport": transport_diagnostics,
        "cost": {
            "observed_wall_seconds": time.perf_counter() - started,
            "judge_aggregate_wall_seconds": sum(judge_walls),
        },
        "raw_answers_persisted_in_result": False,
    }


def _answer_matches(answer: str, atom_groups: Any) -> bool:
    _require(isinstance(atom_groups, list) and atom_groups, "机械答案原子合同无效")

    def normalize(value: str) -> str:
        # The frozen Reader may serialize the same field/value pair as prose,
        # JSON, or a labelled list. Structural punctuation is a word boundary;
        # value-bearing punctuation for times, ranges, percentages, and
        # hyphenated identifiers remains significant.
        value = value.casefold().replace("_", " ")
        value = re.sub(r"[^\w\s:%\-\u2013\u2014]", " ", value, flags=re.UNICODE)
        value = re.sub(r"\s*:\s*", " ", value)
        return re.sub(r"\s+", " ", value).strip()

    normalized = normalize(answer)
    return all(
        isinstance(group, list)
        and group
        and any(normalize(str(atom)) in normalized for atom in group)
        for group in atom_groups
    )


def _percentile(values: list[float], quantile: float) -> float:
    if not values:
        return 0.0
    ordered = sorted(values)
    return ordered[min(len(ordered) - 1, max(0, int((len(ordered) * quantile + 0.999999999) - 1)))]


def _replay_observer(run_root: Path, materials: dict[str, Any], execution: dict[str, Any]) -> dict[str, Any]:
    report = _load_json(run_root / "report.json")
    report["diagnostic_summary"] = _load_json(run_root / "diagnostic-summary.json")
    replay = validation.observe_report(report, materials)
    replay["case_evidence"] = validation.observe_case_evidence(run_root, materials)
    expected = execution["observation"]
    return {
        "exact": replay == expected,
        "replay_identity": evidence.canonical_sha256(replay),
        "recorded_identity": evidence.canonical_sha256(expected),
    }


def _validate_result(
    result: dict[str, Any],
    plan_identity: str,
    reproduction_result: dict[str, Any] | None = None,
) -> None:
    schema = result.get("schema")
    _require(schema in {LEGACY_RESULT_SCHEMA, RESULT_SCHEMA} and result.get("plan_identity") == plan_identity, "最终回答充分性结果错绑")
    content = {key: item for key, item in result.items() if key != "identity"}
    _require(result.get("identity") == evidence.canonical_sha256(content), "最终回答充分性结果身份漂移")
    _require(result.get("formal") is False and result.get("formal_state_written") is False and result.get("blind_gate_executed") is False, "最终回答充分性结果越权")
    phase = result.get("phase")
    _require(phase in {"reproduction", "final"}, "最终回答充分性结果阶段无效")
    if schema == LEGACY_RESULT_SCHEMA:
        _require(phase == "reproduction", "旧 v2 终态会混淆已证根因与修复反证，禁止复用")
        return
    root = _mapping(result, "root_cause")
    if phase == "reproduction":
        _require(result.get("root_cause_evidence") is None and result.get("repair_validation") is None, "根因复现不得伪装成修复验证")
        return
    _require(root.get("status") == "proven" and root.get("responsible_component") == "kernel-context", "终态没有保留已证原始根因")
    _require(reproduction_result is not None, "终态校验缺少原始根因复现证据")
    root_evidence = _mapping(result, "root_cause_evidence")
    _require(
        root_evidence.get("schema") == "ownward.kernel-iteration-stage3-root-cause-evidence/v1"
        and root_evidence.get("reproduction_result_identity") == result.get("reproduction_result_identity")
        and root_evidence.get("reproduction_result_identity") == reproduction_result.get("identity")
        and root_evidence.get("reproduction_plan_identity") == reproduction_result.get("plan_identity")
        and root_evidence.get("root_cause_identity") == evidence.canonical_sha256(root)
        and root == _mapping(reproduction_result, "root_cause")
        and isinstance(root_evidence.get("reproduction_plan_identity"), str)
        and len(str(root_evidence.get("reproduction_plan_identity"))) == 64,
        "终态根因没有绑定原始复现证据",
    )
    repair = _mapping(result, "repair_validation")
    candidate_classification = _mapping(repair, "candidate_classification")
    _require(
        repair.get("schema") == "ownward.kernel-iteration-stage3-repair-validation/v1"
        and repair.get("status") == "repaired-not-reproduced"
        and candidate_classification.get("status") == "not-reproduced-with-counterevidence"
        and candidate_classification.get("responsible_component") == "external-answer-boundary",
        "终态修复反证与已证根因没有分离",
    )
    executions = _mapping(result, "executions")
    repair_evidence = _mapping(repair, "evidence")
    for label in ("diagnosis", "confirmation", "regression"):
        execution = _mapping(executions, f"candidate-{label}")
        _require(
            execution.get("identity") == repair_evidence.get(f"{label}_execution_identity")
            and execution.get("passed") is True
            and repair_evidence.get(f"{label}_passed") is True,
            f"终态修复验证没有绑定通过的 {label} 证据",
        )
    _require(
        repair.get("candidate_subject_identity") == _mapping(result, "stage_reclosure").get("candidate_subject_identity")
        and all(repair_evidence.get(key) is True for key in (
            "diagnosis_judge_controls_passed", "confirmation_judge_controls_passed",
            "diagnosis_replay_exact", "confirmation_replay_exact", "regression_replay_exact",
        ))
        and all(int(repair_evidence.get(key, -1)) == 0 for key in (
            "diagnosis_reader_product_failures", "diagnosis_reader_oracle_failures",
            "confirmation_reader_product_failures", "confirmation_reader_oracle_failures",
        )),
        "终态修复验证的 Reader、Judge 或重放证据不完整",
    )
    stage = _mapping(result, "stage_reclosure")
    _require(
        stage.get("stage3") == "closed"
        and stage.get("stage4") == "requires-current-candidate-revalidation"
        and stage.get("stage5") == "requires-current-candidate-revalidation",
        "终态阶段关闭或后续重证边界无效",
    )
    regeneration = _mapping(result, "regeneration")
    _require(
        isinstance(regeneration.get("product_execution_performed"), bool)
        and isinstance(regeneration.get("model_turn_performed"), bool),
        "终态重生成活动证据无效",
    )
