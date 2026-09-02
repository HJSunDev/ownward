from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import kernel_iteration_evidence as evidence
import kernel_iteration_stage4_resource_cost as resource_cost
import kernel_iteration_validation as validation


CONTRACT_SCHEMA = "ownward.kernel-iteration-stage4-resource-cost-real-capability-wall-contract/v1"
RESULT_SCHEMA = "ownward.kernel-iteration-stage4-resource-cost-real-capability-wall-audit/v1"
CONTRACT_PATH = Path("iteration/v2/stage4-resource-cost-real-capability-wall-contract.json")


def run(suite_root: Path, output_root: Path, formal_state: Path, *, resume: bool) -> dict[str, Any]:
    suite_root = suite_root.resolve()
    repository = suite_root.parents[2]
    output_root = output_root.resolve()
    _require(
        output_root.is_relative_to(repository / ".tmp" / "kernel-v2-major-iteration"),
        "真实语义能力/墙钟审计必须位于非正式 V2 边界",
    )
    contract = load_contract(suite_root)
    formal_state = formal_state.resolve()
    _require(formal_state == repository / contract["constraints"]["formal_state_path"], "正式 state 路径错绑")
    state_sha256 = evidence.file_sha256(formal_state)
    _require(state_sha256 == contract["constraints"]["formal_state_sha256"], "审计前正式 state 漂移")
    result_path = output_root / "result.json"
    if result_path.is_file():
        _require(resume, "真实语义能力/墙钟审计终态已存在；只有 --resume 可逐字复用")
        value = _load_json(result_path)
        _validate_identity(value, RESULT_SCHEMA, "真实语义能力/墙钟审计终态")
        _require(value["contract_identity"] == contract["identity"], "审计终态合同错绑")
        _require(value["formal_state_sha256"] == state_sha256, "审计恢复时正式 state 漂移")
        return {**value, "path": str(result_path), "reused": True, "model_executions": 0, "product_executions": 0}

    token = audit_real_semantic_capability(suite_root, contract)
    wall = audit_wall_headroom(repository, contract)
    _require(evidence.file_sha256(formal_state) == state_sha256, "只读审计改写了正式 state")
    content = {
        "schema": RESULT_SCHEMA,
        "formal": False,
        "formal_state_written": False,
        "contract_identity": contract["identity"],
        "semantic_input_tokens": token,
        "end_to_end_wall_seconds": wall,
        "ownward_data_bytes": contract["closed_dimensions"]["ownward_data_bytes"],
        "stage4_complete": False,
        "implementation_performed": False,
        "implementation_rejected_before_code_change": wall["authorized_routes"] == [],
        "next_validation": (
            "instrument-candidate-only-create-batch-subphases-and-separate-exact-document-embedding-inference-"
            "from-authority-and-derived-durable-writes-before-authorizing-one-route"
        ),
        "formal_state_sha256": state_sha256,
    }
    _require(token["passed"] is True, "真实语义能力没有闭合 Token 组件门")
    _require(contract["closed_dimensions"]["ownward_data_bytes"]["passed"] is True, "产品数据字节门没有保持闭合")
    value = {**content, "identity": evidence.canonical_sha256(content)}
    output_root.mkdir(parents=True, exist_ok=True)
    evidence.atomic_json(result_path, value)
    return {**value, "path": str(result_path), "reused": False, "model_executions": 0, "product_executions": 0}


def audit_real_semantic_capability(suite_root: Path, contract: dict[str, Any]) -> dict[str, Any]:
    repository = suite_root.parents[2]
    sources = contract["semantic_capability"]["sources"]
    balanced = _verified_source(repository, sources["balanced_review"], "紧凑语义同窗复核")
    manifest = _verified_source(repository, sources["representation_manifest"], "语义表示清单")
    receipt = _verified_source(repository, sources["candidate_receipt"], "真实候选收据")
    composition = _verified_source(repository, sources["candidate_composition"], "真实候选组合")
    subject = evidence.validate_v2_subject(
        evidence.load_contract(suite_root),
        _verified_source(repository, sources["candidate_subject"], "真实候选 subject"),
    )
    _require(receipt["subject_identity"] == subject["identity"] == contract["semantic_capability"]["subject_identity"], "真实候选身份错绑")
    _require(receipt["composition_identity"] == composition["identity"], "真实候选组合身份错绑")
    _require(receipt["semantic_representation"] == manifest["representation"], "真实候选语义表示错绑")
    _require(manifest["instruction_identity"] == contract["semantic_capability"]["instruction_identity"], "紧凑语义指令漂移")
    semantic = next((item for item in composition.get("components", []) if item.get("role") == "semantic"), None)
    _require(isinstance(semantic, dict), "真实候选组合缺少语义组件")
    config = semantic.get("config")
    _require(isinstance(config, dict), "真实候选语义组件配置无效")
    _require(config.get("input_representation") == manifest["representation"], "候选组合没有声明真实语义表示")
    _require(config.get("input_representation_manifest_identity") == manifest["identity"], "候选组合语义清单错绑")

    module = validation._load_longmemeval_module(suite_root)
    semantic_contract = module.semantic_representation.load_contract(repository / sources["representation_manifest"]["path"])
    protocol = _load_json(repository / contract["semantic_capability"]["protocol_path"])
    capability = module.ExternalIntelligenceCapability(None, semantic_contract)
    prompt_matches = 0
    source_plans = contract["semantic_capability"]["balanced_source_plans"]
    runs = Path(contract["runtime"]["runs_root"])
    balanced_requests = repository / sources["balanced_review"]["request_root"]
    for material, plan in source_plans.items():
        root = runs / "kernel-iteration" / plan["plan_identity"] / "run"
        _require(resource_cost._tree_identity(root) == plan["run_root_sha256"], f"Token 来源运行漂移: {material}")
        for work_path in sorted((root / "questions").glob("*/semantic-traces/*/work.json")):
            frozen = _load_json(work_path)
            work = frozen.get("work")
            _require(isinstance(work, list) and work, "Token 来源语义工作无效")
            prompt, schema, _work_ids = capability.semantic_request(work, protocol["memory"])
            question_id = work_path.parents[2].name
            request_path = balanced_requests / material / question_id / evidence.canonical_sha256(schema) / "request.json"
            request = _load_json(request_path)
            _require(request.get("prompt_sha256") == module.hashlib.sha256(prompt.encode("utf-8")).hexdigest(), f"当前通用执行器与 Token 请求不等价: {question_id}")
            _require(request.get("output_schema_sha256") == evidence.canonical_sha256(schema), f"当前通用执行器输出 Schema 漂移: {question_id}")
            prompt_matches += 1
    _require(prompt_matches == int(contract["semantic_capability"]["source_calls"]), "Token 请求闭合数不完整")

    execution_config = repository / contract["semantic_capability"]["execution_config_path"]
    runtime = validation.validate_execution_config(suite_root, execution_config)
    results: dict[str, dict[str, Any]] = {}
    trace = {"analysis_calls": 0, "work_items": 0, "fact_equivalence_valid": True}
    quality_root = repository / contract["semantic_capability"]["quality_root"]
    for name in ("development", "regression"):
        item = sources[f"{name}_result"]
        result_path = repository / item["path"]
        before = result_path.read_bytes()
        result = _verified_source(repository, item, f"真实候选 {name} 结果")
        _require(result.get("passed") is True and result.get("subject_identity") == subject["identity"], f"真实候选 {name} 未通过")
        run_root = runtime["runs"] / "kernel-iteration" / result["plan_identity"] / "run"
        _require(resource_cost._tree_identity(run_root) == item["run_root_sha256"], f"真实候选 {name} 运行漂移")
        for question_root in sorted((run_root / "questions").iterdir()):
            if not question_root.is_dir():
                continue
            work_by_id: dict[str, dict[str, Any]] = {}
            for work_path in question_root.glob("semantic-traces/*/work.json"):
                for work in _load_json(work_path)["work"]:
                    work_by_id[str(work["id"])] = work
            plan = _load_json(question_root / "semantic-plan.json")
            _require(plan["transport"]["representation"] == manifest["representation"], "真实运行没有消费组合声明的语义表示")
            _require(plan["transport"]["representation_manifest_identity"] == manifest["identity"], "真实运行语义表示清单错绑")
            for input_path in question_root.glob("semantic-traces/_analysis/*/unit-*/input.json"):
                semantic_input = _load_json(input_path)
                selected = [work_by_id[str(work_id)] for work_id in semantic_input["work_ids"]]
                semantic_contract.validate(selected, semantic_input["representation"])
                _require(semantic_contract.fact_identity(selected) == semantic_input["fact_equivalence_sha256"], "真实运行语义事实身份漂移")
                trace["analysis_calls"] += 1
                trace["work_items"] += len(selected)
        input_manifest = repository / contract["semantic_capability"]["input_manifests"][name]
        resumed = validation.execute_prepared_evidence(
            suite_root,
            quality_root,
            execution_config,
            subject_manifest=repository / sources["candidate_subject"]["path"],
            evidence_type=name,
            input_manifest=input_manifest,
            resume=True,
        )
        _require(resumed["reused_execution"] is True, f"真实候选 {name} 恢复执行了产品或模型")
        _require(result_path.read_bytes() == before, f"真实候选 {name} 恢复没有逐字复用")
        results[name] = result
    _require(trace["analysis_calls"] == 12 and trace["work_items"] == 46, "真实语义能力没有覆盖冻结 12 调用/46 工作")
    expected = contract["semantic_capability"]["quality"]
    _require(results["development"]["observation"]["final_answer_accuracy"] == 1.0, "真实语义能力开发质量未通过")
    _require(results["regression"]["observation"]["final_answer_accuracy"] == 1.0, "真实语义能力回归质量未通过")
    _require(results["development"]["observation"]["questions"] == expected["development_questions"], "开发题数漂移")
    _require(results["regression"]["observation"]["questions"] == expected["regression_questions"], "回归题数漂移")
    _require(balanced["token"]["candidate_component_tokens"] <= balanced["token"]["candidate_component_maximum"], "紧凑 Token 组件未通过")
    return {
        "component": "generic-semantic-instruction-plus-work-payload-native-usage-delta",
        "candidate_component_tokens": int(balanced["token"]["candidate_component_tokens"]),
        "candidate_component_maximum": float(balanced["token"]["candidate_component_maximum"]),
        "passed": True,
        "selection": "candidate-composition-declared",
        "representation": manifest["representation"],
        "representation_manifest_identity": manifest["identity"],
        "candidate_subject_identity": subject["identity"],
        "candidate_composition_identity": composition["identity"],
        "balanced_request_prompt_and_schema_matches": prompt_matches,
        "generic_execution_trace": trace,
        "quality": "development-4/4-long-multifact-5/5-regression-8/8",
        "resume": "byte-identical-zero-model-zero-product-execution",
        "special_candidate_flag": False,
        "wrapper_or_monkeypatch": False,
    }


def audit_wall_headroom(repository: Path, contract: dict[str, Any]) -> dict[str, Any]:
    wall_contract = contract["wall"]
    runs = Path(contract["runtime"]["runs_root"])
    critical = wall_contract["critical_question_ids"]
    samples = []
    reference_counts: dict[str, Any] | None = None
    for repeat in wall_contract["same_binary_repeats"]:
        create_seconds = 0.0
        counts = {"questions": 0, "assets": 0, "short_documents": 0, "long_documents": 0, "embedding_calls": 0, "authority_batch_calls": 0, "derived_batch_writes": 0}
        for material, question_ids in critical.items():
            plan = repeat["plans"][material]
            root = runs / "kernel-iteration" / plan["plan_identity"] / "run"
            _require(resource_cost._tree_identity(root) == plan["run_root_sha256"], f"墙钟重复来源漂移: {repeat['name']}/{material}")
            identity = _load_json(root / "identity.json")
            _require(identity.get("binary_sha256") == repeat["product_binary_sha256"], f"墙钟重复产品二进制不同尺: {repeat['name']}")
            _require(identity.get("input_manifest_sha256") == wall_contract["input_manifest_sha256"][material], f"墙钟重复输入不同尺: {repeat['name']}/{material}")
            _require(identity.get("protocol_sha256") == wall_contract["protocol_sha256"], f"墙钟重复协议不同尺: {repeat['name']}/{material}")
            for question_id in question_ids:
                question_root = root / "questions" / question_id
                question = _load_json(question_root / "result.json")
                create_seconds += float(question["phase_seconds"]["create"])
                sizes = []
                for work_path in question_root.glob("semantic-traces/*/work.json"):
                    sizes.extend(len(str(item["asset"]["content"]).encode("utf-8")) for item in _load_json(work_path)["work"])
                counts["questions"] += 1
                counts["assets"] += len(sizes)
                short = [size for size in sizes if size <= int(wall_contract["semantic_embedding_chunk_bytes"])]
                counts["short_documents"] += len(short)
                counts["long_documents"] += len(sizes) - len(short)
                counts["embedding_calls"] += _bounded_group_count(short, int(wall_contract["semantic_embedding_chunk_bytes"]))
                counts["authority_batch_calls"] += 1
                counts["derived_batch_writes"] += 1
        if reference_counts is None:
            reference_counts = counts
        else:
            _require(counts == reference_counts, "墙钟重复的创建工作量不同尺")
        samples.append({"name": repeat["name"], "create_seconds": create_seconds, "counts": counts})
    create_values = [item["create_seconds"] for item in samples]
    repeatability_error = max(create_values) - min(create_values)
    _require(abs(repeatability_error - float(wall_contract["frozen_repeatability_error_seconds"])) <= 1e-9, "冻结创建重复误差漂移")
    _require(abs(samples[0]["create_seconds"] - float(wall_contract["current_create_seconds"])) <= 1e-9, "当前创建关键链漂移")
    minimum_improvement = float(wall_contract["minimum_improvement_seconds"])
    authorization_minimum = minimum_improvement + repeatability_error

    calibration = _verified_source(repository, wall_contract["vector_runtime_calibration"], "向量运行时校准")
    native = next(
        (item for item in calibration["configurations"] if int(item["threads"]) == 2 and int(item["parallel"]) == 1),
        None,
    )
    _require(isinstance(native, dict), "向量运行时校准缺少产品原生 2/1")
    cold_start_upper_bound = float(native["startup_ms"]) / 1000.0 * int(reference_counts["questions"])
    routes = [
        {
            "name": "eliminate-per-question-vector-cold-start-only",
            "maximum_evidenced_improvement_seconds": cold_start_upper_bound,
            "required_with_repeatability_margin_seconds": authorization_minimum,
            "authorized": cold_start_upper_bound >= authorization_minimum,
            "reason": "even removing every measured cold start is insufficient; moving it outside the measured path would only relabel cost",
        },
        {
            "name": "merge-existing-document-request-boundaries-only",
            "reducible_request_boundaries": int(reference_counts["embedding_calls"] - reference_counts["questions"]),
            "authorized": False,
            "reason": "all exact document vectors remain mandatory and existing receipts do not prove boundary overhead near the required margin",
        },
        {
            "name": "authority-or-derived-durable-write-change",
            "authorized": False,
            "reason": "the immutable trace times the CreateBatch envelope only; no subphase receipt proves either durable write can supply the required margin",
        },
    ]
    authorized = [item["name"] for item in routes if item["authorized"]]
    return {
        "component": "local-create-retrieval-and-semantic-submit-critical-path",
        "v0_candidate_component_baseline_seconds": float(wall_contract["v0_candidate_component_baseline_seconds"]),
        "candidate_component_maximum_seconds": float(wall_contract["candidate_component_maximum_seconds"]),
        "current_candidate_component_seconds": float(wall_contract["current_candidate_component_seconds"]),
        "current_create_seconds": float(wall_contract["current_create_seconds"]),
        "minimum_improvement_seconds": minimum_improvement,
        "frozen_repeatability_error_seconds": repeatability_error,
        "route_authorization_minimum_seconds": authorization_minimum,
        "same_binary_create_samples": samples,
        "mechanical_work_decomposition": reference_counts,
        "known_native_vector_cold_start_seconds_per_question": float(native["startup_ms"]) / 1000.0,
        "known_cold_start_upper_bound_seconds": cold_start_upper_bound,
        "subphase_timing_status": "not-identifiable-from-existing-create-batch-envelope",
        "phase_sum_used_as_wall": False,
        "routes": routes,
        "authorized_routes": authorized,
        "passed": False,
        "status": "open-no-route-has-proven-mathematical-margin",
    }


def load_contract(suite_root: Path) -> dict[str, Any]:
    value = _load_json(suite_root / CONTRACT_PATH)
    _validate_identity(value, CONTRACT_SCHEMA, "真实语义能力/墙钟合同")
    return value


def _bounded_group_count(sizes: list[int], maximum: int) -> int:
    groups, current = 0, 0
    for size in sizes:
        if current and current + size > maximum:
            groups += 1
            current = 0
        current += size
    return groups + int(current > 0)


def _verified_source(repository: Path, item: dict[str, Any], name: str) -> dict[str, Any]:
    path = repository / item["path"]
    _require(path.is_file() and evidence.file_sha256(path) == item["sha256"], f"{name}文件漂移")
    value = _load_json(path)
    if "identity" in item:
        _require(value.get("identity") == item["identity"], f"{name}身份错绑")
    return value


def _validate_identity(value: dict[str, Any], schema: str, name: str) -> None:
    _require(value.get("schema") == schema, f"{name} schema 无效")
    _require(value.get("identity") == evidence.canonical_sha256({key: item for key, item in value.items() if key != "identity"}), f"{name}身份漂移")


def _load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise validation.KernelIterationValidationError(f"无法读取真实语义能力/墙钟证据 {path}: {error}") from error
    _require(isinstance(value, dict), f"真实语义能力/墙钟证据不是对象: {path}")
    return value


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise validation.KernelIterationValidationError(message)
