from __future__ import annotations

from collections import Counter
import hashlib
import json
import math
from pathlib import Path
from typing import Any


DYNAMIC_REPORT_SCHEMA = "ownward.dynamic-acceptance-report/v2"
ABLATION_REPORT_SCHEMA = "ownward.organization-ablation-report/v2"
PROTOCOL_SCHEMA = "ownward.dynamic-acceptance-protocol/v2"
RELATION_TYPES = {
    "same_as",
    "broader_than",
    "narrower_than",
    "part_of",
    "has_part",
    "supports",
    "contradicts",
    "derived_from",
    "applies_in",
    "related_to",
}


def require(condition: bool, message: str) -> None:
    if not condition:
        raise RuntimeError(message)


def load_json(path: Path) -> dict[str, Any]:
    value = json.loads(path.read_text(encoding="utf-8"))
    require(isinstance(value, dict), f"JSON root must be an object: {path}")
    return value


def write_json(path: Path, value: object) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    encoded = json.dumps(value, ensure_ascii=False, indent=2) + "\n"
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(encoded, encoding="utf-8")
    temporary.replace(path)


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def wilson_lower(successes: int, total: int, confidence_level: float) -> float:
    require(0 <= successes <= total, "invalid success count")
    require(0.5 < confidence_level < 1, "invalid confidence level")
    if total == 0:
        return 0.0
    # 第一版协议固定 95% 置信水平；保留显式参数以拒绝悄然改变统计口径。
    require(abs(confidence_level - 0.95) < 1e-12, "unsupported confidence level")
    z = 1.959963984540054
    proportion = successes / total
    denominator = 1 + z * z / total
    center = proportion + z * z / (2 * total)
    margin = z * math.sqrt((proportion * (1 - proportion) + z * z / (4 * total)) / total)
    return (center - margin) / denominator


def canonical_relation(source: str, relation_type: str, target: str) -> tuple[str, str, str]:
    if relation_type == "narrower_than":
        source, relation_type, target = target, "broader_than", source
    elif relation_type == "has_part":
        source, relation_type, target = target, "part_of", source
    if relation_type in {"same_as", "related_to", "contradicts"} and source > target:
        source, target = target, source
    return source, relation_type, target


def generation_prompt(protocol: dict[str, Any], random_seed: str) -> str:
    generation = protocol["generation"]
    return f"""Create the hidden semantic truth for a one-time, post-freeze acceptance run of a general personal-information system.

Random source: {random_seed}
Required generation contract: {json.dumps(generation, ensure_ascii=False, separators=(',', ':'))}

Generate exactly the required number of independent scenarios, balanced equally across the four task classes. Each scenario must contain exactly the required number of nodes and use globally unique scenario and node IDs. Invent unrelated domains, cultures, activities, technologies, time periods, names, and wording; do not reuse common benchmark examples or Ownward examples. Across the full batch cover every information_scope value, cross-time links, multiple belonging, hierarchy, intersection, composition, contextual and non-contextual facts, changed information, and varied expression intent.

This is hidden truth, so describe atomic facts and exact relation/query truth without writing the final natural-language information. Every query must require the expected nodes and exact answer_facts. Cross-time queries require at least two linked nodes. Multi-hop queries require at least three expected nodes connected by a path of two or more relation edges. A context task must include a plausible forbidden node from an incompatible context. Each update task must replace exactly one node and its query must depend on that updated node; all other task classes must contain no update. Across the batch, relation truth must include hierarchy, composition, contextual applicability, and at least one node with multiple direct semantic relations. Relations must use only the semantic relation vocabulary supported by the output schema. Do not mention the product or its implementation."""


def expression_prompt(hidden: dict[str, Any]) -> str:
    return f"""Turn each hidden semantic scenario below into natural-language personal information and one natural-language question.

Preserve every scenario ID and node ID. Express all hidden facts faithfully, but vary language, sentence structure, chronology, domain vocabulary, and explicitness. Do not state relation labels, graph structure, expected IDs, forbidden IDs, information-scope labels, or that this is a test. Each initial node gets exactly one content string. Each hidden update gets exactly one replacement content string for the same node. The question must be answerable only from the intended facts, and answer facts must appear verbatim somewhere in the relevant current information. Do not add facts that create unintended relations or make a forbidden contextual item valid.

Hidden truth:
{json.dumps(hidden, ensure_ascii=False, separators=(',', ':'))}"""


def validation_prompt(hidden: dict[str, Any], expression: dict[str, Any]) -> str:
    return f"""Independently validate generated natural-language information against its hidden semantic truth. You are a data validator, not the system under test.

For every scenario, mark valid only if all facts, relation directions, context applicability, update replacement, expected query evidence, forbidden evidence, and exact answer facts are unambiguously expressed; the question must have one defensible answer from the current information. Reject ambiguity, missing evidence, accidental extra relations, answer leakage in the question, or any changed meaning. Preserve every scenario ID and provide concise issues. Evaluate all scenarios without selecting based on any product result; no product output exists yet.

Hidden truth:
{json.dumps(hidden, ensure_ascii=False, separators=(',', ':'))}

Natural-language expression:
{json.dumps(expression, ensure_ascii=False, separators=(',', ':'))}"""


def agent_prompt(questions: list[dict[str, str]]) -> str:
    return f"""Use only the connected Ownward tools; do not use shell, file, web, or any other tools. First obtain Ownward's collaboration rules. Answer every question below from Ownward. For simple questions search once when sufficient; for complex questions use accumulated evidence, relation navigation when available, and reads until the evidence is complete. Never guess.

For each query_id return the exact answer facts as they appear in current information and the stable Ownward information IDs that jointly support the answer. Do not include unrelated IDs.

Questions:
{json.dumps(questions, ensure_ascii=False, separators=(',', ':'))}"""


def validate_protocol(protocol: dict[str, Any]) -> None:
    require(protocol.get("schema") == PROTOCOL_SCHEMA, "unsupported dynamic protocol")
    generation = protocol.get("generation")
    statistics = protocol.get("statistics")
    execution = protocol.get("execution")
    models = protocol.get("models")
    runtime = protocol.get("runtime")
    product_runtime = protocol.get("product_runtime")
    require(isinstance(generation, dict) and isinstance(statistics, dict), "protocol is incomplete")
    require(isinstance(execution, dict) and isinstance(models, dict), "protocol execution contract or models are missing")
    require(isinstance(runtime, dict) and str(runtime.get("codex_cli_version", "")).startswith("codex-cli "), "Codex CLI version is missing")
    require(
        isinstance(product_runtime, dict)
        and product_runtime.get("mode") == "release-defaults"
        and isinstance(product_runtime.get("prohibited_environment"), list)
        and bool(product_runtime.get("prohibited_environment")),
        "formal product runtime must use isolated release defaults",
    )
    classes = generation.get("task_classes")
    require(isinstance(classes, list) and len(classes) == len(set(classes)) == 4, "task classes must contain four unique values")
    generated = int(generation.get("generated_scenarios", 0))
    minimum_valid = int(generation.get("minimum_valid_scenarios", 0))
    minimum_per_class = int(generation.get("minimum_scenarios_per_task_class", 0))
    require(generated >= minimum_valid >= minimum_per_class * len(classes) > 0, "scenario counts are inconsistent")
    require(int(generation.get("information_per_scenario", 0)) >= 4, "each scenario needs enough information to test organization")
    scope = generation.get("information_scope")
    require(isinstance(scope, list) and len(scope) == len(set(scope)) >= 10, "information scope is incomplete")
    require(float(statistics.get("confidence_level", 0)) == 0.95, "confidence level differs from the frozen method")
    require(bool(str(statistics.get("basis", "")).strip()), "statistical basis is missing")
    for name in (
        "dynamic_task_success_wilson_lower_min",
        "relation_precision_wilson_lower_min",
        "relation_recall_wilson_lower_min",
        "minimum_quality_gain",
        "minimum_latency_or_cost_reduction",
    ):
        require(0 < float(statistics.get(name, 0)) <= 1, f"invalid statistical threshold: {name}")
    require(0 <= float(statistics.get("ablation_equivalence_margin", -1)) < 1, "invalid ablation equivalence margin")
    for name in (
        "dataset_stage_seconds_max",
        "codex_inactivity_seconds",
        "inspection_operation_stall_seconds",
        "organization_operation_stall_seconds",
        "organization_p95_seconds_max",
        "agent_seconds_per_question_max",
        "agent_tool_calls_per_query",
    ):
        require(float(execution.get(name, 0)) > 0, f"invalid dynamic execution value: {name}")
    require(
        float(execution["organization_operation_stall_seconds"])
        > float(execution["organization_p95_seconds_max"]),
        "organization stall protection must exceed the accepted P95",
    )
    require(
        float(execution["inspection_operation_stall_seconds"])
        <= float(execution["organization_p95_seconds_max"]),
        "inspection stall protection must not exceed the accepted organization P95",
    )
    require(int(execution.get("parallel_conditions", 0)) == 2, "full and baseline conditions must run as one parallel pair")
    for role in ("generator", "validator", "external_agent"):
        value = models.get(role)
        require(isinstance(value, dict) and value.get("model") and value.get("reasoning_effort"), f"{role} model is missing")
    require(models["generator"]["model"] != models["validator"]["model"], "generator and validator models must differ")


def validate_hidden_world(hidden: dict[str, Any], protocol: dict[str, Any]) -> list[dict[str, Any]]:
    generation = protocol["generation"]
    scenarios = hidden.get("scenarios")
    require(isinstance(scenarios, list), "hidden world has no scenarios")
    require(len(scenarios) == int(generation["generated_scenarios"]), "hidden world scenario count changed")
    expected_classes = list(generation["task_classes"])
    expected_scope = set(generation["information_scope"])
    information_count = int(generation["information_per_scenario"])
    seen_scenarios: set[str] = set()
    class_counts: Counter[str] = Counter()
    scope_counts: Counter[str] = Counter()
    relation_type_counts: Counter[str] = Counter()
    has_multi_relation_node = False
    for scenario in scenarios:
        require(isinstance(scenario, dict), "scenario must be an object")
        scenario_id = str(scenario.get("id", "")).strip()
        require(scenario_id and scenario_id not in seen_scenarios, "scenario identities must be unique")
        seen_scenarios.add(scenario_id)
        task_class = str(scenario.get("task_class", ""))
        require(task_class in expected_classes, f"unsupported task class: {task_class}")
        class_counts[task_class] += 1
        scopes = scenario.get("information_scope")
        require(isinstance(scopes, list) and scopes, f"scenario {scenario_id} has no information scope")
        require(set(scopes) <= expected_scope, f"scenario {scenario_id} uses an unknown information scope")
        scope_counts.update(str(value) for value in scopes)
        nodes = scenario.get("nodes")
        require(isinstance(nodes, list) and len(nodes) == information_count, f"scenario {scenario_id} has the wrong node count")
        node_ids = [str(node.get("id", "")).strip() for node in nodes if isinstance(node, dict)]
        require(len(node_ids) == len(nodes) and all(node_ids) and len(node_ids) == len(set(node_ids)), f"scenario {scenario_id} has invalid node ids")
        for node in nodes:
            facts = node.get("facts")
            require(isinstance(facts, list) and facts and all(str(item).strip() for item in facts), f"scenario {scenario_id} has an empty fact")
        relations = scenario.get("relations")
        require(isinstance(relations, list) and relations, f"scenario {scenario_id} has no semantic relation")
        relation_keys: set[tuple[str, str, str]] = set()
        relation_degree: Counter[str] = Counter()
        for relation in relations:
            source = str(relation.get("source_id", ""))
            target = str(relation.get("target_id", ""))
            relation_type = str(relation.get("type", ""))
            require(source in node_ids and target in node_ids and source != target, f"scenario {scenario_id} has an invalid relation target")
            require(relation_type in RELATION_TYPES, f"scenario {scenario_id} has an invalid relation type")
            key = canonical_relation(source, relation_type, target)
            require(key not in relation_keys, f"scenario {scenario_id} has duplicate relations")
            relation_keys.add(key)
            relation_type_counts[key[1]] += 1
            relation_degree[source] += 1
            relation_degree[target] += 1
        has_multi_relation_node = has_multi_relation_node or any(value >= 2 for value in relation_degree.values())
        updates = scenario.get("updates")
        require(isinstance(updates, list), f"scenario {scenario_id} updates must be an array")
        for update in updates:
            require(str(update.get("node_id", "")) in node_ids, f"scenario {scenario_id} updates an unknown node")
            facts = update.get("replacement_facts")
            require(isinstance(facts, list) and facts, f"scenario {scenario_id} has an empty update")
        if task_class == "information_update":
            require(len(updates) == 1, f"update scenario {scenario_id} must contain exactly one necessary update")
        else:
            require(not updates, f"non-update scenario {scenario_id} contains an unnecessary update")
        query = scenario.get("query")
        require(isinstance(query, dict), f"scenario {scenario_id} has no query truth")
        expected = query.get("expected_ids")
        forbidden = query.get("forbidden_ids")
        answers = query.get("answer_facts")
        require(isinstance(expected, list) and expected and set(expected) <= set(node_ids), f"scenario {scenario_id} has invalid expected ids")
        require(isinstance(forbidden, list) and set(forbidden) <= set(node_ids) and not set(expected) & set(forbidden), f"scenario {scenario_id} has invalid forbidden ids")
        require(isinstance(answers, list) and answers and all(str(value).strip() for value in answers), f"scenario {scenario_id} has no answer facts")
        if task_class == "cross_time":
            require(len(set(expected)) >= 2, f"cross-time scenario {scenario_id} does not require linked information")
        if task_class == "multi_hop":
            expected_set = {str(value) for value in expected}
            adjacency: dict[str, set[str]] = {value: set() for value in expected_set}
            for source, _, target in relation_keys:
                if source in expected_set and target in expected_set:
                    adjacency[source].add(target)
                    adjacency[target].add(source)
            spans_two_hops = False
            for start in expected_set:
                frontier = {start}
                visited = {start}
                for depth in range(1, 3):
                    frontier = {neighbor for current in frontier for neighbor in adjacency[current]} - visited
                    if depth == 2 and frontier:
                        spans_two_hops = True
                    visited.update(frontier)
            require(len(expected_set) >= 3 and spans_two_hops, f"multi-hop scenario {scenario_id} has no two-hop evidence path")
        if task_class == "context_applicability":
            require(bool(forbidden), f"context scenario {scenario_id} has no incompatible evidence")
        if task_class == "information_update":
            require(
                bool({str(value["node_id"]) for value in updates} & {str(value) for value in expected}),
                f"update scenario {scenario_id} query does not depend on updated information",
            )
    expected_per_class = int(generation["generated_scenarios"]) // len(expected_classes)
    require(all(class_counts[value] == expected_per_class for value in expected_classes), "generated task classes are not balanced")
    require(set(scope_counts) == expected_scope, "generated worlds do not cover the complete information scope")
    require(
        {"broader_than", "part_of", "applies_in"} <= set(relation_type_counts),
        "generated worlds do not cover hierarchy, composition, and contextual relations",
    )
    require(has_multi_relation_node, "generated worlds do not exercise multiple belonging or composition")
    return scenarios


def merge_valid_dataset(
    hidden: dict[str, Any], expression: dict[str, Any], validation: dict[str, Any], protocol: dict[str, Any]
) -> dict[str, Any]:
    hidden_scenarios = {str(item["id"]): item for item in validate_hidden_world(hidden, protocol)}
    expressed = expression.get("scenarios")
    verdicts = validation.get("scenarios")
    require(isinstance(expressed, list) and isinstance(verdicts, list), "expression or validation output is incomplete")
    expression_by_id = {str(item.get("id", "")): item for item in expressed if isinstance(item, dict)}
    validation_by_id = {str(item.get("id", "")): item for item in verdicts if isinstance(item, dict)}
    require(set(expression_by_id) == set(hidden_scenarios), "expression output changed the scenario set")
    require(set(validation_by_id) == set(hidden_scenarios), "validator changed the scenario set")
    validated: list[dict[str, Any]] = []
    rejected: list[dict[str, Any]] = []
    class_counts: Counter[str] = Counter()
    for scenario_id, truth in hidden_scenarios.items():
        text = expression_by_id[scenario_id]
        verdict = validation_by_id[scenario_id]
        information = text.get("information")
        query = text.get("query")
        require(isinstance(information, list), f"scenario {scenario_id} has no expressed information")
        node_ids = {str(node["id"]) for node in truth["nodes"]}
        expressed_ids = {str(item.get("node_id", "")) for item in information if isinstance(item, dict)}
        require(expressed_ids == node_ids and len(information) == len(node_ids), f"scenario {scenario_id} changed information identities")
        require(all(str(item.get("content", "")).strip() for item in information), f"scenario {scenario_id} has empty natural-language information")
        expressed_updates = text.get("updates")
        require(isinstance(expressed_updates, list), f"scenario {scenario_id} updates are missing")
        expected_update_ids = {str(item["node_id"]) for item in truth["updates"]}
        actual_update_ids = {str(item.get("node_id", "")) for item in expressed_updates if isinstance(item, dict)}
        require(actual_update_ids == expected_update_ids and len(expressed_updates) == len(expected_update_ids), f"scenario {scenario_id} changed update identities")
        require(all(str(item.get("content", "")).strip() for item in expressed_updates), f"scenario {scenario_id} has an empty natural-language update")
        require(isinstance(query, dict) and str(query.get("question", "")).strip(), f"scenario {scenario_id} has no natural-language query")
        current_content = {str(item["node_id"]): str(item["content"]) for item in information}
        current_content.update({str(item["node_id"]): str(item["content"]) for item in expressed_updates})
        answer_facts = [str(value) for value in truth["query"]["answer_facts"]]
        expected_ids = {str(value) for value in truth["query"]["expected_ids"]}
        question = str(query["question"])
        for fact in answer_facts:
            require(fact not in question, f"scenario {scenario_id} leaks an answer fact in the question")
            require(
                any(fact in current_content[node_id] for node_id in expected_ids),
                f"scenario {scenario_id} answer truth is absent from current expected information",
            )
            require(
                all(fact not in content for node_id, content in current_content.items() if node_id not in expected_ids),
                f"scenario {scenario_id} answer truth is not unique to expected information",
            )
        issues = verdict.get("issues")
        require(isinstance(issues, list), f"scenario {scenario_id} validator issues are invalid")
        if verdict.get("valid") is True:
            item = {"truth": truth, "expression": text}
            validated.append(item)
            class_counts[str(truth["task_class"])] += 1
        else:
            rejected.append({"id": scenario_id, "issues": [str(issue) for issue in issues]})
    generation = protocol["generation"]
    require(len(validated) >= int(generation["minimum_valid_scenarios"]), "independent validation left too few valid scenarios")
    require(
        all(class_counts[value] >= int(generation["minimum_scenarios_per_task_class"]) for value in generation["task_classes"]),
        "independent validation left a task class underpowered",
    )
    required_per_class = int(generation["minimum_scenarios_per_task_class"])
    selected_counts: Counter[str] = Counter()
    valid: list[dict[str, Any]] = []
    reserve: list[dict[str, Any]] = []
    for item in validated:
        task_class = str(item["truth"]["task_class"])
        if selected_counts[task_class] < required_per_class:
            valid.append(item)
            selected_counts[task_class] += 1
        else:
            reserve.append(item)
    require(len(valid) == int(generation["minimum_valid_scenarios"]), "minimal valid scenario selection changed")
    return {
        "schema": "ownward.dynamic-dataset/v2",
        "valid_scenarios": valid,
        "reserve_scenarios": reserve,
        "rejected_scenarios": rejected,
    }
