from __future__ import annotations

import json
import hashlib
import struct
import zlib
from pathlib import Path
from typing import Any

import kernel_iteration_evidence as evidence
import kernel_iteration_stage4_resource_cost as resource_cost
import kernel_iteration_validation as validation


SCHEMA = "ownward.kernel-iteration-stage4-resource-cost-controllability-audit/v1"
CONTRACT_SCHEMA = "ownward.kernel-iteration-stage4-resource-cost-controllability-audit-contract/v1"
CONTRACT_PATH = Path("iteration/v2/stage4-resource-cost-controllability-audit-contract.json")
DEPENDENCY_MIGRATION_SCHEMA = "ownward.kernel-iteration-direct-dependency-migration/v1"
DEPENDENCY_MIGRATION_PATH = Path("iteration/v2/stage4-resource-cost-raw-vector-lifecycle-dependency-migration.json")
DEPENDENCY_MIGRATION_REASON = "non-stage4-diagnostic-maintenance-changes-only-explicitly-listed-callers-without-changing-frozen-stage4-contracts-or-results"
SEMANTIC_INSTRUCTION = (
    "Act only as Ownward's external semantic capability. Analyze every supplied semantic work item exactly once. "
    "The items came from Ownward's public semantic_work path; the host will validate and submit your result through "
    "the public semantic_submit path. No query, expected answer, answer-session label, question type, or evaluator "
    "signal is available. Preserve meaning, use only explicit content and candidate evidence, and do not invent "
    "relationships. Bodies are listed once and work items reference them by stable body_ref, id, and revision; "
    "candidate metadata contains every similarity, context, and relation field exposed by semantic_work. Return one "
    "analysis per work_id in the supplied order. Use one short sentence per summary, at most 4 short topics, and at "
    "most 4 cues only for durable answer-bearing facts, entities, preferences, events or decisions. Do not turn "
    "source IDs, conversation dates or acknowledgements into cues.\n\nSemantic input:\n"
)


def run(
    suite_root: Path,
    output_root: Path,
    paired_result_path: Path,
    execution_config_path: Path,
    formal_state_path: Path,
    *,
    resume: bool,
) -> dict[str, Any]:
    suite_root = suite_root.resolve()
    output_root = output_root.resolve()
    result_path = output_root / "audit.json"
    contract = load_contract(suite_root)
    paired = _load_json(paired_result_path.resolve())
    _validate_identity(paired, resource_cost.RESULT_SCHEMA, "同尺资源结果")
    _require(paired["identity"] == contract["paired_result"]["identity"], "审计与冻结成对结果错绑")
    state_path = formal_state_path.resolve()
    state_sha256 = evidence.file_sha256(state_path)
    _require(state_sha256 == contract["formal_state"]["sha256"], "资源审计前正式 state 漂移")
    if result_path.is_file():
        _require(resume, "资源同尺审计已存在；只有 --resume 可复用")
        value = _load_json(result_path)
        _validate_identity(value, SCHEMA, "资源同尺审计")
        _require(value["contract_identity"] == contract["identity"], "资源同尺审计恢复合同漂移")
        _require(value["formal_state_sha256"] == state_sha256, "资源同尺审计恢复 state 漂移")
        return {**value, "path": str(result_path), "reused": True}

    runtime = validation.validate_execution_config(suite_root, execution_config_path.resolve())
    semantic = {
        subject: audit_semantic_inputs(runtime["runs"], paired["subjects"][subject]["plans"])
        for subject in ("v0", "v2")
    }
    wall = {
        subject: audit_wall(runtime["runs"], paired["subjects"][subject]["plans"])
        for subject in ("v0", "v2")
    }
    storage = audit_v2_storage(runtime["runs"], paired["subjects"]["v2"]["plans"], paired)
    decisions = evaluate_gates(paired, semantic, wall, storage)
    _require(evidence.file_sha256(state_path) == state_sha256, "资源同尺审计改写了正式 state")
    content = {
        "schema": SCHEMA,
        "formal": False,
        "formal_state_written": False,
        "contract_identity": contract["identity"],
        "paired_result_identity": paired["identity"],
        "candidate_subject_identity": paired["candidate_subject_identity"],
        "semantic_input_attribution": semantic,
        "wall_critical_path": wall,
        "storage_state_attribution": storage,
        "gate_decisions": decisions,
        "superseded_diagnostic": contract["superseded_diagnostic"],
        "candidate_code_changed": False,
        "stage4_complete": False,
        "formal_state_sha256": state_sha256,
        "next_validation": decisions["next_validation"],
    }
    value = {**content, "identity": evidence.canonical_sha256(content)}
    output_root.mkdir(parents=True, exist_ok=True)
    evidence.atomic_json(result_path, value)
    return {**value, "path": str(result_path), "reused": False}


def load_contract(suite_root: Path) -> dict[str, Any]:
    path = suite_root / CONTRACT_PATH
    value = _load_json(path)
    _validate_identity(value, CONTRACT_SCHEMA, "资源同尺审计合同")
    repository = suite_root.parents[2]
    drifted = {}
    for item in value["direct_dependencies"]:
        dependency = repository / item["path"]
        current = evidence.file_sha256(dependency) if dependency.is_file() else None
        if current != item["sha256"]:
            drifted[item["path"]] = {"frozen": item["sha256"], "current": current}
    if drifted:
        _verify_dependency_migration(suite_root, value["identity"], drifted)
    return value


def _verify_dependency_migration(
    suite_root: Path,
    contract_identity: str,
    drifted: dict[str, dict[str, str | None]],
) -> None:
    migration = _load_json(suite_root / DEPENDENCY_MIGRATION_PATH)
    _validate_identity(migration, DEPENDENCY_MIGRATION_SCHEMA, "Stage 4 精确依赖迁移收据")
    _require(migration.get("reason") == DEPENDENCY_MIGRATION_REASON, "Stage 4 精确依赖迁移原因漂移")
    related = migration.get("related_contract_migrations", {}).get(contract_identity, {})
    changes = {
        item["path"]: {"frozen": item["frozen_sha256"], "current": item["current_sha256"]}
        for item in related.get("changes", [])
    }
    classifications = {item["path"]: item.get("classification") for item in related.get("changes", [])}
    _require(changes == drifted, "资源同尺审计依赖漂移不在精确迁移收据内")
    _require(classifications == {
        "benchmarks/acceptance/suite/kernel_iteration_stage4_resource_cost_audit.py": "dependency-receipt-validation-only",
        "benchmarks/acceptance/suite/kernel_iteration_validation.py": "non-stage4-reader-profile-and-cost-proof-validation-only",
        "benchmarks/longmemeval_s/run.py": "reader-profile-validation-and-formal-protocol-unification-only",
    }, "资源同尺审计依赖迁移分类漂移")
    _require(related.get("preserved") == {
        "contract_identity": True,
        "thresholds": True,
        "evidence_identities": True,
        "formal_state_sha256": True,
    }, "资源同尺审计依赖迁移保护边界漂移")


def audit_semantic_inputs(runs: Path, plans: dict[str, Any]) -> dict[str, Any]:
    totals = {
        "calls": 0,
        "observed_input_tokens": 0,
        "cached_input_tokens": 0,
        "prompt_utf8_bytes": 0,
        "authority_content_utf8_bytes": 0,
        "authority_json_escape_utf8_bytes": 0,
        "ownward_instruction_utf8_bytes": 0,
        "ownward_representation_metadata_utf8_bytes": 0,
    }
    for plan in plans.values():
        questions = runs / "kernel-iteration" / str(plan["plan_identity"]) / "run" / "questions"
        for input_path in sorted(questions.glob("*/semantic-traces/_analysis/*/unit-*/input.json")):
            semantic_input = _load_json(input_path)
            representation = semantic_input["representation"]
            serialized = json.dumps(representation, ensure_ascii=False, separators=(",", ":"))
            prompt = SEMANTIC_INSTRUCTION + serialized
            prompt_bytes = len(prompt.encode("utf-8"))
            _require(prompt_bytes == int(semantic_input["input_utf8_bytes"]), f"语义请求字节漂移: {input_path}")
            _require(hashlib.sha256(prompt.encode("utf-8")).hexdigest() == semantic_input["prompt_sha256"], f"语义请求摘要漂移: {input_path}")
            raw_content_bytes = 0
            encoded_content_bytes = 0
            for body in representation["bodies"]:
                content = str(body["content"])
                raw_content_bytes += len(content.encode("utf-8"))
                encoded_content_bytes += len(json.dumps(content, ensure_ascii=False).encode("utf-8")) - 2
            representation_bytes = len(serialized.encode("utf-8"))
            complete = _load_json(input_path.parent / "codex" / "complete.json")
            usage = complete["usage"]
            totals["calls"] += int(usage["calls"])
            totals["observed_input_tokens"] += int(usage["input_tokens"])
            totals["cached_input_tokens"] += int(usage["cached_input_tokens"])
            totals["prompt_utf8_bytes"] += prompt_bytes
            totals["authority_content_utf8_bytes"] += raw_content_bytes
            totals["authority_json_escape_utf8_bytes"] += encoded_content_bytes - raw_content_bytes
            totals["ownward_instruction_utf8_bytes"] += len(SEMANTIC_INSTRUCTION.encode("utf-8"))
            totals["ownward_representation_metadata_utf8_bytes"] += representation_bytes - encoded_content_bytes
    _require(totals["calls"] > 0, "资源审计没有找到语义 Codex 收据")
    known_prompt_parts = (
        totals["authority_content_utf8_bytes"]
        + totals["authority_json_escape_utf8_bytes"]
        + totals["ownward_instruction_utf8_bytes"]
        + totals["ownward_representation_metadata_utf8_bytes"]
    )
    _require(known_prompt_parts == totals["prompt_utf8_bytes"], "语义请求字节分类未闭合")
    return {
        **totals,
        "known_prompt_parts_utf8_bytes": known_prompt_parts,
        "token_ledger": {
            "authority_content_tokens": None,
            "ownward_variable_protocol_tokens": None,
            "codex_fixed_host_context_tokens": None,
            "shared_evaluation_overhead_tokens": None,
            "opaque_observed_input_tokens": totals["observed_input_tokens"],
            "closure_error_tokens": 0,
        },
        "attribution_status": "not-identifiable-from-existing-aggregate-usage-receipts",
        "fail_closed_reason": (
            "The immutable receipt exposes one aggregate input_tokens value for prompt, output schema and Codex host/thread "
            "context. It preserves no category token counters, so byte-complete prompt attribution cannot be converted into "
            "candidate-versus-host token ownership without guessing."
        ),
    }


def audit_wall(runs: Path, plans: dict[str, Any]) -> dict[str, Any]:
    reports: dict[str, Any] = {}
    combined = {name: 0.0 for name in ("create", "semantic", "retrieval", "reader", "judge", "other", "scheduler_residual")}
    total_wall = 0.0
    for material, plan in plans.items():
        root = runs / "kernel-iteration" / str(plan["plan_identity"]) / "run"
        report = _load_json(root / "report.json")
        report_wall = float(report["cost"]["wall_seconds"])
        chain = critical_question_chain(root / "questions")
        chain_wall = sum(float(item["wall_seconds"]) for item in chain)
        residual = report_wall - chain_wall
        _require(residual >= -0.5, f"关键路径超过报告墙钟: {root}")
        residual = max(0.0, residual)
        phase_totals = {name: 0.0 for name in ("create", "semantic", "retrieval", "reader", "judge", "other")}
        for item in chain:
            for name in phase_totals:
                phase_totals[name] += float(item["phase_seconds"][name])
        for name, value in phase_totals.items():
            combined[name] += value
        combined["scheduler_residual"] += residual
        total_wall += report_wall
        reports[material] = {
            "report_wall_seconds": report_wall,
            "critical_question_ids": [item["question_id"] for item in chain],
            "critical_question_wall_seconds": chain_wall,
            "phase_seconds_on_critical_chain": phase_totals,
            "scheduler_and_boundary_residual_seconds": residual,
        }
    candidate_controlled = combined["create"] + combined["semantic"] + combined["retrieval"]
    fixed_reader_judge = combined["reader"] + combined["judge"]
    unclassified = combined["other"] + combined["scheduler_residual"]
    return {
        "materials": reports,
        "true_wall_seconds": total_wall,
        "critical_chain_phase_seconds": combined,
        "candidate_controlled_seconds": candidate_controlled,
        "shared_reader_judge_seconds": fixed_reader_judge,
        "runner_and_unclassified_seconds": unclassified,
        "candidate_controlled_share": candidate_controlled / total_wall,
        "phase_sum_rejected_as_wall": True,
    }


def critical_question_chain(questions: Path) -> list[dict[str, Any]]:
    intervals: list[dict[str, Any]] = []
    for result_path in sorted(questions.glob("*/result.json")):
        value = _load_json(result_path)
        end = result_path.stat().st_mtime_ns / 1_000_000_000
        wall = float(value["wall_seconds"])
        intervals.append({**value, "start": end - wall, "end": end})
    _require(bool(intervals), f"关键路径缺少逐题结果: {questions}")
    current = max(intervals, key=lambda item: item["end"])
    chain = [current]
    while True:
        candidates = [item for item in intervals if item is not current and -0.25 <= current["start"] - item["end"] <= 1.0]
        if not candidates:
            break
        current = max(candidates, key=lambda item: item["end"])
        chain.append(current)
    chain.reverse()
    return chain


def audit_v2_storage(runs: Path, plans: dict[str, Any], paired: dict[str, Any]) -> dict[str, Any]:
    files: list[dict[str, Any]] = []
    current_bytes = 0
    compacted_bytes = 0
    for plan in plans.values():
        questions = runs / "kernel-iteration" / str(plan["plan_identity"]) / "run" / "questions"
        for path in sorted(questions.glob("*/ownward-data/state/organization.binlog")):
            summary = inspect_derived_log(path)
            files.append({"question_id": path.parents[2].name, **summary})
            current_bytes += summary["current_bytes"]
            compacted_bytes += summary["latest_record_bytes"]
    measured = int(paired["subjects"]["v2"]["storage_breakdown"]["derived_index_state_bytes"])
    _require(current_bytes == measured, "派生日志审计与同尺产品字节不闭合")
    authority = int(paired["subjects"]["v2"]["storage_breakdown"]["authority_asset_bytes"])
    control = int(paired["subjects"]["v2"]["storage_breakdown"]["control_state_bytes"])
    v0_total = int(paired["subjects"]["v0"]["ownward_data_bytes"])
    compacted_total = authority + control + compacted_bytes
    return {
        "files": files,
        "current_derived_index_bytes": current_bytes,
        "latest_record_compacted_bytes": compacted_bytes,
        "obsolete_record_bytes": current_bytes - compacted_bytes,
        "compacted_candidate_product_bytes": compacted_total,
        "compacted_candidate_to_v0_ratio": compacted_total / v0_total,
        "compaction_is_existing_product_semantics": True,
        "counterfactual_only_no_product_write": True,
    }


def inspect_derived_log(path: Path) -> dict[str, Any]:
    encoded = path.read_bytes()
    offset = 0
    frames = 0
    latest: dict[str, int] = {}
    while offset < len(encoded):
        _require(len(encoded) - offset >= 20, f"派生日志尾部损坏: {path}")
        _require(encoded[offset:offset + 4] == b"OWD3", f"派生日志记录头无效: {path}")
        metadata_length, vector_length, checksum = struct.unpack_from("<III", encoded, offset + 4)
        total = 16 + metadata_length + vector_length + 4
        _require(offset + total <= len(encoded), f"派生日志记录被截断: {path}")
        payload = encoded[offset + 16:offset + total - 4]
        _require(zlib.crc32(payload) & 0xFFFFFFFF == checksum, f"派生日志记录校验失败: {path}")
        _require(encoded[offset + total - 4:offset + total] == b"DONE", f"派生日志记录未提交: {path}")
        metadata = json.loads(payload[:metadata_length].decode("utf-8"))
        asset_id = str(metadata["asset_id"])
        _require(bool(asset_id), f"派生日志记录缺少资产身份: {path}")
        latest[asset_id] = total
        frames += 1
        offset += total
    return {
        "path": str(path),
        "current_bytes": len(encoded),
        "latest_record_bytes": sum(latest.values()),
        "obsolete_record_bytes": len(encoded) - sum(latest.values()),
        "frames": frames,
        "current_assets": len(latest),
    }


def evaluate_gates(
    paired: dict[str, Any],
    semantic: dict[str, dict[str, Any]],
    wall: dict[str, dict[str, Any]],
    storage: dict[str, Any],
) -> dict[str, Any]:
    dimensions = paired["gates"]["dimensions"]
    v0_half_wall = float(dimensions["end_to_end_wall_seconds"]["v0"]) * 0.5
    noncandidate_floor = wall["v0"]["shared_reader_judge_seconds"] + wall["v0"]["runner_and_unclassified_seconds"]
    semantic_receipts_match = all(
        semantic[subject]["observed_input_tokens"] == int(dimensions["semantic_input_tokens"][subject])
        for subject in ("v0", "v2")
    )
    _require(semantic_receipts_match, "语义收据总 Token 与成对结果不闭合")
    storage_counterfactual_passes = storage["compacted_candidate_to_v0_ratio"] <= 0.5
    return {
        "semantic_input_tokens": {
            "original_global_measurement_preserved": True,
            "original_gate_changed": False,
            "current_passed": False,
            "candidate_controllability": "not-identifiable-from-existing-receipts",
            "migration_receipt_generated": False,
            "decision": "retain-open-and-fail-closed",
            "reason": "Aggregate usage closes the total but does not expose token ownership; no byte-ratio estimate is accepted as a token split.",
        },
        "end_to_end_wall_seconds": {
            "original_gate_changed": False,
            "current_passed": bool(dimensions["end_to_end_wall_seconds"]["passed"]),
            "v0_half_target_seconds": v0_half_wall,
            "v0_noncandidate_floor_seconds": noncandidate_floor,
            "v0_candidate_controlled_seconds": wall["v0"]["candidate_controlled_seconds"],
            "mathematical_headroom_to_half": noncandidate_floor < v0_half_wall,
            "decision": "retain-original-half-gate",
        },
        "ownward_data_bytes": {
            "original_gate_changed": False,
            "current_passed": bool(dimensions["ownward_data_bytes"]["passed"]),
            "proven_candidate_controllable_obsolete_bytes": storage["obsolete_record_bytes"],
            "existing_compaction_counterfactual_ratio": storage["compacted_candidate_to_v0_ratio"],
            "counterfactual_passes_half_gate": storage_counterfactual_passes,
            "decision": "retain-original-half-gate-and-freeze-compaction-root",
        },
        "stage4_complete": False,
        "candidate_implementation_authorized": False,
        "next_validation": "persist-exact-semantic-token-category-counters-in-nonformal-codex-receipts-without-running-a-model-before-changing-the-semantic-protocol",
    }


def _validate_identity(value: dict[str, Any], schema: str, name: str) -> None:
    _require(value.get("schema") == schema, f"{name} schema 无效")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), f"{name}身份漂移")


def _load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise validation.KernelIterationValidationError(f"无法读取资源审计制品 {path}: {error}") from error
    _require(isinstance(value, dict), f"资源审计制品不是对象: {path}")
    return value


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise validation.KernelIterationValidationError(message)
