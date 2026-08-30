from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import kernel_iteration_evidence as evidence
import kernel_iteration_validation as validation


CONTRACT_SCHEMA = "ownward.kernel-iteration-stage4-resource-cost-raw-vector-lifecycle-contract/v1"
RESULT_SCHEMA = "ownward.kernel-iteration-stage4-resource-cost-raw-vector-lifecycle-result/v1"
CONTRACT_PATH = Path("iteration/v2/stage4-resource-cost-raw-vector-lifecycle-contract.json")
DEPENDENCY_MIGRATION_SCHEMA = "ownward.kernel-iteration-direct-dependency-migration/v1"
DEPENDENCY_MIGRATION_PATH = Path("iteration/v2/stage4-resource-cost-raw-vector-lifecycle-dependency-migration.json")


def run(suite_root: Path, output_root: Path, formal_state: Path, *, resume: bool) -> dict[str, Any]:
    suite_root = suite_root.resolve()
    repository = suite_root.parents[2]
    output_root = output_root.resolve()
    _require(
        output_root.is_relative_to(repository / ".tmp" / "kernel-v2-major-iteration"),
        "原始正文向量生命周期证据必须位于非正式 V2 边界",
    )
    contract = load_contract(suite_root)
    formal_state = formal_state.resolve()
    _require(formal_state == repository / contract["formal_state"]["path"], "正式 state 路径错绑")
    state_before = evidence.file_sha256(formal_state)
    _require(state_before == contract["formal_state"]["sha256"], "生命周期审计前正式 state 漂移")
    result_path = output_root / "result.json"
    if result_path.is_file():
        _require(resume, "原始正文向量生命周期终态已存在；只有 --resume 可逐字复用")
        value = _load_json(result_path)
        _validate_identity(value, RESULT_SCHEMA, "原始正文向量生命周期终态")
        _require(value["contract_identity"] == contract["identity"], "生命周期终态合同错绑")
        _require(value["formal_state_sha256"] == state_before, "生命周期恢复时正式 state 漂移")
        return {**value, "path": str(result_path), "reused": True, "model_executions": 0, "product_executions": 0}

    sources = {
        name: _verified_text(repository, item, name)
        for name, item in contract["source_files"].items()
    }
    matched_create = _verified_json(repository, contract["evidence"]["matched_create"], "对称 CreateBatch")
    non_create = _verified_json(repository, contract["evidence"]["non_create_decomposition"], "非 CreateBatch 分解")
    result = evaluate(contract, sources, matched_create, non_create, state_before)
    _require(evidence.file_sha256(formal_state) == state_before, "生命周期审计改写正式 state")
    output_root.mkdir(parents=True, exist_ok=True)
    evidence.atomic_json(result_path, result)
    return {**result, "path": str(result_path), "reused": False, "model_executions": 0, "product_executions": 0}


def evaluate(
    contract: dict[str, Any],
    sources: dict[str, str],
    matched_create: dict[str, Any],
    non_create: dict[str, Any],
    state_sha256: str,
) -> dict[str, Any]:
    _verify_source_lifecycle(sources)
    active = contract["active_gate"]
    gate = matched_create["candidate_controlled_gate"]
    _require(abs(float(gate["v0_controlled_baseline_seconds"]) - float(active["v0_controlled_baseline_seconds"])) <= 1e-12, "V0 可控墙钟基线漂移")
    _require(abs(float(gate["controlled_half_maximum_seconds"]) - float(active["controlled_half_maximum_seconds"])) <= 1e-12, "可控墙钟半数门漂移")
    _require(abs(float(gate["current_plus_error_seconds"]) - float(active["current_plus_error_seconds"])) <= 1e-12, "当前加误差墙钟漂移")
    _require(abs(float(non_create["active_gate"]["required_improvement_seconds"]) - float(active["required_improvement_seconds"])) <= 1e-12, "活动缺口漂移")

    shared = matched_create["shared_cost_classification"]
    gross = float(shared["v2_embedding_after_common_startup_seconds"])
    required = float(active["required_improvement_seconds"])
    gross_margin = gross - required
    _require(gross_margin > 0, "原始正文推理总包络没有结果前所需的理论毛余量")

    lifecycle = [
        {
            "stage": "create-or-update-authority",
            "owner": "authority-and-lexical",
            "observed_behavior": "authority and lexical state become current before collaborative preparation, but the public mutation call waits for preparation",
            "raw_vector_duty": "none-before-collaborative-preparation",
        },
        {
            "stage": "pending-preparation",
            "owner": "core-collaboration",
            "observed_behavior": "every short current authority body is embedded synchronously and stored in the pending derived record",
            "raw_vector_duty": "pending-query-semantic-hit-and-semantic-work-candidate-selection",
        },
        {
            "stage": "semantic-work-freeze",
            "owner": "core-collaboration",
            "observed_behavior": "semantic work resolves the already persisted WorkReference; it does not recompute candidates",
            "raw_vector_duty": "the vector already contributed merged semantic candidates before WorkReference was sealed",
        },
        {
            "stage": "semantic-submit",
            "owner": "core-collaboration",
            "observed_behavior": "semantic-analysis vectors are generated only when the record has no embedding",
            "raw_vector_duty": "a successful short-body raw vector suppresses semantic-analysis vector recovery",
        },
        {
            "stage": "ready-query",
            "owner": "derived-index-and-core-search",
            "observed_behavior": "the ready short record keeps the raw vector and exact query embedding searches it",
            "raw_vector_duty": "normal post-submit semantic retrieval representation",
        },
        {
            "stage": "restart-and-rebuild",
            "owner": "core-generation-and-derived-store",
            "observed_behavior": "startup loads all stored embeddings and rebuild reuses a current-revision same-space embedding before considering semantic analysis",
            "raw_vector_duty": "durable recovery representation and semantic-work candidate input",
        },
        {
            "stage": "embedding-failure",
            "owner": "core-collaboration",
            "observed_behavior": "lexical authority remains available; semantic submit may recover a missing vector from the accepted analysis",
            "raw_vector_duty": "absence is an explicit degraded path, not the normal short-body identity",
        },
    ]

    routes = [
        {
            "route": "defer-raw-vector-until-semantic-work",
            "preserves_public_write_latency": True,
            "preserves_exact_semantic_work": True,
            "required_execution": "perform the same raw document embeddings before returning frozen work",
            "end_to_end_net_removable_seconds": 0.0,
            "authorized": False,
            "reason": "the Codex semantic request cannot start before the vector-dependent WorkReference exists, so this only shifts the same critical work",
        },
        {
            "route": "lazy-raw-vector-only-on-pending-query",
            "preserves_public_write_latency": True,
            "preserves_exact_semantic_work": False,
            "required_execution": "either omit vector-derived candidates or pay the same inference before semantic work",
            "end_to_end_net_removable_seconds": 0.0,
            "authorized": False,
            "reason": "without the vector the frozen candidate identity may change; with it there is no net saving",
        },
        {
            "route": "replace-ready-raw-vector-with-semantic-analysis-vector",
            "preserves_public_write_latency": True,
            "preserves_exact_semantic_work": False,
            "required_execution": "introduce a different ready representation and its document embeddings",
            "end_to_end_net_removable_seconds": 0.0,
            "authorized": False,
            "reason": "current short records retain raw vectors after submit, so replacement changes retrieval representation, ranking and recovery identity",
        },
        {
            "route": "background-raw-vector-after-success",
            "preserves_public_write_latency": True,
            "preserves_exact_semantic_work": False,
            "required_execution": "uncheckpointed work races query, semantic work, restart and update",
            "end_to_end_net_removable_seconds": 0.0,
            "authorized": False,
            "reason": "forbidden deferred debt and no single durable state transition proves completion",
        },
    ]

    content = {
        "schema": RESULT_SCHEMA,
        "formal": False,
        "formal_state_written": False,
        "contract_identity": contract["identity"],
        "active_gate": active,
        "source_trace": {
            "authority_write_and_public_return": "service.Create/CreateBatch/Update",
            "pending_vector_and_work": "collaboration.prepareSemanticWork/prepareSemanticWorkBatch/newPendingSemanticRecord/semanticCandidates",
            "submit_and_ready_representation": "collaboration.prepareSemanticVectorRecoveries/submitSemanticWithRecovery",
            "query": "service.Search plus derived.Index",
            "restart_and_rebuild": "service constructors plus generation.buildCollaborativeGeneration",
            "benchmark_order": "create-batch -> freeze semantic-work -> analyze -> submit -> retrieve",
        },
        "lifecycle": lifecycle,
        "cost_lower_bound": {
            "observed_short_raw_embedding_after_common_startup_seconds": gross,
            "required_real_improvement_seconds": required,
            "gross_opportunity_margin_seconds": gross_margin,
            "proven_net_removable_lower_bound_seconds": 0.0,
            "proven_net_removable_upper_bound_under_exact_work_identity_seconds": 0.0,
            "remaining_shortfall_seconds": required,
            "accounting": "preserving the frozen semantic candidate set requires the same 12 raw vectors before semantic work; moving them across the CreateBatch boundary cannot shorten the full create-to-semantic-to-query critical path",
        },
        "route_assessment": routes,
        "authorization": {
            "source_lifecycle_audited": True,
            "behavior_equivalent_deferral_lifecycle_established": False,
            "net_mathematical_margin_established": False,
            "implementation_authorized": False,
            "candidate_built": False,
            "new_measurement_authorized": False,
            "reason": "the gross envelope is large enough, but no behavior-equivalent lifecycle removes it from the end-to-end path",
        },
        "root_cause": {
            "mechanism": "one untyped derived embedding field conflates pending raw-content candidate/search duties with the ready semantic retrieval representation",
            "why_scheduling_is_not_the_root": "the vector is a semantic input and durable query identity, not merely work performed before the public write returns",
            "next_validation": "freeze-and-prove-a-versioned-pending-versus-ready-retrieval-representation-lifecycle-on-independent-materials-before-revisiting-cost; it must preserve semantic-work candidates, immediate pending queries, ready ranking, restart, rebuild and failure recovery",
        },
        "preserved_evidence": {
            "non_create_result_identity": non_create["identity"],
            "non_create_seconds": non_create["same_observation_critical_path"]["non_create_seconds"],
            "matched_create_result_identity": matched_create["identity"],
            "formal_state_sha256": state_sha256,
        },
        "execution": {
            "model_executions": 0,
            "reader_executions": 0,
            "judge_executions": 0,
            "product_executions": 0,
            "candidate_builds": 0,
            "new_observation_performed": False,
        },
        "decision": "reject-raw-vector-deferral-before-implementation-and-keep-stage4-open",
        "formal_state_sha256": state_sha256,
    }
    return {**content, "identity": evidence.canonical_sha256(content)}


def load_contract(suite_root: Path) -> dict[str, Any]:
    repository = suite_root.parents[2]
    value = _load_json(suite_root / CONTRACT_PATH)
    _validate_identity(value, CONTRACT_SCHEMA, "原始正文向量生命周期合同")
    _require(value.get("frozen_before_implementation") is True, "生命周期合同未在实现前冻结")
    _require(value.get("frozen_before_new_measurement") is True, "生命周期合同未在新测量前冻结")
    _require(value.get("candidate_results_seen") is False, "生命周期合同错误声明已看到候选结果")
    actual = {
        item["path"]: evidence.file_sha256(repository / item["path"])
        for item in value["direct_dependencies"]
        if (repository / item["path"]).is_file()
    }
    drifted = {
        item["path"]: {"frozen": item["sha256"], "current": actual.get(item["path"])}
        for item in value["direct_dependencies"]
        if actual.get(item["path"]) != item["sha256"]
    }
    if drifted:
        migration = _load_json(suite_root / DEPENDENCY_MIGRATION_PATH)
        _validate_identity(migration, DEPENDENCY_MIGRATION_SCHEMA, "生命周期直接依赖迁移收据")
        _require(migration.get("contract_identity") == value["identity"], "生命周期直接依赖迁移合同错绑")
        _require(
            migration.get("reason")
            == "stage2-adds-an-unrelated-blind-admission-reliability-command-without-changing-the-frozen-stage4-audit-or-its-result",
            "生命周期直接依赖迁移原因漂移",
        )
        changes = {
            item["path"]: {"frozen": item["frozen_sha256"], "current": item["current_sha256"]}
            for item in migration.get("changes", [])
        }
        _require(changes == drifted, "生命周期直接依赖漂移不在精确迁移收据内")
        classifications = {
            item["path"]: item.get("classification")
            for item in migration.get("changes", [])
        }
        _require(
            classifications
            == {
                "benchmarks/acceptance/suite/kernel_iteration_stage4_resource_cost_raw_vector_lifecycle.py": "dependency-receipt-validation-only",
                "benchmarks/acceptance/suite/kernel_iteration_run.py": "additive-stage2-blind-admission-reliability-dispatch-only",
            },
            "生命周期直接依赖迁移分类漂移",
        )
        _require(
            migration.get("preserved")
            == {
                "contract_identity": True,
                "source_files": True,
                "thresholds": True,
                "evidence_identities": True,
                "formal_state_sha256": True,
            },
            "生命周期直接依赖迁移保护边界漂移",
        )
    return value


def _verify_source_lifecycle(sources: dict[str, str]) -> None:
    service = sources["service"]
    collaboration = sources["collaboration"]
    generation = sources["generation"]
    store = sources["derived_store"]
    runner = sources["longmemeval_runner"]
    for value in (
        "states = s.prepareSemanticWorkBatch(ctx, values)",
        "organizationResult <- s.prepareSemanticWork(ctx, updated)",
        "records, err := derivedStore.AllWithEmbeddings()",
        "if s.semantic == nil || !s.semantic.HasVectors()",
        "queryVector, err := s.embedder.EmbedQuery(ctx, input.Query)",
    ):
        _require(value in service, f"服务生命周期源码锚点漂移: {value}")
    for value in (
        "generated, embeddingErr := s.embedder.EmbedDocuments(ctx, contents[offset:end])",
        "candidates := s.semanticCandidates(value, vectors, indexes...)",
        "for _, hit := range mergedSemanticHits(vectors[0], indexes, 24)",
        "record.SemanticWorkReference == nil || len(record.Embedding) > 0",
        "if len(record.Embedding) == 0 && len(recovery.vector) > 0",
        "return semantics.ResolveWork(*record.SemanticWorkReference, asset, candidates)",
    ):
        _require(value in collaboration, f"协作生命周期源码锚点漂移: {value}")
    reuse = generation.find("len(previous.Embedding) > 0 && previous.EmbeddingSpace == s.embedder.Space().ID")
    semantic = generation.find("previous.HasSemanticResult()", reuse)
    _require(reuse >= 0 and semantic > reuse, "重建没有先复用当前向量再考虑语义表示")
    _require("Embedding             []float32" in store and "func (r Record) HasSemanticResult() bool" in store, "派生记录身份锚点漂移")
    create = runner.find('runtime.client.call_tool("ownward_create_batch"')
    work = runner.find("freeze_semantic_batch(runtime", create)
    submit = runner.find("submit_semantic_batch(", work)
    _require(create >= 0 and work > create and submit > work, "LongMemEval-S 创建、语义工作与提交顺序漂移")


def _verified_text(repository: Path, item: dict[str, Any], name: str) -> str:
    path = repository / item["path"]
    _require(path.is_file() and evidence.file_sha256(path) == item["sha256"], f"{name} 源码漂移")
    try:
        return path.read_text(encoding="utf-8")
    except (OSError, UnicodeDecodeError) as error:
        raise validation.KernelIterationValidationError(f"无法读取{name}源码 {path}: {error}") from error


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
        raise validation.KernelIterationValidationError(f"无法读取原始正文向量生命周期制品 {path}: {error}") from error
    _require(isinstance(value, dict), f"原始正文向量生命周期制品不是对象: {path}")
    return value


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise validation.KernelIterationValidationError(message)
