from __future__ import annotations

from concurrent.futures import ThreadPoolExecutor
import json
from pathlib import Path
import time
from typing import Any

import kernel_iteration_evidence as evidence
import kernel_iteration_stage4_resource_cost_create_probe as create_probe
import kernel_iteration_validation as validation


CONTRACT_SCHEMA = "ownward.kernel-iteration-stage4-resource-cost-batch-feasibility-contract/v1"
RESULT_SCHEMA = "ownward.kernel-iteration-stage4-resource-cost-batch-feasibility-result/v1"
CONTRACT_PATH = Path("iteration/v2/stage4-resource-cost-batch-feasibility-contract.json")


def run(suite_root: Path, output_root: Path, formal_state: Path, *, resume: bool) -> dict[str, Any]:
    suite_root = suite_root.resolve()
    repository = suite_root.parents[2]
    output_root = output_root.resolve()
    _require(
        output_root.is_relative_to(repository / ".tmp" / "kernel-v2-major-iteration"),
        "批处理可行性必须位于非正式 V2 边界",
    )
    contract = load_contract(suite_root)
    formal_state = formal_state.resolve()
    _require(formal_state == repository / contract["formal_state"]["path"], "批处理可行性正式 state 路径错绑")
    state_before = evidence.file_sha256(formal_state)
    _require(state_before == contract["formal_state"]["sha256"], "批处理可行性前正式 state 漂移")
    result_path = output_root / "result.json"
    if result_path.is_file():
        _require(resume, "批处理可行性终态已存在；只有 --resume 可逐字复用")
        value = _load_json(result_path)
        _validate_identity(value, RESULT_SCHEMA, "批处理可行性终态")
        _require(value["contract_identity"] == contract["identity"], "批处理可行性终态合同错绑")
        _require(value["formal_state_sha256"] == state_before, "批处理可行性恢复时正式 state 漂移")
        return {**value, "path": str(result_path), "reused": True, "model_executions": 0, "product_executions": 0}

    _verify_source_evidence(repository, contract)
    output_root.mkdir(parents=True, exist_ok=True)
    observers = {
        "baseline": create_probe.build_observer(repository, output_root / "observers" / "baseline", contract),
        "batch": create_probe.build_observer(
            repository, output_root / "observers" / "batch", contract, batch_documents=True,
        ),
    }
    _require(observers["baseline"]["batch_documents"] is False, "基线观察器行为漂移")
    _require(observers["batch"]["batch_documents"] is True, "批处理观察器行为漂移")
    cases = create_probe.load_cases(repository, contract)
    module = validation._load_longmemeval_module(suite_root)
    rounds: list[dict[str, Any]] = []
    started = time.perf_counter()
    for round_number, variant_order in enumerate(contract["measurement"]["balanced_variant_order"], start=1):
        variants: dict[str, Any] = {}
        question_order = contract["measurement"]["balanced_question_order"][round_number - 1]
        for variant in variant_order:
            variants[variant] = _run_variant(
                module=module,
                output_root=output_root / "runs" / f"round-{round_number}" / variant,
                observer=observers[variant],
                cases=cases,
                question_order=question_order,
                repeat=round_number,
                contract=contract,
            )
        rounds.append({"round": round_number, "variant_order": variant_order, "variants": variants})
    elapsed = time.perf_counter() - started
    _require(elapsed <= float(contract["measurement"]["maximum_wall_seconds"]), "批处理可行性超过冻结墙钟")
    result = evaluate(contract, observers, rounds, elapsed, state_before)
    _require(evidence.file_sha256(formal_state) == state_before, "批处理可行性改写了正式 state")
    evidence.atomic_json(result_path, result)
    return {**result, "path": str(result_path), "reused": False, "model_executions": 0, "product_executions": 12}


def load_contract(suite_root: Path) -> dict[str, Any]:
    repository = suite_root.parents[2]
    value = _load_json(suite_root / CONTRACT_PATH)
    _validate_identity(value, CONTRACT_SCHEMA, "批处理可行性合同")
    _require(value.get("frozen_before_results") is True and value.get("results_seen") is False, "批处理合同没有在结果前冻结")
    for item in value["direct_dependencies"]:
        _verify_file(repository / item["path"], item, "批处理直接依赖")
    return value


def _verify_source_evidence(repository: Path, contract: dict[str, Any]) -> None:
    for name in ("local_wall_component_migration", "create_subphase_probe"):
        item = contract["sources"][name]
        _verify_file(repository / item["path"], item, name)
        value = _load_json(repository / item["path"])
        _require(value.get("identity") == item["identity"], f"{name} 身份错绑")


def _run_variant(
    *,
    module: Any,
    output_root: Path,
    observer: dict[str, Any],
    cases: dict[str, dict[str, Any]],
    question_order: list[str],
    repeat: int,
    contract: dict[str, Any],
) -> dict[str, Any]:
    started = time.perf_counter()
    with ThreadPoolExecutor(max_workers=len(question_order), thread_name_prefix="create-batch-feasibility") as pool:
        futures = {
            case_id: pool.submit(
                create_probe.run_question,
                module,
                output_root,
                observer,
                cases[case_id],
                repeat,
                contract,
            )
            for case_id in question_order
        }
        questions = [futures[case_id].result() for case_id in question_order]
    return _summarize_variant(questions, time.perf_counter() - started)


def _summarize_variant(questions: list[dict[str, Any]], concurrent_wall_seconds: float) -> dict[str, Any]:
    embedding_seconds = 0.0
    ensure_seconds = 0.0
    embedding_calls = 0
    embedding_inputs = 0
    by_question: dict[str, Any] = {}
    for question in questions:
        vectors: list[str] = []
        question_calls = 0
        question_inputs = 0
        for event in question["events"]:
            seconds = float(event["duration_ns"]) / 1_000_000_000
            if event["phase"] == "embedding.documents":
                embedding_seconds += seconds
                embedding_calls += 1
                question_calls += 1
                count = int(event["input_count"])
                embedding_inputs += count
                question_inputs += count
                vectors.extend(str(value) for value in event["vector_identities"])
            elif event["phase"] == "embedding.ensure_running":
                ensure_seconds += seconds
        by_question[question["case_id"]] = {
            "assets": question["assets"],
            "create_call_seconds": question["create_call_seconds"],
            "envelope_seconds": question["envelope_seconds"],
            "embedding_calls": question_calls,
            "embedding_inputs": question_inputs,
            "vector_identities": vectors,
            "trace_sha256": question["trace_sha256"],
        }
    return {
        "concurrent_wall_seconds": concurrent_wall_seconds,
        "create_call_sum_seconds": sum(item["create_call_seconds"] for item in questions),
        "create_call_critical_path_seconds": max(item["create_call_seconds"] for item in questions),
        "envelope_sum_seconds": sum(item["envelope_seconds"] for item in questions),
        "embedding_seconds": embedding_seconds,
        "embedding_ensure_seconds": ensure_seconds,
        "embedding_calls": embedding_calls,
        "embedding_inputs": embedding_inputs,
        "questions": by_question,
    }


def evaluate(
    contract: dict[str, Any],
    observers: dict[str, dict[str, Any]],
    rounds: list[dict[str, Any]],
    elapsed: float,
    state_sha256: str,
) -> dict[str, Any]:
    paired_improvements: list[float] = []
    vector_equivalence = True
    input_order_equivalence = True
    call_reductions: list[int] = []
    for item in rounds:
        baseline = item["variants"]["baseline"]
        batch = item["variants"]["batch"]
        paired_improvements.append(baseline["envelope_sum_seconds"] - batch["envelope_sum_seconds"])
        call_reductions.append(baseline["embedding_calls"] - batch["embedding_calls"])
        for case_id in baseline["questions"]:
            left = baseline["questions"][case_id]
            right = batch["questions"][case_id]
            vector_equivalence = vector_equivalence and left["vector_identities"] == right["vector_identities"]
            input_order_equivalence = input_order_equivalence and (
                left["embedding_inputs"] == right["embedding_inputs"] == left["assets"] - (1 if case_id == "v2d-long-multifact" else 0)
            )
    gate = contract["route_authorization"]
    conservative_improvement = min(paired_improvements)
    authorized = (
        vector_equivalence
        and input_order_equivalence
        and min(call_reductions) > 0
        and conservative_improvement >= float(gate["minimum_conservative_route_improvement_seconds"])
    )
    content = {
        "schema": RESULT_SCHEMA,
        "formal": False,
        "formal_state_written": False,
        "contract_identity": contract["identity"],
        "source_subject_identity": contract["source_candidate"]["subject_identity"],
        "observers": {name: value["identity"] for name, value in observers.items()},
        "measurement": {
            "rounds": rounds,
            "paired_envelope_improvement_seconds": paired_improvements,
            "conservative_improvement_seconds": conservative_improvement,
            "embedding_call_reductions": call_reductions,
            "vector_identity_exact": vector_equivalence,
            "input_count_and_order_exact": input_order_equivalence,
            "elapsed_seconds": elapsed,
        },
        "decision": {
            "route_authorized": authorized,
            "required_conservative_improvement_seconds": gate["minimum_conservative_route_improvement_seconds"],
            "next_validation": (
                "integrate-per-item-bounded-document-batching-and-rerun-only-directly-invalidated-evidence"
                if authorized
                else "reject-document-batching-and-retain-the-first-unmet-candidate-controlled-wall-root"
            ),
        },
        "model_executions": 0,
        "reader_executions": 0,
        "judge_executions": 0,
        "formal_state_sha256": state_sha256,
    }
    return {**content, "identity": evidence.canonical_sha256(content)}


def _verify_file(path: Path, item: dict[str, Any], name: str) -> None:
    _require(path.is_file() and evidence.file_sha256(path) == item["sha256"], f"{name}文件漂移: {path}")


def _validate_identity(value: dict[str, Any], schema: str, name: str) -> None:
    _require(value.get("schema") == schema, f"{name} schema 无效")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), f"{name}身份漂移")


def _load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise validation.KernelIterationValidationError(f"无法读取批处理证据 {path}: {error}") from error
    _require(isinstance(value, dict), f"批处理证据不是对象: {path}")
    return value


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise validation.KernelIterationValidationError(message)
