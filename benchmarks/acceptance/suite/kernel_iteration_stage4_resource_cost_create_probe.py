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

import kernel_iteration_evidence as evidence
import kernel_iteration_validation as validation


CONTRACT_SCHEMA = "ownward.kernel-iteration-stage4-resource-cost-create-probe-contract/v2"
RESULT_SCHEMA = "ownward.kernel-iteration-stage4-resource-cost-create-probe-result/v1"
CONTRACT_PATH = Path("iteration/v2/stage4-resource-cost-create-probe-contract.json")
TRACE_SCHEMA = "ownward.candidate-create-trace/v1"


def run(suite_root: Path, output_root: Path, formal_state: Path, *, resume: bool) -> dict[str, Any]:
    suite_root = suite_root.resolve()
    repository = suite_root.parents[2]
    output_root = output_root.resolve()
    _require(output_root.is_relative_to(repository / ".tmp" / "kernel-v2-major-iteration"), "CreateBatch 探针必须位于非正式 V2 边界")
    contract = load_contract(suite_root)
    formal_state = formal_state.resolve()
    _require(formal_state == repository / contract["formal_state"]["path"], "CreateBatch 探针正式 state 路径错绑")
    state_before = evidence.file_sha256(formal_state)
    _require(state_before == contract["formal_state"]["sha256"], "CreateBatch 探针前正式 state 漂移")
    result_path = output_root / "result.json"
    if result_path.is_file():
        _require(resume, "CreateBatch 探针终态已存在；只有 --resume 可逐字复用")
        value = _load_json(result_path)
        _validate_identity(value, RESULT_SCHEMA, "CreateBatch 探针终态")
        _require(value["contract_identity"] == contract["identity"], "CreateBatch 探针终态合同错绑")
        _require(value["formal_state_sha256"] == state_before, "CreateBatch 探针恢复时正式 state 漂移")
        return {**value, "path": str(result_path), "reused": True, "model_executions": 0}

    output_root.mkdir(parents=True, exist_ok=True)
    observer = build_observer(repository, output_root / "observer", contract)
    cases = load_cases(repository, contract)
    module = validation._load_longmemeval_module(suite_root)
    repetitions: list[dict[str, Any]] = []
    for repeat, order in enumerate(contract["measurement"]["balanced_order"], start=1):
        repeat_started = time.perf_counter()
        with ThreadPoolExecutor(max_workers=len(order), thread_name_prefix="create-probe") as pool:
            futures = {
                case_id: pool.submit(run_question, module, output_root, observer, cases[case_id], repeat, contract)
                for case_id in order
            }
            questions = [futures[case_id].result() for case_id in order]
        repetitions.append({"repeat": repeat, "order": order, "wall_seconds": time.perf_counter() - repeat_started, "questions": questions})
    result = evaluate(contract, observer, repetitions, state_before)
    _require(evidence.file_sha256(formal_state) == state_before, "CreateBatch 探针改写了正式 state")
    evidence.atomic_json(result_path, result)
    return {**result, "path": str(result_path), "reused": False, "model_executions": 0}


def load_contract(suite_root: Path) -> dict[str, Any]:
    repository = suite_root.parents[2]
    value = _load_json(suite_root / CONTRACT_PATH)
    _validate_identity(value, CONTRACT_SCHEMA, "CreateBatch 子阶段合同")
    _require(value.get("frozen_before_results") is True and value.get("results_seen") is False, "CreateBatch 合同没有在结果前冻结")
    for item in value["direct_dependencies"]:
        path = repository / item["path"]
        _require(path.is_file() and evidence.file_sha256(path) == item["sha256"], f"CreateBatch 直接依赖漂移: {item['path']}")
    return value


def build_observer(repository: Path, root: Path, contract: dict[str, Any], *, batch_documents: bool = False) -> dict[str, Any]:
    source = contract["source_candidate"]
    candidate_root = repository / source["root"]
    receipt = _verified(repository, source["receipt"], "候选收据")
    _require(receipt["subject_identity"] == source["subject_identity"], "CreateBatch 探针候选身份错绑")
    sealed_sources = {
        "binary_sha256": candidate_root / ("ownward.exe" if os.name == "nt" else "ownward"),
        "composition_sha256": candidate_root / "composition.json",
        "overlay_sha256": candidate_root / "go-overlay.json",
        "generated_service_sha256": candidate_root / "core-service.go.overlay",
        "generated_collaboration_sha256": candidate_root / "core-collaboration.go.overlay",
        "embedding_manifest_sha256": candidate_root / "embedding/manifest.json",
    }
    for field, path in sealed_sources.items():
        _require(path.is_file() and evidence.file_sha256(path) == receipt[field], f"CreateBatch 探针候选制品漂移: {field}")
    binary = root / ("ownward-observer.exe" if os.name == "nt" else "ownward-observer")
    observer_embedding = root / "embedding"
    overlay_path = root / "go-overlay.json"
    generated = {
        "service": root / "core-service.go.overlay",
        "collaboration": root / "core-collaboration.go.overlay",
        "embedding": root / "embedding-llama.go.overlay",
        "derived": root / "derived-store.go.overlay",
    }
    receipt_path = root / "observer-receipt.json"
    if receipt_path.is_file():
        observer = _load_json(receipt_path)
        _validate_identity(observer, "ownward.kernel-iteration-create-observer/v1", "CreateBatch 观察器")
        _require(observer["source_subject_identity"] == source["subject_identity"], "CreateBatch 观察器候选漂移")
        _require(observer["batch_documents"] is batch_documents, "CreateBatch 观察器批处理模式漂移")
        for name, path in {"binary": binary, "overlay": overlay_path, **generated}.items():
            _require(path.is_file() and evidence.file_sha256(path) == observer[f"{name}_sha256"], f"CreateBatch 观察器制品漂移: {name}")
        _require(observer_embedding.is_dir() and evidence.file_sha256(observer_embedding / "manifest.json") == receipt["embedding_manifest_sha256"], "CreateBatch 观察器向量制品漂移")
        return {**observer, "binary": binary, "embedding": observer_embedding}

    _require(not root.exists(), "CreateBatch 观察器现场不完整；禁止宽松覆盖")
    root.mkdir(parents=True)
    service_source = (candidate_root / "core-service.go.overlay").read_text(encoding="utf-8")
    collaboration_source = (candidate_root / "core-collaboration.go.overlay").read_text(encoding="utf-8")
    generated["service"].write_text(_instrument_service(service_source), encoding="utf-8")
    if batch_documents:
        collaboration_source = _batch_independent_short_documents(collaboration_source)
    generated["collaboration"].write_text(_instrument_collaboration(collaboration_source), encoding="utf-8")
    generated["embedding"].write_text(_instrument_embedding((repository / "internal/embedding/llama.go").read_text(encoding="utf-8")), encoding="utf-8")
    generated["derived"].write_text(_instrument_derived((repository / "internal/derived/store.go").read_text(encoding="utf-8")), encoding="utf-8")
    overlay = _load_json(candidate_root / "go-overlay.json")
    replacements = dict(overlay["Replace"])
    replacements[str((repository / "internal/core/service.go").resolve())] = str(generated["service"])
    replacements[str((repository / "internal/core/collaboration.go").resolve())] = str(generated["collaboration"])
    replacements[str((repository / "internal/embedding/llama.go").resolve())] = str(generated["embedding"])
    replacements[str((repository / "internal/derived/store.go").resolve())] = str(generated["derived"])
    evidence.atomic_json(overlay_path, {"Replace": replacements})
    composition = candidate_root / "composition.json"
    sealed = base64.b64encode(composition.read_bytes()).decode("ascii")
    ldflags = f"-X main.version={receipt['kernel_generation_identity']} -X github.com/HJSunDev/ownward/manifests/compositions/v1.sealedCompositionBase64={sealed}"
    completed = subprocess.run(
        ["go", "build", "-trimpath", "-overlay", str(overlay_path), "-ldflags", ldflags, "-o", str(binary), "./cmd/ownward"],
        cwd=repository, capture_output=True, text=True, encoding="utf-8", timeout=300, check=False,
    )
    _require(completed.returncode == 0 and binary.is_file(), f"构建 CreateBatch 观察器失败: {completed.stderr.strip()}")
    shutil.copytree(candidate_root / "embedding", observer_embedding, copy_function=os.link)
    content = {
        "schema": "ownward.kernel-iteration-create-observer/v1",
        "source_subject_identity": source["subject_identity"],
        "source_binary_sha256": receipt["binary_sha256"],
        "binary_sha256": evidence.file_sha256(binary),
        "overlay_sha256": evidence.file_sha256(overlay_path),
        **{f"{name}_sha256": evidence.file_sha256(path) for name, path in generated.items()},
        "formal": False,
        "candidate_identity": False,
        "behavioral_delta": (
            "per-item-bounded-document-batching-plus-timing"
            if batch_documents
            else "timing-only-stderr-events"
        ),
        "batch_documents": batch_documents,
    }
    observer = {**content, "identity": evidence.canonical_sha256(content)}
    evidence.atomic_json(receipt_path, observer)
    return {**observer, "binary": binary, "embedding": observer_embedding}


def load_cases(repository: Path, contract: dict[str, Any]) -> dict[str, dict[str, Any]]:
    result: dict[str, dict[str, Any]] = {}
    for item in contract["materials"]:
        materials = _verified(repository, item, "CreateBatch 冻结材料")
        for case in materials["cases"]:
            if case["case_id"] in contract["measurement"]["question_ids"]:
                result[case["case_id"]] = case
    _require(set(result) == set(contract["measurement"]["question_ids"]), "CreateBatch 冻结题目不完整")
    return result


def run_question(
    module: Any,
    output_root: Path,
    observer: dict[str, Any],
    case: dict[str, Any],
    repeat: int,
    contract: dict[str, Any],
) -> dict[str, Any]:
    question_root = output_root / f"repeat-{repeat}" / case["case_id"]
    _require(not question_root.exists(), "CreateBatch 单题探针不允许覆盖")
    data_root = question_root / "ownward-data"
    data_root.mkdir(parents=True)
    environment = os.environ.copy()
    environment["OWNWARD_EMBEDDING_BUNDLE_DIR"] = str(observer["embedding"])
    items = [
        {
            "content": module.session_content(str(session["session_id"]), str(session["date"]), session["turns"]),
            "contexts": [{"key": "source", "value": "LongMemEval-S"}],
        }
        for session in case["sessions"]
    ]
    runtime = module.OwnwardRuntime(observer["binary"], data_root, environment, startup_seconds=60, operation_seconds=60)
    runtime_started = time.perf_counter()
    with runtime:
        runtime_startup_seconds = time.perf_counter() - runtime_started
        assert runtime.client is not None
        create_started = time.perf_counter()
        response = runtime.client.call_tool("ownward_create_batch", {"items": items})
        create_call_seconds = time.perf_counter() - create_started
        values = response.get("results") if isinstance(response, dict) else None
        _require(isinstance(values, list) and len(values) == len(items), f"CreateBatch 探针创建失败: {case['case_id']}")
        _require(all(isinstance(item, dict) and not item.get("error") for item in values), f"CreateBatch 探针包含失败项: {case['case_id']}")
    events = []
    for line in runtime._stderr:
        try:
            event = json.loads(line)
        except json.JSONDecodeError:
            continue
        if isinstance(event, dict) and event.get("schema") == TRACE_SCHEMA:
            events.append(event)
    required = set(contract["instrumentation"]["required_phases"])
    actual = {str(event.get("phase")) for event in events}
    _require(required.issubset(actual), f"CreateBatch 探针阶段不完整: {case['case_id']} {sorted(required - actual)}")
    envelope = [event for event in events if event.get("phase") == "create.envelope"]
    _require(len(envelope) == 1, f"CreateBatch envelope 数量无效: {case['case_id']}")
    embedding_calls = [event for event in events if event.get("phase") == "embedding.documents"]
    _require(sum(int(event["input_count"]) for event in embedding_calls) == sum(len(item["content"].encode("utf-8")) <= 320 for item in items), "CreateBatch 向量输入数量未闭合")
    return {
        "case_id": case["case_id"],
        "assets": len(items),
        "runtime_startup_seconds": runtime_startup_seconds,
        "create_call_seconds": create_call_seconds,
        "envelope_seconds": float(envelope[0]["duration_ns"]) / 1_000_000_000,
        "trace_sha256": evidence.canonical_sha256(events),
        "events": events,
    }


def evaluate(contract: dict[str, Any], observer: dict[str, Any], repetitions: list[dict[str, Any]], state_sha256: str) -> dict[str, Any]:
    samples = [question for repeat in repetitions for question in repeat["questions"]]
    totals = []
    for repeat in repetitions:
        questions = repeat["questions"]
        totals.append({
            "repeat": repeat["repeat"],
            "concurrent_wall_seconds": repeat["wall_seconds"],
            "runtime_startup_sum_seconds": sum(item["runtime_startup_seconds"] for item in questions),
            "create_call_sum_seconds": sum(item["create_call_seconds"] for item in questions),
            "create_call_critical_path_seconds": max(item["create_call_seconds"] for item in questions),
            "envelope_seconds": sum(item["envelope_seconds"] for item in questions),
        })
    phases: dict[str, float] = {}
    counts: dict[str, int] = {}
    input_bytes: list[list[int]] = []
    vector_identities: list[str] = []
    for sample in samples:
        for event in sample["events"]:
            phase = str(event["phase"])
            phases[phase] = phases.get(phase, 0.0) + float(event["duration_ns"]) / 1_000_000_000
            counts[phase] = counts.get(phase, 0) + 1
            if phase == "embedding.documents":
                input_bytes.append([int(item) for item in event["input_bytes"]])
                vector_identities.append(str(event["vector_identity"]))
    per_repeat_phase = {phase: value / len(repetitions) for phase, value in phases.items()}
    envelope = sum(item["envelope_seconds"] for item in totals) / len(totals)
    closure = {
        "envelope_seconds": envelope,
        "measured_subphases_seconds": sum(
            per_repeat_phase.get(name, 0.0)
            for name in contract["instrumentation"]["non_overlapping_create_subphases"]
        ),
        "create_call_minus_envelope_seconds": sum(item["create_call_sum_seconds"] - item["envelope_seconds"] for item in totals) / len(totals),
    }
    result_content = {
        "schema": RESULT_SCHEMA,
        "formal": False,
        "formal_state_written": False,
        "contract_identity": contract["identity"],
        "source_subject_identity": contract["source_candidate"]["subject_identity"],
        "observer_identity": observer["identity"],
        "measurement": {
            "repetitions": totals,
            "phase_seconds_per_repeat": dict(sorted(per_repeat_phase.items())),
            "phase_call_counts": dict(sorted(counts.items())),
            "embedding_input_bytes": input_bytes,
            "embedding_vector_identities": vector_identities,
            "closure": closure,
            "question_trace_identities": [item["trace_sha256"] for item in samples],
        },
        "decision_gate": contract["route_authorization"],
        "model_executions": 0,
        "reader_executions": 0,
        "judge_executions": 0,
        "formal_state_sha256": state_sha256,
    }
    return {**result_content, "identity": evidence.canonical_sha256(result_content)}


def _instrument_service(source: str) -> str:
    source = _replace_once(source, '"encoding/hex"\n', '"encoding/hex"\n\t"encoding/json"\n')
    source = _replace_once(source, '"math"\n', '"math"\n\t"os"\n')
    source = _replace_once(source, '"sync"\n', '"sync"\n\t"sync/atomic"\n')
    marker = "const CollaborationRules = productrules.Collaboration\n"
    helper = '''const CollaborationRules = productrules.Collaboration

var candidateCreateTraceMu sync.Mutex
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
'''
    source = _replace_once(source, marker, helper)
    start = '''func (s *Service) CreateBatch(ctx context.Context, inputs []CreateInput) ([]MutationBatchResult, error) {
	if len(inputs) == 0 || len(inputs) > 20 {'''
    replacement = '''func (s *Service) CreateBatch(ctx context.Context, inputs []CreateInput) ([]MutationBatchResult, error) {
	envelopeStarted := time.Now()
	defer func() {
		traceCandidateCreate(map[string]any{"phase": "create.envelope", "duration_ns": time.Since(envelopeStarted).Nanoseconds(), "input_count": len(inputs)})
	}()
	if len(inputs) == 0 || len(inputs) > 20 {'''
    source = _replace_once(source, start, replacement)
    authority = '''	if len(values) > 0 {
		if _, err := s.authority.CreateAssets(values); err != nil {
			for _, position := range positions {
				results[position].Error = err.Error()
			}
			return results, nil
		}
		for _, value := range values {
			s.index.Upsert(value)
		}
		if s.evidencePlans != nil {
			s.evidencePlans.Reset()
		}
	}'''
    authority_replacement = '''	if len(values) > 0 {
		authorityStarted := time.Now()
		_, authorityErr := s.authority.CreateAssets(values)
		traceCandidateCreate(map[string]any{"phase": "authority.create_batch", "duration_ns": time.Since(authorityStarted).Nanoseconds(), "input_count": len(values), "error": authorityErr != nil})
		if authorityErr != nil {
			for _, position := range positions {
				results[position].Error = authorityErr.Error()
			}
			return results, nil
		}
		lexicalStarted := time.Now()
		for _, value := range values {
			s.index.Upsert(value)
		}
		if s.evidencePlans != nil {
			s.evidencePlans.Reset()
		}
		traceCandidateCreate(map[string]any{"phase": "lexical.memory_index", "duration_ns": time.Since(lexicalStarted).Nanoseconds(), "input_count": len(values)})
	}'''
    return _replace_once(source, authority, authority_replacement)


def _instrument_collaboration(source: str) -> str:
    source = _replace_once(source, '"context"\n', '"context"\n\t"crypto/sha256"\n\t"encoding/json"\n')
    source = _replace_once(source, '"strings"\n', '"strings"\n\t"time"\n')
    embedding = '''	for offset := 0; offset < len(contents); {
		end := boundedEmbeddingBatchEnd(contents, offset)
		generated, embeddingErr := s.embedder.EmbedDocuments(ctx, contents[offset:end])
		if len(generated) != end-offset && embeddingErr == nil {'''
    embedding_replacement = '''	for offset := 0; offset < len(contents); {
		end := boundedEmbeddingBatchEnd(contents, offset)
		inputBytes := make([]int, end-offset)
		for index, content := range contents[offset:end] {
			inputBytes[index] = len([]byte(content))
		}
		embeddingStarted := time.Now()
		generated, embeddingErr := s.embedder.EmbedDocuments(ctx, contents[offset:end])
		encodedVectors, _ := json.Marshal(generated)
		vectorIdentities := make([]string, len(generated))
		for index, vector := range generated {
			encodedVector, _ := json.Marshal(vector)
			vectorIdentities[index] = fmt.Sprintf("%x", sha256.Sum256(encodedVector))
		}
		traceCandidateCreate(map[string]any{
			"phase": "embedding.documents", "duration_ns": time.Since(embeddingStarted).Nanoseconds(),
			"input_count": end-offset, "input_bytes": inputBytes, "output_count": len(generated),
			"vector_identity": fmt.Sprintf("%x", sha256.Sum256(encodedVectors)), "vector_identities": vectorIdentities, "error": embeddingErr != nil,
		})
		if len(generated) != end-offset && embeddingErr == nil {'''
    source = _replace_once(source, embedding, embedding_replacement)
    source = _replace_once(source, '\tstaged := derived.NewIndex(nil)\n', '\trecordStarted := time.Now()\n\tstaged := derived.NewIndex(nil)\n')
    source = _replace_once(source, '''	if len(records) == 0 {
		return states
	}
	s.graphMu.Lock()''', '''	traceCandidateCreate(map[string]any{"phase": "pending-records-and-staged-index", "duration_ns": time.Since(recordStarted).Nanoseconds(), "record_count": len(records)})
	if len(records) == 0 {
		return states
	}
	s.graphMu.Lock()''')
    source = _replace_once(source, '''	if err := s.derivedStore.PutBatch(records); err != nil {
		for _, position := range positionsByRecord {''', '''	derivedStarted := time.Now()
	derivedErr := s.derivedStore.PutBatch(records)
	traceCandidateCreate(map[string]any{"phase": "derived.put_batch", "duration_ns": time.Since(derivedStarted).Nanoseconds(), "record_count": len(records), "error": derivedErr != nil})
	if derivedErr != nil {
		for _, position := range positionsByRecord {''')
    source = _replace_once(source, '''			states[position] = OrganizationState{Status: "pending", Provider: "external-semantic-capability", Error: err.Error()}
		}
		return states
	}
	for _, record := range records {
		s.semantic.Upsert(record)
	}
	return states''', '''			states[position] = OrganizationState{Status: "pending", Provider: "external-semantic-capability", Error: derivedErr.Error()}
		}
		return states
	}
	commitStarted := time.Now()
	for _, record := range records {
		s.semantic.Upsert(record)
	}
	traceCandidateCreate(map[string]any{"phase": "semantic.memory_index", "duration_ns": time.Since(commitStarted).Nanoseconds(), "record_count": len(records)})
	return states''')
    return source


def _instrument_embedding(source: str) -> str:
    source = _replace_once(source, '"net/http"\n', '"net/http"\n\t"os"\n')
    marker = '''type Managed struct {
'''
    helper = '''var candidateEmbeddingTraceMu sync.Mutex

func traceCandidateEmbedding(event map[string]any) {
	event["schema"] = "ownward.candidate-create-trace/v1"
	encoded, err := json.Marshal(event)
	if err == nil {
		candidateEmbeddingTraceMu.Lock()
		_, _ = fmt.Fprintln(os.Stderr, string(encoded))
		candidateEmbeddingTraceMu.Unlock()
	}
}

type Managed struct {
'''
    source = _replace_once(source, marker, helper)
    source = _replace_once(source, '''	if err := m.ensureRunning(ctx); err != nil {
		return nil, err
	}
	body := map[string]any''', '''	ensureStarted := time.Now()
	if err := m.ensureRunning(ctx); err != nil {
		traceCandidateEmbedding(map[string]any{"phase": "embedding.ensure_running", "duration_ns": time.Since(ensureStarted).Nanoseconds(), "error": true})
		return nil, err
	}
	traceCandidateEmbedding(map[string]any{"phase": "embedding.ensure_running", "duration_ns": time.Since(ensureStarted).Nanoseconds(), "error": false})
	body := map[string]any''')
    return source


def _batch_independent_short_documents(source: str) -> str:
    before = '''func boundedEmbeddingBatchEnd(values []string, start int) int {
	end, total := start, 0
	for end < len(values) && end-start < 32 {
		size := len([]byte(values[end]))
		if end > start && total+size > semanticEmbeddingChunkBytes {
			break
		}
		total += size
		end++
	}
	return end
}'''
    after = '''func boundedEmbeddingBatchEnd(values []string, start int) int {
	end := start
	for end < len(values) && end-start < 32 {
		if len([]byte(values[end])) > semanticEmbeddingChunkBytes {
			if end == start {
				end++
			}
			break
		}
		end++
	}
	return end
}'''
    return _replace_once(source, before, after)


def _instrument_derived(source: str) -> str:
    marker = '''// PutBatch appends one bounded state batch with a single durability barrier.
'''
    helper = '''var candidateDerivedTraceMu sync.Mutex

func traceCandidateDerived(event map[string]any) {
	event["schema"] = "ownward.candidate-create-trace/v1"
	encoded, err := json.Marshal(event)
	if err == nil {
		candidateDerivedTraceMu.Lock()
		_, _ = fmt.Fprintln(os.Stderr, string(encoded))
		candidateDerivedTraceMu.Unlock()
	}
}

// PutBatch appends one bounded state batch with a single durability barrier.
'''
    source = _replace_once(source, marker, helper)
    target = '''	if durable {
		if err := s.file.Sync(); err != nil {
			_ = s.rollbackLocked(start)
			return fmt.Errorf("批量持久化派生状态: %w", err)
		}
	}'''
    replacement = '''	if durable {
		barrierStarted := time.Now()
		if err := s.file.Sync(); err != nil {
			traceCandidateDerived(map[string]any{"phase": "derived.durability_barrier", "duration_ns": time.Since(barrierStarted).Nanoseconds(), "error": true})
			_ = s.rollbackLocked(start)
			return fmt.Errorf("批量持久化派生状态: %w", err)
		}
		traceCandidateDerived(map[string]any{"phase": "derived.durability_barrier", "duration_ns": time.Since(barrierStarted).Nanoseconds(), "error": false})
	}'''
    return _replace_once(source, target, replacement)


def _replace_once(source: str, before: str, after: str) -> str:
    _require(source.count(before) == 1, "CreateBatch 观察器源码锚点漂移")
    return source.replace(before, after, 1)


def _verified(repository: Path, item: dict[str, Any], name: str) -> dict[str, Any]:
    path = repository / item["path"]
    _require(path.is_file() and evidence.file_sha256(path) == item["sha256"], f"{name}文件漂移")
    value = _load_json(path)
    if "identity" in item:
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
        raise validation.KernelIterationValidationError(f"无法读取 CreateBatch 证据 {path}: {error}") from error
    _require(isinstance(value, dict), f"CreateBatch 证据不是对象: {path}")
    return value


def _require(condition: bool, message: str) -> None:
    if not condition:
        raise validation.KernelIterationValidationError(message)
