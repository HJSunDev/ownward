from __future__ import annotations

import json
from pathlib import Path
from typing import Any


CONTRACT_SCHEMA = "ownward.acceptance-suite-contract/v1"
LAYER_NAMES = {"core", "product", "community"}
MODE_NAMES = {"targeted", "core", "frontier", "qualification", "full", "longmemeval", "summarize", "self-check"}


def load_contract(path: Path) -> dict[str, Any]:
    with path.open("r", encoding="utf-8") as source:
        value = json.load(source)
    validate_contract(value)
    return value


def validate_contract(value: dict[str, Any]) -> None:
    _require(value.get("schema") == CONTRACT_SCHEMA, "验收契约 schema 无效")
    _require(value.get("suite_version") == "1.0.0", "验收体系版本必须固定为 1.0.0")

    loop = _mapping(value, "optimization_loop")
    _require(loop.get("dimensions") == ["quality", "latency", "resources"], "前沿环必须分别评价质量、时延和资源")
    _require(loop.get("allow_compensating_score") is False, "前沿环不得使用可互相补偿的综合分")
    _require(_mapping(_mapping(loop, "modes"), "targeted").get("max_wall_seconds") == 180, "定向模式预算必须为三分钟")
    _require(_mapping(_mapping(loop, "modes"), "full").get("max_wall_seconds") == 600, "完整模式预算必须为十分钟")
    promotion = _mapping(loop, "promotion")
    _require(all(promotion.get(name) is True for name in ("requires_no_protected_regression", "requires_predeclared_material_improvement", "requires_product_qualification")), "基线晋升条件不完整")
    frontier = _mapping(loop, "external_frontier")
    _require(frontier.get("requires_same_inputs_protocol_environment") is True, "外部前沿必须同口径复现")
    _require(frontier.get("allow_estimated_conversion") is False, "外部前沿不得使用估算换算")

    layers = _mapping(value, "evidence_layers")
    _require(set(layers) == LAYER_NAMES, "正式证据必须且只能包含三层")
    core = _mapping(layers, "core")
    _require(core.get("external_intelligence") is False and core.get("max_wall_seconds") == 300, "固定内核基线必须本地确定且不超过五分钟")
    _require(len(core.get("required_invariants", [])) == 7, "固定内核基线不变量不完整")
    product = _mapping(layers, "product")
    _require(product.get("full_scenarios") == 24 and product.get("full_information") == 120, "固定专项完整集规模无效")
    _require(product.get("qualification_scenarios") == 8 and product.get("qualification_scenarios_per_category") == 2, "固定专项资格集规模无效")
    _require(len(product.get("categories", [])) == 4 and product.get("relation_organization_gain_required") is True, "固定专项覆盖不完整")
    _require(_mapping(_mapping(product, "modes"), "qualification").get("max_wall_seconds") == 1800, "资格集预算必须为三十分钟")
    _require(_mapping(_mapping(product, "modes"), "full").get("max_wall_seconds") == 5400, "完整专项预算必须为九十分钟")
    community = _mapping(layers, "community")
    _require(community.get("version") == "longmemeval-v2/2cc8c540bdb87fe6761629b585e727e1c4704520", "LongMemEval-V2 官方版本无效")
    _require(community.get("domains") == ["web", "enterprise"], "LongMemEval-V2 领域无效")
    _require(_mapping(community, "expected_wall_seconds") == {"min": 14400, "max": 28800}, "LongMemEval-V2 时间边界无效")

    execution = _mapping(value, "execution")
    _require(set(execution.get("modes", [])) == MODE_NAMES, "统一入口模式不完整或包含额外模式")
    _require(execution.get("relationship_authority") == "relationships.py", "内部执行关系必须由唯一权威定义驱动")
    bindings = execution.get("binding_fields", [])
    _require(set(bindings) == {"schema", "suite_version", "candidate", "scopes"}, "候选绑定字段不完整")
    _require(set(execution.get("binding_scopes", [])) == {"frontier", "core", "product", "community"}, "候选分层绑定范围不完整")
    _require(execution.get("artifact_manifest_required") is True, "正式报告必须绑定原始证据清单")
    self_check = _mapping(execution, "self_check")
    _require(not any(self_check.values()), "体系自检不得消费正式样本、晋升基线或生成验收结果")

    reports = _mapping(value, "reports")
    _require(set(reports) == {"frontier", "core", "product", "community", "suite"}, "报告契约不完整")
    schemas: set[str] = set()
    for name, report in reports.items():
        report = _ensure_mapping(report, f"reports.{name}")
        schema = report.get("schema")
        _require(isinstance(schema, str) and schema and schema not in schemas, f"报告 {name} schema 无效或重复")
        schemas.add(schema)
        required = report.get("required")
        _require(isinstance(required, list) and len(required) == len(set(required)) and {"schema", "suite_version"}.issubset(required), f"报告 {name} 必填字段无效")
    _require("dynamic_post_freeze_generation" in value.get("forbidden", []), "必须禁止候选冻结后动态生成正式数据")
    _require("test_only_product_path" in value.get("forbidden", []), "必须禁止产品侧验收专用路径")


def validate_report(contract: dict[str, Any], report_name: str, report: dict[str, Any]) -> None:
    report_contract = _mapping(_mapping(contract, "reports"), report_name)
    _require(report.get("schema") == report_contract.get("schema"), f"{report_name} 报告 schema 无效")
    _require(report.get("suite_version") == contract.get("suite_version"), f"{report_name} 报告体系版本无效")
    missing = [name for name in report_contract.get("required", []) if name not in report]
    _require(not missing, f"{report_name} 报告缺少字段: {', '.join(missing)}")


def _mapping(value: dict[str, Any], name: str) -> dict[str, Any]:
    return _ensure_mapping(value.get(name), name)


def _ensure_mapping(value: Any, name: str) -> dict[str, Any]:
    _require(isinstance(value, dict), f"{name} 必须是对象")
    return value


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise ValueError(message)
