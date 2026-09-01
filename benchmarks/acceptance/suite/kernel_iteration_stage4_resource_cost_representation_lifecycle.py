from __future__ import annotations

import hashlib
import json
import re
from pathlib import Path
from typing import Any

import kernel_iteration_evidence as evidence
import kernel_iteration_validation as validation


CONTRACT_SCHEMA = "ownward.kernel-iteration-stage4-resource-cost-representation-lifecycle-contract/v1"
RESULT_SCHEMA = "ownward.kernel-iteration-stage4-resource-cost-representation-lifecycle-feasibility/v1"
CONTRACT_PATH = Path("iteration/v2/stage4-resource-cost-representation-lifecycle-contract.json")
DEPENDENCY_MIGRATION_SCHEMA = "ownward.kernel-iteration-direct-dependency-migration/v1"
DEPENDENCY_MIGRATION_PATH = Path("iteration/v2/stage4-resource-cost-raw-vector-lifecycle-dependency-migration.json")
DEPENDENCY_MIGRATION_REASON = "version-suite-cli-dispatch-preserves-frozen-stage4-cost-and-representation"
SOURCE_SESSION = re.compile(r"(?:^|\n)Source session: ([^\n]+)")


def run(
    suite_root: Path,
    output_root: Path,
    execution_config: Path,
    formal_state: Path,
    *,
    resume: bool,
) -> dict[str, Any]:
    suite_root = suite_root.resolve()
    repository = suite_root.parents[2]
    output_root = output_root.resolve()
    _require(
        output_root.is_relative_to(repository / ".tmp" / "kernel-v2-major-iteration"),
        "pending/ready 表示可行性证据必须位于非正式 V2 边界",
    )
    contract = load_contract(suite_root)
    formal_state = formal_state.resolve()
    _require(formal_state == repository / contract["formal_state"]["path"], "正式 state 路径错绑")
    state_sha256 = evidence.file_sha256(formal_state)
    _require(state_sha256 == contract["formal_state"]["sha256"], "表示可行性审计前正式 state 漂移")
    result_path = output_root / "result.json"
    if result_path.is_file():
        _require(resume, "pending/ready 表示可行性终态已存在；只有 --resume 可逐字复用")
        value = _load_json(result_path)
        _validate_identity(value, RESULT_SCHEMA, "pending/ready 表示可行性终态")
        _require(value["contract_identity"] == contract["identity"], "表示可行性终态合同错绑")
        _require(value["formal_state_sha256"] == state_sha256, "表示可行性恢复时正式 state 漂移")
        return {**value, "path": str(result_path), "reused": True, "model_executions": 0, "product_executions": 0}

    runtime = validation.validate_execution_config(suite_root, execution_config.resolve())
    sources = {
        name: _verified_text(repository, item, name)
        for name, item in contract["source_files"].items()
    }
    matched = _verified_json(repository, contract["evidence"]["matched_create"], "对称 CreateBatch")
    non_create = _verified_json(repository, contract["evidence"]["non_create"], "非 CreateBatch 分解")
    simple_deferral = _verified_json(repository, contract["evidence"]["simple_deferral_audit"], "简单挪时审计")
    materials = [
        _audit_material(repository, runtime["runs"], item)
        for item in contract["quality_feasibility"]["materials"]
    ]
    result = evaluate(contract, sources, matched, non_create, simple_deferral, materials, state_sha256)
    _require(evidence.file_sha256(formal_state) == state_sha256, "表示可行性审计改写正式 state")
    output_root.mkdir(parents=True, exist_ok=True)
    evidence.atomic_json(result_path, result)
    return {**result, "path": str(result_path), "reused": False, "model_executions": 0, "product_executions": 0}


def evaluate(
    contract: dict[str, Any],
    sources: dict[str, str],
    matched: dict[str, Any],
    non_create: dict[str, Any],
    simple_deferral: dict[str, Any],
    materials: list[dict[str, Any]],
    state_sha256: str,
) -> dict[str, Any]:
    _verify_source_boundaries(sources)
    _require(simple_deferral["decision"] == "reject-raw-vector-deferral-before-implementation-and-keep-stage4-open", "简单挪时拒绝结论漂移")
    _require(simple_deferral["authorization"]["candidate_built"] is False, "简单挪时审计不应包含候选")
    gate = contract["cost_gate"]
    observed_gate = matched["candidate_controlled_gate"]
    for field in (
        "v0_controlled_baseline_seconds",
        "controlled_half_maximum_seconds",
        "current_v2_controlled_seconds",
        "current_plus_error_seconds",
        "repeatability_error_seconds",
    ):
        _require(abs(float(gate[field]) - float(observed_gate[field])) <= 1e-12, f"活动墙钟门漂移: {field}")
    _require(
        abs(float(gate["required_net_improvement_seconds"]) - float(non_create["active_gate"]["required_improvement_seconds"])) <= 1e-12,
        "活动净改善缺口漂移",
    )
    observed_raw = float(matched["shared_cost_classification"]["v2_embedding_after_common_startup_seconds"])
    _require(abs(observed_raw - float(gate["observed_short_raw_inference_after_common_startup_seconds"])) <= 1e-12, "原始正文推理包络漂移")

    cases = sum(int(item["cases"]) for item in materials)
    truth_claims = sum(int(item["truth_claims"]) for item in materials)
    semantic_work = sum(int(item["semantic_work_items"]) for item in materials)
    semantic_only_candidates = sum(int(item["semantic_similarity_candidates"]) for item in materials)
    relations = sum(int(item["accepted_relations"]) for item in materials)
    overlap_ids = set(str(value) for value in gate["overlap_question_ids"])
    overlap_walls = [
        float(value)
        for item in materials
        for question_id, value in item["semantic_request_wall_seconds_by_question"].items()
        if question_id in overlap_ids
    ]
    _require(len(overlap_walls) == len(overlap_ids), "匹配墙钟问题的语义请求收据不完整")
    min_semantic_wall = min(overlap_walls)
    _require(min_semantic_wall >= float(gate["minimum_existing_semantic_request_wall_seconds"]), "现有语义请求没有覆盖封存的后台向量上界")
    _require(all(item["all_required_truth_sources_are_work_assets"] for item in materials), "存在只来自辅助候选的必要事实")
    _require(all(item["all_current_work_assets_have_full_authority_bodies"] for item in materials), "语义工作缺少当前权威正文")
    _require(all(item["all_current_work_items_have_accepted_analysis"] for item in materials), "现有材料缺少可审计的语义结果")

    overhang = max(0.0, float(gate["maximum_existing_create_call_critical_path_seconds"]) - min_semantic_wall)
    current_controlled = float(gate["current_v2_controlled_seconds"])
    projected_controlled = (
        current_controlled
        - observed_raw
        + overhang
        + float(gate["maximum_new_lifecycle_bookkeeping_seconds"])
    )
    projected_with_error = projected_controlled + float(gate["repeatability_error_seconds"])
    projected_net_improvement = current_controlled - projected_controlled
    math_passed = (
        projected_net_improvement >= float(gate["required_net_improvement_seconds"])
        and projected_with_error <= float(gate["controlled_half_maximum_seconds"])
    )
    _require(math_passed, "pending/ready 生命周期没有达到实现前净成本下界")

    content = {
        "schema": RESULT_SCHEMA,
        "formal": False,
        "formal_state_written": False,
        "contract_identity": contract["identity"],
        "external_contract": {
            "candidate_and_work_reference_may_change": True,
            "candidate_change_requires_new_subject": True,
            "required_fact_delivery_and_final_answers_remain_frozen": True,
            "simple_exact-work-deferral_audit_preserved": simple_deferral["identity"],
            "simple_deferral_is_not_used_to_reject_new_subject_semantics": True,
        },
        "lifecycle": contract["representation_lifecycle"],
        "quality_feasibility": {
            "materials": materials,
            "cases": cases,
            "truth_claims": truth_claims,
            "semantic_work_items": semantic_work,
            "semantic_similarity_candidates_in_current_work": semantic_only_candidates,
            "accepted_relations_in_current_results": relations,
            "pending_work_fact_sufficiency": "passed-read-only",
            "pending_query_semantics": "join-exact-current-revision-vector-or-existing-explicit-lexical-failure-open",
            "ready_query_semantics": "same-model-space-and-exact-document-vector",
            "candidate_quality_status": "not-yet-measured-requires-new-subject-execution",
        },
        "cost_feasibility": {
            "current_v2_controlled_seconds": current_controlled,
            "observed_raw_inference_after_common_startup_seconds": observed_raw,
            "minimum_existing_external_semantic_request_wall_seconds": min_semantic_wall,
            "maximum_existing_create_call_critical_path_seconds": float(gate["maximum_existing_create_call_critical_path_seconds"]),
            "background_overhang_seconds": overhang,
            "maximum_new_lifecycle_bookkeeping_seconds": float(gate["maximum_new_lifecycle_bookkeeping_seconds"]),
            "projected_controlled_seconds": projected_controlled,
            "projected_with_repeatability_error_seconds": projected_with_error,
            "controlled_half_maximum_seconds": float(gate["controlled_half_maximum_seconds"]),
            "projected_net_improvement_seconds": projected_net_improvement,
            "required_net_improvement_seconds": float(gate["required_net_improvement_seconds"]),
            "generation_cost_counted": True,
            "migration_cost": "zero-for-independent-empty-candidate-generation; existing states are not migrated",
            "first_ready_query_cost": "zero-additional-when-the-sealed-job-completed-during-semantic-analysis; otherwise-the-query-joins-and-the-cost-remains-accounted",
            "failure_recovery_cost": "on-demand-current-revision-regeneration-is-explicit-and-not-a-success-path-credit",
            "passed": math_passed,
        },
        "authorization": {
            "lifecycle_contract_frozen": True,
            "read_only_quality_feasibility_passed": True,
            "net_mathematical_margin_passed": math_passed,
            "candidate_implementation_authorized": True,
            "candidate_measurement_authorized": True,
            "authorized_route": "sealed-current-revision-raw-vector-job-overlapped-with-external-semantic-analysis-and-atomically-promoted-with-the-accepted-receipt",
            "candidate_must_receive_new_subject_identity": True,
        },
        "execution": {
            "model_executions": 0,
            "product_executions": 0,
            "candidate_builds": 0,
            "read_only_existing_traces_only": True,
        },
        "formal_state_sha256": state_sha256,
        "decision": "authorize-one-independent-v2-lifecycle-candidate-subject-to-full-external-quality-and-ab-ba-gates",
        "next_validation": "implement-the-sealed-revision-bound-vector-lifecycle-without-changing-the-current-product-then-run-lifecycle-failure-tests-before-directly-invalidated-quality-and-ab-ba",
    }
    return {**content, "identity": evidence.canonical_sha256(content)}


def load_contract(suite_root: Path) -> dict[str, Any]:
    repository = suite_root.parents[2]
    value = _load_json(suite_root / CONTRACT_PATH)
    _validate_identity(value, CONTRACT_SCHEMA, "pending/ready 表示生命周期合同")
    _require(value.get("frozen_before_candidate_implementation") is True, "表示生命周期合同未在候选实现前冻结")
    _require(value.get("frozen_before_candidate_measurement") is True, "表示生命周期合同未在候选测量前冻结")
    _require(value.get("candidate_results_seen") is False, "表示生命周期合同错误声明已看到候选结果")
    drifted = {}
    for item in value["source_files"].values():
        path = repository / item["path"]
        current = evidence.file_sha256(path) if path.is_file() else None
        if current != item["sha256"]:
            drifted[item["path"]] = {"frozen": item["sha256"], "current": current}
    if drifted:
        _verify_dependency_migration(
            suite_root, value["identity"], drifted,
            {"benchmarks/longmemeval_s/run.py": "source-context-consumer-and-reader-profile-only-semantic-request-and-frozen-cost-unchanged"},
        )
    return value


def _verify_dependency_migration(
    suite_root: Path,
    contract_identity: str,
    drifted: dict[str, dict[str, str | None]],
    expected_classifications: dict[str, str],
) -> None:
    migration = _load_json(suite_root / DEPENDENCY_MIGRATION_PATH)
    _validate_identity(migration, DEPENDENCY_MIGRATION_SCHEMA, "Stage 4 精确依赖迁移收据")
    _require(migration.get("reason") == DEPENDENCY_MIGRATION_REASON, "Stage 4 精确依赖迁移原因漂移")
    related = migration.get("related_contract_migrations", {}).get(contract_identity, {})
    changes = {
        item["path"]: {"frozen": item["frozen_sha256"], "current": item["current_sha256"]}
        for item in related.get("changes", [])
    }
    classifications = {item["path"]: item.get("classification") for item in related.get("changes", [])}
    _require(changes == drifted, "表示生命周期源码漂移不在精确迁移收据内")
    _require(classifications == expected_classifications, "表示生命周期依赖迁移分类漂移")
    _require(related.get("preserved") == {
        "contract_identity": True,
        "thresholds": True,
        "evidence_identities": True,
        "formal_state_sha256": True,
    }, "表示生命周期依赖迁移保护边界漂移")


def _audit_material(repository: Path, runs_root: Path, item: dict[str, Any]) -> dict[str, Any]:
    material_path = repository / item["path"]
    _require(material_path.is_file() and evidence.file_sha256(material_path) == item["sha256"], f"表示可行性材料漂移: {item['name']}")
    material = _load_json(material_path)
    _validate_identity(material, "ownward.kernel-iteration-materials/v2", f"表示可行性材料 {item['name']}")
    cases = {str(case["case_id"]): case for case in material["cases"]}
    question_root = runs_root / "kernel-iteration" / item["plan_identity"] / "run" / "questions"
    _require(question_root.is_dir(), f"表示可行性运行现场缺失: {item['name']}")
    observed_questions = {path.name for path in question_root.iterdir() if path.is_dir()}
    _require(observed_questions == set(cases), f"表示可行性问题集合漂移: {item['name']}")
    semantic_wall: list[float] = []
    semantic_wall_by_question: dict[str, float] = {}
    work_items = 0
    semantic_similarity_candidates = 0
    accepted_relations = 0
    truth_claims = 0
    full_bodies = True
    truth_sources_are_work = True
    all_analyses = True
    trace_hasher = hashlib.sha256()
    for question_id in sorted(cases):
        question = cases[question_id]
        question_path = question_root / question_id
        bodies: dict[str, str] = {}
        sessions: dict[str, str] = {}
        works: dict[str, str] = {}
        for input_path in sorted((question_path / "semantic-traces" / "_analysis").glob("*/unit-*/input.json")):
            encoded = input_path.read_bytes()
            trace_hasher.update(input_path.relative_to(question_root).as_posix().encode("utf-8") + b"\0" + encoded)
            representation = json.loads(encoded.decode("utf-8"))["representation"]
            for body in representation["bodies"]:
                asset_id, content = str(body["id"]), str(body["content"])
                if asset_id in bodies:
                    _require(bodies[asset_id] == content, f"同一资产正文在语义单元间漂移: {question_id}")
                bodies[asset_id] = content
                match = SOURCE_SESSION.search(content)
                if match:
                    sessions[match.group(1)] = asset_id
            for work in representation["work"]:
                work_id = str(work["work_id"])
                asset = work["asset"]
                asset_id = str(asset["id"])
                works[work_id] = asset_id
                full_bodies = full_bodies and str(asset["body_ref"]) != "" and asset_id in bodies
                semantic_similarity_candidates += sum(1 for candidate in work["candidates"] if "semantic_similarity" in candidate)
            complete = _load_json(input_path.parent / "codex" / "complete.json")
            request_wall = float(complete["usage"]["wall_seconds"])
            semantic_wall.append(request_wall)
            semantic_wall_by_question[question_id] = semantic_wall_by_question.get(question_id, 0.0) + request_wall
        analyses: dict[str, str] = {}
        for analysis_path in sorted((question_path / "semantic-traces").glob("*/analysis.json")):
            encoded = analysis_path.read_bytes()
            trace_hasher.update(analysis_path.relative_to(question_root).as_posix().encode("utf-8") + b"\0" + encoded)
            analysis = json.loads(encoded.decode("utf-8"))
            for submission in analysis["submissions"]:
                work_id = str(submission["work_id"])
                analyses[work_id] = str(submission["asset_id"])
                accepted_relations += len(submission["analysis"].get("relations", []))
        all_analyses = all_analyses and set(analyses) == set(works) and all(analyses[key] == value for key, value in works.items())
        work_assets = set(works.values())
        for claim in question["truth_claims"]:
            truth_claims += 1
            for session_id in claim["evidence_session_ids"]:
                asset_id = sessions.get(str(session_id))
                truth_sources_are_work = truth_sources_are_work and asset_id is not None and asset_id in work_assets
        for session_id in question["answer_session_ids"]:
            asset_id = sessions.get(str(session_id))
            truth_sources_are_work = truth_sources_are_work and asset_id is not None and asset_id in work_assets
        work_items += len(works)
    _require(bool(semantic_wall), f"表示可行性材料没有语义请求收据: {item['name']}")
    return {
        "name": item["name"],
        "material_identity": material["identity"],
        "plan_identity": item["plan_identity"],
        "execution_result_identity": item["execution_result_identity"],
        "trace_input_and_analysis_sha256": trace_hasher.hexdigest(),
        "cases": len(cases),
        "truth_claims": truth_claims,
        "semantic_work_items": work_items,
        "semantic_similarity_candidates": semantic_similarity_candidates,
        "accepted_relations": accepted_relations,
        "semantic_request_wall_seconds": semantic_wall,
        "semantic_request_wall_seconds_by_question": semantic_wall_by_question,
        "all_required_truth_sources_are_work_assets": truth_sources_are_work,
        "all_current_work_assets_have_full_authority_bodies": full_bodies,
        "all_current_work_items_have_accepted_analysis": all_analyses,
    }


def _verify_source_boundaries(sources: dict[str, str]) -> None:
    service = sources["service"]
    collaboration = sources["collaboration"]
    generation = sources["generation"]
    store = sources["derived_store"]
    runner = sources["runner"]
    for anchor in (
        "states = s.prepareSemanticWorkBatch(ctx, values)",
        "queryVector, err := s.embedder.EmbedQuery(ctx, input.Query)",
        "records, err := derivedStore.AllWithEmbeddings()",
    ):
        _require(anchor in service, f"表示生命周期服务锚点漂移: {anchor}")
    for anchor in (
        "generated, embeddingErr := s.embedder.EmbedDocuments(ctx, contents[offset:end])",
        "candidates := s.semanticCandidates(value, vectors, indexes...)",
        "return semantics.ResolveWork(*record.SemanticWorkReference, asset, candidates)",
        "if len(record.Embedding) == 0 && len(recovery.vector) > 0",
    ):
        _require(anchor in collaboration, f"表示生命周期协作锚点漂移: {anchor}")
    _require("previous.EmbeddingSpace == s.embedder.Space().ID" in generation, "重建没有封存向量空间")
    _require("Embedding             []float32" in store and "SemanticReceipt" in store, "派生记录没有同时封存表示与语义收据")
    create = runner.find('runtime.client.call_tool("ownward_create_batch"')
    semantic = runner.find("freeze_semantic_batch(runtime", create)
    submit = runner.find("submit_semantic_batch(", semantic)
    retrieve = runner.find("retrieve(runtime", submit)
    _require(create >= 0 and semantic > create and submit > semantic and retrieve > submit, "执行顺序不是 create->semantic->submit->query")


def _verified_text(repository: Path, item: dict[str, Any], name: str) -> str:
    path = repository / item["path"]
    current = evidence.file_sha256(path) if path.is_file() else None
    if current != item["sha256"]:
        _verify_dependency_migration(
            repository / "benchmarks" / "acceptance" / "suite",
            "e39272da7f832ed8275f99284aa03ad8fdf1b68b7833a368b9bece116ef93ce8",
            {item["path"]: {"frozen": item["sha256"], "current": current}},
            {"benchmarks/longmemeval_s/run.py": "source-context-consumer-and-reader-profile-only-semantic-request-and-frozen-cost-unchanged"},
        )
    return path.read_text(encoding="utf-8")


def _verified_json(repository: Path, item: dict[str, Any], name: str) -> dict[str, Any]:
    path = repository / item["path"]
    _require(path.is_file() and evidence.file_sha256(path) == item["sha256"], f"{name}文件漂移")
    value = _load_json(path)
    _require(value.get("identity") == item["identity"], f"{name}身份错绑")
    return value


def _validate_identity(value: dict[str, Any], schema: str, name: str) -> None:
    _require(value.get("schema") == schema, f"{name} schema 无效")
    content = {key: item for key, item in value.items() if key != "identity"}
    _require(value.get("identity") == evidence.canonical_sha256(content), f"{name}身份漂移")


def _load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise validation.KernelIterationValidationError(f"无法读取 pending/ready 表示制品 {path}: {error}") from error
    _require(isinstance(value, dict), f"pending/ready 表示制品不是对象: {path}")
    return value


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise validation.KernelIterationValidationError(message)
