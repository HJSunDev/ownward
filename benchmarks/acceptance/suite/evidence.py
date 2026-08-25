from __future__ import annotations

import hashlib
import json
import math
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from contract import load_contract, validate_report
from materials import load_json, validate_materials


class EvidenceError(ValueError):
    pass


ARTIFACT_SCHEMA = "ownward.acceptance-artifacts/v1"
SENSITIVE_ARTIFACT_NAMES = {"auth.json", "credentials.json", ".env"}


def attach_artifacts(report: dict[str, Any], report_path: Path, paths: list[Path]) -> None:
    """Bind a completed report to the exact raw evidence files that support it."""
    base = report_path.resolve().parent.parent
    files: list[dict[str, Any]] = []
    seen: set[str] = set()
    for source in paths:
        source = source.resolve()
        _require(source.is_relative_to(base), f"原始证据不在统一验收工作区: {source}")
        candidates = [source] if source.is_file() else sorted(path for path in source.rglob("*") if path.is_file())
        _require(candidates, f"原始证据为空: {source}")
        for path in candidates:
            _require(not path.is_symlink() and path.resolve().is_relative_to(base), f"原始证据不得越出工作区或使用符号链接: {path}")
            _require(path.name.lower() not in SENSITIVE_ARTIFACT_NAMES, f"原始证据包含认证文件: {path.name}")
            relative = path.relative_to(base).as_posix()
            if relative in seen:
                continue
            seen.add(relative)
            files.append({"path": relative, "sha256": _file_sha256(path), "bytes": path.stat().st_size})
    _require(files, "报告缺少原始证据")
    files.sort(key=lambda item: item["path"])
    report["artifacts"] = {
        "schema": ARTIFACT_SCHEMA,
        "base": "..",
        "files": files,
        "manifest_sha256": _canonical_sha256(files),
    }


def validate_report_artifacts(report_path: Path, report: dict[str, Any]) -> str:
    manifest = report.get("artifacts")
    _require(isinstance(manifest, dict), "报告缺少原始证据清单")
    _require(manifest.get("schema") == ARTIFACT_SCHEMA, "原始证据清单 schema 无效")
    _require(manifest.get("base") == "..", "原始证据清单基准目录无效")
    files = manifest.get("files")
    _require(isinstance(files, list) and files, "原始证据清单为空")
    _require(manifest.get("manifest_sha256") == _canonical_sha256(files), "原始证据清单摘要无效")
    base = report_path.resolve().parent.parent
    seen: set[str] = set()
    for item in files:
        _require(isinstance(item, dict), "原始证据条目无效")
        relative = str(item.get("path", ""))
        relative_path = Path(relative)
        _require(relative and not relative_path.is_absolute() and ".." not in relative_path.parts, "原始证据路径无效")
        _require(relative_path.name.lower() not in SENSITIVE_ARTIFACT_NAMES, "原始证据清单包含认证文件")
        _require(relative not in seen, "原始证据路径重复")
        seen.add(relative)
        path = (base / relative_path).resolve()
        _require(path.is_relative_to(base) and path.is_file() and not path.is_symlink(), f"原始证据缺失: {relative}")
        _require(item.get("bytes") == path.stat().st_size, f"原始证据大小变化: {relative}")
        _require(item.get("sha256") == _file_sha256(path), f"原始证据内容变化: {relative}")
    return str(manifest["manifest_sha256"])


def validate_adapters(suite_root: Path) -> dict[str, Any]:
    contract = load_contract(suite_root / "contract.json")
    adapters = load_json(suite_root / "adapters.json")
    _require(adapters.get("schema") == "ownward.acceptance-adapters/v1", "适配器契约 schema 无效")
    _require(adapters.get("suite_version") == contract["suite_version"], "适配器契约体系版本无效")
    interface = _mapping(adapters, "product_interface")
    _require(interface.get("allowed") == ["ownward_cli", "ownward_mcp"], "验收必须只使用产品公开 CLI 与 MCP")
    _require(interface.get("test_only_product_path") is False, "验收不得要求产品测试专用路径")
    loop = _mapping(adapters, "optimization_loop")
    _require(loop.get("version") == contract["optimization_loop"]["benchmark_version"], "内核前沿观察器版本无效")
    _require(loop.get("output_schema") == "ownward.core-frontier-observation/v1", "内核前沿观察器输出无效")
    _require(loop.get("external_intelligence") is False, "内核前沿观察器不得调用外部智能")
    _require((suite_root / str(loop.get("implementation"))).resolve().is_file(), "内核前沿观察器实现不存在")
    layers = _mapping(adapters, "layers")
    _require(set(layers) == set(contract["evidence_layers"]), "适配器必须且只能覆盖三层证据")
    for name, definition in contract["evidence_layers"].items():
        adapter = _mapping(layers, name)
        _require(adapter.get("version") == definition.get("version"), f"{name} 适配器版本无效")
        _require(adapter.get("report_schema") == definition.get("output_schema"), f"{name} 报告契约无效")
    for relative in (
        layers["core"].get("implementation"),
        layers["product"].get("implementation"),
        layers["product"].get("scorer"),
        layers["product"].get("resource_adapter"),
    ):
        _require(isinstance(relative, str) and (suite_root / relative).is_file(), f"验收适配实现不存在: {relative}")
    community = _mapping(layers, "community")
    _require(community.get("official_revision") == "9e0b455f4ef0e2ab8f2e582289761153549043fc", "LongMemEval-S 官方版本未固定")
    _require(community.get("data_revision") == "98d7416c24c778c2fee6e6f3006e7a073259d48f", "LongMemEval-S 数据版本未固定")
    _require(community.get("data_sha256") == "d6f21ea9d60a0d56f34a05b609c79c88a451d2ae03597821ea3d5a9678c3a442", "LongMemEval-S 数据摘要未固定")
    _require(community.get("questions") == 500, "LongMemEval-S 题量无效")
    _require(community.get("capability_sources") == {"semantic": "codex", "reader": "codex", "judge": "official-openai"}, "LongMemEval-S 能力来源未固定")
    adapter_path = (suite_root / str(community.get("adapter"))).resolve()
    protocol_path = (suite_root / str(community.get("protocol"))).resolve()
    _require(adapter_path.is_file() and protocol_path.is_file(), "LongMemEval-S 适配器或协议不存在")
    source = adapter_path.read_text(encoding="utf-8")
    _require(f'OFFICIAL_CODE_REVISION = "{community["official_revision"]}"' in source, "LongMemEval-S 适配器未绑定官方版本")
    return adapters


def validate_layer_report(
    contract: dict[str, Any],
    layer: str,
    report: dict[str, Any],
    *,
    expected_binding: dict[str, str] | None = None,
) -> None:
    _require(layer in {"core", "product", "community"}, f"未知证据层: {layer}")
    validate_report(contract, layer, report)
    if expected_binding:
        for field in ("candidate", "binary_sha256"):
            _require(report.get(field) == expected_binding.get(field), f"{layer} 报告的 {field} 与候选不一致")
    _require(_is_sha256(str(report.get("binary_sha256", ""))), f"{layer} 报告的二进制摘要无效")
    _require(isinstance(report.get("environment"), dict) and report["environment"], f"{layer} 报告缺少环境身份")
    _require(isinstance(report.get("inputs"), dict) and report["inputs"], f"{layer} 报告缺少输入绑定")
    if layer == "core":
        _validate_core(contract, report)
    elif layer == "product":
        _validate_product(contract, report)
    else:
        _validate_community(contract, report)


def validate_suite_inputs(suite_root: Path) -> None:
    validate_materials(suite_root / "materials")
    validate_adapters(suite_root)


def build_core_report(
    contract: dict[str, Any],
    binding: dict[str, Any],
    invariants: dict[str, bool],
    *,
    started_at: str,
    finished_at: str,
) -> dict[str, Any]:
    report = {
        "schema": contract["evidence_layers"]["core"]["output_schema"],
        "suite_version": contract["suite_version"],
        "candidate": binding["candidate"],
        "binary_sha256": binding["binary_sha256"],
        "environment": binding["environment"],
        "inputs": binding["inputs"],
        "invariants": invariants,
        "passed": all(invariants.values()),
        "started_at": started_at,
        "finished_at": finished_at,
    }
    validate_layer_report(contract, "core", report)
    return report


def score_product(
    contract: dict[str, Any],
    dataset: dict[str, Any],
    qualification: dict[str, Any],
    mode: str,
    results: list[dict[str, Any]],
    binding: dict[str, Any],
) -> dict[str, Any]:
    definition = contract["evidence_layers"]["product"]
    _require(mode in {"qualification", "full"}, "专项评分模式无效")
    scenarios = dataset.get("scenarios")
    _require(isinstance(scenarios, list), "专项数据集场景无效")
    wanted = set(qualification["scenario_ids"]) if mode == "qualification" else {
        item["truth"]["id"] for item in scenarios
    }
    selected = {item["truth"]["id"]: item for item in scenarios if item["truth"]["id"] in wanted}
    by_id = {item.get("scenario_id"): item for item in results if isinstance(item, dict)}
    _require(set(by_id) == set(selected), "专项结果没有且只覆盖当前执行集")
    categories = {
        name: {"scenarios": 0, "passed": True, "recall": 0.0, "precision": 0.0, "ndcg": 0.0}
        for name in definition["categories"]
    }
    gains: list[float] = []
    relation_evidence_precisions: list[float] = []
    latencies: list[float] = []
    semantic_latencies: list[float] = []
    agent_query_latencies: list[float] = []
    end_to_end_latencies: list[float] = []
    resource_peaks: list[float] = []
    scenario_details: list[dict[str, Any]] = []
    for identifier, scenario in selected.items():
        result = by_id[identifier]
        query = scenario["truth"]["query"]
        expected = list(query["expected_ids"])
        forbidden = set(query.get("forbidden_ids", []))
        returned = list(result.get("returned_ids", []))
        relation = list(result.get("relation_ids", []))
        navigation = set(result.get("navigation_ids", []))
        recall = _recall(returned, expected)
        precision = _precision(returned, expected, forbidden)
        ndcg = _ndcg(returned, expected)
        expected_facts = set(query.get("answer_facts", []))
        actual_facts = set(result.get("answer_facts", []))
        gain = _recall(relation, expected)
        relation_set = set(relation)
        relation_precision = len(relation_set.intersection(expected)) / len(relation_set) if relation_set else 1.0
        category = scenario["truth"]["task_class"]
        facts_passed = actual_facts == expected_facts and result.get("grounded") is True
        passed = recall == 1.0 and not forbidden.intersection(returned) and facts_passed
        aggregate = categories[category]
        aggregate["scenarios"] += 1
        aggregate["passed"] = aggregate["passed"] and passed
        aggregate["recall"] += recall
        aggregate["precision"] += precision
        aggregate["ndcg"] += ndcg
        gains.append(gain)
        relation_evidence_precisions.append(relation_precision)
        navigation_supported_gain = bool(set(relation).intersection(expected).intersection(navigation))
        latencies.append(float(result.get("latency_ms", math.inf)))
        semantic_latencies.append(float(result.get("semantic_ms", math.inf)))
        agent_query_latencies.append(float(result.get("agent_query_ms", math.inf)))
        end_to_end_latencies.append(float(result.get("end_to_end_ms", math.inf)))
        resource_peaks.append(float(result.get("peak_mib", math.inf)))
        scenario_details.append({
            "scenario_id": identifier,
            "category": category,
            "recall": recall,
            "precision": precision,
            "ndcg": ndcg,
            "relation_gain": gain,
            "relation_evidence_precision": relation_precision,
            "used_navigation": result.get("used_navigation") is True,
            "navigation_supported_gain": navigation_supported_gain,
            "answer_facts_exact": facts_passed,
            "grounded": result.get("grounded") is True,
            "passed": passed,
        })
    for value in categories.values():
        count = value["scenarios"]
        for metric in ("recall", "precision", "ndcg"):
            value[metric] /= count
    organization_gain_passed = (
        max(gains, default=0.0) > 0
        and min(gains, default=0.0) >= 0
        and min(relation_evidence_precisions, default=1.0) == 1.0
    )
    execution_passed = all(item.get("within_latency_budget") is True and item.get("within_resource_budget") is True for item in results)
    now = datetime.now(timezone.utc).isoformat()
    report = {
        "schema": definition["output_schema"],
        "suite_version": contract["suite_version"],
        "dataset_version": definition["version"],
        "mode": mode,
        "candidate": binding["candidate"],
        "binary_sha256": binding["binary_sha256"],
        "environment": binding["environment"],
        "inputs": binding["inputs"],
        "categories": categories,
        "organization_gain": {
            "passed": organization_gain_passed,
            "minimum": min(gains),
            "maximum": max(gains),
            "evidence_precision_minimum": min(relation_evidence_precisions),
        },
        "quality": {"passed": all(value["passed"] for value in categories.values()), "scenarios": scenario_details},
        "latency": {
            "passed": execution_passed,
            "ownward_query_max_ms": max(latencies),
            "semantic_collaboration_max_ms": max(semantic_latencies),
            "agent_query_max_ms": max(agent_query_latencies),
            "scenario_end_to_end_max_ms": max(end_to_end_latencies),
        },
        "resources": {"passed": execution_passed, "peak_mib": max(resource_peaks)},
        "passed": all(value["passed"] for value in categories.values()) and organization_gain_passed and execution_passed,
        "started_at": now,
        "finished_at": now,
    }
    validate_layer_report(contract, "product", report)
    return report


def _validate_core(contract: dict[str, Any], report: dict[str, Any]) -> None:
    required = contract["evidence_layers"]["core"]["required_invariants"]
    invariants = report.get("invariants")
    _require(isinstance(invariants, dict) and set(invariants) == set(required), "内核报告不变量集合无效")
    _require(all(isinstance(value, bool) for value in invariants.values()), "内核报告不变量判定无效")
    _require(report.get("passed") is all(invariants.values()), "内核报告总判定与不变量不一致")


def _validate_product(contract: dict[str, Any], report: dict[str, Any]) -> None:
    definition = contract["evidence_layers"]["product"]
    _require(report.get("dataset_version") == definition["version"], "专项报告数据版本无效")
    mode = report.get("mode")
    _require(mode in {"qualification", "full"}, "专项报告模式无效")
    categories = report.get("categories")
    _require(isinstance(categories, dict) and set(categories) == set(definition["categories"]), "专项报告能力类别无效")
    expected_scenarios = definition[f"{mode}_scenarios"]
    _require(sum(int(value.get("scenarios", 0)) for value in categories.values()) == expected_scenarios, "专项报告场景数无效")
    _require(all(isinstance(value.get("passed"), bool) for value in categories.values()), "专项报告类别判定无效")
    gain = report.get("organization_gain")
    _require(isinstance(gain, dict) and isinstance(gain.get("passed"), bool), "专项报告组织结构增益判定无效")
    tracks: list[bool] = []
    for name in ("quality", "latency", "resources"):
        track = report.get(name)
        _require(isinstance(track, dict) and isinstance(track.get("passed"), bool), f"专项报告 {name} 轨判定无效")
        tracks.append(track["passed"])
    expected = all(value["passed"] for value in categories.values()) and gain["passed"] and all(tracks)
    _require(report.get("passed") is expected, "专项报告总判定与分轨结果不一致")


def _validate_community(contract: dict[str, Any], report: dict[str, Any]) -> None:
    definition = contract["evidence_layers"]["community"]
    _require(report.get("official_version") == definition["version"], "社区报告官方版本无效")
    _require(report.get("capabilities") == definition["capabilities"], "社区报告能力来源无效")
    benchmark = report.get("benchmark")
    _require(isinstance(benchmark, dict) and benchmark.get("questions") == definition["questions"] and benchmark.get("complete") is True, "社区报告基准范围不完整")
    _require(set(benchmark.get("question_types", [])) == set(definition["question_types"]), "社区报告问题类型不完整")
    quality = report.get("quality")
    _require(isinstance(quality, dict) and isinstance(quality.get("accuracy"), (int, float)) and 0 <= quality["accuracy"] <= 1, "社区报告准确率无效")
    _require(quality.get("minimum_accuracy") == definition["minimum_accuracy"], "社区报告质量门槛漂移")
    _require(quality.get("passed") is (quality["accuracy"] >= definition["minimum_accuracy"]), "社区报告质量判定无效")
    retrieval = report.get("retrieval")
    _require(isinstance(retrieval, dict) and all(isinstance(retrieval.get(name), (int, float)) and retrieval[name] > 0 for name in ("mean_ms", "p95_ms", "max_ms")), "社区报告检索时延无效")
    cost = report.get("cost")
    _require(isinstance(cost, dict) and cost.get("within_budget") is True and isinstance(cost.get("wall_seconds"), (int, float)), "社区报告成本边界无效")
    submission = report.get("submission")
    _require(isinstance(submission, dict), "社区报告缺少固定 submission")
    _require(_is_sha256(str(submission.get("package_sha256", ""))), "社区报告归档摘要无效")
    _require(_is_sha256(str(submission.get("official_evaluation_sha256", ""))), "社区报告官方评测摘要无效")
    _require(_is_sha256(str(submission.get("hypotheses_sha256", ""))), "社区报告回答摘要无效")
    _require(_is_sha256(str(submission.get("checkpoint_manifest_sha256", ""))), "社区报告检查点清单摘要无效")
    expected = benchmark["complete"] and quality["passed"] and cost["within_budget"]
    _require(report.get("passed") is expected, "社区报告总判定与分轨结果不一致")


def _recall(returned: list[str], expected: list[str]) -> float:
    return len(set(returned).intersection(expected)) / len(expected) if expected else 1.0


def _precision(returned: list[str], expected: list[str], forbidden: set[str]) -> float:
    relevant = set(expected)
    considered = [identifier for identifier in returned if identifier in relevant or identifier in forbidden]
    return len([identifier for identifier in considered if identifier in relevant]) / len(considered) if considered else 0.0


def _ndcg(returned: list[str], expected: list[str]) -> float:
    expected_set = set(expected)
    dcg = sum((1 / math.log2(index + 2)) for index, identifier in enumerate(returned) if identifier in expected_set)
    ideal = sum(1 / math.log2(index + 2) for index in range(len(expected)))
    return dcg / ideal if ideal else 1.0


def _mapping(value: dict[str, Any], name: str) -> dict[str, Any]:
    nested = value.get(name)
    _require(isinstance(nested, dict), f"{name} 必须是对象")
    return nested


def _is_sha256(value: str) -> bool:
    return len(value) == 64 and all(character in "0123456789abcdef" for character in value)


def _file_sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def _canonical_sha256(value: Any) -> str:
    encoded = json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":")).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise EvidenceError(message)
