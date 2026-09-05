from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import evidence_identity
from execution_support import ExecutionError, require


RESULT_SCHEMA = "ownward.kernel-iteration-blind-suite-execution-result/v1"
PLAN_SCHEMA = "ownward.kernel-iteration-blind-suite-execution-plan/v1"
LEVELS = (5, 15, 25, 50)


def require_blind_completion(config: dict[str, Any]) -> dict[str, Any]:
    """Require one complete, identity-bound blind-suite chain before final product validation."""
    gate = config.get("final_validation_gate")
    require(isinstance(gate, dict), "qualification/full 必须先完成 5/15/25/50 四级盲测")
    output_root = Path(str(gate.get("blind_suite_output", ""))).resolve()
    suite_identity = str(gate.get("suite_identity", ""))
    terminal_plan_identity = str(gate.get("terminal_plan_identity", ""))
    evaluation_batch_identity = str(gate.get("evaluation_batch_identity", ""))
    require(
        output_root.is_dir()
        and _is_sha256(suite_identity)
        and _is_sha256(terminal_plan_identity)
        and _is_sha256(evaluation_batch_identity),
        "最终内部验收必须显式绑定套题、评测批次和 50 题终态计划",
    )

    candidate = config.get("candidate")
    require(isinstance(candidate, dict), "最终内部验收缺少候选清单")
    manifest_path = Path(str(candidate.get("component_manifest", ""))).resolve()
    require(manifest_path.is_file(), "最终内部验收缺少候选组件清单")
    manifest = _load(manifest_path)
    require(manifest.get("identity") == evidence_identity.canonical_sha256({
        name: value for name, value in manifest.items() if name != "identity"
    }), "最终内部验收候选组件清单身份漂移")
    candidate_identity = str(manifest.get("source_subject_identity", ""))
    require(_is_sha256(candidate_identity), "候选组件清单缺少盲测 subject 身份")

    candidate_root = output_root / "blind-suite-runs" / suite_identity / candidate_identity
    terminal_root = candidate_root / terminal_plan_identity
    terminal_path = terminal_root / "result.json"
    require(terminal_path.is_file(), "指定的 50 题盲测终态不存在")
    terminal = _load(terminal_path)
    require(terminal.get("level") == 50 and terminal.get("stage6_complete") is True, "指定计划不是完成的 50 题终态")
    result = _validate_chain(candidate_root, terminal_root, terminal, suite_identity, candidate_identity)
    require(result["evaluation_batch_identity"] == evaluation_batch_identity, "最终内部验收绑定了另一评测批次")
    return result


def _validate_chain(
    candidate_root: Path,
    terminal_root: Path,
    terminal: dict[str, Any],
    suite_identity: str,
    candidate_identity: str,
) -> dict[str, Any]:
    current_root = terminal_root
    current_result = terminal
    expected_batch: str | None = None
    expected_conditions: dict[str, Any] | None = None
    child_plan: dict[str, Any] | None = None
    chain: list[dict[str, Any]] = []
    for level in reversed(LEVELS):
        result_path = current_root / "result.json"
        plan_path = current_root / "plan.json"
        require(result_path.is_file() and plan_path.is_file(), f"盲测 {level} 题终态不完整")
        result = current_result if current_root == terminal_root else _load(result_path)
        plan = _load(plan_path)
        _validate_identity(result, f"盲测 {level} 题结果")
        _validate_identity(plan, f"盲测 {level} 题计划")
        require(result.get("schema") == RESULT_SCHEMA and plan.get("schema") == PLAN_SCHEMA, f"盲测 {level} 题 schema 无效")
        require(result.get("plan_identity") == plan.get("identity"), f"盲测 {level} 题结果与计划错绑")
        require(result.get("level") == level and plan.get("level") == level, f"盲测 {level} 题级别错绑")
        require(result.get("suite_identity") == suite_identity and plan.get("suite_identity") == suite_identity, f"盲测 {level} 题套件错绑")
        require(result.get("candidate_subject_identity") == candidate_identity and plan.get("candidate_subject_identity") == candidate_identity, f"盲测 {level} 题候选错绑")
        require(result.get("formal") is False and result.get("formal_state_written") is False, f"盲测 {level} 题越权写入正式状态")
        require(result.get("contains_reversible_question_answer_evidence_or_case_ids") is False, f"盲测 {level} 题终态泄露可逆内容")
        quality_passed = result.get("passed") is True and result.get("candidate_decision") is True
        continued_after_rejudgment = child_plan is not None
        require(quality_passed or continued_after_rejudgment, f"盲测 {level} 题没有形成可继续的候选通过事实")
        if level == 50:
            require(
                result.get("stage6_complete") is True
                and result.get("passed") is True
                and result.get("next_level") is None,
                "50 题盲测没有完成",
            )
        else:
            require(result.get("next_level") == LEVELS[LEVELS.index(level) + 1] or continued_after_rejudgment, f"盲测 {level} 题没有授权下一关")

        batch = str(plan.get("evaluation_batch_identity", ""))
        conditions = plan.get("shared_conditions")
        require(_is_sha256(batch) and isinstance(conditions, dict), f"盲测 {level} 题共享身份不完整")
        if expected_batch is None:
            expected_batch, expected_conditions = batch, conditions
        require(batch == expected_batch and conditions == expected_conditions, f"盲测 {level} 题不属于同一评测批次和共享条件")
        if child_plan is not None:
            dependencies = _mapping(child_plan, "direct_dependencies")
            require(child_plan.get("previous_plan_identity") == plan["identity"], f"盲测 {level} 题未被下一关连续引用")
            require(dependencies.get("previous-partition-result") == result["identity"], f"盲测 {level} 题结果未被下一关绑定")
        chain.append({"level": level, "plan_identity": plan["identity"], "result_identity": result["identity"]})
        previous = plan.get("previous_plan_identity")
        if level == 5:
            require(previous is None, "5 题盲测错误声明前级")
            break
        require(_is_sha256(str(previous)), f"盲测 {level} 题缺少前级计划")
        current_root = candidate_root / str(previous)
        current_result = _load(current_root / "result.json")
        child_plan = plan

    return {
        "schema": "ownward.final-internal-validation-gate/v1",
        "suite_identity": suite_identity,
        "candidate_subject_identity": candidate_identity,
        "evaluation_batch_identity": expected_batch,
        "chain": list(reversed(chain)),
        "passed": True,
    }


def _load(path: Path) -> dict[str, Any]:
    value = json.loads(path.read_text(encoding="utf-8"))
    require(isinstance(value, dict), f"JSON 必须是对象: {path}")
    return value


def _validate_identity(value: dict[str, Any], label: str) -> None:
    content = {name: item for name, item in value.items() if name != "identity"}
    require(value.get("identity") == evidence_identity.canonical_sha256(content), f"{label}身份漂移")


def _mapping(value: dict[str, Any], name: str) -> dict[str, Any]:
    nested = value.get(name)
    require(isinstance(nested, dict), f"{name} 必须是对象")
    return nested


def _is_sha256(value: str) -> bool:
    return len(value) == 64 and all(character in "0123456789abcdef" for character in value)
