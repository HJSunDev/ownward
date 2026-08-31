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
import kernel_iteration_validation as validation


CONTRACT_SCHEMA = "ownward.kernel-iteration-stage3-answer-sufficiency-contract/v1"
PLAN_SCHEMA = "ownward.kernel-iteration-stage3-answer-sufficiency-plan/v1"
RESULT_SCHEMA = "ownward.kernel-iteration-stage3-answer-sufficiency-result/v1"
CONTRACT_RELATIVE = Path("iteration/v2/stage3-answer-sufficiency-contract.json")


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
    _require(trigger.get("first_observed_gaps") == {"evidence_read_answer_incorrect": 1, "none": 24}, "25 题聚合首缺口漂移")
    sources = _mapping(value, "sources")
    loaded: dict[str, dict[str, Any]] = {}
    for name in ("diagnosis_materials", "regression_materials"):
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
    regression = loaded["regression_materials"]
    diagnosis_facts = {validation._case_fact_identity(case) for case in diagnosis["cases"]}
    regression_facts = {validation._case_fact_identity(case) for case in regression["cases"]}
    _require(diagnosis_facts.isdisjoint(regression_facts), "诊断与正确能力回归事实重合")
    coverage = _mapping(value, "coverage")
    _require(set(coverage.get("diagnosis_required", [])) == {case["coverage"] for case in diagnosis["cases"]}, "诊断覆盖漂移")
    _require(set(coverage.get("regression_required", [])) == {case["coverage"] for case in regression["cases"]}, "回归覆盖漂移")
    repetitions = _mapping(value, "repetitions")
    _require(repetitions == {
        "product_context_total_per_case_including_original": 3,
        "oracle_context_per_case": 2,
        "identical_prompt_required": True,
        "model_and_effort_unchanged": True,
    }, "Reader 重复合同漂移")
    gates = _mapping(value, "gates")
    _require(gates.get("maximum_total_wall_seconds") == 600 and gates.get("formal_state_writes") == 0, "诊断成本或正式状态边界漂移")
    return {**value, "loaded": loaded}


def run(
    suite_root: Path,
    output_root: Path,
    candidate_execution_config: Path,
    baseline_execution_config: Path,
    candidate_subject_manifest: Path,
    formal_state: Path,
    *,
    resume: bool = False,
    execute: Callable[..., dict[str, Any]] | None = None,
    codex_diagnose: Callable[..., dict[str, Any]] | None = None,
) -> dict[str, Any]:
    suite_root = suite_root.resolve()
    output_root = output_root.resolve()
    repository = suite_root.parents[2]
    evidence._validate_output_boundary(repository, output_root)
    contract = load_contract(suite_root)
    state_path = formal_state.resolve()
    _require(state_path.is_file(), "最终回答充分性诊断缺少正式 state 只读基线")
    state_before = state_path.read_bytes()
    candidate_runtime = validation.validate_execution_config(suite_root, candidate_execution_config.resolve())
    baseline_runtime = validation.validate_execution_config(suite_root, baseline_execution_config.resolve())
    subject = _load_json(candidate_subject_manifest.resolve())
    _require(subject.get("identity") == _mapping(contract, "subject").get("identity"), "最终回答充分性诊断候选错绑")
    dependencies = {
        "contract": contract["identity"],
        "controller": evidence.file_sha256(Path(__file__).resolve()),
        "controller-entry": evidence.file_sha256(suite_root / "kernel_iteration_run.py"),
        "candidate-config": evidence.file_sha256(candidate_execution_config.resolve()),
        "baseline-config": evidence.file_sha256(baseline_execution_config.resolve()),
        "candidate-subject": subject["identity"],
        "formal-state": evidence.file_sha256(state_path),
        "validation-contract": validation.load_validation_contract(suite_root)["identity"],
    }
    content = {
        "schema": PLAN_SCHEMA,
        "purpose": "independent-answer-sufficiency-root-diagnosis",
        "contract_identity": contract["identity"],
        "subject_identity": subject["identity"],
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
        _validate_result(result, plan["identity"])
        _require(state_path.read_bytes() == state_before, "最终回答充分性终态复用改写正式 state")
        return {**result, "result_path": str(result_path), "reused": True, "model_or_product_execution": False}
    if plan_path.is_file():
        _require(resume and _load_json(plan_path) == plan, "最终回答充分性计划身份漂移")
    else:
        evidence.atomic_json(plan_path, plan)

    started = time.perf_counter()
    input_root = root / "inputs"
    executions: dict[str, dict[str, Any]] = {}
    execution_callback = execute or validation.execute_prepared_evidence
    for material_name, evidence_type in (("diagnosis_materials", "development"), ("regression_materials", "regression")):
        materials = contract["loaded"][material_name]
        material_path = input_root / ("diagnosis.json" if evidence_type == "development" else "regression.json")
        input_path = input_root / ("diagnosis-input.json" if evidence_type == "development" else "regression-input.json")
        evidence.atomic_json(material_path, materials)
        validation.build_input_manifest(suite_root, material_path, candidate_execution_config.resolve(), evidence_type, input_path)
        candidate = execution_callback(
            suite_root, output_root / "answer-sufficiency-executions", candidate_execution_config.resolve(),
            subject_manifest=candidate_subject_manifest.resolve(), evidence_type=evidence_type,
            input_manifest=input_path, resume=resume,
        )
        candidate_result = Path(str(candidate["execution_result"])).resolve()
        executions[f"candidate-{evidence_type}"] = _load_json(candidate_result)
        if executions[f"candidate-{evidence_type}"]["passed"]:
            baseline = execution_callback(
                suite_root, output_root / "answer-sufficiency-executions", baseline_execution_config.resolve(),
                selector="v0", evidence_type=evidence_type, input_manifest=input_path,
                candidate_result_path=candidate_result, resume=resume,
            )
            executions[f"v0-{evidence_type}"] = _load_json(Path(str(baseline["execution_result"])).resolve())

    diagnosis_execution = executions["candidate-development"]
    diagnosis_run_root = candidate_runtime["runs"] / "kernel-iteration" / diagnosis_execution["plan_identity"] / "run"
    _require(diagnosis_run_root.is_dir(), "最终回答充分性诊断缺少候选原始执行轨迹")
    codex_result = (codex_diagnose or _diagnose_codex_boundaries)(
        suite_root, output_root / "answer-sufficiency-codex", candidate_runtime,
        contract["loaded"]["diagnosis_materials"], diagnosis_run_root,
    )
    observer_replay = _replay_observer(diagnosis_run_root, contract["loaded"]["diagnosis_materials"], diagnosis_execution)
    classification = classify_root(contract, diagnosis_execution, executions.get("candidate-regression"), codex_result, observer_replay)
    elapsed = time.perf_counter() - started
    gates = _mapping(contract, "gates")
    stage3_closed = classification["status"] in {"proven", "not-reproduced-with-counterevidence"}
    stage4_unchanged = classification["responsible_component"] != "kernel-context"
    # This diagnostic may identify a failure boundary without changing it.  A
    # frozen Reader/Judge/executor identity therefore does not invalidate the
    # already sealed Stage 5 evidence.  Only a kernel-context finding changes
    # the candidate dependency that Stage 5 actually consumed.
    stage5_unchanged = classification["responsible_component"] != "kernel-context"
    result_content = {
        "schema": RESULT_SCHEMA,
        "plan_identity": plan["identity"],
        "contract_identity": contract["identity"],
        "status": "passed" if stage3_closed and elapsed <= float(gates["maximum_total_wall_seconds"]) else "failed",
        "passed": stage3_closed and elapsed <= float(gates["maximum_total_wall_seconds"]),
        "formal": False,
        "formal_state_written": False,
        "blind_gate_executed": False,
        "candidate_decision": None,
        "root_cause": classification,
        "executions": {
            name: {"identity": item["identity"], "passed": item["passed"], "subject_identity": item["subject_identity"]}
            for name, item in sorted(executions.items())
        },
        "codex_boundary_diagnosis": codex_result,
        "observer_replay": observer_replay,
        "stage_reclosure": {
            "stage3": "closed" if stage3_closed else "open",
            "stage4": "unchanged-and-valid" if stage4_unchanged else "reopen",
            "stage5": "unchanged-and-valid" if stage5_unchanged else "reopen",
            "candidate_subject_identity": subject["identity"],
            "kernel_generation_identity": subject["kernel_generation_identity"],
            "kernel_effect_identity": subject["kernel_effect_identity"],
        },
        "cost": {"wall_seconds": elapsed, "maximum_wall_seconds": float(gates["maximum_total_wall_seconds"])},
        "formal_state_sha256": evidence.file_sha256(state_path),
        "next_action": "restart-stage6-from-fresh-five-question-gate" if stage3_closed and stage4_unchanged and stage5_unchanged else "repair-proven-responsible-component-and-reclose-affected-stages",
    }
    result = {**result_content, "identity": evidence.canonical_sha256(result_content)}
    evidence.atomic_json(result_path, result)
    _require(state_path.read_bytes() == state_before, "最终回答充分性诊断改写正式 state")
    _validate_result(result, plan["identity"])
    return {**result, "result_path": str(result_path), "reused": False, "model_or_product_execution": True}


def classify_root(
    contract: dict[str, Any],
    diagnosis_execution: dict[str, Any],
    regression_execution: dict[str, Any] | None,
    codex_result: dict[str, Any],
    observer_replay: dict[str, Any],
) -> dict[str, Any]:
    cases = diagnosis_execution["observation"].get("case_evidence", [])
    missing_claims = sum(int(item["truth_claims"]) - int(item["delivered_truth_claims"]) for item in cases)
    diagnosis_accuracy = float(diagnosis_execution["observation"]["final_answer_accuracy"])
    regression_accuracy = float(regression_execution["observation"]["final_answer_accuracy"]) if regression_execution else 0.0
    product_failures = int(codex_result["reader"]["product_context_failures"])
    oracle_failures = int(codex_result["reader"]["oracle_context_failures"])
    product_variations = int(codex_result["reader"]["product_context_variations"])
    oracle_variations = int(codex_result["reader"]["oracle_context_variations"])
    judge_failed = not bool(codex_result["judge"]["controls_passed"])
    if missing_claims > 0 or (product_failures > 0 and oracle_failures == 0):
        component = "kernel-context"
        status = "proven"
        mechanism = "required-truth-not-stably-usable-in-product-context"
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
            missing_claims == 0
            and diagnosis_accuracy >= float(gates["product_execution_accuracy"])
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
    command_prefix = module.CodexAppServer.direct_command_prefix(
        runtime["codex_binary"], module.codex_session.command_prefix(runtime["codex_binary"]),
    )

    def factory(_index: int, _generation: int) -> Any:
        runtime_root = module.isolated_runtime_root(stage_root / ".codex-runtime")
        environment = module.codex_session.isolated_environment(runtime["codex_auth_file"], runtime_root / "codex-home")
        return module.CodexAppServer(runtime["codex_binary"], runtime["codex_auth_file"], runtime_root, command_prefix, environment)

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
    with _attribution_context(renderer_context, "official-prompt-renderer", "renderer-lifecycle") as evaluator_renderer, \
            _attribution_context(module.CodexAppServerPool(4, factory), "transport", "transport-lifecycle") as transport:
        capability = module.CodexCapability(transport)

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

        def run_judge(job: dict[str, Any]) -> dict[str, Any]:
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
                futures = [executor.submit(run_judge, job) for job in judge_jobs]
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
    normalized = re.sub(r"\s+", " ", answer.casefold()).strip()
    return all(isinstance(group, list) and group and any(str(atom).casefold() in normalized for atom in group) for group in atom_groups)


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


def _validate_result(result: dict[str, Any], plan_identity: str) -> None:
    _require(result.get("schema") == RESULT_SCHEMA and result.get("plan_identity") == plan_identity, "最终回答充分性结果错绑")
    content = {key: item for key, item in result.items() if key != "identity"}
    _require(result.get("identity") == evidence.canonical_sha256(content), "最终回答充分性结果身份漂移")
    _require(result.get("formal") is False and result.get("formal_state_written") is False and result.get("blind_gate_executed") is False, "最终回答充分性结果越权")
