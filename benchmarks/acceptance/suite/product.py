from __future__ import annotations

import hashlib
import json
from pathlib import Path
from typing import Any

from evidence import EvidenceError, score_product
from materials import load_json


class ProductExecutionError(ValueError):
    pass


def prepare_tasks(
    dataset: dict[str, Any],
    qualification: dict[str, Any],
    mode: str,
) -> dict[str, Any]:
    _require(mode in {"qualification", "full"}, "专项执行模式无效")
    scenarios = dataset.get("scenarios")
    _require(isinstance(scenarios, list), "专项数据集场景无效")
    wanted = set(qualification.get("scenario_ids", [])) if mode == "qualification" else {
        scenario["truth"]["id"] for scenario in scenarios
    }
    selected = [scenario for scenario in scenarios if scenario["truth"]["id"] in wanted]
    _require(len(selected) == (8 if mode == "qualification" else 24), "专项执行集规模无效")
    tasks = []
    for scenario in selected:
        expression = scenario["expression"]
        tasks.append({
            "scenario_id": expression["id"],
            "information": expression["information"],
            "updates": expression["updates"],
            "query": expression["query"],
        })
    return {
        "schema": "ownward.product-tasks/v1",
        "dataset_version": dataset["version"],
        "mode": mode,
        "dataset_sha256": _sha256_json(dataset),
        "execution": {
            "isolation": "one-empty-ownward-partition-per-scenario",
            "semantic_capability": "connected-external-agent-through-ownward-semantic-work",
            "steps": [
                "create every information item through Ownward MCP and retain fixture-id to stable-id mapping",
                "complete pending semantic work through Ownward's public semantic collaboration contract",
                "apply every update to the same stable identity and complete replacement semantic work",
                "run the natural-language query once through public search and map its evidence to direct_ids",
                "let a fresh external-agent query session use search/read/navigate and map all answer evidence to returned_ids",
                "record latency and complete process-tree resource evidence without changing product behavior",
            ],
            "result_fields": [
                "scenario_id",
                "direct_ids",
                "returned_ids",
                "navigation_ids",
                "answer_facts",
                "grounded",
                "used_navigation",
                "latency_ms",
                "semantic_ms",
                "agent_query_ms",
                "end_to_end_ms",
                "peak_mib",
                "within_latency_budget",
                "within_resource_budget",
            ],
            "prohibited": [
                "truth exposure to product or external agent",
                "test-only product path",
                "candidate-specific prompt or tuning",
                "manual answer substitution",
            ],
        },
        "tasks": tasks,
    }


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
        for field in ("direct_ids", "returned_ids", "navigation_ids"):
            identifiers = item.get(field)
            _require(isinstance(identifiers, list) and len(identifiers) == len(set(identifiers)), f"{identifier} 的 {field} 无效")
            _require(set(identifiers) <= allowed[identifier], f"{identifier} 的 {field} 未映射回冻结信息身份")
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


def load_default_materials(suite_root: Path) -> tuple[dict[str, Any], dict[str, Any]]:
    root = suite_root / "materials" / "product" / "v1"
    return load_json(root / "dataset.json"), load_json(root / "qualification.json")


def _sha256_json(value: Any) -> str:
    encoded = json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise ProductExecutionError(message)
