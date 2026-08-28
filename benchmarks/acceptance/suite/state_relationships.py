from __future__ import annotations

from report_relationships import RelationshipError, TARGET_STAGES


BASELINE_AGGREGATES = ("core", "frontier", "qualification")
IMPACT_FEEDBACK = {"local": (), "asset": ("core",), "retrieval": ("targeted",), "organization": ("targeted",)}
IMPACT_STAGES = {
    "local": (),
    "asset": ("identity", "incremental_consistency", "organization", "indexing"),
    "retrieval": ("indexing", "lexical", "vector", "graph", "context", "fusion"),
    "organization": (
        "relations", "merge_split", "incremental_consistency", "organization", "indexing",
        "lexical", "vector", "graph", "context", "fusion",
    ),
}
STAGE_PLANS = {
    "kernel-baseline": ("core", "frontier", "qualification"),
    "stable-candidate": ("core", "full"),
    "final-candidate": ("core", "full", "longmemeval", "summarize"),
}
MODE_INVALIDATION = {
    "targeted": ("targeted",), "core": ("core", "summarize"), "frontier": ("frontier",),
    "qualification": ("qualification",), "full": ("full", "summarize"),
    "longmemeval": ("longmemeval", "summarize"),
}
SCOPE_RESULTS = {
    "frontier": ("targeted", "frontier"), "core": ("core",),
    "product": ("qualification", "full"), "community": ("longmemeval",),
}


def plan_for_impacts(impacts: list[str]) -> list[str]:
    _require(impacts, "至少需要一个变更影响范围")
    unknown = set(impacts) - set(IMPACT_FEEDBACK)
    _require(not unknown, f"未知变更影响范围: {', '.join(sorted(unknown))}")
    wanted = {mode for impact in impacts for mode in IMPACT_FEEDBACK[impact]}
    return [mode for mode in ("targeted", "core") if mode in wanted]


def stages_for_impacts(impacts: list[str]) -> list[str]:
    if "targeted" not in plan_for_impacts(impacts):
        return []
    wanted = {stage for impact in impacts for stage in IMPACT_STAGES[impact]}
    return [stage for stage in TARGET_STAGES if stage in wanted]


def plan_for_stage(stage: str) -> list[str]:
    plan = STAGE_PLANS.get(stage)
    _require(plan is not None, f"未知验收阶段: {stage}")
    return list(plan)


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise RelationshipError(message)
