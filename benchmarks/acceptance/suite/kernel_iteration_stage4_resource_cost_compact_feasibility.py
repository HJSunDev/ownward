from __future__ import annotations

from concurrent.futures import ThreadPoolExecutor, as_completed
import hashlib
import json
from pathlib import Path
import statistics
import sys
import time
from typing import Any

import kernel_iteration_evidence as evidence
import kernel_iteration_stage4_resource_cost_matched_calibration as matched
import kernel_iteration_validation as validation


LONGMEM_ROOT = Path(__file__).resolve().parents[2] / "longmemeval_s"
if str(LONGMEM_ROOT) not in sys.path:
    sys.path.insert(0, str(LONGMEM_ROOT))
import semantic_representation  # noqa: E402


CONTRACT_SCHEMA = "ownward.kernel-iteration-stage4-resource-cost-compact-feasibility-contract/v1"
RESULT_SCHEMA = "ownward.kernel-iteration-stage4-resource-cost-compact-feasibility/v1"
CONTRACT_PATH = Path("iteration/v2/stage4-resource-cost-compact-feasibility-contract.json")
BALANCED_CONTRACT_SCHEMA = "ownward.kernel-iteration-stage4-resource-cost-compact-balanced-contract/v2"
BALANCED_RESULT_SCHEMA = "ownward.kernel-iteration-stage4-resource-cost-compact-balanced/v2"
BALANCED_CONTRACT_PATH = Path("iteration/v2/stage4-resource-cost-compact-balanced-contract.json")


def compact_instruction() -> str:
    return semantic_representation.compact_instruction()


def compact_semantic_input(original: dict[str, Any]) -> dict[str, Any]:
    return semantic_representation.compact_semantic_input(original)


def validate_equivalence(original: dict[str, Any], compact: dict[str, Any]) -> None:
    semantic_representation.validate_compact_equivalence(original, compact)


def validate_for_semantic_submission(
    module: Any,
    original: dict[str, Any],
    value: dict[str, Any],
    call: dict[str, Any],
    settings: dict[str, Any],
    validation_root: Path,
) -> str:
    analyses = value.get("analyses")
    _require(
        isinstance(analyses, list)
        and [item.get("work_id") for item in analyses if isinstance(item, dict)] == call["work_ids"],
        "紧凑协议输出遗漏或重排 work_id",
    )
    by_ref = {item["body_ref"]: item for item in original["bodies"]}
    work = []
    for item in original["work"]:
        asset_ref = item["asset"]["body_ref"]
        body = by_ref[asset_ref]
        work.append({
            "id": item["work_id"],
            "asset": {"id": body["id"], "revision": body["revision"]},
        })
    output_identity = evidence.canonical_sha256(value)
    unit_identity = evidence.canonical_sha256({"work_ids": call["work_ids"], "output": output_identity})
    batch_id = f"balanced-{output_identity[:20]}"
    frozen = {
        "question_identity": call["question_id"],
        "batch_index": 0,
        "batch_id": batch_id,
        "work_sha256": evidence.canonical_sha256(work),
        "work": work,
    }
    unit = {"unit_index": 0, "identity": unit_identity, "batch_indexes": [0], "work_ids": call["work_ids"]}
    unit_result = {
        "identity": unit_identity,
        "work_ids": call["work_ids"],
        "analyses": analyses,
        "usage": module._empty_usage(),
    }
    combined = module.combine_semantic_batch(
        frozen,
        [unit],
        {0: unit_result},
        validation_root / output_identity,
        settings,
    )
    submissions = combined.get("submissions")
    _require(isinstance(submissions, list) and len(submissions) == len(work), "紧凑协议提交前校验遗漏工作")
    _require(
        [item.get("work_id") for item in submissions if isinstance(item, dict)] == call["work_ids"],
        "紧凑协议提交前校验重排工作",
    )
    _require(
        all(
            item.get("schema") == "ownward.semantic-submission/v1"
            and item.get("asset_id") == source["asset"]["id"]
            and item.get("asset_revision") == source["asset"]["revision"]
            and item.get("status") == "complete"
            and item.get("capability") == {
                "id": "codex",
                "version": settings["semantic_model"],
                "execution": "longmemeval-s",
            }
            for item, source in zip(submissions, work)
        ),
        "紧凑协议提交绑定未通过现有完整性校验",
    )
    return evidence.canonical_sha256(submissions)


def run_balanced(
    suite_root: Path,
    output_root: Path,
    execution_config: Path,
    formal_state: Path,
    *,
    resume: bool,
) -> dict[str, Any]:
    suite_root = suite_root.resolve()
    repository = suite_root.parents[2]
    output_root = output_root.resolve()
    contract = load_balanced_contract(suite_root)
    _require(output_root.is_relative_to(repository / ".tmp" / "kernel-v2-major-iteration"), "紧凑协议平衡复核必须位于非正式 V2 边界")
    execution_config = execution_config.resolve()
    formal_state = formal_state.resolve()
    _verify_file(execution_config, contract["execution_config"])
    _require(formal_state == repository / contract["formal_state"]["path"], "紧凑协议平衡复核正式 state 路径错绑")
    state_before = evidence.file_sha256(formal_state)
    _require(state_before == contract["formal_state"]["sha256"], "紧凑协议平衡复核前正式 state 漂移")
    result_path = output_root / "balanced-result.json"
    if result_path.is_file():
        _require(resume, "紧凑协议平衡复核终态已存在；只有 --resume 可逐字复用")
        value = _load_json(result_path)
        _validate_identity(value, BALANCED_RESULT_SCHEMA, "紧凑协议平衡复核终态")
        _require(value["contract_identity"] == contract["identity"], "紧凑协议平衡复核终态合同错绑")
        _require(value["formal_state_sha256"] == state_before, "紧凑协议平衡复核恢复时正式 state 漂移")
        return {**value, "path": str(result_path), "reused": True, "model_executions": 0, "product_executions": 0}

    runtime = validation.validate_execution_config(suite_root, execution_config)
    protocol = _load_json(runtime["protocol"])
    matched_contract = matched.load_contract(suite_root)
    source_calls = [item for item in matched.discover_source_calls(runtime["runs"], matched_contract) if item["subject"] == "v2"]
    _require(len(source_calls) == int(contract["execution"]["source_calls"]), "紧凑协议平衡复核来源调用数漂移")
    output_root.mkdir(parents=True, exist_ok=True)
    source_before = matched.discover_source_calls(runtime["runs"], matched_contract)
    started = time.perf_counter()
    execution = execute_balanced_requests(
        suite_root=suite_root,
        output_root=output_root,
        runtime=runtime,
        protocol=protocol,
        calls=source_calls,
        contract=contract,
    )
    elapsed = time.perf_counter() - started
    _require(elapsed <= float(contract["execution"]["maximum_wall_seconds"]), "紧凑协议平衡复核超过冻结墙钟")
    _require(matched.discover_source_calls(runtime["runs"], matched_contract) == source_before, "紧凑协议平衡复核改写既有请求收据")
    decision = evaluate_balanced(execution, contract)
    state_after = evidence.file_sha256(formal_state)
    _require(state_after == state_before, "紧凑协议平衡复核改写正式 state")
    content = {
        "schema": BALANCED_RESULT_SCHEMA,
        "formal": False,
        "formal_state_written": False,
        "contract_identity": contract["identity"],
        "model_profile": contract["model_profile"],
        "request_count": len(execution["requests"]),
        "transport": execution["transport"],
        "token": decision["token"],
        "wall": decision["wall"],
        "integrity": decision["integrity"],
        "implementation_authorized": decision["implementation_authorized"],
        "stage4_complete": False,
        "next_validation": decision["next_validation"],
        "calibration_elapsed_seconds": elapsed,
        "formal_state_sha256": state_after,
    }
    value = {**content, "identity": evidence.canonical_sha256(content)}
    evidence.atomic_json(result_path, value)
    return {
        **value,
        "path": str(result_path),
        "reused": False,
        "model_executions": execution["transport"]["actual_model_executions"],
        "product_executions": 0,
    }


def run(
    suite_root: Path,
    output_root: Path,
    execution_config: Path,
    formal_state: Path,
    *,
    resume: bool,
) -> dict[str, Any]:
    suite_root = suite_root.resolve()
    repository = suite_root.parents[2]
    output_root = output_root.resolve()
    contract = load_contract(suite_root)
    _require(output_root.is_relative_to(repository / ".tmp" / "kernel-v2-major-iteration"), "紧凑协议可行性必须位于非正式 V2 边界")
    execution_config = execution_config.resolve()
    formal_state = formal_state.resolve()
    _verify_file(execution_config, contract["execution_config"])
    _require(formal_state == repository / contract["formal_state"]["path"], "紧凑协议正式 state 路径错绑")
    state_before = evidence.file_sha256(formal_state)
    _require(state_before == contract["formal_state"]["sha256"], "紧凑协议前正式 state 漂移")
    result_path = output_root / "feasibility.json"
    if result_path.is_file():
        _require(resume, "紧凑协议可行性终态已存在；只有 --resume 可复用")
        value = _load_json(result_path)
        _validate_identity(value, RESULT_SCHEMA, "紧凑协议可行性终态")
        _require(value["contract_identity"] == contract["identity"], "紧凑协议可行性合同错绑")
        _require(value["formal_state_sha256"] == state_before, "紧凑协议恢复时正式 state 漂移")
        return {**value, "path": str(result_path), "reused": True, "model_executions": 0, "product_executions": 0}
    runtime = validation.validate_execution_config(suite_root, execution_config)
    protocol = _load_json(runtime["protocol"])
    matched_contract = matched.load_contract(suite_root)
    source_calls = [item for item in matched.discover_source_calls(runtime["runs"], matched_contract) if item["subject"] == "v2"]
    matched_result = _load_json(repository / contract["matched_calibration"]["path"])
    _validate_identity(matched_result, matched.RESULT_SCHEMA, "匹配差分来源终态")
    _require(matched_result["identity"] == contract["matched_calibration"]["identity"], "匹配差分来源身份漂移")
    output_root.mkdir(parents=True, exist_ok=True)
    started = time.perf_counter()
    requests, transport = execute_requests(
        suite_root=suite_root,
        output_root=output_root,
        runtime=runtime,
        protocol=protocol,
        calls=source_calls,
        contract=contract,
    )
    elapsed = time.perf_counter() - started
    _require(elapsed <= float(contract["execution"]["maximum_wall_seconds"]), "紧凑协议可行性超过冻结墙钟")
    evaluation = evaluate(source_calls, requests, matched_result, contract)
    state_after = evidence.file_sha256(formal_state)
    _require(state_after == state_before, "紧凑协议可行性改写正式 state")
    content = {
        "schema": RESULT_SCHEMA,
        "formal": False,
        "formal_state_written": False,
        "contract_identity": contract["identity"],
        "request_count": len(requests),
        "calibration_elapsed_seconds": elapsed,
        "transport": transport,
        "token": evaluation["token"],
        "wall": evaluation["wall"],
        "implementation_authorized": evaluation["implementation_authorized"],
        "next_validation": evaluation["next_validation"],
        "formal_state_sha256": state_after,
    }
    value = {**content, "identity": evidence.canonical_sha256(content)}
    evidence.atomic_json(result_path, value)
    return {**value, "path": str(result_path), "reused": False, "model_executions": len(requests), "product_executions": 0}


def execute_requests(
    *,
    suite_root: Path,
    output_root: Path,
    runtime: dict[str, Any],
    protocol: dict[str, Any],
    calls: list[dict[str, Any]],
    contract: dict[str, Any],
) -> tuple[list[dict[str, Any]], dict[str, Any]]:
    module = validation._load_longmemeval_module(suite_root)
    instruction = compact_instruction()
    _require(evidence.canonical_sha256(instruction) == contract["protocol"]["instruction_identity"], "紧凑协议指令漂移")
    command_prefix = module.CodexAppServer.direct_command_prefix(runtime["codex_binary"], module.codex_session.command_prefix(runtime["codex_binary"]))
    transport_parent = output_root / ".codex-runtime"

    def factory(_index: int, _generation: int) -> Any:
        runtime_root = module.isolated_runtime_root(transport_parent)
        environment = module.codex_session.isolated_environment(runtime["codex_auth_file"], runtime_root / "codex-home")
        return module.CodexAppServer(runtime["codex_binary"], runtime["codex_auth_file"], runtime_root, command_prefix, environment)

    def invoke(call: dict[str, Any], transport: Any) -> dict[str, Any]:
        input_path = runtime["runs"] / "kernel-iteration" / contract["source_plans"][call["material"]]["plan_identity"] / "run" / call["source_input"]
        original = _load_json(input_path)["representation"]
        compact = compact_semantic_input(original)
        prompt = instruction + json.dumps(compact, ensure_ascii=False, separators=(",", ":"))
        stage = output_root / "requests" / call["material"] / call["question_id"] / call["schema_identity"]

        def validate(value: dict[str, Any]) -> None:
            analyses = value.get("analyses")
            _require(isinstance(analyses, list) and [item.get("work_id") for item in analyses if isinstance(item, dict)] == call["work_ids"], "紧凑协议输出遗漏或重排 work_id")

        capability = module.CodexCapability(transport)
        _value, usage = capability._invoke(
            prompt=prompt,
            schema=call["schema"],
            stage=stage,
            model=contract["model_profile"]["model"],
            effort=contract["model_profile"]["reasoning_effort"],
            timeout_seconds=float(protocol["memory"]["semantic_timeout_seconds"]),
            attempts=int(protocol["memory"]["semantic_attempts"]),
            validate=validate,
        )
        _require(int(usage["cached_input_tokens"]) == 0, "紧凑协议可行性出现缓存输入")
        return {
            "material": call["material"],
            "question_id": call["question_id"],
            "schema_identity": call["schema_identity"],
            "work_count": len(call["work_ids"]),
            "prompt_sha256": evidence.canonical_sha256(prompt),
            "compact_input_identity": evidence.canonical_sha256(compact),
            "input_tokens": int(usage["input_tokens"]),
            "wall_seconds": float(usage["wall_seconds"]),
            "attempts": int(usage["attempts"]),
            "retries": int(usage["retries"]),
        }

    requests = []
    with module.CodexAppServerPool(int(contract["execution"]["app_server_pool_size"]), factory) as transport:
        with ThreadPoolExecutor(max_workers=int(contract["execution"]["max_concurrent_turns"])) as pool:
            futures = [pool.submit(invoke, call, transport) for call in calls]
            for future in as_completed(futures):
                requests.append(future.result())
        diagnostics = transport.diagnostics()
    if transport_parent.is_dir() and not any(transport_parent.iterdir()):
        transport_parent.rmdir()
    _require(len(requests) == len(calls), "紧凑协议可行性请求不完整")
    _require(diagnostics["worker_restarts"] == 0 and diagnostics["rate_limit_observed"] is False, "紧凑协议可行性传输不稳定")
    return sorted(requests, key=lambda item: (item["material"], item["question_id"])), diagnostics


def execute_balanced_requests(
    *,
    suite_root: Path,
    output_root: Path,
    runtime: dict[str, Any],
    protocol: dict[str, Any],
    calls: list[dict[str, Any]],
    contract: dict[str, Any],
) -> dict[str, Any]:
    module = validation._load_longmemeval_module(suite_root)
    current_instruction = module.CodexCapability.semantic_instruction()
    compact = compact_instruction()
    _require(evidence.canonical_sha256(current_instruction) == contract["protocol"]["current_instruction_identity"], "平衡复核 current 指令漂移")
    _require(evidence.canonical_sha256(compact) == contract["protocol"]["compact_instruction_identity"], "平衡复核 compact 指令漂移")
    settings = protocol["memory"]
    command_prefix = module.CodexAppServer.direct_command_prefix(runtime["codex_binary"], module.codex_session.command_prefix(runtime["codex_binary"]))
    transport_parent = output_root / ".codex-runtime"

    def factory(_index: int, _generation: int) -> Any:
        runtime_root = module.isolated_runtime_root(transport_parent)
        environment = module.codex_session.isolated_environment(runtime["codex_auth_file"], runtime_root / "codex-home")
        return module.CodexAppServer(runtime["codex_binary"], runtime["codex_auth_file"], runtime_root, command_prefix, environment)

    def invoke(call: dict[str, Any], variant: str, repeat: int, transport: Any) -> dict[str, Any]:
        input_path = runtime["runs"] / "kernel-iteration" / contract["source_plans"][call["material"]]["plan_identity"] / "run" / call["source_input"]
        source = _load_json(input_path)
        original = source["representation"]
        compact_value = compact_semantic_input(original)
        if variant == "current":
            prompt = current_instruction + json.dumps(original, ensure_ascii=False, separators=(",", ":"))
            _require(hashlib.sha256(prompt.encode("utf-8")).hexdigest() == source["prompt_sha256"], "平衡复核 current 请求不等于冻结请求")
            representation_identity = evidence.canonical_sha256(original)
        else:
            prompt = compact + json.dumps(compact_value, ensure_ascii=False, separators=(",", ":"))
            representation_identity = evidence.canonical_sha256(compact_value)
        stage = output_root / "requests" / f"repeat-{repeat:02d}" / variant / call["material"] / call["question_id"] / call["schema_identity"]

        def validate(value: dict[str, Any]) -> None:
            validate_for_semantic_submission(
                module,
                original,
                value,
                call,
                settings,
                stage / "_submission-validation",
            )

        capability = module.CodexCapability(transport)
        value, usage, measurement_wall, cached_attempts = _invoke_uncached(
            capability=capability,
            prompt=prompt,
            schema=call["schema"],
            stage=stage,
            model=contract["model_profile"]["model"],
            effort=contract["model_profile"]["reasoning_effort"],
            timeout_seconds=float(settings["semantic_timeout_seconds"]),
            attempts=int(contract["execution"]["bounded_attempts"]),
            validate=validate,
        )
        submission_identity = validate_for_semantic_submission(
            module,
            original,
            value,
            call,
            settings,
            stage / "_submission-validation",
        )
        return {
            "repeat": repeat,
            "variant": variant,
            "material": call["material"],
            "question_id": call["question_id"],
            "schema_identity": call["schema_identity"],
            "work_count": len(call["work_ids"]),
            "body_count": len(original["bodies"]),
            "body_chars": sum(len(item["content"]) for item in original["bodies"]),
            "source_representation_identity": evidence.canonical_sha256(original),
            "representation_identity": representation_identity,
            "submission_identity": submission_identity,
            "input_tokens": int(usage["input_tokens"]),
            "cached_input_tokens": int(usage["cached_input_tokens"]),
            "wall_seconds": measurement_wall,
            "attempts": int(usage["attempts"]),
            "retries": int(usage["retries"]),
            "discarded_cached_attempts": cached_attempts,
            "rate_limit_events": int(usage["rate_limit_events"]),
            "interrupted_attempts": int(usage["interrupted_attempts"]),
        }

    requests: list[dict[str, Any]] = []
    batches: list[dict[str, Any]] = []
    order = contract["execution"]["balanced_order"]
    with module.CodexAppServerPool(int(contract["execution"]["app_server_pool_size"]), factory) as transport:
        for repeat, variants in enumerate(order, 1):
            for position, variant in enumerate(variants, 1):
                batch_started = time.perf_counter()
                with ThreadPoolExecutor(max_workers=int(contract["execution"]["max_concurrent_turns"])) as pool:
                    futures = [pool.submit(invoke, call, variant, repeat, transport) for call in calls]
                    batch_requests = [future.result() for future in as_completed(futures)]
                batch_elapsed = time.perf_counter() - batch_started
                requests.extend(batch_requests)
                batches.append({
                    "repeat": repeat,
                    "position": position,
                    "variant": variant,
                    "wall_seconds": batch_elapsed,
                    "requests": len(batch_requests),
                })
        diagnostics = transport.diagnostics()
    if transport_parent.is_dir() and not any(transport_parent.iterdir()):
        transport_parent.rmdir()
    expected = int(contract["execution"]["source_calls"]) * len(order) * 2
    _require(len(requests) == expected, "紧凑协议平衡复核请求不完整")
    _require(diagnostics["per_worker_max_active"] == 1, "平衡复核单 worker 出现多活动 turn")
    _require(diagnostics["worker_restarts"] == 0 and diagnostics["rate_limit_observed"] is False, "平衡复核传输不稳定")
    diagnostics = {
        **diagnostics,
        "discarded_cached_attempts": sum(item["discarded_cached_attempts"] for item in requests),
        "actual_model_executions": len(requests) + sum(item["discarded_cached_attempts"] for item in requests),
    }
    return {
        "requests": sorted(requests, key=lambda item: (item["repeat"], item["variant"], item["material"], item["question_id"])),
        "batch_elapsed_seconds": batches,
        "transport": diagnostics,
    }


def _invoke_uncached(
    *,
    capability: Any,
    prompt: str,
    schema: dict[str, Any],
    stage: Path,
    model: str,
    effort: str,
    timeout_seconds: float,
    attempts: int,
    validate: Any,
) -> tuple[dict[str, Any], dict[str, Any], float, int]:
    audit = stage / "_audit"
    discarded = len(list(audit.glob("cached-complete-*.json"))) if audit.is_dir() else 0
    while True:
        value, usage = capability._invoke(
            prompt=prompt,
            schema=schema,
            stage=stage,
            model=model,
            effort=effort,
            timeout_seconds=timeout_seconds,
            attempts=attempts,
            validate=validate,
        )
        attempt_number = int(usage["attempts"])
        metadata = _load_json(stage / f"attempt-{attempt_number:03d}" / "metadata.json")
        if int(usage.get("cached_input_tokens", 0)) == 0:
            return value, usage, float(metadata["wall_seconds"]), discarded
        complete = stage / "complete.json"
        _require(complete.is_file(), "缓存请求缺少终态收据")
        audit.mkdir(parents=True, exist_ok=True)
        archived = audit / f"cached-complete-{evidence.file_sha256(complete)}.json"
        if archived.is_file():
            _require(archived.read_bytes() == complete.read_bytes(), "缓存请求审计收据漂移")
            complete.unlink()
        else:
            complete.replace(archived)
        discarded += 1
        _require(attempt_number < attempts, "紧凑协议平衡复核无法取得 cache=0 请求")


def evaluate_balanced(execution: dict[str, Any], contract: dict[str, Any]) -> dict[str, Any]:
    requests = execution["requests"]
    variants = {variant: [item for item in requests if item["variant"] == variant] for variant in ("current", "compact")}
    repeats = len(contract["execution"]["balanced_order"])
    expected_per_variant = int(contract["execution"]["source_calls"]) * repeats
    _require(all(len(items) == expected_per_variant for items in variants.values()), "平衡复核 variant 请求数漂移")
    _require(all(item["cached_input_tokens"] == 0 for item in requests), "平衡复核最终请求包含缓存输入")
    _require(all(item["rate_limit_events"] == 0 for item in requests), "平衡复核观察到限流")

    observed_token_totals = {
        variant: [
            sum(item["input_tokens"] for item in variants[variant] if item["repeat"] == repeat)
            for repeat in range(1, repeats + 1)
        ]
        for variant in variants
    }
    current_tokens = int(contract["token_gate"]["current_full_input_tokens"])
    compact_tokens = int(contract["token_gate"]["compact_full_input_tokens"])
    component_tokens = compact_tokens - int(contract["token_gate"]["matched_minimum_tokens"])
    token_passed = component_tokens <= float(contract["token_gate"]["candidate_component_maximum"])

    by_key = {
        (item["repeat"], item["variant"], item["material"], item["question_id"]): item
        for item in requests
    }
    paired_deltas = []
    for repeat in range(1, repeats + 1):
        for current in [item for item in variants["current"] if item["repeat"] == repeat]:
            compact = by_key[(repeat, "compact", current["material"], current["question_id"])]
            _require(current["source_representation_identity"] == compact["source_representation_identity"], "平衡复核 A/B 来源事实错绑")
            _require(current["schema_identity"] == compact["schema_identity"], "平衡复核 A/B Schema 错绑")
            _require(current["work_count"] == compact["work_count"] and current["body_count"] == compact["body_count"] and current["body_chars"] == compact["body_chars"], "平衡复核 A/B 工作或正文不等价")
            paired_deltas.append(compact["wall_seconds"] - current["wall_seconds"])
    _require(len(paired_deltas) == int(contract["wall_gate"]["minimum_paired_requests"]), "平衡复核成对样本不足")
    repeat_differences = []
    for variant in ("current", "compact"):
        keys = {(item["material"], item["question_id"]) for item in variants[variant]}
        for material, question_id in keys:
            repeat_differences.append(abs(
                by_key[(1, variant, material, question_id)]["wall_seconds"]
                - by_key[(2, variant, material, question_id)]["wall_seconds"]
            ))
    repeatability_error = max(repeat_differences)
    current_mean = statistics.mean(item["wall_seconds"] for item in variants["current"])
    compact_mean = statistics.mean(item["wall_seconds"] for item in variants["compact"])
    effect = statistics.mean(paired_deltas)
    _require(abs((compact_mean - current_mean) - effect) <= 1e-9, "平衡复核成对效应不闭合")
    nondegraded = effect <= repeatability_error
    implementation_authorized = token_passed and nondegraded
    return {
        "token": {
            "current_full_input_tokens": current_tokens,
            "compact_full_input_tokens": compact_tokens,
            "matched_minimum_tokens": int(contract["token_gate"]["matched_minimum_tokens"]),
            "candidate_component_tokens": component_tokens,
            "candidate_component_maximum": float(contract["token_gate"]["candidate_component_maximum"]),
            "observed_native_usage_totals_diagnostic": observed_token_totals,
            "observed_native_usage_may_include_host_context_drift": True,
            "passed": token_passed,
        },
        "wall": {
            "balanced_order": contract["execution"]["balanced_order"],
            "current_request_mean_seconds": current_mean,
            "compact_request_mean_seconds": compact_mean,
            "compact_minus_current_paired_mean_seconds": effect,
            "repeatability_error_seconds": repeatability_error,
            "non_degradation_rule": contract["wall_gate"]["non_degradation_rule"],
            "nondegraded": nondegraded,
            "paired_request_count": len(paired_deltas),
            "paired_request_mean_delta_seconds": statistics.mean(paired_deltas),
            "paired_request_min_delta_seconds": min(paired_deltas),
            "paired_request_max_delta_seconds": max(paired_deltas),
            "within_variant_repeat_difference_count": len(repeat_differences),
            "within_variant_repeat_difference_mean_seconds": statistics.mean(repeat_differences),
            "within_variant_repeat_difference_max_seconds": max(repeat_differences),
        },
        "integrity": {
            "source_calls": int(contract["execution"]["source_calls"]),
            "repeats": repeats,
            "work_count_per_variant_repeat": sum(item["work_count"] for item in variants["current"] if item["repeat"] == 1),
            "body_chars_per_variant_repeat": sum(item["body_chars"] for item in variants["current"] if item["repeat"] == 1),
            "all_current_and_compact_sources_equal": True,
            "all_output_schemas_equal": True,
            "all_outputs_passed_existing_submission_preflight": True,
            "final_cached_input_tokens": 0,
            "discarded_cached_attempts": execution["transport"]["discarded_cached_attempts"],
        },
        "implementation_authorized": implementation_authorized,
        "next_validation": (
            "implement-compact-equivalent-semantic-protocol-in-isolated-v2-candidate"
            if implementation_authorized
            else "reject-compact-protocol-and-freeze-next-token-representation-route"
        ),
    }


def evaluate(
    source_calls: list[dict[str, Any]],
    requests: list[dict[str, Any]],
    matched_result: dict[str, Any],
    contract: dict[str, Any],
) -> dict[str, Any]:
    by_key = {(item["material"], item["question_id"]): item for item in requests}
    matched_v2 = matched_result["subjects"]["v2"]
    minimum_by_key = {
        (item["material"], item["question_id"]): int(item["fixed_host_schema_and_minimum_request_tokens"])
        for item in matched_v2["request_ledger"]
    }
    compact_total = sum(item["input_tokens"] for item in requests)
    minimum_total = sum(minimum_by_key[(item["material"], item["question_id"])] for item in requests)
    component = compact_total - minimum_total
    token_maximum = float(contract["component_gates"]["semantic_input_tokens_maximum"])
    token_passed = component <= token_maximum

    wall = matched_v2["wall_critical_path"]
    critical_ids = {
        (material, question_id)
        for material, item in wall["materials"].items()
        for question_id in item["critical_question_ids"]
    }
    full_semantic_request_wall = sum(
        float(call["full_usage"]["wall_seconds"])
        for call in source_calls
        if (call["material"], call["question_id"]) in critical_ids
    )
    compact_semantic_request_wall = sum(
        float(by_key[key]["wall_seconds"])
        for key in critical_ids
    )
    critical_semantic_phase = float(wall["critical_chain_phase_seconds"]["semantic"])
    nonrequest_semantic_overhead = critical_semantic_phase - full_semantic_request_wall
    _require(nonrequest_semantic_overhead >= -0.5, "紧凑协议关键路径语义请求超过语义阶段")
    predicted_wall = float(wall["true_wall_seconds"]) - critical_semantic_phase + max(0.0, nonrequest_semantic_overhead) + compact_semantic_request_wall
    fixed_floor = (
        float(wall["shared_reader_judge_seconds"])
        + float(wall["runner_and_unclassified_seconds"])
        + float(matched_v2["semantic_minimum_four_worker_wall_lower_bound_seconds"])
    )
    component_wall = max(0.0, predicted_wall - fixed_floor)
    wall_maximum = float(contract["component_gates"]["end_to_end_wall_seconds_maximum"])
    wall_passed = component_wall <= wall_maximum
    authorized = token_passed and wall_passed
    return {
        "token": {
            "compact_full_input_tokens": compact_total,
            "matched_minimum_tokens": minimum_total,
            "candidate_component_tokens": component,
            "maximum_tokens": token_maximum,
            "passed": token_passed,
        },
        "wall": {
            "current_true_wall_seconds": float(wall["true_wall_seconds"]),
            "current_critical_semantic_phase_seconds": critical_semantic_phase,
            "current_critical_full_request_wall_seconds": full_semantic_request_wall,
            "nonrequest_semantic_overhead_seconds": max(0.0, nonrequest_semantic_overhead),
            "compact_critical_request_wall_seconds": compact_semantic_request_wall,
            "predicted_true_wall_seconds": predicted_wall,
            "fixed_floor_seconds": fixed_floor,
            "candidate_component_seconds": component_wall,
            "maximum_seconds": wall_maximum,
            "passed": wall_passed,
        },
        "implementation_authorized": authorized,
        "next_validation": "implement-compact-equivalent-semantic-protocol-in-isolated-v2-candidate" if authorized else "reject-compact-protocol-and-preserve-first-failed-component",
    }


def load_contract(suite_root: Path) -> dict[str, Any]:
    value = _load_json(suite_root / CONTRACT_PATH)
    _validate_identity(value, CONTRACT_SCHEMA, "紧凑协议可行性合同")
    repository = suite_root.parents[2]
    for item in value["direct_dependencies"]:
        _verify_file(repository / item["path"], item)
    return value


def load_balanced_contract(suite_root: Path) -> dict[str, Any]:
    value = _load_json(suite_root / BALANCED_CONTRACT_PATH)
    _validate_identity(value, BALANCED_CONTRACT_SCHEMA, "紧凑协议平衡复核合同")
    repository = suite_root.parents[2]
    for item in value["direct_dependencies"]:
        _verify_file(repository / item["path"], item)
    return value


def _verify_file(path: Path, item: dict[str, Any]) -> None:
    _require(path.is_file() and evidence.file_sha256(path) == item["sha256"], f"紧凑协议直接依赖漂移: {item['path']}")


def _validate_identity(value: dict[str, Any], schema: str, name: str) -> None:
    _require(value.get("schema") == schema, f"{name} schema 无效")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), f"{name}身份漂移")


def _load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise validation.KernelIterationValidationError(f"无法读取紧凑协议制品 {path}: {error}") from error
    _require(isinstance(value, dict), f"紧凑协议制品不是对象: {path}")
    return value


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise validation.KernelIterationValidationError(message)
