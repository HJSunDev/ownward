from __future__ import annotations

import json
import math
from pathlib import Path
from typing import Any

import kernel_iteration_evidence as evidence
import kernel_iteration_validation as validation


CONTRACT = "iteration/v2/stage4-hierarchical-retrieval-feasibility-contract.json"
RESULT_SCHEMA = "ownward.kernel-iteration-stage4-hierarchical-retrieval-feasibility-result/v1"


def run(repository: Path, output: Path, formal_state: Path) -> dict[str, Any]:
    repository, output, formal_state = repository.resolve(), output.resolve(), formal_state.resolve()
    _require(output.is_relative_to(repository / ".tmp"), "分层检索可行性证据只能写入非正式 .tmp 边界")
    _require(not output.exists(), "分层检索可行性结果已存在；禁止覆盖或选择性重跑")
    contract_path = Path(__file__).resolve().parent / CONTRACT
    contract = _read_json(contract_path)
    validate_contract(repository, contract)
    state_before = evidence.file_sha256(formal_state)
    _require(state_before == contract["formal_state_sha256"], "可行性判定前正式 state 漂移")

    theorem = _derive_worst_case_theorem(contract["fusion_contract"])
    traces = [_evaluate_trace(repository, item, theorem) for item in contract["representative_oracle_traces"]]
    total = sum(item["distinct_requests"] for item in traces)
    safe = sum(item["safe_bypass_requests"] for item in traces)
    coverage = safe / total if total else 0.0
    required = float(contract["gates"]["representative_safe_bypass_fraction_minimum"])
    feasible = coverage >= required
    state_after = evidence.file_sha256(formal_state)
    _require(state_after == state_before, "分层检索可行性判定改写了正式 state")

    content: dict[str, Any] = {
        "schema": RESULT_SCHEMA,
        "formal": False,
        "formal_state_written": False,
        "candidate_implemented": False,
        "contract_sha256": evidence.file_sha256(contract_path),
        "controller_sha256": evidence.file_sha256(Path(__file__).resolve()),
        "theorem": theorem,
        "traces": traces,
        "coverage": {
            "representative_requests": total,
            "safe_bypass_requests": safe,
            "safe_bypass_fraction": coverage,
            "required_bypass_fraction": required,
            "p95_fallback_fraction_maximum": contract["gates"]["fallback_request_fraction_maximum"],
            "passed": feasible,
        },
        "cost_basis": contract["cost_basis"],
        "oracle": {
            "full_semantic_trace_requests": total,
            "safe_certificates_compared_byte_for_byte": safe,
            "delivery_mismatches": 0,
            "note": "every representative trace is an existing complete semantic execution; no safe certificate was issued, so no semantic work was rerun",
        },
        "route_status": "feasible-for-candidate-implementation" if feasible else "rejected-before-candidate-implementation",
        "retrieval_latency_closed": False,
        "next_validation": (
            "implement-exact-fail-open-semantic-bypass-candidate"
            if feasible else contract["next_if_rejected"]
        ),
        "model_calls": 0,
        "product_executions": 0,
        "formal_state_sha256_before": state_before,
        "formal_state_sha256_after": state_after,
    }
    value = {**content, "identity": evidence.canonical_sha256(content)}
    evidence.atomic_json(output, value)
    return {**value, "path": str(output)}


def validate_contract(repository: Path, contract: dict[str, Any]) -> None:
    _require(contract.get("schema") == "ownward.kernel-iteration-stage4-hierarchical-retrieval-feasibility-contract/v1", "分层检索可行性合同 schema 错误")
    _require(contract.get("frozen_before_feasibility_results") is True, "可行性门槛必须在结果前冻结")
    _require(contract.get("candidate_implementation_allowed_before_pass") is False, "可行性通过前不得实现候选")
    fusion = contract["fusion_contract"]
    _require(fusion["rrf_offset"] == 60 and fusion["lexical_weight"] == fusion["semantic_weight"] == 1.0, "融合上界合同漂移")
    _require(evidence.file_sha256(repository / "internal/core/service.go") == fusion["source_sha256"], "正式融合实现身份漂移")
    gates = contract["gates"]
    _require(gates["retrieval_mean_ms_maximum"] == 41.201 and gates["retrieval_p95_ms_maximum"] == 78.001, "正式检索门槛漂移")
    _require(gates["representative_safe_bypass_fraction_minimum"] >= 0.95, "p95 安全覆盖门槛不足")
    cost = contract["cost_basis"]
    required_mean = (
        float(cost["fallback_full_retrieval_mean_ms"]) - float(gates["retrieval_mean_ms_maximum"])
    ) / (
        float(cost["fallback_full_retrieval_mean_ms"]) - float(cost["optimistic_nonsemantic_mean_floor_ms"])
    )
    _require(math.isclose(required_mean, float(cost["mean_required_bypass_fraction"]), rel_tol=0, abs_tol=1e-12), "mean 覆盖门计算漂移")
    _require(cost["required_bypass_fraction"] == max(required_mean, float(cost["p95_required_bypass_fraction"])), "总覆盖门未机械合并 mean/p95")
    _require(contract["cost_basis"]["required_bypass_fraction"] >= 0.95, "成本覆盖门未包含 p95 约束")
    _require(len(contract["representative_oracle_traces"]) >= 6, "代表轨迹覆盖不足")


def _derive_worst_case_theorem(fusion: dict[str, Any]) -> dict[str, Any]:
    offset = int(fusion["rrf_offset"])
    lexical_first = 1.0 / (offset + 1)
    lexical_second = 1.0 / (offset + 2)
    semantic_first = 1.0 / (offset + 1)
    semantic_third = 1.0 / (offset + 3)
    original_first_under_adversary = lexical_first + semantic_third
    original_second_under_adversary = lexical_second + semantic_first
    reversible = original_second_under_adversary > original_first_under_adversary
    _require(reversible, "最坏语义贡献无法机械反转直接种子；证明实现与融合合同不一致")
    return {
        "lexical_rank_1": lexical_first,
        "lexical_rank_2": lexical_second,
        "semantic_rank_1_upper_bound": semantic_first,
        "semantic_rank_3_legal_contribution": semantic_third,
        "lexical_rank_1_plus_semantic_rank_3": original_first_under_adversary,
        "lexical_rank_2_plus_semantic_rank_1": original_second_under_adversary,
        "direct_order_reversible_with_three_semantic_eligible_documents": reversible,
        "consequence": "without observing exact query semantics, any request with at least three eligible vector documents and at least two returned sources lacks a proof that source order, relation seeds, reads, and context remain unchanged",
    }


def _evaluate_trace(repository: Path, spec: dict[str, Any], theorem: dict[str, Any]) -> dict[str, Any]:
    path = (repository / spec["path"]).resolve()
    _require(path.is_relative_to(repository), f"代表轨迹越过仓库边界: {path}")
    _require(evidence.file_sha256(path) == spec["sha256"], f"代表轨迹摘要漂移: {spec['name']}")
    value = _read_json(path)
    _require(value.get("identity") == spec["identity"], f"代表轨迹身份错绑: {spec['name']}")
    _require(int(spec["minimum_semantic_eligible_documents"]) >= 3, f"代表轨迹语义候选不足: {spec['name']}")
    count = int(spec["distinct_requests"])
    if spec["kind"] == "execution-result":
        cases = value.get("observation", {}).get("case_evidence", [])
        _require(len(cases) == count, f"代表轨迹题数漂移: {spec['name']}")
        _require(all(int(item["selection"]["returned_sources"]) >= 2 for item in cases), f"代表轨迹不是多结果请求: {spec['name']}")
        _require(value.get("formal") is False and value.get("formal_state_written") is False, f"代表轨迹越过非正式边界: {spec['name']}")
    elif spec["kind"] == "real-scale-summary":
        metrics = value["metrics"]["candidate"]
        _require(value.get("formal") is False and value.get("formal_state_written") is False, "真实规模证据越过非正式边界")
        _require(metrics["asset_count_range"][0] >= spec["minimum_semantic_eligible_documents"], "真实规模资产下界漂移")
        _require(metrics["returned_sources_mean"] >= 2 and metrics["stable_selection_trace_per_case"] is True, "真实规模轨迹不完整")
        _require(metrics["target_delivery_complete"] is True, "完整语义 oracle 交付不完整")
    else:
        raise validation.KernelIterationValidationError(f"未知代表轨迹类型: {spec['kind']}")
    unsafe = theorem["direct_order_reversible_with_three_semantic_eligible_documents"]
    return {
        "name": spec["name"],
        "trace_identity": spec["identity"],
        "trace_sha256": spec["sha256"],
        "distinct_requests": count,
        "full_semantic_oracle_complete": True,
        "safe_bypass_requests": 0 if unsafe else count,
        "unsafe_reason": "unseen-semantic-rank-can-change-direct-order-and-relation-seeds" if unsafe else None,
    }


def _read_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        raise validation.KernelIterationValidationError(f"无法读取 JSON {path}: {error}") from error
    _require(isinstance(value, dict), f"JSON 顶层必须为对象: {path}")
    return value


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise validation.KernelIterationValidationError(message)
