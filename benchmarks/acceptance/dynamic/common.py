from __future__ import annotations

from collections import Counter
import hashlib
import json
import math
from pathlib import Path
import random
from typing import Any


DYNAMIC_REPORT_SCHEMA = "ownward.dynamic-acceptance-report/v2"
ABLATION_REPORT_SCHEMA = "ownward.organization-ablation-report/v2"
PROTOCOL_SCHEMA = "ownward.dynamic-acceptance-protocol/v3"
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
CONTENT_SCENARIOS_PER_PARTITION = 4


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


def dataset_implementation_sha256() -> str:
    digest = hashlib.sha256()
    root = Path(__file__).resolve().parent
    for name in ("common.py", "schemas.py", "verify.py", "preflight.py"):
        path = root / name
        require(path.is_file(), f"dynamic dataset implementation is incomplete: {name}")
        encoded = name.encode("utf-8")
        digest.update(len(encoded).to_bytes(8, "little"))
        digest.update(encoded)
        digest.update(bytes.fromhex(sha256(path)))
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


def _seeded_random(random_seed: str) -> random.Random:
    digest = hashlib.sha256(random_seed.encode("utf-8")).digest()
    return random.Random(int.from_bytes(digest, "big"))


def build_hidden_structure(protocol: dict[str, Any], random_seed: str) -> dict[str, Any]:
    generation = protocol["generation"]
    classes = list(generation["task_classes"])
    scenario_count = int(generation["generated_scenarios"])
    node_count = int(generation["information_per_scenario"])
    require(scenario_count % len(classes) == 0, "scenario count must be divisible by task classes")
    require(node_count >= 4, "hidden structure requires at least four nodes per scenario")
    rng = _seeded_random(random_seed)
    task_classes = [value for value in classes for _ in range(scenario_count // len(classes))]
    rng.shuffle(task_classes)
    scopes = list(generation["information_scope"])
    rng.shuffle(scopes)
    variants: Counter[str] = Counter()
    scenarios: list[dict[str, Any]] = []
    for index, task_class in enumerate(task_classes):
        variant = variants[task_class]
        variants[task_class] += 1
        scenario_id = f"s{index + 1:02d}-{rng.getrandbits(32):08x}"
        node_ids = [f"{scenario_id}-n{node + 1}" for node in range(node_count)]
        scenario_scopes = list(dict.fromkeys(scopes[(index * 3 + offset) % len(scopes)] for offset in range(3)))
        roles = ["independent distractor"] * node_count
        updates: list[str] = []
        if task_class == "cross_time":
            roles[:4] = [
                "earlier basis with a unique provenance detail absent from the later conclusion and evidence",
                "later conclusion derived from the basis without restating its unique provenance detail",
                "later evidence supporting the conclusion without restating the basis or conclusion",
                "broader but query-irrelevant category",
            ]
            relations = [(1, "derived_from", 0), (2, "supports", 1)]
            expected, forbidden = [0, 1, 2], [3]
        elif task_class == "multi_hop":
            roles[:4] = [
                "origin fact with a unique cause or observation absent from later nodes",
                "intermediate conclusion derived from the origin without restating that unique cause or observation",
                "downstream fact supported by the intermediate conclusion without restating either earlier fact",
                "query-irrelevant distractor",
            ]
            relations = [(1, "derived_from", 0), (1, "supports", 2), (3, "related_to", 2)]
            expected, forbidden = [0, 1, 2], [3]
        elif task_class == "context_applicability":
            roles[:4] = [
                "fact valid in the uniquely current required context",
                "the only current context in the scenario",
                "plausible competing fact valid only in a clearly non-current alternative context",
                "clearly non-current alternative context",
            ]
            relations = [(0, "applies_in", 1), (2, "applies_in", 3)]
            expected, forbidden = [0, 1], [2, 3]
        elif task_class == "information_update":
            roles[:4] = [
                "outdated fact later replaced by a different current value whose exact specification includes a unique detail absent from all other nodes",
                "evidence supporting only the replacement current value while neither stating nor entailing its unique exact detail",
                "component of only the replacement current fact while neither stating nor entailing its unique exact detail or supporting result",
                "query-irrelevant distractor",
            ]
            relations = [(1, "supports", 0), (2, "part_of", 0), (3, "related_to", 0)]
            expected, forbidden, updates = [0, 1, 2], [3], [node_ids[0]]
        else:
            raise RuntimeError(f"unsupported task class: {task_class}")
        if node_count > 4:
            for node in range(4, node_count):
                roles[node] = (
                    "query-irrelevant item directly associated with the fourth node, "
                    "but not its category, instance, component, evidence, basis, result, context, equivalent, or contradiction"
                )
            relations.extend((node, "related_to", 3) for node in range(4, node_count))
            forbidden.extend(range(4, node_count))
        # Hierarchy is a required batch-level capability. Alternate inverse labels so
        # the frozen contract also exercises canonical direction normalization.
        if task_class == "cross_time":
            if variant % 2 == 0:
                relations.append((3, "broader_than", 0))
            else:
                relations.append((0, "narrower_than", 3))
        scenarios.append(
            {
                "id": scenario_id,
                "task_class": task_class,
                "information_scope": scenario_scopes,
                "nodes": [{"id": node_id, "role": roles[node]} for node, node_id in enumerate(node_ids)],
                "relations": [
                    {"source_id": node_ids[source], "type": relation_type, "target_id": node_ids[target]}
                    for source, relation_type, target in relations
                ],
                "update_node_ids": updates,
                "query": {
                    "expected_ids": [node_ids[value] for value in expected],
                    "forbidden_ids": [node_ids[value] for value in forbidden],
                },
            }
        )
    return {
        "schema": "ownward.dynamic-hidden-structure/v1",
        "seed_sha256": hashlib.sha256(random_seed.encode("utf-8")).hexdigest(),
        "scenarios": scenarios,
    }


def generation_prompt(protocol: dict[str, Any], random_seed: str, structure: dict[str, Any] | None = None) -> str:
    structure = structure or build_hidden_structure(protocol, random_seed)
    relation_contract = protocol["relation_contract"]
    return f"""Create only the open-domain semantic facts for a one-time, post-freeze acceptance run of a general personal-information system.

Random source: {random_seed}
Canonical relation contract: {json.dumps(relation_contract, ensure_ascii=False, separators=(',', ':'))}
Frozen structural skeleton: {json.dumps(structure, ensure_ascii=False, separators=(',', ':'))}

The structure, identities, relation topology, update targets, query dependencies, and scopes are already frozen by deterministic code. Return natural-language facts and one question only for the exact scenario and node keys required by the response schema. Do not emit, reinterpret, or change structural fields. Invent unrelated domains, cultures, activities, technologies, time periods, names, and wording; do not reuse common benchmark examples or Ownward examples.

For each node, create concise atomic facts that exactly satisfy its frozen role and every incident canonical relation, including source-to-target direction. The relation must be unambiguously inferable from the node texts themselves; do not rely on hidden labels to supply missing meaning. In particular, an applies_in source fact must identify the target context without repeating the current-context fact that the context node contributes to the answer, and a supports or derived_from pair must make only the frozen, most precise type defensible in its required direction. A related_to pair must express a direct association for which none of the other canonical types is defensible: neither endpoint may be written as the other's category, instance, component, evidence, basis, result, context, equivalent, or contradiction. Make each node's first fact unique within its scenario. Every expected node must contribute an independently requested answer detail absent from and not entailed by every other node, so no proper subset of the expected nodes can fully answer the question. A later conclusion may name the subject of its earlier basis but must not repeat the basis's unique provenance, cause, or observation; later evidence may name what it supports but must not restate either earlier answer detail. In information_update scenarios, the initial update-target node must state only the old value, its replacement must state only a genuinely different new current value with one exact distinguishing detail found nowhere else, and no replacement fact may repeat any initial fact. Supporting evidence and components may use a short unique name to identify the replacement subject and make their relations inferable, but they must neither state nor entail the replacement's distinguishing detail and must each contribute a different requested answer fact. In context_applicability scenarios, only the required-context node may describe the user's current situation; the applicable-fact node may use a short context label but must not restate that current situation, while the incompatible-context node must explicitly be past, hypothetical, another person's, or otherwise non-current and its competing fact must remain plausible only there. Multi-hop facts must make the origin, intermediate conclusion, and downstream result independently necessary rather than provide a direct shortcut.

Write one natural question per scenario that requires every expected node and no forbidden node. Make the requested evidence explicit according to the task class: cross_time asks separately for the earlier basis including its unique provenance, cause, or observation, the later conclusion or decision derived from it, and the later supporting result; multi_hop asks separately for the unique origin observation, the intermediate conclusion, and the downstream supported fact; context_applicability asks for both the current context and the fact or practice that applies in it; information_update asks for the exact current specification including its unique distinguishing detail, its separate supporting result, and its separate required component. Before returning, verify that a complete answer must state the first current fact of every expected node rather than merely implying one of them. Do not copy an answer sentence into the question. Do not mention the product, benchmark, graph, relation labels, IDs, scopes, or implementation."""


def assemble_hidden_world(structure: dict[str, Any], content: dict[str, Any]) -> dict[str, Any]:
    require(structure.get("schema") == "ownward.dynamic-hidden-structure/v1", "hidden structure schema changed")
    generated = content.get("scenarios")
    require(isinstance(generated, dict), "hidden semantic content has no scenarios")
    expected_scenarios = {str(value["id"]): value for value in structure["scenarios"]}
    require(set(generated) == set(expected_scenarios), "hidden semantic content changed scenario identities")
    scenarios: list[dict[str, Any]] = []
    for scenario_id, structural in expected_scenarios.items():
        semantic = generated[scenario_id]
        require(isinstance(semantic, dict), f"scenario {scenario_id} semantic content is invalid")
        node_content = semantic.get("nodes")
        update_content = semantic.get("updates")
        require(isinstance(node_content, dict) and isinstance(update_content, dict), f"scenario {scenario_id} semantic content is incomplete")
        node_ids = [str(value["id"]) for value in structural["nodes"]]
        update_ids = [str(value) for value in structural["update_node_ids"]]
        require(set(node_content) == set(node_ids), f"scenario {scenario_id} semantic content changed node identities")
        require(set(update_content) == set(update_ids), f"scenario {scenario_id} semantic content changed update identities")
        nodes: list[dict[str, Any]] = []
        first_facts: dict[str, str] = {}
        all_facts: list[str] = []
        for node_id in node_ids:
            facts = node_content[node_id]
            require(isinstance(facts, list) and facts and all(str(value).strip() for value in facts), f"scenario {scenario_id} has an empty fact")
            normalized = [str(value).strip() for value in facts]
            first_facts[node_id] = normalized[0]
            all_facts.extend(normalized)
            nodes.append({"id": node_id, "facts": normalized})
        require(len(all_facts) == len(set(all_facts)), f"scenario {scenario_id} repeats hidden facts")
        updates: list[dict[str, Any]] = []
        current_first_facts = dict(first_facts)
        for node_id in update_ids:
            facts = update_content[node_id]
            require(isinstance(facts, list) and facts and all(str(value).strip() for value in facts), f"scenario {scenario_id} has an empty update")
            normalized = [str(value).strip() for value in facts]
            require(not set(normalized) & set(all_facts), f"scenario {scenario_id} update does not establish new truth")
            current_first_facts[node_id] = normalized[0]
            updates.append({"node_id": node_id, "replacement_facts": normalized})
        expected_ids = [str(value) for value in structural["query"]["expected_ids"]]
        scenarios.append(
            {
                "id": scenario_id,
                "task_class": structural["task_class"],
                "information_scope": structural["information_scope"],
                "nodes": nodes,
                "relations": structural["relations"],
                "updates": updates,
                "query": {
                    "intent": f"Retrieve the current facts jointly supported by {len(expected_ids)} required information items.",
                    "expected_ids": expected_ids,
                    "forbidden_ids": structural["query"]["forbidden_ids"],
                    "answer_facts": [current_first_facts[node_id] for node_id in expected_ids],
                },
            }
        )
    return {"scenarios": scenarios}


def build_natural_expression(hidden: dict[str, Any], content: dict[str, Any]) -> dict[str, Any]:
    scenarios = hidden.get("scenarios")
    generated = content.get("scenarios")
    require(isinstance(scenarios, list) and isinstance(generated, dict), "natural expression inputs are incomplete")
    expressed: list[dict[str, Any]] = []
    for scenario in scenarios:
        scenario_id = str(scenario["id"])
        semantic = generated.get(scenario_id)
        require(isinstance(semantic, dict), f"scenario {scenario_id} has no generated expression")
        question = str(semantic.get("question", "")).strip()
        require(question, f"scenario {scenario_id} has no natural-language question")
        information = [
            {"node_id": str(node["id"]), "content": " ".join(str(value) for value in node["facts"])}
            for node in scenario["nodes"]
        ]
        updates = [
            {"node_id": str(update["node_id"]), "content": " ".join(str(value) for value in update["replacement_facts"])}
            for update in scenario["updates"]
        ]
        for fact in scenario["query"]["answer_facts"]:
            require(str(fact) not in question, f"scenario {scenario_id} leaks an answer fact in the question")
        expressed.append({"id": scenario_id, "information": information, "updates": updates, "query": {"question": question}})
    return {"scenarios": expressed}


def _scenario_partitions(
    hidden: dict[str, Any], protocol: dict[str, Any], maximum_scenarios: int
) -> list[dict[str, Any]]:
    scenarios = hidden.get("scenarios")
    require(isinstance(scenarios, list), "hidden world has no scenarios")
    partitions: list[dict[str, Any]] = []
    for task_class in protocol["generation"]["task_classes"]:
        task_scenarios = [value for value in scenarios if value.get("task_class") == task_class]
        require(bool(task_scenarios), f"validation task class is empty: {task_class}")
        for offset in range(0, len(task_scenarios), maximum_scenarios):
            sequence = offset // maximum_scenarios + 1
            partitions.append(
                {
                    "id": f"{task_class}_{sequence:02d}",
                    "task_class": task_class,
                    "hidden": {"scenarios": task_scenarios[offset : offset + maximum_scenarios]},
                }
            )
    require(
        sum(len(value["hidden"]["scenarios"]) for value in partitions) == len(scenarios),
        "validation partitions do not cover the hidden world",
    )
    return partitions


def content_partitions(hidden: dict[str, Any], protocol: dict[str, Any]) -> list[dict[str, Any]]:
    return _scenario_partitions(hidden, protocol, CONTENT_SCENARIOS_PER_PARTITION)


def validation_partitions(hidden: dict[str, Any], protocol: dict[str, Any]) -> list[dict[str, Any]]:
    return _scenario_partitions(hidden, protocol, int(protocol["generation"]["validation_scenarios_per_batch"]))


def expression_prompt(hidden: dict[str, Any], protocol: dict[str, Any]) -> str:
    return f"""Turn each hidden semantic scenario below into natural-language personal information and one natural-language question.

Preserve every scenario ID and node ID. Express all hidden facts faithfully, but vary language, sentence structure, chronology, domain vocabulary, and explicitness. Do not state relation labels, graph structure, expected IDs, forbidden IDs, information-scope labels, or that this is a test. Each initial node gets exactly one content string. Each hidden update gets exactly one replacement content string for the same node. The question must be answerable only from the intended facts, and answer facts must appear verbatim somewhere in the relevant current information. Preserve the canonical relation meanings and directions below without making any expected relation equally defensible as another relation type. Do not add facts that create unintended relations or make a forbidden contextual item valid.

Canonical relation contract:
{json.dumps(protocol["relation_contract"], ensure_ascii=False, separators=(',', ':'))}

Hidden truth:
{json.dumps(hidden, ensure_ascii=False, separators=(',', ':'))}"""


def validation_prompt(hidden: dict[str, Any], expression: dict[str, Any], protocol: dict[str, Any]) -> str:
    return f"""Independently validate generated natural-language information against its hidden semantic truth. You are a data validator, not the system under test.

For every scenario, mark valid only if all facts, canonical relation types and directions, context applicability, update replacement, expected query evidence, forbidden evidence, and exact answer facts are unambiguously expressed; the question must have one defensible answer from the current information. Each expected node must contribute unique information required by the question: reject the scenario if any proper subset of expected nodes semantically answers the complete question, even when wording differs. Forbidden nodes are deliberate distractors that must remain present in the scenario's information; "forbidden" means they must not be needed for, included in, or made applicable to the answer, not that their text must be absent. Reject a scenario when its text supports a different canonical relation, more than one equally defensible canonical relation, either direction, missing evidence, duplicated expected evidence, accidental extra relations, answer leakage in the question, or any changed meaning. Preserve every scenario ID and provide concise issues. Evaluate all scenarios without selecting based on any product result; no product output exists yet.

Canonical relation contract:
{json.dumps(protocol["relation_contract"], ensure_ascii=False, separators=(',', ':'))}

Hidden truth:
{json.dumps(hidden, ensure_ascii=False, separators=(',', ':'))}

Natural-language expression:
{json.dumps(expression, ensure_ascii=False, separators=(',', ':'))}"""


def agent_prompt(questions: list[dict[str, str]], tool_calls_per_query: int) -> str:
    total_tool_call_budget = len(questions) * tool_calls_per_query
    return f"""Use only the connected Ownward tools; do not use shell, file, web, or any other tools. First obtain Ownward's collaboration rules. Answer every question below from Ownward. The complete session may use at most {total_tool_call_budget} Ownward tool calls, including the rules call, with no more than {tool_calls_per_query} calls allocated to any question. For simple questions search once when sufficient; for complex questions use accumulated evidence and relation navigation only until the exact supporting facts and IDs are complete. Do not inspect unrelated candidates after the required evidence is identified. Never guess.

For each query_id return the exact answer facts as they appear in current information and the stable Ownward information IDs that jointly support the answer. Do not include unrelated IDs.

Questions:
{json.dumps(questions, ensure_ascii=False, separators=(',', ':'))}"""


def validate_protocol(protocol: dict[str, Any]) -> None:
    require(protocol.get("schema") == PROTOCOL_SCHEMA, "unsupported dynamic protocol")
    generation = protocol.get("generation")
    statistics = protocol.get("statistics")
    execution = protocol.get("execution")
    relation_contract = protocol.get("relation_contract")
    models = protocol.get("models")
    runtime = protocol.get("runtime")
    product_runtime = protocol.get("product_runtime")
    require(isinstance(generation, dict) and isinstance(statistics, dict), "protocol is incomplete")
    require(isinstance(execution, dict) and isinstance(models, dict), "protocol execution contract or models are missing")
    require(isinstance(relation_contract, dict), "canonical relation contract is missing")
    relation_definitions = relation_contract.get("types")
    require(
        isinstance(relation_definitions, dict)
        and set(relation_definitions) == RELATION_TYPES
        and all(str(value).strip() for value in relation_definitions.values()),
        "canonical relation definitions are incomplete",
    )
    require("source_id" in str(relation_contract.get("direction", "")) and "target_id" in str(relation_contract.get("direction", "")), "canonical relation direction is missing")
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
    validation_batch = int(generation.get("validation_scenarios_per_batch", 0))
    require(generated >= minimum_valid >= minimum_per_class * len(classes) > 0, "scenario counts are inconsistent")
    require(generated % len(classes) == 0, "scenario count must be divisible by task classes")
    require(
        0 < validation_batch <= generated // len(classes),
        "validation batch size must fit the generated pool for each task class",
    )
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
        "ownward_operation_stall_seconds",
        "semantic_stage_seconds_max",
        "agent_seconds_per_question_max",
        "agent_tool_calls_per_query",
        "dataset_parallelism",
    ):
        require(float(execution.get(name, 0)) > 0, f"invalid dynamic execution value: {name}")
    require(float(execution["inspection_operation_stall_seconds"]) <= float(execution["ownward_operation_stall_seconds"]), "inspection timeout must not exceed the Ownward operation boundary")
    require(int(execution.get("dataset_parallelism", 0)) > 0, "dataset parallelism must be positive")
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
    require(set(validation_by_id) <= set(hidden_scenarios), "validator introduced an unknown scenario")
    validated: list[dict[str, Any]] = []
    rejected: list[dict[str, Any]] = []
    class_counts: Counter[str] = Counter()
    for scenario_id, verdict in validation_by_id.items():
        truth = hidden_scenarios[scenario_id]
        text = expression_by_id[scenario_id]
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
