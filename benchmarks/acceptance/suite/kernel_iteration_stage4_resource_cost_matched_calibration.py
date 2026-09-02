from __future__ import annotations

from concurrent.futures import ThreadPoolExecutor, as_completed
import json
import math
from pathlib import Path
import time
from typing import Any

import kernel_iteration_evidence as evidence
import kernel_iteration_stage4_resource_cost as resource_cost
import kernel_iteration_stage4_resource_cost_audit as resource_audit
import kernel_iteration_validation as validation


CONTRACT_SCHEMA = "ownward.kernel-iteration-stage4-resource-cost-matched-calibration-contract/v1"
RESULT_SCHEMA = "ownward.kernel-iteration-stage4-resource-cost-matched-calibration/v1"
CONTRACT_PATH = Path("iteration/v2/stage4-resource-cost-matched-calibration-contract.json")


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
    _require(
        output_root.is_relative_to(repository / ".tmp" / "kernel-v2-major-iteration"),
        "匹配差分校准必须位于非正式 V2 边界",
    )
    execution_config = execution_config.resolve()
    formal_state = formal_state.resolve()
    _verify_file(execution_config, contract["execution_config"])
    _require(formal_state == repository / contract["formal_state"]["path"], "匹配差分正式 state 路径错绑")
    state_before = evidence.file_sha256(formal_state)
    _require(state_before == contract["formal_state"]["sha256"], "匹配差分前正式 state 漂移")
    result_path = output_root / "calibration.json"
    if result_path.is_file():
        _require(resume, "匹配差分终态已存在；只有 --resume 可逐字复用")
        value = _load_json(result_path)
        _validate_identity(value, RESULT_SCHEMA, "匹配差分终态")
        _require(value["contract_identity"] == contract["identity"], "匹配差分终态合同错绑")
        _require(value["formal_state_sha256"] == state_before, "匹配差分恢复时正式 state 漂移")
        return {**value, "path": str(result_path), "reused": True, "model_executions": 0, "product_executions": 0}

    runtime = validation.validate_execution_config(suite_root, execution_config)
    protocol = _load_json(runtime["protocol"])
    _validate_runtime_contract(runtime, protocol, contract)
    source_calls = discover_source_calls(runtime["runs"], contract)
    output_root.mkdir(parents=True, exist_ok=True)
    calibration_started = time.perf_counter()
    calibration = execute_calibration(
        suite_root=suite_root,
        output_root=output_root,
        runtime=runtime,
        protocol=protocol,
        calls=source_calls,
        contract=contract,
    )
    calibration_elapsed = time.perf_counter() - calibration_started
    _require(calibration_elapsed <= float(contract["execution"]["maximum_wall_seconds"]), "匹配差分校准超过冻结墙钟")
    _require(discover_source_calls(runtime["runs"], contract) == source_calls, "匹配差分校准改写了既有完整请求收据")
    state_after = evidence.file_sha256(formal_state)
    _require(state_after == state_before, "匹配差分改写了正式 state")
    evaluation = evaluate(
        contract=contract,
        source_calls=source_calls,
        calibration=calibration,
        runs=runtime["runs"],
    )
    content = {
        "schema": RESULT_SCHEMA,
        "formal": False,
        "formal_state_written": False,
        "contract_identity": contract["identity"],
        "source_call_count": len(source_calls),
        "calibration_request_count": len(calibration["requests"]),
        "model_profile": contract["model_profile"],
        "transport": calibration["transport"],
        "calibration_elapsed_seconds": calibration_elapsed,
        "subjects": evaluation["subjects"],
        "gate_migration": evaluation["gate_migration"],
        "stage4_complete": False,
        "implementation_authorized": evaluation["implementation_authorized"],
        "next_validation": evaluation["next_validation"],
        "formal_state_sha256": state_after,
    }
    value = {**content, "identity": evidence.canonical_sha256(content)}
    evidence.atomic_json(result_path, value)
    return {
        **value,
        "path": str(result_path),
        "reused": False,
        "model_executions": len(calibration["requests"]),
        "product_executions": 0,
    }


def load_contract(suite_root: Path) -> dict[str, Any]:
    value = _load_json(suite_root / CONTRACT_PATH)
    _validate_identity(value, CONTRACT_SCHEMA, "匹配差分校准合同")
    repository = suite_root.parents[2]
    for item in value["direct_dependencies"]:
        _verify_file(repository / item["path"], item)
    return value


def discover_source_calls(runs: Path, contract: dict[str, Any]) -> list[dict[str, Any]]:
    calls: list[dict[str, Any]] = []
    for subject in ("v0", "v2"):
        expected = contract["sources"][subject]
        subject_calls: list[dict[str, Any]] = []
        for material in ("development", "regression"):
            plan = expected["plans"][material]
            root = runs / "kernel-iteration" / plan["plan_identity"] / "run"
            _require(root.is_dir(), f"匹配差分来源运行缺失: {root}")
            _require(resource_cost._tree_identity(root) == plan["run_root_sha256"], f"匹配差分来源运行漂移: {root}")
            for input_path in sorted((root / "questions").glob("*/semantic-traces/_analysis/*/unit-*/input.json")):
                semantic_input = _load_json(input_path)
                work_ids = semantic_input.get("work_ids")
                _require(isinstance(work_ids, list) and work_ids and all(isinstance(item, str) for item in work_ids), "匹配差分 work_id 无效")
                schema = semantic_output_schema(work_ids)
                schema_identity = evidence.canonical_sha256(schema)
                request_path = input_path.parent / "codex" / "request.json"
                complete_path = input_path.parent / "codex" / "complete.json"
                request = _load_json(request_path)
                complete = _load_json(complete_path)
                _require(schema_identity == semantic_input["schema_sha256"] == request["output_schema_sha256"], "匹配差分输出 Schema 漂移")
                _require(request["model"] == contract["model_profile"]["model"], "匹配差分完整请求模型漂移")
                _require(request["reasoning_effort"] == contract["model_profile"]["reasoning_effort"], "匹配差分完整请求 effort 漂移")
                usage = complete.get("usage")
                _require(isinstance(usage, dict) and int(usage.get("calls", 0)) == 1, "匹配差分完整请求收据无效")
                relative = input_path.relative_to(root).as_posix()
                subject_calls.append({
                    "subject": subject,
                    "material": material,
                    "question_id": input_path.parents[4].name,
                    "source_input": relative,
                    "source_input_sha256": evidence.file_sha256(input_path),
                    "source_request_sha256": evidence.file_sha256(request_path),
                    "source_complete_sha256": evidence.file_sha256(complete_path),
                    "schema": schema,
                    "schema_identity": schema_identity,
                    "work_ids": work_ids,
                    "full_usage": {
                        "input_tokens": int(usage["input_tokens"]),
                        "cached_input_tokens": int(usage.get("cached_input_tokens", 0)),
                        "wall_seconds": float(usage["wall_seconds"]),
                    },
                })
        _require(len(subject_calls) == int(expected["calls"]), f"{subject} 匹配差分调用数漂移")
        _require(sum(item["full_usage"]["input_tokens"] for item in subject_calls) == int(expected["input_tokens"]), f"{subject} 完整 Token 不闭合")
        _require(sum(item["full_usage"]["cached_input_tokens"] for item in subject_calls) == int(expected["cached_input_tokens"]), f"{subject} 完整缓存 Token 漂移")
        calls.extend(subject_calls)
    return calls


def semantic_output_schema(work_ids: list[str]) -> dict[str, Any]:
    return {
        "type": "object",
        "additionalProperties": False,
        "required": ["analyses"],
        "properties": {
            "analyses": {
                "type": "array",
                "minItems": len(work_ids),
                "maxItems": len(work_ids),
                "items": {
                    "type": "object",
                    "additionalProperties": False,
                    "required": ["work_id", "summary", "topics", "cues"],
                    "properties": {
                        "work_id": {"type": "string", "enum": work_ids},
                        "summary": {"type": "string", "maxLength": 320},
                        "topics": {"type": "array", "maxItems": 4, "items": {"type": "string", "maxLength": 100}},
                        "cues": {
                            "type": "array",
                            "maxItems": 4,
                            "items": {
                                "type": "object",
                                "additionalProperties": False,
                                "required": ["text", "kind"],
                                "properties": {
                                    "text": {"type": "string", "maxLength": 200},
                                    "kind": {"type": "string", "maxLength": 40},
                                },
                            },
                        },
                    },
                },
            },
        },
    }


def execute_calibration(
    *,
    suite_root: Path,
    output_root: Path,
    runtime: dict[str, Any],
    protocol: dict[str, Any],
    calls: list[dict[str, Any]],
    contract: dict[str, Any],
) -> dict[str, Any]:
    module = validation._load_longmemeval_module(suite_root)
    instruction = module.ExternalIntelligenceCapability.semantic_instruction()
    _require(evidence.canonical_sha256(instruction) == contract["request_shapes"]["semantic_instruction_identity"], "匹配差分语义指令漂移")
    prompts = {
        "minimum": str(contract["request_shapes"]["minimum_prompt"]),
        "instruction": instruction,
    }
    transport_parent = output_root / ".external-intelligence-runtime"
    external = runtime["external_intelligence"]

    def calibrate(call: dict[str, Any], shape: str, transport: Any) -> dict[str, Any]:
        stage = output_root / "requests" / call["subject"] / call["material"] / call["question_id"] / call["schema_identity"] / shape

        def validate(value: dict[str, Any]) -> None:
            analyses = value.get("analyses")
            _require(
                isinstance(analyses, list)
                and [item.get("work_id") for item in analyses if isinstance(item, dict)] == call["work_ids"],
                "匹配差分输出没有按 Schema work_id 原序返回",
            )

        capability = module.ExternalIntelligenceCapability(transport)
        _value, usage = capability._invoke(
            role="semantic-organization",
            prompt=prompts[shape],
            schema=call["schema"],
            stage=stage,
            model=contract["model_profile"]["model"],
            effort=contract["model_profile"]["reasoning_effort"],
            timeout_seconds=float(protocol["memory"]["semantic_timeout_seconds"]),
            attempts=int(protocol["memory"]["semantic_attempts"]),
            validate=validate,
        )
        _require(int(usage["calls"]) == 1, "匹配差分校准发生非单次成功调用")
        _require(int(usage["cached_input_tokens"]) == 0, "匹配差分缓存策略与既有收据不一致")
        return {
            "subject": call["subject"],
            "material": call["material"],
            "question_id": call["question_id"],
            "schema_identity": call["schema_identity"],
            "work_count": len(call["work_ids"]),
            "shape": shape,
            "usage": {
                "input_tokens": int(usage["input_tokens"]),
                "cached_input_tokens": int(usage["cached_input_tokens"]),
                "wall_seconds": float(usage["wall_seconds"]),
                "attempts": int(usage["attempts"]),
                "retries": int(usage["retries"]),
                "rate_limit_events": int(usage["rate_limit_events"]),
                "interrupted_attempts": int(usage["interrupted_attempts"]),
            },
        }

    requests: list[dict[str, Any]] = []
    with module.open_external_intelligence_runtime(
        driver=external["driver"], binary=external["binary"], credential_file=external["credential_file"],
        max_active=int(contract["execution"]["app_server_pool_size"]),
        worker_processes=int(contract["execution"]["app_server_pool_size"]), runtime_parent=transport_parent,
    ) as transport:
        work = []
        # Alternate shapes so neither shape is systematically assigned only cold or warm workers.
        for index, call in enumerate(calls):
            shapes = ("minimum", "instruction") if index % 2 == 0 else ("instruction", "minimum")
            for shape in shapes:
                work.append((call, shape))
        with ThreadPoolExecutor(max_workers=int(contract["execution"]["max_concurrent_turns"])) as pool:
            futures = [pool.submit(calibrate, call, shape, transport) for call, shape in work]
            for future in as_completed(futures):
                requests.append(future.result())
        diagnostics = transport.diagnostics()
    if transport_parent.is_dir() and not any(transport_parent.iterdir()):
        transport_parent.rmdir()
    _require(len(requests) == len(calls) * 2, "匹配差分校准请求不完整")
    _require(diagnostics["per_worker_max_active"] == 1, "匹配差分 App Server 出现多活动 turn")
    _require(diagnostics["worker_restarts"] == 0, "匹配差分发生 worker 重启")
    _require(diagnostics["rate_limit_observed"] is False, "匹配差分观察到限流")
    return {"requests": sorted(requests, key=lambda item: (item["subject"], item["material"], item["question_id"], item["schema_identity"], item["shape"])), "transport": diagnostics}


def evaluate(
    *,
    contract: dict[str, Any],
    source_calls: list[dict[str, Any]],
    calibration: dict[str, Any],
    runs: Path,
) -> dict[str, Any]:
    calibrated = {
        (item["subject"], item["material"], item["question_id"], item["schema_identity"], item["shape"]): item
        for item in calibration["requests"]
    }
    subjects: dict[str, dict[str, Any]] = {}
    for subject in ("v0", "v2"):
        calls = [item for item in source_calls if item["subject"] == subject]
        ledger = []
        for call in calls:
            key = (subject, call["material"], call["question_id"], call["schema_identity"])
            minimum = calibrated[(*key, "minimum")]["usage"]
            instruction = calibrated[(*key, "instruction")]["usage"]
            full = call["full_usage"]
            _require(instruction["input_tokens"] >= minimum["input_tokens"], "匹配差分通用指令 Token 小于最小请求")
            _require(full["input_tokens"] >= instruction["input_tokens"], "匹配差分完整请求 Token 小于通用指令请求")
            ledger.append({
                "material": call["material"],
                "question_id": call["question_id"],
                "schema_identity": call["schema_identity"],
                "work_count": len(call["work_ids"]),
                "fixed_host_schema_and_minimum_request_tokens": minimum["input_tokens"],
                "generic_semantic_instruction_increment_tokens": instruction["input_tokens"] - minimum["input_tokens"],
                "work_payload_increment_tokens": full["input_tokens"] - instruction["input_tokens"],
                "full_input_tokens": full["input_tokens"],
                "minimum_wall_seconds": minimum["wall_seconds"],
                "instruction_wall_seconds": instruction["wall_seconds"],
                "full_wall_seconds": full["wall_seconds"],
            })
        fixed_tokens = sum(item["fixed_host_schema_and_minimum_request_tokens"] for item in ledger)
        instruction_tokens = sum(item["generic_semantic_instruction_increment_tokens"] for item in ledger)
        payload_tokens = sum(item["work_payload_increment_tokens"] for item in ledger)
        full_tokens = sum(item["full_input_tokens"] for item in ledger)
        _require(fixed_tokens + instruction_tokens + payload_tokens == full_tokens, f"{subject} Token 账本未闭合")
        minimal_walls = [item["minimum_wall_seconds"] for item in ledger]
        minimum_four_worker_lower_bound = max(max(minimal_walls), sum(minimal_walls) / int(contract["execution"]["question_workers"]))
        plans = contract["sources"][subject]["plans"]
        wall = resource_audit.audit_wall(runs, plans)
        subjects[subject] = {
            "calls": len(ledger),
            "token_ledger": {
                "fixed_host_schema_and_minimum_request_tokens": fixed_tokens,
                "generic_semantic_instruction_increment_tokens": instruction_tokens,
                "work_payload_increment_tokens": payload_tokens,
                "candidate_controllable_increment_tokens": instruction_tokens + payload_tokens,
                "full_input_tokens": full_tokens,
                "closure_error_tokens": 0,
            },
            "semantic_minimum_four_worker_wall_lower_bound_seconds": minimum_four_worker_lower_bound,
            "wall_critical_path": wall,
            "request_ledger": ledger,
        }
    migration = evaluate_gate_migration(subjects, contract)
    semantic_open = migration["semantic_input_tokens"]["candidate_component_passed"] is False
    wall_open = migration["end_to_end_wall_seconds"]["candidate_component_passed"] is False
    implementation_authorized = semantic_open or wall_open
    if implementation_authorized:
        next_validation = "prove-mandatory-semantic-facts-token-lower-bound-can-pass-before-changing-protocol"
    else:
        next_validation = "close-end-to-end-resource-cost-after-component-gate-and-protection-verification"
    return {
        "subjects": subjects,
        "gate_migration": migration,
        "implementation_authorized": implementation_authorized,
        "next_validation": next_validation,
    }


def evaluate_gate_migration(subjects: dict[str, dict[str, Any]], contract: dict[str, Any]) -> dict[str, Any]:
    v0 = subjects["v0"]
    v2 = subjects["v2"]
    token_target = float(contract["existing_global_gates"]["v0_semantic_input_tokens"]) * 0.5
    v0_fixed_tokens = int(v0["token_ledger"]["fixed_host_schema_and_minimum_request_tokens"])
    token_unreachable = v0_fixed_tokens > token_target
    v0_controlled_tokens = int(v0["token_ledger"]["candidate_controllable_increment_tokens"])
    v2_controlled_tokens = int(v2["token_ledger"]["candidate_controllable_increment_tokens"])
    token_component_maximum = v0_controlled_tokens * 0.5

    wall_target = float(contract["existing_global_gates"]["v0_end_to_end_wall_seconds"]) * 0.5
    v0_wall = v0["wall_critical_path"]
    v2_wall = v2["wall_critical_path"]
    v0_shared = float(v0_wall["shared_reader_judge_seconds"]) + float(v0_wall["runner_and_unclassified_seconds"])
    v2_shared = float(v2_wall["shared_reader_judge_seconds"]) + float(v2_wall["runner_and_unclassified_seconds"])
    v0_fixed_wall = v0_shared + float(v0["semantic_minimum_four_worker_wall_lower_bound_seconds"])
    v2_fixed_wall = v2_shared + float(v2["semantic_minimum_four_worker_wall_lower_bound_seconds"])
    wall_unreachable = v0_fixed_wall > wall_target
    v0_controlled_wall = max(0.0, float(v0_wall["true_wall_seconds"]) - v0_fixed_wall)
    v2_controlled_wall = max(0.0, float(v2_wall["true_wall_seconds"]) - v2_fixed_wall)
    wall_component_maximum = v0_controlled_wall * 0.5
    return {
        "semantic_input_tokens": {
            "original_global_metric_preserved_as_diagnostic": True,
            "original_global_half_target": token_target,
            "v0_fixed_host_schema_minimum_floor": v0_fixed_tokens,
            "original_global_gate_mathematically_unreachable": token_unreachable,
            "migration_allowed": token_unreachable,
            "candidate_component": "generic-semantic-instruction-plus-work-payload-native-usage-delta",
            "v0_candidate_component_baseline": v0_controlled_tokens,
            "candidate_component_maximum": token_component_maximum,
            "v2_candidate_component": v2_controlled_tokens,
            "candidate_component_ratio": v2_controlled_tokens / v0_controlled_tokens,
            "candidate_component_passed": v2_controlled_tokens <= token_component_maximum,
        },
        "end_to_end_wall_seconds": {
            "original_global_metric_preserved_as_diagnostic": True,
            "original_global_half_target": wall_target,
            "v0_shared_and_minimum_semantic_floor": v0_fixed_wall,
            "original_global_gate_mathematically_unreachable": wall_unreachable,
            "migration_allowed": wall_unreachable,
            "candidate_component": "create-retrieval-persistence-plus-semantic-work-dependent-critical-path-residual",
            "v0_candidate_component_baseline": v0_controlled_wall,
            "candidate_component_maximum": wall_component_maximum,
            "v2_candidate_component": v2_controlled_wall,
            "candidate_component_ratio": v2_controlled_wall / v0_controlled_wall if v0_controlled_wall > 0 else math.inf,
            "candidate_component_passed": v2_controlled_wall <= wall_component_maximum,
        },
        "ownward_data_bytes": {
            "original_gate_changed": False,
            "candidate_ratio": float(contract["closed_storage_dimension"]["candidate_ratio"]),
            "maximum_ratio": 0.5,
            "passed": True,
        },
        "formal_acceptance_contract_changed": False,
        "cross_dimension_compensation": False,
    }


def _validate_runtime_contract(runtime: dict[str, Any], protocol: dict[str, Any], contract: dict[str, Any]) -> None:
    _require(protocol["memory"]["semantic_model"] == contract["model_profile"]["model"], "匹配差分模型漂移")
    _require(protocol["memory"]["semantic_reasoning_effort"] == contract["model_profile"]["reasoning_effort"], "匹配差分 effort 漂移")
    _require(runtime["external_intelligence"]["driver"] == "codex-app-server/v1", "匹配差分传输漂移")
    _require(int(protocol["execution"]["codex_max_active"]) == int(contract["execution"]["app_server_pool_size"]), "匹配差分外部智能池漂移")
    _require(int(protocol["execution"]["max_workers"]) == int(contract["execution"]["question_workers"]), "匹配差分问题并发漂移")
    _require(evidence.file_sha256(runtime["external_intelligence"]["binary"]) == contract["model_profile"]["codex_binary_sha256"], "匹配差分外部智能执行制品漂移")


def _verify_file(path: Path, item: dict[str, Any]) -> None:
    _require(path.is_file() and evidence.file_sha256(path) == item["sha256"], f"匹配差分直接依赖漂移: {item['path']}")


def _validate_identity(value: dict[str, Any], schema: str, name: str) -> None:
    _require(value.get("schema") == schema, f"{name} schema 无效")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), f"{name}身份漂移")


def _load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise validation.KernelIterationValidationError(f"无法读取匹配差分制品 {path}: {error}") from error
    _require(isinstance(value, dict), f"匹配差分制品不是对象: {path}")
    return value


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise validation.KernelIterationValidationError(message)
