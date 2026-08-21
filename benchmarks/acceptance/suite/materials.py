from __future__ import annotations

import hashlib
import json
import math
from collections import Counter
from pathlib import Path
from typing import Any


class MaterialsError(ValueError):
    pass


def load_json(path: Path) -> dict[str, Any]:
    value = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(value, dict):
        raise MaterialsError(f"{path} 必须包含 JSON 对象")
    return value


def validate_materials(root: Path) -> dict[str, Any]:
    manifest = load_json(root / "manifest.json")
    _require(manifest.get("schema") == "ownward.acceptance-materials-manifest/v1", "材料清单 schema 无效")
    _require(manifest.get("suite_version") == "1.0.0", "材料清单体系版本无效")
    files = manifest.get("files")
    _require(isinstance(files, list) and len(files) == 5, "材料清单必须固定五份材料")
    for item in files:
        _require(isinstance(item, dict), "材料清单项必须是对象")
        path = Path(str(item.get("path", "")))
        if not path.is_absolute():
            candidates = [Path.cwd() / path, root.parents[3] / path]
            path = next((candidate for candidate in candidates if candidate.is_file()), candidates[0])
        _require(path.is_file(), f"材料不存在: {item.get('path')}")
        actual = hashlib.sha256(path.read_bytes()).hexdigest()
        _require(actual == item.get("sha256"), f"材料摘要不匹配: {item.get('path')}")

    core = load_json(root / "core" / "v1" / "dataset.json")
    _require(core.get("schema") == "ownward.core-frontier-materials/v1", "内核材料 schema 无效")
    assets = core.get("assets")
    _require(isinstance(assets, list) and len(assets) >= 24, "内核材料规模不足")
    _require(all(isinstance(item, dict) for item in assets), "内核资产材料无效")
    asset_ids = [item.get("fixture_id") for item in assets]
    _require(
        all(isinstance(identifier, str) and identifier for identifier in asset_ids)
        and len(asset_ids) == len(set(asset_ids)),
        "内核材料存在空白或重复身份",
    )
    _require(
        all(isinstance(item.get("content"), str) and item["content"].strip() and _valid_contexts(item.get("contexts", [])) for item in assets),
        "内核资产内容或场景无效",
    )
    _require(core.get("scales") == [10, 20, len(assets)], "内核材料多规模定义无效")
    embeddings = core.get("frozen_embeddings")
    _require(isinstance(embeddings, dict) and set(embeddings) == set(asset_ids), "冻结向量没有覆盖全部资产")
    _require(all(_valid_vector(vector) for vector in embeddings.values()), "冻结资产向量无效")
    queries = core.get("queries")
    _require(isinstance(queries, list) and queries and all(isinstance(item, dict) for item in queries), "内核查询材料无效")
    query_ids = [item.get("query_id") for item in queries]
    _require(
        all(isinstance(identifier, str) and identifier for identifier in query_ids)
        and len(query_ids) == len(set(query_ids)),
        "内核查询身份空白或重复",
    )
    allowed_query_types = {"explicit_object", "semantic_intent", "relation_constraint", "context_applicability"}
    for query in queries:
        identifier = str(query["query_id"])
        expected = query.get("expected_ids")
        forbidden = query.get("forbidden_ids", [])
        relation_path = query.get("required_relation_path", [])
        _require(query.get("type") in allowed_query_types and isinstance(query.get("query"), str) and query["query"].strip(), f"查询 {identifier} 的类型或文本无效")
        _require(isinstance(expected, list) and expected and len(expected) == len(set(expected)) and set(expected) <= set(asset_ids), f"查询 {identifier} 的期望身份无效")
        _require(isinstance(forbidden, list) and len(forbidden) == len(set(forbidden)) and not set(expected) & set(forbidden) and set(forbidden) <= set(asset_ids), f"查询 {identifier} 的排除身份无效")
        _require(isinstance(relation_path, list) and len(relation_path) == len(set(relation_path)) and set(relation_path) <= set(expected), f"查询 {identifier} 的关系路径无效")
        _require(_valid_contexts(query.get("contexts", [])), f"查询 {identifier} 的场景无效")
    query_embeddings = core.get("frozen_query_embeddings")
    _require(isinstance(query_embeddings, dict) and set(query_embeddings) == set(query_ids), "冻结查询向量没有覆盖全部查询")
    _require(all(_valid_vector(vector) for vector in query_embeddings.values()), "冻结查询向量无效")
    semantics = core.get("frozen_semantics")
    _require(isinstance(semantics, dict), "冻结语义材料无效")
    semantic_contexts = semantics.get("contexts")
    _require(isinstance(semantic_contexts, dict) and set(semantic_contexts) == set(asset_ids), "冻结场景没有覆盖全部资产")
    _require(all(_valid_contexts(contexts) for contexts in semantic_contexts.values()), "冻结场景内容无效")
    relations = semantics.get("relations")
    truth = core.get("truth")
    _require(isinstance(relations, list) and isinstance(truth, dict) and truth.get("relations") == relations, "冻结关系与关系真值不一致")
    relation_keys: set[tuple[str, str, str]] = set()
    for relation in relations:
        _require(isinstance(relation, dict), "冻结关系无效")
        key = (str(relation.get("source_id", "")), str(relation.get("type", "")), str(relation.get("target_id", "")))
        _require(key[0] in asset_ids and key[2] in asset_ids and key[0] != key[2] and key[1] and key not in relation_keys, "冻结关系端点或身份无效")
        relation_keys.add(key)
    changes = core.get("change_sequences")
    _require(isinstance(changes, list) and changes and all(isinstance(item, dict) for item in changes), "增量变更材料无效")
    change_ids = [item.get("fixture_id") for item in changes]
    _require(len(change_ids) == len(set(change_ids)) and set(change_ids) <= set(asset_ids), "增量变更身份无效")
    original = {item["fixture_id"]: item["content"] for item in assets}
    _require(all(isinstance(item.get("content"), str) and item["content"].strip() and item["content"] != original[item["fixture_id"]] and _valid_contexts(item.get("contexts", [])) for item in changes), "增量变更内容无效")
    variants = core.get("deterministic_variants", {})
    _require(isinstance(variants.get("seed"), int) and variants.get("count", 0) > 1, "确定性变体未冻结")
    _require(
        variants.get("operations") == [
            "stable_identity_remap", "ingestion_order_permutation", "timestamp_translation",
            "context_order_permutation", "irrelevant_item_injection",
        ],
        "确定性变体操作无效",
    )

    product = load_json(root / "product" / "v1" / "dataset.json")
    scenarios = product.get("scenarios")
    _require(product.get("frozen") is True and isinstance(scenarios, list), "专项数据集未冻结")
    _require(len(scenarios) == 24, "专项数据集必须包含 24 个场景")
    counts: Counter[str] = Counter()
    scenario_ids: set[str] = set()
    information_total = 0
    for scenario in scenarios:
        truth = _mapping(scenario, "truth")
        expression = _mapping(scenario, "expression")
        scenario_id = truth.get("id")
        category = truth.get("task_class")
        information = expression.get("information")
        _require(isinstance(scenario_id, str) and scenario_id not in scenario_ids, "专项场景身份无效")
        _require(expression.get("id") == scenario_id, f"场景 {scenario_id} 的表达身份无效")
        _require(isinstance(information, list) and len(information) == 5, f"场景 {scenario_id} 必须包含五条信息")
        node_items = truth.get("nodes")
        _require(isinstance(node_items, list) and len(node_items) == 5, f"场景 {scenario_id} 必须包含五个真值节点")
        nodes = {item.get("id") for item in node_items if isinstance(item, dict)}
        expressed = {item.get("node_id") for item in information if isinstance(item, dict)}
        _require(len(nodes) == 5 and len(expressed) == 5, f"场景 {scenario_id} 的信息身份重复")
        _require(nodes == expressed, f"场景 {scenario_id} 的真值与表达信息不一致")
        facts_by_node: dict[str, list[str]] = {}
        for item in node_items:
            facts = item.get("facts")
            _require(isinstance(facts, list) and facts and all(isinstance(value, str) and value for value in facts), f"场景 {scenario_id} 的节点事实无效")
            facts_by_node[str(item["id"])] = facts
        for item in information:
            node_id = str(item.get("node_id"))
            _require(item.get("content") in facts_by_node[node_id], f"场景 {scenario_id} 的输入表达与真值事实不一致")
        relations = truth.get("relations")
        _require(isinstance(relations, list) and relations, f"场景 {scenario_id} 缺少关系真值")
        relation_keys: set[tuple[str, str, str]] = set()
        for relation in relations:
            _require(isinstance(relation, dict), f"场景 {scenario_id} 的关系真值无效")
            key = (str(relation.get("source_id")), str(relation.get("type")), str(relation.get("target_id")))
            _require(key[0] in nodes and key[2] in nodes and key[0] != key[2] and key[1], f"场景 {scenario_id} 的关系端点无效")
            _require(key not in relation_keys, f"场景 {scenario_id} 的关系真值重复")
            relation_keys.add(key)
        truth_updates = truth.get("updates")
        expression_updates = expression.get("updates")
        _require(isinstance(truth_updates, list) and isinstance(expression_updates, list), f"场景 {scenario_id} 的更新无效")
        truth_update_map = {str(item.get("node_id")): item.get("replacement_facts") for item in truth_updates if isinstance(item, dict)}
        expression_update_map = {str(item.get("node_id")): item.get("content") for item in expression_updates if isinstance(item, dict)}
        _require(len(truth_update_map) == len(truth_updates) == len(expression_update_map) == len(expression_updates), f"场景 {scenario_id} 的更新身份重复")
        _require(set(truth_update_map) == set(expression_update_map) <= nodes, f"场景 {scenario_id} 的更新身份不一致")
        for node_id, replacements in truth_update_map.items():
            _require(isinstance(replacements, list) and replacements and expression_update_map[node_id] in replacements, f"场景 {scenario_id} 的更新表达与真值不一致")
        query = _mapping(truth, "query")
        expected = set(query.get("expected_ids", []))
        forbidden = set(query.get("forbidden_ids", []))
        _require(expected and not expected & forbidden and expected | forbidden == nodes, f"场景 {scenario_id} 的查询真值无效")
        answer_facts = query.get("answer_facts")
        current_facts = {
            fact
            for node_id in expected
            for fact in (truth_update_map.get(node_id) or facts_by_node[node_id])
        }
        _require(isinstance(answer_facts, list) and set(answer_facts) == current_facts and len(answer_facts) == len(current_facts), f"场景 {scenario_id} 的答案真值不完整")
        question = _mapping(expression, "query").get("question")
        _require(isinstance(question, str) and question.strip(), f"场景 {scenario_id} 的自然语言查询无效")
        scenario_ids.add(scenario_id)
        counts[str(category)] += 1
        information_total += len(information)
    expected_counts = {name: 6 for name in ("cross_time", "multi_hop", "context_applicability", "information_update")}
    _require(dict(counts) == expected_counts, "专项数据集能力分布无效")
    _require(information_total == 120, "专项数据集必须包含 120 条信息")

    qualification = load_json(root / "product" / "v1" / "qualification.json")
    qualification_ids = qualification.get("scenario_ids")
    _require(isinstance(qualification_ids, list) and len(qualification_ids) == len(set(qualification_ids)) == 8, "资格集必须包含八个不同场景")
    _require(set(qualification_ids) <= scenario_ids, "资格集引用未知场景")
    qualification_counts = Counter(
        _scenario_category(scenarios, identifier) for identifier in qualification_ids
    )
    _require(all(qualification_counts[name] == 2 for name in expected_counts), "资格集必须每类选择两个场景")

    review = load_json(root / "product" / "v1" / "review.json")
    reviewed = review.get("reviewed_scenarios")
    _require(isinstance(reviewed, list) and len(reviewed) == 24, "冷审结果必须覆盖全部场景")
    _require({item.get("id") for item in reviewed if isinstance(item, dict)} == scenario_ids, "冷审结果身份不完整")
    _require(all(item.get("valid") is True and not item.get("issues") for item in reviewed), "固定数据包含未通过冷审的场景")

    calibration = load_json(root / "frontier" / "v1" / "calibration.json")
    _require(calibration.get("status") in {"comparable", "incomparable"}, "外部前沿校准状态无效")
    if calibration.get("status") == "incomparable":
        _require(bool(calibration.get("conclusion")), "不可比结论必须说明边界")
    return manifest


def _scenario_category(scenarios: list[dict[str, Any]], identifier: str) -> str:
    for scenario in scenarios:
        truth = _mapping(scenario, "truth")
        if truth.get("id") == identifier:
            return str(truth.get("task_class"))
    raise MaterialsError(f"资格集引用未知场景: {identifier}")


def _mapping(value: dict[str, Any], name: str) -> dict[str, Any]:
    nested = value.get(name)
    _require(isinstance(nested, dict), f"{name} 必须是对象")
    return nested


def _valid_vector(value: Any) -> bool:
    return (
        isinstance(value, list)
        and len(value) == 16
        and all(isinstance(item, (int, float)) and math.isfinite(float(item)) for item in value)
    )


def _valid_contexts(value: Any) -> bool:
    if not isinstance(value, list):
        return False
    keys: set[tuple[str, str]] = set()
    for item in value:
        if not isinstance(item, dict):
            return False
        key = (str(item.get("key", "")).strip(), str(item.get("value", "")).strip())
        if not key[0] or not key[1] or key in keys:
            return False
        keys.add(key)
    return True


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise MaterialsError(message)
