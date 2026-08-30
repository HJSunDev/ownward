from __future__ import annotations

import base64
from concurrent.futures import ThreadPoolExecutor
import json
import os
from pathlib import Path
import shutil
import subprocess
import time
from typing import Any
import zipfile

import kernel_iteration_evidence as evidence
import kernel_iteration_stage4_resource_cost_create_probe as create_probe
import kernel_iteration_validation as validation


CONTRACT_SCHEMA = "ownward.kernel-iteration-stage4-resource-cost-matched-create-contract/v2"
RESULT_SCHEMA = "ownward.kernel-iteration-stage4-resource-cost-matched-create-result/v2"
CONTRACT_PATH = Path("iteration/v2/stage4-resource-cost-matched-create-contract.json")
TRACE_SCHEMA = create_probe.TRACE_SCHEMA


def run(suite_root: Path, output_root: Path, formal_state: Path, *, resume: bool) -> dict[str, Any]:
    suite_root = suite_root.resolve()
    repository = suite_root.parents[2]
    output_root = output_root.resolve()
    _require(output_root.is_relative_to(repository / ".tmp" / "kernel-v2-major-iteration"), "匹配 CreateBatch 证据必须位于非正式 V2 边界")
    contract = load_contract(suite_root)
    formal_state = formal_state.resolve()
    _require(formal_state == repository / contract["formal_state"]["path"], "正式 state 路径错绑")
    state_before = evidence.file_sha256(formal_state)
    _require(state_before == contract["formal_state"]["sha256"], "匹配观测前正式 state 漂移")
    result_path = output_root / "result.json"
    if result_path.is_file():
        _require(resume, "匹配 CreateBatch 终态已存在；只有 --resume 可逐字复用")
        value = _load_json(result_path)
        _validate_identity(value, RESULT_SCHEMA, "匹配 CreateBatch 终态")
        _require(value["contract_identity"] == contract["identity"], "匹配 CreateBatch 终态合同错绑")
        _require(value["formal_state_sha256"] == state_before, "匹配 CreateBatch 恢复时正式 state 漂移")
        return {**value, "path": str(result_path), "reused": True, "model_executions": 0, "product_executions": 0}

    output_root.mkdir(parents=True, exist_ok=True)
    observers = {
        "v0": build_v0_observer(repository, output_root / "observer-v0", contract),
        "v2": {**create_probe.build_observer(repository, output_root / "observer-v2", contract), "measurement_role": "v2"},
    }
    controls = {
        "v0": _control(contract, repository, "v0"),
        "v2": _control(contract, repository, "v2"),
    }
    cases = create_probe.load_cases(repository, contract)
    module = validation._load_longmemeval_module(suite_root)

    equivalence: dict[str, Any] = {}
    for subject in ("v0", "v2"):
        control_samples = _run_subject(module, output_root / "equivalence" / subject / "control", controls[subject], cases, contract, trace_required=False)
        observer_samples = _run_subject(module, output_root / "equivalence" / subject / "observer", observers[subject], cases, contract, trace_required=True)
        control_behavior = [item["behavior"] for item in control_samples]
        observer_behavior = [item["behavior"] for item in observer_samples]
        _require(control_behavior == observer_behavior, f"{subject} 观察器改变了冻结 CreateBatch 可观察行为")
        equivalence[subject] = {
            "control_binary_sha256": evidence.file_sha256(controls[subject]["binary"]),
            "observer_binary_sha256": observers[subject]["binary_sha256"],
            "behavior_identity": evidence.canonical_sha256(control_behavior),
            "cases": len(control_behavior),
            "equivalent": True,
        }

    rounds: list[dict[str, Any]] = []
    for round_index, subject_order in enumerate(contract["measurement"]["subject_order"], start=1):
        round_value = {"round": round_index, "subject_order": subject_order, "subjects": {}}
        for subject in subject_order:
            started = time.perf_counter()
            samples = _run_subject(module, output_root / f"round-{round_index}" / subject, observers[subject], cases, contract, trace_required=True)
            round_value["subjects"][subject] = _summarize_subject(samples, time.perf_counter() - started)
        rounds.append(round_value)

    result = evaluate(contract, observers, equivalence, rounds, state_before)
    _require(evidence.file_sha256(formal_state) == state_before, "匹配 CreateBatch 观测改写了正式 state")
    evidence.atomic_json(result_path, result)
    return {**result, "path": str(result_path), "reused": False, "model_executions": 0}


def load_contract(suite_root: Path) -> dict[str, Any]:
    repository = suite_root.parents[2]
    value = _load_json(suite_root / CONTRACT_PATH)
    _validate_identity(value, CONTRACT_SCHEMA, "匹配 CreateBatch 合同")
    _require(value.get("frozen_before_results") is True and value.get("results_seen") is False, "匹配 CreateBatch 合同没有在结果前冻结")
    for item in value["direct_dependencies"]:
        path = repository / item["path"]
        _require(path.is_file() and evidence.file_sha256(path) == item["sha256"], f"匹配 CreateBatch 直接依赖漂移: {item['path']}")
    return value


def build_v0_observer(repository: Path, root: Path, contract: dict[str, Any]) -> dict[str, Any]:
    source = contract["subjects"]["v0"]
    receipt_path = root / "observer-receipt.json"
    binary = root / ("ownward-observer.exe" if os.name == "nt" else "ownward-observer")
    embedding_root = root / "embedding"
    if receipt_path.is_file():
        receipt = _load_json(receipt_path)
        _validate_identity(receipt, "ownward.kernel-iteration-v0-create-observer/v1", "V0 CreateBatch 观察器")
        _require(receipt["source_commit"] == source["audit_commit"], "V0 观察器来源提交漂移")
        _require(binary.is_file() and evidence.file_sha256(binary) == receipt["binary_sha256"], "V0 观察器二进制漂移")
        _require(embedding_root.is_dir() and evidence.file_sha256(embedding_root / "manifest.json") == source["embedding_manifest_sha256"], "V0 观察器向量包漂移")
        return {**receipt, "binary": binary, "embedding": embedding_root, "measurement_role": "v0"}

    _require(not root.exists(), "V0 CreateBatch 观察器现场不完整；禁止宽松覆盖")
    root.mkdir(parents=True)
    archive = root / "source.zip"
    source_root = root / "source"
    completed = subprocess.run(
        ["git", "archive", "--format=zip", "-o", str(archive), source["audit_commit"]],
        cwd=repository, capture_output=True, text=True, encoding="utf-8", timeout=120, check=False,
    )
    _require(completed.returncode == 0, f"提取 V0 审计源码失败: {completed.stderr.strip()}")
    with zipfile.ZipFile(archive) as packaged:
        packaged.extractall(source_root)
    archive.unlink()
    source_files = {
        "service": source_root / "internal/core/service.go",
        "collaboration": source_root / "internal/core/collaboration.go",
        "embedding": source_root / "internal/embedding/llama.go",
        "derived": source_root / "internal/derived/store.go",
    }
    original_hashes = {name: evidence.file_sha256(path) for name, path in source_files.items()}
    source_files["service"].write_text(_instrument_v0_service(source_files["service"].read_text(encoding="utf-8")), encoding="utf-8")
    source_files["collaboration"].write_text(_instrument_v0_collaboration(source_files["collaboration"].read_text(encoding="utf-8")), encoding="utf-8")
    source_files["embedding"].write_text(create_probe._instrument_embedding(source_files["embedding"].read_text(encoding="utf-8")), encoding="utf-8")
    source_files["derived"].write_text(_instrument_v0_derived(source_files["derived"].read_text(encoding="utf-8")), encoding="utf-8")
    completed = subprocess.run(
        ["go", "build", "-trimpath", "-o", str(binary), "./cmd/ownward"],
        cwd=source_root, capture_output=True, text=True, encoding="utf-8", timeout=300, check=False,
    )
    _require(completed.returncode == 0 and binary.is_file(), f"构建 V0 CreateBatch 观察器失败: {completed.stderr.strip()}")
    frozen_embedding = repository / source["release_embedding_root"]
    _require(evidence.file_sha256(frozen_embedding / "manifest.json") == source["embedding_manifest_sha256"], "V0 冻结向量包漂移")
    shutil.copytree(frozen_embedding, embedding_root, copy_function=os.link)
    content = {
        "schema": "ownward.kernel-iteration-v0-create-observer/v1",
        "source_commit": source["audit_commit"],
        "source_binary_sha256": source["binary_sha256"],
        "source_file_sha256": original_hashes,
        "instrumented_file_sha256": {name: evidence.file_sha256(path) for name, path in source_files.items()},
        "binary_sha256": evidence.file_sha256(binary),
        "embedding_manifest_sha256": source["embedding_manifest_sha256"],
        "behavioral_delta": "timing-only-stderr-events",
        "formal": False,
    }
    receipt = {**content, "identity": evidence.canonical_sha256(content)}
    evidence.atomic_json(receipt_path, receipt)
    return {**receipt, "binary": binary, "embedding": embedding_root, "measurement_role": "v0"}


def _control(contract: dict[str, Any], repository: Path, subject: str) -> dict[str, Any]:
    value = contract["subjects"][subject]
    binary = repository / value["binary_path"]
    embedding_root = repository / value["embedding_root"]
    _require(binary.is_file() and evidence.file_sha256(binary) == value["binary_sha256"], f"{subject} 冻结二进制漂移")
    _require(embedding_root.is_dir() and evidence.file_sha256(embedding_root / "manifest.json") == value["embedding_manifest_sha256"], f"{subject} 冻结向量包漂移")
    return {"binary": binary, "embedding": embedding_root, "measurement_role": subject}


def _run_subject(module: Any, root: Path, subject: dict[str, Any], cases: dict[str, dict[str, Any]], contract: dict[str, Any], *, trace_required: bool) -> list[dict[str, Any]]:
    order = contract["measurement"]["question_order"]
    with ThreadPoolExecutor(max_workers=len(order), thread_name_prefix="matched-create") as pool:
        futures = {
            case_id: pool.submit(_run_question, module, root, subject, cases[case_id], contract, trace_required)
            for case_id in order
        }
        return [futures[case_id].result() for case_id in order]


def _run_question(module: Any, root: Path, subject: dict[str, Any], case: dict[str, Any], contract: dict[str, Any], trace_required: bool) -> dict[str, Any]:
    question_root = root / case["case_id"]
    _require(not question_root.exists(), "匹配 CreateBatch 单题不允许覆盖")
    data_root = question_root / "ownward-data"
    data_root.mkdir(parents=True)
    environment = os.environ.copy()
    environment["OWNWARD_EMBEDDING_BUNDLE_DIR"] = str(subject["embedding"])
    items = [
        {"content": module.session_content(str(session["session_id"]), str(session["date"]), session["turns"]), "contexts": [{"key": "source", "value": "LongMemEval-S"}]}
        for session in case["sessions"]
    ]
    runtime = module.OwnwardRuntime(subject["binary"], data_root, environment, startup_seconds=60, operation_seconds=60)
    with runtime:
        assert runtime.client is not None
        started = time.perf_counter()
        response = runtime.client.call_tool("ownward_create_batch", {"items": items})
        call_seconds = time.perf_counter() - started
    values = response.get("results") if isinstance(response, dict) else None
    _require(isinstance(values, list) and len(values) == len(items), f"匹配 CreateBatch 创建失败: {case['case_id']}")
    behavior = _normalize_behavior(values)
    _require(all(not item.get("error") for item in values if isinstance(item, dict)), f"匹配 CreateBatch 包含失败项: {case['case_id']}")
    events = []
    for line in runtime._stderr:
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        if isinstance(event, dict) and event.get("schema") == TRACE_SCHEMA:
            events.append(event)
    if trace_required:
        _require(any(item.get("phase") == "create.envelope" for item in events), f"{case['case_id']} 缺少 CreateBatch envelope")
        embedding = [item for item in events if item.get("phase") == "embedding.documents"]
        expected_inputs = len(items) if subject["measurement_role"] == "v0" else sum(len(item["content"].encode("utf-8")) <= 320 for item in items)
        _require(sum(int(item["input_count"]) for item in embedding) == expected_inputs, f"{case['case_id']} embedding 输入未闭合")
    else:
        _require(not events, f"{case['case_id']} 冻结 control 意外包含观察事件")
    return {
        "case_id": case["case_id"], "assets": len(items), "call_seconds": call_seconds,
        "behavior": behavior, "events": events, "trace_identity": evidence.canonical_sha256(events),
    }


def _normalize_behavior(values: list[Any]) -> list[dict[str, Any]]:
    result: list[dict[str, Any]] = []
    for value in values:
        _require(isinstance(value, dict), "CreateBatch 返回项不是对象")
        if value.get("error"):
            result.append({"error": str(value["error"])})
            continue
        mutation = value.get("result")
        _require(isinstance(mutation, dict), "CreateBatch 返回项缺少 result")
        information = mutation.get("information")
        organization = mutation.get("organization")
        _require(isinstance(information, dict) and isinstance(organization, dict), "CreateBatch 返回结构无效")
        result.append({
            "information": {
                "schema": information.get("schema"), "revision": information.get("revision"), "kind": information.get("kind"),
                "content": information.get("content"), "contexts": information.get("contexts", []),
                "relations": information.get("relations", []), "source": information.get("source", {}),
            },
            "organization": {
                "status": organization.get("status"), "provider": organization.get("provider"),
                "error_present": bool(organization.get("error")), "required_action": organization.get("required_action"),
            },
        })
    return result


def _summarize_subject(samples: list[dict[str, Any]], concurrent_wall_seconds: float) -> dict[str, Any]:
    phase_seconds: dict[str, float] = {}
    phase_calls: dict[str, int] = {}
    vector_ids: list[str] = []
    input_bytes: list[int] = []
    for sample in samples:
        for event in sample["events"]:
            phase = str(event["phase"])
            phase_seconds[phase] = phase_seconds.get(phase, 0.0) + float(event["duration_ns"]) / 1_000_000_000
            phase_calls[phase] = phase_calls.get(phase, 0) + 1
            if phase == "embedding.documents":
                vector_ids.extend(str(item) for item in event.get("vector_identities", []))
                input_bytes.extend(int(item) for item in event.get("input_bytes", []))
    return {
        "concurrent_wall_seconds": concurrent_wall_seconds,
        "create_call_sum_seconds": sum(item["call_seconds"] for item in samples),
        "create_call_critical_path_seconds": max(item["call_seconds"] for item in samples),
        "phase_seconds": dict(sorted(phase_seconds.items())),
        "phase_calls": dict(sorted(phase_calls.items())),
        "embedding_vector_identities": vector_ids,
        "embedding_input_bytes": input_bytes,
        "behavior_identity": evidence.canonical_sha256([item["behavior"] for item in samples]),
        "trace_identities": [item["trace_identity"] for item in samples],
    }


def evaluate(contract: dict[str, Any], observers: dict[str, dict[str, Any]], equivalence: dict[str, Any], rounds: list[dict[str, Any]], state_sha256: str) -> dict[str, Any]:
    summaries = {subject: [round_value["subjects"][subject] for round_value in rounds] for subject in ("v0", "v2")}
    for subject, values in summaries.items():
        _require(all(item["behavior_identity"] == equivalence[subject]["behavior_identity"] for item in values), f"{subject} 重复运行行为漂移")
        _require(all(item["embedding_input_bytes"] == values[0]["embedding_input_bytes"] for item in values), f"{subject} embedding 输入漂移")
        _require(all(item["embedding_vector_identities"] == values[0]["embedding_vector_identities"] for item in values), f"{subject} embedding 向量漂移")
    v0_short_vectors = [
        (size, vector_identity)
        for size, vector_identity in zip(summaries["v0"][0]["embedding_input_bytes"], summaries["v0"][0]["embedding_vector_identities"])
        if size <= int(contract["measurement"]["shared_short_document_maximum_bytes"])
    ]
    v2_short_vectors = list(zip(summaries["v2"][0]["embedding_input_bytes"], summaries["v2"][0]["embedding_vector_identities"]))
    _require(v0_short_vectors == v2_short_vectors, "V0/V2 共同短正文或精确文档向量不同尺")
    means: dict[str, dict[str, float]] = {}
    for subject, values in summaries.items():
        phases = sorted({phase for item in values for phase in item["phase_seconds"]})
        means[subject] = {
            "create_envelope_seconds": sum(item["phase_seconds"]["create.envelope"] for item in values) / len(values),
            "embedding_documents_seconds": sum(item["phase_seconds"]["embedding.documents"] for item in values) / len(values),
            "embedding_ensure_running_seconds": sum(item["phase_seconds"]["embedding.ensure_running"] for item in values) / len(values),
            "concurrent_wall_seconds": sum(item["concurrent_wall_seconds"] for item in values) / len(values),
            **{f"phase:{phase}": sum(item["phase_seconds"].get(phase, 0.0) for item in values) / len(values) for phase in phases},
        }
    common_runtime_startup = min(means["v0"]["embedding_ensure_running_seconds"], means["v2"]["embedding_ensure_running_seconds"])
    gate = contract["candidate_controlled_gate"]
    v0_controlled = float(gate["historical_v0_local_seconds"]) - common_runtime_startup
    v2_controlled = float(gate["current_v2_local_seconds"]) - common_runtime_startup
    maximum = v0_controlled * float(gate["relative_maximum"])
    repeat_error = float(gate["repeatability_error_seconds"])
    passed = v2_controlled + repeat_error <= maximum
    content = {
        "schema": RESULT_SCHEMA,
        "formal": False,
        "formal_state_written": False,
        "contract_identity": contract["identity"],
        "observer_identities": {subject: value["identity"] for subject, value in observers.items()},
        "observer_equivalence": equivalence,
        "rounds": rounds,
        "means": means,
        "shared_cost_classification": {
            "rule": "paired-common-ensure-running-floor-only",
            "same_model_space_prefix_pooling_normalization_truncation": True,
            "same_short_input_bytes_and_exact_vectors": True,
            "v0_also_embeds_the_long_document": True,
            "common_ensure_running_seconds": common_runtime_startup,
            "ensure_running_reported_inside_embedding_and_subtracted_once": True,
            "inference_and_request_scheduling_left_candidate_controlled_because_the_frozen_subjects_do_not_have_the_same_complete_document_input_set": True,
            "v2_embedding_after_common_startup_seconds": means["v2"]["embedding_documents_seconds"] - common_runtime_startup,
            "v0_embedding_after_common_startup_seconds": means["v0"]["embedding_documents_seconds"] - common_runtime_startup,
        },
        "candidate_controlled_gate": {
            "historical_v0_local_seconds": gate["historical_v0_local_seconds"],
            "current_v2_local_seconds": gate["current_v2_local_seconds"],
            "v0_controlled_baseline_seconds": v0_controlled,
            "controlled_half_maximum_seconds": maximum,
            "current_v2_controlled_seconds": v2_controlled,
            "repeatability_error_seconds": repeat_error,
            "current_plus_error_seconds": v2_controlled + repeat_error,
            "passed": passed,
        },
        "decision": "close-local-wall-component-with-migration-receipt" if passed else "retain-open-and-authorize-no-route-without-mathematical-margin",
        "model_executions": 0,
        "reader_executions": 0,
        "judge_executions": 0,
        "formal_state_sha256": state_sha256,
    }
    return {**content, "identity": evidence.canonical_sha256(content)}


def _instrument_v0_service(source: str) -> str:
    source = create_probe._replace_once(source, '"encoding/hex"\n', '"encoding/hex"\n\t"encoding/json"\n')
    source = create_probe._replace_once(source, '"sort"\n', '"os"\n\t"sort"\n')
    source = create_probe._replace_once(source, '"sync"\n', '"sync"\n\t"sync/atomic"\n')
    marker = "const CollaborationRules = `"
    _require(source.count(marker) == 1, "V0 Service trace 锚点漂移")
    helper = '''var candidateCreateTraceMu sync.Mutex
var candidateCreateTraceOverheadNs atomic.Int64

func traceCandidateCreate(event map[string]any) {
	started := time.Now()
	event["schema"] = "ownward.candidate-create-trace/v1"
	event["observer_previous_ns"] = candidateCreateTraceOverheadNs.Load()
	encoded, err := json.Marshal(event)
	if err == nil {
		candidateCreateTraceMu.Lock()
		_, _ = fmt.Fprintln(os.Stderr, string(encoded))
		candidateCreateTraceMu.Unlock()
	}
	candidateCreateTraceOverheadNs.Add(time.Since(started).Nanoseconds())
}

const CollaborationRules = `'''
    source = source.replace(marker, helper, 1)
    source = create_probe._replace_once(source, '''func (s *Service) CreateBatch(ctx context.Context, inputs []CreateInput) ([]MutationBatchResult, error) {
	if len(inputs) == 0 || len(inputs) > 20 {''', '''func (s *Service) CreateBatch(ctx context.Context, inputs []CreateInput) ([]MutationBatchResult, error) {
	envelopeStarted := time.Now()
	defer func() { traceCandidateCreate(map[string]any{"phase": "create.envelope", "duration_ns": time.Since(envelopeStarted).Nanoseconds(), "input_count": len(inputs)}) }()
	if len(inputs) == 0 || len(inputs) > 20 {''')
    source = create_probe._replace_once(source, '''	if err := s.store.Create(value); err != nil {
		return domain.Information{}, err
	}
	s.index.Upsert(value)''', '''	authorityStarted := time.Now()
	storeErr := s.store.Create(value)
	traceCandidateCreate(map[string]any{"phase": "authority.create_asset", "duration_ns": time.Since(authorityStarted).Nanoseconds(), "error": storeErr != nil})
	if storeErr != nil {
		return domain.Information{}, storeErr
	}
	indexStarted := time.Now()
	s.index.Upsert(value)
	traceCandidateCreate(map[string]any{"phase": "lexical.memory_index", "duration_ns": time.Since(indexStarted).Nanoseconds(), "input_count": 1})''')
    return source


def _instrument_v0_collaboration(source: str) -> str:
    source = create_probe._replace_once(source, '"context"\n', '"context"\n\t"crypto/sha256"\n\t"encoding/json"\n')
    source = create_probe._replace_once(source, '"errors"\n', '"errors"\n\t"fmt"\n')
    source = create_probe._replace_once(source, '"strings"\n', '"strings"\n\t"time"\n')
    source = create_probe._replace_once(source, '''	vectors, embeddingErr := s.embedder.EmbedDocuments(ctx, contents)
	if len(vectors) != len(values) && embeddingErr == nil {''', '''	inputBytes := make([]int, len(contents))
	for index, content := range contents { inputBytes[index] = len([]byte(content)) }
	embeddingStarted := time.Now()
	vectors, embeddingErr := s.embedder.EmbedDocuments(ctx, contents)
	encodedVectors, _ := json.Marshal(vectors)
	vectorIdentities := make([]string, len(vectors))
	for index, vector := range vectors { encoded, _ := json.Marshal(vector); vectorIdentities[index] = fmt.Sprintf("%x", sha256.Sum256(encoded)) }
	traceCandidateCreate(map[string]any{"phase": "embedding.documents", "duration_ns": time.Since(embeddingStarted).Nanoseconds(), "input_count": len(contents), "input_bytes": inputBytes, "output_count": len(vectors), "vector_identity": fmt.Sprintf("%x", sha256.Sum256(encodedVectors)), "vector_identities": vectorIdentities, "error": embeddingErr != nil})
	if len(vectors) != len(values) && embeddingErr == nil {''')
    source = create_probe._replace_once(source, '''func (s *Service) prepareSemanticWorkWithVector(value domain.Information, vector []float32, embeddingErr error) OrganizationState {
	previousDependents := s.semantic.Dependents(value.ID)''', '''func (s *Service) prepareSemanticWorkWithVector(value domain.Information, vector []float32, embeddingErr error) OrganizationState {
	recordStarted := time.Now()
	previousDependents := s.semantic.Dependents(value.ID)''')
    source = create_probe._replace_once(source, '''	s.graphMu.Lock()
	defer s.graphMu.Unlock()
	if err := s.derivedStore.Put(record); err != nil {
		return OrganizationState{Status: "pending", Provider: "external-semantic-capability", Error: err.Error()}
	}
	s.semantic.Upsert(record)''', '''	traceCandidateCreate(map[string]any{"phase": "pending-record", "duration_ns": time.Since(recordStarted).Nanoseconds(), "record_count": 1})
	s.graphMu.Lock()
	defer s.graphMu.Unlock()
	derivedStarted := time.Now()
	derivedErr := s.derivedStore.Put(record)
	traceCandidateCreate(map[string]any{"phase": "derived.put", "duration_ns": time.Since(derivedStarted).Nanoseconds(), "record_count": 1, "error": derivedErr != nil})
	if derivedErr != nil {
		return OrganizationState{Status: "pending", Provider: "external-semantic-capability", Error: derivedErr.Error()}
	}
	indexStarted := time.Now()
	s.semantic.Upsert(record)
	traceCandidateCreate(map[string]any{"phase": "semantic.memory_index", "duration_ns": time.Since(indexStarted).Nanoseconds(), "record_count": 1})''')
    return source


def _instrument_v0_derived(source: str) -> str:
    marker = "var ErrStaleRecord = errors.New(\"派生状态版本早于当前版本\")\n"
    helper = '''var ErrStaleRecord = errors.New("派生状态版本早于当前版本")

var candidateDerivedTraceMu sync.Mutex

func traceCandidateDerived(event map[string]any) {
	event["schema"] = "ownward.candidate-create-trace/v1"
	encoded, err := json.Marshal(event)
	if err == nil {
		candidateDerivedTraceMu.Lock()
		_, _ = fmt.Fprintln(os.Stderr, string(encoded))
		candidateDerivedTraceMu.Unlock()
	}
}
'''
    source = create_probe._replace_once(source, marker, helper)
    source = create_probe._replace_once(source, '''	if err := s.file.Sync(); err != nil {
		_ = s.rollbackLocked(start)
		return fmt.Errorf("持久化派生状态: %w", err)
	}
	s.records[record.AssetID]''', '''	barrierStarted := time.Now()
	if err := s.file.Sync(); err != nil {
		traceCandidateDerived(map[string]any{"phase": "derived.durability_barrier", "duration_ns": time.Since(barrierStarted).Nanoseconds(), "error": true})
		_ = s.rollbackLocked(start)
		return fmt.Errorf("持久化派生状态: %w", err)
	}
	traceCandidateDerived(map[string]any{"phase": "derived.durability_barrier", "duration_ns": time.Since(barrierStarted).Nanoseconds(), "error": false})
	s.records[record.AssetID]''')
    return source


def _validate_identity(value: dict[str, Any], schema: str, name: str) -> None:
    _require(value.get("schema") == schema, f"{name} schema 无效")
    _require(value.get("identity") == evidence.canonical_sha256({key: item for key, item in value.items() if key != "identity"}), f"{name}身份漂移")


def _load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, UnicodeDecodeError, json.JSONDecodeError) as error:
        raise validation.KernelIterationValidationError(f"无法读取匹配 CreateBatch 证据 {path}: {error}") from error
    _require(isinstance(value, dict), f"匹配 CreateBatch 证据不是对象: {path}")
    return value


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise validation.KernelIterationValidationError(message)
