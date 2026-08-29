from __future__ import annotations

import hashlib
import json
from pathlib import Path
import time
from typing import Any

import kernel_iteration_evidence as evidence
import kernel_iteration_stage4_runtime_implementation_probe as base
import kernel_iteration_stage4_vector_runtime as vector_runtime


CONTRACT_SCHEMA = "ownward.kernel-iteration-stage4-runtime-implementation-batch2-contract/v1"
RESULT_SCHEMA = "ownward.kernel-iteration-stage4-runtime-implementation-result/v1"


def probe(
    suite_root: Path,
    reference_bundle: Path,
    implementation_root: Path,
    archive_path: Path,
    implementation: str,
    output_path: Path,
    formal_state: Path,
) -> dict[str, Any]:
    suite_root = suite_root.resolve()
    repository = suite_root.parents[2]
    reference_bundle = reference_bundle.resolve()
    implementation_root = implementation_root.resolve()
    archive_path = archive_path.resolve()
    output_path = output_path.resolve()
    formal_state = formal_state.resolve()
    base._require(output_path.is_relative_to(repository / ".tmp"), "运行实现探针只能写入非正式 .tmp 边界")
    base._require(not output_path.exists(), "运行实现探针结果已存在；禁止选择性覆盖")

    contract_path = suite_root / "iteration" / "v2" / "stage4-retrieval-latency-runtime-implementation-batch2-contract.json"
    contract = base._load_json(contract_path)
    base._require(contract.get("schema") == CONTRACT_SCHEMA, "运行实现探针合同 schema 无效")
    base._require(contract.get("frozen_before_measurement") is True, "运行实现探针合同未在结果前冻结")
    implementation_contract = contract.get("implementations", {}).get(implementation)
    base._require(isinstance(implementation_contract, dict), "运行实现没有冻结合同")
    base._require(
        evidence.file_sha256(archive_path) == implementation_contract.get("official_archive_sha256"),
        "运行实现官方制品摘要漂移",
    )

    reference_manifest_path = reference_bundle / "manifest.json"
    reference_manifest = base._load_json(reference_manifest_path)
    reference = contract["reference"]
    base._require(evidence.file_sha256(reference_manifest_path) == reference["embedding_manifest_sha256"], "参考向量清单漂移")
    model = reference_bundle / str(reference_manifest["model"]["path"])
    reference_runtime = reference_bundle / str(reference_manifest["runtime"]["entry"])
    base._require(evidence.file_sha256(model) == reference["model_sha256"], "参考模型漂移")
    base._require(evidence.canonical_sha256(list(vector_runtime.QUERIES)) == reference["queries_sha256"], "隔离查询集合漂移")
    runtime = implementation_root / "llama-server.exe"
    base._require(runtime.is_file(), "运行实现缺少 llama-server.exe")
    state_before = evidence.file_sha256(formal_state)
    base._require(state_before == contract["formal_state_sha256"], "运行实现探针前正式 state 漂移")

    measurement = contract["measurement"]
    with vector_runtime._Server(reference_runtime, model, int(measurement["threads"]), 1) as server:
        reference_vectors = {query: server.vector(query) for query in vector_runtime.QUERIES}

    measured: list[float] = []
    candidate_vectors: dict[str, list[float]] = {}
    peak_working_set_bytes = 0
    runtime_log = ""
    server: base._ImplementationServer | None = None
    try:
        with base._ImplementationServer(
            runtime,
            model,
            int(measurement["threads"]),
            int(measurement["parallel"]),
            ["--device", str(implementation_contract["device"]), "--n-gpu-layers", str(implementation_contract["gpu_layers"])],
        ) as server:
            for index in range(int(measurement["warmups"])):
                server.vector(vector_runtime.QUERIES[index % len(vector_runtime.QUERIES)])
            for index in range(int(measurement["repetitions"])):
                query = vector_runtime.QUERIES[index % len(vector_runtime.QUERIES)]
                started = time.perf_counter()
                candidate_vectors[query] = server.vector(query)
                measured.append((time.perf_counter() - started) * 1000)
            peak_working_set_bytes = server.peak_working_set_bytes
        runtime_log = server.runtime_log
    except Exception as error:
        if server is not None:
            runtime_log = server.runtime_log
            peak_working_set_bytes = server.peak_working_set_bytes
        state_after = evidence.file_sha256(formal_state)
        base._require(state_after == state_before, "运行实现失败探针改写了正式 state")
        content = {
            "schema": RESULT_SCHEMA,
            "formal": False,
            "formal_state_written": False,
            "contract_sha256": evidence.file_sha256(contract_path),
            "controller_sha256": evidence.file_sha256(Path(__file__).resolve()),
            "base_controller_sha256": evidence.file_sha256(Path(base.__file__).resolve()),
            "implementation": implementation,
            "official_archive_sha256": evidence.file_sha256(archive_path),
            "runtime_sha256": evidence.file_sha256(runtime),
            "reference_runtime_sha256": evidence.file_sha256(reference_runtime),
            "model_sha256": evidence.file_sha256(model),
            "space_identity": reference["space_id"],
            "queries_sha256": reference["queries_sha256"],
            "measurement": measurement,
            "failure": {
                "phase": "warmup-or-isolated-query",
                "type": type(error).__name__,
                "message": str(error),
            },
            "peak_working_set_bytes": peak_working_set_bytes,
            "runtime_log_sha256": hashlib.sha256(runtime_log.encode("utf-8")).hexdigest(),
            "passed": False,
            "formal_state_sha256_before": state_before,
            "formal_state_sha256_after": state_after,
        }
        value = {**content, "identity": evidence.canonical_sha256(content)}
        evidence.atomic_json(output_path, value)
        return {**value, "path": str(output_path)}

    maximum_drift = max(
        abs(actual - expected)
        for query in vector_runtime.QUERIES
        for actual, expected in zip(candidate_vectors[query], reference_vectors[query])
    )
    summary = base._summary(measured)
    gates = contract["gates"]
    mean_passed = float(summary["mean_ms"]) < float(gates["retrieval_mean_ms_maximum_exclusive"])
    p95_passed = float(summary["p95_ms"]) <= float(gates["exact_query_p95_ms_maximum"])
    drift_passed = maximum_drift <= float(gates["maximum_vector_component_drift"])
    backend_proven = str(implementation_contract["backend_log_marker"]).lower() in runtime_log.lower()
    state_after = evidence.file_sha256(formal_state)
    base._require(state_after == state_before, "运行实现探针改写了正式 state")

    content = {
        "schema": RESULT_SCHEMA,
        "formal": False,
        "formal_state_written": False,
        "contract_sha256": evidence.file_sha256(contract_path),
        "controller_sha256": evidence.file_sha256(Path(__file__).resolve()),
        "base_controller_sha256": evidence.file_sha256(Path(base.__file__).resolve()),
        "implementation": implementation,
        "official_archive_sha256": evidence.file_sha256(archive_path),
        "runtime_sha256": evidence.file_sha256(runtime),
        "reference_runtime_sha256": evidence.file_sha256(reference_runtime),
        "model_sha256": evidence.file_sha256(model),
        "space_identity": reference["space_id"],
        "queries_sha256": reference["queries_sha256"],
        "measurement": measurement,
        "summary": summary,
        "maximum_vector_component_drift": maximum_drift,
        "vector_sha256": {query: base._vector_sha256(candidate_vectors[query]) for query in vector_runtime.QUERIES},
        "reference_vector_sha256": {query: base._vector_sha256(reference_vectors[query]) for query in vector_runtime.QUERIES},
        "peak_working_set_bytes": peak_working_set_bytes,
        "runtime_log_sha256": hashlib.sha256(runtime_log.encode("utf-8")).hexdigest(),
        "backend_proven": backend_proven,
        "gates": {
            "mean_passed": mean_passed,
            "p95_passed": p95_passed,
            "drift_passed": drift_passed,
            "backend_proven": backend_proven,
        },
        "passed": mean_passed and p95_passed and drift_passed and backend_proven,
        "formal_state_sha256_before": state_before,
        "formal_state_sha256_after": state_after,
    }
    value = {**content, "identity": evidence.canonical_sha256(content)}
    evidence.atomic_json(output_path, value)
    return {**value, "path": str(output_path)}
