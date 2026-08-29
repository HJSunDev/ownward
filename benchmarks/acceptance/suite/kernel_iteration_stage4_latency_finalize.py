from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import kernel_iteration_evidence as evidence
import kernel_iteration_stage4 as stage4
import kernel_iteration_stage4_latency_candidate as latency
import kernel_iteration_validation as validation


RESULT_SCHEMA = "ownward.kernel-iteration-stage4-retrieval-latency-evidence/v2"


def finalize(
    suite_root: Path,
    output_root: Path,
    subject_manifest: Path,
    execution_config: Path,
    performance_result: Path,
    semantic_protection_result: Path,
    previous_semantic_protection_result: Path,
    fail_closed_result: Path,
    previous_fail_closed_result: Path,
    generalization_result: Path,
    multisource_result: Path,
    development_result: Path,
    regression_result: Path,
    formal_state: Path,
    *,
    resume: bool = False,
) -> dict[str, Any]:
    suite_root, output_root = suite_root.resolve(), output_root.resolve()
    route = latency.load_route(suite_root)
    _require(route.get("root", {}).get("status") == "closed", "检索时延仍有未关闭的真实主路径；禁止生成终态")
    subject_path = subject_manifest.resolve()
    subject = evidence.validate_v2_subject(evidence.load_contract(suite_root), _load_json(subject_path))
    runtime = validation.validate_execution_config(suite_root, execution_config.resolve())
    _require(evidence.file_sha256(runtime["binary"]) == subject["content"]["artifacts"]["binary"], "检索时延终态二进制与 subject 错绑")
    destination = output_root / "result.json"
    if destination.is_file():
        _require(resume, "检索时延终态已经存在；必须显式 resume")
        value = _load_json(destination)
        _validate_identity(value, RESULT_SCHEMA, "检索时延终态")
        _require(value.get("controller_sha256") == evidence.file_sha256(Path(__file__).resolve()), "检索时延终态控制器身份漂移")
        _require(value.get("subject_identity") == subject["identity"] and value.get("route_identity") == route["identity"], "检索时延终态身份漂移")
        return {**value, "reused": True, "path": str(destination)}

    performance = _load_json(performance_result.resolve())
    _validate_identity(performance, latency.RESULT_SCHEMA, "检索时延性能结果")
    _require(
        performance.get("route_identity") == route["identity"]
        and performance.get("subjects", {}).get("candidate") == subject["identity"],
        "检索时延性能结果错绑",
    )
    _require(performance.get("passed") is True and all(performance.get("checks", {}).values()), "检索时延性能门未通过")
    _validate_performance_attribution(performance, route)

    inputs = suite_root / "iteration" / "v2"
    expected_inputs = {
        "semantic": _load_json(inputs / "stage4-retrieval-semantic-protection-input-v4.json")["identity"],
        "fail_closed": _load_json(inputs / "stage4-retrieval-semantic-failclosed-input-v2.json")["identity"],
        "generalization": _load_json(inputs / "stage4-retrieval-semantic-generalization-input-v1.json")["identity"],
        "multisource": _load_json(inputs / "stage4-latency-multisource-input-v2.json")["identity"],
        "development": _load_json(inputs / "stage4-latency-development-input-v2.json")["identity"],
        "regression": _load_json(inputs / "stage4-latency-regression-input-v2.json")["identity"],
    }
    result_paths = {
        "semantic": semantic_protection_result.resolve(),
        "fail_closed": fail_closed_result.resolve(),
        "generalization": generalization_result.resolve(),
        "multisource": multisource_result.resolve(),
        "development": development_result.resolve(),
        "regression": regression_result.resolve(),
    }
    results = {name: validation._load_execution_result(path) for name, path in result_paths.items()}
    for name, result in results.items():
        _require(result.get("subject_identity") == subject["identity"] and result.get("subject_role") == "v2-candidate", f"{name} 质量结果错绑")
        _require(result.get("input_identity") == expected_inputs[name], f"{name} 质量输入漂移")
        _require(result.get("passed") is True and result.get("candidate_decision") is True, f"{name} 质量绝对门未通过")

    previous_semantic = validation._load_execution_result(previous_semantic_protection_result.resolve())
    _require(
        previous_semantic.get("subject_identity") == route["subjects"]["previous-v2"]
        and previous_semantic.get("input_identity") == expected_inputs["semantic"]
        and previous_semantic.get("passed") is True,
        "前序 V2 语义保护证据错绑或未通过",
    )
    semantic = results["semantic"]["observation"]
    semantic_cases = semantic["case_evidence"]
    semantic_materials = _load_json(inputs / "stage4-retrieval-semantic-protection-materials-v3.json")
    _require(
        semantic_materials["identity"] == route["semantic_protection"]["materials_identity"]
        and expected_inputs["semantic"] == route["semantic_protection"]["input_identity"],
        "语义锚点保护材料没有绑定冻结路线",
    )
    _validate_semantic_protection(semantic, semantic_materials, require_fast_path=True)
    _validate_semantic_protection(previous_semantic["observation"], semantic_materials, require_fast_path=False)

    previous_fail_closed = validation._load_execution_result(previous_fail_closed_result.resolve())
    _require(
        previous_fail_closed.get("subject_identity") == route["subjects"]["previous-v2"]
        and previous_fail_closed.get("input_identity") == expected_inputs["fail_closed"]
        and previous_fail_closed.get("passed") is True,
        "前序 V2 失败关闭证据错绑或未通过",
    )
    fail_closed = results["fail_closed"]["observation"]
    fail_closed_cases = fail_closed["case_evidence"]
    fail_closed_materials = _load_json(inputs / "stage4-retrieval-semantic-failclosed-materials-v1.json")
    _require(
        fail_closed_materials["identity"] == route["fail_closed_protection"]["materials_identity"]
        and expected_inputs["fail_closed"] == route["fail_closed_protection"]["input_identity"],
        "失败关闭保护材料没有绑定冻结路线",
    )
    _validate_fail_closed_protection(fail_closed, fail_closed_materials)
    _validate_fail_closed_protection(previous_fail_closed["observation"], fail_closed_materials)

    generalization = results["generalization"]["observation"]
    generalization_cases = generalization["case_evidence"]
    generalization_materials = _load_json(inputs / "stage4-retrieval-semantic-generalization-materials-v1.json")
    _require(
        generalization_materials["identity"] == route["generalization_protection"]["materials_identity"]
        and expected_inputs["generalization"] == route["generalization_protection"]["input_identity"],
        "通用结构排序保护材料没有绑定冻结路线",
    )
    _validate_generalization_protection(generalization, generalization_materials)

    multi = results["multisource"]["observation"]
    multi_cases = multi["case_evidence"]
    _require(
        multi["questions"] == 3
        and multi["final_answer_accuracy"] == 1.0
        and multi["fact_delivery"]["complete"] is True
        and sum(int(item["truth_claims"]) for item in multi_cases) == 6
        and sum(int(item["delivered_truth_claims"]) for item in multi_cases) == 6
        and all(int(item["search_returned_sources"]) == int(item["expected_sources"]) for item in multi_cases)
        and all(int(item["read_sources"]) == int(item["expected_sources"]) for item in multi_cases),
        "多来源 3/3 或六项必要事实保护退化",
    )
    development = results["development"]["observation"]
    development_cases = development["case_evidence"]
    long_case = next((item for item in development_cases if item.get("coverage") == "long-session-multi-fact"), None)
    _require(
        development["questions"] == 4
        and development["final_answer_accuracy"] == 1.0
        and development["fact_delivery"]["complete"] is True
        and isinstance(long_case, dict)
        and int(long_case["truth_claims"]) == int(long_case["delivered_truth_claims"]) == 5,
        "原开发 4/4 或长多事实 5/5 保护退化",
    )
    regression = results["regression"]["observation"]
    _require(regression["questions"] == 8 and regression["final_answer_accuracy"] == 1.0 and regression["fact_delivery"]["complete"] is True, "固定回归 8/8 保护退化")
    protected = [item for item in development_cases if item.get("coverage") in {"long-session-multi-fact", "temporal-update-conflict", "structured-boundary"}]
    expected = sum(int(item["expected_sources"]) for item in protected)
    returned = sum(min(int(item["search_returned_sources"]), int(item["expected_sources"])) for item in protected)
    semantic_recall = returned / expected if expected else 0.0
    _require(semantic_recall == 1.0, "长资产语义召回保护退化")

    all_cases = [*semantic_cases, *fail_closed_cases, *generalization_cases, *multi_cases, *development_cases, *regression["case_evidence"]]
    read_units = max(int(item["selection"]["selected_units"]) for item in all_cases)
    context_chars = max(int(item["context_chars"]) for item in all_cases)
    _require(read_units <= int(route["gates"]["read_units_maximum"]), "质量保护读取数越界")
    _require(context_chars <= int(route["gates"]["context_chars_maximum"]), "质量保护上下文越界")
    _require(route["implementation"]["persistent_state_growth_bytes"] == 0, "检索时延路线引入持久状态")

    quality_root = _common_evidence_output_root({name: path for name, path in result_paths.items() if name not in {"semantic", "fail_closed"}})
    resume_proofs = {
        "multisource": stage4._prove_resume(suite_root, quality_root, subject_path, execution_config.resolve(), "development", inputs / "stage4-latency-multisource-input-v2.json", result_paths["multisource"], results["multisource"]),
        "development": stage4._prove_resume(suite_root, quality_root, subject_path, execution_config.resolve(), "development", inputs / "stage4-latency-development-input-v2.json", result_paths["development"], results["development"]),
        "regression": stage4._prove_resume(suite_root, quality_root, subject_path, execution_config.resolve(), "regression", inputs / "stage4-latency-regression-input-v2.json", result_paths["regression"], results["regression"]),
        "semantic": stage4._prove_resume(
            suite_root, _evidence_output_root(result_paths["semantic"]), subject_path,
            execution_config.resolve(), "integrated", inputs / "stage4-retrieval-semantic-protection-input-v4.json",
            result_paths["semantic"], results["semantic"],
        ),
        "fail_closed": stage4._prove_resume(
            suite_root, _evidence_output_root(result_paths["fail_closed"]), subject_path,
            execution_config.resolve(), "integrated", inputs / "stage4-retrieval-semantic-failclosed-input-v2.json",
            result_paths["fail_closed"], results["fail_closed"],
        ),
        "generalization": stage4._prove_resume(
            suite_root, _evidence_output_root(result_paths["generalization"]), subject_path,
            execution_config.resolve(), "integrated", inputs / "stage4-retrieval-semantic-generalization-input-v1.json",
            result_paths["generalization"], results["generalization"],
        ),
    }
    state_path = formal_state.resolve()
    state_before = evidence.file_sha256(state_path)
    _require(state_before == route["formal_state_sha256"], "检索时延终态前正式 state 漂移")
    state_after = evidence.file_sha256(state_path)
    _require(state_after == state_before, "检索时延终态改写了正式 state")
    wall = sum(float(result["observation"]["latency"]["wall_seconds"]) for result in results.values())
    _require(wall <= 600.0, "检索时延受影响质量反馈超过 600 秒")

    content = {
        "schema": RESULT_SCHEMA,
        "formal": False,
        "formal_state_written": False,
        "route_identity": route["identity"],
        "controller_sha256": evidence.file_sha256(Path(__file__).resolve()),
        "subject_identity": subject["identity"],
        "kernel_generation_identity": subject["content"]["kernel_generation_identity"],
        "kernel_effect_identity": subject["content"]["kernel_effect_identity"],
        "binary_sha256": evidence.file_sha256(runtime["binary"]),
        "performance_result": performance["identity"],
        "quality_results": {name: result["identity"] for name, result in results.items()},
        "metrics": {
            "retrieval_mean_ms": performance["metrics"]["candidate"]["mean_ms"],
            "retrieval_p95_ms": performance["metrics"]["candidate"]["p95_ms"],
            "wall_mean_ms": performance["metrics"]["candidate"]["wall_mean_ms"],
            "wall_p95_ms": performance["metrics"]["candidate"]["wall_p95_ms"],
            "previous_v2_mean_improvement_ms": -float(performance["candidate_minus_previous_v2_mean_ms"]),
            "previous_v2_p95_improvement_ms": -float(performance["candidate_minus_previous_v2_p95_ms"]),
            "semantic_protection_accuracy": semantic["final_answer_accuracy"],
            "fail_closed_accuracy": fail_closed["final_answer_accuracy"],
            "generalization_accuracy": generalization["final_answer_accuracy"],
            "multisource_accuracy": multi["final_answer_accuracy"],
            "multisource_delivered_truth_claims": 6,
            "multisource_truth_claims": 6,
            "development_accuracy": development["final_answer_accuracy"],
            "long_multifact_delivered_claims": 5,
            "long_multifact_truth_claims": 5,
            "regression_accuracy": regression["final_answer_accuracy"],
            "long_asset_semantic_recall": semantic_recall,
            "read_units_maximum": read_units,
            "context_chars_maximum": context_chars,
            "persistent_state_growth_bytes": 0,
            "affected_quality_wall_seconds": wall,
        },
        "resume": resume_proofs,
        "formal_state_sha256_before": state_before,
        "formal_state_sha256_after": state_after,
        "current_product_or_baseline_switched": False,
        "stage4_complete": False,
        "blind_gate_run": False,
        "passed": True,
    }
    value = {**content, "identity": evidence.canonical_sha256(content)}
    evidence.atomic_json(destination, value)
    return {**value, "reused": False, "path": str(destination)}


def _common_evidence_output_root(paths: dict[str, Path]) -> Path:
    roots = {path.parents[5] for path in paths.values()}
    _require(
        len(roots) == 1 and "kernel-v2-major-iteration" in next(iter(roots)).parts
        and "stage4-retrieval-latency" in next(iter(roots)).parts,
        "质量结果不属于同一隔离执行根",
    )
    return next(iter(roots))


def _evidence_output_root(path: Path) -> Path:
    root = path.parents[5]
    _require("kernel-v2-major-iteration" in root.parts and "stage4-retrieval-latency" in root.parts, "语义保护结果不属于隔离执行根")
    return root


def _validate_performance_attribution(performance: dict[str, Any], route: dict[str, Any]) -> None:
    transport = route["shared_evaluation"]["mcp_transport_sha256"]
    execution_identities = performance.get("execution_identities", {})
    _require(
        set(execution_identities) == {"v0", "previous-v2", "candidate"}
        and all(item.get("mcp_transport_sha256") == transport for item in execution_identities.values()),
        "三代性能没有绑定同一最终传输",
    )
    _require(
        performance.get("superseded_evidence", {}).get("eligible_for_candidate_decision") is False
        and performance.get("common_transport_benefit_counted_as_kernel_improvement") is False,
        "旧传输诊断或共同连接收益被错误计入内核结论",
    )


def _validate_semantic_protection(observation: dict[str, Any], materials: dict[str, Any], *, require_fast_path: bool) -> None:
    cases = observation["case_evidence"]
    expected_signals = [item["selection"]["expected_sources"][0]["channel_signals"] for item in cases]
    _require(
        observation["questions"] == 2
        and observation["final_answer_accuracy"] == 1.0
        and observation["fact_delivery"]["complete"] is True
        and all(len(item["sessions"]) == 24 for item in materials["cases"])
        and all(item["selection"]["returned_sources"] == 24 for item in cases)
        and all(item["search_returned_sources"] == item["read_sources"] == item["expected_sources"] == 1 for item in cases)
        and all("semantic" in signals for signals in expected_signals)
        and all(("semantic-organized-query" in signals) is require_fast_path for signals in expected_signals),
        "弱词法/强语义或意图区分保护退化",
    )


def _validate_fail_closed_protection(observation: dict[str, Any], materials: dict[str, Any]) -> None:
    cases = observation["case_evidence"]
    _require(len(cases) == 1 and len(materials["cases"]) == 1, "失败关闭保护题数漂移")
    case = cases[0]
    expected = case["selection"]["expected_sources"]
    _require(len(expected) == 1, "失败关闭保护目标来源数量漂移")
    signals = expected[0]["channel_signals"]
    _require(
        observation["questions"] == 1
        and observation["final_answer_accuracy"] == 1.0
        and observation["fact_delivery"]["complete"] is True
        and len(materials["cases"][0]["sessions"]) == 24
        and case["selection"]["returned_sources"] == 24
        and case["search_returned_sources"] == case["read_sources"] == case["expected_sources"] == 1
        and expected[0]["read"] is True
        and "semantic" in signals
        and "semantic-organized-query" not in signals,
        "无关键词复用来源没有经精确语义失败关闭路径完整交付",
    )


def _validate_generalization_protection(observation: dict[str, Any], materials: dict[str, Any]) -> None:
    cases = observation["case_evidence"]
    material_cases = materials["cases"]
    signals = [item["selection"]["expected_sources"][0]["channel_signals"] for item in cases]
    _require(
        observation["questions"] == 2
        and observation["final_answer_accuracy"] == 1.0
        and observation["fact_delivery"]["complete"] is True
        and len(material_cases) == 2
        and any("chinese" in item["coverage"] for item in material_cases)
        and any("spanish" in item["coverage"] for item in material_cases)
        and len({item["coverage"] for item in material_cases}) == 2
        and all(len(item["sessions"]) == 10 for item in material_cases)
        and all(item["selection"]["returned_sources"] == 10 for item in cases)
        and all(item["search_returned_sources"] == item["read_sources"] == item["expected_sources"] == 1 for item in cases)
        and all("semantic" in item and "semantic-organized-query" in item for item in signals),
        "跨语言跨领域的通用结构排序保护退化",
    )


def _validate_identity(value: dict[str, Any], schema: str, name: str) -> None:
    _require(value.get("schema") == schema, f"{name} schema 无效")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), f"{name}身份漂移")


def _load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise validation.KernelIterationValidationError(f"无法读取检索时延终态制品 {path}: {error}") from error
    _require(isinstance(value, dict), f"检索时延终态制品不是对象: {path}")
    return value


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise validation.KernelIterationValidationError(message)
