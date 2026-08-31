from __future__ import annotations

import json
from pathlib import Path
from typing import Any, Callable

import kernel_iteration_evidence as evidence


class MaterialSchedulingError(ValueError):
    pass


CHECKPOINT_SCHEMA = "ownward.kernel-iteration-local-replacement-checkpoint/v1"


def run_local_replacement(
    work: list[tuple[int, str, str]],
    maximum_rounds: int,
    checkpoint_path: Path,
    *,
    generate: Callable[[list[tuple[int, str, str]], int], tuple[list[tuple[int, dict[str, Any], dict[str, Any]]], dict[str, Any]]],
    admit: Callable[[list[dict[str, Any]], int], tuple[dict[str, Any], dict[str, Any], list[str]]],
) -> dict[str, Any]:
    """Generate once, then replace only rejected cases and re-admit the full ordered set."""
    _require(maximum_rounds >= 1, "材料局部替换轮次必须为正")
    expected_ids = [case_id for _, _, case_id in work]
    _require(len(expected_ids) == len(set(expected_ids)), "材料局部替换工作身份重复")
    checkpoint_path = checkpoint_path.resolve()
    cases: dict[str, dict[str, Any]] = {}
    pending = list(expected_ids)
    rounds: list[dict[str, Any]] = []
    generation_usages: list[dict[str, Any]] = []
    admission_usages: list[dict[str, Any]] = []
    scheduler = {
        "policy": "bounded-independent-lanes-rejected-only-original-order/v1",
        "max_active_limit": 0,
        "max_active_observed": 0,
        "submitted": 0,
        "per_worker_max_active_turns": 1,
        "result_order": "frozen-coverage-order",
    }
    next_round = 1
    if checkpoint_path.is_file():
        checkpoint = _load_checkpoint(checkpoint_path, expected_ids, maximum_rounds)
        cases = {str(item["case_id"]): dict(item["case"]) for item in checkpoint["cases"]}
        pending = list(checkpoint["pending_case_ids"])
        rounds = [dict(item) for item in checkpoint["rounds"]]
        generation_usages = [dict(item) for item in checkpoint["generation_usages"]]
        admission_usages = [dict(item) for item in checkpoint["admission_usages"]]
        scheduler = dict(checkpoint["scheduler"])
        next_round = int(checkpoint["next_round"])

    by_id = {case_id: item for item in work for case_id in [item[2]]}
    final_admission: dict[str, Any] | None = None
    for round_index in range(next_round, maximum_rounds + 1):
        selected = [by_id[case_id] for case_id in expected_ids if case_id in set(pending)]
        _require(bool(selected), "材料局部替换没有待生成项")
        generated, round_scheduler = generate(selected, round_index)
        _require([item[0] for item in generated] == [item[0] for item in selected], "局部生成没有按冻结原序重组")
        generated_ids = []
        _require(len(generated) == len(selected), "局部生成结果数量漂移")
        for (_, _, expected_id), (_, case, usage) in zip(selected, generated):
            actual_id = str(case.get("case_id"))
            _require(actual_id == expected_id, "局部生成案例身份错绑")
            cases[expected_id] = case
            generated_ids.append(expected_id)
            generation_usages.append(dict(usage))
        ordered = [cases[case_id] for case_id in expected_ids]
        admission, admission_usage, rejected_ids = admit(ordered, round_index)
        _require(set(rejected_ids).issubset(set(expected_ids)), "质量准入返回未知拒绝案例")
        _require(len(rejected_ids) == len(set(rejected_ids)), "质量准入返回重复拒绝案例")
        _require(int(admission.get("rejected_count", -1)) == len(rejected_ids), "质量准入拒绝数量与局部替换集合不一致")
        admission_usages.append(dict(admission_usage))
        scheduler = _merge_scheduler(scheduler, round_scheduler)
        receipt = {
            "round": round_index,
            "generated_count": len(generated_ids),
            "preserved_count": len(expected_ids) - len(generated_ids),
            "full_admission_questions": len(expected_ids),
            "rejected_count": len(rejected_ids),
            "failure_aggregate": admission.get("failure_aggregate"),
            "material_identity": evidence.canonical_sha256(ordered),
            "rejected_case_set_identity": evidence.canonical_sha256(sorted(rejected_ids)),
        }
        rounds.append(receipt)
        final_admission = admission
        if not rejected_ids:
            checkpoint_path.unlink(missing_ok=True)
            return {
                "passed": True,
                "cases": ordered,
                "admission": admission,
                "generation_usages": generation_usages,
                "admission_usages": admission_usages,
                "scheduler": scheduler,
                "rounds": rounds,
            }
        pending = [case_id for case_id in expected_ids if case_id in set(rejected_ids)]
        if round_index < maximum_rounds:
            _write_checkpoint(
                checkpoint_path,
                expected_ids,
                maximum_rounds,
                round_index + 1,
                cases,
                pending,
                rounds,
                generation_usages,
                admission_usages,
                scheduler,
            )

    checkpoint_path.unlink(missing_ok=True)
    _require(final_admission is not None, "材料局部替换没有形成质量准入结果")
    return {
        "passed": False,
        "cases": [cases[case_id] for case_id in expected_ids],
        "admission": final_admission,
        "generation_usages": generation_usages,
        "admission_usages": admission_usages,
        "scheduler": scheduler,
        "rounds": rounds,
    }


def _merge_scheduler(total: dict[str, Any], current: dict[str, Any]) -> dict[str, Any]:
    _require(current.get("per_worker_max_active_turns") == 1, "生成 worker 不是单活动 turn")
    _require(current.get("result_order") == "frozen-coverage-order", "生成结果顺序漂移")
    return {
        "policy": "bounded-independent-lanes-rejected-only-original-order/v1",
        "max_active_limit": max(int(total.get("max_active_limit", 0)), int(current.get("max_active_limit", 0))),
        "max_active_observed": max(int(total.get("max_active_observed", 0)), int(current.get("max_active_observed", 0))),
        "submitted": int(total.get("submitted", 0)) + int(current.get("submitted", 0)),
        "per_worker_max_active_turns": 1,
        "result_order": "frozen-coverage-order",
    }


def _write_checkpoint(
    path: Path,
    expected_ids: list[str],
    maximum_rounds: int,
    next_round: int,
    cases: dict[str, dict[str, Any]],
    pending: list[str],
    rounds: list[dict[str, Any]],
    generation_usages: list[dict[str, Any]],
    admission_usages: list[dict[str, Any]],
    scheduler: dict[str, Any],
) -> None:
    content = {
        "schema": CHECKPOINT_SCHEMA,
        "expected_case_ids": expected_ids,
        "maximum_rounds": maximum_rounds,
        "next_round": next_round,
        "cases": [{"case_id": case_id, "case": cases[case_id]} for case_id in expected_ids if case_id in cases],
        "pending_case_ids": pending,
        "rounds": rounds,
        "generation_usages": generation_usages,
        "admission_usages": admission_usages,
        "scheduler": scheduler,
    }
    evidence.atomic_json(path, {**content, "identity": evidence.canonical_sha256(content)})


def _load_checkpoint(path: Path, expected_ids: list[str], maximum_rounds: int) -> dict[str, Any]:
    value = json.loads(path.read_text(encoding="utf-8"))
    _require(isinstance(value, dict), "材料局部替换检查点顶层无效")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("schema") == CHECKPOINT_SCHEMA, "材料局部替换检查点 schema 无效")
    _require(value.get("identity") == evidence.canonical_sha256(content), "材料局部替换检查点摘要漂移")
    _require(value.get("expected_case_ids") == expected_ids, "材料局部替换检查点工作身份漂移")
    _require(value.get("maximum_rounds") == maximum_rounds, "材料局部替换检查点最大轮次漂移")
    next_round = int(value.get("next_round", 0))
    _require(2 <= next_round <= maximum_rounds, "材料局部替换检查点下一轮无效")
    pending = value.get("pending_case_ids")
    _require(isinstance(pending, list) and pending and set(pending).issubset(set(expected_ids)), "材料局部替换检查点拒绝集合无效")
    saved = value.get("cases")
    _require(isinstance(saved, list) and {item.get("case_id") for item in saved if isinstance(item, dict)} == set(expected_ids), "材料局部替换检查点没有保存完整当前集合")
    return value


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise MaterialSchedulingError(message)
