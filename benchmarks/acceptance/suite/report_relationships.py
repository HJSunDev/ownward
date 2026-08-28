from __future__ import annotations

from typing import Any


class RelationshipError(ValueError):
    pass


TARGET_STAGES = (
    "identity", "relations", "merge_split", "incremental_consistency", "organization", "indexing",
    "lexical", "vector", "graph", "context", "fusion",
)

MODE_SCOPES = {
    "targeted": "frontier", "frontier": "frontier", "core": "core",
    "qualification": "product", "full": "product", "longmemeval": "community",
}

# Eligibility is an execution precondition, not invalidation propagation.
START_ELIGIBILITY = {
    "targeted": (), "core": (), "frontier": (),
    "qualification": ("core", "frontier"), "full": ("core", "qualification"),
    "longmemeval": ("core", "full"), "summarize": ("core", "full", "longmemeval"),
}

SUMMARY_AGGREGATES = ("core", "full", "longmemeval")

SCOPE_CONFIG = {
    "frontier": {"sections": ("frontier",), "candidate_binary": False, "embedding": False, "codex": False},
    "core": {"sections": ("candidate",), "candidate_binary": True, "embedding": True, "codex": False},
    "product": {"sections": ("candidate", "product"), "candidate_binary": True, "embedding": True, "codex": True},
    "community": {"sections": ("candidate", "community"), "candidate_binary": True, "embedding": True, "codex": True},
}

SCOPE_MATERIALS = {
    "frontier": (
        "benchmarks/acceptance/suite/materials/core/v1/dataset.json",
        "benchmarks/acceptance/suite/materials/frontier/v1/calibration.json",
    ),
    "core": ("benchmarks/acceptance/suite/materials/core/v1/dataset.json",),
    "product": (
        "benchmarks/acceptance/suite/materials/product/v2/dataset.json",
        "benchmarks/acceptance/suite/materials/product/v2/qualification.json",
        "benchmarks/acceptance/suite/materials/product/v2/review.json",
        "benchmarks/acceptance/suite/adapters/product_resource/thresholds.json",
    ),
    "community": (),
}


def scope_for_mode(mode: str) -> str:
    scope = MODE_SCOPES.get(mode)
    _require(scope is not None, f"模式 {mode} 没有独立绑定范围")
    return scope


def enabled_scopes(config: dict[str, Any]) -> tuple[str, ...]:
    value = config.get("enabled_scopes")
    _require(isinstance(value, list) and value, "执行配置必须显式声明 enabled_scopes")
    _require(all(isinstance(item, str) for item in value), "enabled_scopes 必须是字符串数组")
    _require(len(value) == len(set(value)), "enabled_scopes 不得重复")
    unknown = set(value) - set(SCOPE_CONFIG)
    _require(not unknown, f"enabled_scopes 包含未知范围: {', '.join(sorted(unknown))}")
    return tuple(scope for scope in SCOPE_CONFIG if scope in value)


def selection_identity(mode: str, config: dict[str, Any]) -> dict[str, Any] | None:
    if mode != "targeted":
        return None
    frontier = config.get("frontier")
    _require(isinstance(frontier, dict), "定向执行缺少 frontier 配置")
    stages = frontier.get("targeted_stages")
    _require(isinstance(stages, list) and stages, "定向模式必须声明受影响阶段")
    _require(all(isinstance(item, str) for item in stages), "targeted_stages 必须是字符串数组")
    _require(len(stages) == len(set(stages)) and set(stages) <= set(TARGET_STAGES), "targeted_stages 包含重复或未知阶段")
    return {"targeted_stages": [stage for stage in TARGET_STAGES if stage in stages]}


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise RelationshipError(message)
