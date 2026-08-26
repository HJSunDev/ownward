from __future__ import annotations

import copy
import hashlib
import json
import math
from collections import Counter
from pathlib import Path
from typing import Any


class MaterialsError(ValueError):
    pass


ACTIVE_PRODUCT_VERSION = "ownward-product-dataset/v2"
ACTIVE_PRODUCT_DIRECTORY = "v2"
PRODUCT_V1_DIGESTS = {
    "dataset_sha256": "818da669d9f168347dea9ac40c7cc8b8432e2c0f648353aae78e345d4628268a",
    "qualification_sha256": "4292aff3235da76adae55ccda055d7fdf22c05ab6cb3bdd18a5ce19cb9f4a47b",
    "review_sha256": "a6652bb79c1b3b3fefca42d853bc0d0a5e63f54df4f086db634db592a86b3547",
}
S27_V2_QUESTION = (
    "What earlier incident occurred, including its specific circumstance and the realization I formed then; "
    "what distinct practice did I adopt afterward for tense group situations; and what later result supports "
    "that adopted practice?"
)


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
    expected_manifest_paths = {
        "benchmarks/acceptance/suite/materials/core/v1/dataset.json",
        "benchmarks/acceptance/suite/materials/product/v1/dataset.json",
        "benchmarks/acceptance/suite/materials/product/v1/qualification.json",
        "benchmarks/acceptance/suite/materials/product/v1/review.json",
        "benchmarks/acceptance/suite/materials/product/v2/dataset.json",
        "benchmarks/acceptance/suite/materials/product/v2/qualification.json",
        "benchmarks/acceptance/suite/materials/product/v2/review.json",
        "benchmarks/acceptance/suite/materials/frontier/v1/calibration.json",
        "benchmarks/acceptance/suite/materials/optimization/v1/direction-organization-granularity.json",
        "benchmarks/acceptance/suite/materials/optimization/v1/direction-semantic-representation.json",
        "benchmarks/acceptance/suite/materials/optimization/v1/direction-budget-selection.json",
        "benchmarks/acceptance/suite/materials/optimization/v1/direction-storage-deposition.json",
    }
    _require(isinstance(files, list) and len(files) == len(expected_manifest_paths), "材料清单规模无效")
    _require(
        {str(item.get("path")) for item in files if isinstance(item, dict)} == expected_manifest_paths,
        "材料清单没有同时且仅封存历史 v1 与活动 v2",
    )
    for item in files:
        _require(isinstance(item, dict), "材料清单项必须是对象")
        path = Path(str(item.get("path", "")))
        if not path.is_absolute():
            candidates = [Path.cwd() / path, root.parents[3] / path]
            path = next((candidate for candidate in candidates if candidate.is_file()), candidates[0])
        _require(path.is_file(), f"材料不存在: {item.get('path')}")
        actual = hashlib.sha256(path.read_bytes()).hexdigest()
        _require(actual == item.get("sha256"), f"材料摘要不匹配: {item.get('path')}")

    historical_root = root / "product" / "v1"
    for name, filename in (("dataset_sha256", "dataset.json"), ("qualification_sha256", "qualification.json"), ("review_sha256", "review.json")):
        actual = hashlib.sha256((historical_root / filename).read_bytes()).hexdigest()
        _require(actual == PRODUCT_V1_DIGESTS[name], f"历史 product v1 材料发生漂移: {filename}")

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

    _validate_optimization_view(root / "optimization" / "v1" / "direction-organization-granularity.json")
    _validate_semantic_representation_view(root / "optimization" / "v1" / "direction-semantic-representation.json")
    _validate_budget_selection_view(root / "optimization" / "v1" / "direction-budget-selection.json")
    _validate_storage_deposition_view(root / "optimization" / "v1" / "direction-storage-deposition.json")

    historical_product = load_json(historical_root / "dataset.json")
    product_root = root / "product" / ACTIVE_PRODUCT_DIRECTORY
    product = load_json(product_root / "dataset.json")
    _require(historical_product.get("version") == "ownward-product-dataset/v1", "历史专项数据集版本无效")
    _require(product.get("schema") == historical_product.get("schema") == "ownward.product-dataset/v1", "专项数据集 schema 无效")
    _require(product.get("version") == ACTIVE_PRODUCT_VERSION, "活动专项数据集版本无效")
    historical_comparable = copy.deepcopy(historical_product)
    active_comparable = copy.deepcopy(product)
    historical_comparable["version"] = ACTIVE_PRODUCT_VERSION
    historical_s27 = _scenario(historical_comparable["scenarios"], "s27-68857f46")
    active_s27 = _scenario(active_comparable["scenarios"], "s27-68857f46")
    active_question = _mapping(_mapping(active_s27, "expression"), "query").get("question")
    _require(active_question == S27_V2_QUESTION, "product v2 的 s27 问题未按冷审文本冻结")
    _mapping(_mapping(active_s27, "expression"), "query")["question"] = _mapping(
        _mapping(historical_s27, "expression"), "query",
    )["question"]
    _require(active_comparable == historical_comparable, "product v2 包含版本身份与 s27 问题之外的语义变化")
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

    historical_qualification = load_json(historical_root / "qualification.json")
    qualification = load_json(product_root / "qualification.json")
    _require(qualification.get("schema") == historical_qualification.get("schema") == "ownward.product-qualification/v1", "专项资格集 schema 无效")
    _require(qualification.get("dataset_version") == ACTIVE_PRODUCT_VERSION, "活动专项资格集版本无效")
    comparable_qualification = copy.deepcopy(qualification)
    comparable_qualification["dataset_version"] = historical_qualification.get("dataset_version")
    _require(comparable_qualification == historical_qualification, "product v2 资格集选择发生变化")
    qualification_ids = qualification.get("scenario_ids")
    _require(isinstance(qualification_ids, list) and len(qualification_ids) == len(set(qualification_ids)) == 8, "资格集必须包含八个不同场景")
    _require(set(qualification_ids) <= scenario_ids, "资格集引用未知场景")
    qualification_counts = Counter(
        _scenario_category(scenarios, identifier) for identifier in qualification_ids
    )
    _require(all(qualification_counts[name] == 2 for name in expected_counts), "资格集必须每类选择两个场景")

    historical_review = load_json(historical_root / "review.json")
    review = load_json(product_root / "review.json")
    _require(review.get("schema") == historical_review.get("schema") == "ownward.product-dataset-review/v1", "专项冷审 schema 无效")
    _require(review.get("dataset_version") == ACTIVE_PRODUCT_VERSION, "活动专项冷审版本无效")
    reviewed = review.get("reviewed_scenarios")
    _require(isinstance(reviewed, list) and len(reviewed) == 24, "冷审结果必须覆盖全部场景")
    _require({item.get("id") for item in reviewed if isinstance(item, dict)} == scenario_ids, "冷审结果身份不完整")
    _require(all(item.get("valid") is True and not item.get("issues") for item in reviewed), "固定数据包含未通过冷审的场景")
    _require(reviewed == historical_review.get("reviewed_scenarios"), "product v2 未原样继承 24 个场景的有效性结论")
    review_source = _mapping(review, "source")
    _require(review_source.get("kind") == "minimal-version-correction", "product v2 冷审来源无效")
    _require(review_source.get("base_dataset_version") == "ownward-product-dataset/v1", "product v2 冷审基线版本无效")
    _require(review_source.get("base_files") == PRODUCT_V1_DIGESTS, "product v2 冷审未绑定原始 v1 摘要")
    inherited = _mapping(review_source, "inherited_review")
    fresh = _mapping(review_source, "fresh_review")
    _require(inherited.get("scenario_count") == 23 and inherited.get("rule"), "product v2 未记录 23 个继承冷审场景")
    _require(fresh.get("scenario_id") == "s27-68857f46" and fresh.get("changed_field") == "expression.query.question" and fresh.get("method"), "product v2 未记录 s27 新冷审来源")
    adversarial = _mapping(review, "adversarial_review")
    checks = adversarial.get("checks")
    _require(
        adversarial.get("scenario_id") == "s27-68857f46"
        and adversarial.get("valid") is True
        and isinstance(checks, list)
        and len(checks) == 3
        and all(isinstance(item, dict) and item.get("passed") is True and item.get("challenge") and item.get("finding") for item in checks)
        and bool(adversarial.get("conclusion")),
        "product v2 的 s27 最强反方冷审不完整",
    )

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


def _validate_optimization_view(path: Path) -> None:
    view = load_json(path)
    _require(view.get("schema") == "ownward.core-frontier-materials/v1", "内核优化视图 schema 无效")
    _require(view.get("version") == "ownward-kernel-optimization-view/v1", "内核优化视图版本无效")
    contract = _mapping(view, "optimization_view")
    _require(contract.get("id") == "organization-granularity-v1", "内核优化视图身份无效")
    _require(contract.get("direction") == "information_representation_and_organization", "内核优化视图方向无效")
    source = _mapping(contract, "formal_source")
    _require(source.get("candidate") == "99f519018df99bd5202b0c571b8e43481cd1b80e", "优化视图未绑定 V0")
    _require(source.get("formal_errors") == 258 and source.get("mapped_cluster_errors") == 119, "优化视图正式失败规模无效")
    _require(all(isinstance(source.get(name), str) and len(source[name]) == 64 for name in ("diagnostics_sha256", "report_sha256")), "优化视图正式证据摘要无效")
    scoring = _mapping(contract, "scoring")
    _require(scoring.get("context_budget_chars") == 24000 and scoring.get("read_limit") == 8, "优化视图预算口径漂移")
    baseline = _mapping(contract, "v0_baseline")
    gate = _mapping(contract, "frozen_gate")
    _require(math.isclose(float(baseline.get("required_evidence_budget_recall", -1)), 7 / 18), "优化视图 V0 召回基线漂移")
    _require(math.isclose(float(baseline.get("required_evidence_budget_error_rate", -1)), 11 / 18), "优化视图 V0 错误基线漂移")
    _require(math.isclose(float(gate.get("required_evidence_budget_recall_min", -1)), 7 / 9), "优化视图召回门槛漂移")
    _require(math.isclose(float(gate.get("required_evidence_budget_error_rate_max", -1)), 11 / 36), "优化视图错误门槛漂移")
    _require(float(gate.get("scale_evidence_recall_min", -1)) == 1.0, "优化视图长资产召回门槛漂移")
    _require(gate.get("query_workflow_relative_gate") == "candidate_not_above_paired_v0_plus_combined_repeatability_error", "优化视图查询相对成本门槛漂移")
    _require(float(gate.get("query_workflow_p95_ms_max", -1)) == 600.0, "优化视图查询成本门槛漂移")
    _require(gate.get("query_workflow_absolute_gate") == "existing_warm_query_p95_ms_max", "优化视图查询绝对成本门槛漂移")
    _require(gate.get("query_workflow_limit_source_sha256") == "18887027da298676b955d687571712beea571ead0a4995cf514a4bf432f60026", "优化视图查询成本权威摘要漂移")
    _require(gate.get("protected_regression_allowed") is False, "优化视图不得允许保护指标退化")
    efficiency = _mapping(contract, "v0_efficiency_baseline")
    efficiency_names = {
        "organization_input_overhead_ratio", "organization_vector_overhead_ratio",
        "derived_record_overhead_ratio", "derived_vector_overhead_ratio",
        "rebuild_input_overhead_ratio", "rebuild_vector_overhead_ratio",
        "rebuilt_record_overhead_ratio", "rebuilt_vector_overhead_ratio",
    }
    _require(all(float(efficiency.get(name, -1)) == 0.0 for name in efficiency_names), "优化视图 V0 结构效率基线漂移")
    profile = _mapping(contract, "formal_length_profile")
    _require(profile.get("asset_count") == 23867 and profile.get("total_content_chars") == 248978442, "优化视图正式长度总体漂移")
    _require([profile.get(name) for name in ("p50_chars", "p90_chars", "p99_chars", "max_chars")] == [10595, 17092, 20803, 78215], "优化视图正式长度分位漂移")
    leakage = _mapping(contract, "leakage_policy")
    _require(all(leakage.get(name) is False for name in ("formal_question_text", "formal_answer", "formal_gold_identity", "formal_session_content")), "优化视图泄漏边界无效")
    assets = view.get("assets")
    _require(isinstance(assets, list) and len(assets) == 20 and all(isinstance(item, dict) for item in assets), "优化视图资产规模无效")
    asset_ids = [item.get("fixture_id") for item in assets]
    _require(len(asset_ids) == len(set(asset_ids)) and all(isinstance(item, str) and item for item in asset_ids), "优化视图资产身份无效")
    for asset in assets:
        _require(isinstance(asset.get("content"), str) and asset["content"].strip(), "优化视图资产内容无效")
        repeat = asset.get("padding_repeat", 0)
        _require(isinstance(repeat, int) and repeat >= 0, "优化视图固定填充次数无效")
        _require(repeat == 0 or isinstance(asset.get("padding"), str) and asset["padding"], "优化视图固定填充无效")
        target = asset.get("target_runes", 0)
        _require(isinstance(target, int) and target >= 0, "优化视图固定长度无效")
        if target:
            _require(target >= len(asset["content"]) and isinstance(asset.get("padding"), str) and asset["padding"], "优化视图固定长度填充无效")
            _require(asset.get("fact_position") == "middle", "优化视图长资产事实位置无效")
    scale_targets = {item["fixture_id"]: item.get("target_runes") for item in assets if item.get("target_runes")}
    _require(scale_targets == {"S50": 10595, "S90": 17092, "S99": 20803, "SMAX": 78215, "SNOISE": 20803}, "优化视图长资产分位样本漂移")
    queries = view.get("queries")
    _require(isinstance(queries, list) and len(queries) == 10, "优化视图查询规模无效")
    roles = Counter(item.get("view_role") for item in queries if isinstance(item, dict))
    _require(roles == {"primary": 3, "protection": 2, "scale": 5}, "优化视图主样本、保护样本与规模样本分布无效")
    for query in queries:
        read_limit = 3 if query.get("view_role") == "scale" else 8
        _require(query.get("context_budget_chars") == 24000 and query.get("read_limit") == read_limit, "优化视图查询预算漂移")
        expected = query.get("expected_ids")
        forbidden = query.get("forbidden_ids", [])
        _require(isinstance(expected, list) and expected and set(expected) <= set(asset_ids), "优化视图期望身份无效")
        _require(isinstance(forbidden, list) and set(forbidden) <= set(asset_ids) and not set(expected) & set(forbidden), "优化视图排除身份无效")


def _validate_semantic_representation_view(path: Path) -> None:
    view = load_json(path)
    _require(view.get("schema") == "ownward.core-frontier-materials/v1", "语义表示视图 schema 无效")
    _require(view.get("version") == "ownward-kernel-optimization-view/v1", "语义表示视图版本无效")
    contract = _mapping(view, "optimization_view")
    _require(contract.get("id") == "semantic-representation-v1", "语义表示视图身份无效")
    _require(contract.get("direction") == "semantic_capability_and_representation_model", "语义表示视图方向无效")
    source = _mapping(contract, "formal_source")
    observed = _mapping(source, "mechanical_observation")
    _require(source.get("candidate") == "99f519018df99bd5202b0c571b8e43481cd1b80e", "语义表示视图未绑定 V0")
    _require(source.get("formal_errors") == 258 and source.get("mapped_cluster_errors") == 116, "语义表示视图正式规模无效")
    _require(
        observed == {
            "questions_with_oversized_embedding_failure": 116,
            "questions_with_any_vector": 0,
            "questions_with_non_lexical_search_signal": 0,
            "derived_records_without_vector": 5443,
        },
        "语义表示视图的正式机制观察漂移",
    )
    baseline = _mapping(contract, "v0_baseline")
    gate = _mapping(contract, "frozen_gate")
    _require(float(baseline.get("semantic_search_recall", -1)) == 0.0, "语义表示 V0 召回基线漂移")
    _require(float(baseline.get("semantic_search_error_rate", -1)) == 1.0, "语义表示 V0 错误基线漂移")
    _require(float(gate.get("semantic_search_recall_min", -1)) == 1.0, "语义表示召回门槛漂移")
    _require(float(gate.get("semantic_search_error_rate_max", -1)) == 0.0, "语义表示错误门槛漂移")
    _require(float(gate.get("semantic_embedding_calls_max", -1)) == 2.0, "语义表示有界调用门槛漂移")
    _require(float(gate.get("rebuild_semantic_embedding_calls_max", -1)) == 0.0, "语义表示重建复用门槛漂移")
    _require(float(gate.get("rebuild_semantic_recall_min", -1)) == 1.0, "语义表示重建召回门槛漂移")
    _require(float(gate.get("derived_vectors_per_asset_max", -1)) == 1.0, "语义表示单向量门槛漂移")
    _require(float(gate.get("semantic_input_to_source_bytes_max", -1)) == 1.0, "语义表示输入规模门槛漂移")
    _require(float(gate.get("semantic_search_p95_ms_max", -1)) == 78.0, "语义查询成本门槛漂移")
    _require(gate.get("protected_regression_allowed") is False, "语义表示视图不得允许保护退化")
    leakage = _mapping(contract, "leakage_policy")
    _require(all(leakage.get(name) is False for name in ("formal_question_text", "formal_answer", "formal_gold_identity", "formal_session_content")), "语义表示视图泄漏边界无效")
    assets = view.get("assets")
    _require(isinstance(assets, list) and len(assets) == 6 and all(isinstance(item, dict) for item in assets), "语义表示视图资产规模无效")
    asset_ids = [item.get("fixture_id") for item in assets]
    _require(len(asset_ids) == len(set(asset_ids)) and all(isinstance(item, str) and item for item in asset_ids), "语义表示视图资产身份无效")
    _require(sum(int(item.get("padding_repeat", 0)) > 0 for item in assets) == 4, "语义表示视图必须覆盖四项长资产")
    queries = view.get("queries")
    _require(isinstance(queries, list) and len(queries) == 6, "语义表示视图查询规模无效")
    _require(Counter(item.get("view_role") for item in queries) == {"primary": 4, "protection": 2}, "语义表示视图角色分布无效")
    embeddings = view.get("frozen_embeddings")
    keys = view.get("semantic_embedding_keys")
    semantics = _mapping(view, "frozen_semantics")
    analyses = semantics.get("analyses")
    _require(isinstance(embeddings, dict) and set(embeddings) == set(asset_ids) and all(_valid_vector(item) for item in embeddings.values()), "语义表示视图资产向量无效")
    _require(isinstance(keys, dict) and set(keys) == set(asset_ids) and len(set(keys.values())) == len(asset_ids), "语义表示视图嵌入键无效")
    _require(isinstance(analyses, dict) and set(analyses) == set(asset_ids), "语义表示视图冻结分析无效")
    for fixture_id, key in keys.items():
        _require(isinstance(key, str) and key and key in json.dumps(analyses[fixture_id], ensure_ascii=False), f"语义表示视图 {fixture_id} 未把检索键保留在冻结分析中")
    query_embeddings = view.get("frozen_query_embeddings")
    query_ids = {item["query_id"] for item in queries}
    _require(isinstance(query_embeddings, dict) and set(query_embeddings) == query_ids and all(_valid_vector(item) for item in query_embeddings.values()), "优化视图查询向量无效")


def _validate_storage_deposition_view(path: Path) -> None:
    view = load_json(path)
    _require(view.get("schema") == "ownward.core-frontier-materials/v1", "存储沉淀视图 schema 无效")
    _require(view.get("version") == "ownward-kernel-optimization-view/v1", "存储沉淀视图版本无效")
    contract = _mapping(view, "optimization_view")
    _require(contract.get("id") == "storage-deposition-v1", "存储沉淀视图身份无效")
    _require(contract.get("direction") == "data_structure_and_storage_architecture", "存储沉淀视图方向无效")
    observed = _mapping(contract, "formal_scale_observation")
    _require(observed.get("candidate") == "99f519018df99bd5202b0c571b8e43481cd1b80e", "存储沉淀视图未绑定 V0")
    _require(
        observed.get("derived_log_bytes") == 8390585705
        and observed.get("derived_record_count") == 47734
        and observed.get("current_asset_count") == 23867
        and observed.get("semantic_work_bytes") == 8330782630,
        "存储沉淀视图正式规模观察漂移",
    )
    _require(float(observed.get("semantic_work_share", 0)) > 0.99, "存储沉淀视图未证明主要放大来源")
    profile = _mapping(contract, "formal_length_profile")
    _require(profile.get("asset_count") == 23867 and profile.get("total_content_chars") == 248978442, "存储沉淀视图正式长度总体漂移")
    _require(
        [profile.get(name) for name in ("min_chars", "p25_chars", "p50_chars", "p75_chars", "p90_chars", "p95_chars", "p99_chars", "max_chars")]
        == [103, 6075, 10595, 14643, 17092, 18416, 20803, 78215],
        "存储沉淀视图正式长度分布漂移",
    )
    gate = _mapping(contract, "frozen_gate")
    _require(float(gate.get("storage_amplification_ratio_max", -1)) == 0.5, "存储沉淀降幅门槛漂移")
    _require(float(gate.get("derived_storage_amplification_ratio_max", -1)) == 0.5, "派生存储降幅门槛漂移")
    _require(float(gate.get("authority_history_reclaim_ratio_max", -1)) == 0.5, "权威历史压实门槛漂移")
    _require(float(gate.get("storage_view_query_p95_ms_max", -1)) == 600.0, "存储视图查询成本门槛漂移")
    _require(gate.get("protected_regression_allowed") is False, "存储沉淀视图不得允许保护退化")
    leakage = _mapping(contract, "leakage_policy")
    _require(all(leakage.get(name) is False for name in ("formal_question_text", "formal_answer", "formal_gold_identity", "formal_session_content")), "存储沉淀视图泄漏边界无效")
    assets = view.get("assets")
    _require(isinstance(assets, list) and len(assets) == 8, "存储沉淀视图资产规模无效")
    targets = [item.get("target_runes") for item in assets]
    _require(targets == [103, 6075, 10595, 14643, 17092, 18416, 20803, 78215], "存储沉淀视图长度样本漂移")
    _require(all(isinstance(item.get("content"), str) and item["content"].startswith("marker-d") for item in assets), "存储沉淀视图事实身份无效")
    queries = view.get("queries")
    _require(isinstance(queries, list) and len(queries) == len(assets), "存储沉淀视图查询规模无效")
    _require(all(item.get("view_role") == "protection" for item in queries), "存储沉淀视图查询角色无效")


def _validate_budget_selection_view(path: Path) -> None:
    view = load_json(path)
    _require(view.get("schema") == "ownward.kernel-consumer-optimization-view/v1", "预算选择视图 schema 无效")
    _require(view.get("version") == "ownward-kernel-optimization-view/v1", "预算选择视图版本无效")
    contract = _mapping(view, "optimization_view")
    _require(contract.get("id") == "budget-fit-evidence-selection-v2", "预算选择视图身份无效")
    _require(contract.get("direction") == "retrieval_architecture_and_algorithm", "预算选择视图方向无效")
    source = _mapping(contract, "formal_source")
    observed = _mapping(source, "mechanical_observation")
    _require(source.get("candidate") == "99f519018df99bd5202b0c571b8e43481cd1b80e", "预算选择视图未绑定 V0")
    _require(source.get("formal_errors") == 258 and source.get("mapped_cluster_errors") == 14, "预算选择视图正式规模无效")
    _require(
        observed == {
            "all_targets_returned": 14,
            "all_target_sets_fit_budget": 14,
            "prefix_budget_stop_on_unread_target": 14,
            "single_target_questions": 12,
            "two_target_questions": 2,
            "target_rank_2": 8,
            "target_rank_3": 5,
            "target_rank_4": 1,
        },
        "预算选择视图的正式机制观察漂移",
    )
    scoring = _mapping(contract, "scoring")
    _require(
        scoring.get("context_budget_chars") == 24000
        and scoring.get("read_limit") == 8
        and scoring.get("evidence_search_limit_per_source") == 3
        and scoring.get("candidate_policy") == "rank-depth-diagonal-budget-fit/v1"
        and scoring.get("score_unit") == "required_source_bound_evidence_item",
        "预算选择视图消费合同漂移",
    )
    baseline = _mapping(contract, "v0_baseline")
    gate = _mapping(contract, "frozen_gate")
    _require(float(baseline.get("required_evidence_question_recall", -1)) == 0.0, "预算选择 V0 召回基线漂移")
    _require(float(baseline.get("required_evidence_question_error_rate", -1)) == 1.0, "预算选择 V0 错误基线漂移")
    _require(float(gate.get("required_evidence_item_recall_min", -1)) == 1.0, "预算选择证据项召回门槛漂移")
    _require(float(gate.get("required_evidence_question_recall_min", -1)) == 1.0, "预算选择问题召回门槛漂移")
    _require(float(gate.get("required_evidence_question_error_rate_max", -1)) == 0.0, "预算选择错误门槛漂移")
    for name in (
        "rank_depth_diagonal_order_rate_min", "top_rank_three_passage_coverage_min",
        "multi_source_multi_depth_coverage_min", "budget_skip_continuation_rate_min",
        "lazy_evidence_search_rate_min", "context_budget_compliance_min",
        "read_limit_compliance_min", "selection_policy_identity_rate_min",
    ):
        _require(float(gate.get(name, -1)) == 1.0, f"预算选择保护门槛漂移: {name}")
    _require(float(gate.get("consumer_retrieval_p95_ms_max", -1)) == 600.0, "预算选择时延门槛漂移")
    _require(gate.get("consumer_retrieval_limit_source_sha256") == "18887027da298676b955d687571712beea571ead0a4995cf514a4bf432f60026", "预算选择时延权威摘要漂移")
    _require(gate.get("protected_regression_allowed") is False, "预算选择视图不得允许保护退化")
    protected = _mapping(contract, "protected_chains")
    _require(protected.get("on_demand_evidence_delivery_commit") == "e6bfc82c0750ed0db30ff67b9ec6f7a3ca446570", "按需证据保护身份漂移")
    _require(protected.get("semantic_representation_commit") == "436e12c3b97ad6c78254fdc8296a5aa6bb8c8665", "语义表示保护身份漂移")
    leakage = _mapping(contract, "leakage_policy")
    _require(all(leakage.get(name) is False for name in ("formal_question_text", "formal_answer", "formal_gold_identity", "formal_session_content")), "预算选择视图泄漏边界无效")
    cases = view.get("cases")
    _require(isinstance(cases, list) and len(cases) == 8 and all(isinstance(item, dict) for item in cases), "预算选择视图样本规模无效")
    case_ids = [item.get("case_id") for item in cases]
    _require(len(case_ids) == len(set(case_ids)) and all(isinstance(item, str) and item for item in case_ids), "预算选择视图样本身份无效")
    for case in cases:
        sources = case.get("sources")
        _require(isinstance(case.get("query"), str) and case["query"].strip(), "预算选择查询无效")
        _require(isinstance(sources, list) and 2 <= len(sources) <= 24 and all(isinstance(item, dict) for item in sources), "预算选择来源集合无效")
        source_ids = [item.get("fixture_id") for item in sources]
        _require(len(source_ids) == len(set(source_ids)) and all(isinstance(item, str) and item for item in source_ids), "预算选择来源身份无效")
        required = case.get("required_evidence")
        _require(isinstance(required, list) and required and all(isinstance(item, dict) for item in required), "预算选择必要证据无效")
        required_pairs = [(item.get("source_id"), item.get("marker")) for item in required]
        _require(
            len(required_pairs) == len(set(required_pairs))
            and all(isinstance(source_id, str) and source_id in source_ids and isinstance(marker, str) and marker for source_id, marker in required_pairs),
            "预算选择必要证据身份无效",
        )
        contents: dict[str, str] = {}
        for source_item in sources:
            segments = source_item.get("segments")
            if segments is not None:
                _require(isinstance(segments, list) and 1 <= len(segments) <= 3 and all(isinstance(item, dict) for item in segments), "预算选择分段来源无效")
                built: list[str] = []
                for segment in segments:
                    content = segment.get("content")
                    width = segment.get("width")
                    padding = segment.get("padding_char")
                    _require(
                        isinstance(content, str) and content.strip() and width == 384
                        and isinstance(padding, str) and len(padding) == 1 and len(content) <= width,
                        "预算选择分段内容无效",
                    )
                    built.append(content.ljust(width, padding))
                contents[str(source_item["fixture_id"])] = "".join(built)
            else:
                _require(isinstance(source_item.get("content"), str) and source_item["content"].strip(), "预算选择来源内容无效")
                repeat = source_item.get("padding_repeat", 0)
                _require(isinstance(repeat, int) and repeat >= 0, "预算选择来源填充次数无效")
                _require(repeat == 0 or isinstance(source_item.get("padding"), str) and source_item["padding"], "预算选择来源填充无效")
                contents[str(source_item["fixture_id"])] = str(source_item["content"]) + str(source_item.get("padding", "")) * repeat
        _require(all(marker in contents[source_id] for source_id, marker in required_pairs), "预算选择必要证据标记未绑定来源正文")
        maximum_calls = case.get("max_evidence_search_calls")
        _require(isinstance(maximum_calls, int) and 1 <= maximum_calls <= len(sources), "预算选择惰性发现上限无效")
        _require(int(case.get("context_budget_chars", 24000)) > 0 and 0 < int(case.get("read_limit", 8)) <= 8, "预算选择样本成本边界无效")
    by_id = {str(item["case_id"]): item for item in cases}
    _require(len(by_id["B07"]["sources"]) == 24 and len(by_id["B07"]["required_evidence"]) == 3, "预算选择最大来源同源深度保护无效")
    _require(
        len({item["source_id"] for item in by_id["B08"]["required_evidence"]}) >= 2
        and sum(item["source_id"] == "B08-A" for item in by_id["B08"]["required_evidence"]) >= 2,
        "预算选择多来源多深度保护无效",
    )


def _scenario(scenarios: Any, identifier: str) -> dict[str, Any]:
    _require(isinstance(scenarios, list), "专项场景集合无效")
    for scenario in scenarios:
        if isinstance(scenario, dict) and _mapping(scenario, "truth").get("id") == identifier:
            return scenario
    raise MaterialsError(f"专项数据集缺少场景: {identifier}")


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
