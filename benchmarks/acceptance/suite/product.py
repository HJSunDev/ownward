from __future__ import annotations

import hashlib
import json
from pathlib import Path
from typing import Any

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


def load_default_materials(suite_root: Path) -> tuple[dict[str, Any], dict[str, Any]]:
    root = suite_root / "materials" / "product" / "v2"
    return load_json(root / "dataset.json"), load_json(root / "qualification.json")


def _sha256_json(value: Any) -> str:
    encoded = json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise ProductExecutionError(message)
