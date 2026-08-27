from __future__ import annotations

from typing import Any

from evidence import EvidenceError, score_product
from product import ProductExecutionError


def score_results(
    contract: dict[str, Any],
    dataset: dict[str, Any],
    qualification: dict[str, Any],
    tasks: dict[str, Any],
    results: dict[str, Any],
    binding: dict[str, Any],
) -> dict[str, Any]:
    _require(tasks.get("schema") == "ownward.product-tasks/v1", "专项任务 schema 无效")
    _require(results.get("schema") == "ownward.product-results/v1", "专项结果 schema 无效")
    _require(results.get("dataset_version") == tasks.get("dataset_version"), "专项结果数据版本不一致")
    _require(results.get("mode") == tasks.get("mode"), "专项结果模式不一致")
    task_items = tasks.get("tasks")
    result_items = results.get("results")
    _require(isinstance(task_items, list) and isinstance(result_items, list), "专项任务或结果集合无效")
    allowed: dict[str, set[str]] = {
        item["scenario_id"]: {value["node_id"] for value in item["information"]}
        for item in task_items
    }
    _require({item.get("scenario_id") for item in result_items} == set(allowed), "专项结果没有且只覆盖执行任务")
    for item in result_items:
        identifier = item["scenario_id"]
        for field in ("direct_ids", "relation_ids", "returned_ids", "navigation_ids"):
            identifiers = item.get(field)
            _require(isinstance(identifiers, list) and len(identifiers) == len(set(identifiers)), f"{identifier} 的 {field} 无效")
            _require(set(identifiers) <= allowed[identifier], f"{identifier} 的 {field} 未映射回冻结信息身份")
        _require(set(item["relation_ids"]) <= set(item["direct_ids"]), f"{identifier} 的关系证据不属于直接检索结果")
        facts = item.get("answer_facts")
        _require(isinstance(facts, list) and all(isinstance(value, str) and value for value in facts), f"{identifier} 的 answer_facts 无效")
        _require(item.get("grounded") is True, f"{identifier} 的回答没有被 Ownward 工具证据支持")
        _require(isinstance(item.get("used_navigation"), bool), f"{identifier} 的关系导航证据无效")
        _require(not item["navigation_ids"] or item["used_navigation"] is True, f"{identifier} 的导航结果缺少真实调用证据")
        for field in ("latency_ms", "semantic_ms", "agent_query_ms", "end_to_end_ms", "peak_mib"):
            _require(isinstance(item.get(field), (int, float)) and item[field] >= 0, f"{identifier} 的 {field} 无效")
        for field in ("within_latency_budget", "within_resource_budget"):
            _require(isinstance(item.get(field), bool), f"{identifier} 的 {field} 无效")
    try:
        return score_product(contract, dataset, qualification, tasks["mode"], result_items, binding)
    except EvidenceError as error:
        raise ProductExecutionError(str(error)) from error


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise ProductExecutionError(message)
